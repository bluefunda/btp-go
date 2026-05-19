package kyma

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bluefunda/btp-go/binding"
)

// writeBinding builds a directory at root/<name>/ with one file per
// (key, value) entry in fields.
func writeBinding(t *testing.T, root, name string, fields map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	for k, v := range fields {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, k, err)
		}
	}
}

func TestProvider_Connectivity(t *testing.T) {
	root := t.TempDir()
	writeBinding(t, root, "my-conn", map[string]string{
		"type":                        "connectivity",
		"clientid":                    "cid-123",
		"clientsecret":                "csecret",
		"token_service_url":           "https://uaa.example/oauth/token",
		"onpremise_proxy_host":        "connectivity-proxy.kyma-system.svc.cluster.local",
		"onpremise_socks5_proxy_port": "20003",
		"onpremise_proxy_http_port":   "20004",
	})

	p := NewProvider(WithRoot(root))
	got, err := binding.Connectivity(p, "")
	if err != nil {
		t.Fatalf("Connectivity: unexpected error: %v", err)
	}
	want := &binding.ConnectivityBinding{
		ClientID:                 "cid-123",
		ClientSecret:             "csecret",
		TokenServiceURL:          "https://uaa.example/oauth/token",
		OnpremiseProxyHost:       "connectivity-proxy.kyma-system.svc.cluster.local",
		OnpremiseProxySocks5Port: "20003",
		OnpremiseProxyHTTPPort:   "20004",
	}
	if *got != *want {
		t.Errorf("Connectivity mismatch:\n got = %+v\nwant = %+v", got, want)
	}
}

func TestProvider_Destination_TokenURLFallback(t *testing.T) {
	root := t.TempDir()
	writeBinding(t, root, "my-dest", map[string]string{
		"type":         "destination",
		"clientid":     "did",
		"clientsecret": "dsec",
		"uri":          "https://destination.example",
		"url":          "https://uaa.example",
	})
	p := NewProvider(WithRoot(root))
	got, err := binding.Destination(p, "")
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	if got.TokenServiceURL != "https://uaa.example" {
		t.Errorf("TokenServiceURL fallback to url failed: %q", got.TokenServiceURL)
	}
	if got.URI != "https://destination.example" {
		t.Errorf("URI: got %q", got.URI)
	}
}

func TestProvider_XSUAA(t *testing.T) {
	root := t.TempDir()
	writeBinding(t, root, "auth", map[string]string{
		"type":         "xsuaa",
		"clientid":     "x",
		"clientsecret": "y",
		"url":          "https://auth.example",
		"uaadomain":    "authentication.eu10.hana.ondemand.com",
	})
	p := NewProvider(WithRoot(root))
	got, err := binding.XSUAA(p, "")
	if err != nil {
		t.Fatalf("XSUAA: %v", err)
	}
	if got.UAADomain != "authentication.eu10.hana.ondemand.com" {
		t.Errorf("UAADomain: got %q", got.UAADomain)
	}
}

func TestProvider_NameDisambiguation(t *testing.T) {
	root := t.TempDir()
	writeBinding(t, root, "first-conn", map[string]string{
		"type":     "connectivity",
		"clientid": "alpha",
	})
	writeBinding(t, root, "second-conn", map[string]string{
		"type":     "connectivity",
		"clientid": "beta",
	})

	p := NewProvider(WithRoot(root))
	got, err := binding.Connectivity(p, "second-conn")
	if err != nil {
		t.Fatalf("Connectivity(second-conn): %v", err)
	}
	if got.ClientID != "beta" {
		t.Errorf("expected clientid=beta, got %q", got.ClientID)
	}
}

func TestProvider_NotFound(t *testing.T) {
	root := t.TempDir()
	writeBinding(t, root, "only-dest", map[string]string{
		"type": "destination",
	})
	p := NewProvider(WithRoot(root))
	_, err := binding.Connectivity(p, "")
	if !errors.Is(err, binding.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}

	_, err = binding.Connectivity(p, "nonexistent")
	if !errors.Is(err, binding.ErrNotFound) {
		t.Errorf("expected ErrNotFound for explicit name, got %v", err)
	}
}

// TestProvider_RootMissing covers the case where SERVICE_BINDING_ROOT points
// at a path that does not exist (e.g. running locally without bindings).
func TestProvider_RootMissing(t *testing.T) {
	p := NewProvider(WithRoot("/this-path-does-not-exist-xyz"))
	_, err := binding.Connectivity(p, "")
	if !errors.Is(err, binding.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing root, got %v", err)
	}
}

// TestProvider_AtomicUpdateSentinels ensures we ignore the ".." entries that
// Kubernetes inserts during atomic Secret/ConfigMap updates.
func TestProvider_AtomicUpdateSentinels(t *testing.T) {
	root := t.TempDir()
	bindingDir := filepath.Join(root, "..2026_04_27_15_30_00.123456789")
	if err := os.MkdirAll(bindingDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "..data"), []byte(""), 0o644); err != nil {
		t.Fatalf("write ..data: %v", err)
	}
	writeBinding(t, root, "real-conn", map[string]string{
		"type":     "connectivity",
		"clientid": "real",
	})
	p := NewProvider(WithRoot(root))
	got, err := binding.Connectivity(p, "")
	if err != nil {
		t.Fatalf("Connectivity: %v", err)
	}
	if got.ClientID != "real" {
		t.Errorf("got clientid=%q, want real", got.ClientID)
	}
}

// TestProvider_CredentialsBlobFallback covers the older SAP BTP Service
// Operator format where credentials are projected as a single JSON file
// instead of one file per key.
func TestProvider_CredentialsBlobFallback(t *testing.T) {
	root := t.TempDir()
	writeBinding(t, root, "blob-conn", map[string]string{
		"type":        "connectivity",
		"credentials": `{"clientid":"from-blob","clientsecret":"sec","onpremise_proxy_host":"proxy.local"}`,
	})
	p := NewProvider(WithRoot(root))
	got, err := binding.Connectivity(p, "")
	if err != nil {
		t.Fatalf("Connectivity: %v", err)
	}
	if got.ClientID != "from-blob" || got.OnpremiseProxyHost != "proxy.local" {
		t.Errorf("blob fallback wrong: %+v", got)
	}
}
