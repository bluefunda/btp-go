package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/bluefunda/btp-go/ingest"
)

// Header names the gateway reads and writes on every request.
const (
	HeaderAPIKey  = "X-API-Key"
	HeaderTraceID = "X-Trace-Id"
	// HeaderRequestID is accepted as an inbound alias for HeaderTraceID,
	// because SAP middleware and API gateways commonly set it instead.
	HeaderRequestID = "X-Request-Id"
)

type ctxKey int

const traceIDKey ctxKey = iota

// TraceIDFrom returns the trace ID carried on ctx, or "" if the request did
// not pass through the trace middleware.
func TraceIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(traceIDKey).(string)
	return id
}

// middleware is a handler decorator.
type middleware func(http.Handler) http.Handler

// chain applies decorators so that the first listed runs outermost.
func chain(h http.Handler, mw ...middleware) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// traceID gives every request a correlation ID, reusing a caller-supplied
// one when it looks sane.
//
// An inbound value is only honoured if it is short and alphanumeric.
// Otherwise it would be echoed into a response header and every log line
// from an unauthenticated request — a free header-injection and log-forging
// primitive.
func traceID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(HeaderTraceID)
		if id == "" {
			id = r.Header.Get(HeaderRequestID)
		}
		if !isSafeTraceID(id) {
			id = ingest.NewTraceID()
		}
		w.Header().Set(HeaderTraceID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), traceIDKey, id)))
	})
}

func isSafeTraceID(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// requestLogger emits one structured line per request.
func requestLogger(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			attrs := []any{
				"trace_id", TraceIDFrom(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.written,
				"duration_ms", time.Since(start).Milliseconds(),
				"remote", clientIP(r),
			}
			switch {
			case isProbe(r.URL.Path):
				// Probes fire every few seconds forever; at info level they
				// would drown out everything worth reading.
				log.Debug("request", attrs...)
			case rec.status >= 500:
				log.Error("request", attrs...)
			case rec.status >= 400:
				log.Warn("request", attrs...)
			default:
				log.Info("request", attrs...)
			}
		})
	}
}

func isProbe(path string) bool { return path == "/healthz" || path == "/readyz" }

// recoverer turns a panic into a 500 instead of a dropped connection, and
// keeps the rest of the process running.
func recoverer(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// A panic after the client hangs up is noise, not a bug.
				if errors.Is(r.Context().Err(), context.Canceled) {
					return
				}
				log.Error("panic recovered",
					"trace_id", TraceIDFrom(r.Context()),
					"method", r.Method,
					"path", r.URL.Path,
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				writeJSON(w, http.StatusInternalServerError, ErrorResponse{
					TraceID: TraceIDFrom(r.Context()),
					Code:    CodeInternal,
					Error:   "internal error",
				})
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// AuthOptions configures request authentication.
type AuthOptions struct {
	// APIKeys are the accepted credential values, presented either as
	// X-API-Key or as an Authorization bearer token.
	APIKeys []string

	// RequireClientCert demands a verified TLS client certificate. The TLS
	// handshake does the verification; this makes the handler refuse a
	// request that somehow arrived without one.
	RequireClientCert bool

	// AllowedClientCNs optionally restricts which certificate subjects are
	// accepted. Empty means any certificate the CA signed.
	AllowedClientCNs []string

	// AllowAnonymous disables authentication entirely. It has to be set
	// deliberately — an unauthenticated ingestion endpoint is a way to
	// write straight into someone's document archive.
	AllowAnonymous bool
}

type authenticator struct {
	keyDigests [][32]byte
	opts       AuthOptions
}

func newAuthenticator(opts AuthOptions) (*authenticator, error) {
	if len(opts.APIKeys) == 0 && !opts.RequireClientCert && !opts.AllowAnonymous {
		return nil, errors.New("api: no authentication configured; set APIKeys, RequireClientCert, or AllowAnonymous")
	}
	a := &authenticator{opts: opts}
	for _, k := range opts.APIKeys {
		// Hashing first means the comparison is fixed-width, so it leaks
		// neither the key length nor its first differing byte.
		a.keyDigests = append(a.keyDigests, sha256.Sum256([]byte(k)))
	}
	return a, nil
}

// middleware enforces the configured credentials.
//
// When both an API key and mTLS are configured, both are required. Layered
// controls that silently degrade to whichever is weakest are not controls.
func (a *authenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.opts.AllowAnonymous {
			next.ServeHTTP(w, r)
			return
		}

		if a.opts.RequireClientCert {
			if err := a.checkClientCert(r); err != nil {
				a.deny(w, r, err.Error())
				return
			}
		}

		if len(a.keyDigests) > 0 && !a.checkAPIKey(r) {
			a.deny(w, r, "missing or invalid API key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (a *authenticator) checkClientCert(r *http.Request) error {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return errors.New("client certificate required")
	}
	if len(a.opts.AllowedClientCNs) == 0 {
		return nil
	}
	cn := r.TLS.PeerCertificates[0].Subject.CommonName
	if slices.Contains(a.opts.AllowedClientCNs, cn) {
		return nil
	}
	return errors.New("client certificate subject is not allowed")
}

func (a *authenticator) checkAPIKey(r *http.Request) bool {
	presented := r.Header.Get(HeaderAPIKey)
	if presented == "" {
		if v, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			presented = strings.TrimSpace(v)
		}
	}
	if presented == "" {
		return false
	}

	got := sha256.Sum256([]byte(presented))
	// Every configured key is compared, without an early exit, so the time
	// taken does not reveal which key matched or how many are configured.
	matched := 0
	for _, want := range a.keyDigests {
		matched |= subtle.ConstantTimeCompare(got[:], want[:])
	}
	return matched == 1
}

func (a *authenticator) deny(w http.ResponseWriter, r *http.Request, reason string) {
	if len(a.keyDigests) > 0 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ingest-gateway"`)
	}
	writeJSON(w, http.StatusUnauthorized, ErrorResponse{
		TraceID: TraceIDFrom(r.Context()),
		Code:    CodeUnauthorized,
		Error:   reason,
	})
}

// statusRecorder captures the response status and size for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written int
	wrote   bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.written += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// readBody reads at most maxBytes, returning *http.MaxBytesError when the
// caller sent more.
func readBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	return io.ReadAll(r.Body)
}

// isJSONContentType accepts application/json and any +json suffix, ignoring
// parameters such as charset.
func isJSONContentType(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// clientIP prefers the left-most X-Forwarded-For entry, which is what a
// Cloud Foundry or Kubernetes ingress sets. It is advisory: the header is
// caller-controlled and appears in logs only.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	return r.RemoteAddr
}
