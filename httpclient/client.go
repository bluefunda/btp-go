package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"time"

	"github.com/bluefunda/btp-go/connectivity"
	"github.com/bluefunda/btp-go/destination"
)

// ErrNoURL indicates the destination has no usable URL field.
var ErrNoURL = errors.New("httpclient: destination has no URL")

// Config tunes the client returned by New.
type Config struct {
	// Dialer is the SAP Cloud Connector connectivity dialer. When non-nil
	// AND the destination's ProxyType is "OnPremise", the client's
	// transport routes every TCP open through the connectivity tunnel.
	// Pass nil for Internet destinations or when running in a Kyma-style
	// environment where a Transparent Proxy provides an in-cluster Service
	// hostname (the destination's URL itself resolves to that Service).
	Dialer *connectivity.Dialer

	// TLSConfig optionally overrides the default TLS configuration used
	// by the underlying http.Transport. The zero value uses Go defaults
	// (system root CAs, hostname verification on).
	TLSConfig *tls.Config

	// Timeout is the per-request timeout assigned to the returned
	// *http.Client. Zero defaults to 30s. Set explicitly to disable
	// (use http.Client.Timeout = 0).
	Timeout time.Duration

	// DisableCookieJar suppresses the default cookie jar. Set when the
	// caller manages cookies elsewhere or doesn't want session affinity.
	DisableCookieJar bool

	// ExtraHeaders are applied to every outgoing request after the
	// destination's auth headers. Useful for static headers like
	// "Accept: application/json" or "x-api-key" overrides.
	ExtraHeaders http.Header
}

// New returns an *http.Client and the destination's base URL configured
// according to dest + cfg. Per-request paths should be appended to the
// returned base URL by the caller.
//
// dest.AuthTokens (the per-token HTTPHeader prepared by the BTP Destination
// Service) is attached to outgoing requests via a wrapping RoundTripper.
// When dest has no AuthTokens, the client sends no auth header.
func New(dest *destination.Destination, cfg Config) (*http.Client, string, error) {
	if dest == nil {
		return nil, "", errors.New("httpclient: nil destination")
	}
	if dest.URL == "" {
		return nil, "", fmt.Errorf("%w: %q", ErrNoURL, dest.Name)
	}

	transport := newTransport(dest, cfg)
	rt := http.RoundTripper(transport)
	if hdr := buildAuthHeader(dest); hdr != nil {
		rt = &authTransport{base: rt, header: hdr}
	}
	if len(cfg.ExtraHeaders) > 0 {
		rt = &headerTransport{base: rt, header: cfg.ExtraHeaders.Clone()}
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	client := &http.Client{
		Transport: rt,
		Timeout:   timeout,
	}
	if !cfg.DisableCookieJar {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, "", fmt.Errorf("httpclient: cookie jar: %w", err)
		}
		client.Jar = jar
	}

	return client, strings.TrimRight(dest.URL, "/"), nil
}

// FetchCSRF performs a GET against fetchURL with X-CSRF-Token: Fetch and
// returns the X-CSRF-Token header from the response, suitable for use in
// subsequent OData v2 write requests on the same client (which carries
// any set-cookie session state via its jar).
//
// fetchURL is typically the destination's service-document URL, e.g.
// "<base>/sap/opu/odata/sap/MY_SERVICE/" — many SAP OData services accept
// a Fetch request against any safe URL on the service path.
func FetchCSRF(ctx context.Context, client *http.Client, fetchURL string) (string, error) {
	if client == nil {
		return "", errors.New("httpclient: nil client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-CSRF-Token", "Fetch")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	tok := resp.Header.Get("X-CSRF-Token")
	if tok == "" {
		return "", fmt.Errorf("httpclient: no X-CSRF-Token in response (status %d)", resp.StatusCode)
	}
	return tok, nil
}

// newTransport returns an *http.Transport whose DialContext is wired to
// the BTP connectivity dialer when the destination is OnPremise and a
// dialer is provided. Otherwise it returns a clone of http.DefaultTransport
// with the optional TLSConfig applied.
func newTransport(dest *destination.Destination, cfg Config) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.TLSConfig != nil {
		base.TLSClientConfig = cfg.TLSConfig
	}
	if cfg.Dialer != nil && strings.EqualFold(dest.ProxyType, "OnPremise") {
		dialer := cfg.Dialer
		locID := dest.CloudConnectorLocationID
		base.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, portStr, err := net.SplitHostPort(address)
			if err != nil {
				return nil, fmt.Errorf("httpclient: parse %q: %w", address, err)
			}
			portNum, err := strconv.ParseUint(portStr, 10, 16)
			if err != nil {
				return nil, fmt.Errorf("httpclient: parse port %q: %w", portStr, err)
			}
			return dialer.Dial(ctx, host, uint16(portNum), locID)
		}
	}
	return base
}

// buildAuthHeader picks the first usable AuthToken's HTTPHeader. Returns
// nil if no AuthTokens are present or all are error states.
func buildAuthHeader(dest *destination.Destination) http.Header {
	for _, t := range dest.AuthTokens {
		if t.Error != "" {
			continue
		}
		if t.HTTPHeader.Key != "" && t.HTTPHeader.Value != "" {
			h := http.Header{}
			h.Set(t.HTTPHeader.Key, t.HTTPHeader.Value)
			return h
		}
		// Fall back to constructing "Authorization: <Type> <Value>" if
		// the destination service didn't pre-build the HTTPHeader.
		if t.Type != "" && t.Value != "" {
			h := http.Header{}
			h.Set("Authorization", t.Type+" "+t.Value)
			return h
		}
	}
	return nil
}

// authTransport injects auth headers on every outgoing request.
type authTransport struct {
	base   http.RoundTripper
	header http.Header
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = cloneReq(req)
	for k, vs := range t.header {
		if req.Header.Get(k) != "" {
			continue // caller already set it
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(req)
}

// headerTransport applies an arbitrary header set on every request.
type headerTransport struct {
	base   http.RoundTripper
	header http.Header
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = cloneReq(req)
	for k, vs := range t.header {
		if req.Header.Get(k) != "" {
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return t.base.RoundTrip(req)
}

func cloneReq(req *http.Request) *http.Request {
	r2 := *req
	r2.Header = req.Header.Clone()
	if r2.Header == nil {
		r2.Header = http.Header{}
	}
	return &r2
}
