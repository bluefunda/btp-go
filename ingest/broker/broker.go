// Package broker owns everything that touches NATS JetStream: the
// connection, the events stream, and the asynchronous publisher.
//
// [Connect] and [EnsureTopology] bring the infrastructure up. [Publisher]
// takes validated events off the HTTP handler and gets them durably stored
// in JetStream, applying backpressure rather than buffering without limit.
// What happens to the stream afterward — who consumes it and how — is
// outside this package's concern; the gateway is a proxy, not a delivery
// pipeline.
package broker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ConnOptions describes how to reach the NATS server or cluster.
type ConnOptions struct {
	// URL is a comma-separated list of server URLs.
	URL string

	// Name identifies this client in `nats server report connections`.
	Name string

	// User, Password and Token are optional credentials. Token wins when
	// both are supplied.
	User     string
	Password string
	Token    string

	// CredsFile is a NATS .creds file; NKeySeedFile is a bare NKey seed.
	CredsFile    string
	NKeySeedFile string

	// CAFile, CertFile and KeyFile configure TLS to the server. CertFile
	// and KeyFile together enable client-certificate authentication.
	CAFile   string
	CertFile string
	KeyFile  string

	// ConnectTimeout bounds the initial dial.
	ConnectTimeout time.Duration

	// ReconnectWait is the pause between reconnect attempts, and
	// MaxReconnects caps them (-1 for unlimited).
	ReconnectWait time.Duration
	MaxReconnects int

	// Logger receives connection lifecycle events. Required.
	Logger *slog.Logger
}

// Connect dials NATS and installs structured lifecycle logging.
//
// It fails fast when the server is unreachable rather than returning a
// not-yet-connected handle. A gateway that cannot reach NATS cannot do
// anything useful, so it is better for the orchestrator to see a failed
// start than for the process to sit healthy-looking and reject every
// request.
func Connect(opts ConnOptions) (*nats.Conn, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 10 * time.Second
	}
	if opts.ReconnectWait <= 0 {
		opts.ReconnectWait = 2 * time.Second
	}
	if opts.MaxReconnects == 0 {
		opts.MaxReconnects = -1
	}

	natsOpts := []nats.Option{
		nats.Name(opts.Name),
		nats.Timeout(opts.ConnectTimeout),
		nats.ReconnectWait(opts.ReconnectWait),
		nats.MaxReconnects(opts.MaxReconnects),
		nats.ReconnectJitter(100*time.Millisecond, time.Second),
		nats.PingInterval(20 * time.Second),
		nats.MaxPingsOutstanding(3),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			log.Error("nats disconnected", "error", errString(err))
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info("nats reconnected", "url", nc.ConnectedUrl())
		}),
		nats.ClosedHandler(func(_ *nats.Conn) {
			// Reached either on a deliberate Close or after reconnect
			// attempts are exhausted, so it is worth a warning either way.
			log.Warn("nats connection closed, no further reconnects")
		}),
		nats.ErrorHandler(func(_ *nats.Conn, sub *nats.Subscription, err error) {
			attrs := []any{"error", errString(err)}
			if sub != nil {
				attrs = append(attrs, "subject", sub.Subject)
			}
			log.Error("nats async error", attrs...)
		}),
	}

	switch {
	case opts.CredsFile != "":
		natsOpts = append(natsOpts, nats.UserCredentials(opts.CredsFile))
	case opts.NKeySeedFile != "":
		opt, err := nats.NkeyOptionFromSeed(opts.NKeySeedFile)
		if err != nil {
			return nil, fmt.Errorf("broker: nkey seed: %w", err)
		}
		natsOpts = append(natsOpts, opt)
	case opts.Token != "":
		natsOpts = append(natsOpts, nats.Token(opts.Token))
	case opts.User != "":
		natsOpts = append(natsOpts, nats.UserInfo(opts.User, opts.Password))
	}

	tlsCfg, err := buildTLS(opts)
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		natsOpts = append(natsOpts, nats.Secure(tlsCfg))
	}

	nc, err := nats.Connect(opts.URL, natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("broker: connect to %q: %w", opts.URL, err)
	}
	log.Info("nats connected",
		"url", nc.ConnectedUrl(),
		"server_id", nc.ConnectedServerId(),
		"server_version", nc.ConnectedServerVersion(),
	)
	return nc, nil
}

func buildTLS(opts ConnOptions) (*tls.Config, error) {
	if opts.CAFile == "" && opts.CertFile == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("broker: read NATS CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("broker: NATS CA file %q contains no certificates", opts.CAFile)
		}
		cfg.RootCAs = pool
	}

	if opts.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.CertFile, opts.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("broker: load NATS client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// TopologyOptions describes the stream the gateway publishes into.
type TopologyOptions struct {
	// StreamName and SubjectFilter define the events stream. SubjectFilter
	// is a wildcard such as "events.>".
	StreamName    string
	SubjectFilter string

	// MaxAge is the retention window. Zero means unlimited.
	MaxAge time.Duration

	// MaxBytes caps the stream on disk. Zero means unlimited.
	MaxBytes int64

	// Replicas is the stream's replica count.
	Replicas int

	// DuplicateWindow is the server-side Nats-Msg-Id dedupe window.
	DuplicateWindow time.Duration

	// Logger is required.
	Logger *slog.Logger
}

// EnsureTopology creates or updates the events stream the gateway publishes
// into.
//
// It uses limits-based retention rather than a work queue: a work queue
// deletes a message the moment some consumer acks it, which silently
// discards the gateway's copy the instant anyone downstream reads it. With
// limits retention the published events stay on the stream for the
// configured window regardless of who reads them or how many times.
func EnsureTopology(ctx context.Context, js jetstream.JetStream, opts TopologyOptions) error {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.Replicas < 1 {
		opts.Replicas = 1
	}

	events := jetstream.StreamConfig{
		Name:        opts.StreamName,
		Description: "Business events published by the ingestion gateway",
		Subjects:    []string{opts.SubjectFilter},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		Discard:     jetstream.DiscardOld,
		MaxAge:      opts.MaxAge,
		MaxBytes:    orUnlimited(opts.MaxBytes),
		Replicas:    opts.Replicas,
		Duplicates:  opts.DuplicateWindow,
	}
	if _, err := js.CreateOrUpdateStream(ctx, events); err != nil {
		return fmt.Errorf("broker: ensure stream %q: %w", opts.StreamName, err)
	}
	log.Info("jetstream stream ready",
		"stream", opts.StreamName,
		"subjects", opts.SubjectFilter,
		"max_age", opts.MaxAge.String(),
		"replicas", opts.Replicas,
	)
	return nil
}

// orUnlimited maps 0 to the JetStream sentinel for "no limit". Sending a
// literal 0 would be rejected by the server as an invalid size.
func orUnlimited(v int64) int64 {
	if v <= 0 {
		return -1
	}
	return v
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
