package binding

import "errors"

// ErrNotFound is returned when a requested service binding does not exist.
var ErrNotFound = errors.New("binding: not found")

// ErrUnsupported is returned by providers that are not yet implemented for
// the current runtime environment.
var ErrUnsupported = errors.New("binding: provider not supported in this runtime")

// Provider retrieves typed service bindings from the current runtime
// environment (Cloud Foundry, Kyma, etc.).
//
// The name argument selects among multiple instances of the same service
// type. Pass an empty string to select the first available instance.
type Provider interface {
	// Connectivity returns the binding for the SAP Connectivity service.
	Connectivity(name string) (*ConnectivityBinding, error)

	// Destination returns the binding for the SAP Destination service.
	Destination(name string) (*DestinationBinding, error)

	// XSUAA returns the binding for the SAP XSUAA service.
	XSUAA(name string) (*XSUAABinding, error)
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
