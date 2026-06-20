package jobs

import (
	"testing"
	"time"
)

// TestFilterFiniteJobs_DropsContinuousKeepsFinite asserts the /jobs
// FINITE-work view (#3996 follow-up): the continuous reconcilers (install /
// reconcile / reconciler leaves) are dropped, finite kinds (step / task /
// cron / mutation / lifecycle) are kept, and a group left with no surviving
// descendant is pruned while a group with a surviving finite child stays
// with a correct rollup.
func TestFilterFiniteJobs_DropsContinuousKeepsFinite(t *testing.T) {
	dep := "dep-x"
	t0 := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	bk := JobID(dep, GroupBootstrapKit)
	rec := JobID(dep, GroupReconcilers)
	prov := JobID(dep, GroupProvisioner)

	in := []Job{
		// bootstrap-kit holds only continuous install leaves → group pruned.
		{ID: bk, DeploymentID: dep, JobName: GroupBootstrapKit, Type: JobTypeGroup, Kind: KindGroup},
		{ID: JobID(dep, "install-cilium"), DeploymentID: dep, JobName: "install-cilium", Kind: KindInstall, Type: JobTypeInstall, ParentID: bk, Status: StatusSucceeded},
		{ID: JobID(dep, "install-flux"), DeploymentID: dep, JobName: "install-flux", Kind: KindInstall, Type: JobTypeInstall, ParentID: bk, Status: StatusRunning},

		// reconcilers group holds only Kustomization reconciles + a
		// reconciler Deployment → group pruned.
		{ID: rec, DeploymentID: dep, JobName: GroupReconcilers, Type: JobTypeGroup, Kind: KindGroup},
		{ID: JobID(dep, "reconcile-apps"), DeploymentID: dep, JobName: "reconcile-apps", Kind: KindReconcile, Type: JobTypeInstall, ParentID: rec, Status: StatusRunning},
		{ID: JobID(dep, "reconciler-sso-bridge"), DeploymentID: dep, JobName: "reconciler-sso-bridge", Kind: KindReconciler, Type: JobTypeInstall, ParentID: rec, Status: StatusHealthy},

		// provisioner group holds finite work → group + children kept.
		{ID: prov, DeploymentID: dep, JobName: GroupProvisioner, Type: JobTypeGroup, Kind: KindGroup},
		{ID: JobID(dep, "provisioner-step-tofu-init"), DeploymentID: dep, JobName: "provisioner-step-tofu-init", Kind: KindStep, Type: JobTypeInstall, ParentID: prov, Status: StatusSucceeded, StartedAt: &t0, FinishedAt: ptrT(t0.Add(time.Second))},
		{ID: JobID(dep, "task-backup-once"), DeploymentID: dep, JobName: "task-backup-once", Kind: KindTask, Type: JobTypeInstall, ParentID: prov, Status: StatusRunning, StartedAt: ptrT(t0.Add(2 * time.Second))},
		{ID: JobID(dep, "cron-snapshot"), DeploymentID: dep, JobName: "cron-snapshot", Kind: KindCron, Type: JobTypeInstall, ParentID: prov, Status: StatusSucceeded, StartedAt: &t0},
		{ID: JobID(dep, "mutation-create-bucket"), DeploymentID: dep, JobName: "mutation-create-bucket", Kind: KindMutation, Type: JobTypeInstall, ParentID: prov, Status: StatusSucceeded, StartedAt: &t0},
	}

	out := FilterFiniteJobs(in)

	// Survivors: provisioner group + 4 finite leaves = 5.
	if len(out) != 5 {
		t.Fatalf("want 5 survivors (group + 4 finite leaves), got %d: %+v", len(out), names(out))
	}
	for _, j := range out {
		if IsContinuousReconciler(j.Kind) {
			t.Errorf("continuous reconciler survived: %q (kind=%q)", j.JobName, j.Kind)
		}
		if j.JobName == GroupBootstrapKit || j.JobName == GroupReconcilers {
			t.Errorf("empty group %q should be pruned", j.JobName)
		}
	}
	var g *Job
	for i := range out {
		if out[i].Type == JobTypeGroup {
			g = &out[i]
		}
	}
	if g == nil || g.JobName != GroupProvisioner {
		t.Fatalf("provisioner group missing/wrong: %+v", out)
	}
	if len(g.ChildIDs) != 4 {
		t.Errorf("provisioner ChildIDs: want 4, got %d (%v)", len(g.ChildIDs), g.ChildIDs)
	}
	// succeeded + running + succeeded + succeeded → running rollup.
	if g.Status != StatusRunning {
		t.Errorf("provisioner rollup status: want running, got %q", g.Status)
	}
}

// TestFilterFiniteJobs_LegacyKindBackfill asserts a row persisted before
// the Kind field existed (Kind == "") is classified by JobName so an
// "install-*" legacy leaf is still recognised as continuous and dropped,
// while a "task-*" legacy leaf survives.
func TestFilterFiniteJobs_LegacyKindBackfill(t *testing.T) {
	dep := "dep-legacy"
	in := []Job{
		{ID: JobID(dep, "install-legacy"), DeploymentID: dep, JobName: "install-legacy", Type: JobTypeInstall, Status: StatusSucceeded},
		{ID: JobID(dep, "task-legacy"), DeploymentID: dep, JobName: "task-legacy", Type: JobTypeInstall, Status: StatusSucceeded},
	}
	out := FilterFiniteJobs(in)
	if len(out) != 1 || out[0].JobName != "task-legacy" {
		t.Fatalf("legacy backfill: want only task-legacy, got %v", names(out))
	}
}

// TestFilterFiniteJobs_AllContinuousYieldsEmpty asserts a deployment whose
// /jobs store is nothing but continuous reconcilers returns an empty list
// (never nil — the handler marshals []).
func TestFilterFiniteJobs_AllContinuousYieldsEmpty(t *testing.T) {
	dep := "dep-all-recon"
	in := []Job{
		{ID: JobID(dep, "install-a"), DeploymentID: dep, JobName: "install-a", Kind: KindInstall, Type: JobTypeInstall},
		{ID: JobID(dep, "reconcile-b"), DeploymentID: dep, JobName: "reconcile-b", Kind: KindReconcile, Type: JobTypeInstall},
	}
	out := FilterFiniteJobs(in)
	if len(out) != 0 {
		t.Fatalf("want empty, got %v", names(out))
	}
	if out == nil {
		t.Fatalf("want non-nil empty slice")
	}
}

func names(js []Job) []string {
	out := make([]string, len(js))
	for i := range js {
		out[i] = js[i].JobName
	}
	return out
}

func ptrT(t time.Time) *time.Time { return &t }
