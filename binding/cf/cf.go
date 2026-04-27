// Package cf implements binding.Provider by parsing the VCAP_SERVICES
// environment variable present in Cloud Foundry application instances.
//
// Service instances are looked up by service label ("connectivity",
// "destination", "xsuaa"). When multiple instances of the same label exist,
// pass the instance name to the Provider method to select the correct one;
// pass "" to select the first.
package cf

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/bluefunda/btp-go/binding"
)

// vcapServices is the top-level structure of VCAP_SERVICES JSON.
type vcapServices map[string][]vcapInstance

type vcapInstance struct {
	Name        string          `json:"name"`
	Credentials json.RawMessage `json:"credentials"`
}

// Provider implements binding.Provider for Cloud Foundry VCAP_SERVICES.
type Provider struct {
	svc vcapServices
}

// NewProvider parses VCAP_SERVICES from the environment and returns a Provider.
// Returns an error if the environment variable is absent or malformed.
func NewProvider() (*Provider, error) {
	raw := os.Getenv("VCAP_SERVICES")
	if raw == "" {
		return nil, fmt.Errorf("cf: VCAP_SERVICES is not set")
	}
	var svc vcapServices
	if err := json.Unmarshal([]byte(raw), &svc); err != nil {
		return nil, fmt.Errorf("cf: parse VCAP_SERVICES: %w", err)
	}
	return &Provider{svc: svc}, nil
}

// findCredentials returns the raw credentials JSON for the named service
// instance. If name is "", it returns the first instance under the label.
func (p *Provider) findCredentials(label, name string) (json.RawMessage, error) {
	instances, ok := p.svc[label]
	if !ok || len(instances) == 0 {
		return nil, fmt.Errorf("%w: service %q not found in VCAP_SERVICES", binding.ErrNotFound, label)
	}
	if name == "" {
		return instances[0].Credentials, nil
	}
	for _, inst := range instances {
		if inst.Name == name {
			return inst.Credentials, nil
		}
	}
	return nil, fmt.Errorf("%w: service %q instance %q not found", binding.ErrNotFound, label, name)
}

// Connectivity returns the ConnectivityBinding for the named connectivity
// service instance (pass "" for the first).
func (p *Provider) Connectivity(name string) (*binding.ConnectivityBinding, error) {
	raw, err := p.findCredentials("connectivity", name)
	if err != nil {
		return nil, err
	}
	var creds struct {
		ClientID                string `json:"clientid"`
		ClientSecret            string `json:"clientsecret"`
		TokenServiceURL         string `json:"token_service_url"`
		OnpremiseProxyHost      string `json:"onpremise_proxy_host"`
		OnpremiseSocks5ProxyPort string `json:"onpremise_socks5_proxy_port"`
		OnpremiseProxyHTTPPort  string `json:"onpremise_proxy_http_port"`
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("cf: decode connectivity credentials: %w", err)
	}
	return &binding.ConnectivityBinding{
		ClientID:                 creds.ClientID,
		ClientSecret:             creds.ClientSecret,
		TokenServiceURL:          creds.TokenServiceURL,
		OnpremiseProxyHost:       creds.OnpremiseProxyHost,
		OnpremiseProxySocks5Port: creds.OnpremiseSocks5ProxyPort,
		OnpremiseProxyHTTPPort:   creds.OnpremiseProxyHTTPPort,
	}, nil
}

// Destination returns the DestinationBinding for the named destination
// service instance (pass "" for the first).
func (p *Provider) Destination(name string) (*binding.DestinationBinding, error) {
	raw, err := p.findCredentials("destination", name)
	if err != nil {
		return nil, err
	}
	var creds struct {
		ClientID        string `json:"clientid"`
		ClientSecret    string `json:"clientsecret"`
		URI             string `json:"uri"`
		URL             string `json:"url"`              // token base URL (destination service)
		TokenServiceURL string `json:"token_service_url"` // alternate name used in some regions
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("cf: decode destination credentials: %w", err)
	}
	tokenSvcURL := creds.TokenServiceURL
	if tokenSvcURL == "" {
		tokenSvcURL = creds.URL
	}
	return &binding.DestinationBinding{
		ClientID:        creds.ClientID,
		ClientSecret:    creds.ClientSecret,
		URI:             creds.URI,
		TokenServiceURL: tokenSvcURL,
	}, nil
}

// XSUAA returns the XSUAABinding for the named xsuaa service instance
// (pass "" for the first).
func (p *Provider) XSUAA(name string) (*binding.XSUAABinding, error) {
	raw, err := p.findCredentials("xsuaa", name)
	if err != nil {
		return nil, err
	}
	var creds struct {
		ClientID     string `json:"clientid"`
		ClientSecret string `json:"clientsecret"`
		URL          string `json:"url"`
		UAAdomain    string `json:"uaadomain"`
	}
	if err := json.Unmarshal(raw, &creds); err != nil {
		return nil, fmt.Errorf("cf: decode xsuaa credentials: %w", err)
	}
	return &binding.XSUAABinding{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		URL:          creds.URL,
		UAADomain:    creds.UAAdomain,
	}, nil
}
