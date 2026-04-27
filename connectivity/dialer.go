package connectivity

import (
	"context"
	"fmt"
	"net"
	"time"
)

// TokenSource is the interface for obtaining a bearer JWT. It is deliberately
// defined here (not imported from xsuaa) so that the connectivity module
// remains stdlib-only. Any concrete type that implements
//
//	Token(ctx context.Context) (string, error)
//
// satisfies it — including xsuaa.NewClientCredentialsSource().
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Config holds the parameters required to construct a Dialer.
type Config struct {
	// ProxyHost is the hostname of the Connectivity Service SOCKS5 proxy,
	// taken from the connectivity binding's onpremise_proxy_host field.
	ProxyHost string

	// ProxyPort is the port of the SOCKS5 proxy (typically "20004"),
	// taken from the onpremise_socks5_proxy_port field.
	ProxyPort string

	// TokenSource provides the JWT for SAP's 0x80 auth method. When nil,
	// the dialer falls back to method 0x00 (no authentication).
	TokenSource TokenSource

	// DialTimeout is the maximum time allowed for a complete Dial call,
	// including the TCP connection and SOCKS5 handshake. Defaults to 30s.
	DialTimeout time.Duration
}

// Dialer opens TCP tunnels through SAP Cloud Connector via the Connectivity
// Service's SOCKS5 proxy. It is safe for concurrent use.
type Dialer struct {
	cfg Config
}

// NewDialer creates a Dialer from cfg. If cfg.DialTimeout is zero it defaults
// to 30 seconds.
func NewDialer(cfg Config) *Dialer {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 30 * time.Second
	}
	return &Dialer{cfg: cfg}
}

// Dial opens a tunnel to virtualHost:virtualPort on the on-premises network.
// virtualHost and virtualPort must match an SCC virtual mapping. Pass an
// empty string for locationID to use the default Cloud Connector location.
//
// The returned net.Conn is ready for any overlaid protocol (SSH, TLS, etc.).
// Errors are returned as *StageError so callers can inspect which stage failed.
func (d *Dialer) Dial(ctx context.Context, virtualHost string, virtualPort uint16, locationID string) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, d.cfg.DialTimeout)
	defer cancel()

	proxyAddr := net.JoinHostPort(d.cfg.ProxyHost, d.cfg.ProxyPort)
	nd := &net.Dialer{}
	conn, err := nd.DialContext(dialCtx, "tcp", proxyAddr)
	if err != nil {
		return nil, stageErr("dial", fmt.Errorf("connect to proxy %s: %w", proxyAddr, err))
	}

	if err := handshake(dialCtx, conn, d.cfg.TokenSource, virtualHost, virtualPort, locationID); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}
