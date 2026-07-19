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
	// #5253 — the pre-fix console-downgrade signature (hw276): the primary
	// converged (OutcomeReady) but the #4706 console gate latched the record
	// failed. Such PERSISTED records must qualify so a catalyst-api restart
	// fires their producer chain; an already-fired one must not re-fire.
	if !h.shouldConvergedLateRescue(mk("failed", helmwatch.OutcomeReady, false)) {
		t.Errorf("failed+OutcomeReady+unfired must qualify (#5253, the hw276 pre-fix console-downgrade shape)")
	}
	if h.shouldConvergedLateRescue(mk("failed", helmwatch.OutcomeReady, true)) {
		t.Errorf("already-fired handover must not re-fire for the #5253 shape")
	}
}

// TestRunConvergedLateRescue_ConsoleDowngradeRecord proves the #5253 rescue
// end-to-end for a pre-fix persisted record (failed + Phase1Outcome=="ready" +
// the #4706 console error on dep.Error): once the live census confirms the
// converged primary, the record flips ready and the stale console error is
// re-homed onto the non-fatal ConsoleDegraded surface (the rescued record must
// not read ready-with-FailureCard).
func TestRunConvergedLateRescue_ConsoleDowngradeRecord(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{log: silentLogger(), kubeconfigsDir: dir}
	const depID = "hw276rescue"
	if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	origCensus := censusHelmReleases
	censusHelmReleases = func(kubeconfigPath string) (int, int, error) { return 66, 66, nil }
	defer func() { censusHelmReleases = origCensus }()

	consoleErr := "Phase 1 converged (all HelmReleases installed) but the console is NOT externally reachable: HTTP 404"
	dep := &Deployment{
		ID:     depID,
		Status: "failed",
		Error:  consoleErr,
		Request: provisioner.Request{
			Regions: []provisioner.RegionSpec{{Provider: "huawei"}, {Provider: "huawei"}},
		},
		Result: &provisioner.Result{Phase1Outcome: helmwatch.OutcomeReady},
	}

	h.runConvergedLateRescue(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "ready" {
		t.Fatalf("census-confirmed console-downgrade record must flip ready, got %q", dep.Status)
	}
	if dep.Error != "" {
		t.Fatalf("rescued record must not keep the #4706 error on dep.Error (FailureCard), got %q", dep.Error)
	}
	if !dep.Result.ConsoleDegraded || dep.Result.ConsoleDegradedDetail != consoleErr {
		t.Fatalf("console signal must be re-homed onto ConsoleDegraded/Detail, got %v / %q",
			dep.Result.ConsoleDegraded, dep.Result.ConsoleDegradedDetail)
	}
	if dep.Result.Phase1FinishedAt == nil {
		t.Fatalf("rescue must stamp Phase1FinishedAt")
	}
}
