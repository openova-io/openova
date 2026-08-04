package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// Tests for #5489 — `kubectl get organizations -o wide` printed
// `vCluster: Ready` for host-tier Orgs. The controller stamped
// status.vcluster{name, hostCluster, phase} UNCONDITIONALLY, so a
// namespace-isolated Org (which authors NO vCluster — the host `<slug>`
// namespace is the boundary) carried a green phase over an unauthored
// object, and the CRD printer column (products/catalyst/chart/crds/
// organization.yaml, .status.vcluster.phase) surfaced it to the operator.
// The Ready condition MESSAGE was already honest (#4813) while the field
// beside it was not; predecessor #3669.
//
// Anti-theater: the host-tier assertions fail against the pre-fix code, and
// the vcluster-tier rows are the control — a fix that blanked the block for
// every tier would satisfy "no fake Ready" while erasing the real status of
// Orgs that DO have a vCluster.

func TestVClusterStatusFor_TierMatrix(t *testing.T) {
	t.Parallel()

	// Host tiers (""/s/free — gitops.BoundaryIsVcluster == false): the block
	// stays zero, so the printer column renders blank instead of a
	// fabricated phase.
	for _, plan := range []string{"", "s", "free"} {
		got := vclusterStatusFor("acme", "ct-eu-mgt-prod", plan, "Ready")
		if got != (orgapi.VClusterStatus{}) {
			t.Errorf("plan=%q: host tier must stamp an empty vcluster block, got %+v", plan, got)
		}
	}

	// vcluster tiers (m/l/xl/flexi): the exact pre-#5489 stamp survives —
	// name + hostCluster + whatever phase vclusterReadiness derived.
	for _, plan := range []string{"m", "l", "xl", "flexi"} {
		got := vclusterStatusFor("acme", "ct-eu-mgt-prod", plan, "Provisioning")
		want := orgapi.VClusterStatus{Name: "acme", HostCluster: "ct-eu-mgt-prod", Phase: "Provisioning"}
		if got != want {
			t.Errorf("plan=%q: got %+v want %+v", plan, got, want)
		}
	}
}

// TestVClusterStatusFor_SerializedAbsence proves the honest wire shape, in
// both directions: the host-tier status JSON carries no phase (the printer
// column's jsonPath resolves to nothing → blank cell), while the
// vcluster-tier control DOES carry it. The control is what makes the
// negative assertion non-vacuous — a marshalling change that dropped the
// field everywhere would trip it.
func TestVClusterStatusFor_SerializedAbsence(t *testing.T) {
	t.Parallel()

	host, err := json.Marshal(orgapi.OrganizationStatus{
		VCluster: vclusterStatusFor("acme", "hc", "s", "Ready"),
	})
	if err != nil {
		t.Fatalf("marshal host-tier: %v", err)
	}
	if strings.Contains(string(host), `"phase"`) {
		t.Errorf("host-tier status must serialize with no vcluster phase, got %s", host)
	}

	vc, err := json.Marshal(orgapi.OrganizationStatus{
		VCluster: vclusterStatusFor("acme", "hc", "m", "Ready"),
	})
	if err != nil {
		t.Fatalf("marshal vcluster-tier: %v", err)
	}
	if !strings.Contains(string(vc), `"phase":"Ready"`) {
		t.Errorf("vcluster-tier control must keep phase Ready, got %s", vc)
	}
}

// TestReconcile_HostTier_NoVClusterStatusStamp is the end-to-end proof at
// the Reconcile seam: a fully-provisioned host-tier Org (plan s — namespace
// Active + boundary quota/limits seeded, no HR because none is ever
// authored) goes Ready=True with the honest namespace-boundary message, and
// its status.vcluster block stays EMPTY — where the pre-fix controller
// stamped {name: acme, hostCluster: …, phase: Ready} over nothing.
func TestReconcile_HostTier_NoVClusterStatusStamp(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	org.Spec.PlanSlug = "s"

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}
	r, _, _ := makeReconciler(t, org, ns,
		boundaryLimitRange("acme"), boundaryResourceQuota("acme"))

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("a fully-provisioned host-tier Org must stop the requeue, got %v", res)
	}

	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get post-reconcile: %v", err)
	}
	if got.Status.VCluster != (orgapi.VClusterStatus{}) {
		t.Errorf("host-tier Org must not stamp status.vcluster (no vCluster is authored), got %+v",
			got.Status.VCluster)
	}
	if got.Status.Conditions[0].Type != "Ready" || got.Status.Conditions[0].Status != "True" {
		t.Errorf("host-tier Org must still go Ready=True off the namespace boundary, got %+v",
			got.Status.Conditions[0])
	}
	// The message keeps naming the REAL boundary — that honesty (#4813) is
	// what the empty vcluster block now matches instead of contradicting.
	if !strings.Contains(got.Status.Conditions[0].Message, "no vCluster authored") {
		t.Errorf("Ready message must name the namespace boundary, got %q",
			got.Status.Conditions[0].Message)
	}
}
