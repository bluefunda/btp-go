package xsuaa

import "context"

// TokenSource is implemented by any value that can return a bearer JWT.
// NewClientCredentialsSource returns a TokenSource backed by the XSUAA
// client-credentials grant. The interface is also satisfied by any value
// with a Token(ctx context.Context) (string, error) method, making it
// easy to supply test stubs without importing this package.
//
// The connectivity and destination modules redeclare a structurally identical
// interface to remain stdlib-only; values satisfying this interface satisfy
// those interfaces too.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}
