package cf

import (
	"errors"
	"testing"

	"github.com/bluefunda/btp-go/binding"
)

const fixtureVCAP = `{
  "connectivity": [
    {
      "name": "my-connectivity",
      "credentials": {
        "clientid": "conn-id",
        "clientsecret": "conn-secret",
        "token_service_url": "https://xsuaa.example.com",
        "onpremise_proxy_host": "connectivityproxy.internal",
        "onpremise_socks5_proxy_port": "20004",
        "onpremise_proxy_http_port": "20003"
      }
    }
  ],
  "destination": [
    {
      "name": "my-destination",
      "credentials": {
        "clientid": "dest-id",
        "clientsecret": "dest-secret",
        "uri": "https://destination-configuration.cfapps.eu10.hana.ondemand.com",
        "token_service_url": "https://xsuaa.example.com"
      }
    }
  ],
  "xsuaa": [
    {
      "name": "my-xsuaa-primary",
      "credentials": {
        "clientid": "xsuaa-id-1",
        "clientsecret": "xsuaa-secret-1",
        "url": "https://my-tenant.authentication.eu10.hana.ondemand.com",
        "uaadomain": "authentication.eu10.hana.ondemand.com"
      }
    },
    {
      "name": "my-xsuaa-secondary",
      "credentials": {
        "clientid": "xsuaa-id-2",
        "clientsecret": "xsuaa-secret-2",
        "url": "https://my-tenant-2.authentication.eu10.hana.ondemand.com",
        "uaadomain": "authentication.eu10.hana.ondemand.com"
      }
    }
  ]
}`

func makeProvider(t *testing.T) *Provider {
	t.Helper()
	t.Setenv("VCAP_SERVICES", fixtureVCAP)
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("NewProvider(): %v", err)
	}
	return p
}

func TestConnectivityBinding(t *testing.T) {
	p := makeProvider(t)
	b, err := p.Connectivity("")
	if err != nil {
		t.Fatalf("Connectivity: %v", err)
	}
	if b.ClientID != "conn-id" {
		t.Errorf("ClientID: got %q, want %q", b.ClientID, "conn-id")
	}
	if b.OnpremiseProxyHost != "connectivityproxy.internal" {
		t.Errorf("OnpremiseProxyHost: got %q", b.OnpremiseProxyHost)
	}
	if b.OnpremiseProxySocks5Port != "20004" {
		t.Errorf("OnpremiseProxySocks5Port: got %q", b.OnpremiseProxySocks5Port)
	}
	if b.OnpremiseProxyHTTPPort != "20003" {
		t.Errorf("OnpremiseProxyHTTPPort: got %q", b.OnpremiseProxyHTTPPort)
	}
}

func TestDestinationBinding(t *testing.T) {
	p := makeProvider(t)
	b, err := p.Destination("")
	if err != nil {
		t.Fatalf("Destination: %v", err)
	}
	if b.ClientID != "dest-id" {
		t.Errorf("ClientID: got %q, want %q", b.ClientID, "dest-id")
	}
	if b.URI != "https://destination-configuration.cfapps.eu10.hana.ondemand.com" {
		t.Errorf("URI: got %q", b.URI)
	}
}

func TestXSUAABinding(t *testing.T) {
	p := makeProvider(t)
	b, err := p.XSUAA("")
	if err != nil {
		t.Fatalf("XSUAA: %v", err)
	}
	// "" selects the first instance.
	if b.ClientID != "xsuaa-id-1" {
		t.Errorf("ClientID: got %q, want %q", b.ClientID, "xsuaa-id-1")
	}
	if b.UAADomain != "authentication.eu10.hana.ondemand.com" {
		t.Errorf("UAADomain: got %q", b.UAADomain)
	}
}

func TestXSUAABinding_SelectByName(t *testing.T) {
	p := makeProvider(t)
	b, err := p.XSUAA("my-xsuaa-secondary")
	if err != nil {
		t.Fatalf("XSUAA(secondary): %v", err)
	}
	if b.ClientID != "xsuaa-id-2" {
		t.Errorf("ClientID: got %q, want %q", b.ClientID, "xsuaa-id-2")
	}
}

func TestErrNotFound_MissingService(t *testing.T) {
	// Provide VCAP with no connectivity entry.
	t.Setenv("VCAP_SERVICES", `{"xsuaa":[{"name":"x","credentials":{"clientid":"i","clientsecret":"s","url":"u","uaadomain":"d"}}]}`)
	p, err := NewProvider()
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	_, err = p.Connectivity("")
	if !errors.Is(err, binding.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestErrNotFound_WrongInstanceName(t *testing.T) {
	p := makeProvider(t)
	_, err := p.XSUAA("nonexistent-name")
	if !errors.Is(err, binding.ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong name, got %v", err)
	}
}
