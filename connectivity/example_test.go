package connectivity_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/bluefunda/btp-go/connectivity"
)

func ExampleDialer_Dial() {
	// TokenSource is typically xsuaa.NewClientCredentialsSource(...).
	// Pass nil to fall back to SOCKS5 no-auth (method 0x00).
	dialer := connectivity.NewDialer(connectivity.Config{
		ProxyHost: "connectivityproxy.internal.cf.eu10.hana.ondemand.com",
		ProxyPort: "20004",
		// TokenSource: myXsuaaSource,
	})

	conn, err := dialer.Dial(context.Background(), "internal-sftp.corp", 22, "")
	if err != nil {
		var se *connectivity.StageError
		if errors.As(err, &se) {
			fmt.Printf("failed at stage: %s\n", se.Stage)
		}
		return
	}
	defer conn.Close()
	// conn is a plain net.Conn; layer SSH, SFTP, or any protocol on top.
	_ = conn
}

func ExampleREPMessage() {
	fmt.Println(connectivity.REPMessage(0x00)) // succeeded
	fmt.Println(connectivity.REPMessage(0x05)) // connection refused
	// Output:
	// succeeded
	// connection refused
}
