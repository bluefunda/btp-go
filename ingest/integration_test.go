package ingest_test

// End-to-end coverage against a real NATS server with JetStream enabled.
//
// The tests run when either NATS_TEST_URL points at a JetStream-enabled
// server or a `nats-server` binary is on PATH (in which case one is started
// on a free port with a temporary store directory). Otherwise they skip, so
// `go test ./...` stays green on a machine with neither.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bluefunda/btp-go/ingest"
	"github.com/bluefunda/btp-go/ingest/api"
	"github.com/bluefunda/btp-go/ingest/broker"
	"github.com/bluefunda/btp-go/ingest/idem"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	testStream        = "IT_EVENTS"
	testSubjectPrefix = "it.events"
	testBucket        = "IT_IDEMPOTENCY"
	testAPIKey        = "0123456789abcdef0123"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// natsURL returns a usable server URL, starting one if necessary, or skips
// the test.
func natsURL(t *testing.T) string {
	t.Helper()

	if url := os.Getenv("NATS_TEST_URL"); url != "" {
		return url
	}

	bin, err := exec.LookPath("nats-server")
	if err != nil {
		t.Skip("no nats-server on PATH and NATS_TEST_URL is unset; skipping integration test")
	}

	port := freePort(t)
	cmd := exec.Command(bin,
		"--port", strconv.Itoa(port),
		"--jetstream",
		"--store_dir", t.TempDir(),
		"--log", "/dev/null",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start nats-server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	url := fmt.Sprintf("nats://127.0.0.1:%d", port)
	waitForServer(t, url)
	return url
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForServer(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		nc, err := nats.Connect(url, nats.Timeout(500*time.Millisecond))
		if err == nil {
			nc.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nats-server at %s never became reachable", url)
}

// harness is a fully wired gateway: HTTP front end, JetStream topology, and
// KV-backed idempotency. There is no relay worker and no downstream sink —
// this is a pure HTTP-to-JetStream proxy, and the tests verify messages land
// on the stream by reading them back with a plain consumer.
type harness struct {
	server    *httptest.Server
	js        jetstream.JetStream
	publisher *broker.Publisher
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	log := discardLogger()
	url := natsURL(t)
	ctx := t.Context()

	nc, err := broker.Connect(broker.ConnOptions{URL: url, Name: "integration-test", Logger: log})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}

	// Each test gets a clean stream and bucket; leftovers from a previous
	// run would make message counts meaningless.
	_ = js.DeleteStream(ctx, testStream)
	_ = js.DeleteKeyValue(ctx, testBucket)

	if err := broker.EnsureTopology(ctx, js, broker.TopologyOptions{
		StreamName:      testStream,
		SubjectFilter:   testSubjectPrefix + ".>",
		MaxAge:          time.Hour,
		Replicas:        1,
		DuplicateWindow: 2 * time.Minute,
		Logger:          log,
	}); err != nil {
		t.Fatalf("topology: %v", err)
	}

	store, err := idem.NewKeyValue(ctx, js, idem.KeyValueConfig{
		Bucket:   testBucket,
		TTL:      time.Hour,
		Replicas: 1,
		Manage:   true,
	})
	if err != nil {
		t.Fatalf("kv store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	publisher := broker.NewPublisher(js, broker.PublisherOptions{
		QueueSize:   256,
		Concurrency: 4,
		Timeout:     5 * time.Second,
		Retries:     2,
		Logger:      log,
		OnFailure: func(ctx context.Context, s broker.Submission, _ error) {
			_ = store.Release(ctx, s.DedupeKey)
		},
	})
	t.Cleanup(func() { _ = publisher.Drain(context.Background()) })

	handler, err := api.NewHandler(api.Options{
		Publisher:     publisher,
		Idempotency:   store,
		SubjectPrefix: testSubjectPrefix,
		Auth:          api.AuthOptions{APIKeys: []string{testAPIKey}},
		Logger:        log,
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &harness{server: server, js: js, publisher: publisher}
}

func (h *harness) post(t *testing.T, key string) *http.Response {
	t.Helper()

	body := fmt.Sprintf(`{"business_object":"BILLING_DOC","operation":"CREATE","key":%q,`+
		`"timestamp":"2023-10-27T10:00:00Z","payload":{"amount":"1234.56","doc":%q}}`, key, key)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		h.server.URL+"/ingest", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(api.HeaderAPIKey, testAPIKey)

	resp, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return resp
}

func decodeAccept(t *testing.T, resp *http.Response) api.AcceptResponse {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()

	var out api.AcceptResponse
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// streamMsgCount returns how many messages the events stream currently
// holds.
func streamMsgCount(t *testing.T, js jetstream.JetStream) uint64 {
	t.Helper()
	stream, err := js.Stream(t.Context(), testStream)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	info, err := stream.Info(t.Context())
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	return info.State.Msgs
}

// readOneMessage waits for exactly one message to appear on the events
// stream and returns it, acking it off an ephemeral consumer so repeat
// calls in the same test do not see it again.
func readOneMessage(t *testing.T, js jetstream.JetStream) jetstream.Msg {
	t.Helper()

	cons, err := js.CreateOrUpdateConsumer(t.Context(), testStream, jetstream.ConsumerConfig{
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		batch, err := cons.Fetch(1, jetstream.FetchMaxWait(500*time.Millisecond))
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		for msg := range batch.Messages() {
			if err := msg.Ack(); err != nil {
				t.Fatalf("ack: %v", err)
			}
			return msg
		}
		if err := batch.Error(); err != nil {
			t.Fatalf("batch: %v", err)
		}
	}
	t.Fatal("no message arrived within 20s")
	return nil
}

// The whole pipeline: HTTP in, JetStream out. Nothing consumes the stream on
// the gateway's behalf — this is exactly what a proxy is expected to do and
// no more.
func TestIntegrationIngestPublishesToStream(t *testing.T) {
	h := newHarness(t)

	const key = "SAP-ISU-100"
	resp := h.post(t, key)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}

	accepted := decodeAccept(t, resp)
	if accepted.Status != api.StatusAccepted {
		t.Errorf("status field = %q, want %q", accepted.Status, api.StatusAccepted)
	}
	if accepted.Subject != testSubjectPrefix+".billing_doc.create" {
		t.Errorf("subject = %q", accepted.Subject)
	}

	msg := readOneMessage(t, h.js)
	if msg.Subject() != testSubjectPrefix+".billing_doc.create" {
		t.Errorf("stream subject = %q", msg.Subject())
	}

	var ev ingest.Event
	if err := json.Unmarshal(msg.Data(), &ev); err != nil {
		t.Fatalf("stream body is not an event envelope: %v", err)
	}
	if ev.Key != key {
		t.Errorf("stream event key = %q, want %q", ev.Key, key)
	}
	if got := msg.Headers().Get(broker.HeaderTraceID); got != accepted.TraceID {
		t.Errorf("stream header %s = %q, want %q", broker.HeaderTraceID, got, accepted.TraceID)
	}
}

// Duplicate suppression through the real KV bucket, which is the part that
// has to hold across replicas.
func TestIntegrationDuplicateSuppression(t *testing.T) {
	h := newHarness(t)

	const key = "SAP-ISU-200"

	first := h.post(t, key)
	if first.StatusCode != http.StatusAccepted {
		t.Fatalf("first post: status = %d, want 202", first.StatusCode)
	}
	_ = decodeAccept(t, first)

	second := h.post(t, key)
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("second post: status = %d, want 409", second.StatusCode)
	}
	if got := decodeAccept(t, second).Status; got != api.StatusDuplicate {
		t.Errorf("status field = %q, want %q", got, api.StatusDuplicate)
	}

	waitFor(t, "the event lands on the stream", func() bool {
		return streamMsgCount(t, h.js) == 1
	})

	// Give a second copy time to show up if suppression leaked.
	time.Sleep(500 * time.Millisecond)
	if n := streamMsgCount(t, h.js); n != 1 {
		t.Errorf("stream holds %d messages, want 1", n)
	}
}

// Nats-Msg-Id gives the server a second dedupe window behind the KV store,
// which is what makes an ambiguous publish retry safe.
func TestIntegrationServerSideMsgIDDeduplication(t *testing.T) {
	h := newHarness(t)

	ev := ingest.Event{
		BusinessObject: "BILLING_DOC",
		Operation:      "CREATE",
		Key:            "SAP-ISU-600",
		Timestamp:      time.Now().UTC(),
		Payload:        json.RawMessage(`{"amount":"1.00"}`),
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	sub := broker.Submission{
		Event:      ev,
		Raw:        raw,
		TraceID:    ingest.NewTraceID(),
		Subject:    ev.Subject(testSubjectPrefix),
		DedupeKey:  ev.DedupeKey(),
		ReceivedAt: time.Now(),
	}

	// Publish the same submission twice, bypassing the KV store the way a
	// retry after an ambiguous timeout would.
	first, err := h.publisher.SubmitAndWait(t.Context(), sub)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if first.Duplicate {
		t.Error("the first publish was reported as a duplicate")
	}

	second, err := h.publisher.SubmitAndWait(t.Context(), sub)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if !second.Duplicate {
		t.Error("the second publish was not recognised as a duplicate")
	}
	if second.Sequence != first.Sequence {
		t.Errorf("sequence = %d, want the original %d", second.Sequence, first.Sequence)
	}

	if n := streamMsgCount(t, h.js); n != 1 {
		t.Errorf("stream holds %d messages, want 1", n)
	}
}

// Releasing a reservation has to make the key claimable again, or a failed
// publish would block that document for the whole TTL.
func TestIntegrationReservationRollbackAllowsRetry(t *testing.T) {
	url := natsURL(t)
	log := discardLogger()
	ctx := t.Context()

	nc, err := broker.Connect(broker.ConnOptions{URL: url, Name: "rollback-test", Logger: log})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	_ = js.DeleteKeyValue(ctx, testBucket)

	store, err := idem.NewKeyValue(ctx, js, idem.KeyValueConfig{
		Bucket: testBucket, TTL: time.Hour, Replicas: 1, Manage: true,
	})
	if err != nil {
		t.Fatalf("kv store: %v", err)
	}
	defer func() { _ = store.Close() }()

	const key = "BILLING_DOC.SAP-ISU-700"

	if err := store.Reserve(ctx, key, []byte(`{}`)); err != nil {
		t.Fatalf("first Reserve() = %v", err)
	}
	if err := store.Reserve(ctx, key, []byte(`{}`)); err == nil {
		t.Fatal("second Reserve() = nil, want a duplicate error")
	}
	if err := store.Release(ctx, key); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	// Purge, not Delete: a delete tombstone would fail the next Create's
	// revision check and silently block the key.
	if err := store.Reserve(ctx, key, []byte(`{}`)); err != nil {
		t.Fatalf("Reserve() after Release = %v, want nil", err)
	}
}

// waitFor polls cond until it holds, failing the test on timeout.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after 20s waiting for: %s", what)
}
