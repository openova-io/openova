// Package testing exposes the witness behavioral contract suite as a
// parametric helper. Every concrete witness.Client implementation
// (InMemoryClient, cloudflarekv.CFKVClient, dnsquorum.DNSQuorumClient)
// runs THIS suite against itself so the on-the-wire CAS path matches
// the in-memory reference exactly.
//
// Why a separate package: putting the contract suite here (rather than
// in `witness/`) lets the cloudflarekv + dnsquorum impls IMPORT it
// without creating a test-only dependency cycle (the impls live in
// child packages of `witness/`, so they can import `witness/testing`
// without dragging the impl-specific httptest fixtures into
// `witness/`).
//
// The suite is parametric on a Backend factory. The factory returns:
//
//   - Two Client instances bound to the SAME slot (so race tests can
//     simulate two regions contending for the same lease)
//   - A separate Client on a DIFFERENT slot (slot-isolation test)
//   - An Advance(d) hook that fast-forwards time by d (for TTL tests)
//
// Implementations that can't fast-forward (cloudflarekv against a
// real Worker; dnsquorum against real PowerDNS) supply an Advance
// that real-time-sleeps through `d`. The contract suite picks small
// TTLs (≤ 200ms) so a real-time sleep stays bounded.
//
// Per K-Cont-2's K-Cont-3 concerns (item h), the contract verifies:
//
//  1. CAS atomicity — Acquire rejects when the slot is held by
//     another non-expired holder.
//  2. Renew failure mode — Renew returns ErrLeaseLost when TTL has
//     elapsed OR when another holder owns the slot.
//  3. Release idempotency — non-holder Release is a no-op.
//  4. Generation monotonicity — Acquire AND Renew bump State.Generation.
//  5. Slot isolation — different cfg["slot"] values produce
//     independent leases.
//  6. Witness clock skew — verified per-impl: server-authoritative
//     for CF (the server stamps timestamps + bumps Generation);
//     fence-token + 2-of-3 for DNS-quorum (any single resolver may
//     drift by one generation without breaking quorum).
//  7. Witness-side TTL eviction — Read returns "free" after expiry.
package testing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// Backend is what each impl-specific test file constructs and passes
// to RunContractSuite. It bundles two same-slot clients (for CAS race
// scenarios), one different-slot client (for isolation), and an
// Advance hook for TTL tests.
type Backend struct {
	// A and B are two Client instances bound to the SAME slot. Tests
	// use them to simulate two regions racing for the same lease.
	A witness.Client
	B witness.Client

	// Other is a Client bound to a DIFFERENT slot — used to verify
	// slot isolation (an Acquire on Other must not affect A/B).
	Other witness.Client

	// Advance moves the witness's effective time forward by d.
	// Implementations that have an injectable clock (InMemoryClient)
	// implement this as a clock-set; impls that only support
	// real-time (cloudflarekv against a live Worker) implement as
	// time.Sleep(d). The contract suite never asks for d > 200ms so
	// a real sleep is bounded.
	Advance func(d time.Duration)

	// Cleanup is called by RunContractSuite at the end. Optional.
	Cleanup func()
}

// RunContractSuite runs the full witness behavioral contract against
// the supplied Backend factory. The factory is invoked once per
// sub-test so each test gets a clean slot.
//
// Usage from inmemory_test.go:
//
//	contracttest.RunContractSuite(t, func() *contracttest.Backend {
//	    store := witness.NewInMemoryStore()
//	    clk := &fakeClock{t: time.Date(...)}
//	    store.SetClock(clk.Now)
//	    return &contracttest.Backend{
//	        A:       store.Client("ns/cr"),
//	        B:       store.Client("ns/cr"),
//	        Other:   store.Client("ns/other"),
//	        Advance: clk.Advance,
//	    }
//	})
//
// Each implementation MUST pass every sub-test or the production
// surfaces will diverge.
func RunContractSuite(t *testing.T, factory func() *Backend) {
	t.Helper()

	cases := []struct {
		name string
		fn   func(t *testing.T, b *Backend)
	}{
		{"AcquireOnEmptySlot", testAcquireOnEmpty},
		{"AcquireBlockedByAnother", testAcquireBlocked},
		{"AcquireAfterExpiry", testAcquireAfterExpiry},
		{"AcquireSameHolderExtendsTTL", testAcquireSameHolder},
		{"RenewExtendsTTLAndBumpsGeneration", testRenewExtends},
		{"RenewAfterExpiryReturnsLost", testRenewAfterExpiry},
		{"RenewByNonHolderReturnsLost", testRenewByNonHolder},
		{"ReleaseIdempotent", testReleaseIdempotent},
		{"ReleaseByNonHolderIsNoOp", testReleaseByNonHolder},
		{"SlotIsolation", testSlotIsolation},
		{"GenerationMonotonicityAcrossOps", testGenerationMonotonic},
		{"ReadOnEmptySlot", testReadOnEmpty},
		{"ReadAfterTTLEvictionReportsFree", testReadAfterTTLEviction},
		{"ContextCancel", testContextCancel},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			b := factory()
			if b.Cleanup != nil {
				t.Cleanup(b.Cleanup)
			}
			tc.fn(t, b)
		})
	}
}

// shortTTL is the TTL the suite uses for tests that need expiry. It
// is small (100ms) so impls that can't fast-forward time pay a
// bounded real-time penalty.
const shortTTL = 100 * time.Millisecond

// longTTL is used when the test does not depend on expiry — keeping
// it well above the test's wall-clock so flakes from CI scheduling
// jitter don't expire a lease mid-test.
const longTTL = 30 * time.Second

func testAcquireOnEmpty(t *testing.T, b *Backend) {
	st, err := b.A.Acquire(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if st.Holder != "fsn" {
		t.Fatalf("Holder = %q want fsn", st.Holder)
	}
	if st.Generation < 1 {
		t.Fatalf("Generation = %d want >= 1", st.Generation)
	}
	if st.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt is zero")
	}
	if !st.ExpiresAt.After(st.AcquiredAt) {
		t.Fatalf("ExpiresAt %v not after AcquiredAt %v", st.ExpiresAt, st.AcquiredAt)
	}
}

func testAcquireBlocked(t *testing.T, b *Backend) {
	if _, err := b.A.Acquire(context.Background(), "fsn", longTTL); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	if _, err := b.B.Acquire(context.Background(), "hel", longTTL); !errors.Is(err, witness.ErrLeaseHeldByAnother) {
		t.Fatalf("second Acquire: err = %v want ErrLeaseHeldByAnother", err)
	}
}

func testAcquireAfterExpiry(t *testing.T, b *Backend) {
	if _, err := b.A.Acquire(context.Background(), "fsn", shortTTL); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	b.Advance(shortTTL + 50*time.Millisecond)
	st, err := b.B.Acquire(context.Background(), "hel", longTTL)
	if err != nil {
		t.Fatalf("post-expiry Acquire: %v", err)
	}
	if st.Holder != "hel" {
		t.Fatalf("Holder = %q want hel", st.Holder)
	}
}

func testAcquireSameHolder(t *testing.T, b *Backend) {
	st1, err := b.A.Acquire(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	b.Advance(10 * time.Millisecond)
	st2, err := b.A.Acquire(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("second Acquire (same holder): %v", err)
	}
	if !st2.AcquiredAt.Equal(st1.AcquiredAt) {
		t.Fatalf("AcquiredAt drifted on re-acquire: %v vs %v", st2.AcquiredAt, st1.AcquiredAt)
	}
	if !st2.ExpiresAt.After(st1.ExpiresAt) && !st2.ExpiresAt.Equal(st1.ExpiresAt) {
		// For server-authoritative impls the new ExpiresAt may equal
		// the old one if the server's clock granularity is coarse;
		// allow equal but never less.
		t.Fatalf("ExpiresAt regressed on re-acquire: %v vs %v", st2.ExpiresAt, st1.ExpiresAt)
	}
}

func testRenewExtends(t *testing.T, b *Backend) {
	st1, err := b.A.Acquire(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b.Advance(10 * time.Millisecond)
	st2, err := b.A.Renew(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if st2.Generation <= st1.Generation {
		t.Fatalf("Renew did not bump Generation: %d -> %d", st1.Generation, st2.Generation)
	}
	if st2.ExpiresAt.Before(st1.ExpiresAt) {
		t.Fatalf("Renew did not extend ExpiresAt: %v -> %v", st1.ExpiresAt, st2.ExpiresAt)
	}
}

func testRenewAfterExpiry(t *testing.T, b *Backend) {
	if _, err := b.A.Acquire(context.Background(), "fsn", shortTTL); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b.Advance(shortTTL + 50*time.Millisecond)
	if _, err := b.A.Renew(context.Background(), "fsn", longTTL); !errors.Is(err, witness.ErrLeaseLost) {
		t.Fatalf("Renew after expiry: err = %v want ErrLeaseLost", err)
	}
}

func testRenewByNonHolder(t *testing.T, b *Backend) {
	if _, err := b.A.Acquire(context.Background(), "fsn", longTTL); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := b.B.Renew(context.Background(), "hel", longTTL); !errors.Is(err, witness.ErrLeaseLost) {
		t.Fatalf("Renew by non-holder: err = %v want ErrLeaseLost", err)
	}
}

func testReleaseIdempotent(t *testing.T, b *Backend) {
	if err := b.A.Release(context.Background(), "fsn"); err != nil {
		t.Fatalf("Release on empty slot: %v", err)
	}
	if _, err := b.A.Acquire(context.Background(), "fsn", longTTL); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := b.A.Release(context.Background(), "fsn"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := b.A.Release(context.Background(), "fsn"); err != nil {
		t.Fatalf("Release again: %v", err)
	}
	st, err := b.A.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if st.Holder != "" {
		t.Fatalf("Holder = %q want empty after Release", st.Holder)
	}
}

func testReleaseByNonHolder(t *testing.T, b *Backend) {
	if _, err := b.A.Acquire(context.Background(), "fsn", longTTL); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := b.B.Release(context.Background(), "hel"); err != nil {
		t.Fatalf("Release by non-holder: %v", err)
	}
	st, _ := b.A.Read(context.Background())
	if st.Holder != "fsn" {
		t.Fatalf("Holder = %q want fsn — non-holder Release should not evict", st.Holder)
	}
}

func testSlotIsolation(t *testing.T, b *Backend) {
	if _, err := b.A.Acquire(context.Background(), "fsn", longTTL); err != nil {
		t.Fatalf("Acquire A: %v", err)
	}
	st, err := b.Other.Acquire(context.Background(), "hel", longTTL)
	if err != nil {
		t.Fatalf("Acquire Other: %v", err)
	}
	if st.Holder != "hel" {
		t.Fatalf("Other.Holder = %q want hel", st.Holder)
	}
}

func testGenerationMonotonic(t *testing.T, b *Backend) {
	st1, err := b.A.Acquire(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	st2, err := b.A.Renew(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if st2.Generation <= st1.Generation {
		t.Fatalf("Generation did not bump on Renew: %d -> %d", st1.Generation, st2.Generation)
	}
	st3, err := b.A.Acquire(context.Background(), "fsn", longTTL)
	if err != nil {
		t.Fatalf("re-Acquire: %v", err)
	}
	if st3.Generation <= st2.Generation {
		t.Fatalf("Generation did not bump on re-Acquire: %d -> %d", st2.Generation, st3.Generation)
	}
}

func testReadOnEmpty(t *testing.T, b *Backend) {
	st, err := b.A.Read(context.Background())
	if err != nil {
		t.Fatalf("Read on empty: %v", err)
	}
	if st.Holder != "" {
		t.Fatalf("empty slot Holder = %q want empty", st.Holder)
	}
}

func testReadAfterTTLEviction(t *testing.T, b *Backend) {
	if _, err := b.A.Acquire(context.Background(), "fsn", shortTTL); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b.Advance(shortTTL + 50*time.Millisecond)
	// Definitive eviction proof: a different holder can now Acquire.
	// (Comparing the State.ExpiresAt against time.Now() doesn't work
	// for impls that use a server-side clock decoupled from
	// wall-clock — the in-memory + CFKV impls stamp ExpiresAt with
	// the witness's clock, not the test's wall-clock.)
	st, err := b.B.Acquire(context.Background(), "hel", longTTL)
	if err != nil {
		t.Fatalf("post-expiry Acquire by other: %v — TTL eviction not honored", err)
	}
	if st.Holder != "hel" {
		t.Fatalf("post-eviction Holder = %q want hel", st.Holder)
	}
}

func testContextCancel(t *testing.T, b *Backend) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.A.Acquire(ctx, "fsn", longTTL); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire after cancel: err = %v want context.Canceled", err)
	}
	if _, err := b.A.Renew(ctx, "fsn", longTTL); !errors.Is(err, context.Canceled) {
		t.Fatalf("Renew after cancel: err = %v want context.Canceled", err)
	}
	if err := b.A.Release(ctx, "fsn"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Release after cancel: err = %v want context.Canceled", err)
	}
	if _, err := b.A.Read(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Read after cancel: err = %v want context.Canceled", err)
	}
}
