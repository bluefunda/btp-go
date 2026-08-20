package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluefunda/btp-go/ingest"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ErrQueueFull is returned by [Publisher.Submit] when the submission queue
// has no room. It is the signal to shed load: the HTTP layer turns it into
// 503 with a Retry-After, which tells SAP to back off instead of driving the
// gateway further into the ground.
var ErrQueueFull = errors.New("broker: publish queue full")

// ErrPublisherClosed is returned by [Publisher.Submit] after shutdown began.
var ErrPublisherClosed = errors.New("broker: publisher closed")

// Submission is one validated event on its way to JetStream.
type Submission struct {
	// Event is the decoded, validated envelope.
	Event ingest.Event

	// Raw is the request body exactly as SAP sent it. It is what gets
	// published: re-marshalling would drop unknown fields and could
	// reformat the SAP decimals inside Payload.
	Raw []byte

	// TraceID correlates the HTTP request with the resulting stream message.
	TraceID string

	// Subject is the resolved JetStream subject.
	Subject string

	// DedupeKey is the idempotency reservation held for this event, handed
	// back to the failure hook if the publish never lands.
	DedupeKey string

	// ReceivedAt is when the gateway accepted the request.
	ReceivedAt time.Time
}

// Ack is a JetStream publisher confirm.
type Ack struct {
	Stream   string
	Sequence uint64

	// Duplicate is true when JetStream recognised the Nats-Msg-Id inside
	// the stream's duplicate window and stored nothing. The event is safe
	// either way; it just means this publish was a repeat.
	Duplicate bool
}

// Result is the outcome of one submission, delivered to the channel returned
// by [Publisher.Submit].
type Result struct {
	Ack *Ack
	Err error
}

// FailureHook runs when a submission is abandoned after exhausting retries.
//
// The gateway wires this to release the idempotency reservation. Without it,
// an event that failed to publish would still be remembered as "seen", and
// every SAP retry of that document would be rejected as a duplicate until
// the TTL expired — a silent, permanent loss.
type FailureHook func(ctx context.Context, s Submission, err error)

// PublisherOptions configures a [Publisher].
type PublisherOptions struct {
	// QueueSize is the depth of the submission queue and therefore the
	// number of events that can be in flight before /ingest starts
	// shedding load.
	QueueSize int

	// Concurrency is the number of goroutines draining the queue.
	Concurrency int

	// Timeout bounds a single publish-and-await-ack attempt.
	Timeout time.Duration

	// Retries is how many additional attempts a failed publish gets.
	Retries int

	// RetryWait is the base delay between attempts; it doubles each time.
	RetryWait time.Duration

	// OnFailure is invoked for abandoned submissions. Optional but
	// strongly recommended.
	OnFailure FailureHook

	// SourceSystem, when set, is stamped on every published message as
	// HeaderSourceSystem — a free-form label for whatever upstream system
	// is calling /ingest (e.g. "sap-s4hana", "salesforce", "internal-crm").
	// Left empty, the header is omitted; the gateway itself has no opinion
	// on what the upstream is.
	SourceSystem string

	// Logger is required.
	Logger *slog.Logger
}

// Publisher moves validated events into JetStream off the request path.
//
// Submit hands the event to a bounded queue and returns immediately, so the
// ABAP caller gets its 202 without waiting on a network round trip to NATS.
// The queue is bounded on purpose: an unbounded one converts a NATS outage
// into an out-of-memory kill, and takes the buffered events with it.
type Publisher struct {
	js   MsgPublisher
	opts PublisherOptions
	log  *slog.Logger

	queue chan job
	wg    sync.WaitGroup

	mu     sync.RWMutex
	closed bool

	published atomic.Uint64
	failed    atomic.Uint64
	duplicate atomic.Uint64
	inflight  atomic.Int64
}

type job struct {
	sub    Submission
	result chan Result
}

// MsgPublisher is the slice of the JetStream API that publishing needs.
// [jetstream.JetStream] satisfies it. Narrowing it here keeps the publisher
// testable without a running broker.
type MsgPublisher interface {
	PublishMsg(ctx context.Context, msg *nats.Msg, opts ...jetstream.PublishOpt) (*jetstream.PubAck, error)
}

// NewPublisher creates a Publisher and starts its worker goroutines.
// Call [Publisher.Drain] to shut it down.
func NewPublisher(js MsgPublisher, opts PublisherOptions) *Publisher {
	if opts.QueueSize < 1 {
		opts.QueueSize = 1024
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 4
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.RetryWait <= 0 {
		opts.RetryWait = 200 * time.Millisecond
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	p := &Publisher{
		js:    js,
		opts:  opts,
		log:   opts.Logger,
		queue: make(chan job, opts.QueueSize),
	}
	for range opts.Concurrency {
		p.wg.Go(p.loop)
	}
	return p
}

// Submit enqueues a submission without blocking.
//
// The returned channel receives exactly one [Result] once the publish
// settles. Callers that do not care may discard it; the channel is buffered
// so an abandoned reader never stalls a worker.
func (p *Publisher) Submit(s Submission) (<-chan Result, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return nil, ErrPublisherClosed
	}

	res := make(chan Result, 1)
	select {
	case p.queue <- job{sub: s, result: res}:
		return res, nil
	default:
		return nil, ErrQueueFull
	}
}

// SubmitAndWait publishes and waits for the JetStream ack. It is the
// durable-but-slower path behind PUBLISH_SYNC, for deployments that would
// rather make SAP wait than risk losing an in-flight event to a pod restart.
func (p *Publisher) SubmitAndWait(ctx context.Context, s Submission) (*Ack, error) {
	res, err := p.Submit(s)
	if err != nil {
		return nil, err
	}
	select {
	case r := <-res:
		return r.Ack, r.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Publisher) loop() {
	for j := range p.queue {
		p.inflight.Add(1)
		ack, err := p.publish(j.sub)
		p.inflight.Add(-1)

		switch {
		case err != nil:
			p.failed.Add(1)
			p.log.Error("publish abandoned",
				"trace_id", j.sub.TraceID,
				"subject", j.sub.Subject,
				"business_object", j.sub.Event.BusinessObject,
				"key", j.sub.Event.Key,
				"error", err.Error(),
			)
			if p.opts.OnFailure != nil {
				// A fresh context: the request context is long gone, and
				// releasing the reservation matters more than the deadline
				// of the request that created it.
				hookCtx, cancel := context.WithTimeout(context.Background(), p.opts.Timeout)
				p.opts.OnFailure(hookCtx, j.sub, err)
				cancel()
			}
		case ack.Duplicate:
			p.duplicate.Add(1)
			p.published.Add(1)
			p.log.Warn("publish deduplicated by jetstream",
				"trace_id", j.sub.TraceID,
				"subject", j.sub.Subject,
				"stream", ack.Stream,
				"sequence", ack.Sequence,
			)
		default:
			p.published.Add(1)
			p.log.Debug("published",
				"trace_id", j.sub.TraceID,
				"subject", j.sub.Subject,
				"stream", ack.Stream,
				"sequence", ack.Sequence,
				"queue_latency_ms", time.Since(j.sub.ReceivedAt).Milliseconds(),
			)
		}

		j.result <- Result{Ack: ack, Err: err}
		close(j.result)
	}
}

// publish sends one message, retrying transient failures with exponential
// backoff. Nats-Msg-Id makes the retries safe: JetStream collapses repeats
// inside the stream's duplicate window, so an ack lost on the wire cannot
// produce two stored copies.
func (p *Publisher) publish(s Submission) (*Ack, error) {
	msg := &nats.Msg{
		Subject: s.Subject,
		Data:    s.Raw,
		Header:  nats.Header{},
	}
	msg.Header.Set(HeaderTraceID, s.TraceID)
	msg.Header.Set(HeaderBusinessObject, s.Event.BusinessObject)
	msg.Header.Set(HeaderOperation, s.Event.Operation)
	msg.Header.Set(HeaderBusinessKey, s.Event.Key)
	msg.Header.Set(HeaderEventTime, s.Event.Timestamp.UTC().Format(time.RFC3339Nano))
	msg.Header.Set(HeaderReceivedAt, s.ReceivedAt.UTC().Format(time.RFC3339Nano))
	if p.opts.SourceSystem != "" {
		msg.Header.Set(HeaderSourceSystem, p.opts.SourceSystem)
	}

	if len(msg.Data) == 0 {
		raw, err := json.Marshal(s.Event)
		if err != nil {
			return nil, fmt.Errorf("broker: marshal event: %w", err)
		}
		msg.Data = raw
	}

	var lastErr error
	wait := p.opts.RetryWait

	for attempt := 0; attempt <= p.opts.Retries; attempt++ {
		if attempt > 0 {
			time.Sleep(wait)
			if wait < 5*time.Second {
				wait *= 2
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), p.opts.Timeout)
		ack, err := p.js.PublishMsg(ctx, msg, jetstream.WithMsgID(s.Event.MsgID()))
		cancel()

		if err == nil {
			return &Ack{Stream: ack.Stream, Sequence: ack.Sequence, Duplicate: ack.Duplicate}, nil
		}
		lastErr = err

		if !isRetryablePublishError(err) {
			return nil, fmt.Errorf("broker: publish to %q: %w", s.Subject, err)
		}
		p.log.Warn("publish attempt failed, retrying",
			"trace_id", s.TraceID,
			"subject", s.Subject,
			"attempt", attempt+1,
			"max_attempts", p.opts.Retries+1,
			"error", err.Error(),
		)
	}
	return nil, fmt.Errorf("broker: publish to %q after %d attempts: %w",
		s.Subject, p.opts.Retries+1, lastErr)
}

// isRetryablePublishError separates "NATS is having a moment" from "this
// message will never be accepted". Retrying the latter just delays the
// inevitable while holding a queue slot.
func isRetryablePublishError(err error) bool {
	switch {
	case errors.Is(err, jetstream.ErrInvalidSubject),
		errors.Is(err, nats.ErrMaxPayload),
		errors.Is(err, jetstream.ErrNoStreamResponse):
		// ErrNoStreamResponse means no stream is bound to the subject.
		// That is a topology problem, not a transient one, and retrying
		// only delays the operator finding out.
		return false
	}

	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		// 4xx from the JetStream API is a rejected message; 5xx and
		// transport errors are worth another attempt.
		return apiErr.Code >= 500
	}
	return true
}

// Drain stops accepting submissions, waits for the queue to empty, and
// returns.
//
// It is called after the HTTP listener has stopped, so the queue is a closed
// set by then. If ctx expires first, Drain returns its error and the
// remaining events are reported as lost — which is the honest outcome, and
// why the shutdown timeout should exceed the publish timeout.
func (p *Publisher) Drain(ctx context.Context) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.queue)
	p.mu.Unlock()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("broker: publisher drain timed out with %d queued and %d in flight: %w",
			len(p.queue), p.inflight.Load(), ctx.Err())
	}
}

// Stats is a point-in-time view of publisher activity, surfaced on /readyz
// and in the shutdown log line.
type Stats struct {
	Queued        int    `json:"queued"`
	QueueCapacity int    `json:"queue_capacity"`
	InFlight      int64  `json:"in_flight"`
	Published     uint64 `json:"published"`
	Duplicate     uint64 `json:"duplicate"`
	Failed        uint64 `json:"failed"`
}

// Stats returns current publisher counters.
func (p *Publisher) Stats() Stats {
	return Stats{
		Queued:        len(p.queue),
		QueueCapacity: cap(p.queue),
		InFlight:      p.inflight.Load(),
		Published:     p.published.Load(),
		Duplicate:     p.duplicate.Load(),
		Failed:        p.failed.Load(),
	}
}

// Saturated reports whether the queue is at or above 90% full. /readyz uses
// it to drop the pod out of the load-balancer rotation before it has to
// start returning 503s.
func (p *Publisher) Saturated() bool {
	return len(p.queue)*10 >= cap(p.queue)*9
}
