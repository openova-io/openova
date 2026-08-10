// tier_gate_lockstep_4292_test.go — the funnel half of the #4292 TIER GATE
// lockstep contract (UAT row 100).
//
// BoundaryIsVcluster's doc-comment states the contract explicitly: it "MUST
// stay in lockstep with the org-controller's authoritative gate
// boundaryIsVcluster in core/controllers/organization/internal/gitops/
// manifests.go (const allTiersVcluster + the same free/S/"" → host-ns,
// m/l/xl/flexi → vCluster switch)".
//
// Half of that contract was documentation only: this package named
// `allTiersVcluster` in prose but never declared it, so "flip both together"
// had nothing to flip here. Flipping the controller-side const to true would
// have authored a real vCluster for every free/S Org while this funnel kept
// routing the apps tree straight at the host `<slug>` namespace with no
// kubeConfig — the apps landing outside the boundary that was just authored
// for them. At the shipped value (false) all copies agree, which is precisely
// why nothing surfaced it.
package gitops

import "testing"

// tierGateSwitchIsDeclared fails to COMPILE if this package stops declaring the
// Sovereign-level switch. A behavioural assertion cannot cover that: a const
// that does not exist is a build error, not a wrong answer, and a comment
// promising one is worth nothing.
const tierGateSwitchIsDeclared = allTiersVcluster

// TestBoundaryIsVcluster_HonoursAllTiersSwitch asserts the switch is WIRED,
// not merely present — and it asserts correctly in either switch position, so
// flipping the Sovereign-level policy does not require editing this test.
func TestBoundaryIsVcluster_HonoursAllTiersSwitch(t *testing.T) {
	t.Parallel()
	for _, slug := range []string{"", "free", "s", "S", "  s "} {
		got := BoundaryIsVcluster(slug)
		if got != tierGateSwitchIsDeclared {
			t.Errorf("BoundaryIsVcluster(%q) = %v; with allTiersVcluster=%v the "+
				"host-ns tier must return %v — the switch is declared but not "+
				"consulted", slug, got, tierGateSwitchIsDeclared, tierGateSwitchIsDeclared)
		}
	}
}

// TestBoundaryIsVcluster_PaidTiersAlwaysVcluster pins the half of the table the
// switch cannot change: a paid plan is vCluster-backed in either position.
func TestBoundaryIsVcluster_PaidTiersAlwaysVcluster(t *testing.T) {
	t.Parallel()
	// The CRD enum (products/catalyst/chart/crds/organization.yaml,
	// spec.planSlug) minus the host-ns arm.
	for _, slug := range []string{"m", "l", "xl", "flexi", "M", " flexi "} {
		if !BoundaryIsVcluster(slug) {
			t.Errorf("BoundaryIsVcluster(%q) = false, want true — m/l/xl/flexi get a "+
				"dedicated Org vCluster and the apps tree must be redirected into "+
				"it via spec.kubeConfig", slug)
		}
	}
	// Negative control: an unknown slug must NOT silently join the host-ns arm.
	// planQuota defaults an unknown slug to the smallest cap, but the BOUNDARY
	// decision is a different question and defaulting it to host-ns would put a
	// paid Org's apps outside the vCluster the controller authored.
	if !BoundaryIsVcluster("enterprise-2027") {
		t.Errorf("BoundaryIsVcluster(unknown slug) = false — an unrecognised plan " +
			"must fall to the vCluster arm, not quietly into free/S")
	}
}
