package jobs

import (
	"testing"
	"time"
)

// TestFilterFiniteJobs_DropsContinuousKeepsFinite asserts the /jobs view
// selector (#3996 follow-up + #5019 install lens): the open-ended
// reconciler leaves (reconcile / reconciler) are dropped, install leaves
// are KEPT along with their bootstrap-kit group (issue #5019 — the ~65
// bootstrap-kit install rows must be walkable on /jobs), finite kinds
// (step / task / cron / mutation / lifecycle) are kept, and a group left
// with no surviving descendant is pruned while a group with surviving
// children stays with a correct rollup.
func TestFilterFiniteJobs_DropsContinuousKeepsFinite(t *testing.T) {
	dep := "dep-x"
	t0 := time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)
	bk := JobID(dep, GroupBootstrapKit)
	rec := JobID(dep, GroupReconcilers)
	prov := JobID(dep, GroupProvisioner)

	in := []Job{
		// bootstrap-kit holds install leaves → group + leaves KEPT (#5019).
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

	// Survivors: bootstrap-kit group + 2 install leaves + provisioner
	// group + 4 finite leaves = 8.
	if len(out) != 8 {
		t.Fatalf("want 8 survivors (bk group + 2 installs + prov group + 4 finite leaves), got %d: %+v", len(out), names(out))
	}
	byName := map[string]*Job{}
	for i := range out {
		if IsContinuousReconciler(out[i].Kind) {
			t.Errorf("continuous reconciler survived: %q (kind=%q)", out[i].JobName, out[i].Kind)
		}
		if out[i].JobName == GroupReconcilers {
			t.Errorf("empty group %q should be pruned", out[i].JobName)
		}
		byName[out[i].JobName] = &out[i]
	}
	// #5019 — the install lens: install leaves + their group survive.
	for _, want := range []string{GroupBootstrapKit, "install-cilium", "install-flux"} {
		if byName[want] == nil {
			t.Errorf("install row %q missing from /jobs view (#5019)", want)
		}
	}
	bkGroup := byName[GroupBootstrapKit]
	if bkGroup != nil {
		if len(bkGroup.ChildIDs) != 2 {
			t.Errorf("bootstrap-kit ChildIDs: want 2, got %d (%v)", len(bkGroup.ChildIDs), bkGroup.ChildIDs)
		}
		// succeeded + running → running rollup.
		if bkGroup.Status != StatusRunning {
			t.Errorf("bootstrap-kit rollup status: want running, got %q", bkGroup.Status)
		}
	}
	g := byName[GroupProvisioner]
	if g == nil {
		t.Fatalf("provisioner group missing: %+v", names(out))
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
// the Kind field existed (Kind == "") is classified by JobName: an
// "install-*" legacy leaf survives (installs are /jobs rows per #5019), a
// "reconcile-*" legacy leaf is recognised as continuous and dropped, and a
// "task-*" legacy leaf survives.
func TestFilterFiniteJobs_LegacyKindBackfill(t *testing.T) {
	dep := "dep-legacy"
	in := []Job{
		{ID: JobID(dep, "install-legacy"), DeploymentID: dep, JobName: "install-legacy", Type: JobTypeInstall, Status: StatusSucceeded},
		{ID: JobID(dep, "reconcile-legacy"), DeploymentID: dep, JobName: "reconcile-legacy", Type: JobTypeInstall, Status: StatusRunning},
		{ID: JobID(dep, "task-legacy"), DeploymentID: dep, JobName: "task-legacy", Type: JobTypeInstall, Status: StatusSucceeded},
	}
	out := FilterFiniteJobs(in)
	got := map[string]bool{}
	for _, j := range out {
		got[j.JobName] = true
	}
	if len(out) != 2 || !got["install-legacy"] || !got["task-legacy"] {
		t.Fatalf("legacy backfill: want install-legacy + task-legacy, got %v", names(out))
	}
	if got["reconcile-legacy"] {
		t.Fatalf("legacy reconcile leaf must be dropped, got %v", names(out))
	}
}

// TestFilterFiniteJobs_AllContinuousYieldsEmpty asserts a deployment whose
// /jobs store is nothing but open-ended reconcilers returns an empty list
// (never nil — the handler marshals []).
func TestFilterFiniteJobs_AllContinuousYieldsEmpty(t *testing.T) {
	dep := "dep-all-recon"
	in := []Job{
		{ID: JobID(dep, "reconcile-b"), DeploymentID: dep, JobName: "reconcile-b", Kind: KindReconcile, Type: JobTypeInstall},
		{ID: JobID(dep, "reconciler-c"), DeploymentID: dep, JobName: "reconciler-c", Kind: KindReconciler, Type: JobTypeInstall},
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
