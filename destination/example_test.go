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
	fmt.Printf("host=%s port=%s user=%s\n", dest.Host, dest.Port, dest.ResolvedUser())
}

func ExampleDestination_PortNum() {
	// dest is resolved via client.Find; constructed directly here for illustration.
	dest := &destination.Destination{Port: "22"}

	portNum, err := dest.PortNum()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(portNum)
	// Output:
	// 22
}

func ExampleDestination_ResolvedUser() {
	// ResolvedUser checks the top-level User field first, then Properties["User"].
	dest := &destination.Destination{
		Properties: map[string]string{"User": "sftpuser"},
	}
	fmt.Println(dest.ResolvedUser())
	// Output:
	// sftpuser
}

func ExampleDestination_ResolvedPassword() {
	// ResolvedPassword checks the top-level Password field first, then Properties["Password"].
	dest := &destination.Destination{
		Properties: map[string]string{"Password": "secret"},
	}
	fmt.Println(dest.ResolvedPassword())
	// Output:
	// secret
}
