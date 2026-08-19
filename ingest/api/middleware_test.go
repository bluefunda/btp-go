package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthRejectsMissingAndWrongKeys(t *testing.T) {
	tests := []struct {
		name       string
		setHeaders func(*http.Request)
		wantStatus int
	}{
		{
			name:       "no credentials",
			setHeaders: func(*http.Request) {},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong key",
			setHeaders: func(r *http.Request) { r.Header.Set(HeaderAPIKey, "wrong-key-wrong-key") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "empty key header",
			setHeaders: func(r *http.Request) { r.Header.Set(HeaderAPIKey, "") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "key with trailing whitespace",
			setHeaders: func(r *http.Request) { r.Header.Set(HeaderAPIKey, testAPIKey+" ") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "correct key in X-API-Key",
			setHeaders: func(r *http.Request) { r.Header.Set(HeaderAPIKey, testAPIKey) },
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "correct key as a bearer token",
			setHeaders: func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testAPIKey) },
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "bearer prefix is case sensitive per RFC 6750",
			setHeaders: func(r *http.Request) { r.Header.Set("Authorization", "bearer "+testAPIKey) },
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, nil)

			req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
			req.Header.Set("Content-Type", "application/json")
			tt.setHeaders(req)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			if tt.wantStatus == http.StatusUnauthorized {
				if got := decodeError(t, rec).Code; got != CodeUnauthorized {
					t.Errorf("code = %q, want %q", got, CodeUnauthorized)
				}
				if got := rec.Header().Get("WWW-Authenticate"); got == "" {
					t.Error("no WWW-Authenticate header on a 401")
				}
				// An unauthenticated request must not touch the store.
				if reserves, _ := h.store.counts(); reserves != 0 {
					t.Errorf("made %d reservations for an unauthenticated request", reserves)
				}
			}
		})
	}
}

func TestAuthAcceptsAnyConfiguredKey(t *testing.T) {
	const second = "fedcba9876543210fedc"
	h := newHarness(t, func(o *Options) { o.Auth.APIKeys = []string{testAPIKey, second} })

	for _, key := range []string{testAPIKey, second} {
		req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(HeaderAPIKey, key)
		// Vary the business key so the second call is not a duplicate.
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)

		if rec.Code == http.StatusUnauthorized {
			t.Errorf("key %q was rejected", key)
		}
	}
}

// Health probes cannot present credentials, so they sit outside the auth
// chain. A probe that fails closed takes the pod down for the wrong reason.
func TestHealthEndpointsAreUnauthenticated(t *testing.T) {
	h := newHarness(t, nil)

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200: %s", path, rec.Code, rec.Body)
		}
	}
}

func TestReadyzReportsCheckFailures(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Checks = []Check{
			{Name: "nats", Probe: func(context.Context) error {
				return errors.New("not connected: connection is down")
			}},
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body)
	}

	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != HealthDegraded {
		t.Errorf("status = %q, want %q", resp.Status, HealthDegraded)
	}
	if !strings.Contains(resp.Checks["nats"], "down") {
		t.Errorf("checks[nats] = %q, want the failure reason", resp.Checks["nats"])
	}
	// Liveness must stay green: killing the container will not fix NATS.
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 while a dependency is down", rec.Code)
	}
}

// Saturation should drop the instance out of rotation before it has to start
// answering 503 to real traffic.
func TestReadyzReportsSaturation(t *testing.T) {
	h := newHarness(t, nil)
	h.pub.saturated = true

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}

	var resp HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Checks["publish_queue"] != "saturated" {
		t.Errorf("checks[publish_queue] = %q, want %q", resp.Checks["publish_queue"], "saturated")
	}
	if resp.Idempotency != "test" {
		t.Errorf("idempotency = %q, want the store kind", resp.Idempotency)
	}
}

func TestTraceIDIsGeneratedAndPropagated(t *testing.T) {
	h := newHarness(t, nil)

	t.Run("a sane inbound id is reused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(HeaderTraceID, "sap-correlation-42")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)

		if got := rec.Header().Get(HeaderTraceID); got != "sap-correlation-42" {
			t.Errorf("trace id = %q, want it echoed back", got)
		}
	})

	t.Run("X-Request-Id is accepted as an alias", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set(HeaderRequestID, "gateway-req-7")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)

		if got := rec.Header().Get(HeaderTraceID); got != "gateway-req-7" {
			t.Errorf("trace id = %q, want the X-Request-Id value", got)
		}
	})

	// A caller-supplied value is echoed into a header and into every log
	// line, so anything that could forge a log record or split a header is
	// replaced rather than sanitised.
	for _, hostile := range []string{
		"has spaces",
		"newline\r\ninjected",
		"semi;colon",
		strings.Repeat("a", 65),
		"",
	} {
		t.Run("hostile id is replaced: "+hostile, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			req.Header.Set(HeaderTraceID, hostile)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)

			got := rec.Header().Get(HeaderTraceID)
			if got == hostile {
				t.Fatalf("trace id %q was echoed unchanged", hostile)
			}
			if len(got) != 32 {
				t.Errorf("trace id = %q, want a generated 32-character id", got)
			}
		})
	}
}

// A panic must not take the process down or leave the client hanging.
func TestRecovererTurnsPanicsInto500(t *testing.T) {
	log := testLogger()
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}), recoverer(log), traceID)

	req := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code != CodeInternal {
		t.Errorf("code = %q, want %q", resp.Code, CodeInternal)
	}
	// The panic text must not reach the client.
	if strings.Contains(rec.Body.String(), "boom") {
		t.Error("panic detail leaked into the response body")
	}
}

func TestNewHandlerRequiresAuthentication(t *testing.T) {
	_, err := NewHandler(Options{
		Publisher:   &fakePublisher{},
		Idempotency: newCountingStore(),
		Logger:      testLogger(),
	})
	if err == nil {
		t.Fatal("NewHandler() accepted a config with no authentication")
	}
	if !strings.Contains(err.Error(), "authentication") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
}

func TestNewHandlerAllowsExplicitAnonymous(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Auth = AuthOptions{AllowAnonymous: true}
	})

	req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
	}
}

func TestMTLSRequiresAClientCertificate(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Auth = AuthOptions{RequireClientCert: true}
	})

	t.Run("plain http is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})

	t.Run("a presented certificate is accepted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
		req.Header.Set("Content-Type", "application/json")
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
			{Subject: pkix.Name{CommonName: "sap-prd-01"}},
		}}
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body)
		}
	})
}

func TestMTLSCommonNameAllowlist(t *testing.T) {
	newReq := func(cn string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
		req.Header.Set("Content-Type", "application/json")
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
			{Subject: pkix.Name{CommonName: cn}},
		}}
		return req
	}

	h := newHarness(t, func(o *Options) {
		o.Auth = AuthOptions{RequireClientCert: true, AllowedClientCNs: []string{"sap-prd-01"}}
	})

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, newReq("sap-prd-01"))
	if rec.Code != http.StatusAccepted {
		t.Errorf("allowed CN: status = %d, want 202: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.handler.ServeHTTP(rec, newReq("attacker-cert"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("disallowed CN: status = %d, want 401", rec.Code)
	}
}

// Layered controls that degrade to the weakest one are not controls.
func TestMTLSAndAPIKeyAreBothEnforcedWhenBothConfigured(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.Auth = AuthOptions{APIKeys: []string{testAPIKey}, RequireClientCert: true}
	})

	withCert := func(r *http.Request) {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{
			{Subject: pkix.Name{CommonName: "sap-prd-01"}},
		}}
	}

	tests := []struct {
		name       string
		mutate     func(*http.Request)
		wantStatus int
	}{
		{"certificate only", withCert, http.StatusUnauthorized},
		{"key only", func(r *http.Request) { r.Header.Set(HeaderAPIKey, testAPIKey) }, http.StatusUnauthorized},
		{"both", func(r *http.Request) {
			withCert(r)
			r.Header.Set(HeaderAPIKey, testAPIKey)
		}, http.StatusAccepted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(validBody))
			req.Header.Set("Content-Type", "application/json")
			tt.mutate(req)
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
		})
	}
}

func TestIsJSONContentType(t *testing.T) {
	tests := []struct {
		ct   string
		want bool
	}{
		{"application/json", true},
		{"application/json; charset=utf-8", true},
		{"application/problem+json", true},
		{"APPLICATION/JSON", true},
		{"text/plain", false},
		{"application/xml", false},
		{"nonsense", false},
	}
	for _, tt := range tests {
		if got := isJSONContentType(tt.ct); got != tt.want {
			t.Errorf("isJSONContentType(%q) = %v, want %v", tt.ct, got, tt.want)
		}
	}
}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name string
		xff  string
		want string
	}{
		{"no header", "", "192.0.2.1:1234"},
		{"single entry", "203.0.113.9", "203.0.113.9"},
		{"proxy chain uses the left-most entry", "203.0.113.9, 10.0.0.1, 10.0.0.2", "203.0.113.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "192.0.2.1:1234"
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(req); got != tt.want {
				t.Errorf("clientIP() = %q, want %q", got, tt.want)
			}
		})
	}
}
