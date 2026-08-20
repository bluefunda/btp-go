package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// setEnv applies the given variables for the duration of the test and clears
// every other variable Load reads, so a developer's shell cannot influence
// the result.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, k := range allKeys {
		t.Setenv(k, "")
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

// allKeys is every environment variable Load consults.
var allKeys = []string{
	"SERVICE_NAME", "ENVIRONMENT", "LOG_LEVEL", "SHUTDOWN_TIMEOUT",
	"HTTP_ADDR", "PORT", "HTTP_READ_HEADER_TIMEOUT", "HTTP_READ_TIMEOUT",
	"HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "HTTP_MAX_BODY_BYTES",
	"API_KEYS", "TLS_CERT_FILE", "TLS_KEY_FILE", "TLS_CLIENT_CA_FILE",
	"TLS_ALLOWED_CLIENT_CNS", "ALLOW_ANONYMOUS", "INGEST_DUPLICATE_STATUS",
	"NATS_URL", "NATS_USER", "NATS_PASSWORD", "NATS_TOKEN", "NATS_CREDS_FILE",
	"NATS_NKEY_SEED_FILE", "NATS_CA_FILE", "NATS_CERT_FILE", "NATS_KEY_FILE",
	"NATS_CONNECT_TIMEOUT", "NATS_RECONNECT_WAIT", "NATS_MAX_RECONNECTS",
	"NATS_DRAIN_TIMEOUT", "STREAM_NAME", "STREAM_SUBJECT_PREFIX",
	"STREAM_MAX_AGE", "STREAM_MAX_BYTES", "STREAM_REPLICAS",
	"STREAM_DUPLICATE_WINDOW", "STREAM_MANAGE", "PUBLISH_TIMEOUT",
	"PUBLISH_RETRIES", "PUBLISH_QUEUE_SIZE", "PUBLISH_CONCURRENCY",
	"PUBLISH_SYNC", "IDEMPOTENCY_BACKEND", "IDEMPOTENCY_BUCKET",
	"IDEMPOTENCY_TTL", "IDEMPOTENCY_TIMEOUT", "IDEMPOTENCY_REPLICAS",
}

const testKey = "0123456789abcdef0123"

func TestLoadDefaults(t *testing.T) {
	setEnv(t, map[string]string{"API_KEYS": testKey})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"HTTP.Addr", cfg.HTTP.Addr, ":8080"},
		{"HTTP.DuplicateStatus", cfg.HTTP.DuplicateStatus, 409},
		{"Service.LogLevel", cfg.Service.LogLevel, slog.LevelInfo},
		{"NATS.URL", cfg.NATS.URL, "nats://127.0.0.1:4222"},
		{"NATS.MaxReconnects", cfg.NATS.MaxReconnects, -1},
		{"Stream.Name", cfg.Stream.Name, "EVENTS"},
		{"Stream.SubjectPrefix", cfg.Stream.SubjectPrefix, "events"},
		{"Idempotency.Backend", cfg.Idempotency.Backend, BackendNATS},
		{"Idempotency.TTL", cfg.Idempotency.TTL, 24 * time.Hour},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if got, want := cfg.EventSubjectFilter(), "events.>"; got != want {
		t.Errorf("EventSubjectFilter() = %q, want %q", got, want)
	}
}

// Cloud Foundry and similar platforms inject PORT rather than a full address.
func TestLoadUsesPortWhenAddrUnset(t *testing.T) {
	setEnv(t, map[string]string{"API_KEYS": testKey, "PORT": "9090"})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got, want := cfg.HTTP.Addr, ":9090"; got != want {
		t.Errorf("HTTP.Addr = %q, want %q", got, want)
	}
}

func TestLoadHTTPAddrBeatsPort(t *testing.T) {
	setEnv(t, map[string]string{"API_KEYS": testKey, "PORT": "9090", "HTTP_ADDR": "127.0.0.1:7070"})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if got, want := cfg.HTTP.Addr, "127.0.0.1:7070"; got != want {
		t.Errorf("HTTP.Addr = %q, want %q", got, want)
	}
}

func TestLoadReportsEveryParseErrorAtOnce(t *testing.T) {
	setEnv(t, map[string]string{
		"API_KEYS":         testKey,
		"IDEMPOTENCY_TTL":  "forever",
		"PUBLISH_SYNC":     "perhaps",
		"LOG_LEVEL":        "shouty",
		"STREAM_MAX_BYTES": "lots",
	})

	_, err := Load()
	if err != nil {
		msg := err.Error()
		for _, want := range []string{"IDEMPOTENCY_TTL", "PUBLISH_SYNC", "LOG_LEVEL", "STREAM_MAX_BYTES"} {
			if !strings.Contains(msg, want) {
				t.Errorf("error does not mention %s:\n%s", want, msg)
			}
		}
		return
	}
	t.Fatal("Load() = nil, want parse errors")
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "no authentication configured",
			env:     map[string]string{},
			wantErr: "no authentication configured",
		},
		{
			name:    "anonymous is allowed when explicit",
			env:     map[string]string{"ALLOW_ANONYMOUS": "true"},
			wantErr: "",
		},
		{
			name:    "short api key",
			env:     map[string]string{"API_KEYS": "short"},
			wantErr: "at least 16 characters",
		},
		{
			name:    "unknown idempotency backend",
			env:     map[string]string{"API_KEYS": testKey, "IDEMPOTENCY_BACKEND": "redis"},
			wantErr: "IDEMPOTENCY_BACKEND",
		},
		{
			name:    "unsupported duplicate status",
			env:     map[string]string{"API_KEYS": testKey, "INGEST_DUPLICATE_STATUS": "418"},
			wantErr: "INGEST_DUPLICATE_STATUS",
		},
		{
			name:    "client ca without a server certificate",
			env:     map[string]string{"API_KEYS": testKey, "TLS_CLIENT_CA_FILE": "/ca.pem"},
			wantErr: "requires TLS_CERT_FILE",
		},
		{
			name:    "half a tls keypair",
			env:     map[string]string{"API_KEYS": testKey, "TLS_CERT_FILE": "/c.pem"},
			wantErr: "must be set together",
		},
		{
			name:    "replica count out of range",
			env:     map[string]string{"API_KEYS": testKey, "STREAM_REPLICAS": "9"},
			wantErr: "STREAM_REPLICAS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.env)
			_, err := Load()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Load() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Load() = nil, want an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Load() = %v, want an error containing %q", err, tt.wantErr)
			}
		})
	}
}
