package ingest_test

// The examples below have no "Output" comment, so they are compiled but not
// executed — they would need a live NATS server.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/bluefunda/btp-go/ingest"
	"github.com/bluefunda/btp-go/ingest/api"
	"github.com/bluefunda/btp-go/ingest/broker"
	"github.com/bluefunda/btp-go/ingest/idem"
	"github.com/nats-io/nats.go/jetstream"
)

// Validating an inbound envelope, which is the first thing /ingest does.
func ExampleEvent_Validate() {
	body := []byte(`{
        "business_object": "BILLING_DOC",
        "operation": "CREATE",
        "key": "SAP-ISU-1928340192",
        "timestamp": "2023-10-27T10:00:00Z",
        "payload": {"amount": "1234.56"}
    }`)

	var ev ingest.Event
	if err := json.Unmarshal(body, &ev); err != nil {
		panic(err)
	}

	// Normalize before validating: it upper-cases the identity fields so
	// that "billing_doc" and "BILLING_DOC" deduplicate as one document.
	ev.Normalize()

	if err := ev.Validate(); err != nil {
		var verrs ingest.ValidationErrors
		if errors.As(err, &verrs) {
			for _, ve := range verrs {
				fmt.Printf("%s: %s\n", ve.Field, ve.Reason)
			}
		}
		return
	}

	fmt.Println(ev.Subject("events")) // events.billing_doc.create
	fmt.Println(ev.DedupeKey())       // BILLING_DOC.SAP-ISU-1928340192
}

// Wiring the gateway: topology, idempotency, publisher, HTTP handler. None
// of this assumes anything about the upstream caller, and nothing reads the
// stream back out — the gateway is a proxy from HTTP into JetStream, full
// stop. Whatever consumes the stream afterward is a separate concern.
func Example_ingestionGateway() {
	ctx := context.Background()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	nc, err := broker.Connect(broker.ConnOptions{
		URL:    "nats://127.0.0.1:4222",
		Name:   "ingest-gateway",
		Logger: log,
	})
	if err != nil {
		panic(err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		panic(err)
	}

	if err := broker.EnsureTopology(ctx, js, broker.TopologyOptions{
		StreamName:      "EVENTS",
		SubjectFilter:   "events.>",
		MaxAge:          7 * 24 * time.Hour,
		Replicas:        1,
		DuplicateWindow: 2 * time.Minute,
		Logger:          log,
	}); err != nil {
		panic(err)
	}

	// KV Create is atomic on the server, so exactly one of N concurrent
	// replicas can claim a given document.
	store, err := idem.NewKeyValue(ctx, js, idem.KeyValueConfig{
		Bucket: "IDEMPOTENCY",
		TTL:    24 * time.Hour,
		Manage: true,
	})
	if err != nil {
		panic(err)
	}
	defer func() { _ = store.Close() }()

	publisher := broker.NewPublisher(js, broker.PublisherOptions{
		QueueSize:   4096,
		Concurrency: 8,
		Timeout:     10 * time.Second,
		Retries:     3,
		Logger:      log,
		// Hand the claim back when a publish is abandoned, or the caller's
		// retry of that document would be rejected as a duplicate and lost.
		OnFailure: func(ctx context.Context, s broker.Submission, _ error) {
			_ = store.Release(ctx, s.DedupeKey)
		},
	})
	defer func() { _ = publisher.Drain(ctx) }()

	handler, err := api.NewHandler(api.Options{
		Publisher:     publisher,
		Idempotency:   store,
		SubjectPrefix: "events",
		Auth:          api.AuthOptions{APIKeys: []string{os.Getenv("API_KEY")}},
		Logger:        log,
	})
	if err != nil {
		panic(err)
	}

	server, err := api.NewServer(handler, api.ServerOptions{Addr: ":8080", Logger: log})
	if err != nil {
		panic(err)
	}
	if err := server.Serve(ctx, 30*time.Second); err != nil {
		panic(err)
	}
}
