# sftp-count example

[![Go Reference](https://pkg.go.dev/badge/github.com/bluefunda/btp-go/examples/sftp-count.svg)](https://pkg.go.dev/github.com/bluefunda/btp-go/examples/sftp-count)

A minimal Cloud Foundry application that exposes:

- `GET /sftp/count?destination=<name>` — returns the number of files in the remote directory configured on the named BTP destination.
- `GET /healthz` — liveness check.

## About this example

`sftp-count` wires SSH at the **primitive level**: it calls `dest.PortNum()` to
parse the port, `dest.ResolvedUser()` / `dest.ResolvedPassword()` to get
credentials, `connectivity.Dialer.Dial` to open a SOCKS5 tunnel, and then
builds `ssh.ClientConfig` by hand, calls `ssh.NewClientConn`, and assembles
`ssh.NewClient` itself. There is no retry logic for transient sshd
`MaxStartups` rejections.

This is intentional — it exists to show how the low-level `connectivity` and
`destination` primitives compose with `golang.org/x/crypto/ssh` when you need
full control over the SSH handshake.

**For new code, prefer [`examples/sftp-sshclient`](../sftp-sshclient/) instead.**
That example uses `sshclient.Dial`, which wraps all of the above — port parsing,
`ssh.ClientConfig` assembly from destination properties, and exponential-backoff
retry — in a single call:

```go
sshc, err := sshclient.Dial(ctx, sshclient.Config{
    Dialer:    dialer,
    RetryOpts: sshclient.RetryOpts{MaxAttempts: 4, BaseDelay: 200 * time.Millisecond, Jitter: true},
}, dest)
```

## Prerequisites

- SAP BTP Cloud Foundry subaccount
- Cloud Connector connected to the subaccount with a virtual mapping for the SFTP host
- CF CLI installed and logged in to your CF org/space

## Service instances

Create the required service instances (adjust plan names for your region):

```bash
cf create-service xsuaa application my-xsuaa
cf create-service connectivity lite my-connectivity
cf create-service destination lite my-destination
```

Update `manifest.yml` if your instance names differ.

## Destination configuration

In the BTP Destination cockpit (or via the Destinations API), create a destination of type **TCP** / **OnPremise** with:

| Field | Example value |
|-------|---------------|
| Name | MY_SFTP_DEST |
| Type | TCP |
| Proxy Type | OnPremise |
| Host | sftp.corp.internal |
| Port | 22 |
| User (Additional Property) | sftpuser |
| RemotePath (Additional Property) | /incoming |
| sshKey (Additional Property) | -----BEGIN OPENSSH PRIVATE KEY----- ... |

## Build and push

```bash
cd examples/sftp-count
cf push
```

The `go_buildpack` will run `go build .` inside the workspace module layout.

## Manual verification

After `cf push` succeeds:

```bash
APP_URL=$(cf app btp-go-sftp-count | grep routes | awk '{print $2}')

# Liveness check
curl https://${APP_URL}/healthz
# Expected: {"ok":true}

# File count
curl "https://${APP_URL}/sftp/count?destination=MY_SFTP_DEST"
# Expected: {"destination":"MY_SFTP_DEST","remotePath":"/incoming","count":42,"elapsedMs":312}
```

Compare the `count` value with what the reference service returns for the same destination to confirm identical behaviour.
