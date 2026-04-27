package destination

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// TokenSource is the interface for obtaining a bearer JWT. It is defined
// locally so that the destination module remains stdlib-only. Any concrete
// type that implements Token(ctx) (string, error) satisfies it — including
// xsuaa.NewClientCredentialsSource().
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Client is a client for the SAP BTP Destination Service. It is safe for
// concurrent use.
type Client struct {
	serviceURL string
	tokens     TokenSource
	http       *http.Client
}

// NewClient creates a new Client. serviceURL is the base URL from the
// destination service binding (e.g.
// "https://destination-configuration.cfapps.eu10.hana.ondemand.com").
// When httpClient is nil, http.DefaultClient is used.
func NewClient(serviceURL string, tokens TokenSource, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		serviceURL: strings.TrimRight(serviceURL, "/"),
		tokens:     tokens,
		http:       httpClient,
	}
}

// destEnvelope is the JSON envelope returned by the Destination Service.
type destEnvelope struct {
	DestinationConfiguration map[string]string `json:"destinationConfiguration"`
	AuthTokens               []AuthToken       `json:"authTokens"`
}

// Find resolves a destination by name from the subaccount-level Destination
// Service. It calls tokens.Token(ctx) to obtain a bearer JWT for the API
// request.
func (c *Client) Find(ctx context.Context, name string) (*Destination, error) {
	tok, err := c.tokens.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("destination: token: %w", err)
	}

	endpoint := c.serviceURL +
		"/destination-configuration/v1/destinations/" +
		url.PathEscape(name)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("destination: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("destination: request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("destination: API returned %d: %s", resp.StatusCode, raw)
	}

	var env destEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("destination: decode response: %w", err)
	}

	return extractDestination(env), nil
}

// knownFields lists the destination configuration map keys that are mapped to
// named Destination struct fields. All others land in Properties.
var knownFields = map[string]bool{
	"Name":                     true,
	"Type":                     true,
	"ProxyType":                true,
	"Authentication":           true,
	"URL":                      true,
	"host":                     true,
	"port":                     true,
	"user":                     true,
	"password":                 true,
	"path":                     true,
	"CloudConnectorLocationId": true,
}

// extractDestination maps the raw destinationConfiguration map to a typed
// Destination. Unknown fields are collected into Properties.
func extractDestination(env destEnvelope) *Destination {
	cfg := env.DestinationConfiguration
	d := &Destination{
		Name:                     cfg["Name"],
		Type:                     cfg["Type"],
		ProxyType:                cfg["ProxyType"],
		Authentication:           cfg["Authentication"],
		URL:                      cfg["URL"],
		Host:                     cfg["host"],
		Port:                     cfg["port"],
		User:                     cfg["user"],
		Password:                 cfg["password"],
		Path:                     cfg["path"],
		CloudConnectorLocationID: cfg["CloudConnectorLocationId"],
		AuthTokens:               env.AuthTokens,
		Properties:               make(map[string]string),
	}
	for k, v := range cfg {
		if !knownFields[k] {
			d.Properties[k] = v
		}
	}
	return d
}
