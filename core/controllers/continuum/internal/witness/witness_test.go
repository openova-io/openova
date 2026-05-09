// Tests for the witness contract + InMemoryClient + Selector dispatch.
//
// As of K-Cont-3 (#1101) the BEHAVIORAL contract — Acquire / Renew /
// Release / Read invariants — is encoded in
// `internal/witness/testing/contract.go` and exported as
// `RunContractSuite(t, factoryFn)`. THIS file invokes that suite for
// the in-memory backend; the K-Cont-3 cloudflarekv + dnsquorum impls
// invoke the SAME suite from their own test files. Behavioral drift
// between the in-memory reference and a wire impl surfaces as a test
// failure in the impl's package.
//
// What stays here: tests for the Selector dispatch + State.IsHeldBy
// helper + the SelectorFunc adapter — all in-package surfaces.

package witness_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
	// Blank-import the K-Cont-3 impl packages so their init()
	// register Factory bindings on the witness package registry.
	// The cmd/main.go binary does the same — keeping the test
	// import in sync ensures behaviour parity.
	_ "github.com/openova-io/openova/core/controllers/continuum/internal/witness/cloudflarekv"
	_ "github.com/openova-io/openova/core/controllers/continuum/internal/witness/dnsquorum"
	contracttest "github.com/openova-io/openova/core/controllers/continuum/internal/witness/testing"
)

// frozenClock returns a clock the test can advance manually. Used by
// the in-memory backend factory below.
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

// TestInMemory_ContractSuite runs the full witness behavioral contract
// against InMemoryClient. The cloudflarekv + dnsquorum impls run the
// SAME suite — see their respective _test.go files. Any divergence
// fails here and there simultaneously.
func TestInMemory_ContractSuite(t *testing.T) {
	t.Parallel()
	contracttest.RunContractSuite(t, func() *contracttest.Backend {
		store := witness.NewInMemoryStore()
		clk := newClock()
		store.SetClock(clk.Now)
		return &contracttest.Backend{
			A:       store.Client("ns/cr-main"),
			B:       store.Client("ns/cr-main"), // SAME slot — race scenarios
			Other:   store.Client("ns/cr-other"),
			Advance: clk.Advance,
		}
	})
}

func TestInMemory_State_IsHeldBy(t *testing.T) {
	t.Parallel()
	now := time.Now()
	st := witness.State{
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
	if (witness.State{}).IsHeldBy("fsn", now) {
		t.Fatalf("empty State should never claim a holder")
	}
}

func TestDefaultSelector_InMemoryRefusedInProd(t *testing.T) {
	t.Parallel()
	s := &witness.DefaultSelector{InMemoryAllowed: false}
	_, err := s.Select("in-memory", nil)
	if err == nil {
		t.Fatalf("expected refusal for in-memory in production")
	}
}

func TestDefaultSelector_InMemoryAllowed(t *testing.T) {
	t.Parallel()
	s := &witness.DefaultSelector{InMemoryAllowed: true}
	cli, err := s.Select("in-memory", map[string]any{"slot": "ns/foo"})
	if err != nil {
		t.Fatalf("Select in-memory: %v", err)
	}
	if cli == nil {
		t.Fatalf("expected non-nil Client")
	}
	cli2, _ := s.Select("in-memory", map[string]any{"slot": "ns/foo"})
	if _, err := cli.Acquire(context.Background(), "fsn", 30*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, err := cli2.Acquire(context.Background(), "hel", 30*time.Second); !errors.Is(err, witness.ErrLeaseHeldByAnother) {
		t.Fatalf("shared store: err = %v want ErrLeaseHeldByAnother", err)
	}
}

func TestDefaultSelector_UnknownKind(t *testing.T) {
	t.Parallel()
	s := &witness.DefaultSelector{}
	if _, err := s.Select("not-a-kind", nil); err == nil {
		t.Fatalf("expected error for unknown kind")
	}
}

// TestDefaultSelector_RealKindsConstructorFailure — for the real
// kinds (cloudflare-kv + dns-quorum) the DefaultSelector dispatches
// to the K-Cont-3 impls; their constructors error on missing
// required cfg keys (slot, baseURL/dnsServers, token/tsig). The
// dispatch must NOT return ErrNotImplemented.
func TestDefaultSelector_RealKindsConstructorFailure(t *testing.T) {
	t.Parallel()
	s := &witness.DefaultSelector{}
	for _, kind := range []string{"cloudflare-kv", "dns-quorum"} {
		_, err := s.Select(kind, nil)
		if err == nil {
			t.Fatalf("Select(%q) with empty cfg: expected error", kind)
		}
		if errors.Is(err, witness.ErrNotImplemented) {
			t.Fatalf("Select(%q): K-Cont-3 should NOT return ErrNotImplemented (impls are wired); got %v", kind, err)
		}
	}
}

func TestSelectorFunc(t *testing.T) {
	t.Parallel()
	want := witness.NewInMemoryStore().Client("x")
	f := witness.SelectorFunc(func(kind string, cfg map[string]any) (witness.Client, error) {
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
