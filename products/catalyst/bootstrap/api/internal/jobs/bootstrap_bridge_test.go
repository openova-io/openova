// bootstrap_bridge_test.go — proves the BootstrapBridge fills the ~30-minute
// "Bootstrapping cluster" window with a group + three live step children whose
// status rolls up so the provisioning timeline shows continuous motion instead
// of a static "Provision <provider>: Success".
package jobs

import (
	"strings"
	"testing"
	"time"
)

func newBootstrapFixture(t *testing.T) (*Store, *BootstrapBridge, string) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	depID := "dep-bootstrap"
	return st, NewBootstrapBridge(st, depID), depID
}

func findBootstrapJob(t *testing.T, in []Job, name string) Job {
	t.Helper()
	for _, j := range in {
		if j.JobName == name {
			return j
		}
	}
	t.Fatalf("job %q not found in %d rows", name, len(in))
	return Job{}
}

// TestBootstrapBridge_SeedProjectsGroupAndChain is the core structural test:
// Seed materialises the "Bootstrapping cluster" group + the three step leaves
// in pending, each step depending on the prior one (linear chain).
func TestBootstrapBridge_SeedProjectsGroupAndChain(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)

	if err := bb.Seed(); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	group := findBootstrapJob(t, got, GroupClusterConverge)
	if group.Type != JobTypeGroup {
		t.Fatalf("group Type = %q, want %q", group.Type, JobTypeGroup)
	}
	if group.DisplayName != GroupClusterConvergeDisplay {
		t.Fatalf("group DisplayName = %q, want %q", group.DisplayName, GroupClusterConvergeDisplay)
	}
	// Empty/pending group at seed time.
	if group.Status != StatusPending {
		t.Fatalf("group Status = %q, want pending at seed", group.Status)
	}

	nodes := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepNodesBooting))
	kube := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepKubeconfig))
	flux := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepFluxInstalling))

	for _, s := range []Job{nodes, kube, flux} {
		if s.Status != StatusPending {
			t.Fatalf("step %q Status = %q, want pending", s.JobName, s.Status)
		}
		if s.ParentID != JobID(depID, GroupClusterConverge) {
			t.Fatalf("step %q ParentID = %q, want group id", s.JobName, s.ParentID)
		}
		if s.Kind != KindStep {
			t.Fatalf("step %q Kind = %q, want %q", s.JobName, s.Kind, KindStep)
		}
	}

	// Linear chain: nodes has no dep; kube depends on nodes; flux depends on kube.
	if len(nodes.DependsOn) != 0 {
		t.Fatalf("nodes DependsOn = %v, want empty", nodes.DependsOn)
	}
	if len(kube.DependsOn) != 1 || kube.DependsOn[0] != ActivityStepJobName(GroupClusterConverge, BootstrapStepNodesBooting) {
		t.Fatalf("kube DependsOn = %v, want [nodes step]", kube.DependsOn)
	}
	if len(flux.DependsOn) != 1 || flux.DependsOn[0] != ActivityStepJobName(GroupClusterConverge, BootstrapStepKubeconfig) {
		t.Fatalf("flux DependsOn = %v, want [kube step]", flux.DependsOn)
	}
}

// TestBootstrapBridge_GroupRunsWhileNodesBooting proves the group rolls up to
// "running" the instant the nodes-booting step starts — this is the signal
// that fills the void: the operator never sees a static Success behind which
// nothing is moving.
func TestBootstrapBridge_GroupRunsWhileNodesBooting(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)
	now := time.Now().UTC()

	if err := bb.Seed(); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if err := bb.StartStep(BootstrapStepNodesBooting, "Nodes booting", now); err != nil {
		t.Fatalf("StartStep: %v", err)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	group := findBootstrapJob(t, got, GroupClusterConverge)
	if group.Status != StatusRunning {
		t.Fatalf("group Status = %q, want running while nodes booting", group.Status)
	}
	nodes := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepNodesBooting))
	if nodes.Status != StatusRunning {
		t.Fatalf("nodes Status = %q, want running", nodes.Status)
	}
	if nodes.StartedAt == nil {
		t.Fatalf("nodes StartedAt nil, want stamped")
	}
}

// TestBootstrapBridge_Heartbeat appends live log lines to the running step
// WITHOUT changing its status — the always-there motion during the long wait.
func TestBootstrapBridge_Heartbeat(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)
	now := time.Now().UTC()

	_ = bb.Seed()
	_ = bb.StartStep(BootstrapStepNodesBooting, "started", now)

	if err := bb.Heartbeat(BootstrapStepNodesBooting, "cloud-init: running module final", now.Add(time.Second)); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	jobName := ActivityStepJobName(GroupClusterConverge, BootstrapStepNodesBooting)
	job, execs, err := st.GetJob(depID, jobName)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != StatusRunning {
		t.Fatalf("after heartbeat status = %q, want running (heartbeat must not terminate)", job.Status)
	}
	if len(execs) != 1 {
		t.Fatalf("execs = %d, want 1 (heartbeat reuses the open execution)", len(execs))
	}
	page, err := st.PageLogs(depID, execs[0].ID, 0, 100)
	if err != nil {
		t.Fatalf("PageLogs: %v", err)
	}
	var sawHeartbeat bool
	for _, l := range page.Lines {
		if strings.Contains(l.Message, "module final") {
			sawHeartbeat = true
		}
	}
	if !sawHeartbeat {
		t.Fatalf("heartbeat line not found in %d log lines", len(page.Lines))
	}
}

// TestBootstrapBridge_MarkKubeconfigReceived advances the window: nodes +
// kubeconfig steps succeed, flux step starts running — exactly the timeline
// motion the operator sees the instant the kubeconfig PUT lands.
func TestBootstrapBridge_MarkKubeconfigReceived(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)
	now := time.Now().UTC()

	_ = bb.Seed()
	_ = bb.StartStep(BootstrapStepNodesBooting, "started", now)
	if err := bb.MarkKubeconfigReceived("kubeconfig received (4321 bytes)", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("MarkKubeconfigReceived: %v", err)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	nodes := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepNodesBooting))
	kube := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepKubeconfig))
	flux := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepFluxInstalling))

	if nodes.Status != StatusSucceeded {
		t.Fatalf("nodes Status = %q, want succeeded", nodes.Status)
	}
	if kube.Status != StatusSucceeded {
		t.Fatalf("kube Status = %q, want succeeded", kube.Status)
	}
	if flux.Status != StatusRunning {
		t.Fatalf("flux Status = %q, want running after kubeconfig received", flux.Status)
	}
	// The group is still running because flux is in flight.
	group := findBootstrapJob(t, got, GroupClusterConverge)
	if group.Status != StatusRunning {
		t.Fatalf("group Status = %q, want running while flux installs", group.Status)
	}
}

// TestBootstrapBridge_FluxProgressLiveLabel proves the flux step's DisplayName
// carries the live "HR X/Y ready" counter the operator watches climb, and that
// an unchanged census is a no-op (no churn at the poll cadence).
func TestBootstrapBridge_FluxProgressLiveLabel(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)
	now := time.Now().UTC()

	_ = bb.Seed()
	_ = bb.StartStep(BootstrapStepNodesBooting, "started", now)
	_ = bb.MarkKubeconfigReceived("kc", now.Add(time.Minute))

	jobName := ActivityStepJobName(GroupClusterConverge, BootstrapStepFluxInstalling)

	if err := bb.SetFluxProgress(3, 11, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("SetFluxProgress: %v", err)
	}
	job, execs, err := st.GetJob(depID, jobName)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	wantLabel := "Flux installing — HR 3/11 ready"
	if job.DisplayName != wantLabel {
		t.Fatalf("flux DisplayName = %q, want %q", job.DisplayName, wantLabel)
	}

	before, _ := st.PageLogs(depID, execs[0].ID, 0, 1000)
	// Same census again → no-op (no new label, no new log line).
	if err := bb.SetFluxProgress(3, 11, now.Add(3*time.Minute)); err != nil {
		t.Fatalf("SetFluxProgress repeat: %v", err)
	}
	after, _ := st.PageLogs(depID, execs[0].ID, 0, 1000)
	if after.Total != before.Total {
		t.Fatalf("repeat unchanged census appended a log line (%d → %d), want no-op", before.Total, after.Total)
	}

	// Census advances → label climbs.
	if err := bb.SetFluxProgress(11, 11, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("SetFluxProgress climb: %v", err)
	}
	job, _, _ = st.GetJob(depID, jobName)
	if job.DisplayName != "Flux installing — HR 11/11 ready" {
		t.Fatalf("flux DisplayName = %q, want climbed to 11/11", job.DisplayName)
	}
}

// TestBootstrapBridge_MarkConverged closes the window cleanly — flux succeeds,
// the whole group rolls up to succeeded so the bootstrap-kit install rows take
// over the timeline.
func TestBootstrapBridge_MarkConverged(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)
	now := time.Now().UTC()

	_ = bb.Seed()
	_ = bb.StartStep(BootstrapStepNodesBooting, "started", now)
	_ = bb.MarkKubeconfigReceived("kc", now.Add(time.Minute))
	_ = bb.SetFluxProgress(11, 11, now.Add(2*time.Minute))
	if err := bb.MarkConverged("converged", now.Add(3*time.Minute)); err != nil {
		t.Fatalf("MarkConverged: %v", err)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	group := findBootstrapJob(t, got, GroupClusterConverge)
	if group.Status != StatusSucceeded {
		t.Fatalf("group Status = %q, want succeeded after convergence", group.Status)
	}
	flux := findBootstrapJob(t, got, ActivityStepJobName(GroupClusterConverge, BootstrapStepFluxInstalling))
	if flux.Status != StatusSucceeded {
		t.Fatalf("flux Status = %q, want succeeded", flux.Status)
	}
	if flux.FinishedAt == nil {
		t.Fatalf("flux FinishedAt nil, want stamped on convergence")
	}
}

// TestBootstrapBridge_KubeconfigTimeoutFailsHonestly proves a failed window
// (kubeconfig never arrives) surfaces the steps as failed, not perpetually
// running — the timeline must never lie about a dead prov.
func TestBootstrapBridge_KubeconfigTimeoutFailsHonestly(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)
	now := time.Now().UTC()

	_ = bb.Seed()
	_ = bb.StartStep(BootstrapStepNodesBooting, "started", now)
	if err := bb.FinishStep(BootstrapStepNodesBooting, StatusFailed, "no kubeconfig", now.Add(time.Minute)); err != nil {
		t.Fatalf("FinishStep nodes: %v", err)
	}
	if err := bb.FinishStep(BootstrapStepKubeconfig, StatusFailed, "no kubeconfig", now.Add(time.Minute)); err != nil {
		t.Fatalf("FinishStep kube: %v", err)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	group := findBootstrapJob(t, got, GroupClusterConverge)
	if group.Status != StatusFailed {
		t.Fatalf("group Status = %q, want failed when kubeconfig never arrives", group.Status)
	}
}

// TestBootstrapBridge_SeedIdempotent proves re-seeding (Pod-restart resume)
// never un-starts a running step.
func TestBootstrapBridge_SeedIdempotent(t *testing.T) {
	st, bb, depID := newBootstrapFixture(t)
	now := time.Now().UTC()

	_ = bb.Seed()
	_ = bb.StartStep(BootstrapStepNodesBooting, "started", now)
	// Re-seed (resume) must preserve the running nodes step.
	if err := bb.Seed(); err != nil {
		t.Fatalf("Seed re-run: %v", err)
	}
	job, _, err := st.GetJob(depID, ActivityStepJobName(GroupClusterConverge, BootstrapStepNodesBooting))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != StatusRunning {
		t.Fatalf("after re-seed nodes Status = %q, want still running (idempotent)", job.Status)
	}
	if job.StartedAt == nil {
		t.Fatalf("after re-seed nodes StartedAt nil, want preserved")
	}
}
