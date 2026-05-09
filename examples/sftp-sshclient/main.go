// Command sftp-sshclient is an example Cloud Foundry application that exposes
// GET /sftp/count?destination=<name> returning the file count from a remote
// directory. Unlike examples/sftp-count (which dials raw SSH), this example
// uses sshclient.Dial to show the high-level wrapper.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/bluefunda/btp-go/binding/auto"
	"github.com/bluefunda/btp-go/connectivity"
	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/xsuaa"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	prov, err := auto.NewProvider()
	if err != nil {
		slog.Error("binding provider", "err", err)
		os.Exit(1)
	}

	xb, err := prov.XSUAA("")
	if err != nil {
		slog.Error("xsuaa binding", "err", err)
		os.Exit(1)
	}
	db, err := prov.Destination("")
	if err != nil {
		slog.Error("destination binding", "err", err)
		os.Exit(1)
	}
	cb, err := prov.Connectivity("")
	if err != nil {
		slog.Error("connectivity binding", "err", err)
		os.Exit(1)
	}

	_ = xsuaa.NewClientCredentialsSource(xsuaa.Config{
		ClientID:     xb.ClientID,
		ClientSecret: xb.ClientSecret,
		TokenURL:     xb.URL + "/oauth/token",
	})

	destSrc := xsuaa.NewClientCredentialsSource(xsuaa.Config{
		ClientID:     db.ClientID,
		ClientSecret: db.ClientSecret,
		TokenURL:     db.TokenServiceURL + "/oauth/token",
	})
	destClient := destination.NewClient(db.URI, destSrc, nil)

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

func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}` + "\n")) //nolint:errcheck
}
