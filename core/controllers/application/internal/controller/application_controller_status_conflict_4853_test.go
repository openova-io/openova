// Tests for the #4853 status-update conflict race in the application
// controller. The periodic re-list goroutine and the watch goroutine both
// call Reconcile -> updateStatus on the same Application; a bare
// Get-then-UpdateStatus loses a race on resourceVersion and logs
// `update status: Operation cannot be fulfilled ... the object has been
// modified` every ~30s. updateStatus now wraps the read-modify-write in
// retry.RetryOnConflict so the loser self-heals instead of erroring.
package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

// injectStatusConflicts prepends a reactor on the fake dynamic client that
// returns a 409 Conflict for the first `n` status-subresource updates, then
// passes through. Returns a pointer to the live counter of injected conflicts.
func injectStatusConflicts(t *testing.T, r *Reconciler, n int) *int {
	t.Helper()
	fake, ok := r.Dynamic.(*dynamicfake.FakeDynamicClient)
	if !ok {
		t.Fatalf("Dynamic is %T, want *dynamicfake.FakeDynamicClient", r.Dynamic)
	}
	fired := 0
	fake.PrependReactor("update", "applications", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil // only race the /status write
		}
		if fired < n {
			fired++
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps.openova.io", Resource: "applications"},
				"uat-hw228-pg",
				errors.New("the object has been modified; please apply your latest changes to the latest version and try again"),
			)
		}
		return false, nil, nil // pass through to the tracker
	})
	return &fired
}

// TestUpdateStatus_RetriesOnConflict is the #4853 guard: a single injected
// 409 Conflict on the status write must be transparently retried, and the
// caller must see a nil error with the intended status applied.
func TestUpdateStatus_RetriesOnConflict(t *testing.T) {
	app := makeApp("acme", "uat-hw228-pg", "acme-prod", "bp-postgres", "0.2.10", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, map[string]interface{}{})
	r := newReconciler(t, newFakeGitea(), app)
	fired := injectStatusConflicts(t, r, 1)

	err := r.updateStatus(context.Background(), app, statusUpdate{
		Phase:   PhaseReady,
		Ready:   "True",
		Reason:  "Healthy",
		Message: "all regions ready",
	})
	if err != nil {
		t.Fatalf("updateStatus returned error on a single retryable conflict: %v", err)
	}
	if *fired != 1 {
		t.Fatalf("expected exactly 1 injected conflict to be consumed, got %d", *fired)
	}

	got := readApp(t, r, "acme", "uat-hw228-pg")
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != PhaseReady {
		t.Errorf("status.phase = %q after conflict-heal, want %q", phase, PhaseReady)
	}
	// The Ready condition must reflect the intended write, proving the retry
	// re-applied the desired status (not a stale pre-conflict value).
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	var readyStatus string
	for _, c := range conds {
		if cm, ok := c.(map[string]interface{}); ok {
			if t, _ := cm["type"].(string); t == "Ready" {
				readyStatus, _ = cm["status"].(string)
			}
		}
	}
	if readyStatus != "True" {
		t.Errorf("Ready condition status = %q after conflict-heal, want %q", readyStatus, "True")
	}
}

// TestUpdateStatus_PersistentConflictSurfaces proves the retry does NOT
// silently swallow a conflict that never clears: once RetryOnConflict
// exhausts its budget the error must still surface (wrapped as
// "update status: ..."), so a genuine wedge stays LOUD rather than looking
// like a successful write.
func TestUpdateStatus_PersistentConflictSurfaces(t *testing.T) {
	app := makeApp("acme", "uat-hw228-pg", "acme-prod", "bp-postgres", "0.2.10", "single-region",
		[]string{"hetzner-fsn-rtz-prod"}, map[string]interface{}{})
	r := newReconciler(t, newFakeGitea(), app)
	// More than retry.DefaultRetry.Steps (5) so the budget is exhausted.
	injectStatusConflicts(t, r, 100)

	err := r.updateStatus(context.Background(), app, statusUpdate{
		Phase: PhaseReady,
		Ready: "True",
	})
	if err == nil {
		t.Fatalf("updateStatus swallowed a persistent conflict; want a surfaced error")
	}
	if !strings.Contains(err.Error(), "update status") {
		t.Errorf("error = %q, want it wrapped with %q", err.Error(), "update status")
	}
	if !apierrors.IsConflict(err) {
		t.Errorf("error should still be a recognisable Conflict; got %v", err)
	}
}
