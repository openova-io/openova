package controller

import (
	"context"
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"

	"k8s.io/apimachinery/pkg/types"
)

// #4993 — the org-controller must NOT clobber `vcluster/host-apps/kustomization.yaml`
// once it exists. The funnel merges the host-native per-app HTTPRoutes
// (app-<x>-hostroute.yaml — the durable fix for the vcluster-tier 404, since loft
// vcluster 0.33.4 syncs no httproutes) into that index alongside the CNP +
// provisioning-rbac baseline. If a subsequent Org reconcile force-overwrote it
// with its baseline-only list, Flux's `kustomize build ./vcluster/host-apps` would
// drop the app routes and the customer's app would 404 again. So the controller
// SEEDS the index once (baseline) and never overwrites it — exactly like the apps
// kustomization (#4384). The baseline DOCS (ciliumnetworkpolicy.yaml /
// provisioning-rbac.yaml) are still authored + kept current via their own PutFile.
func TestReconcile_HostAppsKustomization_SeedOnly_NotClobbered_4993(t *testing.T) {
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

	// Simulate the funnel cart install merging the customer's host-native app
	// route into the index (the read-modify-write the provisioning service does).
	merged := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-wordpress-hostroute.yaml
  - ciliumnetworkpolicy.yaml
  - provisioning-rbac.yaml
`
	gs.files[kustKey] = fileEntry{sha: seeded.sha, content: []byte(merged)}

	// Re-reconcile. The org-controller must leave the funnel-merged index untouched.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	after := string(gs.files[kustKey].content)
	if !strings.Contains(after, "app-wordpress-hostroute.yaml") {
		t.Errorf("regression: org-controller CLOBBERED the funnel-merged host-apps kustomization — app route dropped (404 returns):\n%s", after)
	}
	for _, want := range []string{"ciliumnetworkpolicy.yaml", "provisioning-rbac.yaml"} {
		if !strings.Contains(after, want) {
			t.Errorf("host-apps kustomization lost baseline %q after reconcile:\n%s", want, after)
		}
	}
}
