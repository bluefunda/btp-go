package sshclient

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/bluefunda/btp-go/destination"
	"golang.org/x/crypto/ssh"
)

// Dialer opens a TCP connection to a remote host. *connectivity.Dialer
// satisfies this interface, as does any in-process fake for testing.
type Dialer interface {
	Dial(ctx context.Context, host string, port uint16, locationID string) (net.Conn, error)
}

// Config controls how SSH sessions are established.
type Config struct {
	// Dialer opens the underlying TCP connection to the destination host.
	// Pass *connectivity.Dialer for real BTP traffic, or any Dialer
	// implementation for testing. Required.
	Dialer Dialer

	// RetryOpts controls backoff on transient handshake errors. The zero
	// value disables retry (one attempt only).
	RetryOpts RetryOpts

	// HostKeyCallback overrides the default. When nil, the package uses
	// ssh.InsecureIgnoreHostKey — matching the reality that on-prem hosts
	// reached via Cloud Connector usually don't have callable host-key
	// verification. Override this in regulated environments.
	HostKeyCallback ssh.HostKeyCallback
}

// RetryOpts controls how Dial retries transient handshake failures.
//
// On burst load (many parallel connections from CF instances or goroutines),
// the on-prem sshd's MaxStartups setting probabilistically drops new
// connections — appearing to the client as "ssh: handshake failed: EOF".
// Retrying with exponential backoff + jitter cleanly absorbs this.
type RetryOpts struct {
	// MaxAttempts is the total attempt count including the first.
	// Values <= 1 disable retry.
	MaxAttempts int

	// BaseDelay is the delay before the second attempt. Each subsequent
	// retry doubles the previous delay. Zero defaults to 200ms.
	BaseDelay time.Duration

	// Jitter applies ±25% random jitter to each backoff window when true.
	Jitter bool
}

// Dial opens an *ssh.Client to dest's Host:Port via cfg.Dialer (SOCKS5),
// authenticating with dest.User/Password and dest.Properties["sshKey"].
// On transient handshake errors (see IsTransientSSHError) it retries per
// cfg.RetryOpts. The returned *ssh.Client must be Close()'d by the caller.
func Dial(ctx context.Context, cfg Config, dest *destination.Destination) (*ssh.Client, error) {
	if cfg.Dialer == nil {
		return nil, errors.New("sshclient: Config.Dialer is required")
	}
	if dest == nil {
		return nil, errors.New("sshclient: destination is required")
	}

	attempts := cfg.RetryOpts.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	base := cfg.RetryOpts.BaseDelay
	if base <= 0 {
		base = 200 * time.Millisecond
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			delay := base << uint(attempt-1)
			if cfg.RetryOpts.Jitter {
				delay += time.Duration(rand.Int64N(int64(delay) / 2))
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		client, err := dialOnce(ctx, cfg, dest)
		if err == nil {
			return client, nil
		}
		lastErr = err
		if !IsTransientSSHError(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("sshclient: after %d attempts: %w", attempts, lastErr)
}

// dialOnce performs a single Dial attempt without retry.
func dialOnce(ctx context.Context, cfg Config, dest *destination.Destination) (*ssh.Client, error) {
	portNum, err := strconv.ParseUint(dest.Port, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("sshclient: invalid port %q: %w", dest.Port, err)
	}

	conn, err := cfg.Dialer.Dial(ctx, dest.Host, uint16(portNum), dest.CloudConnectorLocationID)
	if err != nil {
		return nil, err
	}

	sshCfg, err := SSHConfigFromDestination(dest)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if cfg.HostKeyCallback != nil {
		sshCfg.HostKeyCallback = cfg.HostKeyCallback
	}

	hostPort := net.JoinHostPort(dest.Host, dest.Port)
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, hostPort, sshCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh: %w", err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// SSHConfigFromDestination builds an *ssh.ClientConfig from User/Password
// (top-level fields) and Properties["User"|"Password"|"sshKey"].
//
// Auth precedence: if Properties["sshKey"] is present it is parsed as a
// PEM private key and added as ssh.PublicKeys; if a non-empty Password (or
// Properties["Password"]) is present it is added as ssh.Password. At
// least one must be configured. The default HostKeyCallback is
// ssh.InsecureIgnoreHostKey.
func SSHConfigFromDestination(dest *destination.Destination) (*ssh.ClientConfig, error) {
	user := firstNonEmpty(dest.User, dest.Properties["User"], "root")
	var auth []ssh.AuthMethod
	if key := dest.Properties["sshKey"]; key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("sshclient: parse sshKey: %w", err)
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	pass := firstNonEmpty(dest.Password, dest.Properties["Password"])
	if pass != "" {
		auth = append(auth, ssh.Password(pass))
	}
	if len(auth) == 0 {
		return nil, errors.New("sshclient: no SSH auth on destination (set User+Password or sshKey)")
	}
	return &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	}, nil
}

// IsTransientSSHError reports whether err from Dial is worth retrying:
// handshake EOF (typically MaxStartups rejection), dial timeouts, and
// connection reset/refused. Auth and config errors are not transient.
func IsTransientSSHError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "handshake failed: EOF"),
		strings.Contains(s, "connection reset"),
		strings.Contains(s, "connection refused"),
		strings.Contains(s, "i/o timeout"),
		strings.Contains(s, "dial tcp"):
		return true
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
