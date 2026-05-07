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

// ---- Finder interface test ----

// stubFinder is a test double that satisfies the Finder interface.
type stubFinder struct {
	dest *Destination
	err  error
}

func (s *stubFinder) Find(_ context.Context, _ string) (*Destination, error) {
	return s.dest, s.err
}

func TestFinderInterface_StubSatisfiesInterface(t *testing.T) {
	var f Finder = &stubFinder{dest: &Destination{Name: "STUB"}}
	d, err := f.Find(context.Background(), "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Name != "STUB" {
		t.Errorf("Name = %q, want STUB", d.Name)
	}
}

func TestFinderInterface_ClientSatisfiesInterface(t *testing.T) {
	srv := newTestServer(t, http.StatusOK, fixtureResponse)
	defer srv.Close()
	// *Client must be assignable to Finder — verified at compile time by the
	// var _ Finder = (*Client)(nil) assertion in client.go.
	var f Finder = NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	d, err := f.Find(context.Background(), "MY_SFTP")
	if err != nil {
		t.Fatalf("Find via Finder: %v", err)
	}
	if d.Name != "MY_SFTP" {
		t.Errorf("Name = %q, want MY_SFTP", d.Name)
	}
}

// ---- BestAuthToken tests ----

func TestBestAuthToken_ReturnsFirstValidToken(t *testing.T) {
	d := &Destination{
		AuthTokens: []AuthToken{
			{Type: "Bearer", Value: "good", Error: ""},
			{Type: "Bearer", Value: "also-good", Error: ""},
		},
	}
	tok, ok := d.BestAuthToken()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if tok.Value != "good" {
		t.Errorf("Value = %q, want good", tok.Value)
	}
}

func TestBestAuthToken_SkipsErrorTokens(t *testing.T) {
	d := &Destination{
		AuthTokens: []AuthToken{
			{Type: "Bearer", Value: "bad", Error: "token fetch failed"},
			{Type: "Bearer", Value: "good", Error: ""},
		},
	}
	tok, ok := d.BestAuthToken()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if tok.Value != "good" {
		t.Errorf("Value = %q, want good", tok.Value)
	}
}

func TestBestAuthToken_EmptySliceReturnsFalse(t *testing.T) {
	d := &Destination{}
	_, ok := d.BestAuthToken()
	if ok {
		t.Error("expected ok=false for empty AuthTokens")
	}
}

func TestBestAuthToken_AllErrorsReturnsFalse(t *testing.T) {
	d := &Destination{
		AuthTokens: []AuthToken{
			{Error: "err1"},
			{Error: "err2"},
		},
	}
	_, ok := d.BestAuthToken()
	if ok {
		t.Error("expected ok=false when all tokens have errors")
	}
}

// ---- ListAll tests ----

// listFixture serves different JSON depending on the URL path.
func listFixture(t *testing.T, subaccountBody, instanceBody interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing Authorization header")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/destination-configuration/v1/subaccountDestinations":
			json.NewEncoder(w).Encode(subaccountBody) //nolint:errcheck
		case r.URL.Path == "/destination-configuration/v1/instanceDestinations":
			json.NewEncoder(w).Encode(instanceBody) //nolint:errcheck
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

var subaccountDests = []map[string]string{
	{"Name": "SUB_A", "Type": "HTTP", "URL": "https://a.example.com", "ProxyType": "Internet"},
	{"Name": "SUB_B", "Type": "TCP", "host": "b.internal", "port": "22"},
}
var instanceDests = []map[string]string{
	{"Name": "INST_X", "Type": "HTTP", "URL": "https://x.example.com", "ProxyType": "Internet"},
}

func TestListAll_ReturnsMergedResults(t *testing.T) {
	srv := listFixture(t, subaccountDests, instanceDests)
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	dests, err := client.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error: %v", err)
	}

	if len(dests) != 3 {
		t.Fatalf("expected 3 destinations, got %d", len(dests))
	}
	// Subaccount results come first.
	if dests[0].Name != "SUB_A" {
		t.Errorf("[0].Name = %q, want SUB_A", dests[0].Name)
	}
	if dests[1].Name != "SUB_B" {
		t.Errorf("[1].Name = %q, want SUB_B", dests[1].Name)
	}
	if dests[2].Name != "INST_X" {
		t.Errorf("[2].Name = %q, want INST_X", dests[2].Name)
	}
}

func TestListAll_EmptyScopesReturnEmptySlice(t *testing.T) {
	srv := listFixture(t, []map[string]string{}, []map[string]string{})
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	dests, err := client.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error: %v", err)
	}
	if len(dests) != 0 {
		t.Errorf("expected 0 destinations, got %d", len(dests))
	}
}

func TestListAll_SubaccountErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/destination-configuration/v1/subaccountDestinations" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]")) //nolint:errcheck
	}))
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	_, err := client.ListAll(context.Background())
	if err == nil {
		t.Fatal("expected error when subaccount scope returns 403")
	}
}

func TestListAll_ExtractsFieldsCorrectly(t *testing.T) {
	srv := listFixture(t, subaccountDests, []map[string]string{})
	defer srv.Close()

	client := NewClient(srv.URL, &staticTokenSource{tok: "tok"}, nil)
	dests, err := client.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll() error: %v", err)
	}
	if dests[0].URL != "https://a.example.com" {
		t.Errorf("URL = %q, want https://a.example.com", dests[0].URL)
	}
	if dests[1].Host != "b.internal" || dests[1].Port != "22" {
		t.Errorf("Host/Port = %q/%q, want b.internal/22", dests[1].Host, dests[1].Port)
	}
}
