package controller

import (
	"context"
	"strings"
	"testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"k8s.io/apimachinery/pkg/types"
)

// #4384 + #5104 — the org-controller's `vcluster/apps/kustomization.yaml` write
// is a RECONCILING MERGE: it must (a) NEVER clobber the funnel-merged app
// entries (#4384 — a baseline-only overwrite would drop the customer's apps
// from `kustomize build ./vcluster/apps` and the purchased app would never
// land), and (b) ALWAYS heal its own baseline entries back into the index
// (#5104 — the former seed-only skip left the #4992 vcluster target-ns
// `namespace.yaml` orphaned whenever the funnel committed the index first: the
// file was authored on every reconcile but never applied, and Flux wedged
// permanently on `namespaces "<slug>" not found`; 2/2 funnel Orgs on hw255).
func TestReconcile_AppsKustomization_MergesBaseline_NeverClobbers_5104(t *testing.T) {
	t.Parallel()
	org := sampleOrg() // plan m → vcluster tier → baseline = namespace.yaml + networkpolicy.yaml
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
	for _, want := range []string{"networkpolicy.yaml", "namespace.yaml"} {
		if !strings.Contains(string(seeded.content), want) {
			t.Fatalf("seeded apps kustomization missing baseline %q:\n%s", want, seeded.content)
		}
	}

	// Simulate the funnel cart install having committed ITS merged index FIRST
	// — the exact hw255 wedge shape: app docs + NP baseline present, the #4992
	// vcluster target-ns namespace.yaml MISSING (the funnel merge that produced
	// it did not know the doc). The CNP is NOT in this tree (#4475 §1).
	merged := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-umami.yaml
  - db-postgres.yaml
  - networkpolicy.yaml
`
	gs.files[kustKey] = fileEntry{sha: seeded.sha, content: []byte(merged)}

	// Re-reconcile. The org-controller must MERGE: preserve every funnel entry
	// AND heal namespace.yaml back into the index.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	after := string(gs.files[kustKey].content)
	for _, want := range []string{"app-umami.yaml", "db-postgres.yaml", "networkpolicy.yaml"} {
		if !strings.Contains(after, "- "+want) {
			t.Errorf("regression (#4384 class): org-controller dropped funnel entry %q from the apps kustomization:\n%s", want, after)
		}
	}
	if !strings.Contains(after, "- namespace.yaml") {
		t.Errorf("regression (#5104): org-controller failed to heal namespace.yaml into the funnel-merged apps index — the vcluster target-ns stays orphaned and Flux wedges on `namespaces \"<slug>\" not found`:\n%s", after)
	}

	// Third reconcile: the index is now complete — the controller must leave
	// it byte-untouched (write-free steady state, no commit churn).
	healed := gs.files[kustKey]
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}
	if got := string(gs.files[kustKey].content); got != string(healed.content) {
		t.Errorf("steady-state reconcile churned the apps kustomization:\n--- before ---\n%s\n--- after ---\n%s", healed.content, got)
	}

	// Sanity: the Org still resolves (no error path taken).
	var got = sampleOrg()
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, got); err != nil {
		t.Fatalf("get org: %v", err)
	}
}

// #4567 + #5104 — the merge-write must also self-heal a stale
// `ciliumnetworkpolicy.yaml` entry out of the apps index (the CNP file only
// ever exists in `vcluster/host-apps/`; a stale apps-index entry breaks the
// whole kustomize build) while still preserving the funnel entries.
func TestReconcile_AppsKustomization_StripsStaleCNPEntry_5104(t *testing.T) {
	t.Parallel()
	org := sampleOrg()
	r, gs, _ := makeReconciler(t, org)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	const kustKey = "acme/catalyst-tenant/vcluster/apps/kustomization.yaml"
	seeded := gs.files[kustKey]

	stale := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - app-wordpress.yaml
  - ciliumnetworkpolicy.yaml
  - networkpolicy.yaml
`
	gs.files[kustKey] = fileEntry{sha: seeded.sha, content: []byte(stale)}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	after := string(gs.files[kustKey].content)
	if strings.Contains(after, "- ciliumnetworkpolicy.yaml") {
		t.Errorf("stale CNP entry survived the merge — it lives in host-apps/, so this breaks the apps kustomize build (#4567):\n%s", after)
	}
	for _, want := range []string{"app-wordpress.yaml", "networkpolicy.yaml", "namespace.yaml"} {
		if !strings.Contains(after, "- "+want) {
			t.Errorf("merge lost %q while stripping the stale CNP entry:\n%s", want, after)
		}
	}
}
