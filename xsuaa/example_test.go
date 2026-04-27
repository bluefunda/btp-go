package xsuaa_test

import (
	"context"
	"fmt"
	"log"

	"github.com/bluefunda/btp-go/xsuaa"
)

func ExampleNewClientCredentialsSource() {
	src := xsuaa.NewClientCredentialsSource(xsuaa.Config{
		ClientID:     "sb-myapp!t1234",
		ClientSecret: "secret",
		TokenURL:     "https://my-tenant.authentication.eu10.hana.ondemand.com/oauth/token",
	})

	token, err := src.Token(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	// token is a bearer JWT; pass it to downstream service calls.
	_ = fmt.Sprintf("Bearer %s", token)
}
