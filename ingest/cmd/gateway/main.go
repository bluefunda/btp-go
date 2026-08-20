// Command gateway is a generic asynchronous ingestion proxy between an HTTP
// upstream and NATS JetStream.
//
// Any caller (a commonly-deployed case is an SAP ABAP system posting via its
// HTTP destination configuration) posts a business event to POST /ingest.
// The gateway validates the envelope, suppresses duplicates within a
// configurable TTL window, and publishes the event durably to a JetStream
// stream — nothing more. It does not consume the stream back out, relay to
// any downstream system, or know what happens to an event after it is
// published; that is entirely the concern of whatever reads the stream
// afterward. It runs unchanged on Cloud Foundry, Kyma, Kubernetes, or a
// laptop.
//
// Configuration is entirely environment-driven; see the config package and
// the README for the full list.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/bluefunda/btp-go/ingest/api"
	"github.com/bluefunda/btp-go/ingest/broker"
	"github.com/bluefunda/btp-go/ingest/config"
	"github.com/bluefunda/btp-go/ingest/idem"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	if err := run(); err != nil {
		// The logger may not exist yet if configuration failed, so this
		// one message goes to stderr unconditionally.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: cfg.Service.LogLevel,
	})).With(
		"service", cfg.Service.Name,
		"env", cfg.Service.Environment,
	)
	slog.SetDefault(log)

	log.Info("starting",
		"go_version", runtime.Version(),
		"http_addr", cfg.HTTP.Addr,
		"nats_url", cfg.NATS.URL,
		"stream", cfg.Stream.Name,
		"subject_prefix", cfg.Stream.SubjectPrefix,
		"publish_sync", cfg.Stream.PublishSync,
	)

	// SIGINT and SIGTERM cancel this context, which unwinds every stage in
	// order below.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	nc, err := broker.Connect(broker.ConnOptions{
		URL:            cfg.NATS.URL,
		Name:           cfg.Service.Name,
		User:           cfg.NATS.User,
		Password:       cfg.NATS.Password,
		Token:          cfg.NATS.Token,
		CredsFile:      cfg.NATS.CredsFile,
		NKeySeedFile:   cfg.NATS.NKeySeedFile,
		CAFile:         cfg.NATS.CAFile,
		CertFile:       cfg.NATS.CertFile,
		KeyFile:        cfg.NATS.KeyFile,
		ConnectTimeout: cfg.NATS.ConnectTimeout,
		ReconnectWait:  cfg.NATS.ReconnectWait,
		MaxReconnects:  cfg.NATS.MaxReconnects,
		Logger:         log,
	})
	if err != nil {
		return err
	}
	defer closeNATS(nc, cfg.NATS.DrainTimeout, log)

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}

	setupCtx, cancelSetup := context.WithTimeout(ctx, 30*time.Second)
	defer cancelSetup()

	if cfg.Stream.Manage {
		if err := broker.EnsureTopology(setupCtx, js, broker.TopologyOptions{
			StreamName:      cfg.Stream.Name,
			SubjectFilter:   cfg.EventSubjectFilter(),
			MaxAge:          cfg.Stream.MaxAge,
			MaxBytes:        cfg.Stream.MaxBytes,
			Replicas:        cfg.Stream.Replicas,
			DuplicateWindow: cfg.Stream.DuplicateWindow,
			Logger:          log,
		}); err != nil {
			return err
		}
	} else {
		log.Warn("stream management disabled, assuming topology exists", "stream", cfg.Stream.Name)
	}

	store, err := newIdempotencyStore(setupCtx, js, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	log.Info("idempotency store ready", "backend", store.Kind(), "ttl", cfg.Idempotency.TTL.String())

	publisher := broker.NewPublisher(js, broker.PublisherOptions{
		QueueSize:   cfg.Stream.PublishQueueSize,
		Concurrency: cfg.Stream.PublishConcurrency,
		Timeout:     cfg.Stream.PublishTimeout,
		Retries:     cfg.Stream.PublishRetries,
		Logger:      log,
		OnFailure: func(ctx context.Context, s broker.Submission, cause error) {
			// The event never reached JetStream, so the reservation has to
			// go back. Otherwise the caller's next retry of this document is
			// rejected as a duplicate and it is lost for the whole TTL.
			if relErr := store.Release(ctx, s.DedupeKey); relErr != nil {
				log.Error("could not release reservation for abandoned publish",
					"trace_id", s.TraceID,
					"dedupe_key", s.DedupeKey,
					"publish_error", cause.Error(),
					"release_error", relErr.Error(),
				)
				return
			}
			log.Warn("released reservation for abandoned publish",
				"trace_id", s.TraceID, "dedupe_key", s.DedupeKey, "error", cause.Error())
		},
	})

	handler, err := api.NewHandler(api.Options{
		Publisher:          publisher,
		Idempotency:        store,
		SubjectPrefix:      cfg.Stream.SubjectPrefix,
		MaxBodyBytes:       cfg.HTTP.MaxBodyBytes,
		IdempotencyTimeout: cfg.Idempotency.Timeout,
		PublishSync:        cfg.Stream.PublishSync,
		PublishTimeout:     cfg.Stream.PublishTimeout,
		DuplicateStatus:    cfg.HTTP.DuplicateStatus,
		Auth: api.AuthOptions{
			APIKeys:           cfg.HTTP.APIKeys,
			RequireClientCert: cfg.HTTP.ClientCAFile != "",
			AllowedClientCNs:  cfg.HTTP.AllowedClientCNs,
			AllowAnonymous:    cfg.HTTP.AllowAnonymous,
		},
		Checks: []api.Check{natsCheck(nc)},
		Logger: log,
	})
	if err != nil {
		return err
	}

	server, err := api.NewServer(handler, api.ServerOptions{
		Addr:              cfg.HTTP.Addr,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.ReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
		TLSCertFile:       cfg.HTTP.TLSCertFile,
		TLSKeyFile:        cfg.HTTP.TLSKeyFile,
		ClientCAFile:      cfg.HTTP.ClientCAFile,
		Logger:            log,
	})
	if err != nil {
		return err
	}

	var (
		wg       sync.WaitGroup
		serveErr error
	)

	wg.Go(func() {
		serveErr = server.Serve(ctx, cfg.Service.ShutdownTimeout)
		// A listener that dies on its own (port already bound, TLS files
		// unreadable) has to bring the process down.
		if serveErr != nil {
			stop()
		}
	})

	<-ctx.Done()
	log.Info("shutdown signal received, draining")

	// Order matters. The HTTP server is already draining, so no new
	// submissions can arrive; flush what is queued before the connection
	// goes away, and only then let the deferred NATS drain run.
	wg.Wait()

	drainCtx, cancelDrain := context.WithTimeout(context.Background(), cfg.Service.ShutdownTimeout)
	defer cancelDrain()

	if err := publisher.Drain(drainCtx); err != nil {
		log.Error("publisher drain incomplete", "error", err.Error(), "stats", publisher.Stats())
	} else {
		log.Info("publisher drained", "stats", publisher.Stats())
	}

	if serveErr != nil {
		return serveErr
	}
	log.Info("shutdown complete")
	return nil
}

// newIdempotencyStore builds the duplicate-suppression store.
func newIdempotencyStore(
	ctx context.Context,
	js jetstream.JetStream,
	cfg *config.Config,
	log *slog.Logger,
) (idem.Store, error) {
	if cfg.Idempotency.Backend == config.BackendMemory {
		log.Warn("using in-memory idempotency store; duplicates are only suppressed within this process. " +
			"Set IDEMPOTENCY_BACKEND=nats for any deployment with more than one replica")
		return idem.NewMemory(cfg.Idempotency.TTL), nil
	}
	return idem.NewKeyValue(ctx, js, idem.KeyValueConfig{
		Bucket:   cfg.Idempotency.Bucket,
		TTL:      cfg.Idempotency.TTL,
		Replicas: cfg.Idempotency.Replicas,
		Manage:   cfg.Stream.Manage,
	})
}

// natsCheck reports the connection state to /readyz. It is a readiness
// signal only: an instance that cannot reach NATS cannot deduplicate or
// publish, so it should leave the rotation without being restarted.
func natsCheck(nc *nats.Conn) api.Check {
	return api.Check{
		Name: "nats",
		Probe: func(context.Context) error {
			if !nc.IsConnected() {
				return errors.New("not connected: " + nc.Status().String())
			}
			return nil
		},
	}
}

// closeNATS flushes anything still buffered before the socket goes away.
//
// This is deferred, so it runs after the publisher has drained: by then
// every publish has been acknowledged and the flush is a formality. It stays
// because "a formality" and "guaranteed empty" are not the same thing.
func closeNATS(nc *nats.Conn, timeout time.Duration, log *slog.Logger) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if err := nc.FlushTimeout(timeout); err != nil {
		log.Warn("nats flush before close failed", "error", err.Error())
	}
	// The connection's own ClosedHandler logs the close itself.
	nc.Close()
}
