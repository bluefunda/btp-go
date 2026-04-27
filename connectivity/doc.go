// Package connectivity opens TCP tunnels through SAP Cloud Connector using
// the Connectivity Service's SOCKS5 proxy with SAP's proprietary 0x80
// authentication method.
//
// Basic usage — dial a virtual host registered in Cloud Connector:
//
//	import "github.com/bluefunda/btp-go/connectivity"
//
//	dialer := connectivity.NewDialer(connectivity.Config{
//	    ProxyHost:   "connectivityproxy.internal.cf.eu10.hana.ondemand.com",
//	    ProxyPort:   "20004",
//	    TokenSource: myXsuaaSource, // satisfies connectivity.TokenSource
//	})
//
//	conn, err := dialer.Dial(ctx, "internal-host.corp", 22, "")
//	if err != nil {
//	    // errors.As(err, &connectivity.StageError{}) reveals which stage failed
//	    log.Fatal(err)
//	}
//	defer conn.Close()
//	// conn is now a plain net.Conn ready for SSH, SFTP, or any protocol.
//
// The package is stdlib-only and has zero third-party dependencies.
package connectivity
