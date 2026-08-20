// Package config loads the gateway configuration from the process
// environment.
//
// Everything is settable by environment variable so the same image runs
// unchanged on Kubernetes, Cloud Foundry, and a laptop. [Load] never returns
// a partially valid Config: it collects every problem it finds and returns
// them together, because discovering misconfiguration one restart at a time
// is the slowest way to bring up a service.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the full gateway configuration.
type Config struct {
	Service     Service
	HTTP        HTTP
	NATS        NATS
	Stream      Stream
	Idempotency Idempotency
}

// Service holds process-level identity and logging settings.
type Service struct {
	// Name identifies this deployment in logs and as the NATS client name.
	Name string

	// Environment is a free-form deployment label ("dev", "prod").
	Environment string

	// LogLevel is one of debug, info, warn, error.
	LogLevel slog.Level

	// ShutdownTimeout bounds the whole graceful shutdown sequence.
	ShutdownTimeout time.Duration
}

// HTTP holds the inbound listener configuration.
type HTTP struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string

	// ReadHeaderTimeout, ReadTimeout, WriteTimeout and IdleTimeout are
	// passed straight to http.Server. ReadHeaderTimeout is the one that
	// actually stops Slowloris; the others bound total request time.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// MaxBodyBytes caps a single /ingest payload.
	MaxBodyBytes int64

	// APIKeys are the accepted values of the X-API-Key header (or a Bearer
	// token). Empty means API-key authentication is disabled.
	APIKeys []string

	// TLSCertFile and TLSKeyFile enable HTTPS when both are set.
	TLSCertFile string
	TLSKeyFile  string

	// ClientCAFile enables mTLS: presented client certificates are verified
	// against this CA bundle.
	ClientCAFile string

	// AllowedClientCNs optionally restricts mTLS to specific certificate
	// common names. Empty means any certificate signed by ClientCAFile.
	AllowedClientCNs []string

	// AllowAnonymous must be set explicitly to run with no authentication
	// at all. It exists so that "no auth" is always a deliberate act.
	AllowAnonymous bool

	// DuplicateStatus is the HTTP status returned when the idempotency
	// store rejects an event as a duplicate. 409 is the honest answer;
	// 200 or 202 is friendlier to callers that treat any non-2xx as a
	// hard failure and retry forever.
	DuplicateStatus int
}

// NATS holds connection settings for the NATS server or cluster.
type NATS struct {
	// URL is a comma-separated list of server URLs.
	URL string

	// User, Password and Token are the optional credentials. A Token wins
	// over User/Password when both are set.
	User     string
	Password string
	Token    string

	// CredsFile is a NATS .creds file (NGS / decentralized auth).
	CredsFile string

	// NKeySeedFile is an NKey seed file, an alternative to CredsFile.
	NKeySeedFile string

	// CAFile, CertFile and KeyFile configure TLS to the NATS server.
	CAFile   string
	CertFile string
	KeyFile  string

	// ConnectTimeout bounds the initial dial.
	ConnectTimeout time.Duration

	// ReconnectWait is the pause between reconnect attempts.
	ReconnectWait time.Duration

	// MaxReconnects is the attempt limit; -1 means retry forever, which is
	// the right default for a long-lived gateway.
	MaxReconnects int

	// DrainTimeout bounds connection drain during shutdown.
	DrainTimeout time.Duration
}

// Stream holds the JetStream topology the gateway publishes into.
type Stream struct {
	// Name is the events stream, e.g. "EVENTS".
	Name string

	// SubjectPrefix is prepended to every published subject; the stream
	// binds "<SubjectPrefix>.>".
	SubjectPrefix string

	// MaxAge is the stream retention window. Zero means unlimited.
	MaxAge time.Duration

	// MaxBytes caps stream size on disk. Zero means unlimited.
	MaxBytes int64

	// Replicas is the JetStream replica count (1 for single-server dev,
	// 3 for a production cluster).
	Replicas int

	// DuplicateWindow is the JetStream server-side Nats-Msg-Id dedupe
	// window — a second line of defence behind the KV store.
	DuplicateWindow time.Duration

	// Manage controls whether the gateway creates or updates the stream on
	// start. Set false when topology is owned by a platform team.
	Manage bool

	// PublishTimeout bounds a single publish-and-await-ack.
	PublishTimeout time.Duration

	// PublishRetries is the number of times a publish is retried before it
	// is considered failed.
	PublishRetries int

	// PublishQueueSize is the depth of the in-process submission queue.
	// It is the backpressure knob: once full, /ingest sheds load with 503
	// instead of growing without bound.
	PublishQueueSize int

	// PublishConcurrency is the number of goroutines draining the queue.
	PublishConcurrency int

	// PublishSync makes /ingest wait for the JetStream ack before
	// answering. Slower, but the 202 then means "durably stored".
	PublishSync bool
}

// Idempotency configures duplicate suppression.
type Idempotency struct {
	// Backend is "nats" (JetStream KV, shared across replicas) or "memory"
	// (per-process, for local development only).
	Backend string

	// Bucket is the KV bucket name when Backend is "nats".
	Bucket string

	// TTL is the duplicate suppression window.
	TTL time.Duration

	// Timeout bounds a single reservation call.
	Timeout time.Duration

	// Replicas is the KV bucket replica count.
	Replicas int
}

// Backend identifiers for Idempotency.Backend.
const (
	BackendNATS   = "nats"
	BackendMemory = "memory"
)

// Load reads the configuration from the environment, applying defaults, and
// validates it.
func Load() (*Config, error) {
	var e envReader

	cfg := &Config{
		Service: Service{
			Name:            e.str("SERVICE_NAME", "ingest-gateway"),
			Environment:     e.str("ENVIRONMENT", "dev"),
			LogLevel:        e.level("LOG_LEVEL", slog.LevelInfo),
			ShutdownTimeout: e.dur("SHUTDOWN_TIMEOUT", 30*time.Second),
		},
		HTTP: HTTP{
			Addr:              addr(e.str("HTTP_ADDR", ""), e.str("PORT", "")),
			ReadHeaderTimeout: e.dur("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       e.dur("HTTP_READ_TIMEOUT", 15*time.Second),
			WriteTimeout:      e.dur("HTTP_WRITE_TIMEOUT", 15*time.Second),
			IdleTimeout:       e.dur("HTTP_IDLE_TIMEOUT", 60*time.Second),
			MaxBodyBytes:      e.i64("HTTP_MAX_BODY_BYTES", 1<<20),
			APIKeys:           e.list("API_KEYS"),
			TLSCertFile:       e.str("TLS_CERT_FILE", ""),
			TLSKeyFile:        e.str("TLS_KEY_FILE", ""),
			ClientCAFile:      e.str("TLS_CLIENT_CA_FILE", ""),
			AllowedClientCNs:  e.list("TLS_ALLOWED_CLIENT_CNS"),
			AllowAnonymous:    e.boolean("ALLOW_ANONYMOUS", false),
			DuplicateStatus:   e.integer("INGEST_DUPLICATE_STATUS", 409),
		},
		NATS: NATS{
			URL:            e.str("NATS_URL", "nats://127.0.0.1:4222"),
			User:           e.str("NATS_USER", ""),
			Password:       e.str("NATS_PASSWORD", ""),
			Token:          e.str("NATS_TOKEN", ""),
			CredsFile:      e.str("NATS_CREDS_FILE", ""),
			NKeySeedFile:   e.str("NATS_NKEY_SEED_FILE", ""),
			CAFile:         e.str("NATS_CA_FILE", ""),
			CertFile:       e.str("NATS_CERT_FILE", ""),
			KeyFile:        e.str("NATS_KEY_FILE", ""),
			ConnectTimeout: e.dur("NATS_CONNECT_TIMEOUT", 10*time.Second),
			ReconnectWait:  e.dur("NATS_RECONNECT_WAIT", 2*time.Second),
			MaxReconnects:  e.integer("NATS_MAX_RECONNECTS", -1),
			DrainTimeout:   e.dur("NATS_DRAIN_TIMEOUT", 15*time.Second),
		},
		Stream: Stream{
			Name:               e.str("STREAM_NAME", "EVENTS"),
			SubjectPrefix:      e.str("STREAM_SUBJECT_PREFIX", "events"),
			MaxAge:             e.dur("STREAM_MAX_AGE", 168*time.Hour),
			MaxBytes:           e.i64("STREAM_MAX_BYTES", 0),
			Replicas:           e.integer("STREAM_REPLICAS", 1),
			DuplicateWindow:    e.dur("STREAM_DUPLICATE_WINDOW", 2*time.Minute),
			Manage:             e.boolean("STREAM_MANAGE", true),
			PublishTimeout:     e.dur("PUBLISH_TIMEOUT", 10*time.Second),
			PublishRetries:     e.integer("PUBLISH_RETRIES", 3),
			PublishQueueSize:   e.integer("PUBLISH_QUEUE_SIZE", 4096),
			PublishConcurrency: e.integer("PUBLISH_CONCURRENCY", 8),
			PublishSync:        e.boolean("PUBLISH_SYNC", false),
		},
		Idempotency: Idempotency{
			Backend:  strings.ToLower(e.str("IDEMPOTENCY_BACKEND", BackendNATS)),
			Bucket:   e.str("IDEMPOTENCY_BUCKET", "IDEMPOTENCY"),
			TTL:      e.dur("IDEMPOTENCY_TTL", 24*time.Hour),
			Timeout:  e.dur("IDEMPOTENCY_TIMEOUT", 3*time.Second),
			Replicas: e.integer("IDEMPOTENCY_REPLICAS", 1),
		},
	}

	if len(e.errs) > 0 {
		return nil, errors.Join(e.errs...)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks cross-field invariants that defaults alone cannot enforce.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.HTTP.Addr == "" {
		bad("config: HTTP_ADDR must not be empty")
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		bad("config: HTTP_MAX_BODY_BYTES must be positive")
	}
	switch c.HTTP.DuplicateStatus {
	case 200, 202, 409:
	default:
		bad("config: INGEST_DUPLICATE_STATUS must be one of 200, 202, 409 (got %d)", c.HTTP.DuplicateStatus)
	}
	if len(c.HTTP.APIKeys) == 0 && c.HTTP.ClientCAFile == "" && !c.HTTP.AllowAnonymous {
		bad("config: no authentication configured; set API_KEYS, TLS_CLIENT_CA_FILE, or ALLOW_ANONYMOUS=true")
	}
	for _, k := range c.HTTP.APIKeys {
		if len(k) < 16 {
			bad("config: every API_KEYS entry must be at least 16 characters")
			break
		}
	}
	if (c.HTTP.TLSCertFile == "") != (c.HTTP.TLSKeyFile == "") {
		bad("config: TLS_CERT_FILE and TLS_KEY_FILE must be set together")
	}
	if c.HTTP.ClientCAFile != "" && c.HTTP.TLSCertFile == "" {
		bad("config: TLS_CLIENT_CA_FILE requires TLS_CERT_FILE and TLS_KEY_FILE (mTLS needs a server certificate)")
	}
	if len(c.HTTP.AllowedClientCNs) > 0 && c.HTTP.ClientCAFile == "" {
		bad("config: TLS_ALLOWED_CLIENT_CNS requires TLS_CLIENT_CA_FILE")
	}

	if c.NATS.URL == "" {
		bad("config: NATS_URL must not be empty")
	}
	if (c.NATS.CertFile == "") != (c.NATS.KeyFile == "") {
		bad("config: NATS_CERT_FILE and NATS_KEY_FILE must be set together")
	}

	if c.Stream.Name == "" {
		bad("config: STREAM_NAME must not be empty")
	}
	if c.Stream.SubjectPrefix == "" {
		bad("config: STREAM_SUBJECT_PREFIX must not be empty")
	}
	if c.Stream.Replicas < 1 || c.Stream.Replicas > 5 {
		bad("config: STREAM_REPLICAS must be between 1 and 5")
	}
	if c.Stream.PublishQueueSize < 1 {
		bad("config: PUBLISH_QUEUE_SIZE must be positive")
	}
	if c.Stream.PublishConcurrency < 1 {
		bad("config: PUBLISH_CONCURRENCY must be positive")
	}
	if c.Stream.PublishRetries < 0 {
		bad("config: PUBLISH_RETRIES must not be negative")
	}

	switch c.Idempotency.Backend {
	case BackendNATS, BackendMemory:
	default:
		bad("config: IDEMPOTENCY_BACKEND must be %q or %q (got %q)",
			BackendNATS, BackendMemory, c.Idempotency.Backend)
	}
	if c.Idempotency.TTL <= 0 {
		bad("config: IDEMPOTENCY_TTL must be positive")
	}
	if c.Idempotency.Replicas < 1 || c.Idempotency.Replicas > 5 {
		bad("config: IDEMPOTENCY_REPLICAS must be between 1 and 5")
	}

	return errors.Join(errs...)
}

// EventSubjectFilter is the wildcard subject the events stream binds to.
func (c *Config) EventSubjectFilter() string { return c.Stream.SubjectPrefix + ".>" }

// addr resolves the listen address, preferring an explicit HTTP_ADDR and
// falling back to the PORT variable that Cloud Foundry and Heroku-style
// platforms inject.
func addr(httpAddr, port string) string {
	if httpAddr != "" {
		return httpAddr
	}
	if port != "" {
		return ":" + port
	}
	return ":8080"
}

// envReader reads typed values from the environment, accumulating parse
// errors instead of failing on the first one.
type envReader struct {
	errs []error
}

func (e *envReader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (e *envReader) integer(key string, def int) int {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("config: %s: %q is not an integer", key, raw))
		return def
	}
	return v
}

func (e *envReader) i64(key string, def int64) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("config: %s: %q is not an integer", key, raw))
		return def
	}
	return v
}

func (e *envReader) boolean(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("config: %s: %q is not a boolean", key, raw))
		return def
	}
	return v
}

func (e *envReader) dur(key string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("config: %s: %q is not a duration (e.g. 30s, 5m, 24h)", key, raw))
		return def
	}
	return v
}

// list splits a comma-separated variable, dropping empty entries.
func (e *envReader) list(key string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (e *envReader) level(key string, def slog.Level) slog.Level {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(raw)); err != nil {
		e.errs = append(e.errs, fmt.Errorf("config: %s: %q is not a log level (debug, info, warn, error)", key, raw))
		return def
	}
	return lvl
}
