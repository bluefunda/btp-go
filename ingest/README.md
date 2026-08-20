# ingest — generic HTTP-to-NATS-JetStream ingestion proxy

[![Go Reference](https://pkg.go.dev/badge/github.com/bluefunda/btp-go/ingest.svg)](https://pkg.go.dev/github.com/bluefunda/btp-go/ingest)

An asynchronous ingestion proxy. Any upstream system posts a business event
over HTTP; the gateway validates it, suppresses duplicates, and publishes it
durably to NATS JetStream. That is the whole job — the gateway does not
consume the stream back out, does not relay to any downstream system, and
has no opinion on what reads the stream or when. It runs unchanged on Cloud
Foundry, Kyma, plain Kubernetes, or a laptop.

A common deployment sits in front of an SAP ABAP system's HTTP destination
configuration, but nothing about the gateway assumes SAP: any caller that
can POST JSON works.

```
caller  ──POST /ingest──▶  api  ──▶  idem  ──▶  broker.Publisher
                            │         │                │ JetStream (publisher confirms)
                       API key /   NATS KV             ▼
                         mTLS      atomic       events.<bo>.<op>
                                   claim
```

The one design decision everything else follows from: **the caller is
released before NATS is touched.** `/ingest` claims the idempotency key,
hands the event to a bounded in-process queue, and answers `202` with a
trace ID. The calling process is free in under a millisecond. Publisher
confirms happen behind it, and a failed publish releases the claim so the
caller's next retry is accepted rather than silently swallowed.

## Layout

| Path | Purpose |
|------|---------|
| `.` | [`Event`](event.go) envelope, validation, subject and dedupe-key derivation |
| `config/` | environment-driven configuration with cross-field validation |
| `api/` | `POST /ingest`, `GET /healthz`, `GET /readyz`, auth, tracing, access log |
| `broker/` | NATS connection, stream topology, the asynchronous publisher |
| `idem/` | idempotency stores: JetStream KV (production) and in-memory (dev) |
| `cmd/gateway/` | the runnable service |

The layers are exported rather than tucked under `internal/`, so a consumer
can embed just the publisher into their own binary if they want to. The only
external dependency is [`nats.go`](https://github.com/nats-io/nats.go); the
router, logging, and TLS are standard library.

## Run it locally

Start NATS with JetStream:

```bash
# Docker
docker run --rm -p 4222:4222 nats:2.10-alpine -js

# or, with a local binary
nats-server -js -sd /tmp/nats-store
```

Run the gateway. `memory` idempotency avoids needing anything provisioned:

```bash
cd ingest
export API_KEYS=local-dev-key-0123456789
export IDEMPOTENCY_BACKEND=memory
export LOG_LEVEL=debug
go run ./cmd/gateway
```

The stream and KV bucket are created on start.

### Send an event

```bash
curl -i -X POST http://localhost:8080/ingest \
  -H "Content-Type: application/json" \
  -H "X-API-Key: local-dev-key-0123456789" \
  -d '{
        "business_object": "BILLING_DOC",
        "operation": "CREATE",
        "key": "EVT-1928340192",
        "timestamp": "2023-10-27T10:00:00Z",
        "payload": {"amount": "1234.56", "currency": "EUR"}
      }'
```

```
HTTP/1.1 202 Accepted
X-Trace-Id: 4f8a1c2b9d3e5a7f6b0c8d2e4a1f9c3b

{
  "trace_id": "4f8a1c2b9d3e5a7f6b0c8d2e4a1f9c3b",
  "status": "accepted",
  "subject": "events.billing_doc.create",
  "received_at": "2026-08-18T09:14:02.117Z"
}
```

Send it again and the duplicate is rejected:

```
HTTP/1.1 409 Conflict

{"trace_id":"...","status":"duplicate","subject":"events.billing_doc.create", ...}
```

### Client code samples

The gateway is just JSON over HTTP, so any caller that can send that works.
Two representative ones:

**Go**

```go
// poster.go — an upstream caller posting one business event to the gateway.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type event struct {
	BusinessObject string          `json:"business_object"`
	Operation      string          `json:"operation"`
	Key            string          `json:"key"`
	Timestamp      string          `json:"timestamp"`
	Payload        json.RawMessage `json:"payload"`
}

func main() {
	ev := event{
		BusinessObject: "BILLING_DOC",
		Operation:      "CREATE",
		Key:            "EVT-1928340192",
		Timestamp:      time.Now().UTC().Format(time.RFC3339),
		Payload:        json.RawMessage(`{"amount":"1234.56","currency":"EUR"}`),
	}

	body, err := json.Marshal(ev)
	if err != nil {
		panic(err)
	}

	req, err := http.NewRequest(http.MethodPost, "http://localhost:8080/ingest", bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "local-dev-key-0123456789")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("%s\n%s\n", resp.Status, respBody)
}
```

A `202` means queued (or stored, with `PUBLISH_SYNC=true`); a `409` means the
same `business_object`/`key` was already seen inside the idempotency TTL and
is safe to ignore on retry; anything else carries a machine-readable `code`
field in the body worth branching on.

**ABAP**

The common deployment is an ABAP system posting through an HTTP destination
maintained in transaction `SM59` (RFC destination type `G`/`H`, host and path
pointing at the gateway's `/ingest`), so no destination-specific setup is
shown here beyond the destination name itself:

```abap
"! Posts one business event to the ingest gateway via an HTTP
"! destination maintained in transaction SM59.
DATA: lo_client TYPE REF TO if_http_client,
      lv_body   TYPE string,
      lv_status TYPE i,
      lv_resp   TYPE string.

cl_http_client=>create_by_destination(
  EXPORTING
    destination = 'INGEST_GATEWAY'
  IMPORTING
    client      = lo_client
  EXCEPTIONS
    OTHERS      = 1 ).
IF sy-subrc <> 0.
  " destination not found or misconfigured
  RETURN.
ENDIF.

lo_client->request->set_method( if_http_request=>co_request_method_post ).
lo_client->request->set_header_field( name = 'Content-Type' value = 'application/json' ).
lo_client->request->set_header_field( name = 'X-API-Key'    value = 'local-dev-key-0123456789' ).

lv_body = |\{ "business_object": "BILLING_DOC", "operation": "CREATE", | &&
          |"key": "EVT-1928340192", | &&
          |"timestamp": "{ sy-datum DATE = ISO }T{ sy-uzeit TIME = ISO }Z", | &&
          |"payload": \{ "amount": "1234.56", "currency": "EUR" \} \}|.

lo_client->request->set_cdata( lv_body ).

lo_client->send( EXCEPTIONS OTHERS = 1 ).
lo_client->receive( EXCEPTIONS OTHERS = 1 ).

lo_client->response->get_status( IMPORTING code = lv_status ).
lv_resp = lo_client->response->get_cdata( ).

CASE lv_status.
  WHEN 202.
    " accepted — lv_resp carries trace_id, status, and subject
  WHEN 409.
    " duplicate within the TTL window — safe to ignore on retry
  WHEN OTHERS.
    " 4xx/5xx — lv_resp carries a machine-readable `code` field
ENDCASE.

lo_client->close( ).
```

### Watch it move

With the [`nats` CLI](https://github.com/nats-io/natscli):

```bash
nats stream ls
nats stream info EVENTS
nats sub 'events.>'
nats kv ls IDEMPOTENCY
nats kv get IDEMPOTENCY BILLING_DOC.EVT-1928340192
```

Whatever consumes `events.>` afterward — a separate service, a different
team's consumer, a batch job — is outside this gateway's concern. It only
guarantees the event landed on the stream exactly once.

### Consuming a published event

A minimal Go consumer for the `BILLING_DOC`/`CREATE` event posted above,
using a durable JetStream pull consumer so restarts resume where they left
off instead of replaying or dropping messages:

```go
// consumer.go — a separate service reading events the gateway published.
// It is independent of the gateway; any number of these can read the same
// stream at their own pace under JetStream's pull-consumer model.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func main() {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		log.Fatal(err)
	}
	defer nc.Close()

	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()

	stream, err := js.Stream(ctx, "EVENTS")
	if err != nil {
		log.Fatal(err)
	}

	// Durable, so the consumer's position survives a restart, and filtered
	// to exactly the subject the earlier example published to.
	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       "billing-doc-create-consumer",
		FilterSubject: "events.billing_doc.create",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		log.Fatal(err)
	}

	for {
		msgs, err := cons.Fetch(10, jetstream.FetchMaxWait(5*time.Second))
		if err != nil {
			log.Println("fetch:", err)
			continue
		}
		for msg := range msgs.Messages() {
			fmt.Printf("subject=%s trace_id=%s\n%s\n",
				msg.Subject(), msg.Headers().Get("X-Trace-Id"), msg.Data())
			_ = msg.Ack()
		}
	}
}
```

`AckExplicitPolicy` means a message that is fetched but never acked (the
process crashes mid-handling) is redelivered rather than lost — ack only
after the event is durably handled on the consumer's side.

## Docker

```bash
docker build -t ingest-gateway:dev ingest/

docker run --rm -p 8080:8080 \
  -e NATS_URL=nats://host.docker.internal:4222 \
  -e API_KEYS=local-dev-key-0123456789 \
  ingest-gateway:dev
```

The image is distroless and runs as uid 65532 with no writable volumes. It has
no shell, so configure the probes in the orchestrator rather than as a
`HEALTHCHECK`:

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 8080 }
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
```

## Endpoints

### `POST /ingest`

Authenticated. Accepts one event envelope from any caller.

| Status | Meaning |
|--------|---------|
| `202 Accepted` | queued (`"status":"accepted"`), or durably stored when `PUBLISH_SYNC=true` (`"status":"stored"`, with `stream` and `sequence`) |
| `400 Bad Request` | malformed JSON or an invalid envelope; `details` lists every offending field |
| `401 Unauthorized` | missing or wrong API key, or no acceptable client certificate |
| `409 Conflict` | duplicate within the TTL window (see `INGEST_DUPLICATE_STATUS`) |
| `413 Payload Too Large` | body exceeds `HTTP_MAX_BODY_BYTES` |
| `415 Unsupported Media Type` | `Content-Type` is set and is not JSON |
| `503 Service Unavailable` | backpressure (`Retry-After` set), or the idempotency store is unreachable |

Errors carry a stable machine-readable `code` so a caller can branch without
parsing prose:

```json
{
  "trace_id": "4f8a…",
  "code": "invalid_event",
  "error": "event envelope is invalid",
  "details": [{"field": "timestamp", "reason": "is required and must be RFC 3339"}]
}
```

### `GET /healthz` and `GET /readyz`

Unauthenticated — a kubelet cannot present an API key.

`/healthz` is liveness and answers `200` whenever the process can serve HTTP.
It deliberately does **not** check NATS: killing containers because a shared
broker blipped turns one outage into a fleet-wide restart storm.

`/readyz` is readiness and reports `503` when NATS is unreachable or the
publish queue is saturated, which drops the instance out of rotation while it
drains instead of making it answer `503` to real traffic.

```json
{"status":"degraded","checks":{"nats":"ok","publish_queue":"saturated"},"idempotency":"nats-kv:IDEMPOTENCY"}
```

## Configuration

Everything is environment-driven. `Load` collects **all** problems and reports
them together, so a misconfiguration costs one restart rather than one per
mistake.

### Service

| Variable | Default | Notes |
|----------|---------|-------|
| `SERVICE_NAME` | `ingest-gateway` | also the NATS client name |
| `ENVIRONMENT` | `dev` | free-form label on every log line |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SHUTDOWN_TIMEOUT` | `30s` | bounds the whole drain sequence |

### HTTP

| Variable | Default | Notes |
|----------|---------|-------|
| `HTTP_ADDR` | `:8080` | falls back to `:$PORT` (Cloud Foundry) |
| `HTTP_MAX_BODY_BYTES` | `1048576` | per-request cap |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | the one that stops Slowloris |
| `HTTP_READ_TIMEOUT` / `HTTP_WRITE_TIMEOUT` / `HTTP_IDLE_TIMEOUT` | `15s` / `15s` / `60s` | |
| `API_KEYS` | — | comma-separated; each at least 16 characters |
| `TLS_CERT_FILE` / `TLS_KEY_FILE` | — | enable HTTPS (both or neither) |
| `TLS_CLIENT_CA_FILE` | — | enable mTLS; verified during the handshake |
| `TLS_ALLOWED_CLIENT_CNS` | — | optional certificate-subject allowlist |
| `ALLOW_ANONYMOUS` | `false` | must be set explicitly to run with no auth |
| `INGEST_DUPLICATE_STATUS` | `409` | `200`/`202` for callers that retry on any non-2xx |

Startup fails unless at least one of `API_KEYS`, `TLS_CLIENT_CA_FILE`, or
`ALLOW_ANONYMOUS` is set. When both an API key and mTLS are configured, **both**
are enforced — layered controls that degrade to the weakest one are not
controls.

### NATS

| Variable | Default | Notes |
|----------|---------|-------|
| `NATS_URL` | `nats://127.0.0.1:4222` | comma-separated for a cluster |
| `NATS_USER` / `NATS_PASSWORD` / `NATS_TOKEN` | — | token wins over user/password |
| `NATS_CREDS_FILE` / `NATS_NKEY_SEED_FILE` | — | decentralized auth |
| `NATS_CA_FILE` / `NATS_CERT_FILE` / `NATS_KEY_FILE` | — | TLS to the broker |
| `NATS_CONNECT_TIMEOUT` | `10s` | startup fails if the broker is unreachable |
| `NATS_RECONNECT_WAIT` | `2s` | |
| `NATS_MAX_RECONNECTS` | `-1` | unlimited, which is right for a long-lived gateway |
| `NATS_DRAIN_TIMEOUT` | `15s` | |

### Stream and publishing

| Variable | Default | Notes |
|----------|---------|-------|
| `STREAM_NAME` | `EVENTS` | |
| `STREAM_SUBJECT_PREFIX` | `events` | stream binds `<prefix>.>` |
| `STREAM_MAX_AGE` | `168h` | 7 days |
| `STREAM_MAX_BYTES` | `0` | 0 = unlimited |
| `STREAM_REPLICAS` | `1` | use `3` on a cluster |
| `STREAM_DUPLICATE_WINDOW` | `2m` | server-side `Nats-Msg-Id` dedupe |
| `STREAM_MANAGE` | `true` | set `false` when a platform team owns the topology |
| `PUBLISH_TIMEOUT` | `10s` | one publish-and-await-ack |
| `PUBLISH_RETRIES` | `3` | transient failures only |
| `PUBLISH_QUEUE_SIZE` | `4096` | the backpressure knob |
| `PUBLISH_CONCURRENCY` | `8` | queue drain goroutines |
| `PUBLISH_SYNC` | `false` | wait for the ack before answering (see below) |

### Idempotency

| Variable | Default | Notes |
|----------|---------|-------|
| `IDEMPOTENCY_BACKEND` | `nats` | `nats` (KV, shared) or `memory` (single replica only) |
| `IDEMPOTENCY_BUCKET` | `IDEMPOTENCY` | |
| `IDEMPOTENCY_TTL` | `24h` | the duplicate suppression window |
| `IDEMPOTENCY_TIMEOUT` | `3s` | on the request path, so keep it short |
| `IDEMPOTENCY_REPLICAS` | `1` | |

## How the guarantees are made

**Duplicate suppression is atomic.** `KeyValue.Create` is a compare-and-set on
the server, so exactly one of N concurrent replicas can claim a document —
a read-then-write would let two pods both conclude "not seen before". Release
uses `Purge`, not `Delete`: a delete tombstone would fail the next `Create`'s
revision check and silently block that key for the whole TTL.

**Nothing is lost when a publish fails.** The key is claimed *before* the
publish so a duplicate arriving mid-flight still loses. If the publish is then
abandoned, the failure hook releases the claim. Both halves are needed: the
first without the second silently drops documents; the second without the
first admits duplicates.

**Publish retries cannot double-store.** Every message carries
`Nats-Msg-Id` = `<business_object>.<key>.<operation>`, so JetStream collapses
repeats inside `STREAM_DUPLICATE_WINDOW`. An ack lost on the wire is safe to
retry.

**Backpressure is explicit.** The submission queue is bounded. Once full,
`/ingest` answers `503` with `Retry-After` and hands the claim back. An
unbounded queue would convert a NATS outage into an OOM kill and take the
buffered events with it.

**Shutdown drains in order.** `SIGTERM` → stop accepting HTTP and finish
in-flight requests → flush the publish queue → flush and close NATS.

### `PUBLISH_SYNC`

Default `false`: `202` means *queued*. A pod killed with events still in the
queue loses them, and because shutdown drains the queue that window is the
drain timeout, not indefinite.

Set `true` and `/ingest` waits for the JetStream ack, so `202` means *durably
stored* and the response carries `stream` and `sequence`. It costs one NATS
round trip per request. Use it when losing an event is worse than making the
caller wait.

## What this service does not do

On purpose: it does not consume the stream back out, does not relay events to
any downstream system, and does not retry or dead-letter *delivery* failures
— there is no delivery, only publish. If you need something to read
`events.>` and act on it, that is a separate consumer you write and deploy
independently; JetStream's pull-consumer model means any number of them can
read the same stream at their own pace without this gateway knowing or
caring.

## Tests

```bash
cd ingest
go test ./...
go test -race ./...
```

The end-to-end tests in [`integration_test.go`](integration_test.go) run
against a real JetStream server: they start one automatically when
`nats-server` is on `PATH`, honour `NATS_TEST_URL` when set, and skip
otherwise. They cover the full loop — HTTP in, event lands on the stream —
plus duplicate suppression through the real KV bucket, server-side
`Nats-Msg-Id` dedupe, and reservation rollback.

```bash
# against your own server
NATS_TEST_URL=nats://127.0.0.1:4222 go test -run TestIntegration -v ./
```

## License

Apache-2.0. See [LICENSE](../LICENSE).
