package xsuaa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tokenResponse builds the JSON body returned by the fake XSUAA server.
func tokenResponse(token string, expiresIn int) []byte {
	b, _ := json.Marshal(map[string]interface{}{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   expiresIn,
	})
	return b
}

func TestToken_SuccessfulFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.FormValue("grant_type") != "client_credentials" {
			t.Errorf("expected grant_type=client_credentials, got %q", r.FormValue("grant_type"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(tokenResponse("tok-abc", 3600))
	}))
	defer srv.Close()

	src := NewClientCredentialsSource(Config{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     srv.URL + "/oauth/token",
	})

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("Token() error: %v", err)
	}
	if tok != "tok-abc" {
		t.Errorf("Token() = %q, want %q", tok, "tok-abc")
	}
}

func TestToken_CachingServerCalledOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write(tokenResponse("cached-token", 3600))
	}))
	defer srv.Close()

	src := NewClientCredentialsSource(Config{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     srv.URL + "/oauth/token",
	})

	tok1, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token() error: %v", err)
	}
	tok2, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token() error: %v", err)
	}
	if tok1 != tok2 {
		t.Errorf("expected same token from cache: %q vs %q", tok1, tok2)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected 1 server call, got %d", n)
	}
}

func TestToken_ExpiredTokenTriggersRefetch(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			// Very short expiry (1s) minus skew(60s) = already expired immediately.
			w.Write(tokenResponse("first-token", 1))
		} else {
			w.Write(tokenResponse("second-token", 3600))
		}
	}))
	defer srv.Close()

	src := NewClientCredentialsSource(Config{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     srv.URL + "/oauth/token",
		Skew:         2 * time.Second, // larger than expires_in so token is immediately stale
	})

	tok1, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token() error: %v", err)
	}

	// Token should be considered expired (expiresAt is in the past).
	tok2, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token() error: %v", err)
	}

	if tok1 == tok2 {
		t.Errorf("expected different tokens after expiry, both = %q", tok1)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("expected 2 server calls, got %d", n)
	}
}

func TestToken_ConcurrentCallsCoalesce(t *testing.T) {
	var calls atomic.Int32
	unblock := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		<-unblock // hold until we release all goroutines
		w.Header().Set("Content-Type", "application/json")
		w.Write(tokenResponse("coalesced-token", 3600))
	}))
	defer srv.Close()

	src := NewClientCredentialsSource(Config{
		ClientID:     "id",
		ClientSecret: "secret",
		TokenURL:     srv.URL + "/oauth/token",
	})

	const numGoroutines = 10
	var wg sync.WaitGroup
	tokens := make([]string, numGoroutines)
	errs := make([]error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tokens[idx], errs[idx] = src.Token(context.Background())
		}(i)
	}

	// Give goroutines time to queue up, then unblock the server.
	time.Sleep(50 * time.Millisecond)
	close(unblock)

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d Token() error: %v", i, err)
		}
		if tokens[i] != "coalesced-token" {
			t.Errorf("goroutine %d token = %q, want %q", i, tokens[i], "coalesced-token")
		}
	}

	// The server should have been called exactly once (singleflight coalescing).
	if n := calls.Load(); n != 1 {
		t.Errorf("expected 1 server call due to coalescing, got %d", n)
	}
}
