// Package idem provides duplicate suppression for the ingestion gateway.
//
// The contract is deliberately narrow: [Store.Reserve] atomically claims a
// key, or reports [ErrDuplicate] if someone already holds it. That single
// operation is what makes /ingest idempotent under SAP's at-least-once retry
// behaviour, and it has to be atomic — a read-then-write would let two
// concurrent replicas both conclude "not seen before" for the same document.
//
// Reservations are also releasable. The gateway claims the key before it
// publishes, so a duplicate arriving mid-publish is still rejected; if the
// publish then fails permanently, [Store.Release] hands the key back so SAP's
// next retry is accepted rather than silently swallowed.
package idem

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrDuplicate is returned by Reserve when the key is already held.
var ErrDuplicate = errors.New("idem: duplicate key within TTL window")

// Store is a TTL-bounded set of claimed keys.
//
// Implementations must be safe for concurrent use.
type Store interface {
	// Reserve atomically claims key, storing meta alongside it for
	// operator forensics. It returns ErrDuplicate if the key is already
	// held.
	Reserve(ctx context.Context, key string, meta []byte) error

	// Release drops a reservation. Releasing a key that is not held is not
	// an error.
	Release(ctx context.Context, key string) error

	// Kind names the backing implementation, for logs and /readyz output.
	Kind() string

	// Close releases any resources held by the store.
	Close() error
}

// Reservation is the JSON value stored against a reserved key. It is what an
// operator sees when they run `nats kv get IDEMPOTENCY <key>` while
// working out why an event was rejected.
type Reservation struct {
	TraceID   string    `json:"trace_id"`
	Subject   string    `json:"subject"`
	Key       string    `json:"key"`
	Operation string    `json:"operation"`
	ClaimedAt time.Time `json:"claimed_at"`
}

// Memory is an in-process [Store] backed by a map with per-key expiry.
//
// It is correct for a single replica and useless for more than one, since
// two pods do not share a map. Use it for local development and tests; use
// [KeyValue] in production.
type Memory struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]entry

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

type entry struct {
	meta      []byte
	expiresAt time.Time
}

// NewMemory returns an in-memory store whose reservations expire after ttl.
// A background goroutine evicts expired keys every ttl/10 (at least once a
// second) so that a burst of one-shot keys does not pin memory forever.
func NewMemory(ttl time.Duration) *Memory {
	if ttl <= 0 {
		ttl = time.Hour
	}
	m := &Memory{
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]entry),
		stop:    make(chan struct{}),
	}

	interval := max(ttl/10, time.Second)
	m.wg.Go(func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				m.evictExpired()
			}
		}
	})
	return m
}

// Reserve implements [Store].
func (m *Memory) Reserve(_ context.Context, key string, meta []byte) error {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()

	if e, ok := m.entries[key]; ok && e.expiresAt.After(now) {
		return ErrDuplicate
	}
	m.entries[key] = entry{meta: meta, expiresAt: now.Add(m.ttl)}
	return nil
}

// Release implements [Store].
func (m *Memory) Release(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

// Kind implements [Store].
func (m *Memory) Kind() string { return "memory" }

// Close stops the eviction goroutine. It is safe to call more than once.
func (m *Memory) Close() error {
	m.stopOnce.Do(func() { close(m.stop) })
	m.wg.Wait()
	return nil
}

// Len returns the number of live reservations. It exists for tests and for
// an operator-facing gauge.
func (m *Memory) Len() int {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.entries {
		if e.expiresAt.After(now) {
			n++
		}
	}
	return n
}

func (m *Memory) evictExpired() {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, e := range m.entries {
		if !e.expiresAt.After(now) {
			delete(m.entries, k)
		}
	}
}
