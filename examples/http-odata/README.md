# http-odata example

[![Go Reference](https://pkg.go.dev/badge/github.com/bluefunda/btp-go/examples/http-odata.svg)](https://pkg.go.dev/github.com/bluefunda/btp-go/examples/http-odata)

A minimal Cloud Foundry application demonstrating `httpclient` wired end-to-end
with SAP BTP service bindings. It exposes two endpoints:

- `GET /odata?destination=<name>&path=<odata-path>` — reads an OData resource
  (auth token injected automatically from the destination)
- `POST /odata?destination=<name>&path=<odata-path>` — fetches a CSRF token,
  then writes to the resource (OData v2 flow with cookie-jar session)
- `GET /healthz` — liveness check

## How it differs from sftp-count

`sftp-count` uses `connectivity.Dialer` (SOCKS5, port 20004) for raw TCP.
This example uses `httpclient.HTTPProxyConfig` (HTTP CONNECT, port 20003),
which is the correct proxy mode for HTTP/HTTPS destinations whose Cloud
Connector row is configured with Protocol=HTTP.

## Prerequisites

- SAP BTP Cloud Foundry subaccount
- CF CLI installed and logged in
- An HTTP destination configured in the BTP Destination cockpit

## Service instances

```bash
cf create-service xsuaa application my-xsuaa
cf create-service connectivity lite my-connectivity
cf create-service destination lite my-destination
```

Update `manifest.yml` if your instance names differ.

## Destination configuration

Create an HTTP destination in the BTP cockpit:

| Field | Example value |
|-------|---------------|
| Name | MY_HTTP_DEST |
| Type | HTTP |
| URL | https://my-system.corp.internal/sap/opu/odata/sap/MY_SERVICE |
| Proxy Type | OnPremise |
| Authentication | BasicAuthentication / OAuth2ClientCredentials / NoAuthentication |

For Internet destinations set Proxy Type = Internet; the HTTP CONNECT proxy
is skipped automatically.

## Build and push

```bash
cd examples/http-odata
cf push
```

## Manual verification

```bash
APP_URL=$(cf app btp-go-http-odata | grep routes | awk '{print $2}')

# Liveness
curl https://${APP_URL}/healthz

# OData read — service document
curl "https://${APP_URL}/odata?destination=MY_HTTP_DEST"

# OData read — entity set
curl "https://${APP_URL}/odata?destination=MY_HTTP_DEST&path=/Items?%24top=5"

# OData write (CSRF fetched automatically)
curl -X POST \
  "https://${APP_URL}/odata?destination=MY_HTTP_DEST&path=/Items" \
  -H "Content-Type: application/json" \
  -d '{"Name":"test","Value":"42"}'
```
