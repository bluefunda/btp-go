// Package kyma implements binding.Provider for SAP BTP Kyma (Kubernetes)
// runtime environments where the SAP BTP Service Operator projects each
// service binding's credentials as files under a per-binding directory.
//
// Layout (Servicebinding.io v1.0.0):
//
//	$SERVICE_BINDING_ROOT/<binding-name>/
//	    type             # service kind: "connectivity" | "destination" | "xsuaa" | ...
//	    provider         # optional, e.g. "sap"
//	    clientid         # one file per credential key
//	    clientsecret
//	    token_service_url
//	    ...
//
// The Kubernetes Secret/Volume projection inserts atomic-update sentinels
// (".." prefixed names) which this package filters out. Subdirectory names
// are arbitrary labels chosen at bind time; service kind is determined by
// the contents of the "type" file. Pass an explicit binding name to a
// Provider method to disambiguate when multiple bindings of the same kind
// exist.
//
// The package is stdlib-only.
package kyma

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bluefunda/btp-go/binding"
)

// DefaultRoot is the directory used when SERVICE_BINDING_ROOT is unset.
// Matches the Servicebinding.io spec default.
const DefaultRoot = "/bindings"

// Provider implements binding.Provider for Kyma file-mounted service bindings.
type Provider struct {
	root string
}

// Option customises a Provider at construction time.
type Option func(*Provider)

// WithRoot overrides the bindings directory (defaults to SERVICE_BINDING_ROOT
// env, falling back to "/bindings"). Useful for tests and for clusters that
// mount bindings under a non-standard path.
func WithRoot(path string) Option {
	return func(p *Provider) { p.root = path }
}

// NewProvider returns a Provider that reads bindings from
// SERVICE_BINDING_ROOT (defaulting to "/bindings"). Use WithRoot to override.
func NewProvider(opts ...Option) *Provider {
	p := &Provider{root: os.Getenv("SERVICE_BINDING_ROOT")}
	if p.root == "" {
		p.root = DefaultRoot
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Connectivity returns the binding for the named connectivity service
// instance. Pass "" to match any binding whose type is "connectivity".
func (p *Provider) Connectivity(name string) (*binding.ConnectivityBinding, error) {
	files, err := p.findBinding("connectivity", name)
	if err != nil {
		return nil, err
	}
	return &binding.ConnectivityBinding{
		ClientID:                 files["clientid"],
		ClientSecret:             files["clientsecret"],
		TokenServiceURL:          files["token_service_url"],
		OnpremiseProxyHost:       files["onpremise_proxy_host"],
		OnpremiseProxySocks5Port: files["onpremise_socks5_proxy_port"],
		OnpremiseProxyHTTPPort:   files["onpremise_proxy_http_port"],
	}, nil
}

// Destination returns the binding for the named destination service instance.
// Pass "" to match any binding whose type is "destination".
func (p *Provider) Destination(name string) (*binding.DestinationBinding, error) {
	files, err := p.findBinding("destination", name)
	if err != nil {
		return nil, err
	}
	tokenSvcURL := files["token_service_url"]
	if tokenSvcURL == "" {
		tokenSvcURL = files["url"]
	}
	return &binding.DestinationBinding{
		ClientID:        files["clientid"],
		ClientSecret:    files["clientsecret"],
		URI:             files["uri"],
		TokenServiceURL: tokenSvcURL,
	}, nil
}

// XSUAA returns the binding for the named xsuaa service instance.
// Pass "" to match any binding whose type is "xsuaa".
func (p *Provider) XSUAA(name string) (*binding.XSUAABinding, error) {
	files, err := p.findBinding("xsuaa", name)
	if err != nil {
		return nil, err
	}
	return &binding.XSUAABinding{
		ClientID:     files["clientid"],
		ClientSecret: files["clientsecret"],
		URL:          files["url"],
		UAADomain:    files["uaadomain"],
	}, nil
}

// findBinding returns the file map for a binding directory matching wantType,
// or wrapped binding.ErrNotFound if no such directory exists. When name is
// non-empty the directory's basename must equal it; otherwise the first
// matching directory wins (lexical order from os.ReadDir).
func (p *Provider) findBinding(wantType, name string) (map[string]string, error) {
	entries, err := os.ReadDir(p.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: bindings root %q does not exist", binding.ErrNotFound, p.root)
		}
		return nil, fmt.Errorf("kyma: read bindings root %q: %w", p.root, err)
	}

	var lastErr error
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "..") {
			continue
		}
		if name != "" && e.Name() != name {
			continue
		}
		dir := filepath.Join(p.root, e.Name())
		files, err := readBindingDir(dir)
		if err != nil {
			lastErr = err
			continue
		}
		if !strings.EqualFold(files["type"], wantType) {
			continue
		}
		return files, nil
	}
	if lastErr != nil {
		return nil, fmt.Errorf("kyma: %w (last error: %v)", binding.ErrNotFound, lastErr)
	}
	if name != "" {
		return nil, fmt.Errorf("%w: no binding directory %q under %s", binding.ErrNotFound, name, p.root)
	}
	return nil, fmt.Errorf("%w: no binding of type %q under %s", binding.ErrNotFound, wantType, p.root)
}

// readBindingDir reads each regular file in dir and returns a key→value map
// keyed by filename (whitespace-trimmed). Skips Kubernetes atomic-update
// sentinels (any entry whose name starts with ".."). If a single
// "credentials" file is present and contains a JSON object, its top-level
// keys are merged in as fallbacks for keys not present as separate files —
// this handles older SAP BTP Service Operator versions that project
// credentials as a single JSON blob rather than per-key files.
func readBindingDir(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), "..") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out[e.Name()] = strings.TrimSpace(string(data))
	}
	if blob, ok := out["credentials"]; ok && strings.HasPrefix(blob, "{") {
		var nested map[string]any
		if json.Unmarshal([]byte(blob), &nested) == nil {
			for k, v := range nested {
				if _, exists := out[k]; exists {
					continue
				}
				switch x := v.(type) {
				case string:
					out[k] = x
				case float64:
					out[k] = strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", x), "0"), ".")
				case bool:
					out[k] = fmt.Sprintf("%t", x)
				}
			}
		}
	}
	return out, nil
}
