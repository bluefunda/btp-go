// Package destination provides a client for the SAP BTP Destination Service
// REST API. It resolves named destinations and returns typed structs containing
// the connection parameters and auth tokens.
//
// Usage:
//
//	import "github.com/bluefunda/btp-go/destination"
//
//	client := destination.NewClient(
//	    "https://destination-configuration.cfapps.eu10.hana.ondemand.com",
//	    myTokenSource, // satisfies destination.TokenSource
//	    nil,           // use http.DefaultClient
//	)
//
//	dest, err := client.Find(ctx, "MY_SFTP_DEST")
//	if err != nil {
//	    log.Fatal(err)
//	}
//	// dest.Host, dest.Port, dest.Properties["User"] etc.
//
// The package is stdlib-only and has zero third-party dependencies.
package destination
