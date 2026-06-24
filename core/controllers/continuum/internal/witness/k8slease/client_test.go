// Tests for the k8s-lease witness (#3829). We exercise the CAS
// contract against a fake dynamic client (k8s.io/client-go/dynamic/fake)
// — the SAME fake the continuum-controller tests use — so the
// read-modify-write path runs through real apimachinery codecs.
//
// The k8s Lease object is SECOND-granular by API design
// (spec.leaseDurationSeconds is an integer), so this suite uses
// second-scale TTLs rather than the witness/testing contract suite's
// 100ms shortTTL (which assumes sub-second granularity the Lease wire
// shape cannot carry).
package k8slease

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

func newFakeDyn() *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			leaseGVR: "LeaseList",
		})
}

func newClient(t *testing.T, dyn DynLeaseAccess, slot string) *K8sLeaseClient {
	t.Helper()
	c, err := New(dyn, "catalyst-system", slot)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestFactory_RequiresDyn(t *testing.T) {
	if _, err := factory(map[string]any{"slot": "ns/x"}, nil); err == nil {
		t.Fatal("expected error when cfg[\"dyn\"] is absent")
	}
}

func TestFactory_BuildsClient(t *testing.T) {
	dyn := newFakeDyn()
	c, err := factory(map[string]any{
		"slot":      "cnpg/cnpg-pair-bp-cnpg-pair-continuum",
		"namespace": "catalyst-system",
		"dyn":       DynLeaseAccess(dyn),
	}, nil)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	kc, ok := c.(*K8sLeaseClient)
	if !ok {
		t.Fatalf("want *K8sLeaseClient, got %T", c)
	}
	if kc.LeaseName != "cw-cnpg-cnpg-pair-bp-cnpg-pair-continuum" {
		t.Fatalf("unexpected lease name %q", kc.LeaseName)
	}
}

// TestAcquireOnEmptyCreatesLease is the core happy path that was broken
// live: a healthy 2-region pair must yield a held lease so the Continuum
// CR goes Ready=True / LeaseHeld=True with leaseHolder=<primary>.
func TestAcquireOnEmptyCreatesLease(t *testing.T) {
	dyn := newFakeDyn()
	c := newClient(t, dyn, "cnpg/dr")

	st, err := c.Acquire(context.Background(), "hw-me-east-215-a-rtz-prod", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if st.Holder != "hw-me-east-215-a-rtz-prod" {
		t.Fatalf("holder = %q, want region-a", st.Holder)
	}
	if st.Generation != 1 {
		t.Fatalf("generation = %d, want 1", st.Generation)
	}
	if st.ExpiresAt.IsZero() || !st.ExpiresAt.After(time.Now()) {
		t.Fatalf("expiresAt not in the future: %v", st.ExpiresAt)
	}

	// Read back through a fresh client (simulating the reconcile loop's
	// status read) — the lease must be visible + held.
	got, err := newClient(t, dyn, "cnpg/dr").Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Holder != "hw-me-east-215-a-rtz-prod" {
		t.Fatalf("read-back holder = %q", got.Holder)
	}
}

func TestAcquireBlockedByAnotherHolder(t *testing.T) {
	dyn := newFakeDyn()
	a := newClient(t, dyn, "cnpg/dr")
	b := newClient(t, dyn, "cnpg/dr")

	if _, err := a.Acquire(context.Background(), "region-a", 30*time.Second); err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	_, err := b.Acquire(context.Background(), "region-b", 30*time.Second)
	if err != witness.ErrLeaseHeldByAnother {
		t.Fatalf("b.Acquire err = %v, want ErrLeaseHeldByAnother", err)
	}
}

func TestAcquireSameHolderExtendsAndPreservesAcquiredAt(t *testing.T) {
	dyn := newFakeDyn()
	c := newClient(t, dyn, "cnpg/dr")

	first, err := c.Acquire(context.Background(), "region-a", 30*time.Second)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	time.Sleep(1100 * time.Millisecond) // cross a second boundary
	second, err := c.Acquire(context.Background(), "region-a", 30*time.Second)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if !second.AcquiredAt.Equal(first.AcquiredAt) {
		t.Fatalf("acquiredAt drifted: first=%v second=%v", first.AcquiredAt, second.AcquiredAt)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("generation did not bump: %d -> %d", first.Generation, second.Generation)
	}
}

func TestRenewExtendsAndBumpsGeneration(t *testing.T) {
	dyn := newFakeDyn()
	c := newClient(t, dyn, "cnpg/dr")

	acq, err := c.Acquire(context.Background(), "region-a", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	rn, err := c.Renew(context.Background(), "region-a", 30*time.Second)
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if rn.Generation <= acq.Generation {
		t.Fatalf("renew did not bump generation: %d -> %d", acq.Generation, rn.Generation)
	}
}

func TestRenewByNonHolderReturnsLost(t *testing.T) {
	dyn := newFakeDyn()
	a := newClient(t, dyn, "cnpg/dr")
	if _, err := a.Acquire(context.Background(), "region-a", 30*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	b := newClient(t, dyn, "cnpg/dr")
	if _, err := b.Renew(context.Background(), "region-b", 30*time.Second); err != witness.ErrLeaseLost {
		t.Fatalf("Renew(non-holder) = %v, want ErrLeaseLost", err)
	}
}

// TestAcquireAfterExpiryAllowsFailover proves the region-kill path: an
// expired lease (the dead primary stopped renewing) is takeable by the
// standby region.
func TestAcquireAfterExpiryAllowsFailover(t *testing.T) {
	dyn := newFakeDyn()
	a := newClient(t, dyn, "cnpg/dr")
	// 1s TTL — the smallest the Lease wire shape carries.
	if _, err := a.Acquire(context.Background(), "region-a", 1*time.Second); err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	// Wait past expiry (renewTime + 1s).
	time.Sleep(2100 * time.Millisecond)

	b := newClient(t, dyn, "cnpg/dr")
	st, err := b.Acquire(context.Background(), "region-b", 30*time.Second)
	if err != nil {
		t.Fatalf("b.Acquire after expiry = %v, want success (failover)", err)
	}
	if st.Holder != "region-b" {
		t.Fatalf("post-failover holder = %q, want region-b", st.Holder)
	}
}

func TestRenewAfterExpiryReturnsLost(t *testing.T) {
	dyn := newFakeDyn()
	c := newClient(t, dyn, "cnpg/dr")
	if _, err := c.Acquire(context.Background(), "region-a", 1*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	time.Sleep(2100 * time.Millisecond)
	if _, err := c.Renew(context.Background(), "region-a", 1*time.Second); err != witness.ErrLeaseLost {
		t.Fatalf("Renew after expiry = %v, want ErrLeaseLost", err)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	dyn := newFakeDyn()
	c := newClient(t, dyn, "cnpg/dr")
	if _, err := c.Acquire(context.Background(), "region-a", 30*time.Second); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := c.Release(context.Background(), "region-a"); err != nil {
		t.Fatalf("Release: %v", err)
	}
	// Slot is now free; a new holder can take it.
	st, err := newClient(t, dyn, "cnpg/dr").Acquire(context.Background(), "region-b", 30*time.Second)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if st.Holder != "region-b" {
		t.Fatalf("holder after release+acquire = %q", st.Holder)
	}
	// Non-holder Release is a no-op (does not evict region-b).
	if err := c.Release(context.Background(), "region-a"); err != nil {
		t.Fatalf("non-holder Release: %v", err)
	}
	got, _ := newClient(t, dyn, "cnpg/dr").Read(context.Background())
	if got.Holder != "region-b" {
		t.Fatalf("non-holder release evicted the live holder: %q", got.Holder)
	}
}

func TestSlotIsolation(t *testing.T) {
	dyn := newFakeDyn()
	a := newClient(t, dyn, "cnpg/dr-one")
	b := newClient(t, dyn, "cnpg/dr-two")
	if _, err := a.Acquire(context.Background(), "region-a", 30*time.Second); err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	// b's slot is independent — acquiring it must succeed.
	if _, err := b.Acquire(context.Background(), "region-x", 30*time.Second); err != nil {
		t.Fatalf("b.Acquire (different slot) = %v, want success", err)
	}
	if a.LeaseName == b.LeaseName {
		t.Fatalf("distinct slots produced identical lease names: %q", a.LeaseName)
	}
}

func TestReadOnEmptySlotIsFree(t *testing.T) {
	dyn := newFakeDyn()
	st, err := newClient(t, dyn, "cnpg/dr").Read(context.Background())
	if err != nil {
		t.Fatalf("Read empty: %v", err)
	}
	if st.Holder != "" {
		t.Fatalf("empty slot reported holder %q", st.Holder)
	}
}

func TestLeaseObjectNameEncoding(t *testing.T) {
	if got := leaseObjectName("cnpg/cnpg-pair-bp-cnpg-pair-continuum"); got != "cw-cnpg-cnpg-pair-bp-cnpg-pair-continuum" {
		t.Fatalf("encoding = %q", got)
	}
}
