package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/httpclient"
)

type odataHandler struct {
	destClient *destination.Client
	httpProxy  *httpclient.HTTPProxyConfig
}

type errResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// clientFor resolves the named destination and returns a configured *http.Client
// and base URL. The Cloud Connector HTTP CONNECT proxy is applied only when
// dest.ProxyType is "OnPremise"; Internet destinations use a direct transport.
func (h *odataHandler) clientFor(ctx context.Context, destName string) (*http.Client, string, error) {
	dest, err := h.destClient.Find(ctx, destName)
	if err != nil {
		return nil, "", fmt.Errorf("destination %q: %w", destName, err)
	}
	cfg := httpclient.Config{
		Timeout:      30 * time.Second,
		ExtraHeaders: http.Header{"Accept": []string{"application/json"}},
	}
	if strings.EqualFold(dest.ProxyType, "OnPremise") {
		cfg.HTTPProxy = h.httpProxy
	}
	return httpclient.New(dest, cfg)
}

// readHandler handles GET /odata?destination=<name>&path=<odata-path>
//
// Fetches the resource at <base-url><path> and streams the response back.
// path defaults to "/" (service document). The destination's auth token is
// attached automatically by the httpclient transport.
//
// Example:
//
//	GET /odata?destination=MY_HTTP_DEST&path=/sap/opu/odata/sap/MY_SVC/Items?%24top=5
func (h *odataHandler) readHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	destName := r.URL.Query().Get("destination")
	if destName == "" {
		writeJSON(w, http.StatusBadRequest, errResponse{Error: "'destination' query param required"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}

	client, baseURL, err := h.clientFor(ctx, destName)
	if err != nil {
		slog.ErrorContext(ctx, "client build", "destination", destName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errResponse{Error: err.Error()})
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse{Error: err.Error()})
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "odata read", "destination", destName, "path", path, "err", err)
		writeJSON(w, http.StatusBadGateway, errResponse{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	slog.InfoContext(ctx, "odata read", "destination", destName, "path", path, "status", resp.StatusCode)
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

// writeHandler handles POST /odata?destination=<name>&path=<odata-path>
//
// Performs the OData v2 write flow: fetches a CSRF token against the service
// root (the cookie jar on the shared client preserves the session), then
// issues the POST with X-CSRF-Token set. The request body is forwarded verbatim.
//
// Example:
//
//	POST /odata?destination=MY_HTTP_DEST&path=/sap/opu/odata/sap/MY_SVC/Items
//	Content-Type: application/json
//	{"Name":"example","Quantity":1}
func (h *odataHandler) writeHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	destName := r.URL.Query().Get("destination")
	if destName == "" {
		writeJSON(w, http.StatusBadRequest, errResponse{Error: "'destination' query param required"})
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeJSON(w, http.StatusBadRequest, errResponse{Error: "'path' query param required"})
		return
	}

	client, baseURL, err := h.clientFor(ctx, destName)
	if err != nil {
		slog.ErrorContext(ctx, "client build", "destination", destName, "err", err)
		writeJSON(w, http.StatusInternalServerError, errResponse{Error: err.Error()})
		return
	}

	// Fetch CSRF token. SAP OData v2 services bind the token to the session
	// cookie set on this GET; the client's jar carries that cookie into the POST.
	csrf, err := httpclient.FetchCSRF(ctx, client, baseURL+"/")
	if err != nil {
		slog.ErrorContext(ctx, "csrf fetch", "destination", destName, "err", err)
		writeJSON(w, http.StatusBadGateway, errResponse{Error: fmt.Sprintf("csrf: %v", err)})
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errResponse{Error: err.Error()})
		return
	}
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("X-CSRF-Token", csrf)

	resp, err := client.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "odata write", "destination", destName, "path", path, "err", err)
		writeJSON(w, http.StatusBadGateway, errResponse{Error: err.Error()})
		return
	}
	defer resp.Body.Close()

	slog.InfoContext(ctx, "odata write", "destination", destName, "path", path, "status", resp.StatusCode)
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
