package controller

import (
	"context"
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"k8s.io/apimachinery/pkg/types"
)

// #4993 + #5104 — the org-controller's `vcluster/host-apps/kustomization.yaml`
// write is a RECONCILING MERGE (formerly seed-only): it must never clobber the
// funnel-merged host-native app-route entries (app-<x>-hostroute.yaml — the
// durable fix for the vcluster-tier 404, since loft vcluster 0.33.4 syncs no
// httproutes; dropping them would 404 the customer's app again), it must heal
// its own baseline entries (ciliumnetworkpolicy.yaml / provisioning-rbac.yaml)
// back into an index the funnel committed first, and it must leave a complete
// index byte-untouched (write-free steady state).
func TestReconcile_HostAppsKustomization_MergesBaseline_NeverClobbers_5104(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, gs, _ := makeReconciler(t, org)

	// First reconcile seeds vcluster/host-apps/kustomization.yaml.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	const kustKey = "acme/catalyst-tenant/vcluster/host-apps/kustomization.yaml"
	seeded, ok := gs.files[kustKey]
	if !ok {
		t.Fatalf("controller did not seed %s", kustKey)
	}
	for _, want := range []string{"ciliumnetworkpolicy.yaml", "provisioning-rbac.yaml"} {
		if !strings.Contains(string(seeded.content), want) {
			t.Fatalf("seeded host-apps kustomization missing baseline %q:\n%s", want, seeded.content)
		}
	}

	// Simulate the funnel having committed ITS index FIRST, missing the
	// provisioning-rbac baseline entry (the #5104 funnel-first shape — an
	// older funnel build that predates a controller baseline doc).
	merged := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-wordpress-hostroute.yaml
  - ciliumnetworkpolicy.yaml
`
	gs.files[kustKey] = fileEntry{sha: seeded.sha, content: []byte(merged)}

	// Re-reconcile: funnel route entry preserved, missing baseline healed.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	after := string(gs.files[kustKey].content)
	if !strings.Contains(after, "- app-wordpress-hostroute.yaml") {
		t.Errorf("regression (#4993 class): org-controller dropped the funnel's app route from the host-apps kustomization (404 returns):\n%s", after)
	}
	for _, want := range []string{"ciliumnetworkpolicy.yaml", "provisioning-rbac.yaml"} {
		if !strings.Contains(after, "- "+want) {
			t.Errorf("host-apps kustomization missing baseline %q after merge reconcile (#5104):\n%s", want, after)
		}
	}

	// Third reconcile on the now-complete index: byte-untouched.
	healed := gs.files[kustKey]
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if got := string(gs.files[kustKey].content); got != string(healed.content) {
		t.Errorf("steady-state reconcile churned the host-apps kustomization:\n--- before ---\n%s\n--- after ---\n%s", healed.content, got)
	}
}
