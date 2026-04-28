package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bluefunda/btp-go/destination"
)

// fakeDest builds a minimal *destination.Destination with the supplied URL,
// proxy type, and one Bearer auth token (or none if token is empty).
func fakeDest(url, proxyType, token string) *destination.Destination {
	d := &destination.Destination{
		Name:      "fake",
		Type:      "HTTP",
		ProxyType: proxyType,
		URL:       url,
	}
	if token != "" {
		d.AuthTokens = []destination.AuthToken{{
			Type:  "Bearer",
			Value: token,
			HTTPHeader: struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			}{
				Key:   "Authorization",
				Value: "Bearer " + token,
			},
		}}
	}
	return d
}

func TestNew_RejectsNilDestination(t *testing.T) {
	_, _, err := New(nil, Config{})
	if err == nil {
		t.Fatal("expected error for nil dest")
	}
}

func TestNew_RejectsEmptyURL(t *testing.T) {
	d := &destination.Destination{Name: "x"}
	_, _, err := New(d, Config{})
	if !errors.Is(err, ErrNoURL) {
		t.Fatalf("expected ErrNoURL, got %v", err)
	}
}

func TestNew_AttachesAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, base, err := New(fakeDest(srv.URL, "Internet", "tok-abc"), Config{})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(base + "/whatever")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got, want := gotAuth, "Bearer tok-abc"; got != want {
		t.Errorf("Authorization: got %q, want %q", got, want)
	}
}

func TestNew_NoAuthWhenAuthTokensEmpty(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	client, base, _ := New(fakeDest(srv.URL, "Internet", ""), Config{})
	resp, _ := client.Get(base + "/")
	resp.Body.Close()

	if gotAuth != "" {
		t.Errorf("expected no Authorization, got %q", gotAuth)
	}
}

func TestNew_DoesNotOverrideCallerProvidedAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
	}))
	defer srv.Close()

	client, base, _ := New(fakeDest(srv.URL, "Internet", "from-dest"), Config{})
	req, _ := http.NewRequest("GET", base+"/", nil)
	req.Header.Set("Authorization", "Bearer from-caller")
	resp, _ := client.Do(req)
	resp.Body.Close()

	if gotAuth != "Bearer from-caller" {
		t.Errorf("caller auth not preserved: got %q", gotAuth)
	}
}

func TestNew_ExtraHeadersApplied(t *testing.T) {
	var gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
	}))
	defer srv.Close()

	client, base, _ := New(fakeDest(srv.URL, "Internet", ""), Config{
		ExtraHeaders: http.Header{"Accept": []string{"application/json"}},
	})
	resp, _ := client.Get(base + "/")
	resp.Body.Close()

	if gotAccept != "application/json" {
		t.Errorf("Accept: got %q, want application/json", gotAccept)
	}
}

func TestNew_CookieJarEnabledByDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// First request: set cookie. Second: echo it back.
		if c, _ := r.Cookie("session"); c != nil {
			w.Header().Set("X-Echo-Cookie", c.Value)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "s1"})
	}))
	defer srv.Close()

	client, base, _ := New(fakeDest(srv.URL, "Internet", ""), Config{})
	resp, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	resp2, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if got := resp2.Header.Get("X-Echo-Cookie"); got != "s1" {
		t.Errorf("cookie not retained across requests: got %q", got)
	}
}

func TestNew_CookieJarDisabled(t *testing.T) {
	client, _, _ := New(fakeDest("https://example", "Internet", ""), Config{DisableCookieJar: true})
	if client.Jar != nil {
		t.Error("expected nil cookie jar when disabled")
	}
}

func TestNew_TimeoutDefault(t *testing.T) {
	client, _, _ := New(fakeDest("https://example", "Internet", ""), Config{})
	if client.Timeout != 30*time.Second {
		t.Errorf("default timeout: got %v, want 30s", client.Timeout)
	}
}

func TestNew_TimeoutOverride(t *testing.T) {
	client, _, _ := New(fakeDest("https://example", "Internet", ""), Config{Timeout: 5 * time.Second})
	if client.Timeout != 5*time.Second {
		t.Errorf("timeout: got %v, want 5s", client.Timeout)
	}
}

func TestNew_TrailingSlashStrippedFromBaseURL(t *testing.T) {
	_, base, _ := New(fakeDest("https://example.com/api/", "Internet", ""), Config{})
	if !strings.HasSuffix(base, "/api") || strings.HasSuffix(base, "//") {
		t.Errorf("trailing slash not stripped: %q", base)
	}
}

func TestFetchCSRF(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CSRF-Token") == "Fetch" {
			w.Header().Set("X-CSRF-Token", "csrf-xyz")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	client, base, _ := New(fakeDest(srv.URL, "Internet", "tok"), Config{})
	tok, err := FetchCSRF(context.Background(), client, base+"/")
	if err != nil {
		t.Fatal(err)
	}
	if tok != "csrf-xyz" {
		t.Errorf("got token %q, want csrf-xyz", tok)
	}
}

func TestFetchCSRF_NoTokenInResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client, base, _ := New(fakeDest(srv.URL, "Internet", ""), Config{})
	_, err := FetchCSRF(context.Background(), client, base+"/")
	if err == nil || !strings.Contains(err.Error(), "no X-CSRF-Token") {
		t.Errorf("expected 'no X-CSRF-Token' error, got %v", err)
	}
}

func TestFetchCSRF_NilClient(t *testing.T) {
	_, err := FetchCSRF(context.Background(), nil, "https://x")
	if err == nil {
		t.Fatal("expected error for nil client")
	}
}

// fakeBody is a minimal request body used to confirm Body is preserved
// when our middleware clones the request.
type fakeBody struct{ s string }

func (f *fakeBody) Read(p []byte) (int, error) { return strings.NewReader(f.s).Read(p) }

func TestNew_PreservesRequestBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != "payload" {
			t.Errorf("server got body %q, want %q", body, "payload")
		}
	}))
	defer srv.Close()

	client, base, _ := New(fakeDest(srv.URL, "Internet", "tok"), Config{})
	req, _ := http.NewRequest("POST", base+"/echo", strings.NewReader("payload"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}
