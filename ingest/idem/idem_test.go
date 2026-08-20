package idem

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMemoryReserveThenDuplicate(t *testing.T) {
	store := NewMemory(time.Minute)
	t.Cleanup(func() { _ = store.Close() })

	ctx := t.Context()
	if err := store.Reserve(ctx, "BILLING_DOC.SAP-1", []byte(`{"trace_id":"abc"}`)); err != nil {
		t.Fatalf("first Reserve() = %v, want nil", err)
	}
	if err := store.Reserve(ctx, "BILLING_DOC.SAP-1", nil); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second Reserve() = %v, want ErrDuplicate", err)
	}
	if err := store.Reserve(ctx, "BILLING_DOC.SAP-2", nil); err != nil {
		t.Fatalf("Reserve() of a different key = %v, want nil", err)
	}
}

func TestMemoryRelease(t *testing.T) {
	store := NewMemory(time.Minute)
	t.Cleanup(func() { _ = store.Close() })

	ctx := t.Context()
	if err := store.Reserve(ctx, "k", nil); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	if err := store.Release(ctx, "k"); err != nil {
		t.Fatalf("Release() = %v", err)
	}
	// Releasing is the rollback path for a failed publish; the key has to
	// be claimable again afterwards or SAP's retry would be swallowed.
	if err := store.Reserve(ctx, "k", nil); err != nil {
		t.Fatalf("Reserve() after Release = %v, want nil", err)
	}

	// Release must be safe on a key nobody holds: it runs on error paths
	// that may already have released.
	if err := store.Release(ctx, "never-seen"); err != nil {
		t.Errorf("Release() of an unheld key = %v, want nil", err)
	}
}

func TestMemoryExpiry(t *testing.T) {
	store := NewMemory(time.Hour)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	store.now = func() time.Time { return now }

	ctx := t.Context()
	if err := store.Reserve(ctx, "k", nil); err != nil {
		t.Fatalf("Reserve() = %v", err)
	}
	if err := store.Reserve(ctx, "k", nil); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Reserve() inside the window = %v, want ErrDuplicate", err)
	}

	now = now.Add(time.Hour + time.Second)
	if err := store.Reserve(ctx, "k", nil); err != nil {
		t.Fatalf("Reserve() after the TTL = %v, want nil", err)
	}
}

func TestMemoryEvictsExpiredEntries(t *testing.T) {
	store := NewMemory(time.Hour)
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now()
	store.now = func() time.Time { return now }

	ctx := t.Context()
	for _, k := range []string{"a", "b", "c"} {
		if err := store.Reserve(ctx, k, nil); err != nil {
			t.Fatalf("Reserve(%q) = %v", k, err)
		}
	}
	if got := store.Len(); got != 3 {
		t.Fatalf("Len() = %d, want 3", got)
	}

	now = now.Add(2 * time.Hour)
	store.evictExpired()

	if got := store.Len(); got != 0 {
		t.Fatalf("Len() after eviction = %d, want 0", got)
	}
	store.mu.Lock()
	remaining := len(store.entries)
	store.mu.Unlock()
	if remaining != 0 {
		t.Errorf("%d entries still held in the map after eviction", remaining)
	}
}

// The whole point of Reserve is that concurrent callers cannot both win.
func TestMemoryReserveIsAtomicUnderConcurrency(t *testing.T) {
	store := NewMemory(time.Minute)
	t.Cleanup(func() { _ = store.Close() })

	const goroutines = 64
	var (
		wg       sync.WaitGroup
		accepted atomic.Int64
		start    = make(chan struct{})
	)

	for range goroutines {
		wg.Go(func() {
			<-start
			if err := store.Reserve(context.Background(), "hot-key", nil); err == nil {
				accepted.Add(1)
			}
		})
	}
	close(start)
	wg.Wait()

	if got := accepted.Load(); got != 1 {
		t.Fatalf("%d goroutines saw the key as new, want exactly 1", got)
	}
}

func TestMemoryCloseIsIdempotent(t *testing.T) {
	store := NewMemory(time.Minute)
	if err := store.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}

func TestMemoryKind(t *testing.T) {
	store := NewMemory(time.Minute)
	t.Cleanup(func() { _ = store.Close() })

	if got := store.Kind(); got != "memory" {
		t.Errorf("Kind() = %q, want %q", got, "memory")
	}
}

// Memory must satisfy Store, since that is what the API layer is wired to.
var _ Store = (*Memory)(nil)
var _ Store = (*KeyValue)(nil)
