# btp-go

[![Go Reference](https://pkg.go.dev/badge/github.com/bluefunda/btp-go.svg)](https://pkg.go.dev/github.com/bluefunda/btp-go)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

A Go workspace monorepo of small, focused, stdlib-only libraries for consuming
SAP BTP (Business Technology Platform) services from Go applications — on Cloud
Foundry, Kyma, or any Go-capable runtime.

## Module overview

| Module | Import path | Purpose |
|--------|-------------|---------|
| `connectivity` | `github.com/bluefunda/btp-go/connectivity` | Open TCP tunnels through SAP Cloud Connector via the SOCKS5 proxy (SAP 0x80 auth) |
| `xsuaa` | `github.com/bluefunda/btp-go/xsuaa` | Fetch and cache client-credentials tokens from SAP XSUAA |
| `destination` | `github.com/bluefunda/btp-go/destination` | Look up named destinations from the SAP Destination Service REST API |
| `binding` | `github.com/bluefunda/btp-go/binding` | Parse service bindings from VCAP_SERVICES (CF), Kyma, or auto-detect |
| `sshclient` | `github.com/bluefunda/btp-go/sshclient` | High-level SSH/SFTP client over the Cloud Connector tunnel (wraps connectivity + destination) |
| `httpclient` | `github.com/bluefunda/btp-go/httpclient` | HTTP/REST/OData client wired for a Destination's auth + Cloud Connector tunnel; OData v2 CSRF helper |
| `ingest` | `github.com/bluefunda/btp-go/ingest` | Generic asynchronous HTTP → NATS JetStream ingestion proxy: idempotent `/ingest` endpoint, durable publisher with backpressure |

All modules are stdlib-only except `sshclient` (`golang.org/x/crypto`) and
`ingest` (`github.com/nats-io/nats.go`).

## Dependency diagram

```
consumer app
    ├── binding/auto   (detects CF vs Kyma at runtime)
    │   ├── binding/cf    (VCAP_SERVICES)
    │   └── binding/kyma  (Servicebinding.io file layout)
    ├── connectivity   (SOCKS5 dialer — SAP 0x80 auth)
    ├── destination    (Destination Service REST client)
    ├── sshclient      (SSH/SFTP over Cloud Connector tunnel)
    ├── httpclient     (HTTP/OData client with auth + proxy wiring)
    ├── ingest         (generic HTTP → JetStream ingestion gateway; standalone service)
    └── xsuaa          (token source)

connectivity ─── (local TokenSource interface, satisfied by xsuaa)
destination  ─── (local TokenSource interface, satisfied by xsuaa)
sshclient    ─── (local Dialer interface, satisfied by connectivity.Dialer)
httpclient   ─── (local Dialer + TokenSource interfaces)

Zero cross-module imports among the library modules.
Each module is independently go get-able by semver tag.
```

### Binding API

The `binding.Provider` interface has a single method:

```go
type Provider interface {
    Binding(serviceType, name string) (map[string]string, error)
}
```

Typed credentials are returned by package-level extractor functions:

```go
import (
    "github.com/bluefunda/btp-go/binding"
    "github.com/bluefunda/btp-go/binding/auto"
)

prov, _ := auto.NewProvider()

cb, _ := binding.Connectivity(prov, "")   // *binding.ConnectivityBinding
db, _ := binding.Destination(prov, "")   // *binding.DestinationBinding
xb, _ := binding.XSUAA(prov, "")         // *binding.XSUAABinding
```

Adding support for a new SAP service type requires only a new extractor
function, not a change to the `Provider` interface.

### Destination API

`destination.Destination` exposes helper methods for common field access:

```go
import "github.com/bluefunda/btp-go/destination"

dest, _ := client.Find(ctx, "MY_DEST")

portNum, err := dest.PortNum()       // parses Port as uint16
user    := dest.ResolvedUser()       // checks User field, then Properties["User"]
pass    := dest.ResolvedPassword()   // checks Password field, then Properties["Password"]
```

These helpers remove the need for inline `strconv.ParseUint` calls and manual
`Properties` map lookups in calling code.

## Quickstart

**SFTP over Cloud Connector** — [`examples/sftp-count`](examples/sftp-count/) [![Go Reference](https://pkg.go.dev/badge/github.com/bluefunda/btp-go/examples/sftp-count.svg)](https://pkg.go.dev/github.com/bluefunda/btp-go/examples/sftp-count)

A complete Cloud Foundry app that counts files on a remote SFTP server via
the Cloud Connector, exercising `binding`, `xsuaa`, `connectivity`, and
`destination` end-to-end.

```bash
cd examples/sftp-count
cf push
curl "https://<app-route>/sftp/count?destination=MY_SFTP_DEST"
```

**HTTP / OData destinations** — [`examples/http-odata`](examples/http-odata/) [![Go Reference](https://pkg.go.dev/badge/github.com/bluefunda/btp-go/examples/http-odata.svg)](https://pkg.go.dev/github.com/bluefunda/btp-go/examples/http-odata)

A complete Cloud Foundry app that proxies OData reads and writes through
`httpclient`, wired via the Connectivity HTTP CONNECT proxy (port 20003) with
automatic CSRF-token / cookie-jar handling.

```bash
cd examples/http-odata
cf push
curl "https://<app-route>/odata?destination=MY_HTTP_DEST&path=/Items?%24top=5"
curl -X POST "https://<app-route>/odata?destination=MY_HTTP_DEST&path=/Items" \
  -H "Content-Type: application/json" -d '{"Name":"test"}'
```

**HTTP → JetStream ingestion proxy** — [`ingest`](ingest/) [![Go Reference](https://pkg.go.dev/badge/github.com/bluefunda/btp-go/ingest.svg)](https://pkg.go.dev/github.com/bluefunda/btp-go/ingest)

A runnable microservice (and reusable layers) that accepts business events
over HTTP from any upstream caller, suppresses duplicates via a JetStream KV
bucket, and publishes them durably with publisher confirms and backpressure.
That is the whole job — it does not consume the stream back out or relay
anywhere; whatever reads `events.>` afterward is a separate concern. A common
deployment sits in front of an SAP ABAP system's HTTP destination
configuration, but nothing about the gateway assumes SAP.

```bash
docker run --rm -p 4222:4222 nats:2.10-alpine -js
cd ingest && API_KEYS=local-dev-key-0123456789 IDEMPOTENCY_BACKEND=memory go run ./cmd/gateway
curl -X POST localhost:8080/ingest -H "X-API-Key: local-dev-key-0123456789" \
  -H 'Content-Type: application/json' \
  -d '{"business_object":"BILLING_DOC","operation":"CREATE","key":"EVT-1928340192",
       "timestamp":"2023-10-27T10:00:00Z","payload":{"amount":"1234.56"}}'
```

**SFTP via sshclient** — [`examples/sftp-sshclient`](examples/sftp-sshclient/)
shows the same SFTP count use-case as `sftp-count` but uses `sshclient.Dial`
(the high-level wrapper with retry) instead of raw `golang.org/x/crypto/ssh`.

```bash
cd examples/sftp-sshclient
cf push
curl "https://<app-route>/sftp/count?destination=MY_SFTP_DEST"
```

## Local development

```bash
git clone https://github.com/bluefunda/btp-go
cd btp-go
go build ./...
go test ./...
```

## Milestones

| Milestone | Status | Description |
|-----------|--------|-------------|
| **M1** | ✅ shipped | Core modules: `xsuaa`, `connectivity`, `destination`, `binding` (CF + Kyma), `sshclient` |
| **M2** | planned | Oracle consumer using `go-ora` (pure Go), layers cleanly on `net.Conn` from `connectivity.Dial` |
| **M3** | ✅ shipped | HTTP destination support — `httpclient.New(dest, cfg)` returns a configured `*http.Client` (auth headers, Cloud Connector dialer, cookie jar) plus `FetchCSRF` for OData v2 writes |
| **M4** | ✅ shipped | Kyma `binding` provider — reads Servicebinding.io file-mounted secrets; handles per-key files, JSON-blob fallback, and Kubernetes atomic-update sentinels |
| **M5** | planned | Principal propagation and user-token-exchange flows in `xsuaa` |
| **M6** | ✅ shipped | `ingest` — generic asynchronous HTTP → NATS JetStream ingestion proxy: idempotent HTTP ingestion, durable publishing with confirms and backpressure |

## Support model

`btp-go` is maintained by BlueFunda. Security fixes are released within 7 days
of disclosure. Feature requests are evaluated quarterly. Please open a GitHub
issue for bug reports or enhancement proposals.

## License

Apache-2.0. See [LICENSE](LICENSE).

Authored by Amish Kushwaha, open-sourced under Apache 2.0 by BlueFunda, Inc.
