// activity_bridge_test.go — proves the GENERIC source-bridge projects a
// declared activity (e.g. the 11-step cutover) into the same Job +
// Execution + LogLine model the canvas reads, WITH dependsOn edges and a
// group whose status rolls up from its steps (issue #3646).
package jobs

import (
	"testing"
	"time"
)

func newActivityFixture(t *testing.T) (*Store, *ActivityBridge, string) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	depID := "dep-activity"
	return st, NewActivityBridge(st, depID, GroupCutover, GroupCutoverDisplay), depID
}

// findJob returns the Job with the given JobName from a slice, or fails.
func findActivityJob(t *testing.T, in []Job, name string) Job {
	t.Helper()
	for _, j := range in {
		if j.JobName == name {
			return j
		}
	}
	t.Fatalf("job %q not found in %d rows", name, len(in))
	return Job{}
}

// TestActivityBridge_SeedProjectsStepsWithEdges is the core acceptance
// test for #3646: seeding a cutover-style activity projects one Job per
// step under the Cutover group, each step depending on the prior step,
// and the group rolls its status up from the steps.
func TestActivityBridge_SeedProjectsStepsWithEdges(t *testing.T) {
	st, ab, depID := newActivityFixture(t)

	// A 3-step slice deliberately given OUT of order to prove the bridge
	// sorts by Order before chaining the edges.
	steps := []ActivityStep{
		{Slug: "harbor-prewarm", DisplayName: "Harbor Prewarm", Order: 3},
		{Slug: "gitea-mirror", DisplayName: "Gitea Mirror", Order: 1},
		{Slug: "registry-pivot", DisplayName: "Registry Pivot", Order: 2},
	}
	if err := ab.SeedSteps(steps); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}

	// The Cutover group must exist as a group Job.
	group := findActivityJob(t, got, GroupCutover)
	if group.Type != JobTypeGroup {
		t.Fatalf("group %q: Type = %q, want %q", GroupCutover, group.Type, JobTypeGroup)
	}
	if group.DisplayName != GroupCutoverDisplay {
		t.Errorf("group DisplayName = %q, want %q", group.DisplayName, GroupCutoverDisplay)
	}
	// All steps pending => group pending.
	if group.Status != StatusPending {
		t.Errorf("group status = %q, want %q (all steps pending)", group.Status, StatusPending)
	}

	// Each step is a leaf under the Cutover group.
	mirror := findActivityJob(t, got, ActivityStepJobName(GroupCutover, "gitea-mirror"))
	pivot := findActivityJob(t, got, ActivityStepJobName(GroupCutover, "registry-pivot"))
	prewarm := findActivityJob(t, got, ActivityStepJobName(GroupCutover, "harbor-prewarm"))

	for _, s := range []Job{mirror, pivot, prewarm} {
		if s.Type != JobTypeInstall {
			t.Errorf("step %q: Type = %q, want %q", s.JobName, s.Type, JobTypeInstall)
		}
		if s.ParentID != JobID(depID, GroupCutover) {
			t.Errorf("step %q: ParentID = %q, want %q", s.JobName, s.ParentID, JobID(depID, GroupCutover))
		}
		if s.Status != StatusPending {
			t.Errorf("step %q: Status = %q, want %q", s.JobName, s.Status, StatusPending)
		}
	}

	// Edges: first step (gitea-mirror, order 1) has NO deps; registry-pivot
	// depends on gitea-mirror; harbor-prewarm depends on registry-pivot.
	if len(mirror.DependsOn) != 0 {
		t.Errorf("gitea-mirror DependsOn = %v, want [] (first step)", mirror.DependsOn)
	}
	wantPivotDep := ActivityStepJobName(GroupCutover, "gitea-mirror")
	if len(pivot.DependsOn) != 1 || pivot.DependsOn[0] != wantPivotDep {
		t.Errorf("registry-pivot DependsOn = %v, want [%q]", pivot.DependsOn, wantPivotDep)
	}
	wantPrewarmDep := ActivityStepJobName(GroupCutover, "registry-pivot")
	if len(prewarm.DependsOn) != 1 || prewarm.DependsOn[0] != wantPrewarmDep {
		t.Errorf("harbor-prewarm DependsOn = %v, want [%q]", prewarm.DependsOn, wantPrewarmDep)
	}

	// The group must list all three steps as children.
	if len(group.ChildIDs) != 3 {
		t.Errorf("group ChildIDs = %v, want 3 children", group.ChildIDs)
	}
}

// TestActivityBridge_StepLifecycleRollsUp proves a step transitioning
// running → succeeded drives an Execution + log lines and rolls the group
// status up; and that a failed step rolls the group up to failed (never a
// misleading "succeeded" — the #3646 defect).
func TestActivityBridge_StepLifecycleRollsUp(t *testing.T) {
	st, ab, depID := newActivityFixture(t)
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	steps := []ActivityStep{
		{Slug: "gitea-mirror", Order: 1},
		{Slug: "registry-pivot", Order: 2},
	}
	if err := ab.SeedSteps(steps); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}

	// Step 1 runs then succeeds.
	if err := ab.StartStep(steps[0], "mirror started", base); err != nil {
		t.Fatalf("StartStep(0): %v", err)
	}
	// Mid-run the group is running.
	group := findActivityJob(t, mustListA(t, st, depID), GroupCutover)
	if group.Status != StatusRunning {
		t.Fatalf("group status mid-run = %q, want %q", group.Status, StatusRunning)
	}
	if err := ab.FinishStep(steps[0], ActivityResultSuccess, "mirror done", base.Add(time.Minute)); err != nil {
		t.Fatalf("FinishStep(0): %v", err)
	}

	// Step 1 must now be succeeded with a finished Execution carrying log
	// lines (a real exec surface, not a fabricated row).
	step1, execs, err := st.GetJob(depID, ActivityStepJobName(GroupCutover, "gitea-mirror"))
	if err != nil {
		t.Fatalf("GetJob(step1): %v", err)
	}
	if step1.Status != StatusSucceeded {
		t.Errorf("step1 status = %q, want %q", step1.Status, StatusSucceeded)
	}
	if len(execs) != 1 {
		t.Fatalf("step1 executions = %d, want 1", len(execs))
	}
	if !IsTerminal(execs[0].Status) {
		t.Errorf("step1 execution status = %q, want terminal", execs[0].Status)
	}
	page, err := st.PageLogs(depID, execs[0].ID, 1, 100)
	if err != nil {
		t.Fatalf("PageLogs(step1): %v", err)
	}
	if page.Total < 2 {
		t.Errorf("step1 log lines = %d, want >= 2 (start + finish)", page.Total)
	}

	// Group is still running (step 2 pending).
	group = findActivityJob(t, mustListA(t, st, depID), GroupCutover)
	if group.Status != StatusPending && group.Status != StatusRunning {
		t.Errorf("group status after step1 = %q, want pending/running (step2 not started)", group.Status)
	}

	// Step 2 fails — the group MUST roll up to failed (the #3646
	// regression was the group/leaf reading "succeeded" while the
	// execution had not truly completed).
	if err := ab.StartStep(steps[1], "pivot started", base.Add(2*time.Minute)); err != nil {
		t.Fatalf("StartStep(1): %v", err)
	}
	if err := ab.FinishStep(steps[1], ActivityResultFailed, "pivot blew up", base.Add(3*time.Minute)); err != nil {
		t.Fatalf("FinishStep(1): %v", err)
	}
	group = findActivityJob(t, mustListA(t, st, depID), GroupCutover)
	if group.Status != StatusFailed {
		t.Errorf("group status after step2 failure = %q, want %q", group.Status, StatusFailed)
	}
}

// TestActivityBridge_ResumeReusesExecution proves the resume idempotency
// property: re-calling StartStep for a step that already has an open
// Execution does NOT mint a second Execution (the cutover engine resumes
// across Pod restarts and re-emits transitions).
func TestActivityBridge_ResumeReusesExecution(t *testing.T) {
	st, ab, depID := newActivityFixture(t)
	base := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)

	step := ActivityStep{Slug: "egress-block-test", Order: 1}
	if err := ab.SeedSteps([]ActivityStep{step}); err != nil {
		t.Fatalf("SeedSteps: %v", err)
	}
	if err := ab.StartStep(step, "first start", base); err != nil {
		t.Fatalf("StartStep#1: %v", err)
	}
	// Simulate a Pod restart: a NEW bridge over the SAME store (in-memory
	// cursor lost). The persisted LatestExecutionID must be reused.
	ab2 := NewActivityBridge(st, depID, GroupCutover, GroupCutoverDisplay)
	if err := ab2.SeedSteps([]ActivityStep{step}); err != nil {
		t.Fatalf("SeedSteps#2: %v", err)
	}
	if err := ab2.StartStep(step, "resumed start", base.Add(time.Minute)); err != nil {
		t.Fatalf("StartStep#2 (resume): %v", err)
	}

	_, execs, err := st.GetJob(depID, ActivityStepJobName(GroupCutover, "egress-block-test"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("executions after resume = %d, want 1 (resume must reuse the open Execution, not mint a second)", len(execs))
	}
}

func mustListA(t *testing.T, st *Store, depID string) []Job {
	t.Helper()
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	return got
}
