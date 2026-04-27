package xsuaa

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config holds the parameters for obtaining client-credentials tokens from
// XSUAA.
type Config struct {
	// ClientID is the OAuth2 client identifier.
	ClientID string

	// ClientSecret is the OAuth2 client secret.
	ClientSecret string

	// TokenURL is the full URL of the token endpoint, typically
	// <xsuaa-url>/oauth/token.
	TokenURL string

	// HTTPClient is the HTTP client used for token requests. When nil,
	// http.DefaultClient is used.
	HTTPClient *http.Client

	// Skew is how far before the token expires a refresh is triggered.
	// Defaults to 60 seconds when zero.
	Skew time.Duration
}

// tokenEntry holds a cached token value and its expiry time.
type tokenEntry struct {
	value     string
	expiresAt time.Time
}

// clientCredentialsSource implements TokenSource with in-memory caching and
// a hand-rolled singleflight so concurrent callers during a refresh share one
// HTTP request.
type clientCredentialsSource struct {
	cfg Config

	mu       sync.Mutex
	cached   *tokenEntry
	inflight bool
	waiters  []chan struct{}
	// result and err hold the outcome of the most recent in-flight fetch,
	// broadcast to all waiters once complete.
	result tokenEntry
	err    error
}

// NewClientCredentialsSource returns a TokenSource that fetches and caches a
// client-credentials token from cfg.TokenURL. Concurrent calls to Token share
// a single in-flight HTTP request.
func NewClientCredentialsSource(cfg Config) TokenSource {
	if cfg.Skew == 0 {
		cfg.Skew = 60 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	return &clientCredentialsSource{cfg: cfg}
}

// Token returns a valid bearer token, fetching a new one from XSUAA if the
// cached value has expired or is absent.
func (s *clientCredentialsSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()

	// Cache hit: return immediately.
	if s.cached != nil && time.Now().Before(s.cached.expiresAt) {
		val := s.cached.value
		s.mu.Unlock()
		return val, nil
	}

	// Another goroutine is already fetching: subscribe to its result.
	if s.inflight {
		ch := make(chan struct{}, 1)
		s.waiters = append(s.waiters, ch)
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ch:
		}

		s.mu.Lock()
		result, err := s.result, s.err
		s.mu.Unlock()
		if err != nil {
			return "", err
		}
		return result.value, nil
	}

	// We are the leader: set inflight and fetch outside the lock.
	s.inflight = true
	s.mu.Unlock()

	entry, err := s.fetch(ctx)

	s.mu.Lock()
	s.inflight = false
	if err == nil {
		s.cached = &entry
		s.result = entry
		s.err = nil
	} else {
		s.cached = nil
		s.result = tokenEntry{}
		s.err = err
	}
	waiters := s.waiters
	s.waiters = nil
	s.mu.Unlock()

	// Wake all waiters.
	for _, ch := range waiters {
		close(ch)
	}

	if err != nil {
		return "", err
	}
	return entry.value, nil
}

// fetch performs the HTTP client-credentials grant and returns the token entry.
func (s *clientCredentialsSource) fetch(ctx context.Context) (tokenEntry, error) {
	body := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.TokenURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return tokenEntry{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s.cfg.ClientID, s.cfg.ClientSecret)

	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return tokenEntry{}, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return tokenEntry{}, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, raw)
	}

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return tokenEntry{}, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return tokenEntry{}, fmt.Errorf("token response missing access_token")
	}

	expiry := time.Now().Add(time.Duration(tr.ExpiresIn)*time.Second - s.cfg.Skew)
	return tokenEntry{value: tr.AccessToken, expiresAt: expiry}, nil
}
