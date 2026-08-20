package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bluefunda/btp-go/ingest"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeJS records published messages and replays a scripted sequence of
// results, so publish behaviour can be tested without a broker.
type fakeJS struct {
	mu       sync.Mutex
	msgs     []*nats.Msg
	results  []publishResult
	fallback publishResult
	calls    atomic.Int64
}

type publishResult struct {
	ack *jetstream.PubAck
	err error
}

func (f *fakeJS) PublishMsg(_ context.Context, msg *nats.Msg, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	n := f.calls.Add(1)

	f.mu.Lock()
	defer f.mu.Unlock()

	// Copy: the caller reuses the message across retries.
	clone := *msg
	clone.Header = nats.Header{}
	for k, v := range msg.Header {
		clone.Header[k] = append([]string(nil), v...)
	}
	f.msgs = append(f.msgs, &clone)

	if int(n) <= len(f.results) {
		r := f.results[n-1]
		return r.ack, r.err
	}
	if f.fallback.ack == nil && f.fallback.err == nil {
		return &jetstream.PubAck{Stream: "TEST", Sequence: uint64(n)}, nil
	}
	return f.fallback.ack, f.fallback.err
}

func (f *fakeJS) published() []*nats.Msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*nats.Msg(nil), f.msgs...)
}

func testSubmission() Submission {
	ev := ingest.Event{
		BusinessObject: "BILLING_DOC",
		Operation:      "CREATE",
		Key:            "SAP-ISU-1",
		Timestamp:      time.Date(2023, 10, 27, 10, 0, 0, 0, time.UTC),
		Payload:        json.RawMessage(`{"amount":"1.00"}`),
	}
	raw, _ := json.Marshal(ev)
	return Submission{
		Event:      ev,
		Raw:        raw,
		TraceID:    "trace-1",
		Subject:    "events.billing_doc.create",
		DedupeKey:  ev.DedupeKey(),
		ReceivedAt: time.Now(),
	}
}

func TestPublisherPublishesWithTracingHeaders(t *testing.T) {
	js := &fakeJS{}
	p := NewPublisher(js, PublisherOptions{Concurrency: 1, SourceSystem: "sap-s4hana", Logger: testLogger()})

	res, err := p.Submit(testSubmission())
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	got := <-res
	if got.Err != nil {
		t.Fatalf("publish failed: %v", got.Err)
	}
	if got.Ack.Sequence != 1 {
		t.Errorf("Sequence = %d, want 1", got.Ack.Sequence)
	}

	msgs := js.published()
	if len(msgs) != 1 {
		t.Fatalf("published %d messages, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.Subject != "events.billing_doc.create" {
		t.Errorf("Subject = %q", msg.Subject)
	}
	for header, want := range map[string]string{
		HeaderTraceID:        "trace-1",
		HeaderBusinessObject: "BILLING_DOC",
		HeaderOperation:      "CREATE",
		HeaderBusinessKey:    "SAP-ISU-1",
		HeaderSourceSystem:   "sap-s4hana",
	} {
		if got := msg.Header.Get(header); got != want {
			t.Errorf("header %s = %q, want %q", header, got, want)
		}
	}

	if err := p.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if stats := p.Stats(); stats.Published != 1 || stats.Failed != 0 {
		t.Errorf("Stats() = %+v, want 1 published and 0 failed", stats)
	}
}

// The gateway has no opinion on what upstream is calling it; the header is
// only sent when the deployment configures one.
func TestPublisherOmitsSourceSystemHeaderWhenUnset(t *testing.T) {
	js := &fakeJS{}
	p := NewPublisher(js, PublisherOptions{Concurrency: 1, Logger: testLogger()})
	t.Cleanup(func() { _ = p.Drain(context.Background()) })

	res, err := p.Submit(testSubmission())
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if got := <-res; got.Err != nil {
		t.Fatalf("publish failed: %v", got.Err)
	}

	if got := js.published()[0].Header.Get(HeaderSourceSystem); got != "" {
		t.Errorf("header %s = %q, want it omitted", HeaderSourceSystem, got)
	}
}

// The body must go out byte-for-byte as SAP sent it, unknown fields and all.
func TestPublisherPublishesRawBodyVerbatim(t *testing.T) {
	js := &fakeJS{}
	p := NewPublisher(js, PublisherOptions{Concurrency: 1, Logger: testLogger()})
	t.Cleanup(func() { _ = p.Drain(context.Background()) })

	sub := testSubmission()
	sub.Raw = []byte(`{"business_object":"BILLING_DOC","sap_only_field":"keep me"}`)

	res, err := p.Submit(sub)
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if got := <-res; got.Err != nil {
		t.Fatalf("publish failed: %v", got.Err)
	}

	if got, want := string(js.published()[0].Data), string(sub.Raw); got != want {
		t.Errorf("published body = %s, want %s", got, want)
	}
}

func TestPublisherRetriesTransientFailures(t *testing.T) {
	js := &fakeJS{results: []publishResult{
		{err: errors.New("connection reset")},
		{err: errors.New("connection reset")},
		{ack: &jetstream.PubAck{Stream: "TEST", Sequence: 7}},
	}}
	p := NewPublisher(js, PublisherOptions{
		Concurrency: 1,
		Retries:     3,
		RetryWait:   time.Millisecond,
		Logger:      testLogger(),
	})
	t.Cleanup(func() { _ = p.Drain(context.Background()) })

	res, err := p.Submit(testSubmission())
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	got := <-res
	if got.Err != nil {
		t.Fatalf("publish failed after retries: %v", got.Err)
	}
	if got.Ack.Sequence != 7 {
		t.Errorf("Sequence = %d, want 7", got.Ack.Sequence)
	}
	if n := js.calls.Load(); n != 3 {
		t.Errorf("made %d attempts, want 3", n)
	}
}

// A subject with no stream bound to it is a topology error. Retrying it just
// burns the queue slot and delays the operator finding out.
func TestPublisherDoesNotRetryPermanentFailures(t *testing.T) {
	js := &fakeJS{fallback: publishResult{err: jetstream.ErrNoStreamResponse}}
	p := NewPublisher(js, PublisherOptions{
		Concurrency: 1,
		Retries:     5,
		RetryWait:   time.Millisecond,
		Logger:      testLogger(),
	})
	t.Cleanup(func() { _ = p.Drain(context.Background()) })

	res, err := p.Submit(testSubmission())
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	if got := <-res; got.Err == nil {
		t.Fatal("publish succeeded, want an error")
	}
	if n := js.calls.Load(); n != 1 {
		t.Errorf("made %d attempts, want 1", n)
	}
}

// Losing the publish must hand the idempotency claim back, or SAP's retry of
// the same document would be rejected as a duplicate and lost outright.
func TestPublisherCallsFailureHookWithDedupeKey(t *testing.T) {
	js := &fakeJS{fallback: publishResult{err: jetstream.ErrNoStreamResponse}}

	var (
		mu       sync.Mutex
		released []string
		done     = make(chan struct{})
	)
	p := NewPublisher(js, PublisherOptions{
		Concurrency: 1,
		Logger:      testLogger(),
		OnFailure: func(_ context.Context, s Submission, _ error) {
			mu.Lock()
			released = append(released, s.DedupeKey)
			mu.Unlock()
			close(done)
		},
	})
	t.Cleanup(func() { _ = p.Drain(context.Background()) })

	sub := testSubmission()
	if _, err := p.Submit(sub); err != nil {
		t.Fatalf("Submit() = %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("failure hook was never called")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(released) != 1 || released[0] != sub.DedupeKey {
		t.Errorf("released = %v, want [%s]", released, sub.DedupeKey)
	}
}

// Backpressure: a full queue must be refused, not grown.
func TestPublisherSubmitReturnsErrQueueFullWhenSaturated(t *testing.T) {
	block := make(chan struct{})
	js := &blockingJS{release: block}
	p := NewPublisher(js, PublisherOptions{QueueSize: 2, Concurrency: 1, Logger: testLogger()})

	// One submission is picked up by the single worker and blocks there;
	// two more fill the queue.
	for i := range 3 {
		if _, err := p.Submit(testSubmission()); err != nil {
			t.Fatalf("Submit() #%d = %v, want nil", i+1, err)
		}
	}
	waitFor(t, func() bool { return p.Stats().Queued == 2 })

	if _, err := p.Submit(testSubmission()); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Submit() on a full queue = %v, want ErrQueueFull", err)
	}
	if !p.Saturated() {
		t.Error("Saturated() = false, want true")
	}

	close(block)
	if err := p.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
}

func TestPublisherSubmitAfterDrain(t *testing.T) {
	p := NewPublisher(&fakeJS{}, PublisherOptions{Concurrency: 1, Logger: testLogger()})
	if err := p.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if _, err := p.Submit(testSubmission()); !errors.Is(err, ErrPublisherClosed) {
		t.Fatalf("Submit() after Drain = %v, want ErrPublisherClosed", err)
	}
	if err := p.Drain(t.Context()); err != nil {
		t.Fatalf("second Drain() = %v, want nil", err)
	}
}

// Shutdown must flush what is already queued, not drop it.
func TestPublisherDrainFlushesQueuedSubmissions(t *testing.T) {
	js := &fakeJS{}
	p := NewPublisher(js, PublisherOptions{QueueSize: 64, Concurrency: 2, Logger: testLogger()})

	const n = 50
	for range n {
		if _, err := p.Submit(testSubmission()); err != nil {
			t.Fatalf("Submit() = %v", err)
		}
	}
	if err := p.Drain(t.Context()); err != nil {
		t.Fatalf("Drain() = %v", err)
	}
	if got := len(js.published()); got != n {
		t.Errorf("published %d messages, want %d", got, n)
	}
	if stats := p.Stats(); stats.Published != n {
		t.Errorf("Stats().Published = %d, want %d", stats.Published, n)
	}
}

func TestPublisherReportsJetStreamDeduplication(t *testing.T) {
	js := &fakeJS{fallback: publishResult{
		ack: &jetstream.PubAck{Stream: "TEST", Sequence: 3, Duplicate: true},
	}}
	p := NewPublisher(js, PublisherOptions{Concurrency: 1, Logger: testLogger()})
	t.Cleanup(func() { _ = p.Drain(context.Background()) })

	res, err := p.Submit(testSubmission())
	if err != nil {
		t.Fatalf("Submit() = %v", err)
	}
	got := <-res
	if got.Err != nil {
		t.Fatalf("publish failed: %v", got.Err)
	}
	if !got.Ack.Duplicate {
		t.Error("Ack.Duplicate = false, want true")
	}
	waitFor(t, func() bool { return p.Stats().Duplicate == 1 })
}

func TestSubmitAndWait(t *testing.T) {
	p := NewPublisher(&fakeJS{}, PublisherOptions{Concurrency: 1, Logger: testLogger()})
	t.Cleanup(func() { _ = p.Drain(context.Background()) })

	ack, err := p.SubmitAndWait(t.Context(), testSubmission())
	if err != nil {
		t.Fatalf("SubmitAndWait() = %v", err)
	}
	if ack.Stream != "TEST" {
		t.Errorf("Stream = %q, want %q", ack.Stream, "TEST")
	}
}

func TestIsRetryablePublishError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"transport error", errors.New("write: broken pipe"), true},
		{"no stream bound", jetstream.ErrNoStreamResponse, false},
		{"invalid subject", jetstream.ErrInvalidSubject, false},
		{"payload too large", nats.ErrMaxPayload, false},
		{"api 4xx", &jetstream.APIError{Code: 400, Description: "bad request"}, false},
		{"api 5xx", &jetstream.APIError{Code: 503, Description: "no responders"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRetryablePublishError(tt.err); got != tt.want {
				t.Errorf("isRetryablePublishError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// blockingJS holds every publish until release is closed, so the queue can
// be filled deterministically.
type blockingJS struct {
	release chan struct{}
}

func (b *blockingJS) PublishMsg(_ context.Context, _ *nats.Msg, _ ...jetstream.PublishOpt) (*jetstream.PubAck, error) {
	<-b.release
	return &jetstream.PubAck{Stream: "TEST", Sequence: 1}, nil
}

// waitFor polls cond until it holds or the test times out. Publisher
// counters are updated by background goroutines, so a bare assertion would
// race.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met within 5s")
}
