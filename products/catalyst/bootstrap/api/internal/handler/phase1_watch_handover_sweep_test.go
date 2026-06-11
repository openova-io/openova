// phase1_watch_handover_sweep_test.go — issue #3277: at a SUCCESSFUL
// Phase-1 handover, markPhase1Done sweeps every still-reconciling bp-*
// install Job row terminal so the mother's bootstrap view never shows a
// row "stuck running" after the Sovereign self-manages it. A FAILED
// Phase-1 must NOT sweep — its failure stays visible.
package handler

import (
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// seedRunningInstallRow wires a jobs.Bridge onto dep (backed by h.jobs)
// and records one install-<chart> row stuck in the running state — the
// frozen "running forever" row #3277 describes.
func seedRunningInstallRow(t *testing.T, h *Handler, dep *Deployment, chart string) {
	t.Helper()
	bridge := jobs.NewBridge(h.jobs, dep.ID)
	dep.jobsBridge = bridge
	if err := bridge.OnHelmReleaseEvent(chart, jobs.HelmStateInstalling, "info", "Helm install in progress", time.Now().UTC(), nil); err != nil {
		t.Fatalf("seed running install row: %v", err)
	}
	got, err := h.jobs.ListJobs(dep.ID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, j := range got {
		if j.JobName == jobs.JobNamePrefix+chart && j.Status != jobs.StatusRunning {
			t.Fatalf("pre-condition: install-%s = %q, want %q", chart, j.Status, jobs.StatusRunning)
		}
	}
}

func installRowStatus(t *testing.T, h *Handler, depID, chart string) string {
	t.Helper()
	got, err := h.jobs.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, j := range got {
		if j.JobName == jobs.JobNamePrefix+chart {
			return j.Status
		}
	}
	t.Fatalf("install-%s row not found in %+v", chart, got)
	return ""
}

// TestMarkPhase1Done_SweepsStaleRunningRowsOnReady — the core #3277
// assertion at the handler level: a successful handover (OutcomeReady)
// stamps a still-running install row terminal so it no longer reads
// "running" on the mother.
func TestMarkPhase1Done_SweepsStaleRunningRowsOnReady(t *testing.T) {
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h := NewWithJobsStore(silentLogger(), st)

	dep := &Deployment{
		ID:        "phase1-sweep-ready",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-sweep.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-sweep.example.com",
		},
		OwnerEmail: "operator@sweep.example.com",
	}
	h.deployments.Store(dep.ID, dep)
	seedRunningInstallRow(t, h, dep, "gitea")

	finalStates := map[string]string{
		"cilium": helmwatch.StateInstalled,
		"gitea":  helmwatch.StateInstalling, // still converging at handover
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	if got := installRowStatus(t, h, dep.ID, "gitea"); got != jobs.StatusSucceeded {
		t.Fatalf("post-handover install-gitea = %q, want %q (#3277 sweep)", got, jobs.StatusSucceeded)
	}
}

// TestMarkPhase1Done_PreservesRunningRowsOnFailure — a FAILED Phase-1
// must never be masked. The sweep is gated on OutcomeReady, so a
// running install row on a failed deployment stays running (the watch
// failed; the mother keeps its honest state) — it is NOT stamped
// succeeded.
func TestMarkPhase1Done_PreservesRunningRowsOnFailure(t *testing.T) {
	st, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h := NewWithJobsStore(silentLogger(), st)

	dep := &Deployment{
		ID:        "phase1-sweep-failed",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-failsweep.example.com",
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-failsweep.example.com",
		},
		OwnerEmail: "operator@failsweep.example.com",
	}
	h.deployments.Store(dep.ID, dep)
	seedRunningInstallRow(t, h, dep, "gitea")

	finalStates := map[string]string{
		"cilium":            helmwatch.StateInstalled,
		"catalyst-platform": helmwatch.StateFailed,
		"gitea":             helmwatch.StateInstalling,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeFailed)

	if dep.Status != "failed" {
		t.Fatalf("Status = %q, want %q", dep.Status, "failed")
	}
	// The running row is NOT swept on a failed Phase-1 — the mother keeps
	// its honest, non-handed-over state. (deriveTreeView may propagate
	// the catalyst-platform failure to dependents, but gitea has no dep
	// on it here, so it stays running — never fabricated to succeeded.)
	if got := installRowStatus(t, h, dep.ID, "gitea"); got == jobs.StatusSucceeded {
		t.Fatalf("install-gitea = %q on FAILED Phase-1 — sweep must not run on failure", got)
	}
}
