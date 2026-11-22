package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestReconcile_DeletionStoppedHalfway_SaysWhyOnTheObject — a delete whose
// Gitea calls fail must keep the finalizer AND write the failure on the
// Application. Before, handleDeletion wrote the fixed "removing manifests from
// Gitea; waiting for Flux to drain" and returned the error to a caller that
// only logged it: every 30 s relist repeated the same pass and the object
// read "waiting for Flux to drain" for ever, with the cause only in the
// controller log.
func TestReconcile_DeletionStoppedHalfway_SaysWhyOnTheObject(t *testing.T) {
	bp := makeBlueprint("bp-wp", "1.0.0", nil, []string{"single-region"})
	env := makeEnv("acme-prod", "acme", "prod")
	org := makeOrg("acme")
	app := makeApp("acme", "site", "acme-prod", "bp-wp", "1.0.0", "single-region",
		[]string{"hetzner-fsn-rtz-prod"},
		map[string]interface{}{"replicas": int64(1)})
	fg := newFakeGitea()
	fg.orgsExist["acme"] = true
	r := newReconciler(t, fg, app, env, org, bp)
	ctx := context.Background()

	reconcileFromCluster(t, r, "acme", "site")
	got := readApp(t, r, "acme", "site")
	if !hasFinalizer(got, FinalizerName) {
		t.Fatal("first pass should add the finalizer")
	}
	now := metav1.Now()
	got.SetDeletionTimestamp(&now)
	if _, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Update(ctx, got, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("set deletion timestamp: %v", err)
	}

	fg.failDeleteErr = pseudoErr("gitea: 401 Unauthorized (token revoked)")
	for pass := 1; pass <= 2; pass++ {
		cur := readApp(t, r, "acme", "site")
		if err := r.Reconcile(ctx, cur); err == nil {
			t.Fatalf("pass %d: a delete whose Gitea calls fail must return the error", pass)
		}
		after := readApp(t, r, "acme", "site")
		if !hasFinalizer(after, FinalizerName) {
			t.Fatalf("pass %d: the finalizer was released although the region files are still in Git", pass)
		}
		phase, reason, message := readPhaseAndReason(t, after)
		if phase != PhaseUninstalling || reason != ReasonUninstallBlocked {
			t.Fatalf("pass %d: phase/reason = %s/%s, want %s/%s", pass, phase, reason, PhaseUninstalling, ReasonUninstallBlocked)
		}
		if !strings.Contains(message, "401 Unauthorized") {
			t.Fatalf("pass %d: the Ready message does not carry the Gitea error: %q", pass, message)
		}
	}

	// Gitea recovers: the next pass gets through and releases the object.
	fg.failDeleteErr = nil
	reconcileFromCluster(t, r, "acme", "site")
	if _, ok := fg.get("acme", "catalyst-tenant", "main", "regions/hetzner-fsn-rtz-prod/apps/site.yaml"); ok {
		t.Error("the region file should be deleted once Gitea answers")
	}
	if after, err := r.Dynamic.Resource(ApplicationGVR).Namespace("acme").Get(ctx, "site", metav1.GetOptions{}); err == nil && hasFinalizer(after, FinalizerName) {
		t.Errorf("finalizer still present after a pass that got through: %v", after.GetFinalizers())
	}
}
