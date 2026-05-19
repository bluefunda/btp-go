// Command http-odata is an example Cloud Foundry application that proxies
// OData reads and writes through SAP BTP HTTP destinations, demonstrating
// httpclient wired with the Connectivity Service HTTP CONNECT proxy.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/bluefunda/btp-go/binding"
	"github.com/bluefunda/btp-go/binding/auto"
	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/httpclient"
	"github.com/bluefunda/btp-go/xsuaa"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	prov, err := auto.NewProvider()
	if err != nil {
		slog.Error("binding provider", "err", err)
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

	destSrc := xsuaa.NewClientCredentialsSource(xsuaa.Config{
		ClientID:     db.ClientID,
		ClientSecret: db.ClientSecret,
		TokenURL:     db.TokenServiceURL + "/oauth/token",
	})
	destClient := destination.NewClient(db.URI, destSrc, nil)

	proxy := &httpclient.HTTPProxyConfig{Binding: cb}

	h := &odataHandler{destClient: destClient, httpProxy: proxy}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler)
	mux.HandleFunc("GET /odata", h.readHandler)
	mux.HandleFunc("POST /odata", h.writeHandler)

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
