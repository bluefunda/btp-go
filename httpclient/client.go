package httpclient

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bluefunda/btp-go/binding"
	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/xsuaa"
)

// ErrNoURL indicates the destination has no usable URL field.
var ErrNoURL = errors.New("httpclient: destination has no URL")

// TokenSource mints XSUAA bearer JWTs for proxy auth. *xsuaa.clientCredentialsSource
// satisfies this interface via its Token(ctx) method.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Dialer establishes a proxied TCP connection through the SAP BTP
// Connectivity Service SOCKS5 tunnel. *connectivity.Dialer satisfies this
// interface automatically.
type Dialer interface {
	Dial(ctx context.Context, host string, port uint16, locationID string) (net.Conn, error)
}

// HTTPProxyConfig routes the client's transport through the SAP BTP
// Connectivity Service's HTTP CONNECT proxy (typically
// connectivityproxy.internal:20003). The Cloud Connector matches HTTP rules
// against requests received via this path; SOCKS5 traffic on the same proxy
// matches TCP rules instead. Use this for HTTP/HTTPS destinations whose SCC
// row is configured Protocol=HTTP.
type HTTPProxyConfig struct {
	// Binding is the resolved connectivity service binding. The proxy host,
	// port, and XSUAA credentials are all read from here.
	Binding *binding.ConnectivityBinding

	// TokenSource overrides the XSUAA token source derived from Binding.
	// Leave nil in production; set to a stub in tests.
	TokenSource TokenSource
}

// Config tunes the client returned by New.
type Config struct {
	// Dialer is the SAP Cloud Connector connectivity dialer (SOCKS5).
	// When non-nil AND the destination's ProxyType is "OnPremise", the
	// client's transport routes every TCP open through the connectivity
	// tunnel as raw TCP — matching SCC TCP rules.
	//
	// For HTTP destinations whose SCC row is Protocol=HTTP, prefer the
	// HTTPProxy field below — when both are set, HTTPProxy wins.
	// *connectivity.Dialer satisfies this interface automatically.
	Dialer Dialer

	// HTTPProxy routes via the connectivity proxy's HTTP CONNECT mode
	// instead of SOCKS5. Use for HTTP destinations on CF where SCC has
	// HTTP-protocol rules. When non-nil, Dialer is ignored.
	HTTPProxy *HTTPProxyConfig

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

	// Resolve the connectivity proxy token source once so both the per-request
	// Proxy-Authorization header and the HTTPS CONNECT preamble share a cache.
	var connTokenSrc TokenSource
	if cfg.HTTPProxy != nil {
		if cfg.HTTPProxy.Binding == nil {
			return nil, "", errors.New("httpclient: HTTPProxyConfig.Binding is required")
		}
		connTokenSrc = cfg.HTTPProxy.TokenSource
		if connTokenSrc == nil {
			connTokenSrc = xsuaa.NewClientCredentialsSource(xsuaa.Config{
				ClientID:     cfg.HTTPProxy.Binding.ClientID,
				ClientSecret: cfg.HTTPProxy.Binding.ClientSecret,
				TokenURL:     cfg.HTTPProxy.Binding.TokenServiceURL + "/oauth/token",
			})
		}
	}

	transport := newTransport(dest, cfg, connTokenSrc)
	rt := http.RoundTripper(transport)
	// Inject Proxy-Authorization (and SAP-Connectivity-SCC-Location_ID when
	// the destination carries a CloudConnectorLocationId) on every outgoing
	// request when routing through the HTTP CONNECT proxy.
	if connTokenSrc != nil {
		rt = &proxyAuthTransport{
			base:        rt,
			tokenSource: connTokenSrc,
			locationID:  dest.CloudConnectorLocationID,
		}
	}
	if hdr := buildAuthHeader(dest); hdr != nil {
		rt = &headerTransport{base: rt, header: hdr}
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

// newTransport returns an *http.Transport configured per cfg's routing mode:
//
//   - HTTPProxy != nil → Transport.Proxy points at the connectivity HTTP
//     CONNECT proxy. TLS handshake (for HTTPS targets) happens after CONNECT
//     succeeds. Auth headers (Proxy-Authorization, optional SCC Location_ID)
//     are added by the wrapping proxyAuthTransport for HTTP targets, and
//     by ProxyConnectHeader on the CONNECT preamble for HTTPS targets.
//
//   - Dialer != nil and ProxyType=OnPremise → DialContext routes raw TCP
//     through the SOCKS5 connectivity dialer (matches SCC TCP rules).
//
//   - Otherwise → plain transport.
func newTransport(dest *destination.Destination, cfg Config, connTokenSrc TokenSource) *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.TLSConfig != nil {
		base.TLSClientConfig = cfg.TLSConfig
	}
	if cfg.HTTPProxy != nil {
		b := cfg.HTTPProxy.Binding
		proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(b.OnpremiseProxyHost, b.OnpremiseProxyHTTPPort)}
		base.Proxy = http.ProxyURL(proxyURL)
		// For HTTPS targets, http.Transport sends a CONNECT preamble; the
		// Proxy-Authorization header must accompany that CONNECT (the
		// regular request headers don't reach the proxy on HTTPS).
		if connTokenSrc != nil {
			locID := dest.CloudConnectorLocationID
			base.GetProxyConnectHeader = func(ctx context.Context, _ *url.URL, _ string) (http.Header, error) {
				tok, err := connTokenSrc.Token(ctx)
				if err != nil {
					return nil, err
				}
				h := http.Header{}
				h.Set("Proxy-Authorization", "Bearer "+tok)
				if locID != "" {
					h.Set("SAP-Connectivity-SCC-Location_ID", locID)
				}
				return h, nil
			}
		}
		return base
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

// buildAuthHeader derives the Authorization header for dest. Priority:
//  1. First error-free AuthToken (OAuth/SAML tokens returned by the Destination Service).
//  2. BasicAuthentication via the User/Password fields on the destination config.
//
// Returns nil when neither source is present; the caller's own header is left intact.
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
		if t.Type != "" && t.Value != "" {
			h := http.Header{}
			h.Set("Authorization", t.Type+" "+t.Value)
			return h
		}
	}
	// BasicAuthentication: user/password fields from the destination config.
	// Properties["User"] / Properties["Password"] cover the capitalised keys
	// that SAP services sometimes use alongside the lowercase canonical fields.
	user := firstNonEmpty(dest.User, dest.Properties["User"])
	if user != "" {
		pass := firstNonEmpty(dest.Password, dest.Properties["Password"])
		h := http.Header{}
		h.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
		return h
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// proxyAuthTransport injects Proxy-Authorization (and optional
// SAP-Connectivity-SCC-Location_ID) on every outgoing request — required
// for HTTP targets routed via the connectivity HTTP CONNECT proxy. For
// HTTPS targets the headers ride the CONNECT preamble via Transport's
// GetProxyConnectHeader hook (see newTransport).
type proxyAuthTransport struct {
	base        http.RoundTripper
	tokenSource TokenSource
	locationID  string
}

func (t *proxyAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tok, err := t.tokenSource.Token(req.Context())
	if err != nil {
		return nil, fmt.Errorf("httpclient: proxy token: %w", err)
	}
	req = req.Clone(req.Context())
	req.Header.Set("Proxy-Authorization", "Bearer "+tok)
	if t.locationID != "" {
		req.Header.Set("SAP-Connectivity-SCC-Location_ID", t.locationID)
	}
	return t.base.RoundTrip(req)
}

// headerTransport applies a set of default headers to every outgoing request,
// skipping any key the caller has already set.
type headerTransport struct {
	base   http.RoundTripper
	header http.Header
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
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
