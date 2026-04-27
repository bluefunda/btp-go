package destination

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// staticTokenSource always returns the same token.
type staticTokenSource struct{ tok string }

func (s *staticTokenSource) Token(_ context.Context) (string, error) { return s.tok, nil }

// fixtureResponse is the JSON returned by the test server.
var fixtureResponse = map[string]interface{}{
	"destinationConfiguration": map[string]string{
		"Name":                     "MY_SFTP",
		"Type":                     "TCP",
		"ProxyType":                "OnPremise",
		"Authentication":           "NoAuthentication",
		"URL":                      "",
		"host":                     "sftp.internal",
		"port":                     "22",
		"CloudConnectorLocationId": "loc-eu",
		"User":                     "sftpuser",
		"RemotePath":               "/incoming",
		"ExtraKey":                 "extra-value",
	},
	"authTokens": []map[string]interface{}{
		{
			"type":       "Bearer",
			"value":      "authTok",
			"http_header": map[string]string{"key": "Authorization", "value": "Bearer authTok"},
			"expires_in": "3600",
			"error":      "",
		},
	},
}

func newTestServer(t *testing.T, status int, body interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(body)
	}))
}

func TestFind_ExtractsAllFields(t *testing.T) {
	srv := newTestServer(t, http.StatusOK, fixtureResponse)
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "bearer-tok"}, nil)
	dest, err := client.Find(context.Background(), "MY_SFTP")
	if err != nil {
		t.Fatalf("Find() error: %v", err)
	}

	checks := []struct {
		field string
		got   string
		want  string
	}{
		{"Name", dest.Name, "MY_SFTP"},
		{"Type", dest.Type, "TCP"},
		{"ProxyType", dest.ProxyType, "OnPremise"},
		{"Authentication", dest.Authentication, "NoAuthentication"},
		{"Host", dest.Host, "sftp.internal"},
		{"Port", dest.Port, "22"},
		{"CloudConnectorLocationID", dest.CloudConnectorLocationID, "loc-eu"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.field, c.got, c.want)
		}
	}
}

func TestFind_PropertiesHasRemainder(t *testing.T) {
	srv := newTestServer(t, http.StatusOK, fixtureResponse)
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	dest, err := client.Find(context.Background(), "MY_SFTP")
	if err != nil {
		t.Fatalf("Find() error: %v", err)
	}

	if dest.Properties["User"] != "sftpuser" {
		t.Errorf("Properties[User]: got %q, want %q", dest.Properties["User"], "sftpuser")
	}
	if dest.Properties["RemotePath"] != "/incoming" {
		t.Errorf("Properties[RemotePath]: got %q, want %q", dest.Properties["RemotePath"], "/incoming")
	}
	if dest.Properties["ExtraKey"] != "extra-value" {
		t.Errorf("Properties[ExtraKey]: got %q, want %q", dest.Properties["ExtraKey"], "extra-value")
	}
	// Known fields should not appear in Properties.
	for _, k := range []string{"Name", "Type", "ProxyType", "Authentication", "host", "port"} {
		if _, ok := dest.Properties[k]; ok {
			t.Errorf("known field %q should not appear in Properties", k)
		}
	}
}

func TestFind_AuthTokens(t *testing.T) {
	srv := newTestServer(t, http.StatusOK, fixtureResponse)
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	dest, err := client.Find(context.Background(), "MY_SFTP")
	if err != nil {
		t.Fatalf("Find() error: %v", err)
	}

	if len(dest.AuthTokens) != 1 {
		t.Fatalf("expected 1 auth token, got %d", len(dest.AuthTokens))
	}
	if dest.AuthTokens[0].Type != "Bearer" {
		t.Errorf("AuthTokens[0].Type = %q, want Bearer", dest.AuthTokens[0].Type)
	}
	if dest.AuthTokens[0].Value != "authTok" {
		t.Errorf("AuthTokens[0].Value = %q, want authTok", dest.AuthTokens[0].Value)
	}
}

func TestFind_Non200ReturnsError(t *testing.T) {
	srv := newTestServer(t, http.StatusNotFound, map[string]string{"error": "not found"})
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	_, err := client.Find(context.Background(), "MISSING")
	if err == nil {
		t.Fatal("expected error for non-200 response, got nil")
	}
}
