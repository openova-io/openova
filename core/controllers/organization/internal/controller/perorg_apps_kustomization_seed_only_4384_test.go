package controller

import (
	"context"
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"
)

// #4384 — the org-controller must NOT clobber `vcluster/apps/kustomization.yaml`
// once it exists. The funnel/day-2 cart install read-modify-writes that index
// to enumerate the customer's purchased Applications (alongside the NP/CNP
// boundary baseline). If a subsequent org reconcile overwrote it with its
// baseline-only list, Flux's `kustomize build ./vcluster/apps` would drop the
// customer's apps and the wordpress HelmRelease would never land. So the
// controller SEEDS the index once and never overwrites it.
func TestReconcile_AppsKustomization_SeedOnly_NotClobbered_4384(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, gs, _ := makeReconciler(t, org)

	// First reconcile seeds vcluster/apps/kustomization.yaml.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	const kustKey = "acme/catalyst-tenant/vcluster/apps/kustomization.yaml"
	seeded, ok := gs.files[kustKey]
	if !ok {
		t.Fatalf("controller did not seed %s", kustKey)
	}
	if !strings.Contains(string(seeded.content), "networkpolicy.yaml") {
		t.Fatalf("seeded apps kustomization missing the NP baseline:\n%s", seeded.content)
	}

	// Simulate the funnel cart install merging the customer's wordpress app
	// into the index (the read-modify-write the provisioning service does).
	merged := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-wordpress.yaml
  - ciliumnetworkpolicy.yaml
  - db-mysql.yaml
  - networkpolicy.yaml
`
	gs.files[kustKey] = fileEntry{sha: seeded.sha, content: []byte(merged)}

	// Re-reconcile. The org-controller must leave the funnel-merged index
	// untouched (seed-only).
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	after := string(gs.files[kustKey].content)
	if !strings.Contains(after, "app-wordpress.yaml") {
		t.Errorf("regression: org-controller CLOBBERED the funnel-merged apps kustomization — wordpress dropped:\n%s", after)
	}
	for _, want := range []string{"networkpolicy.yaml", "ciliumnetworkpolicy.yaml", "db-mysql.yaml"} {
		if !strings.Contains(after, want) {
			t.Errorf("apps kustomization lost %q after reconcile:\n%s", want, after)
		}
	}

	// Sanity: the Org still resolves (no error path taken).
	var got = sampleOrg()
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, got); err != nil {
		t.Fatalf("get org: %v", err)
	}
}
