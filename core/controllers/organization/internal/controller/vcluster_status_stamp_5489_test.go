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

// Tests for the status.vcluster stamp (#5489, re-authored 2026-09-10).
//
// #5489 asked that `kubectl get organizations -o wide` never print
// `vCluster: Ready` over an UNAUTHORED vCluster — which it did for the free/S
// Organizations the #4292 tier gate kept on a bare host namespace. That gate is
// gone: every Organization authors a real vCluster HelmRelease, so the honest
// stamp is the same for every plan — name + hostCluster + the phase the live
// readback derived — and the honest phase for an Organization whose vCluster
// is not up yet is Pending/Provisioning, never blank and never Ready.
//
// Anti-theater: the plan-s reconcile below FAILS against the pre-change
// controller (it went Ready=True off the namespace with an empty vcluster
// block), and the second half is the control — the same Organization goes
// Ready once its HR is Ready, so a stamp that were Pending unconditionally
// could not pass.

func TestVClusterStatusFor_EveryOrganizationIsStamped(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"Pending", "Provisioning", "Ready"} {
		got := vclusterStatusFor("acme", "ct-eu-mgt-prod", phase)
		want := orgapi.VClusterStatus{Name: "acme", HostCluster: "ct-eu-mgt-prod", Phase: phase}
		if got != want {
			t.Errorf("phase=%q: got %+v want %+v", phase, got, want)
		}
	}
}

// TestVClusterStatusFor_SerializedPhase proves the wire shape the CRD printer
// column reads (.status.vcluster.phase) carries the derived phase.
func TestVClusterStatusFor_SerializedPhase(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(orgapi.OrganizationStatus{
		VCluster: vclusterStatusFor("acme", "hc", "Ready"),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"phase":"Ready"`) {
		t.Errorf("status must serialize the vcluster phase, got %s", raw)
	}
}

// TestReconcile_PlanS_WaitsOnItsVcluster is the end-to-end proof at the
// Reconcile seam that plan s is no longer special. With the namespace + the
// plan quota/limits present but NO vCluster HelmRelease, the Organization is
// Ready=False:VClusterProvisioning with status.vcluster stamped Pending and a
// requeue armed — exactly what a plan-m Organization gets in the same state.
// Once the HR is Ready it goes Ready=True with the vCluster-naming message.
func TestReconcile_PlanS_WaitsOnItsVcluster(t *testing.T) {
	t.Parallel()
	for _, plan := range []string{"s", "free", ""} {
		plan := plan
		t.Run("plan="+plan, func(t *testing.T) {
			t.Parallel()
			org := sampleOrg()
			org.Spec.PlanSlug = plan
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme"}}

			// No HR yet: must NOT be Ready, must stamp a Pending vcluster block.
			r, _, _ := makeReconciler(t, org, ns,
				boundaryLimitRange("acme"), boundaryResourceQuota("acme"))
			res, err := r.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "acme"},
			})
			if err != nil {
				t.Fatalf("reconcile error: %v", err)
			}
			if res.RequeueAfter == 0 {
				t.Errorf("an Organization waiting on its vCluster must requeue, got %v", res)
			}
			var got orgapi.Organization
			if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
				t.Fatalf("get post-reconcile: %v", err)
			}
			if got.Status.VCluster.Name != "acme" || got.Status.VCluster.Phase != "Pending" {
				t.Errorf("status.vcluster must be stamped Pending for an Organization whose vCluster HR has not landed, got %+v",
					got.Status.VCluster)
			}
			ready := got.Status.Conditions[0]
			if ready.Type != "Ready" || ready.Status != "False" || ready.Reason != pendingBoundaryReason {
				t.Errorf("want Ready=False reason %q while the vCluster HR is absent, got %+v", pendingBoundaryReason, ready)
			}

			// Control: with a Ready HR the same Organization goes Ready.
			r2, _, _ := makeReconciler(t, sampleOrgWithPlan(plan), ns.DeepCopy(),
				boundaryLimitRange("acme"), boundaryResourceQuota("acme"), readyVClusterHR("acme"))
			if _, err := r2.Reconcile(context.Background(), ctrl.Request{
				NamespacedName: types.NamespacedName{Name: "acme"},
			}); err != nil {
				t.Fatalf("reconcile (with HR) error: %v", err)
			}
			var ok orgapi.Organization
			if err := r2.Get(context.Background(), client.ObjectKey{Name: "acme"}, &ok); err != nil {
				t.Fatalf("get post-reconcile (with HR): %v", err)
			}
			if ok.Status.VCluster.Phase != "Ready" {
				t.Errorf("with a Ready HR status.vcluster.phase must be Ready, got %+v", ok.Status.VCluster)
			}
			if c := ok.Status.Conditions[0]; c.Type != "Ready" || c.Status != "True" || c.Message != readyOrgMessage {
				t.Errorf("with a Ready HR want Ready=True message %q, got %+v", readyOrgMessage, c)
			}
		})
	}
}

// sampleOrgWithPlan is sampleOrg with the plan slug set — a fresh object per
// reconciler so the two fake clients in the test above never share state.
func sampleOrgWithPlan(plan string) *orgapi.Organization {
	org := sampleOrg()
	org.Spec.PlanSlug = plan
	return org
}
