package sshclient_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/sshclient"
	"golang.org/x/crypto/ssh"
)

// ---- test helpers ----

// fakeDialer satisfies sshclient.Dialer and returns a pre-built net.Conn.
type fakeDialer struct {
	conn net.Conn
	err  error
}

func (f *fakeDialer) Dial(_ context.Context, _ string, _ uint16, _ string) (net.Conn, error) {
	return f.conn, f.err
}

// addrDialer satisfies sshclient.Dialer by dialing a TCP address on each
// call, failing on the first (succeedOn-1) attempts with a transient error.
type addrDialer struct {
	mu        sync.Mutex
	calls     int
	succeedOn int
	addr      string
}

func (d *addrDialer) Dial(_ context.Context, _ string, _ uint16, _ string) (net.Conn, error) {
	d.mu.Lock()
	d.calls++
	n := d.calls
	d.mu.Unlock()
	if n < d.succeedOn {
		return nil, errors.New("ssh: handshake failed: EOF")
	}
	return net.Dial("tcp", d.addr)
}

func (d *addrDialer) CallCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// sshFixture starts a minimal loopback SSH server that accepts password auth.
// Returns the server address and a cleanup func.
// Using a TCP listener (not net.Pipe) avoids a deadlock: the OS buffer absorbs
// both sides' SSH banners so neither goroutine stalls before a read happens.
func sshFixture(t *testing.T, password string) (addr string, cleanup func()) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}

	serverCfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if string(pw) == password {
				return nil, nil
			}
			return nil, errors.New("bad password")
		},
	}
	serverCfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			tc, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				conn, chans, reqs, err := ssh.NewServerConn(c, serverCfg)
				if err != nil {
					c.Close()
					return
				}
				defer conn.Close()
				go ssh.DiscardRequests(reqs)
				for newChan := range chans {
					newChan.Reject(ssh.UnknownChannelType, "not implemented") //nolint:errcheck
				}
			}(tc)
		}
	}()

	return ln.Addr().String(), func() { ln.Close(); <-done }
}

// dialFixture dials the fixture server and returns the conn.
func dialFixture(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial fixture: %v", err)
	}
	return c
}

// simpleDest returns a Destination wired for password auth.
func simpleDest(user, password string) *destination.Destination {
	return &destination.Destination{
		Host:     "fake.internal",
		Port:     "22",
		User:     user,
		Password: password,
	}
}

// ---- Dial tests ----

func TestDial_SuccessWithFakeServer(t *testing.T) {
	addr, cleanup := sshFixture(t, "s3cr3t")
	defer cleanup()

	client, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer:          &fakeDialer{conn: dialFixture(t, addr)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	}, simpleDest("testuser", "s3cr3t"))
	if err != nil {
		t.Fatalf("Dial() error: %v", err)
	}
	client.Close()
}

func TestDial_WrongPasswordFails(t *testing.T) {
	addr, cleanup := sshFixture(t, "correct")
	defer cleanup()

	_, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer:          &fakeDialer{conn: dialFixture(t, addr)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	}, simpleDest("testuser", "wrong"))
	if err == nil {
		t.Fatal("expected error for wrong password, got nil")
	}
}

func TestDial_DialerErrorPropagates(t *testing.T) {
	sentinel := errors.New("dialer: network unreachable")
	_, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer: &fakeDialer{err: sentinel},
	}, simpleDest("u", "x"))
	if !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel in error chain, got: %v", err)
	}
}

func TestDial_NilDialerReturnsError(t *testing.T) {
	_, err := sshclient.Dial(context.Background(), sshclient.Config{}, simpleDest("u", "x"))
	if err == nil {
		t.Fatal("expected error for nil Dialer")
	}
}

func TestDial_NilDestinationReturnsError(t *testing.T) {
	_, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer: &fakeDialer{err: errors.New("unused")},
	}, nil)
	if err == nil {
		t.Fatal("expected error for nil destination")
	}
}

func TestDial_InvalidPortReturnsError(t *testing.T) {
	dest := &destination.Destination{Host: "h", Port: "notaport", Password: "x"}
	_, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer: &fakeDialer{},
	}, dest)
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestDial_RetryOnTransientError(t *testing.T) {
	addr, cleanup := sshFixture(t, "pass")
	defer cleanup()

	dialer := &addrDialer{succeedOn: 3, addr: addr}

	client, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer: dialer,
		RetryOpts: sshclient.RetryOpts{
			MaxAttempts: 3,
			BaseDelay:   time.Millisecond, // fast in tests
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	}, simpleDest("u", "pass"))
	if err != nil {
		t.Fatalf("Dial() error after retries: %v", err)
	}
	client.Close()

	if got := dialer.CallCount(); got != 3 {
		t.Errorf("expected 3 Dial calls, got %d", got)
	}
}

func TestDial_ExhaustedRetriesReturnsError(t *testing.T) {
	dialer := &addrDialer{succeedOn: 999, addr: "127.0.0.1:1"} // addr unused; all calls fail

	_, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer: dialer,
		RetryOpts: sshclient.RetryOpts{
			MaxAttempts: 2,
			BaseDelay:   time.Millisecond,
		},
	}, simpleDest("u", "x"))
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if got := dialer.CallCount(); got != 2 {
		t.Errorf("expected 2 Dial calls, got %d", got)
	}
}

func TestDial_ContextCancelDuringRetryDelay(t *testing.T) {
	dialer := &fakeDialer{err: errors.New("ssh: handshake failed: EOF")}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := sshclient.Dial(ctx, sshclient.Config{
		Dialer: dialer,
		RetryOpts: sshclient.RetryOpts{
			MaxAttempts: 5,
			BaseDelay:   500 * time.Millisecond,
		},
	}, simpleDest("u", "x"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got: %v", err)
	}
}

// ---- SSHConfigFromDestination tests ----

func TestSSHConfigFromDestination_PasswordFromTopLevel(t *testing.T) {
	dest := &destination.Destination{User: "alice", Password: "s3cr3t"}
	cfg, err := sshclient.SSHConfigFromDestination(dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "alice" {
		t.Errorf("User = %q, want alice", cfg.User)
	}
	if len(cfg.Auth) != 1 {
		t.Errorf("expected 1 auth method, got %d", len(cfg.Auth))
	}
}

func TestSSHConfigFromDestination_PasswordFromProperties(t *testing.T) {
	dest := &destination.Destination{
		Properties: map[string]string{"User": "bob", "Password": "secret"},
	}
	cfg, err := sshclient.SSHConfigFromDestination(dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "bob" {
		t.Errorf("User = %q, want bob", cfg.User)
	}
}

func TestSSHConfigFromDestination_DefaultUserIsRoot(t *testing.T) {
	dest := &destination.Destination{Password: "pw"}
	cfg, err := sshclient.SSHConfigFromDestination(dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "root" {
		t.Errorf("User = %q, want root", cfg.User)
	}
}

func TestSSHConfigFromDestination_NoAuthReturnsError(t *testing.T) {
	dest := &destination.Destination{Host: "h", Port: "22"}
	_, err := sshclient.SSHConfigFromDestination(dest)
	if err == nil {
		t.Fatal("expected error for destination with no auth configured")
	}
}

func TestSSHConfigFromDestination_InvalidSSHKeyReturnsError(t *testing.T) {
	dest := &destination.Destination{
		Password:   "pw",
		Properties: map[string]string{"sshKey": "not-a-pem-key"},
	}
	_, err := sshclient.SSHConfigFromDestination(dest)
	if err == nil {
		t.Fatal("expected error for invalid sshKey")
	}
}

// ---- IsTransientSSHError tests ----

func TestIsTransientSSHError(t *testing.T) {
	cases := []struct {
		msg       string
		transient bool
	}{
		{"ssh: handshake failed: EOF", true},
		{"read tcp 1.2.3.4:22: connection reset by peer", true},
		{"dial tcp: connection refused", true},
		{"read: i/o timeout", true},
		{"dial tcp 10.0.0.1:22: i/o timeout", true},
		{"ssh: unable to authenticate, attempted methods [password]", false},
		{"sshclient: no SSH auth on destination (set User+Password or sshKey)", false},
		{"ssh: disconnect, reason 11: Bye Bye", false},
	}
	for _, tc := range cases {
		got := sshclient.IsTransientSSHError(errors.New(tc.msg))
		if got != tc.transient {
			t.Errorf("IsTransientSSHError(%q) = %v, want %v", tc.msg, got, tc.transient)
		}
	}
	if sshclient.IsTransientSSHError(nil) {
		t.Error("IsTransientSSHError(nil) should return false")
	}
}
