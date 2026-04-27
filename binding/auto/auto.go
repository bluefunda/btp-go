// Package auto selects the appropriate binding.Provider for the current
// runtime environment.
//
// Detection order:
//
//  1. VCAP_SERVICES env var is set → Cloud Foundry → binding/cf
//  2. SERVICE_BINDING_ROOT env var is set → Kyma/Kubernetes → binding/kyma
//  3. /bindings directory exists → Kyma/Kubernetes → binding/kyma
//  4. Otherwise → error
package auto

import (
	"fmt"
	"os"

	"github.com/bluefunda/btp-go/binding"
	"github.com/bluefunda/btp-go/binding/cf"
	"github.com/bluefunda/btp-go/binding/kyma"
)

// NewProvider detects the runtime environment and returns the appropriate
// binding.Provider. Returns an error if no supported environment is detected.
func NewProvider() (binding.Provider, error) {
	// Cloud Foundry: VCAP_SERVICES is always set by the CF runtime.
	if os.Getenv("VCAP_SERVICES") != "" {
		return cf.NewProvider()
	}

	// Kyma / Kubernetes Service Binding operator.
	if os.Getenv("SERVICE_BINDING_ROOT") != "" {
		return kyma.NewProvider(), nil
	}

	// Fallback: check for the default /bindings directory used by the
	// Servicebinding.io spec.
	if _, err := os.Stat("/bindings"); err == nil {
		return kyma.NewProvider(), nil
	}

	return nil, fmt.Errorf("auto: no supported service binding environment detected " +
		"(set VCAP_SERVICES, SERVICE_BINDING_ROOT, or ensure /bindings exists)")
}
