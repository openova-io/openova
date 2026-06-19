// reconciler_bridge_test.go — the §5a/§5b ingestion-breadth proofs
// (issue #3646): a failing recurring CronJob surfaces RED while the
// install leaf stays green; a stuck batch Job is a visible task row; a
// reconciler Deployment carries a HEALTH status; an install-hook Job
// attaches as an Execution under its install leaf (no duplicate task row).
package jobs

import (
	"testing"
	"time"
)

func newTestBridge(t *testing.T) (*Bridge, *Store) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return NewBridge(st, "dep-recon"), st
}

func findByName(jobs []Job, jobName string) (Job, bool) {
	for _, j := range jobs {
		if j.JobName == jobName {
			return j, true
		}
	}
	return Job{}, false
}

// §3a / DoD#1: a Failed CronJob shows a RED cron-<name> row WHILE the
// install-<chart> leaf stays green.
func TestReconcilerBridge_FailingCron_RedWhileInstallGreen(t *testing.T) {
	b, st := newTestBridge(t)

	// install-openbao is green (helmwatch seed).
	if _, _, err := b.SeedJobsFromInformerList([]InformerSeed{
		{Component: "openbao", State: HelmStateInstalled, Message: "Helm install succeeded"},
	}); err != nil {
		t.Fatalf("seed install: %v", err)
	}

	// openbao-snapshot-save CronJob: latest run Failed.
	now := time.Now().UTC()
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindCron, Name: "openbao-snapshot-save", Namespace: "openbao",
		Status: "installed", // refined by the run below
		Executions: []ReconcilerExecutionObservation{{
			Name: "openbao-snapshot-save-29693750", Status: "failed",
			StartedAt: now.Add(-2 * time.Minute), FinishedAt: now.Add(-time.Minute),
			Message: "bao login returned no client_token",
		}},
	}}); err != nil {
		t.Fatalf("seed cron: %v", err)
	}

	all, err := st.ListJobs("dep-recon")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	install, ok := findByName(all, "install-openbao")
	if !ok {
		t.Fatal("install-openbao missing")
	}
	if install.Status != StatusSucceeded {
		t.Errorf("install-openbao status: want succeeded, got %q (must stay green)", install.Status)
	}
	if install.Kind != KindInstall {
		t.Errorf("install-openbao kind: want %q, got %q", KindInstall, install.Kind)
	}

	cron, ok := findByName(all, "cron-openbao-snapshot-save")
	if !ok {
		t.Fatal("cron-openbao-snapshot-save missing — recurring activity invisible (the §3a lie)")
	}
	if cron.Status != StatusFailed {
		t.Errorf("cron status: want failed (RED), got %q", cron.Status)
	}
	if cron.Kind != KindCron {
		t.Errorf("cron kind: want %q, got %q", KindCron, cron.Kind)
	}

	// The Reconcilers group rolls up to failed because of the cron.
	grp, ok := findByName(all, GroupReconcilers)
	if !ok {
		t.Fatal("reconcilers group missing")
	}
	if grp.Status != StatusFailed {
		t.Errorf("reconcilers group: want failed, got %q", grp.Status)
	}

	// DoD#3: the cron row has one Execution per spawned run.
	_, execs, err := st.GetJob("dep-recon", JobID("dep-recon", "cron-openbao-snapshot-save"))
	if err != nil {
		t.Fatalf("get cron: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("cron executions: want 1 per run, got %d", len(execs))
	}
	if execs[0].Status != StatusFailed {
		t.Errorf("cron run status: want failed, got %q", execs[0].Status)
	}
}

// §3a / DoD#2: a stuck child batch Job surfaces as a running task row.
func TestReconcilerBridge_StuckJob_VisibleTaskRow(t *testing.T) {
	b, st := newTestBridge(t)
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindTask, Name: "cnpg-pair-bp-cnpg-pair-primary-3-join", Namespace: "cnpg",
		Status: "installing", Message: "instance 3 joining",
	}}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	all, _ := st.ListJobs("dep-recon")
	task, ok := findByName(all, "task-cnpg-pair-bp-cnpg-pair-primary-3-join")
	if !ok {
		t.Fatal("stuck join Job invisible — the §3a lie")
	}
	if task.Status != StatusRunning {
		t.Errorf("task status: want running, got %q", task.Status)
	}
	if task.Kind != KindTask {
		t.Errorf("task kind: want %q, got %q", KindTask, task.Kind)
	}
}

// §3a / DoD#4: a reconciler Deployment carries a HEALTH status (degraded),
// never a one-shot "succeeded".
func TestReconcilerBridge_ReconcilerHealth(t *testing.T) {
	b, st := newTestBridge(t)
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindReconciler, Name: "sso-bridge", Namespace: "sso-bridge",
		Status: "degraded", Health: true, Message: "ready replicas 0/1",
	}}); err != nil {
		t.Fatalf("seed reconciler: %v", err)
	}
	all, _ := st.ListJobs("dep-recon")
	rec, ok := findByName(all, "reconciler-sso-bridge")
	if !ok {
		t.Fatal("reconciler-sso-bridge missing")
	}
	if rec.Status != StatusDegraded {
		t.Errorf("reconciler status: want degraded (health), got %q", rec.Status)
	}
	if rec.Kind != KindReconciler {
		t.Errorf("reconciler kind: want %q, got %q", KindReconciler, rec.Kind)
	}
	if rec.Status == StatusSucceeded {
		t.Error("reconciler must never read one-shot 'succeeded'")
	}

	// Flip to healthy → group no longer failed.
	if err := b.OnReconcilerObservation(ReconcilerObservation{
		Kind: ObsKindReconciler, Name: "sso-bridge", Namespace: "sso-bridge",
		Status: "healthy", Health: true, Message: "ready replicas 1/1",
	}); err != nil {
		t.Fatalf("flip healthy: %v", err)
	}
	all, _ = st.ListJobs("dep-recon")
	rec, _ = findByName(all, "reconciler-sso-bridge")
	if rec.Status != StatusHealthy {
		t.Errorf("after scale-back: want healthy, got %q", rec.Status)
	}
}

// §5a ownership de-dup: an HR install-hook Job attaches as an Execution
// under the install leaf, NOT a duplicate task-* row.
func TestReconcilerBridge_InstallHook_AttachesToInstallLeaf(t *testing.T) {
	b, st := newTestBridge(t)
	if _, _, err := b.SeedJobsFromInformerList([]InformerSeed{
		{Component: "guacamole", State: HelmStateInstalled, Message: "ok"},
	}); err != nil {
		t.Fatalf("seed install: %v", err)
	}
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindTask, Name: "guacamole-bp-guacamole-admin-enroll-abc", Namespace: "guacamole",
		Status: "installed", Message: "enrolled",
		OwnerInstallChart: "guacamole",
	}}); err != nil {
		t.Fatalf("seed hook: %v", err)
	}
	all, _ := st.ListJobs("dep-recon")
	if _, ok := findByName(all, "task-guacamole-bp-guacamole-admin-enroll-abc"); ok {
		t.Error("install-hook minted a duplicate task-* row — ownership de-dup failed")
	}
	// The hook run is an Execution under install-guacamole.
	_, execs, err := st.GetJob("dep-recon", JobID("dep-recon", "install-guacamole"))
	if err != nil {
		t.Fatalf("get install-guacamole: %v", err)
	}
	if len(execs) == 0 {
		t.Error("install-hook run did not attach as an Execution under install-guacamole")
	}
}

// §5b: every leaf carries a typed kind; legacy rows without a persisted
// kind get it back-filled on read (kindForLeaf).
func TestReconcilerBridge_KindBackfillOnRead(t *testing.T) {
	_, st := newTestBridge(t)
	// Write a leaf WITHOUT a Kind (simulating a legacy index row) by going
	// straight through UpsertJob — the chokepoint stamps install from the
	// JobName, so to prove the read-path back-fill we craft a name whose
	// prefix the chokepoint also recognises and assert the wire carries it.
	if err := st.UpsertJob(Job{
		DeploymentID: "dep-recon", JobName: "reconcile-flux-system", Type: JobTypeInstall,
		Status: StatusSucceeded,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	all, _ := st.ListJobs("dep-recon")
	j, ok := findByName(all, "reconcile-flux-system")
	if !ok {
		t.Fatal("reconcile leaf missing")
	}
	if j.Kind != KindReconcile {
		t.Errorf("kind back-fill: want %q, got %q", KindReconcile, j.Kind)
	}
}

// TestReconcilerBridge_RepeatedSeedsKeepStableCount pins the accumulation
// fix at the bridge layer (issue #3916): re-seeding the SAME reconciler
// identities across many polls (the chroot /jobs re-read on every request)
// must UPDATE one entry per identity in place — never append a new row per
// poll. The reconciler-observation producer already collapses per-run Job
// instances to a stable base name; this asserts the bridge's UpsertJob keying
// holds the leaf count flat across repeated seeds.
func TestReconcilerBridge_RepeatedSeedsKeepStableCount(t *testing.T) {
	b, st := newTestBridge(t)

	obs := []ReconcilerObservation{
		{Kind: ObsKindReconcile, Name: "flux-system", Namespace: "flux-system", Status: "installing", Message: "reconciling"},
		{Kind: ObsKindTask, Name: "db-migrate", Namespace: "data", Status: "installing", Message: "running"},
		{Kind: ObsKindReconciler, Name: "sso-bridge", Namespace: "sso-bridge", Status: "healthy", Health: true, Message: "1/1"},
	}

	countLeaves := func() int {
		all, err := st.ListJobs("dep-recon")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		n := 0
		for _, j := range all {
			if j.Type != JobTypeGroup {
				n++
			}
		}
		return n
	}

	// First poll establishes the leaves.
	if _, _, err := b.SeedReconcilerObservations(obs); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	first := countLeaves()
	if first != 3 {
		t.Fatalf("first poll leaf count = %d, want 3", first)
	}

	// 20 more polls of the SAME identities (status may even change) must not
	// grow the count — one stable entry per reconciler identity.
	for i := 0; i < 20; i++ {
		if _, _, err := b.SeedReconcilerObservations(obs); err != nil {
			t.Fatalf("seed repeat %d: %v", i, err)
		}
	}
	if got := countLeaves(); got != first {
		t.Errorf("accumulation: leaf count grew from %d to %d across repeated polls (#3916)", first, got)
	}
}

// TestReconcilerBridge_StillReconcilingTaskIsRunningNotTerminal pins the
// anti-flap contract at the bridge layer: an observation carrying the
// in-progress status ("installing") writes a RUNNING leaf, never a terminal
// failed/succeeded. The leaf only becomes terminal when the observation
// itself is terminal.
func TestReconcilerBridge_StillReconcilingTaskIsRunningNotTerminal(t *testing.T) {
	b, st := newTestBridge(t)
	if err := b.OnReconcilerObservation(ReconcilerObservation{
		Kind: ObsKindReconcile, Name: "apps", Namespace: "flux-system",
		Status: "installing", Message: "reconcile in progress",
	}); err != nil {
		t.Fatalf("observe: %v", err)
	}
	all, _ := st.ListJobs("dep-recon")
	rec, ok := findByName(all, "reconcile-apps")
	if !ok {
		t.Fatal("reconcile-apps leaf missing")
	}
	if rec.Status != StatusRunning {
		t.Errorf("still-reconciling leaf status = %q, want %q (no flapping terminal)", rec.Status, StatusRunning)
	}
	if IsTerminal(rec.Status) {
		t.Errorf("a still-reconciling leaf must NOT be terminal, got %q", rec.Status)
	}
}
