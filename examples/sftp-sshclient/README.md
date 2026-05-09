# sftp-sshclient example

A minimal Cloud Foundry application that exposes:

- `GET /sftp/count?destination=<name>` — returns the file count from the remote
  directory configured on the named BTP destination
- `GET /healthz` — liveness check

## How it works

```
Startup — main.go
  binding/auto
    ├── xsuaa binding      ──► xsuaa.TokenSource
    ├── destination binding──► destination.Client   (Destination Service API)
    └── connectivity binding──► connectivity.Dialer (SOCKS5 proxy, port 20004)

Per request — GET /sftp/count?destination=MY_SFTP_DEST

  destination.Client.Find("MY_SFTP_DEST")
            │
            ▼
  Destination{host, port, user, sshKey, RemotePath}
            │
            ├─────────────── connectivity.Dialer
            ▼
  sshclient.Dial()      ← opens SOCKS5 tunnel through Cloud Connector
            │              and performs SSH handshake using dest credentials
            ▼
      *ssh.Client
            │
  sftp.NewClient(sshc)
            │
      *sftp.Client
            │
  ReadDir(RemotePath) ──► JSON {destination, remotePath, count, elapsedMs}
```

## How it differs from sftp-count

`sftp-count` dials raw SSH manually: it calls `connectivity.Dialer.Dial`,
builds `*ssh.ClientConfig` by hand, and calls `ssh.NewClientConn` — no retry.

This example uses `sshclient.Dial` instead — the high-level wrapper that
handles port parsing, SSH config assembly from destination properties
(`User`, `Password`, `sshKey`), and automatic retry on transient `MaxStartups`
rejections from sshd. The SFTP logic above the dial point is identical.

## Prerequisites

- SAP BTP Cloud Foundry subaccount
- Cloud Connector connected to the subaccount with a virtual mapping for the SFTP host
- CF CLI installed and logged in

## Service instances

```bash
cf create-service xsuaa application my-xsuaa
cf create-service connectivity lite my-connectivity
cf create-service destination lite my-destination
```

Update `manifest.yml` if your instance names differ.

## Destination configuration

Create a TCP destination in the BTP cockpit:

| Field | Example value |
|-------|---------------|
| Name | MY_SFTP_DEST |
| Type | TCP |
| Proxy Type | OnPremise |
| Host | sftp.corp.internal |
| Port | 22 |
| User (Additional Property) | sftpuser |
| RemotePath (Additional Property) | /incoming |
| sshKey (Additional Property) | -----BEGIN OPENSSH PRIVATE KEY----- … |

## Build and push

```bash
cd examples/sftp-sshclient
cf push
```

## Manual verification

```bash
APP_URL=$(cf app btp-go-sftp-sshclient | grep routes | awk '{print $2}')

curl https://${APP_URL}/healthz
# {"ok":true}

curl "https://${APP_URL}/sftp/count?destination=MY_SFTP_DEST"
# {"destination":"MY_SFTP_DEST","remotePath":"/incoming","count":42,"elapsedMs":280}
```
