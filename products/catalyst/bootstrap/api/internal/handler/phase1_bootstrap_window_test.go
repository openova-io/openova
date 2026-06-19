// phase1_bootstrap_window_test.go — proves runPhase1Watch fills the
// ~30-minute "Bootstrapping cluster" window (between "Provision <provider>:
// Success" and the first bp-* HelmRelease) with a live group + step children
// on the Jobs timeline, instead of a static Success with a silent void.
//
// The provisioning-observability gap: the Phase-0 "Provision <provider>"
// lifecycle group flips Success the moment `tofu apply` returns, then nothing
// streams to the timeline for up to half an hour while cloud-init → k3s →
// kubeconfig-PUT → Flux-install runs cluster-side. These tests assert the new
// GroupClusterConverge group surfaces continuous motion across that window.
package handler

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// handlerWithJobsStore builds a NewWithPDM Handler and attaches a real
// temp-dir jobs Store so the bootstrap-window bridge writes are observable
// (NewWithPDM alone leaves h.jobs nil, which the bridge no-ops against).
func handlerWithJobsStore(t *testing.T) *Handler {
	t.Helper()
	h := NewWithPDM(silentLogger(), &fakePDM{})
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("jobs.NewStore: %v", err)
	}
	h.jobs = js
	return h
}

func findJobByName(t *testing.T, in []jobs.Job, name string) jobs.Job {
	t.Helper()
	for _, j := range in {
		if j.JobName == name {
			return j
		}
	}
	t.Fatalf("job %q not found among %d rows", name, len(in))
	return jobs.Job{}
}

// TestBootstrapWindow_GroupRendersDuringPhase1Watching is the core acceptance
// test for the gap: while the deployment is still "phase1-watching" (the
// kubeconfig has not yet arrived — the exact window that used to be silent),
// the "Bootstrapping cluster" group exists and rolls up to "running" with its
// nodes-booting step in flight, so the operator sees motion the whole time.
func TestBootstrapWindow_GroupRendersDuringPhase1Watching(t *testing.T) {
	h := handlerWithJobsStore(t)
	// No kubeconfig on disk → runPhase1Watch sits in waitForKubeconfig (the
	// real void). We don't run runPhase1Watch here (it would block on the
	// wait); we drive the same seed+start the watch performs at entry and
	// assert the timeline shows the live group.
	dep := makeDeploymentWithKubeconfig(t, h, "bootstrap-window-live", "")
	if dep.Status != "phase1-watching" {
		t.Fatalf("precondition: dep.Status = %q, want phase1-watching", dep.Status)
	}

	bb := h.bootstrapBridgeFor(dep)
	if bb == nil {
		t.Fatalf("bootstrapBridgeFor returned nil with a jobs store wired")
	}
	if err := bb.Seed(); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := bb.StartStep(jobs.BootstrapStepNodesBooting, "Nodes booting", time.Now().UTC()); err != nil {
		t.Fatalf("StartStep: %v", err)
	}
	// A heartbeat (what the kubeconfig-wait loop fires each tick) keeps motion
	// flowing without terminating the step.
	if err := bb.Heartbeat(jobs.BootstrapStepNodesBooting, "cloud-init: running modules:final", time.Now().UTC()); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	got, err := h.jobs.ListJobs(dep.ID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	group := findJobByName(t, got, jobs.GroupClusterConverge)
	if group.DisplayName != jobs.GroupClusterConvergeDisplay {
		t.Errorf("group DisplayName = %q, want %q", group.DisplayName, jobs.GroupClusterConvergeDisplay)
	}
	if group.Status != jobs.StatusRunning {
		t.Errorf("group Status = %q, want running during the phase1-watching window", group.Status)
	}
	if len(group.ChildIDs) != 3 {
		t.Errorf("group ChildIDs = %d, want 3 step children", len(group.ChildIDs))
	}
	nodes := findJobByName(t, got, jobs.ActivityStepJobName(jobs.GroupClusterConverge, jobs.BootstrapStepNodesBooting))
	if nodes.Status != jobs.StatusRunning {
		t.Errorf("nodes-booting Status = %q, want running", nodes.Status)
	}
}

// TestBootstrapWindow_FullWatchConvergesGroup runs the full runPhase1Watch
// against three already-ready HRs and asserts the bootstrap window closes
// cleanly: all three steps succeed, the flux step carries the live "HR X/Y
// ready" counter, and the group rolls up succeeded — the bootstrap-kit install
// rows then own the timeline.
func TestBootstrapWindow_FullWatchConvergesGroup(t *testing.T) {
	h := handlerWithJobsStore(t)
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
		makeReadyHR("bp-cert-manager"),
		makeReadyHR("bp-flux"),
	)
	h.phase1WatchTimeout = 5 * time.Second
	h.bootstrapFluxProgressInterval = 10 * time.Millisecond

	dep := makeDeploymentWithKubeconfig(t, h, "bootstrap-window-converge", "fake-kubeconfig: yaml")
	h.runPhase1Watch(dep)

	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want ready", dep.Status)
	}

	got, err := h.jobs.ListJobs(dep.ID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	group := findJobByName(t, got, jobs.GroupClusterConverge)
	if group.Status != jobs.StatusSucceeded {
		t.Errorf("group Status = %q, want succeeded after convergence", group.Status)
	}

	nodes := findJobByName(t, got, jobs.ActivityStepJobName(jobs.GroupClusterConverge, jobs.BootstrapStepNodesBooting))
	kube := findJobByName(t, got, jobs.ActivityStepJobName(jobs.GroupClusterConverge, jobs.BootstrapStepKubeconfig))
	flux := findJobByName(t, got, jobs.ActivityStepJobName(jobs.GroupClusterConverge, jobs.BootstrapStepFluxInstalling))

	if nodes.Status != jobs.StatusSucceeded {
		t.Errorf("nodes Status = %q, want succeeded", nodes.Status)
	}
	if kube.Status != jobs.StatusSucceeded {
		t.Errorf("kube Status = %q, want succeeded", kube.Status)
	}
	if flux.Status != jobs.StatusSucceeded {
		t.Errorf("flux Status = %q, want succeeded", flux.Status)
	}
	// The flux step's DisplayName must carry the "HR X/Y ready" counter shape
	// the operator watched climb — 3/3 at convergence.
	const wantFluxLabel = "Flux installing — HR 3/3 ready"
	if flux.DisplayName != wantFluxLabel {
		t.Errorf("flux DisplayName = %q, want %q (live HR counter)", flux.DisplayName, wantFluxLabel)
	}
}

// TestBootstrapWindow_KubeconfigTimeoutFailsSteps proves a window where the
// kubeconfig never arrives surfaces the bootstrap steps as FAILED (not
// perpetually running) — the timeline must never lie about a dead prov.
func TestBootstrapWindow_KubeconfigTimeoutFailsSteps(t *testing.T) {
	h := handlerWithJobsStore(t)
	// No kubeconfig + a tiny arrival budget → waitForKubeconfig times out,
	// runPhase1Watch fails the bootstrap steps + markPhase1Done flips failed.
	h.kubeconfigArrivalTimeout = 60 * time.Millisecond
	h.kubeconfigArrivalPollInterval = 20 * time.Millisecond

	dep := makeDeploymentWithKubeconfig(t, h, "bootstrap-window-timeout", "")
	h.runPhase1Watch(dep)

	if dep.Status != "failed" {
		t.Fatalf("Status = %q, want failed (kubeconfig never arrived)", dep.Status)
	}

	got, err := h.jobs.ListJobs(dep.ID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	group := findJobByName(t, got, jobs.GroupClusterConverge)
	if group.Status != jobs.StatusFailed {
		t.Errorf("group Status = %q, want failed when kubeconfig never arrives", group.Status)
	}
}

// TestBootstrapWindow_HeartbeatLineUsesCloudInitTail proves the heartbeat line
// carries the cloud-init log tail (#3132) when present — the load-bearing
// signal that lets the operator literally watch the bootstrap.
func TestBootstrapWindow_HeartbeatLineUsesCloudInitTail(t *testing.T) {
	h := handlerWithJobsStore(t)
	dir := t.TempDir()
	h.kubeconfigsDir = dir
	dep := makeDeploymentWithKubeconfig(t, h, "bootstrap-window-tail", "")

	// No cloud-init log yet → bare numEvents line.
	if got := h.bootstrapHeartbeatLine(dep); got == "" {
		t.Fatalf("heartbeat line empty, want non-empty numEvents fallback")
	}

	// Write a cloud-init log; the tail must appear in the heartbeat line.
	logPath := dir + "/" + dep.ID + "-cloudinit.log"
	if err := os.WriteFile(logPath, []byte("Cloud-init v. 23.4 running\n[  OK  ] k3s.service started\n"), 0o600); err != nil {
		t.Fatalf("write cloud-init log: %v", err)
	}
	line := h.bootstrapHeartbeatLine(dep)
	if want := "k3s.service started"; !strings.Contains(line, want) {
		t.Errorf("heartbeat line = %q, want it to include cloud-init tail %q", line, want)
	}
}
