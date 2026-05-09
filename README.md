# btp-go

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
    └── xsuaa          (token source)

connectivity ─── (local TokenSource interface, satisfied by xsuaa)
destination  ─── (local TokenSource interface, satisfied by xsuaa)
sshclient    ─── (local Dialer interface, satisfied by connectivity.Dialer)
httpclient   ─── (local Dialer + TokenSource interfaces)

Zero cross-module imports among the library modules.
Each module is independently go get-able by semver tag.
```

## Quickstart

**SFTP over Cloud Connector** — [`examples/sftp-count`](examples/sftp-count/)
is a complete Cloud Foundry app that counts files on a remote SFTP server via
the Cloud Connector, exercising `binding`, `xsuaa`, `connectivity`, and
`destination` end-to-end.

```bash
cd examples/sftp-count
cf push
curl "https://<app-route>/sftp/count?destination=MY_SFTP_DEST"
```

**HTTP / OData destinations** — see the package-level examples in
[`httpclient`](httpclient/example_test.go): wiring a destination to an
`*http.Client`, calling OData, and the CSRF-token / cookie-jar write flow.

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

## Support model

`btp-go` is maintained by BlueFunda. Security fixes are released within 7 days
of disclosure. Feature requests are evaluated quarterly. Please open a GitHub
issue for bug reports or enhancement proposals.

## License

Apache-2.0. See [LICENSE](LICENSE).
