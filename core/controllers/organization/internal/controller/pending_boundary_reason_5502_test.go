package controller

import "testing"

// #5502: the Ready=False reason must name the artifact the Org is ACTUALLY
// waiting on, keyed off the same #4292/#4339 tier gate that vclusterReadiness
// and readyOrgMessage use.
//
// Both directions are asserted deliberately. A test that only checked the
// host-tier branch would pass against a function that returned
// "NamespaceProvisioning" unconditionally — which would be a NEW lie for the
// vcluster tier, the exact mirror of the bug being fixed. Asserting only the
// vcluster branch would have passed against the original hardcoded string and
// caught nothing at all.
func TestPendingBoundaryReasonIsTierAware(t *testing.T) {
	// Host-tier: no vCluster HelmRelease is ever authored for these plans
	// (gitops.Render omits vcluster.yaml), so the pending artifact is the host
	// `<slug>` namespace. Reporting VClusterProvisioning here sent the hw291
	// uatcorp walk hunting a vCluster that correctly does not exist.
	for _, plan := range []string{"", "s", "S", "free", "  s  "} {
		if got := pendingBoundaryReason(plan); got != "NamespaceProvisioning" {
			t.Errorf("pendingBoundaryReason(%q) = %q, want NamespaceProvisioning (host-tier authors no vCluster HR)", plan, got)
		}
	}

	// vcluster tier: unchanged — these plans really do wait on the vCluster HR.
	for _, plan := range []string{"m", "l", "xl", "flexi", "M"} {
		if got := pendingBoundaryReason(plan); got != "VClusterProvisioning" {
			t.Errorf("pendingBoundaryReason(%q) = %q, want VClusterProvisioning (vcluster tier waits on the HR)", plan, got)
		}
	}
}

// The pending reason and the Ready=True message must agree about which
// boundary backs the Org. If they ever disagree, one of the two is lying about
// the same tier — the asymmetry #5502 fixed (honest message, wrong reason)
// would reappear silently on any future edit to either helper.
func TestPendingBoundaryReasonAgreesWithReadyOrgMessage(t *testing.T) {
	cases := []struct {
		plan          string
		wantNamespace bool
	}{
		{"", true}, {"s", true}, {"free", true},
		{"m", false}, {"l", false}, {"xl", false}, {"flexi", false},
	}
	for _, tc := range cases {
		reasonSaysNamespace := pendingBoundaryReason(tc.plan) == "NamespaceProvisioning"
		msg := readyOrgMessage(tc.plan)
		msgSaysNamespace := msg == "host namespace Active + Keycloak group + Gitea Org reconciled (namespace-isolated tier — no vCluster authored)"

		if reasonSaysNamespace != msgSaysNamespace {
			t.Errorf("plan %q: pending reason and ready message disagree on the boundary (reason-says-namespace=%v, message-says-namespace=%v, message=%q)",
				tc.plan, reasonSaysNamespace, msgSaysNamespace, msg)
		}
		if reasonSaysNamespace != tc.wantNamespace {
			t.Errorf("plan %q: boundary = namespace? got %v want %v", tc.plan, reasonSaysNamespace, tc.wantNamespace)
		}
	}
}
