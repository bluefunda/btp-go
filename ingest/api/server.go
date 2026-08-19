package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// ServerOptions configures the HTTP listener.
type ServerOptions struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string

	// ReadHeaderTimeout, ReadTimeout, WriteTimeout and IdleTimeout bound
	// request handling.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// TLSCertFile and TLSKeyFile enable HTTPS when both are set.
	TLSCertFile string
	TLSKeyFile  string

	// ClientCAFile enables mTLS. Client certificates are verified against
	// this bundle during the handshake.
	ClientCAFile string

	// Logger is required.
	Logger *slog.Logger
}

// Server wraps an http.Server with the gateway's startup and shutdown
// sequence.
type Server struct {
	http *http.Server
	opts ServerOptions
	log  *slog.Logger
	tls  bool
}

// NewServer builds the listener around handler.
func NewServer(handler http.Handler, opts ServerOptions) (*Server, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ReadHeaderTimeout <= 0 {
		// Without this, a client that opens a connection and never
		// finishes its headers holds a goroutine indefinitely.
		opts.ReadHeaderTimeout = 5 * time.Second
	}

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           handler,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		ReadTimeout:       opts.ReadTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(opts.Logger.Handler(), slog.LevelWarn),
	}

	useTLS := opts.TLSCertFile != "" && opts.TLSKeyFile != ""
	if opts.ClientCAFile != "" && !useTLS {
		return nil, errors.New("api: ClientCAFile requires TLSCertFile and TLSKeyFile")
	}
	if useTLS {
		tlsCfg, err := buildServerTLS(opts)
		if err != nil {
			return nil, err
		}
		srv.TLSConfig = tlsCfg
	}

	return &Server{http: srv, opts: opts, log: opts.Logger, tls: useTLS}, nil
}

func buildServerTLS(opts ServerOptions) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if opts.ClientCAFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(opts.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("api: read client CA file: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("api: client CA file %q contains no certificates", opts.ClientCAFile)
	}
	cfg.ClientCAs = pool

	// Verification happens in the handshake, so an untrusted client never
	// reaches a handler. The authenticator's own certificate check is a
	// second gate for the subject name.
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	return cfg, nil
}

// Serve starts the listener and blocks until ctx is cancelled or the server
// fails, then shuts down gracefully within timeout.
//
// Shutdown drains in-flight requests before returning, which is what makes
// it safe for the caller to drain the publisher next: by then no new
// submissions can arrive.
func (s *Server) Serve(ctx context.Context, timeout time.Duration) error {
	errCh := make(chan error, 1)

	go func() {
		s.log.Info("http server listening", "addr", s.opts.Addr, "tls", s.tls, "mtls", s.opts.ClientCAFile != "")
		var err error
		if s.tls {
			err = s.http.ListenAndServeTLS(s.opts.TLSCertFile, s.opts.TLSKeyFile)
		} else {
			err = s.http.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("api: serve: %w", err)
		}
		return nil
	case <-ctx.Done():
	}

	s.log.Info("http server shutting down", "timeout", timeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		// Force the listener closed so the process can still exit.
		_ = s.http.Close()
		return fmt.Errorf("api: graceful shutdown: %w", err)
	}
	s.log.Info("http server stopped")
	return nil
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.opts.Addr }
