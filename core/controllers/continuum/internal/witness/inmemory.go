// In-memory Client used for unit tests.
//
// This is a deterministic implementation of the witness contract that
// keeps every slot in a process-local map guarded by a mutex. It is
// NOT a production-grade lease store — there is no durability and no
// network round-trip. Tests use it because:
//
//   - The CAS path is exercisable without an HTTP fake / network.
//   - Multi-CR isolation can be asserted by giving each Continuum CR
//     its own slot key (the Selector builds the slot key from the
//     CR's NamespacedName).
//   - TTL expiry is driven by an injectable `now` clock so tests
//     don't sleep.
//
// K-Cont-3's real cloudflare-kv + dns-quorum impls MUST satisfy the
// SAME observable contract: see `witness_contract_test.go` (CAS
// invariants, Renew-after-loss, Release idempotency) — both real
// impls run that test suite by satisfying Client.

package witness

import (
	"context"
	"sync"
	"time"
)

// InMemoryStore is a process-local witness backing store shared by
// many in-memory Clients (each Client tied to one slot). Used by the
// DefaultSelector when kind=in-memory and InMemoryAllowed=true.
//
// The Client interface methods take a holder + ttl per call (so a
// single InMemoryStore can serve many CRs); slot isolation is via the
// slot key stamped at Client construction time.
type InMemoryStore struct {
	mu sync.Mutex

	// slots: slot key → State. State.Holder == "" means the slot is
	// unclaimed.
	slots map[string]State

	// now is the clock function. Tests override; defaults to
	// time.Now.
	now func() time.Time
}

// NewInMemoryStore returns a store with the system clock. Tests that
// want deterministic timing call SetClock.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		slots: map[string]State{},
		now:   time.Now,
	}
}

// SetClock overrides the now() function. Test-only.
func (s *InMemoryStore) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// Client returns a witness Client bound to the given slot. Multiple
// Clients can share the same slot (simulating two regions racing for
// the same lease).
func (s *InMemoryStore) Client(slot string) *InMemoryClient {
	return &InMemoryClient{store: s, slot: slot}
}

// InMemoryClient is the witness Client backed by InMemoryStore.
type InMemoryClient struct {
	store *InMemoryStore
	slot  string
}

// Acquire attempts to claim the slot. CAS invariants:
//
//   - If the current slot has a non-expired holder that's NOT this
//     `holder`, return ErrLeaseHeldByAnother.
//   - If the current slot has expired (ExpiresAt < now), the new
//     holder takes it (this is the failover path: old primary
//     stopped renewing).
//   - If the current slot is held by THIS holder, the call extends
//     the TTL like a Renew (idempotent re-acquire).
func (c *InMemoryClient) Acquire(ctx context.Context, holder string, ttl time.Duration) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()

	cur := c.store.slots[c.slot]
	now := c.store.now()

	// Slot is free OR expired OR already ours → take it / extend.
	if cur.Holder == "" || !now.Before(cur.ExpiresAt) || cur.Holder == holder {
		next := State{
			Holder:     holder,
			AcquiredAt: ifZero(cur.Holder == holder && now.Before(cur.ExpiresAt), cur.AcquiredAt, now),
			ExpiresAt:  now.Add(ttl),
			Generation: cur.Generation + 1,
		}
		c.store.slots[c.slot] = next
		return next, nil
	}

	// Slot is held by another, non-expired holder.
	return cur, ErrLeaseHeldByAnother
}

// Renew extends the TTL on the slot iff `holder` still owns it.
func (c *InMemoryClient) Renew(ctx context.Context, holder string, ttl time.Duration) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()

	cur := c.store.slots[c.slot]
	now := c.store.now()

	// Lost the lease: either expired or another holder took it.
	if cur.Holder != holder || !now.Before(cur.ExpiresAt) {
		return cur, ErrLeaseLost
	}

	next := State{
		Holder:     holder,
		AcquiredAt: cur.AcquiredAt,
		ExpiresAt:  now.Add(ttl),
		Generation: cur.Generation + 1,
	}
	c.store.slots[c.slot] = next
	return next, nil
}

// Release voluntarily relinquishes the slot. Idempotent: a non-holder
// calling Release is a no-op (so a controller restart that lost track
// of who-it-thinks-holds-it doesn't accidentally evict the new
// primary).
func (c *InMemoryClient) Release(ctx context.Context, holder string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()

	cur := c.store.slots[c.slot]
	if cur.Holder != holder {
		return nil
	}
	c.store.slots[c.slot] = State{Generation: cur.Generation + 1}
	return nil
}

// Read returns the current State.
func (c *InMemoryClient) Read(ctx context.Context) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	c.store.mu.Lock()
	defer c.store.mu.Unlock()
	return c.store.slots[c.slot], nil
}

// Compile-time assertion that InMemoryClient satisfies Client.
var _ Client = (*InMemoryClient)(nil)

// ifZero returns `t` when cond is true, else `fallback`. Inlined
// helper to keep Acquire's body shorter; AcquiredAt should preserve
// the ORIGINAL acquisition time when re-acquiring (lease extension
// is not a new acquisition).
func ifZero(cond bool, t, fallback time.Time) time.Time {
	if cond {
		return t
	}
	return fallback
}
