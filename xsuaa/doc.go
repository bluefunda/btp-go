// Package xsuaa provides a client-credentials token source for SAP BTP's
// XSUAA (Extended Services for User Account and Authentication) OAuth2 server.
//
// Usage:
//
//	import "github.com/bluefunda/btp-go/xsuaa"
//
//	src := xsuaa.NewClientCredentialsSource(xsuaa.Config{
//	    ClientID:     "sb-my-app!t1234",
//	    ClientSecret: "secret",
//	    TokenURL:     "https://my-tenant.authentication.eu10.hana.ondemand.com/oauth/token",
//	})
//
//	token, err := src.Token(ctx)
//
// Tokens are cached in memory and refreshed automatically before expiry.
// Concurrent callers during a refresh share a single in-flight HTTP request.
//
// The package is stdlib-only and has zero third-party dependencies.
package xsuaa
