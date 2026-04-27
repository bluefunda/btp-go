package sshclient_test

import (
	"context"
	"log"
	"time"

	"github.com/bluefunda/btp-go/connectivity"
	"github.com/bluefunda/btp-go/destination"
	"github.com/bluefunda/btp-go/sshclient"
)

// Example_dial shows how to open an SSH client to an on-prem host through
// the SAP BTP Connectivity SOCKS5 proxy, with a small retry budget to
// absorb sshd MaxStartups rejections under burst load.
func Example_dial() {
	// Build the SOCKS5 dialer from a connectivity binding (token source omitted).
	dialer := connectivity.NewDialer(connectivity.Config{
		ProxyHost: "connectivityproxy.internal.cf.eu10.hana.ondemand.com",
		ProxyPort: "20004",
	})

	// Resolve the destination from the Destination Service (client construction omitted).
	var destClient *destination.Client // = destination.NewClient(...)
	dest, err := destClient.Find(context.Background(), "MY_SFTP_DEST")
	if err != nil {
		log.Fatal(err)
	}

	sshc, err := sshclient.Dial(context.Background(), sshclient.Config{
		Dialer: dialer,
		RetryOpts: sshclient.RetryOpts{
			MaxAttempts: 4,
			BaseDelay:   200 * time.Millisecond,
			Jitter:      true,
		},
	}, dest)
	if err != nil {
		log.Fatal(err)
	}
	defer sshc.Close()

	// sshc is *ssh.Client — wrap with pkg/sftp, run a session, port-forward, etc.
}
