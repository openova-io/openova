// import_test.go — handover snapshot import merge semantics
// (Refs #3263 #3277).
//
// What this file proves (chroot-side jobs.Store.ImportSnapshot):
//
//  1. A mother snapshot carrying BOTH regions' rows lands in an index
//     that previously held only the chroot's own region-a rows — the
//     region-b rows ("install-<region>:<chart>", Region stamped)
//     materialise, the pre-existing region-a rows survive.
//  2. Re-importing the same snapshot is a no-op (idempotence).
//  3. A terminal local row is NEVER regressed by a non-terminal
//     incoming row.
//  4. Executions append/fill-forward but never alter a terminal row.
package jobs

import (
	"testing"
	"time"
)

func ts(s string) *time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	t = t.UTC()
	return &t
}

// seedChrootRegionA writes the chroot-side pre-state: its own
// bootstrap watch has already recorded a terminal region-a install.
func seedChrootRegionA(t *testing.T, st *Store, depID string) {
	t.Helper()
	j := Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		Type:         JobTypeInstall,
		ParentID:     JobID(depID, GroupBootstrapKit),
		DependsOn:    []string{"install-flux"},
		Status:       StatusSucceeded,
		StartedAt:    ts("2026-06-11T10:00:00Z"),
		FinishedAt:   ts("2026-06-11T10:02:00Z"),
	}
	if err := st.UpsertJob(j); err != nil {
		t.Fatalf("seed UpsertJob: %v", err)
	}
}

// motherSnapshot builds the mother-side payload shape: the same
// region-a row PLUS a region-b row + its latest execution summary.
func motherSnapshot(depID string) ([]Job, []Execution) {
	jobsIn := []Job{
		{
			DeploymentID: depID,
			JobName:      "install-cilium",
			AppID:        "cilium",
			Type:         JobTypeInstall,
			ParentID:     JobID(depID, GroupBootstrapKit),
			DependsOn:    []string{"install-flux"},
			Status:       StatusSucceeded,
			StartedAt:    ts("2026-06-11T10:00:00Z"),
			FinishedAt:   ts("2026-06-11T10:02:00Z"),
		},
		{
			DeploymentID:      depID,
			JobName:           "install-me-east-215-b-1:cilium",
			AppID:             "me-east-215-b-1:cilium",
			Region:            "me-east-215-b-1",
			Type:              JobTypeInstall,
			ParentID:          JobID(depID, GroupBootstrapKit),
			DependsOn:         []string{"install-me-east-215-b-1:flux"},
			Status:            StatusSucceeded,
			StartedAt:         ts("2026-06-11T10:05:00Z"),
			FinishedAt:        ts("2026-06-11T10:08:00Z"),
			LatestExecutionID: "exec-b-cilium",
		},
	}
	execsIn := []Execution{
		{
			ID:           "exec-b-cilium",
			JobID:        JobID(depID, "install-me-east-215-b-1:cilium"),
			DeploymentID: depID,
			Status:       StatusSucceeded,
			StartedAt:    *ts("2026-06-11T10:05:00Z"),
			FinishedAt:   ts("2026-06-11T10:08:00Z"),
			LineCount:    12,
		},
	}
	return jobsIn, execsIn
}

func findJob(jobsOut []Job, id string) *Job {
	for i := range jobsOut {
		if jobsOut[i].ID == id {
			return &jobsOut[i]
		}
	}
	return nil
}

func TestImportSnapshot_AddsRegionBPreservesRegionA(t *testing.T) {
	st := newTestStore(t)
	depID := "hw130dep"
	seedChrootRegionA(t, st, depID)

	jobsIn, execsIn := motherSnapshot(depID)
	jApplied, eApplied, err := st.ImportSnapshot(depID, jobsIn, execsIn)
	if err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}
	if jApplied != 2 {
		t.Errorf("jobsApplied = %d, want 2 (region-a merge + region-b insert)", jApplied)
	}
	if eApplied != 1 {
		t.Errorf("execsApplied = %d, want 1", eApplied)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	a := findJob(got, JobID(depID, "install-cilium"))
	if a == nil {
		t.Fatalf("region-a row missing after import: %+v", got)
	}
	if a.Status != StatusSucceeded {
		t.Errorf("region-a status = %q, want succeeded", a.Status)
	}
	b := findJob(got, JobID(depID, "install-me-east-215-b-1:cilium"))
	if b == nil {
		t.Fatalf("region-b row missing after import: %+v", got)
	}
	if b.Region != "me-east-215-b-1" {
		t.Errorf("region-b Region = %q, want me-east-215-b-1 (the Region column keys off this)", b.Region)
	}
	if b.Status != StatusSucceeded {
		t.Errorf("region-b status = %q, want succeeded", b.Status)
	}

	// The imported execution must be resolvable for the /logs deep-link.
	exec, err := st.FindExecution(depID, "exec-b-cilium")
	if err != nil {
		t.Fatalf("FindExecution(exec-b-cilium): %v", err)
	}
	if exec.Status != StatusSucceeded || exec.LineCount != 12 {
		t.Errorf("imported execution = %+v, want succeeded/12 lines", exec)
	}
}

func TestImportSnapshot_Idempotent(t *testing.T) {
	st := newTestStore(t)
	depID := "hw130dep"
	seedChrootRegionA(t, st, depID)

	jobsIn, execsIn := motherSnapshot(depID)
	if _, _, err := st.ImportSnapshot(depID, jobsIn, execsIn); err != nil {
		t.Fatalf("first ImportSnapshot: %v", err)
	}
	first, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs after first import: %v", err)
	}

	// Re-import the SAME snapshot — counts may report merges but the
	// resulting view must be unchanged.
	if _, _, err := st.ImportSnapshot(depID, jobsIn, execsIn); err != nil {
		t.Fatalf("second ImportSnapshot: %v", err)
	}
	second, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs after second import: %v", err)
	}
	if len(first) != len(second) {
		t.Fatalf("job count changed on re-import: %d → %d", len(first), len(second))
	}
	for i := range first {
		a, b := first[i], second[i]
		if a.ID != b.ID || a.Status != b.Status || a.Region != b.Region ||
			a.DurationMs != b.DurationMs || a.LatestExecutionID != b.LatestExecutionID {
			t.Errorf("row %q changed on re-import: %+v → %+v", a.ID, a, b)
		}
	}
	execs, err := st.ListExecutions(depID)
	if err != nil {
		t.Fatalf("ListExecutions: %v", err)
	}
	if len(execs) != 1 {
		t.Errorf("executions duplicated on re-import: %d, want 1", len(execs))
	}
}

func TestImportSnapshot_NeverRegressesTerminalRow(t *testing.T) {
	st := newTestStore(t)
	depID := "hw130dep"
	seedChrootRegionA(t, st, depID) // terminal succeeded locally

	stale := []Job{{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		Type:         JobTypeInstall,
		ParentID:     JobID(depID, GroupBootstrapKit),
		Status:       StatusRunning, // stale mother view: still running
		StartedAt:    ts("2026-06-11T10:00:00Z"),
	}}
	jApplied, _, err := st.ImportSnapshot(depID, stale, nil)
	if err != nil {
		t.Fatalf("ImportSnapshot: %v", err)
	}
	if jApplied != 0 {
		t.Errorf("jobsApplied = %d, want 0 (terminal row must be skipped)", jApplied)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	a := findJob(got, JobID(depID, "install-cilium"))
	if a == nil {
		t.Fatalf("region-a row vanished: %+v", got)
	}
	if a.Status != StatusSucceeded {
		t.Errorf("terminal row regressed: status = %q, want succeeded", a.Status)
	}
	if a.FinishedAt == nil {
		t.Errorf("terminal row lost FinishedAt")
	}
}

func TestImportSnapshot_ExecutionsNeverAlterTerminal(t *testing.T) {
	st := newTestStore(t)
	depID := "hw130dep"
	_, execsIn := motherSnapshot(depID)

	// First import lands the terminal execution.
	if _, _, err := st.ImportSnapshot(depID, nil, execsIn); err != nil {
		t.Fatalf("first ImportSnapshot: %v", err)
	}
	// A stale re-send claiming the execution is still running must not
	// regress it.
	stale := []Execution{{
		ID:           "exec-b-cilium",
		JobID:        JobID(depID, "install-me-east-215-b-1:cilium"),
		DeploymentID: depID,
		Status:       StatusRunning,
		StartedAt:    *ts("2026-06-11T10:05:00Z"),
		LineCount:    3,
	}}
	_, eApplied, err := st.ImportSnapshot(depID, nil, stale)
	if err != nil {
		t.Fatalf("second ImportSnapshot: %v", err)
	}
	if eApplied != 0 {
		t.Errorf("execsApplied = %d, want 0 (terminal execution must be skipped)", eApplied)
	}
	exec, err := st.FindExecution(depID, "exec-b-cilium")
	if err != nil {
		t.Fatalf("FindExecution: %v", err)
	}
	if exec.Status != StatusSucceeded || exec.LineCount != 12 || exec.FinishedAt == nil {
		t.Errorf("terminal execution altered: %+v", exec)
	}
}

func TestImportSnapshot_ExecutionFillsForwardNonTerminal(t *testing.T) {
	st := newTestStore(t)
	depID := "hw130dep"

	running := []Execution{{
		ID:           "exec-b-cilium",
		JobID:        JobID(depID, "install-me-east-215-b-1:cilium"),
		DeploymentID: depID,
		Status:       StatusRunning,
		StartedAt:    *ts("2026-06-11T10:05:00Z"),
		LineCount:    3,
	}}
	if _, _, err := st.ImportSnapshot(depID, nil, running); err != nil {
		t.Fatalf("first ImportSnapshot: %v", err)
	}
	_, execsIn := motherSnapshot(depID) // terminal version of the same exec
	_, eApplied, err := st.ImportSnapshot(depID, nil, execsIn)
	if err != nil {
		t.Fatalf("second ImportSnapshot: %v", err)
	}
	if eApplied != 1 {
		t.Errorf("execsApplied = %d, want 1 (non-terminal → terminal fill-forward)", eApplied)
	}
	exec, err := st.FindExecution(depID, "exec-b-cilium")
	if err != nil {
		t.Fatalf("FindExecution: %v", err)
	}
	if exec.Status != StatusSucceeded || exec.FinishedAt == nil {
		t.Errorf("execution not filled forward: %+v", exec)
	}
}
