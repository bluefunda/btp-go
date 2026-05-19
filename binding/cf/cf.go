// Package cf implements binding.Provider by parsing the VCAP_SERVICES
// environment variable present in Cloud Foundry application instances.
//
// Service instances are looked up by service label ("connectivity",
// "destination", "xsuaa"). When multiple instances of the same label exist,
// pass the instance name to Binding to select the correct one; pass "" to
// select the first.
package cf

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/bluefunda/btp-go/binding"
)

// vcapServices is the top-level structure of VCAP_SERVICES JSON.
type vcapServices map[string][]vcapInstance

type vcapInstance struct {
	Name        string          `json:"name"`
	Credentials json.RawMessage `json:"credentials"`
}

// Provider implements binding.Provider for Cloud Foundry VCAP_SERVICES.
type Provider struct {
	svc vcapServices
}

// NewProvider parses VCAP_SERVICES from the environment and returns a Provider.
// Returns an error if the environment variable is absent or malformed.
func NewProvider() (*Provider, error) {
	raw := os.Getenv("VCAP_SERVICES")
	if raw == "" {
		return nil, fmt.Errorf("cf: VCAP_SERVICES is not set")
	}
	var svc vcapServices
	if err := json.Unmarshal([]byte(raw), &svc); err != nil {
		return nil, fmt.Errorf("cf: parse VCAP_SERVICES: %w", err)
	}
	return &Provider{svc: svc}, nil
}

// findCredentials returns the raw credentials JSON for the named service
// instance. If name is "", it returns the first instance under the label.
func (p *Provider) findCredentials(label, name string) (json.RawMessage, error) {
	instances, ok := p.svc[label]
	if !ok || len(instances) == 0 {
		return nil, fmt.Errorf("%w: service %q not found in VCAP_SERVICES", binding.ErrNotFound, label)
	}
	if name == "" {
		return instances[0].Credentials, nil
	}
	for _, inst := range instances {
		if inst.Name == name {
			return inst.Credentials, nil
		}
	}
	return nil, fmt.Errorf("%w: service %q instance %q not found", binding.ErrNotFound, label, name)
}

// Binding returns the raw key/value credentials for the named service instance.
// Numeric JSON values are converted to their decimal string representation.
func (p *Provider) Binding(serviceType, name string) (map[string]string, error) {
	raw, err := p.findCredentials(serviceType, name)
	if err != nil {
		return nil, err
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("cf: decode %s credentials: %w", serviceType, err)
	}
	out := make(map[string]string, len(obj))
	for k, v := range obj {
		switch x := v.(type) {
		case string:
			out[k] = x
		case float64:
			out[k] = strconv.FormatFloat(x, 'f', -1, 64)
		case bool:
			out[k] = strconv.FormatBool(x)
		}
	}
	return out, nil
}
