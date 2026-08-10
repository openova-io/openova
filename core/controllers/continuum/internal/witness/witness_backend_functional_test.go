// Behavioural proof of WHICH lease witness can actually take a lease —
// the control behind UAT row G3 (#6071).
//
// THE RECORDED DIAGNOSIS FOR G3 WAS WRONG, AND THIS FILE IS WHY.
//
// G3 was filed as "the Continuum's leaseClient.config.resolvers are
// hardcoded 10.43.0.10/.11/.12 while this cluster's kube-dns is
// 10.96.0.10", with the implied fix "derive the resolver set from the
// cluster". Three independent measurements below show that fix cannot
// work — it is not that it is insufficient, it is that it is not
// expressible and would not help if it were:
//
//	TestDNSQuorumRejectsADerivedResolverSet
//	  a cluster has ONE kube-dns Service IP; dns-quorum refuses to
//	  construct with fewer than 3 servers. "Derive from the cluster"
//	  produces a config the factory rejects outright.
//
//	TestDNSQuorumWriterIsNilForEveryResolverSet
//	  the registered factory hands back a client whose TXTWriter is nil
//	  for EVERY resolver set, including correct ones — so Acquire can
//	  never reach a write. This is the root cause, asserted structurally
//	  and without touching the network.
//
//	TestDNSQuorumFailsWithPlaceholderResolvers
//	  end-to-end reproduction of the live hw293 symptom.
//
//	TestK8sLeaseAcquiresLease
//	  the PAIRED POSITIVE CONTROL on the same dispatch path. Without it
//	  the suite would be satisfied by a world in which every backend is
//	  broken, which would prove nothing about the fix.
//
// All of these go through witness.DefaultSelector — the production
// dispatch path the continuum-controller itself uses — so a wiring
// change that bypasses the registry is caught too.
package witness_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness/dnsquorum"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness/k8slease"
)

// leaseGVR mirrors the (unexported) GVR the k8slease witness uses.
var leaseGVR = schema.GroupVersionResource{
	Group:    "coordination.k8s.io",
	Version:  "v1",
	Resource: "leases",
}

func newFakeDynForSelector() *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{leaseGVR: "LeaseList"},
	)
}

func selectDNSQuorum(t *testing.T, resolvers []string) (witness.Client, error) {
	t.Helper()
	sel := &witness.DefaultSelector{}
	return sel.Select("dns-quorum", map[string]any{
		"slot":      "g7doora/bp-wordpress-tenant",
		"domain":    "lease.openova.io",
		"resolvers": resolvers,
	})
}

// TestDNSQuorumRejectsADerivedResolverSet — the fix G3 asked for cannot
// even be built. A Kubernetes cluster exposes exactly ONE kube-dns
// Service ClusterIP (measured on hw293: region A 10.96.0.10, region B
// 10.97.0.10 — and note they DIFFER, so a single derived list could not
// serve both regions of one DR pair either). dns-quorum needs 3.
func TestDNSQuorumRejectsADerivedResolverSet(t *testing.T) {
	// exactly what "derive the resolver set from the cluster" yields on
	// hw293 region A.
	_, err := selectDNSQuorum(t, []string{"10.96.0.10"})
	if err == nil {
		t.Fatal("expected dns-quorum to refuse a 1-server resolver set")
	}
	if !strings.Contains(err.Error(), "at least 3 servers") {
		t.Fatalf("expected the 3-server quorum requirement, got: %v", err)
	}
	t.Logf("derive-from-cluster produces: %v", err)
}

// TestDNSQuorumWriterIsNilForEveryResolverSet — THE ROOT CAUSE,
// asserted structurally so it needs no network and cannot flake.
//
// dnsquorum's factory ends with New(servers, tsigKey, domain, slot, nil,
// nil) — the TXTWriter is a compile-time nil (client.go:213). writeQuorum
// (client.go:435) therefore refuses before a packet is sent. Acquire can
// never complete, whatever the resolvers say. This is why re-pointing
// resolvers moves the failure later rather than fixing it.
func TestDNSQuorumWriterIsNilForEveryResolverSet(t *testing.T) {
	for _, resolvers := range [][]string{
		{"10.43.0.10", "10.43.0.11", "10.43.0.12"}, // the placeholder set
		{"10.96.0.10", "10.96.0.11", "10.96.0.12"}, // this cluster's CIDR
		{"1.1.1.1", "8.8.8.8", "9.9.9.9"},          // resolvers that genuinely answer
	} {
		c, err := selectDNSQuorum(t, resolvers)
		if err != nil {
			t.Fatalf("Select(dns-quorum, %v): %v", resolvers, err)
		}
		dq, ok := c.(*dnsquorum.DNSQuorumClient)
		if !ok {
			t.Fatalf("want *dnsquorum.DNSQuorumClient, got %T", c)
		}
		if dq.Writer != nil {
			t.Fatalf("resolvers=%v: TXTWriter is non-nil — a real writer has been wired "+
				"since this guard was written; re-evaluate whether dns-quorum is usable "+
				"and whether producers may select it again", resolvers)
		}
		t.Logf("resolvers=%v -> TXTWriter is nil (Acquire can never write)", resolvers)
	}
}

// TestDNSQuorumFailsWithPlaceholderResolvers reproduces the live hw293
// symptom end-to-end: the continuum-controller logged
// "witness read-quorum unavailable — check leaseClient resolvers/wiring"
// every 10s against g7doora/bp-wordpress-tenant, and the CR sat
// phase=Degraded, LeaseHeld=False, with no leaseHolder and no standby.
func TestDNSQuorumFailsWithPlaceholderResolvers(t *testing.T) {
	c, err := selectDNSQuorum(t, []string{"10.43.0.10", "10.43.0.11", "10.43.0.12"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := c.Acquire(ctx, "hw-me-east-215-a-rtz-prod", 30*time.Second)
	if err == nil {
		t.Fatal("dns-quorum Acquire unexpectedly succeeded against the placeholder resolvers")
	}
	// Assert on the VALUE that the row actually surfaces: an empty
	// holder is what makes the console render singleton instead of DR.
	if st.Holder != "" {
		t.Fatalf("expected an empty holder on failure, got %q", st.Holder)
	}
	t.Logf("placeholder resolvers -> Acquire error: %v (holder empty, CR would go Degraded)", err)
}

// TestK8sLeaseAcquiresLease is the PAIRED POSITIVE CONTROL on the same
// dispatch path: the backend this fix switches producers to genuinely
// completes Acquire and returns the holder.
func TestK8sLeaseAcquiresLease(t *testing.T) {
	sel := &witness.DefaultSelector{}
	c, err := sel.Select("k8s-lease", map[string]any{
		"slot":      "g7doora/bp-wordpress-tenant",
		"namespace": "catalyst-system",
		"dyn":       k8slease.DynLeaseAccess(newFakeDynForSelector()),
	})
	if err != nil {
		t.Fatalf("Select(k8s-lease): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const holder = "hw-me-east-215-a-rtz-prod"
	st, err := c.Acquire(ctx, holder, 30*time.Second)
	if err != nil {
		t.Fatalf("k8s-lease Acquire failed: %v", err)
	}
	// Assert on the VALUE, not on the absence of an error: the row's
	// symptom was an empty leaseHolder, which is exactly what the
	// console reads to decide DR-vs-singleton.
	if st.Holder != holder {
		t.Fatalf("k8s-lease Acquire returned Holder=%q, want %q — an empty holder is "+
			"precisely the state that renders the Continuum Degraded with no standby",
			st.Holder, holder)
	}
	if !st.IsHeldBy(holder, time.Now()) {
		t.Fatalf("k8s-lease: IsHeldBy(%q) false immediately after a successful Acquire "+
			"(ExpiresAt=%v)", holder, st.ExpiresAt)
	}
	t.Logf("k8s-lease acquired: holder=%q expires=%v generation=%d",
		st.Holder, st.ExpiresAt, st.Generation)
}
