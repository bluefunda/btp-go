package binding

import "errors"

// ErrNotFound is returned when a requested service binding does not exist.
var ErrNotFound = errors.New("binding: not found")

// ErrUnsupported is returned by providers that are not yet implemented for
// the current runtime environment.
var ErrUnsupported = errors.New("binding: provider not supported in this runtime")

// Provider retrieves raw service binding credentials from the current runtime
// environment (Cloud Foundry, Kyma, etc.).
//
// Binding returns the key/value credential fields for the named instance of
// the given serviceType ("connectivity", "destination", "xsuaa", etc.).
// Pass an empty name to select the first available instance of that type.
//
// Use the typed helper functions (Connectivity, Destination, XSUAA) to
// convert the raw map to a concrete binding struct.
type Provider interface {
	Binding(serviceType, name string) (map[string]string, error)
}

// ConnectivityBinding holds the credentials and proxy endpoints extracted from
// a Cloud Foundry connectivity service instance.
type ConnectivityBinding struct {
	// OnpremiseProxyHost is the hostname of the SOCKS5/HTTP proxy.
	OnpremiseProxyHost string

	// OnpremiseProxySocks5Port is the SOCKS5 proxy port (typically "20004").
	OnpremiseProxySocks5Port string

	// OnpremiseProxyHTTPPort is the HTTP proxy port (typically "20003").
	OnpremiseProxyHTTPPort string

	// ClientID is the OAuth2 client identifier for the connectivity service.
	ClientID string

	// ClientSecret is the OAuth2 client secret.
	ClientSecret string

	// TokenServiceURL is the XSUAA token endpoint for the connectivity service.
	TokenServiceURL string
}

// DestinationBinding holds the credentials for the SAP Destination service.
type DestinationBinding struct {
	// URI is the base URL of the Destination Service REST API.
	URI string

	// ClientID is the OAuth2 client identifier.
	ClientID string

	// ClientSecret is the OAuth2 client secret.
	ClientSecret string

	// TokenServiceURL is the XSUAA token endpoint.
	TokenServiceURL string
}

// XSUAABinding holds the credentials for the SAP XSUAA service.
type XSUAABinding struct {
	// ClientID is the OAuth2 client identifier.
	ClientID string

	// ClientSecret is the OAuth2 client secret.
	ClientSecret string

	// URL is the XSUAA base URL (used to construct the /oauth/token endpoint).
	URL string

	// UAADomain is the UAA domain (e.g. "authentication.eu10.hana.ondemand.com").
	UAADomain string
}

// Connectivity returns the ConnectivityBinding for the named instance.
// Pass an empty name to select the first available connectivity binding.
func Connectivity(p Provider, name string) (*ConnectivityBinding, error) {
	fields, err := p.Binding("connectivity", name)
	if err != nil {
		return nil, err
	}
	return &ConnectivityBinding{
		ClientID:                 fields["clientid"],
		ClientSecret:             fields["clientsecret"],
		TokenServiceURL:          fields["token_service_url"],
		OnpremiseProxyHost:       fields["onpremise_proxy_host"],
		OnpremiseProxySocks5Port: fields["onpremise_socks5_proxy_port"],
		OnpremiseProxyHTTPPort:   fields["onpremise_proxy_http_port"],
	}, nil
}

// Destination returns the DestinationBinding for the named instance.
// Pass an empty name to select the first available destination binding.
func Destination(p Provider, name string) (*DestinationBinding, error) {
	fields, err := p.Binding("destination", name)
	if err != nil {
		return nil, err
	}
	tokenSvcURL := fields["token_service_url"]
	if tokenSvcURL == "" {
		tokenSvcURL = fields["url"]
	}
	return &DestinationBinding{
		ClientID:        fields["clientid"],
		ClientSecret:    fields["clientsecret"],
		URI:             fields["uri"],
		TokenServiceURL: tokenSvcURL,
	}, nil
}

// XSUAA returns the XSUAABinding for the named instance.
// Pass an empty name to select the first available xsuaa binding.
func XSUAA(p Provider, name string) (*XSUAABinding, error) {
	fields, err := p.Binding("xsuaa", name)
	if err != nil {
		return nil, err
	}
	return &XSUAABinding{
		ClientID:     fields["clientid"],
		ClientSecret: fields["clientsecret"],
		URL:          fields["url"],
		UAADomain:    fields["uaadomain"],
	}, nil
}
