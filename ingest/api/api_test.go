package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bluefunda/btp-go/ingest/broker"
	"github.com/bluefunda/btp-go/ingest/idem"
)

const testAPIKey = "0123456789abcdef0123"

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakePublisher records submissions and can be told to reject them.
type fakePublisher struct {
	mu        sync.Mutex
	subs      []broker.Submission
	submitErr error
	result    broker.Result
	saturated bool
}

func (p *fakePublisher) Submit(s broker.Submission) (<-chan broker.Result, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.submitErr != nil {
		return nil, p.submitErr
	}
	p.subs = append(p.subs, s)

	res := make(chan broker.Result, 1)
	r := p.result
	if r.Ack == nil && r.Err == nil {
		r = broker.Result{Ack: &broker.Ack{Stream: "EVENTS", Sequence: uint64(len(p.subs))}}
	}
	res <- r
	close(res)
	return res, nil
}

func (p *fakePublisher) Saturated() bool { return p.saturated }

func (p *fakePublisher) submissions() []broker.Submission {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]broker.Submission(nil), p.subs...)
}

// countingStore wraps a store so tests can assert on reservations and
// releases, and inject failures.
type countingStore struct {
	inner      idem.Store
	mu         sync.Mutex
	reserves   int
	releases   int
	reserveErr error
}

func newCountingStore() *countingStore {
	return &countingStore{inner: idem.NewMemory(time.Minute)}
}

func (s *countingStore) Reserve(ctx context.Context, key string, meta []byte) error {
	s.mu.Lock()
	s.reserves++
	err := s.reserveErr
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return s.inner.Reserve(ctx, key, meta)
}

func (s *countingStore) Release(ctx context.Context, key string) error {
	s.mu.Lock()
	s.releases++
	s.mu.Unlock()
	return s.inner.Release(ctx, key)
}

func (s *countingStore) Kind() string { return "test" }
func (s *countingStore) Close() error { return s.inner.Close() }
func (s *countingStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserves, s.releases
}

type testHarness struct {
	handler http.Handler
	pub     *fakePublisher
	store   *countingStore
}

func newHarness(t *testing.T, mutate func(*Options)) *testHarness {
	t.Helper()

	pub := &fakePublisher{}
	store := newCountingStore()
	t.Cleanup(func() { _ = store.Close() })

	opts := Options{
		Publisher:     pub,
		Idempotency:   store,
		SubjectPrefix: "events",
		Auth:          AuthOptions{APIKeys: []string{testAPIKey}},
		Logger:        testLogger(),
	}
	if mutate != nil {
		mutate(&opts)
	}

	h, err := NewHandler(opts)
	if err != nil {
		t.Fatalf("NewHandler() = %v", err)
	}
	return &testHarness{handler: h, pub: pub, store: store}
}

const validBody = `{
  "business_object": "BILLING_DOC",
  "operation": "CREATE",
  "key": "SAP-ISU-1928340192",
  "timestamp": "2023-10-27T10:00:00Z",
  "payload": {"amount": "1234.56"}
}`

func (h *testHarness) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(HeaderAPIKey, testAPIKey)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

func decodeAccept(t *testing.T, rec *httptest.ResponseRecorder) AcceptResponse {
	t.Helper()
	var resp AcceptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return resp
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return resp
}

// The headline requirement: accept and answer without waiting on the broker.
func TestIngestReturns202WithTraceID(t *testing.T) {
	h := newHarness(t, nil)
	rec := h.post(validBody)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}

	resp := decodeAccept(t, rec)
	if resp.Status != StatusAccepted {
		t.Errorf("status field = %q, want %q", resp.Status, StatusAccepted)
	}
	if len(resp.TraceID) != 32 {
		t.Errorf("trace_id = %q, want 32 hex characters", resp.TraceID)
	}
	if resp.Subject != "events.billing_doc.create" {
		t.Errorf("subject = %q", resp.Subject)
	}
	// The trace ID is echoed as a header too, so SAP can log it without
	// parsing the body.
	if got := rec.Header().Get(HeaderTraceID); got != resp.TraceID {
		t.Errorf("%s header = %q, want %q", HeaderTraceID, got, resp.TraceID)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}

	subs := h.pub.submissions()
	if len(subs) != 1 {
		t.Fatalf("published %d submissions, want 1", len(subs))
	}
	sub := subs[0]
	if sub.TraceID != resp.TraceID {
		t.Errorf("submission trace ID = %q, want %q", sub.TraceID, resp.TraceID)
	}
	if sub.DedupeKey != "BILLING_DOC.SAP-ISU-1928340192" {
		t.Errorf("DedupeKey = %q", sub.DedupeKey)
	}
	if string(sub.Raw) != validBody {
		t.Error("submitted body differs from the request body")
	}
}

// SAP resends the same document when a work process times out; the second
// copy must not reach the stream.
func TestIngestSuppressesDuplicates(t *testing.T) {
	h := newHarness(t, nil)

	if rec := h.post(validBody); rec.Code != http.StatusAccepted {
		t.Fatalf("first post: status = %d, want 202", rec.Code)
	}

	rec := h.post(validBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second post: status = %d, want 409: %s", rec.Code, rec.Body)
	}
	if got := decodeAccept(t, rec).Status; got != StatusDuplicate {
		t.Errorf("status field = %q, want %q", got, StatusDuplicate)
	}
	if n := len(h.pub.submissions()); n != 1 {
		t.Errorf("published %d submissions, want 1", n)
	}
}

// Some ABAP callers retry on any non-2xx forever, so the duplicate status is
// configurable.
func TestIngestDuplicateStatusIsConfigurable(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.DuplicateStatus = http.StatusOK })

	h.post(validBody)
	rec := h.post(validBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := decodeAccept(t, rec).Status; got != StatusDuplicate {
		t.Errorf("status field = %q, want %q", got, StatusDuplicate)
	}
}

// A different operation on the same document is a different event, but it
// shares the dedupe identity by design: one ingest per document per window.
func TestIngestDeduplicatesOnBusinessObjectAndKey(t *testing.T) {
	h := newHarness(t, nil)

	h.post(validBody)
	other := strings.Replace(validBody, `"key": "SAP-ISU-1928340192"`, `"key": "SAP-ISU-OTHER"`, 1)

	if rec := h.post(other); rec.Code != http.StatusAccepted {
		t.Fatalf("a different key was rejected: status = %d", rec.Code)
	}
	if n := len(h.pub.submissions()); n != 2 {
		t.Errorf("published %d submissions, want 2", n)
	}
}

func TestIngestValidationErrors(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantCode   string
		wantFields []string
	}{
		{
			name:     "not json",
			body:     `{nope`,
			wantCode: CodeInvalidJSON,
		},
		{
			name:       "empty object",
			body:       `{}`,
			wantCode:   CodeInvalidEvent,
			wantFields: []string{"business_object", "operation", "key", "timestamp", "payload"},
		},
		{
			name: "missing payload",
			body: `{"business_object":"BILLING_DOC","operation":"CREATE","key":"K",` +
				`"timestamp":"2023-10-27T10:00:00Z"}`,
			wantCode:   CodeInvalidEvent,
			wantFields: []string{"payload"},
		},
		{
			name: "subject injection through the business object",
			body: `{"business_object":"A.>","operation":"CREATE","key":"K",` +
				`"timestamp":"2023-10-27T10:00:00Z","payload":{}}`,
			wantCode:   CodeInvalidEvent,
			wantFields: []string{"business_object"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)
			rec := h.post(tt.body)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
			}
			resp := decodeError(t, rec)
			if resp.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", resp.Code, tt.wantCode)
			}
			if resp.TraceID == "" {
				t.Error("error response carries no trace ID")
			}

			for _, field := range tt.wantFields {
				found := false
				for _, d := range resp.Details {
					if d.Field == field {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("details do not mention %q: %+v", field, resp.Details)
				}
			}

			// An invalid event must not consume the dedupe key, or a
			// corrected resend of the same document would be rejected.
			if reserves, _ := h.store.counts(); reserves != 0 {
				t.Errorf("made %d reservations for an invalid event, want 0", reserves)
			}
			if n := len(h.pub.submissions()); n != 0 {
				t.Errorf("published %d submissions for an invalid event, want 0", n)
			}
		})
	}
}

// A full queue is backpressure, not an error: SAP is told to come back.
func TestIngestSheddsLoadWhenQueueIsFull(t *testing.T) {
	h := newHarness(t, nil)
	h.pub.submitErr = broker.ErrQueueFull

	rec := h.post(validBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec).Code; got != CodeBackpressure {
		t.Errorf("code = %q, want %q", got, CodeBackpressure)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("no Retry-After header on a backpressure response")
	}

	// The claim must be handed back, otherwise the retry this response
	// asks for would be rejected as a duplicate.
	if _, releases := h.store.counts(); releases != 1 {
		t.Errorf("released %d reservations, want 1", releases)
	}
}

func TestIngestRetryAfterShedIsAccepted(t *testing.T) {
	h := newHarness(t, nil)

	h.pub.submitErr = broker.ErrQueueFull
	if rec := h.post(validBody); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	h.pub.submitErr = nil
	if rec := h.post(validBody); rec.Code != http.StatusAccepted {
		t.Fatalf("retry after backpressure: status = %d, want 202: %s", rec.Code, rec.Body)
	}
}

// Publishing without a working idempotency store would defeat the point of
// the endpoint, so an unavailable store fails the request.
func TestIngestRejectsWhenIdempotencyStoreIsDown(t *testing.T) {
	h := newHarness(t, nil)
	h.store.reserveErr = errors.New("kv bucket unreachable")

	rec := h.post(validBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec).Code; got != CodeUpstream {
		t.Errorf("code = %q, want %q", got, CodeUpstream)
	}
	if n := len(h.pub.submissions()); n != 0 {
		t.Errorf("published %d submissions without a working dedupe check, want 0", n)
	}
}

func TestIngestSyncModeReturnsStreamSequence(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.PublishSync = true })

	rec := h.post(validBody)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}

	resp := decodeAccept(t, rec)
	if resp.Status != StatusStored {
		t.Errorf("status field = %q, want %q", resp.Status, StatusStored)
	}
	if resp.Stream != "EVENTS" || resp.Sequence != 1 {
		t.Errorf("stream/sequence = %q/%d, want EVENTS/1", resp.Stream, resp.Sequence)
	}
}

func TestIngestSyncModeReportsPublishFailure(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.PublishSync = true })
	h.pub.result = broker.Result{Err: errors.New("no stream bound to subject")}

	rec := h.post(validBody)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}
	if _, releases := h.store.counts(); releases != 1 {
		t.Errorf("released %d reservations after a failed publish, want 1", releases)
	}
}

func TestIngestRejectsNonJSONContentType(t *testing.T) {
	h := newHarness(t, nil)

	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set(HeaderAPIKey, testAPIKey)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
}

func TestIngestAcceptsJSONContentTypeVariants(t *testing.T) {
	for _, ct := range []string{
		"application/json",
		"application/json; charset=utf-8",
		"application/problem+json",
		"", // ABAP HTTP clients do not always set one
	} {
		t.Run(ct, func(t *testing.T) {
			h := newHarness(t, nil)

			req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
			if ct != "" {
				req.Header.Set("Content-Type", ct)
			}
			req.Header.Set(HeaderAPIKey, testAPIKey)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusAccepted {
				t.Errorf("status = %d, want 202: %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestIngestRejectsOversizedBodies(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.MaxBodyBytes = 128 })

	big := `{"business_object":"BILLING_DOC","operation":"CREATE","key":"K",` +
		`"timestamp":"2023-10-27T10:00:00Z","payload":{"blob":"` + strings.Repeat("x", 4096) + `"}}`

	rec := h.post(big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413: %s", rec.Code, rec.Body)
	}
	if got := decodeError(t, rec).Code; got != CodePayloadTooLarge {
		t.Errorf("code = %q, want %q", got, CodePayloadTooLarge)
	}
}

func TestIngestRejectsWrongMethod(t *testing.T) {
	h := newHarness(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	req.Header.Set(HeaderAPIKey, testAPIKey)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestUnknownPathReturnsJSON404(t *testing.T) {
	h := newHarness(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/nope", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != CodeNotFound {
		t.Errorf("code = %q, want %q", got, CodeNotFound)
	}
}
