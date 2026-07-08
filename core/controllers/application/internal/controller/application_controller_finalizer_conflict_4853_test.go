// Tests for the #4853 FINALIZER-removal conflict loop — the original wedge
// (uat-openclaw stuck phase=Uninstalling for 32h with all resources gone).
// removeFinalizer / ensureFinalizer did a bare Update on the stale reconcile
// object; a persistent concurrent writer 409'd every attempt so the finalizer
// never came off and the reconcile looped forever. #4856 fixed only
// updateStatus; these guard the finalizer paths, which now wrap the
// read-modify-write in retry.RetryOnConflict with a re-GET.
package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// injectFinalizerUpdateConflicts prepends a reactor returning a 409 Conflict
// for the first `n` MAIN-resource (non-status) updates on applications — i.e.
// the finalizer write — then passes through.
func injectFinalizerUpdateConflicts(t *testing.T, r *Reconciler, n int) *int {
	t.Helper()
	fake, ok := r.Dynamic.(*dynamicfake.FakeDynamicClient)
	if !ok {
		t.Fatalf("Dynamic is %T, want *dynamicfake.FakeDynamicClient", r.Dynamic)
	}
	fired := 0
	fake.PrependReactor("update", "applications", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "" {
			return false, nil, nil // only race the main-resource (finalizer) write
		}
		if fired < n {
			fired++
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps.openova.io", Resource: "applications"},
				"uat-openclaw",
				errors.New("the object has been modified; please apply your changes to the latest version and try again"),
			)
		}
		return false, nil, nil
	})
	return &fired
}

// TestRemoveFinalizer_RetriesOnConflict is the #4853 original-symptom guard:
// a single injected 409 on the finalizer strip must be transparently retried
// and the finalizer must actually come off — not loop forever (32h wedge).
func TestRemoveFinalizer_RetriesOnConflict(t *testing.T) {
	app := makeApp("acme", "uat-openclaw", "acme-prod", "bp-openclaw", "0.2.16", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, map[string]interface{}{})
	app.SetFinalizers([]string{FinalizerName})
	r := newReconciler(t, newFakeGitea(), app)
	fired := injectFinalizerUpdateConflicts(t, r, 1)

	if err := r.removeFinalizer(context.Background(), app); err != nil {
		t.Fatalf("removeFinalizer errored on a single retryable conflict: %v", err)
	}
	if *fired != 1 {
		t.Fatalf("expected exactly 1 injected conflict consumed, got %d", *fired)
	}
	got := readApp(t, r, "acme", "uat-openclaw")
	if hasFinalizer(got, FinalizerName) {
		t.Fatalf("finalizer %q still present after removeFinalizer — the CR would never delete (the 32h wedge)", FinalizerName)
	}
}

// TestRemoveFinalizer_PersistentConflictSurfaces proves the retry does not
// silently swallow a conflict that never clears: once the budget is exhausted
// the error surfaces (wrapped "remove finalizer: ...") and stays a Conflict,
// so a genuine wedge is LOUD instead of looking successful.
func TestRemoveFinalizer_PersistentConflictSurfaces(t *testing.T) {
	app := makeApp("acme", "uat-openclaw", "acme-prod", "bp-openclaw", "0.2.16", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, map[string]interface{}{})
	app.SetFinalizers([]string{FinalizerName})
	r := newReconciler(t, newFakeGitea(), app)
	injectFinalizerUpdateConflicts(t, r, 100) // > retry.DefaultRetry.Steps

	err := r.removeFinalizer(context.Background(), app)
	if err == nil {
		t.Fatalf("removeFinalizer swallowed a persistent conflict; want a surfaced error")
	}
	if !strings.Contains(err.Error(), "remove finalizer") {
		t.Errorf("error = %q, want it wrapped with %q", err.Error(), "remove finalizer")
	}
	if !apierrors.IsConflict(err) {
		t.Errorf("error should still be a recognisable Conflict; got %v", err)
	}
}

// TestEnsureFinalizer_RetriesOnConflict guards the add path with the same
// contract — a single 409 heals and the finalizer lands.
func TestEnsureFinalizer_RetriesOnConflict(t *testing.T) {
	app := makeApp("acme", "uat-openclaw", "acme-prod", "bp-openclaw", "0.2.16", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, map[string]interface{}{})
	r := newReconciler(t, newFakeGitea(), app)
	fired := injectFinalizerUpdateConflicts(t, r, 1)

	if err := r.ensureFinalizer(context.Background(), app); err != nil {
		t.Fatalf("ensureFinalizer errored on a single retryable conflict: %v", err)
	}
	if *fired != 1 {
		t.Fatalf("expected exactly 1 injected conflict consumed, got %d", *fired)
	}
	got := readApp(t, r, "acme", "uat-openclaw")
	if !hasFinalizer(got, FinalizerName) {
		t.Fatalf("finalizer %q not present after ensureFinalizer heal", FinalizerName)
	}
}
