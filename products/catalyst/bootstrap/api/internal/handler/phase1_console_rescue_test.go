package handler

import (
	"fmt"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// mkConsoleRescueDep builds a Deployment in the console-unreachable-failed
// shape: status=failed, Phase1Outcome=OutcomeReady (Flux converged, only the
// #4706 external console probe failed), Phase1FinishedAt set, handover unfired.
// Callers override individual fields to exercise the negative gate branches.
func mkConsoleRescueDep(status, outcome string, finished, fired bool) *Deployment {
	r := &provisioner.Result{Phase1Outcome: outcome}
	if finished {
		now := time.Now().UTC()
		r.Phase1FinishedAt = &now
	}
	if fired {
		now := time.Now().UTC()
		r.HandoverFiredAt = &now
	}
	return &Deployment{
		ID:      "hw221consoleunreach",
		Status:  status,
		Request: provisioner.Request{SovereignFQDN: "hw221.omani.works"},
		Result:  r,
		Error:   "Phase 1 converged (all HelmReleases installed) but the console is NOT externally reachable",
	}
}

// TestShouldConsoleUnreachableRescue locks the #4746 residual-rescue gate:
// ONLY a failed + OutcomeReady + finished + unfired record with the production
// console gate wired qualifies. A timeout/hard-failure record (the converged-
// late rescue's job), an already-ready or already-handed-over record, an
// in-flight record, and a Handler with no console gate all fail the gate.
func TestShouldConsoleUnreachableRescue(t *testing.T) {
	probe := func(string) error { return nil }
	hWired := &Handler{log: silentLogger(), consoleProbe: probe}
	hNoGate := &Handler{log: silentLogger()} // consoleProbe == nil

	if !hWired.shouldConsoleUnreachableRescue(mkConsoleRescueDep("failed", helmwatch.OutcomeReady, true, false)) {
		t.Errorf("failed+OutcomeReady+finished+unfired with a wired console gate must qualify (the hw221 shape)")
	}
	if hWired.shouldConsoleUnreachableRescue(mkConsoleRescueDep("failed", helmwatch.OutcomeTimeout, true, false)) {
		t.Errorf("failed+timeout is the converged-late rescue's job, not this one")
	}
	if hWired.shouldConsoleUnreachableRescue(mkConsoleRescueDep("failed", helmwatch.OutcomeFailed, true, false)) {
		t.Errorf("hard failure must never console-rescue")
	}
	if hWired.shouldConsoleUnreachableRescue(mkConsoleRescueDep("failed", helmwatch.OutcomeReady, true, true)) {
		t.Errorf("already-fired handover must not re-rescue")
	}
	if hWired.shouldConsoleUnreachableRescue(mkConsoleRescueDep("ready", helmwatch.OutcomeReady, true, false)) {
		t.Errorf("already-ready records are not rescue candidates")
	}
	if hWired.shouldConsoleUnreachableRescue(mkConsoleRescueDep("failed", helmwatch.OutcomeReady, false, false)) {
		t.Errorf("Phase-1 not terminated (Phase1FinishedAt nil) must not rescue")
	}
	if hNoGate.shouldConsoleUnreachableRescue(mkConsoleRescueDep("failed", helmwatch.OutcomeReady, true, false)) {
		t.Errorf("a Handler with no wired console gate must never flip blind")
	}
	// Defensive: a nil Result must not panic and must not qualify.
	if hWired.shouldConsoleUnreachableRescue(&Deployment{ID: "x", Status: "failed"}) {
		t.Errorf("nil Result must not qualify")
	}
}

// TestRunConsoleUnreachableRescue_ProbeStillDown_LeavesFailed proves the
// conservative half: when the re-probe still fails, the record is left `failed`
// with its Error intact — the rescue never invents readiness.
func TestRunConsoleUnreachableRescue_ProbeStillDown_LeavesFailed(t *testing.T) {
	h := &Handler{
		log:          silentLogger(),
		consoleProbe: func(string) error { return fmt.Errorf("connection refused") },
	}
	dep := mkConsoleRescueDep("failed", helmwatch.OutcomeReady, true, false)
	wantErr := dep.Error

	h.runConsoleUnreachableRescue(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "failed" {
		t.Errorf("status must stay failed when the console is still unreachable; got %q", dep.Status)
	}
	if dep.Error != wantErr {
		t.Errorf("Error must be preserved on a still-down re-probe; got %q", dep.Error)
	}
	if dep.Result.HandoverFiredAt != nil {
		t.Errorf("handover must NOT fire when the console is still unreachable")
	}
}

// TestRunConsoleUnreachableRescue_ProbeUp_FlipsReady proves the heal: a positive
// re-probe flips the converged record ready, clears the stale #4706 Error, and
// stamps Phase1FinishedAt. Post-handover hooks are suppressed and the handover
// signer / store / jobs are unwired so the flip is asserted deterministically
// without standing up the real handover machinery.
func TestRunConsoleUnreachableRescue_ProbeUp_FlipsReady(t *testing.T) {
	h := &Handler{
		log:                       silentLogger(),
		consoleProbe:              func(string) error { return nil }, // console now answers
		suppressPostHandoverHooks: true,                             // no producer goroutines
		// store, handoverSigner, jobs all nil → fireHandover + sweep + persist no-op
	}
	dep := mkConsoleRescueDep("failed", helmwatch.OutcomeReady, false, false)

	h.runConsoleUnreachableRescue(dep)

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status != "ready" {
		t.Fatalf("status must flip to ready when the console now answers; got %q", dep.Status)
	}
	if dep.Error != "" {
		t.Errorf("stale #4706 Error must be cleared on the ready flip; got %q", dep.Error)
	}
	if dep.Result.Phase1FinishedAt == nil {
		t.Errorf("Phase1FinishedAt must be stamped on the ready flip")
	}
}
