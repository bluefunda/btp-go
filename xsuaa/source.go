package xsuaa

import "context"

// TokenSource is implemented by any value that can return a bearer JWT.
// xsuaa.NewClientCredentialsSource returns a TokenSource. The interface
// is also satisfied by any compatible implementation from other packages.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}
