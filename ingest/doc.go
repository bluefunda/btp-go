// Package ingest provides the domain model for a generic asynchronous
// ingestion proxy between an HTTP upstream and NATS JetStream.
//
// The gateway accepts business events over HTTP from any caller (a
// commonly-deployed case is an SAP ABAP system posting via its HTTP
// destination configuration), suppresses duplicates within a configurable
// TTL window, and publishes them durably to a JetStream stream. That is the
// whole job: the gateway does not consume the stream back out, does not know
// or care what reads it afterward, and does not relay anywhere. It is a
// proxy, deployable unchanged on Cloud Foundry, Kyma, Kubernetes, or a
// laptop.
//
//	caller  ──POST /ingest──▶  api  ──▶  idem  ──▶  broker.Publisher
//	                                                       │ JetStream
//	                                                       ▼
//	                                               events.<bo>.<op>
//
// This root package holds the [Event] envelope and its validation rules,
// which every other layer agrees on. The layered packages are:
//
//   - [github.com/bluefunda/btp-go/ingest/config] — environment-driven configuration
//   - [github.com/bluefunda/btp-go/ingest/api] — HTTP surface, auth, health probes
//   - [github.com/bluefunda/btp-go/ingest/broker] — JetStream connection, topology, publisher
//   - [github.com/bluefunda/btp-go/ingest/idem] — idempotency stores (NATS KV or in-memory)
//
// The runnable service lives in cmd/gateway. The layers are exported rather
// than placed under internal/ so that consumers can embed just the publisher
// into their own binary if they want to.
//
// The only external dependency is github.com/nats-io/nats.go. Everything
// else, including the HTTP router, is standard library.
package ingest
