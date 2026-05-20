// Package binding defines the Provider interface and typed binding structs
// for SAP BTP service bindings. Concrete implementations are provided by
// sub-packages:
//
//   - binding/cf  — Cloud Foundry VCAP_SERVICES parser
//   - binding/kyma — Kyma/Kubernetes Service Binding file layout
//   - binding/auto — automatically selects the appropriate provider
//
// Typical usage:
//
//	import (
//	    "github.com/bluefunda/btp-go/binding"
//	    "github.com/bluefunda/btp-go/binding/auto"
//	)
//
//	prov, err := auto.NewProvider()
//	if err != nil {
//	    log.Fatal(err)
//	}
//	xb, err := binding.XSUAA(prov, "")          // "" = first instance
//	db, err := binding.Destination(prov, "")
//	cb, err := binding.Connectivity(prov, "")
//
// The typed extractor functions (Connectivity, Destination, XSUAA) convert
// the raw map returned by Provider.Binding into concrete credential structs.
// Adding support for a new service type requires only a new extractor function,
// not a change to the Provider interface.
//
// The package is stdlib-only and has zero third-party dependencies.
package binding
