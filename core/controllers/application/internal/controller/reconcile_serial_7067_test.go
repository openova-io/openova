package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestReconcileLatest_StaleSnapshotAfterDelete_7067 — a relist snapshot taken
// before the Application was deleted must not re-commit its region files once
// the delete path has removed them (hw307 runs 1794377153 / 1794386610 left
// both regions' HelmReleases declared in Git with no Application).
func TestReconcileLatest_StaleSnapshotAfterDelete_7067(t *testing.T) {
	bp := makeBlueprintWithActiveHotStandbyTopology(
		"bp-grafana", "1.0.6", "mgmt",
		[]string{"mgmt-A", "mgmt-B"},
	)
	env := makeMultiRegionEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "obs", "acme-prod", "bp-grafana", "1.0.6",
		"active-hotstandby",
		[]string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"},
		nil,
	)
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)
	reconcileAndApplyAll(t, r, fg, "acme", "obs")
	if n := regionFilesFor(fg, "acme", "obs"); n == 0 {
		t.Fatal("setup: no region files committed for obs")
	}

	// The relist's snapshot, taken before the delete.
	stale := readApp(t, r, "acme", "obs")

	cur := stale.DeepCopy()
	now := metav1.Now()
	cur.SetDeletionTimestamp(&now)
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").
		Update(context.Background(), cur, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("set deletion timestamp: %v", err)
	}
	reconcileFromCluster(t, r, "acme", "obs")
	if n := regionFilesFor(fg, "acme", "obs"); n != 0 {
		t.Fatalf("delete left %d region file(s)", n)
	}

	// The stale snapshot reaches the reconciler after the delete.
	if err := r.reconcileLatest(context.Background(), stale); err != nil {
		t.Fatalf("reconcileLatest: %v", err)
	}
	if n := regionFilesFor(fg, "acme", "obs"); n != 0 {
		t.Errorf("a stale pre-delete snapshot re-committed %d region file(s) after the delete — orphaned HelmReleases", n)
	}
}
