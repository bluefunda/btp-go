package destination_test

import (
	"context"
	"fmt"
	"log"

	"github.com/bluefunda/btp-go/destination"
)

// staticToken is a stub TokenSource for illustration.
type staticToken struct{ value string }

func (s staticToken) Token(_ context.Context) (string, error) { return s.value, nil }

func ExampleClient_Find() {
	client := destination.NewClient(
		"https://destination-configuration.cfapps.eu10.hana.ondemand.com",
		staticToken{"my-bearer-token"},
		nil, // use http.DefaultClient
	)

	dest, err := client.Find(context.Background(), "MY_SFTP_DEST")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("host=%s port=%s user=%s\n", dest.Host, dest.Port, dest.User)
}
