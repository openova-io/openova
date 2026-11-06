package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// hw307 2026-11-06: after a switchover ran, clearSwitchoverRequest wrote back
// the Continuum copy read at the START of the reconcile. The run had patched
// the status meanwhile, so the full Update hit a resourceVersion conflict, the
// error was discarded, requested stayed true, and every following reconcile
// re-ran the switchover (re-demoting region A). Clearing must succeed from a
// stale copy.
func TestClearSwitchoverRequest_SucceedsFromStaleCopy(t *testing.T) {
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	_ = unstructured.SetNestedField(cr.Object, true, "spec", "switchover", "requested")
	r, _, _ := newReconciler(t, cr)
	ctx := context.Background()

	stale, err := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(ctx, "cr1", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The switchover patches the CR meanwhile -> the stored resourceVersion moves on.
	fresh := stale.DeepCopy()
	_ = unstructured.SetNestedField(fresh.Object, "SwitchingOver", "status", "phase")
	if _, err := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Update(ctx, fresh, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := r.clearSwitchoverRequest(ctx, stale); err != nil {
		t.Fatalf("clearSwitchoverRequest from a stale copy: %v", err)
	}
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(ctx, "cr1", metav1.GetOptions{})
	if v, _, _ := unstructured.NestedBool(got.Object, "spec", "switchover", "requested"); v {
		t.Fatal("spec.switchover.requested is still true — the next reconcile would re-run the switchover")
	}
	if p, _, _ := unstructured.NestedString(got.Object, "status", "phase"); p != "SwitchingOver" {
		t.Fatalf("clearing the request clobbered the status (phase=%q)", p)
	}
}
