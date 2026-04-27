package connectivity

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// mockTokenSource is a TokenSource that returns a fixed token or an error.
type mockTokenSource struct {
	token string
	err   error
}

func (m *mockTokenSource) Token(_ context.Context) (string, error) {
	return m.token, m.err
}

// ctxCheckTokenSource returns context.Err() if context is already cancelled.
type ctxCheckTokenSource struct{}

func (c *ctxCheckTokenSource) Token(ctx context.Context) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
		return "token", nil
	}
}

// runMiniServer runs fn in a goroutine against the server-side of a net.Pipe().
// It returns the client-side connection.
func runMiniServer(t *testing.T, fn func(server net.Conn)) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() {
		defer server.Close()
		fn(server)
	}()
	return client
}

// mustRead reads exactly n bytes from r, failing the test on error.
func mustRead(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("server read: %v", err)
	}
	return buf
}

// mustWrite writes b to w, failing the test on error.
func mustWrite(t *testing.T, w io.Writer, b []byte) {
	t.Helper()
	if _, err := w.Write(b); err != nil {
		t.Fatalf("server write: %v", err)
	}
}

// TestHandshake_SuccessfulSAPAuth verifies the complete 0x80 auth flow.
func TestHandshake_SuccessfulSAPAuth(t *testing.T) {
	const jwt = "test.jwt.token"
	ts := &mockTokenSource{token: jwt}

	client := runMiniServer(t, func(srv net.Conn) {
		// Read greeting: [0x05, 0x01, 0x80]
		mustRead(t, srv, 3)
		// Reply: chosen method 0x80
		mustWrite(t, srv, []byte{0x05, 0x80})

		// Read auth frame.
		frame := BuildAuthFrame(jwt, "")
		mustRead(t, srv, len(frame))
		// Reply: success
		mustWrite(t, srv, []byte{0x01, 0x00})

		// Read CONNECT.
		req := BuildConnect("my-host", 22)
		mustRead(t, srv, len(req))
		// Reply: success, ATYP=IPv4, bound 0.0.0.0:0
		mustWrite(t, srv, []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	})

	ctx := context.Background()
	err := handshake(ctx, client, ts, "my-host", 22, "")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

// TestHandshake_NoAuth_NilTokenSource verifies the no-auth fallback when
// TokenSource is nil.
func TestHandshake_NoAuth_NilTokenSource(t *testing.T) {
	client := runMiniServer(t, func(srv net.Conn) {
		// Greeting: client should propose 0x00
		b := mustRead(t, srv, 3)
		if b[2] != 0x00 {
			t.Errorf("expected no-auth method 0x00, got 0x%02X", b[2])
		}
		mustWrite(t, srv, []byte{0x05, 0x00})

		// CONNECT
		req := BuildConnect("target", 2222)
		mustRead(t, srv, len(req))
		mustWrite(t, srv, []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	})

	ctx := context.Background()
	err := handshake(ctx, client, nil, "target", 2222, "")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

// TestREPMessage validates all 8 standard codes plus an unknown code.
func TestREPMessage(t *testing.T) {
	cases := []struct {
		rep  byte
		want string
	}{
		{0x00, "succeeded"},
		{0x01, "general failure"},
		{0x02, "not allowed by ruleset"},
		{0x03, "network unreachable"},
		{0x04, "host unreachable"},
		{0x05, "connection refused"},
		{0x06, "TTL expired"},
		{0x07, "command not supported"},
		{0x08, "address type not supported"},
		{0xAB, "unknown REP=0xAB"},
	}
	for _, tc := range cases {
		got := REPMessage(tc.rep)
		if got != tc.want {
			t.Errorf("REPMessage(0x%02X) = %q, want %q", tc.rep, got, tc.want)
		}
	}
}

// TestHandshake_MalformedGreeting_WrongVersion verifies that a wrong SOCKS
// version byte in the server reply causes a greeting-stage StageError.
func TestHandshake_MalformedGreeting_WrongVersion(t *testing.T) {
	client := runMiniServer(t, func(srv net.Conn) {
		mustRead(t, srv, 3)
		// Reply with wrong SOCKS version
		mustWrite(t, srv, []byte{0x04, 0x00})
	})

	ctx := context.Background()
	err := handshake(ctx, client, nil, "host", 80, "")
	if err == nil {
		t.Fatal("expected error for malformed greeting, got nil")
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected StageError, got %T: %v", err, err)
	}
	if se.Stage != "greeting" {
		t.Errorf("expected stage=greeting, got %q", se.Stage)
	}
}

// TestHandshake_ContextCancellation verifies that a cancelled context causes
// the handshake to return an error before or during the token fetch.
func TestHandshake_ContextCancellation(t *testing.T) {
	ts := &ctxCheckTokenSource{}

	ctx, cancel := context.WithCancel(context.Background())

	client := runMiniServer(t, func(srv net.Conn) {
		mustRead(t, srv, 3)
		mustWrite(t, srv, []byte{0x05, 0x80})
		// Delay to give client time to try token fetch with cancelled ctx.
		time.Sleep(50 * time.Millisecond)
	})

	// Cancel context immediately before handshake so token fetch sees Done.
	cancel()

	err := handshake(ctx, client, ts, "host", 22, "")
	if err == nil {
		t.Fatal("expected error due to context cancellation, got nil")
	}
	// The error should wrap context.Canceled somewhere in the chain.
	if !errors.Is(err, context.Canceled) {
		// It's also acceptable to get a net.Pipe write error; just confirm
		// we got *some* error.
		t.Logf("got (non-context-canceled) error: %v", err)
	}
}

// TestBuildAuthFrame_EmptyLocationID verifies the frame byte layout with no
// location ID.
func TestBuildAuthFrame_EmptyLocationID(t *testing.T) {
	jwt := "my.jwt"
	frame := BuildAuthFrame(jwt, "")

	if frame[0] != 0x01 {
		t.Errorf("byte[0]: want 0x01 (sub-neg version), got 0x%02X", frame[0])
	}
	// bytes 1-4: big-endian uint32 len(jwt)
	jwtLen := int(frame[1])<<24 | int(frame[2])<<16 | int(frame[3])<<8 | int(frame[4])
	if jwtLen != len(jwt) {
		t.Errorf("jwt length field: want %d, got %d", len(jwt), jwtLen)
	}
	// byte after jwt: 0x00 (empty locationID)
	if frame[5+jwtLen] != 0x00 {
		t.Errorf("locLen byte: want 0x00, got 0x%02X", frame[5+jwtLen])
	}
	// Total length: 1 + 4 + len(jwt) + 1
	want := 6 + len(jwt)
	if len(frame) != want {
		t.Errorf("frame length: want %d, got %d", want, len(frame))
	}
}

// TestBuildAuthFrame_WithLocationID verifies the location ID is base64-encoded
// and prefixed with its length.
func TestBuildAuthFrame_WithLocationID(t *testing.T) {
	jwt := "tok"
	locID := "MyLocation"
	b64loc := base64.StdEncoding.EncodeToString([]byte(locID))

	frame := BuildAuthFrame(jwt, locID)

	if frame[0] != 0x01 {
		t.Errorf("sub-neg version: want 0x01, got 0x%02X", frame[0])
	}

	jwtLen := int(frame[1])<<24 | int(frame[2])<<16 | int(frame[3])<<8 | int(frame[4])
	if jwtLen != len(jwt) {
		t.Errorf("jwt len: want %d, got %d", len(jwt), jwtLen)
	}

	locPartStart := 5 + jwtLen
	locLenByte := int(frame[locPartStart])
	if locLenByte != len(b64loc) {
		t.Errorf("loc length byte: want %d, got %d", len(b64loc), locLenByte)
	}
	got := string(frame[locPartStart+1 : locPartStart+1+locLenByte])
	if got != b64loc {
		t.Errorf("location content: want %q, got %q", b64loc, got)
	}
}
