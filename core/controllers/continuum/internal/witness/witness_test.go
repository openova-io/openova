// Tests for the witness contract + InMemoryClient.
//
// These tests double as the contract spec: K-Cont-3's cloudflare-kv
// and dns-quorum implementations MUST run this same suite (or a
// faithful adaptation that drives a fake transport).

package witness

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// frozenClock returns a clock that the test can advance manually.
// Each call to Now() returns the current value of t.
type frozenClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *frozenClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *frozenClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClock() *frozenClock {
	return &frozenClock{t: time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)}
}

func TestInMemory_AcquireOnEmpty(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	clk := newClock()
	store.SetClock(clk.Now)
	c := store.Client("slotA")

	st, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire empty: %v", err)
	}
	if st.Holder != "fsn" {
		t.Fatalf("Holder = %q want fsn", st.Holder)
	}
	if st.Generation != 1 {
		t.Fatalf("Generation = %d want 1", st.Generation)
	}
	if st.AcquiredAt != clk.Now() {
		t.Fatalf("AcquiredAt should equal clock now")
	}
	if got, want := st.ExpiresAt, clk.Now().Add(30*time.Second); got != want {
		t.Fatalf("ExpiresAt = %v want %v", got, want)
	}
}

func TestInMemory_AcquireBlockedByAnother(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	clk := newClock()
	store.SetClock(clk.Now)
	a := store.Client("slotA")
	b := store.Client("slotA") // same slot

	if _, err := a.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	if _, err := b.Acquire(context.Background(), "hel", 30*time.Second); !errors.Is(err, ErrLeaseHeldByAnother) {
		t.Fatalf("second Acquire: err = %v want ErrLeaseHeldByAnother", err)
	}
}

func TestInMemory_AcquireAfterExpiry(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	clk := newClock()
	store.SetClock(clk.Now)
	a := store.Client("slotA")
	b := store.Client("slotA")

	if _, err := a.Acquire(context.Background(), "fsn", 5*time.Second); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	clk.Advance(10 * time.Second) // past TTL
	st, err := b.Acquire(context.Background(), "hel", 30*time.Second)
	if err != nil {
		t.Fatalf("post-expiry Acquire: %v", err)
	}
	if st.Holder != "hel" {
		t.Fatalf("Holder = %q want hel", st.Holder)
	}
}

func TestInMemory_AcquireSameHolderExtendsTTL(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	clk := newClock()
	store.SetClock(clk.Now)
	c := store.Client("slotA")

	st1, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	clk.Advance(5 * time.Second)
	st2, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("second Acquire (same holder): %v", err)
	}
	if st2.AcquiredAt != st1.AcquiredAt {
		t.Fatalf("AcquiredAt drifted on re-acquire: %v vs %v", st2.AcquiredAt, st1.AcquiredAt)
	}
	if !st2.ExpiresAt.After(st1.ExpiresAt) {
		t.Fatalf("ExpiresAt did not advance on re-acquire: %v not after %v", st2.ExpiresAt, st1.ExpiresAt)
	}
}

func TestInMemory_RenewExtendsTTL(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	clk := newClock()
	store.SetClock(clk.Now)
	c := store.Client("slotA")

	st1, err := c.Acquire(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clk.Advance(10 * time.Second)
	st2, err := c.Renew(context.Background(), "fsn", 30*time.Second)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if !st2.ExpiresAt.After(st1.ExpiresAt) {
		t.Fatalf("Renew did not extend ExpiresAt")
	}
	if st2.Generation <= st1.Generation {
		t.Fatalf("Renew did not bump Generation")
	}
}

func TestInMemory_RenewAfterExpiryReturnsLost(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	clk := newClock()
	store.SetClock(clk.Now)
	c := store.Client("slotA")

	if _, err := c.Acquire(context.Background(), "fsn", 5*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clk.Advance(10 * time.Second)
	if _, err := c.Renew(context.Background(), "fsn", 30*time.Second); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Renew after expiry: err = %v want ErrLeaseLost", err)
	}
}

func TestInMemory_RenewByNonHolderReturnsLost(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	clk := newClock()
	store.SetClock(clk.Now)
	a := store.Client("slotA")
	b := store.Client("slotA")

	if _, err := a.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := b.Renew(context.Background(), "hel", 30*time.Second); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("Renew by non-holder: err = %v want ErrLeaseLost", err)
	}
}

func TestInMemory_ReleaseIdempotent(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	c := store.Client("slotA")

	if err := c.Release(context.Background(), "fsn"); err != nil {
		t.Fatalf("Release on empty slot: %v", err)
	}
	if _, err := c.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := c.Release(context.Background(), "fsn"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := c.Release(context.Background(), "fsn"); err != nil {
		t.Fatalf("Release again: %v", err)
	}
	st, err := c.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st.Holder != "" {
		t.Fatalf("Holder = %q want empty after Release", st.Holder)
	}
}

func TestInMemory_ReleaseByNonHolderIsNoOp(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	a := store.Client("slotA")

	if _, err := a.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Other region tries to release; should be a no-op (so a stale
	// caller doesn't accidentally evict the live primary).
	if err := a.Release(context.Background(), "hel"); err != nil {
		t.Fatalf("Release by non-holder: %v", err)
	}
	st, _ := a.Read(context.Background())
	if st.Holder != "fsn" {
		t.Fatalf("Holder = %q want fsn", st.Holder)
	}
}

func TestInMemory_SlotIsolation(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	a := store.Client("ns1/cr1")
	b := store.Client("ns2/cr2")

	if _, err := a.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	// b should be free regardless.
	st, err := b.Acquire(context.Background(), "hel", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire B: %v", err)
	}
	if st.Holder != "hel" {
		t.Fatalf("B.Holder = %q want hel", st.Holder)
	}
}

func TestInMemory_State_IsHeldBy(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := State{
		Holder:    "fsn",
		ExpiresAt: now.Add(time.Minute),
	}
	if !st.IsHeldBy("fsn", now) {
		t.Fatalf("expected IsHeldBy(fsn) true")
	}
	if st.IsHeldBy("hel", now) {
		t.Fatalf("expected IsHeldBy(hel) false")
	}
	if st.IsHeldBy("fsn", now.Add(2*time.Minute)) {
		t.Fatalf("expected IsHeldBy(fsn, post-expiry) false")
	}
	if (State{}).IsHeldBy("fsn", now) {
		t.Fatalf("empty State should never claim a holder")
	}
}

func TestDefaultSelector_NotImplemented(t *testing.T) {
	t.Parallel()
	s := &DefaultSelector{InMemoryAllowed: false}
	for _, kind := range []string{"cloudflare-kv", "dns-quorum"} {
		_, err := s.Select(kind, nil)
		if !errors.Is(err, ErrNotImplemented) {
			t.Fatalf("Select(%q) err = %v want ErrNotImplemented", kind, err)
		}
	}
}

func TestDefaultSelector_InMemoryRefusedInProd(t *testing.T) {
	t.Parallel()
	s := &DefaultSelector{InMemoryAllowed: false}
	_, err := s.Select("in-memory", nil)
	if err == nil {
		t.Fatalf("expected refusal for in-memory in production")
	}
}

func TestDefaultSelector_InMemoryAllowed(t *testing.T) {
	t.Parallel()
	s := &DefaultSelector{InMemoryAllowed: true}
	cli, err := s.Select("in-memory", map[string]any{"slot": "ns/foo"})
	if err != nil {
		t.Fatalf("Select in-memory: %v", err)
	}
	if cli == nil {
		t.Fatalf("expected non-nil Client")
	}
	// Same selector + same slot should yield isolated CAS state. We
	// can't compare clients directly (each call returns a new
	// wrapper), but we can verify the underlying store carries state
	// across selects.
	cli2, _ := s.Select("in-memory", map[string]any{"slot": "ns/foo"})
	if _, err := cli.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := cli2.Acquire(context.Background(), "hel", 30*time.Second); !errors.Is(err, ErrLeaseHeldByAnother) {
		t.Fatalf("shared store: err = %v want ErrLeaseHeldByAnother", err)
	}
}

func TestDefaultSelector_UnknownKind(t *testing.T) {
	t.Parallel()
	s := &DefaultSelector{}
	if _, err := s.Select("not-a-kind", nil); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

func TestSelectorFunc(t *testing.T) {
	t.Parallel()
	want := NewInMemoryStore().Client("x")
	f := SelectorFunc(func(kind string, cfg map[string]any) (Client, error) {
		return want, nil
	})
	got, err := f.Select("any", nil)
	if err != nil {
		t.Fatalf("SelectorFunc.Select: %v", err)
	}
	if got != want {
		t.Fatalf("SelectorFunc returned wrong client")
	}
}

func TestInMemory_ContextCancel(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStore()
	c := store.Client("slot")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Acquire(ctx, "fsn", 30*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire after cancel: %v", err)
	}
	if _, err := c.Renew(ctx, "fsn", 30*time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Renew after cancel: %v", err)
	}
	if err := c.Release(ctx, "fsn"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Release after cancel: %v", err)
	}
	if _, err := c.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read after cancel: %v", err)
	}
}
