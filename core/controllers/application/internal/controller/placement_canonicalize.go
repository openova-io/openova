package controller

// placement_canonicalize.go — #3969: ONE stored shape for
// `Application.spec.placement`, enforced at the only place that sees every
// Application CR on every Sovereign — the controller's reconcile entry.
//
// # The gap this closes
//
// Measured on hw292 (2026-08-08, 17 Applications: 16 bare strings + 1 object)
// and again on hw307 (2026-11-06, 11 Applications: 11 bare strings), the field
// the EPIC names as the canonical desired state (`placement.targets[]`) could
// not even be written on most CRs without first changing the field's TYPE.
// Every reader type-switched, and the one that did not got a silent wrong
// answer. The EPIC's last comment asked for the shape to be settled and the
// stored CRs migrated "mechanically, with no manual step".
//
// The shape is settled in pkg/apis/blueprint/v1alpha1/placement_spec.go: the
// object is the contract; the bare string is a legacy spelling accepted on
// READ only. Every catalyst-api writer now emits the object. This file is the
// migration for what is already stored.
//
// # How the migration runs
//
// canonicalizePlacement runs at the top of Reconcile, before the
// bootstrap-owned adoption guard, so chart-templated bootstrap CRs
// (`keycloak`, `shared-pg`, …) and producer-written ones (`platform-*`,
// customer installs) all go through it. When `spec.placement` decodes as the
// legacy string (or an object whose `mode` carries a legacy spelling) it
// JSON-merge-patches the single field to the canonical object and updates the
// in-memory CR so the SAME reconcile continues with the canonical shape — no
// extra event round-trip, no second pass that could race the first.
//
// It is one-shot by construction: once the field is an object with a canonical
// `mode`, CanonicalPlacementValue reports changed=false and nothing is written.
//
// # Helm-owned CRs
//
// The chart-templated bootstrap CRs are rendered by a Flux HelmRelease. Helm
// patches unstructured objects with a TWO-way JSON merge patch between the
// previous and the next rendered manifest — the live object is not consulted —
// so a template that still renders `placement: singleton` on both sides of an
// upgrade produces no patch for the field and the migrated object survives.
// drift detection is off on every bootstrap-kit HelmRelease (verified on
// hw307: `spec.driftDetection` unset on bp-keycloak / bp-gitea / bp-harbor /
// bp-openbao / bp-postgres-shared). When a chart template is later changed to
// render the object it writes the same value this migration wrote.
//
// # What it never does
//
// It never INVENTS a placement: an absent `spec.placement` stays absent (the
// Blueprint-default derivation in Reconcile step 4.5 still owns that case). It
// never touches a CR being deleted. It never changes the posture — the mode
// fold is the same Canonicalize the resolver has always applied on read. A
// malformed value is reported as a reconcile error, not migrated over.

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// canonicalizePlacement rewrites a legacy-shaped `spec.placement` to the ONE
// stored shape and mirrors the rewrite onto the in-memory object. Returns
// (migrated, error). A NotFound on the patch means the CR went away between
// the watch event and now — not an error, and nothing to continue with.
func (r *Reconciler) canonicalizePlacement(ctx context.Context, app *unstructured.Unstructured) (bool, error) {
	raw, found, err := unstructured.NestedFieldNoCopy(app.Object, "spec", "placement")
	if err != nil || !found {
		return false, nil
	}
	canonical, changed, derr := bpv1alpha1.CanonicalPlacementValue(raw)
	if derr != nil {
		return false, fmt.Errorf("spec.placement: %w", derr)
	}
	if !changed {
		return false, nil
	}

	patch, err := json.Marshal(map[string]interface{}{
		"spec": map[string]interface{}{"placement": canonical},
	})
	if err != nil {
		return false, fmt.Errorf("marshal placement patch: %w", err)
	}
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace(app.GetNamespace()).
		Patch(ctx, app.GetName(), types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("patch spec.placement to the canonical object: %w", err)
	}

	// Continue THIS reconcile with the canonical shape: parseSpec reads the
	// in-memory object, and every status writer re-fetches before it writes.
	if err := unstructured.SetNestedField(app.Object, canonical, "spec", "placement"); err != nil {
		return true, fmt.Errorf("mirror canonical placement onto the reconcile object: %w", err)
	}
	r.Log.Info("spec.placement migrated to the canonical object (#3969)",
		"app", app.GetName(),
		"namespace", app.GetNamespace(),
		"legacy", fmt.Sprintf("%v", raw),
		"mode", canonical["mode"])
	return true, nil
}
