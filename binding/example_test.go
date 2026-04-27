package binding_test

import (
	"fmt"
	"log"

	"github.com/bluefunda/btp-go/binding/auto"
	"github.com/bluefunda/btp-go/binding/cf"
)

func ExampleProvider() {
	// auto.NewProvider detects the runtime (CF vs Kyma) automatically.
	prov, err := auto.NewProvider()
	if err != nil {
		log.Fatal(err)
	}

	cb, err := prov.Connectivity("")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("proxy host:", cb.OnpremiseProxyHost)
}

func ExampleNewProvider_cloudFoundry() {
	// Use cf.NewProvider directly when you know the app runs on Cloud Foundry.
	prov, err := cf.NewProvider()
	if err != nil {
		log.Fatal(err)
	}

	db, err := prov.Destination("")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("destination URI:", db.URI)
}
