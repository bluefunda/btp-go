// Package api is the inbound HTTP surface of the gateway: the /ingest
// endpoint ABAP posts to, the authentication in front of it, and the
// liveness and readiness probes the platform polls.
//
// The router is the standard library's http.ServeMux, using the method
// patterns added in Go 1.22 ("POST /ingest"). A third-party router would buy
// nothing here — there are three routes and no path parameters.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/bluefunda/btp-go/ingest"
	"github.com/bluefunda/btp-go/ingest/broker"
	"github.com/bluefunda/btp-go/ingest/idem"
)

// EventPublisher is the publishing half of the gateway as the HTTP layer
// sees it. [broker.Publisher] satisfies it.
type EventPublisher interface {
	// Submit enqueues an event, returning broker.ErrQueueFull when there
	// is no room.
	Submit(s broker.Submission) (<-chan broker.Result, error)

	// Saturated reports whether the queue is nearly full, which /readyz
	// uses to shed traffic before Submit starts failing.
	Saturated() bool
}

// Idempotency is the duplicate-suppression half. [idem.Store] satisfies it.
type Idempotency interface {
	// Reserve atomically claims key, returning idem.ErrDuplicate if it is
	// already held.
	Reserve(ctx context.Context, key string, meta []byte) error

	// Release drops a reservation.
	Release(ctx context.Context, key string) error

	// Kind names the backing store, for /readyz output.
	Kind() string
}

// Options configures the HTTP handler.
type Options struct {
	// Publisher and Idempotency are required.
	Publisher   EventPublisher
	Idempotency Idempotency

	// SubjectPrefix is the root of the published subject tree.
	SubjectPrefix string

	// MaxBodyBytes caps a single request body.
	MaxBodyBytes int64

	// IdempotencyTimeout bounds the reservation call. It sits on the
	// request path, so it should be short: a slow KV is a reason to answer
	// 503, not to hold an ABAP work process open.
	IdempotencyTimeout time.Duration

	// PublishSync makes /ingest wait for the JetStream ack before
	// responding, and PublishTimeout bounds that wait.
	PublishSync    bool
	PublishTimeout time.Duration

	// DuplicateStatus is the status code returned for a suppressed
	// duplicate: 409 (accurate), or 200/202 (kinder to ABAP callers that
	// retry on any non-2xx).
	DuplicateStatus int

	// Auth configures request authentication.
	Auth AuthOptions

	// Checks are the readiness probes.
	Checks []Check

	// Logger is required.
	Logger *slog.Logger

	// Now is the clock, overridable in tests.
	Now func() time.Time
}

func (o *Options) setDefaults() error {
	if o.Publisher == nil {
		return errors.New("api: Publisher is required")
	}
	if o.Idempotency == nil {
		return errors.New("api: Idempotency is required")
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 1 << 20
	}
	if o.IdempotencyTimeout <= 0 {
		o.IdempotencyTimeout = 3 * time.Second
	}
	if o.PublishTimeout <= 0 {
		o.PublishTimeout = 10 * time.Second
	}
	if o.DuplicateStatus == 0 {
		o.DuplicateStatus = http.StatusConflict
	}
	return nil
}

// NewHandler builds the gateway's HTTP handler.
//
// Health probes are deliberately outside the authentication chain: a
// kubelet cannot present an API key, and a probe that fails closed takes the
// pod down for the wrong reason.
func NewHandler(opts Options) (http.Handler, error) {
	if err := opts.setDefaults(); err != nil {
		return nil, err
	}
	auth, err := newAuthenticator(opts.Auth)
	if err != nil {
		return nil, err
	}

	h := &handler{opts: opts, log: opts.Logger}

	mux := http.NewServeMux()
	mux.Handle("POST /ingest", auth.middleware(http.HandlerFunc(h.ingest)))
	mux.Handle("GET /healthz", http.HandlerFunc(h.healthz))
	mux.Handle("GET /readyz", http.HandlerFunc(h.readyz))

	// The catch-all below matches every path, which would otherwise
	// suppress ServeMux's own 405 for a known path with the wrong method.
	// These say so explicitly.
	mux.Handle("/ingest", methodNotAllowed(h, http.MethodPost))
	mux.Handle("/healthz", methodNotAllowed(h, http.MethodGet))
	mux.Handle("/readyz", methodNotAllowed(h, http.MethodGet))
	mux.Handle("/", http.HandlerFunc(h.notFound))

	return chain(mux,
		recoverer(opts.Logger),
		traceID,
		requestLogger(opts.Logger),
	), nil
}

type handler struct {
	opts Options
	log  *slog.Logger
}

// AcceptResponse is the body returned for an accepted event.
type AcceptResponse struct {
	// TraceID is the correlation identifier. The caller should log it: it
	// is the single value that ties the HTTP request to the stream message
	// it produced.
	TraceID string `json:"trace_id"`

	// Status is "accepted" (queued), "stored" (durably acked, sync mode),
	// or "duplicate".
	Status string `json:"status"`

	// Subject is where the event was published.
	Subject string `json:"subject"`

	// Stream and Sequence are set only in sync mode, once JetStream has
	// confirmed the write.
	Stream   string `json:"stream,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`

	// ReceivedAt is when the gateway accepted the request.
	ReceivedAt time.Time `json:"received_at"`
}

// Ingest response statuses.
const (
	StatusAccepted  = "accepted"
	StatusStored    = "stored"
	StatusDuplicate = "duplicate"
)

// ErrorResponse is the body returned for any non-2xx answer.
type ErrorResponse struct {
	TraceID string                   `json:"trace_id"`
	Code    string                   `json:"code"`
	Error   string                   `json:"error"`
	Details []ingest.ValidationError `json:"details,omitempty"`
}

// Machine-readable values of ErrorResponse.Code, stable across releases so
// ABAP can branch on them.
const (
	CodeInvalidJSON     = "invalid_json"
	CodeInvalidEvent    = "invalid_event"
	CodeUnsupported     = "unsupported_media_type"
	CodePayloadTooLarge = "payload_too_large"
	CodeUnauthorized    = "unauthorized"
	CodeDuplicate       = "duplicate_event"
	CodeBackpressure    = "backpressure"
	CodeInternal        = "internal_error"
	CodeNotFound        = "not_found"
	CodeUpstream        = "upstream_unavailable"
)

func (h *handler) ingest(w http.ResponseWriter, r *http.Request) {
	trace := TraceIDFrom(r.Context())
	receivedAt := h.opts.Now()

	if ct := r.Header.Get("Content-Type"); ct != "" && !isJSONContentType(ct) {
		h.writeError(w, r, http.StatusUnsupportedMediaType, CodeUnsupported,
			"Content-Type must be application/json", nil)
		return
	}

	body, err := readBody(w, r, h.opts.MaxBodyBytes)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			h.writeError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", h.opts.MaxBodyBytes), nil)
			return
		}
		h.writeError(w, r, http.StatusBadRequest, CodeInvalidJSON, "could not read request body", nil)
		return
	}

	var ev ingest.Event
	if err := json.Unmarshal(body, &ev); err != nil {
		h.writeError(w, r, http.StatusBadRequest, CodeInvalidJSON,
			"body is not valid JSON: "+err.Error(), nil)
		return
	}

	ev.Normalize()
	if err := ev.Validate(); err != nil {
		var verrs ingest.ValidationErrors
		errors.As(err, &verrs)
		h.writeError(w, r, http.StatusBadRequest, CodeInvalidEvent, "event envelope is invalid", verrs)
		return
	}

	subject := ev.Subject(h.opts.SubjectPrefix)
	dedupeKey := ev.DedupeKey()

	// Reserve before publishing, not after. A duplicate that arrives while
	// the first copy is still in the publish queue has to lose, and only an
	// atomic claim taken up front can guarantee that.
	reservation, _ := json.Marshal(idem.Reservation{
		TraceID:   trace,
		Subject:   subject,
		Key:       ev.Key,
		Operation: ev.Operation,
		ClaimedAt: receivedAt.UTC(),
	})

	idemCtx, cancel := context.WithTimeout(r.Context(), h.opts.IdempotencyTimeout)
	err = h.opts.Idempotency.Reserve(idemCtx, dedupeKey, reservation)
	cancel()

	switch {
	case err == nil:
	case errors.Is(err, idem.ErrDuplicate):
		h.log.Info("duplicate event rejected",
			"trace_id", trace,
			"business_object", ev.BusinessObject,
			"operation", ev.Operation,
			"key", ev.Key,
			"dedupe_key", dedupeKey,
		)
		writeJSON(w, h.opts.DuplicateStatus, AcceptResponse{
			TraceID:    trace,
			Status:     StatusDuplicate,
			Subject:    subject,
			ReceivedAt: receivedAt,
		})
		return
	default:
		// The idempotency store is the one dependency /ingest cannot work
		// without: publishing anyway would defeat the whole point.
		h.log.Error("idempotency check failed",
			"trace_id", trace, "dedupe_key", dedupeKey, "error", err.Error())
		h.writeError(w, r, http.StatusServiceUnavailable, CodeUpstream,
			"idempotency store unavailable, retry shortly", nil)
		return
	}

	sub := broker.Submission{
		Event:      ev,
		Raw:        body,
		TraceID:    trace,
		Subject:    subject,
		DedupeKey:  dedupeKey,
		ReceivedAt: receivedAt,
	}

	result, err := h.opts.Publisher.Submit(sub)
	if err != nil {
		// Nothing was published, so the claim has to go back or SAP's
		// retry of this document would be rejected as a duplicate.
		h.release(r.Context(), dedupeKey, trace)

		if errors.Is(err, broker.ErrQueueFull) {
			h.log.Warn("shedding load, publish queue full", "trace_id", trace, "subject", subject)
			w.Header().Set("Retry-After", "5")
			h.writeError(w, r, http.StatusServiceUnavailable, CodeBackpressure,
				"gateway is saturated, retry shortly", nil)
			return
		}
		h.log.Error("submit failed", "trace_id", trace, "error", err.Error())
		h.writeError(w, r, http.StatusServiceUnavailable, CodeUpstream,
			"gateway is shutting down, retry shortly", nil)
		return
	}

	if !h.opts.PublishSync {
		// The whole point of the gateway: the ABAP work process is
		// released here, before NATS has been touched.
		writeJSON(w, http.StatusAccepted, AcceptResponse{
			TraceID:    trace,
			Status:     StatusAccepted,
			Subject:    subject,
			ReceivedAt: receivedAt,
		})
		return
	}

	waitCtx, cancelWait := context.WithTimeout(r.Context(), h.opts.PublishTimeout)
	defer cancelWait()

	select {
	case res := <-result:
		if res.Err != nil {
			h.release(r.Context(), dedupeKey, trace)
			h.log.Error("synchronous publish failed", "trace_id", trace, "error", res.Err.Error())
			h.writeError(w, r, http.StatusServiceUnavailable, CodeUpstream,
				"could not publish event, retry shortly", nil)
			return
		}
		writeJSON(w, http.StatusAccepted, AcceptResponse{
			TraceID:    trace,
			Status:     StatusStored,
			Subject:    subject,
			Stream:     res.Ack.Stream,
			Sequence:   res.Ack.Sequence,
			ReceivedAt: receivedAt,
		})
	case <-waitCtx.Done():
		// The publish is still in flight, so the reservation stays: the
		// event may yet land, and releasing now could admit a duplicate.
		h.log.Warn("synchronous publish timed out, event still in flight",
			"trace_id", trace, "subject", subject)
		writeJSON(w, http.StatusAccepted, AcceptResponse{
			TraceID:    trace,
			Status:     StatusAccepted,
			Subject:    subject,
			ReceivedAt: receivedAt,
		})
	}
}

func (h *handler) release(ctx context.Context, key, trace string) {
	// Detached from the request context: the client may already be gone,
	// but the reservation still has to come back.
	relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), h.opts.IdempotencyTimeout)
	defer cancel()

	if err := h.opts.Idempotency.Release(relCtx, key); err != nil {
		// Not fatal, but worth an alert: this key is now blocked until the
		// TTL expires, and SAP's retries of it will be rejected.
		h.log.Error("could not release idempotency reservation",
			"trace_id", trace, "dedupe_key", key, "error", err.Error())
	}
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	h.writeError(w, r, http.StatusNotFound, CodeNotFound, "no such endpoint", nil)
}

func methodNotAllowed(h *handler, allowed string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allowed)
		h.writeError(w, r, http.StatusMethodNotAllowed, CodeNotFound,
			r.Method+" is not allowed on "+r.URL.Path, nil)
	})
}

func (h *handler) writeError(
	w http.ResponseWriter,
	r *http.Request,
	status int,
	code, msg string,
	details []ingest.ValidationError,
) {
	writeJSON(w, status, ErrorResponse{
		TraceID: TraceIDFrom(r.Context()),
		Code:    code,
		Error:   msg,
		Details: details,
	})
}
