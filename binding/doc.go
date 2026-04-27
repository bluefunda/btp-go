// Package binding defines the Provider interface and typed binding structs
// for SAP BTP service bindings. Concrete implementations are provided by
// sub-packages:
//
//   - binding/cf  — Cloud Foundry VCAP_SERVICES parser
//   - binding/kyma — Kyma/Kubernetes Service Binding (stub, returns ErrUnsupported)
//   - binding/auto — automatically selects the appropriate provider
//
// Typical usage:
//
//	import "github.com/bluefunda/btp-go/binding/auto"
//
//	prov, err := auto.NewProvider()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	xb, err := prov.XSUAA("")          // "" = first instance
//	db, err := prov.Destination("")
//	cb, err := prov.Connectivity("")
//
// The package is stdlib-only and has zero third-party dependencies.
package binding
