// Package sshclient opens SSH connections to hosts reachable through the SAP
// BTP Connectivity Service's SOCKS5 proxy. It maps a Destination Service
// entry (User/Password/sshKey) onto an *ssh.ClientConfig, dials via the
// supplied connectivity.Dialer, and optionally retries transient handshake
// failures (the on-prem sshd's MaxStartups can probabilistically drop
// connections under burst load).
//
// Basic usage — dial an on-prem host registered in Cloud Connector:
//
//	import (
//	    "github.com/bluefunda/btp-go/connectivity"
//	    "github.com/bluefunda/btp-go/destination"
//	    "github.com/bluefunda/btp-go/sshclient"
//	)
//
//	dialer := connectivity.NewDialer(connectivity.Config{...})
//	dest, _ := destClient.Find(ctx, "MY_SFTP_DEST")
//
//	sshc, err := sshclient.Dial(ctx, sshclient.Config{
//	    Dialer:    dialer,
//	    RetryOpts: sshclient.RetryOpts{MaxAttempts: 4, BaseDelay: 200 * time.Millisecond, Jitter: true},
//	}, dest)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer sshc.Close()
//	// sshc is *ssh.Client — wrap with pkg/sftp, github.com/melbahja/goph,
//	// or use ssh.NewSession() directly.
//
// The package depends only on golang.org/x/crypto/ssh plus the sibling
// btp-go modules. It does not pull in any specific SFTP/SCP library, so
// callers can pick their own.
package sshclient
