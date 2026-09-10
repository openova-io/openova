// per_org_flux_suspend_test.go — billing enforcement (products/chargeback
// DESIGN.md §9.7). While spec.suspended is set the per-Org Flux
// Kustomizations carry spec.suspend=true (parked: nothing new reconciles for
// the Organization) and the status carries a Suspended=True condition;
// clearing the flag un-parks them on the next reconcile and the condition is
// gone. An Organization that was never suspended has no `suspend` key and
// no such condition, so every existing spec and status is byte-identical.
package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

func TestReconcilePerOrgFlux_ParksTheKustomizationsWhileSuspended(t *testing.T) {
	const slug = "acme"
	cl := fake.NewClientBuilder().WithScheme(fluxScheme(t)).Build()
	r := &Reconciler{
		Log: logr.Discard(), HostCluster: "hz-fsn-rtz-prod",
		GiteaInClusterURL: "http://gitea-http.gitea.svc.cluster.local:3000", FluxNamespace: "flux-system", FluxIntervalSeconds: 60, Branch: "main",
		FluxGiteaSecretRef: "openova-org-tenants-git-auth",
	}
	r.Client = cl
	org := newTestOrg(slug)
	_, ksName := perOrgFluxNames(slug)
	names := []string{ksName, perOrgAppsKustomizationName(slug), perOrgHostAppsKustomizationName(slug)}

	// Not suspended: no `suspend` key at all.
	if err := r.reconcilePerOrgFlux(context.Background(), org); err != nil {
		t.Fatalf("reconcilePerOrgFlux: %v", err)
	}
	for _, name := range names {
		ks := getFlux(t, cl, fluxKustomizationGVK, "flux-system", name)
		if _, found, _ := unstructured.NestedBool(ks.Object, "spec", "suspend"); found {
			t.Errorf("%s: an Organization that is not suspended must carry no spec.suspend", name)
		}
	}

	// Suspended: every per-Org Kustomization is parked.
	org.Spec.Suspended, org.Spec.SuspendReason = true, "invoice INV-2026-00007 is 45 days overdue"
	if err := r.reconcilePerOrgFlux(context.Background(), org); err != nil {
		t.Fatalf("reconcilePerOrgFlux (suspended): %v", err)
	}
	for _, name := range names {
		ks := getFlux(t, cl, fluxKustomizationGVK, "flux-system", name)
		if s, found, _ := unstructured.NestedBool(ks.Object, "spec", "suspend"); !found || !s {
			t.Errorf("%s: spec.suspend must be true while the Organization is suspended (found=%v value=%v)", name, found, s)
		}
	}

	// Resumed: un-parked on the next reconcile.
	org.Spec.Suspended, org.Spec.SuspendReason = false, ""
	if err := r.reconcilePerOrgFlux(context.Background(), org); err != nil {
		t.Fatalf("reconcilePerOrgFlux (resumed): %v", err)
	}
	for _, name := range names {
		ks := getFlux(t, cl, fluxKustomizationGVK, "flux-system", name)
		if s, found, _ := unstructured.NestedBool(ks.Object, "spec", "suspend"); found && s {
			t.Errorf("%s: spec.suspend must be cleared once the Organization is resumed", name)
		}
	}
}

func TestSuspendedConditionIsPresentOnlyWhileSuspended(t *testing.T) {
	base := []orgapi.Condition{{Type: "Ready", Status: "True"}}
	org := newTestOrg("acme")
	if got := withSuspendedCondition(base, org); len(got) != 1 {
		t.Fatalf("not suspended: conditions = %+v, want the base only", got)
	}
	org.Spec.Suspended, org.Spec.SuspendReason = true, "prepaid balance exhausted"
	got := withSuspendedCondition(base, org)
	if len(got) != 2 || got[1].Type != "Suspended" || got[1].Status != "True" || got[1].Reason != "BillingEnforcement" || got[1].Message != "prepaid balance exhausted" {
		t.Fatalf("suspended: conditions = %+v", got)
	}
	org.Spec.SuspendReason = ""
	if got := withSuspendedCondition(base, org); got[1].Message == "" {
		t.Fatal("a suspension with no reason still says where it came from")
	}
}
