package controller

// Tests for placement_canonicalize.go (#3969 — one stored shape).
//
// The subject is the STORED CR after a reconcile, read back from the fake
// apiserver — not the in-memory object the reconciler was handed — because
// the defect is what every other reader of the cluster sees.

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func storedPlacement(t *testing.T, r *Reconciler, ns, name string) interface{} {
	t.Helper()
	got := readApp(t, r, ns, name)
	raw, _, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", "placement")
	return raw
}

// A legacy string on a normal (fan-out) Application is rewritten to {mode}
// on its first reconcile, and the reconcile still runs to completion on the
// migrated shape (the status it writes is the one the legacy CR would have
// got — the posture did not change, only its spelling).
func TestCanonicalizePlacement_LegacyStringBecomesObject(t *testing.T) {
	app := makeApp("acme", "site", "missing-env", "bp-x", "1.0.0", "active-hotstandby", []string{"hz-fsn-rtz-prod", "hz-hel-rtz-prod"}, nil)
	// VACUITY — the seed must really be the legacy string.
	if _, isStr, _ := unstructured.NestedString(app.Object, "spec", "placement"); !isStr {
		t.Fatal("fixture must seed a STRING spec.placement")
	}
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	reconcileFromCluster(t, r, "acme", "site")

	got := storedPlacement(t, r, "acme", "site")
	want := map[string]interface{}{"mode": "active-hot-standby"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored spec.placement = %#v, want %#v — the legacy string must be migrated to the ONE shape, with the legacy spelling folded", got, want)
	}
	// The reconcile continued past the migration: the missing Environment is
	// still reported, exactly as it would be for a canonical CR.
	phase, reason, _ := readPhaseAndReason(t, readApp(t, r, "acme", "site"))
	if phase != PhasePending || reason != ReasonEnvironmentMissing {
		t.Fatalf("after migration phase/reason = %q/%q, want Pending/%s — the migration must not short-circuit the reconcile", phase, reason, ReasonEnvironmentMissing)
	}
}

// Bootstrap-owned (chart-templated) CRs take the adoption path, which never
// touches spec — so the migration has to sit ABOVE that guard. This is the
// shape every `keycloak` / `shared-pg` / `gitea` CR on hw307 has.
func TestCanonicalizePlacement_BootstrapOwnedCRIsMigratedToo(t *testing.T) {
	app := makeBootstrapOwnedApp("shared-data", "shared-pg", "bp-postgres-shared", "flux-system")
	hr := makeSlotHR("flux-system", "bp-postgres-shared", "True", "Helm install succeeded")
	fg := newFakeGitea()
	r := newReconciler(t, fg, app, hr)

	reconcileFromCluster(t, r, "shared-data", "shared-pg")

	got := storedPlacement(t, r, "shared-data", "shared-pg")
	want := map[string]interface{}{"mode": "singleton"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stored spec.placement = %#v, want %#v", got, want)
	}
	// Adoption still happened on the same pass.
	phase, reason, _ := readPhaseAndReason(t, readApp(t, r, "shared-data", "shared-pg"))
	if phase != PhaseReady || reason != ReasonBootstrapAdopted {
		t.Fatalf("phase/reason = %q/%q, want Ready/%s", phase, reason, ReasonBootstrapAdopted)
	}
	// And NO HelmRelease was rendered — the adoption invariant is untouched.
	if n := countHelmReleases(t, r); n != 1 {
		t.Fatalf("HelmReleases = %d, want 1 (the slot HR only)", n)
	}
}

// One-shot: a CR already on the canonical shape is not written again. The
// probe is resourceVersion on the fake apiserver — a second migration pass
// that re-patched an identical value would still bump it.
func TestCanonicalizePlacement_IsOneShot(t *testing.T) {
	app := makeApp("acme", "site", "missing-env", "bp-x", "1.0.0", "singleton", []string{"hz-fsn-rtz-prod"}, nil)
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)
	ctx := context.Background()

	reconcileFromCluster(t, r, "acme", "site")
	first := readApp(t, r, "acme", "site")
	if _, isObj, _ := unstructured.NestedMap(first.Object, "spec", "placement"); !isObj {
		t.Fatal("first reconcile must migrate")
	}

	// Second pass over the migrated CR: the placement patch must not fire.
	migrated, err := r.canonicalizePlacement(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Fatal("a canonical CR was reported as migrated again — the migration is not one-shot")
	}
	second, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Get(ctx, "site", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if second.GetResourceVersion() != first.GetResourceVersion() {
		t.Fatalf("resourceVersion moved %s → %s on a no-op pass — something still writes", first.GetResourceVersion(), second.GetResourceVersion())
	}
}

// An object that already carries the #3969 model is left byte-identical —
// the migration must never drop `targets` / `clusters` / `vcluster` while
// folding a legacy `mode` spelling, and must not write at all when the mode
// is already canonical.
func TestCanonicalizePlacement_ObjectKeysSurvive(t *testing.T) {
	placement := map[string]interface{}{
		"mode":     "active-hotstandby", // legacy spelling inside the object
		"vcluster": "rtz",
		"clusters": []interface{}{"mgmt-A", "mgmt-B"},
		"targets": []interface{}{
			map[string]interface{}{"region": "a", "cluster": "mgmt-A", "role": "Primary"},
			map[string]interface{}{"region": "b", "cluster": "mgmt-B", "role": "Standby", "standbyType": "Hot"},
		},
	}
	app := makeAppWithPlacement("acme", "site", "missing-env", "bp-x", "1.0.0", placement, []string{"a", "b"})
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	reconcileFromCluster(t, r, "acme", "site")

	got, _ := storedPlacement(t, r, "acme", "site").(map[string]interface{})
	if got["mode"] != "active-hot-standby" {
		t.Fatalf("mode = %v, want the canonical spelling", got["mode"])
	}
	for _, k := range []string{"vcluster", "clusters", "targets"} {
		if !reflect.DeepEqual(got[k], placement[k]) {
			t.Fatalf("%s = %#v, want %#v carried verbatim", k, got[k], placement[k])
		}
	}
}

// An absent placement is NOT invented. The Blueprint-default derivation owns
// that case and must keep seeing an absent field.
func TestCanonicalizePlacement_AbsentStaysAbsent(t *testing.T) {
	app := makeApp("acme", "site", "missing-env", "bp-x", "1.0.0", "", []string{"hz-fsn-rtz-prod"}, nil)
	unstructured.RemoveNestedField(app.Object, "spec", "placement")
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	migrated, err := r.canonicalizePlacement(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Fatal("an absent placement was 'migrated' — the migration invented a posture")
	}
	if raw, found, _ := unstructured.NestedFieldNoCopy(readApp(t, r, "acme", "site").Object, "spec", "placement"); found {
		t.Fatalf("spec.placement = %#v, want absent", raw)
	}
}

// A malformed value is a reconcile error, never migrated over.
func TestCanonicalizePlacement_MalformedIsAnError(t *testing.T) {
	app := makeApp("acme", "site", "missing-env", "bp-x", "1.0.0", "singleton", []string{"hz-fsn-rtz-prod"}, nil)
	_ = unstructured.SetNestedField(app.Object, []interface{}{"singleton"}, "spec", "placement")
	fg := newFakeGitea()
	r := newReconciler(t, fg, app)

	if _, err := r.canonicalizePlacement(context.Background(), app); err == nil {
		t.Fatal("a list-valued spec.placement must be refused, not rewritten")
	}
}
