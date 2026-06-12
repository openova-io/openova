package handler

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// TestShouldConvergedLateRescue locks the #3319 gates: only a
// failed+TIMEOUT record with no fired handover and a resolvable
// kubeconfig qualifies — hard failures and already-handed-over
// records never rescue.
func TestShouldConvergedLateRescue(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw130lateconv"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	nowv := time.Now().UTC()
	now := &nowv
	mk := func(status, outcome string, fired bool) *Deployment {
		r := &provisioner.Result{Phase1Outcome: outcome}
		if fired {
			r.HandoverFiredAt = now
		}
		return &Deployment{ID: depID, Status: status,
			Request: provisioner.Request{Regions: []provisioner.RegionSpec{{Provider: "huawei"}}},
			Result:  r}
	}
	if !h.shouldConvergedLateRescue(mk("failed", helmwatch.OutcomeTimeout, false)) {
		t.Errorf("failed+timeout+unfired must qualify (the hw130 shape)")
	}
	if h.shouldConvergedLateRescue(mk("failed", helmwatch.OutcomeFailed, false)) {
		t.Errorf("hard failure must never rescue")
	}
	if h.shouldConvergedLateRescue(mk("failed", helmwatch.OutcomeTimeout, true)) {
		t.Errorf("already-fired handover must not re-fire")
	}
	if h.shouldConvergedLateRescue(mk("ready", helmwatch.OutcomeReady, false)) {
		t.Errorf("ready records are not rescue candidates")
	}
}
