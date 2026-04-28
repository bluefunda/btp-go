package httpclient_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/bluefunda/btp-go/connectivity"
	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/httpclient"
)

// Example shows the standard use: resolve a destination, build a client,
// call an OData service.
func Example() {
	// Resolved destination from the BTP Destination Service. In real code
	// you'd call destination.Client.Find(ctx, "MY_HTTP_DEST").
	var dest *destination.Destination

	// Connectivity dialer for OnPremise destinations. nil for Internet.
	var dialer *connectivity.Dialer

	client, baseURL, err := httpclient.New(dest, httpclient.Config{
		Dialer:       dialer,
		Timeout:      15 * time.Second,
		ExtraHeaders: http.Header{"Accept": []string{"application/json"}},
	})
	if err != nil {
		log.Fatal(err)
	}

	resp, err := client.Get(baseURL + "/sap/opu/odata/sap/MY_SERVICE/Items?$top=5&$format=json")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	var payload struct {
		D struct {
			Results []map[string]any `json:"results"`
		} `json:"d"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("got %d items\n", len(payload.D.Results))
}

// ExampleFetchCSRF shows the OData v2 write flow: fetch a CSRF token,
// then submit a POST that carries the token + the cookie jar's session.
func ExampleFetchCSRF() {
	var client *http.Client
	var baseURL string

	csrf, err := httpclient.FetchCSRF(context.Background(), client, baseURL+"/sap/opu/odata/sap/MY_SERVICE/")
	if err != nil {
		log.Fatal(err)
	}

	body := io.NopCloser(nil) // your serialised payload
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/Items", body)
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	fmt.Println("created:", resp.Status)
}
