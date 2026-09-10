package controller

import (
	"strings"
	"testing"
)

// #5502 asked that the Ready=False Reason name the artifact the Organization is
// ACTUALLY waiting on. When a #4292 tier gate kept free/S Organizations on a
// bare host namespace that meant two Reasons (NamespaceProvisioning for them,
// VClusterProvisioning for M+). The gate is gone (2026-09-10): every
// Organization waits on its vCluster HelmRelease, so there is ONE Reason and
// ONE Ready message, and both must name the vCluster. The reconcile-seam proof
// that a plan-s Organization actually reports this Reason lives in
// vcluster_status_stamp_5489_test.go (TestReconcile_PlanS_WaitsOnItsVcluster).
func TestPendingBoundaryReasonNamesTheVcluster(t *testing.T) {
	if pendingBoundaryReason != "VClusterProvisioning" {
		t.Errorf("pendingBoundaryReason = %q, want VClusterProvisioning — the Reason is what "+
			"`kubectl get org -o jsonpath` and the walk filter on", pendingBoundaryReason)
	}
	if strings.Contains(pendingBoundaryReason, "Namespace") {
		t.Errorf("a namespace-only pending reason is back: %q", pendingBoundaryReason)
	}
}

// The pending reason and the Ready=True message must agree about which boundary
// backs the Org. With one boundary primitive that means both name the vCluster.
func TestPendingBoundaryReasonAgreesWithReadyOrgMessage(t *testing.T) {
	reasonSaysVcluster := strings.HasPrefix(pendingBoundaryReason, "VCluster")
	msgSaysVcluster := strings.Contains(readyOrgMessage, "vCluster HelmRelease")
	if !reasonSaysVcluster || !msgSaysVcluster {
		t.Errorf("pending reason %q and ready message %q must both name the vCluster boundary",
			pendingBoundaryReason, readyOrgMessage)
	}
}
