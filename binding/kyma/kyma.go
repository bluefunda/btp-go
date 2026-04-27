// Package kyma is a placeholder implementation of binding.Provider for SAP
// BTP Kyma (Kubernetes) runtime environments.
//
// Planned implementation (M4):
//
// The Kyma Service Binding operator writes each service binding's credentials
// as files under /bindings/<binding-name>/. The directory layout mirrors the
// Servicebinding.io (https://servicebinding.io/) specification:
//
//	/bindings/
//	└── my-connectivity/
//	    ├── clientid
//	    ├── clientsecret
//	    ├── token_service_url
//	    └── ...
//
// Each file contains the raw value for the corresponding credential key. This
// provider will read those files, determine the service type from the "type"
// file or from a configured mapping, and return the appropriate typed binding.
//
// Until M4 is implemented, all methods return ErrUnsupported.
package kyma

import "github.com/bluefunda/btp-go/binding"

// Provider implements binding.Provider for Kyma/Kubernetes environments.
// All methods currently return ErrUnsupported.
type Provider struct{}

// NewProvider returns a new Kyma Provider. All method calls will return
// binding.ErrUnsupported until the M4 implementation is complete.
func NewProvider() *Provider {
	return &Provider{}
}

// Connectivity always returns ErrUnsupported.
func (p *Provider) Connectivity(_ string) (*binding.ConnectivityBinding, error) {
	return nil, binding.ErrUnsupported
}

// Destination always returns ErrUnsupported.
func (p *Provider) Destination(_ string) (*binding.DestinationBinding, error) {
	return nil, binding.ErrUnsupported
}

// XSUAA always returns ErrUnsupported.
func (p *Provider) XSUAA(_ string) (*binding.XSUAABinding, error) {
	return nil, binding.ErrUnsupported
}
