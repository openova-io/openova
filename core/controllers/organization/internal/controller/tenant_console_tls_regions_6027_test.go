// tenant_console_tls_regions_6027_test.go — #6027. The witness that decides
// how many regions this Sovereign serves must not read "absent" as "one".
//
// THE DEFECT
// ----------
// #5246/#5957 made the per-Org console listener pair fan out over every region,
// and guarded the fan-out with an INDEPENDENT witness so the region set could
// not be silently truncated by the same step that loses a region: the Cilium
// ClusterMesh config Secret `kube-system/cilium-clustermesh`, whose
// non-certificate keys name every remote cluster.
//
// That witness is absent on a Sovereign that has not established ClusterMesh,
// and `apierrors.IsNotFound` was folded into "zero expected secondaries". So a
// 2-region Sovereign whose mesh is not up is INDISTINGUISHABLE from a
// single-region one, and the guard written to stop a green Org over an
// unwritten region reports no shortfall at all.
//
// Measured read-only on hw293 dep a0077ba47e3720e5, 2026-08-11, both regions:
//
//	kube-system/cilium-clustermesh                 -> NotFound in BOTH regions
//	catalyst/cutover-secondary-kubeconfigs         -> NotFound (its only
//	                                                  producer is runCutover,
//	                                                  and the cutover chart is
//	                                                  still installing, #6004)
//	region A kube-system/cilium-gateway-console    -> console-https-hw293walkone,
//	                                                  console-https-hw293walktwo
//	region B kube-system/cilium-gateway-console    -> the three Sovereign pairs,
//	                                                  ZERO per-Org listeners
//
// and both Organizations nevertheless read fully provisioned. UAT rows R16 /
// 87 / 90 / 95 each measured the customer-visible half of that: 6/12, 8/12 and
// 5/12 fresh-TCP `curl(35) Connection reset by peer` against a per-Org host,
// while the apex host on the SAME VIP answers every time.
//
// THE WITNESS THAT IS ACTUALLY THERE
// ----------------------------------
// `catalyst-system/sovereign-fqdn` carries `configuredRegions` — the
// comma-separated region keys the Sovereign was provisioned with. On hw293 it
// reads `hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod`, it is written at
// bootstrap by the catalyst-platform chart, and it is independent of BOTH
// ClusterMesh and the cutover chart. catalyst-api already consumes exactly this
// key as `CATALYST_CONFIGURED_REGIONS`; the org-controller reaches it the same
// way — a kubelet-injected `configMapKeyRef`, which needs no new RBAC (the
// org-controller SA is denied ConfigMap reads: `auth can-i get configmaps -n
// catalyst-system` -> no, against a `secrets` control that answers yes).
//
// WHAT IS ASSERTED HERE
// ---------------------
// Every case asserts on the VALUE the resolution produced — the shortfall count
// and the region named in it — and each carries a control that answers the
// other way in the same fixture, so none can pass vacuously:
//
//  1. The live hw293 shape (topology declares 2, no mesh, no bridge) reports a
//     shortfall and holds the Org back. The control is the same fixture with
//     the topology witness declaring ONE region, which stays clean.
//  2. The topology witness cannot SHRINK a set the mesh witness declares
//     larger, and vice versa — the expectation is the max of the two, so
//     neither witness alone can hide a region.
//  3. A wired bridge satisfies the topology witness, so the fix reports a
//     shortfall only when one genuinely exists.
//  4. With NEITHER witness present the behaviour is byte-for-byte what it was
//     before this change — legacy and Catalyst-Zero Sovereigns do not start
//     red-flagging every Organization.
package controller

import (
	"context"
	"strings"
	"testing"
)

// hw293ConfiguredRegions mirrors the live `catalyst-system/sovereign-fqdn`
// ConfigMap key `configuredRegions` read off hw293 on 2026-08-11.
const hw293ConfiguredRegions = "hw-me-east-215-a-rtz-prod,hw-me-east-215-b-rtz-prod"

// TestConsoleRegionTargets_TopologyWitnessCatchesUnmeshedSecondary_6027 is the
// hw293 reproduction: a 2-region Sovereign with no ClusterMesh Secret and no
// kubeconfig bridge must report the secondary region as UNWIRED, not resolve to
// a one-region set and call the Organization complete.
func TestConsoleRegionTargets_TopologyWitnessCatchesUnmeshedSecondary_6027(t *testing.T) {
	ctx := context.Background()

	// The live shape: mesh witness absent, bridge absent, topology declares 2.
	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions

	res := f.r.consoleRegionTargets(ctx)

	if len(res.Targets) != 1 || !res.Targets[0].Host {
		t.Fatalf("precondition: with no kubeconfig wired the only writable target is the host region, got %d targets", len(res.Targets))
	}
	if len(res.Unwired) == 0 {
		t.Fatalf("a 2-region Sovereign with zero secondary kubeconfigs reported NO shortfall — "+
			"the per-Org listener can never reach region B, yet the fan-out claims it wrote everywhere. "+
			"unwired=%v unreachable=%v", res.Unwired, res.Unreachable)
	}
	// Assert on the VALUE, not on the key: the message must name the count
	// that is missing and the witness that declared it, or an operator cannot
	// tell a real shortfall from a witness that failed to load.
	joined := strings.Join(res.Unwired, " | ")
	if !strings.Contains(joined, "1 of 1 secondary region") {
		t.Fatalf("shortfall message does not name the 1-of-1 count an operator needs: %q", joined)
	}
	if !strings.Contains(joined, "configuredRegions") {
		t.Fatalf("shortfall message does not name the witness that declared the region, so its warrant cannot be checked: %q", joined)
	}

	// The consumer half — the Org must not read provisioned.
	got := f.r.verifyProvisioned(ctx, f.org)
	if got.complete() {
		t.Fatalf("Organization reads fully provisioned while its console listener never reached the declared secondary region. missing=%v unverifiable=%v",
			got.Missing, got.Unverifiable)
	}

	// CONTROL, same fixture, single-region topology: no shortfall, and the Org
	// is not held back by this check. Proves case 1 discriminates on the region
	// COUNT rather than firing on any Sovereign with no bridge Secret.
	single := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	single.r.ConfiguredRegions = "hw-me-east-215-a-rtz-prod"
	sres := single.r.consoleRegionTargets(ctx)
	if len(sres.Unwired) != 0 || len(sres.Unreachable) != 0 {
		t.Fatalf("control failed: a genuinely single-region Sovereign reported unwired=%v unreachable=%v", sres.Unwired, sres.Unreachable)
	}
}

// TestConsoleRegionTargets_NeitherWitnessMayShrinkTheOther_6027 pins the
// combination rule. The expectation is the MAX of the two witnesses, so a
// witness that under-reports cannot hide a region the other one proves.
func TestConsoleRegionTargets_NeitherWitnessMayShrinkTheOther_6027(t *testing.T) {
	ctx := context.Background()

	// Mesh declares 1 remote; topology declares a single region. The mesh must
	// win — the old behaviour, which this change must not weaken.
	meshOnly := newMultiRegionFixture(t, true, false, regionGatewayListeners(9443, 9080))
	meshOnly.r.ConfiguredRegions = "hw-me-east-215-a-rtz-prod"
	mres := meshOnly.r.consoleRegionTargets(ctx)
	if len(mres.Unwired) == 0 {
		t.Fatalf("a topology witness naming ONE region silently shrank a mesh witness that declares a remote cluster: unwired=%v", mres.Unwired)
	}

	// Topology declares 2; mesh absent. The topology must win — the #6027 case.
	topoOnly := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	topoOnly.r.ConfiguredRegions = hw293ConfiguredRegions
	tres := topoOnly.r.consoleRegionTargets(ctx)
	if len(tres.Unwired) == 0 {
		t.Fatalf("an absent mesh witness silently shrank a topology witness that declares 2 regions: unwired=%v", tres.Unwired)
	}

	// Both declare the same single remote — the expectation must be 1, not the
	// 2 a naive sum would produce. Wire the bridge so a correct expectation of
	// 1 is fully satisfied and reports nothing.
	both := newMultiRegionFixture(t, true, true, regionGatewayListeners(9443, 9080))
	both.r.ConfiguredRegions = hw293ConfiguredRegions
	bres := both.r.consoleRegionTargets(ctx)
	if len(bres.Unwired) != 0 {
		t.Fatalf("two witnesses naming the SAME one remote region were summed instead of maxed — one wired kubeconfig should satisfy them: unwired=%v", bres.Unwired)
	}
	if len(bres.Targets) != 2 {
		t.Fatalf("expected host + 1 secondary target, got %d", len(bres.Targets))
	}
}

// TestConsoleRegionTargets_TopologyWitnessSatisfiedByWiredBridge_6027 proves the
// new witness reports a shortfall only when one genuinely exists.
func TestConsoleRegionTargets_TopologyWitnessSatisfiedByWiredBridge_6027(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, true, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = hw293ConfiguredRegions

	res := f.r.consoleRegionTargets(ctx)
	if len(res.Unwired) != 0 {
		t.Fatalf("topology declares 2 regions and the one secondary kubeconfig IS wired, yet a shortfall was reported: %v", res.Unwired)
	}
	if len(res.Targets) != 2 {
		t.Fatalf("expected host + 1 secondary target, got %d", len(res.Targets))
	}
	if res.Targets[1].Region != secondaryRegionKey {
		t.Fatalf("secondary target names %q, want the bridge Secret's region key %q", res.Targets[1].Region, secondaryRegionKey)
	}
}

// TestConsoleRegionTargets_NoWitnessAtAllIsUnchanged_6027 is the no-regression
// control for legacy and Catalyst-Zero Sovereigns: with neither witness present
// the resolution is exactly what it was before #6027.
func TestConsoleRegionTargets_NoWitnessAtAllIsUnchanged_6027(t *testing.T) {
	ctx := context.Background()

	f := newMultiRegionFixture(t, false, false, regionGatewayListeners(9443, 9080))
	f.r.ConfiguredRegions = "" // no sovereign-fqdn key injected

	res := f.r.consoleRegionTargets(ctx)
	if len(res.Unwired) != 0 || len(res.Unreachable) != 0 {
		t.Fatalf("a Sovereign with neither witness started reporting a shortfall — legacy envs would red-flag every Organization: unwired=%v unreachable=%v",
			res.Unwired, res.Unreachable)
	}
	if len(res.Targets) != 1 || !res.Targets[0].Host {
		t.Fatalf("expected the host-only target set, got %d targets", len(res.Targets))
	}
}
