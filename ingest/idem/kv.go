package idem

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// KeyValue is a [Store] backed by a JetStream Key/Value bucket.
//
// It is the production implementation: the bucket is shared by every replica
// of the gateway, and KV Create is a compare-and-set on the server, so
// exactly one of N concurrent claimants for the same key succeeds no matter
// how many pods are running.
//
// Bucket TTL does the expiry. There is no sweeper to run and no clock
// agreement needed between replicas.
type KeyValue struct {
	kv     jetstream.KeyValue
	bucket string
}

// KeyValueConfig describes the bucket to bind to.
type KeyValueConfig struct {
	// Bucket is the KV bucket name.
	Bucket string

	// TTL is the duplicate suppression window. Every key in a NATS KV
	// bucket shares one TTL, so this is a bucket-level property, not a
	// per-key one.
	TTL time.Duration

	// Replicas is the bucket replica count (1 for a single server).
	Replicas int

	// Manage controls whether the bucket is created if missing. Set false
	// when the bucket is provisioned out of band.
	Manage bool
}

// NewKeyValue binds to the configured bucket, creating it when Manage is set.
//
// If the bucket already exists with a different TTL, the existing TTL wins
// and no error is returned — silently rewriting a shared bucket's retention
// out from under other consumers would be worse than the mismatch.
func NewKeyValue(ctx context.Context, js jetstream.JetStream, cfg KeyValueConfig) (*KeyValue, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("idem: bucket name is required")
	}
	if cfg.Replicas < 1 {
		cfg.Replicas = 1
	}

	if !cfg.Manage {
		kv, err := js.KeyValue(ctx, cfg.Bucket)
		if err != nil {
			return nil, fmt.Errorf("idem: bind kv bucket %q: %w", cfg.Bucket, err)
		}
		return &KeyValue{kv: kv, bucket: cfg.Bucket}, nil
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      cfg.Bucket,
		Description: "Ingestion gateway duplicate suppression",
		History:     1,
		TTL:         cfg.TTL,
		Storage:     jetstream.FileStorage,
		Replicas:    cfg.Replicas,
	})
	if err != nil {
		return nil, fmt.Errorf("idem: create kv bucket %q: %w", cfg.Bucket, err)
	}
	return &KeyValue{kv: kv, bucket: cfg.Bucket}, nil
}

// Reserve implements [Store] using an atomic KV create.
func (k *KeyValue) Reserve(ctx context.Context, key string, meta []byte) error {
	if _, err := k.kv.Create(ctx, key, meta); err != nil {
		if errors.Is(err, jetstream.ErrKeyExists) {
			return ErrDuplicate
		}
		return fmt.Errorf("idem: reserve %q: %w", key, err)
	}
	return nil
}

// Release implements [Store]. A key that is already gone is not an error,
// since Release is a rollback path and must be safe to call twice.
func (k *KeyValue) Release(ctx context.Context, key string) error {
	// Purge rather than Delete: Delete leaves a tombstone revision, and a
	// tombstone makes the next Create for that key fail its
	// expected-revision check. Purge removes the history so the key is
	// genuinely claimable again.
	if err := k.kv.Purge(ctx, key); err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil
		}
		return fmt.Errorf("idem: release %q: %w", key, err)
	}
	return nil
}

// Kind implements [Store].
func (k *KeyValue) Kind() string { return "nats-kv:" + k.bucket }

// Close implements [Store]. The bucket handle borrows the shared NATS
// connection, which the caller owns, so there is nothing to release here.
func (k *KeyValue) Close() error { return nil }
