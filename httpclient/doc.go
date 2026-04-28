// Package httpclient builds an *http.Client tuned for an SAP BTP Destination
// Service entry. It is the foundation of M3 (HTTP destination support).
//
// Three things the client takes care of automatically:
//
//   - Authorization: tokens delivered alongside the destination by the BTP
//     Destination Service (dest.AuthTokens) are attached to outgoing requests
//     using each token's prepared HTTPHeader (Type "Bearer", value, etc.).
//     If the destination has no AuthTokens, no auth header is set; the
//     caller may add their own.
//
//   - On-prem routing: when ProxyType=OnPremise and a connectivity.Dialer
//     is supplied, the client's transport DialContext is replaced so every
//     TCP open goes through the SAP Cloud Connector tunnel. The plain Go
//     http.Transport then negotiates TLS over the dialed conn — meaning
//     https:// destinations behind Cloud Connector work without further
//     configuration.
//
//   - Cookie jar: a per-client cookie jar is enabled by default so OData
//     v2 services that bind a session cookie to the X-CSRF-Token can be
//     used for writes (see FetchCSRF).
//
// Basic usage:
//
//	dest, _ := destClient.Find(ctx, "MY_HTTP_DEST")
//	client, baseURL, err := httpclient.New(dest, httpclient.Config{
//	    Dialer: connDialer, // nil for Internet destinations
//	})
//	resp, err := client.Get(baseURL + "/sap/opu/odata/.../EntitySet?$top=10")
//
// OData v2 write flow:
//
//	csrf, err := httpclient.FetchCSRF(ctx, client, baseURL+"/")
//	req, _ := http.NewRequestWithContext(ctx, "POST", baseURL+"/Items", body)
//	req.Header.Set("X-CSRF-Token", csrf)
//	req.Header.Set("Content-Type", "application/json")
//	resp, _ := client.Do(req)
//
// The package depends only on the sibling btp-go modules and the standard
// library.
package httpclient
