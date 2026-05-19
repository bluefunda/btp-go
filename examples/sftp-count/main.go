// Command sftp-count is an example Cloud Foundry application that exposes
// GET /sftp/count?destination=<name> returning the file count from a remote
// directory, wired through SAP BTP Connectivity Service SOCKS5 proxy.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/bluefunda/btp-go/binding"
	"github.com/bluefunda/btp-go/binding/auto"
	"github.com/bluefunda/btp-go/connectivity"
	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/xsuaa"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// Detect runtime and load service bindings.
	prov, err := auto.NewProvider()
	if err != nil {
		slog.Error("binding provider", "err", err)
		os.Exit(1)
	}

	xb, err := binding.XSUAA(prov, "")
	if err != nil {
		slog.Error("XSUAA binding", "err", err)
		os.Exit(1)
	}
	db, err := binding.Destination(prov, "")
	if err != nil {
		slog.Error("destination binding", "err", err)
		os.Exit(1)
	}
	cb, err := binding.Connectivity(prov, "")
	if err != nil {
		slog.Error("connectivity binding", "err", err)
		os.Exit(1)
	}

	// XSUAA token source for the destination service.
	destSrc := xsuaa.NewClientCredentialsSource(xsuaa.Config{
		ClientID:     db.ClientID,
		ClientSecret: db.ClientSecret,
		TokenURL:     db.TokenServiceURL + "/oauth/token",
	})
	destClient := destination.NewClient(db.URI, destSrc, nil)

	// XSUAA token source for the connectivity service (SOCKS5 proxy auth).
	connSrc := xsuaa.NewClientCredentialsSource(xsuaa.Config{
		ClientID:     cb.ClientID,
		ClientSecret: cb.ClientSecret,
		TokenURL:     cb.TokenServiceURL + "/oauth/token",
	})
	dialer := connectivity.NewDialer(connectivity.Config{
		ProxyHost:   cb.OnpremiseProxyHost,
		ProxyPort:   cb.OnpremiseProxySocks5Port,
		TokenSource: connSrc,
	})

	// XSUAA token source (available for future handlers, e.g. principal propagation).
	_ = xsuaa.NewClientCredentialsSource(xsuaa.Config{
		ClientID:     xb.ClientID,
		ClientSecret: xb.ClientSecret,
		TokenURL:     xb.URL + "/oauth/token",
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler)
	mux.HandleFunc("GET /sftp/count", sftpCountHandler(destClient, dialer))

	addr := ":" + port
	slog.Info("listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

// healthzHandler returns a simple liveness response.
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}` + "\n")) //nolint:errcheck
}
