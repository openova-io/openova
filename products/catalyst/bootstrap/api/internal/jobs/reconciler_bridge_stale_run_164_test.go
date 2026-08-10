// reconciler_bridge_stale_run_164_test.go — UAT row 164, the STORE half.
//
// Row 164's clause: "the cutover group status reflects its real children (a
// group with a failed child reads failed, not a fake Succeeded)".
//
// #5931 fixed the INGESTION half in helmwatch/reconcilers.go — adoptRunHeadline
// makes ListReconcilerObservations emit `failed` for a collapsed leaf whose
// newest run has not terminated but whose older run failed. That fix stops one
// layer short of the wire: the observation is correct and the STORE still ends
// up green/optimistic, for two distinct reasons this file pins.
//
// DEFECT A — the cron leaf has no headline re-assert.
// OnReconcilerObservation upserts the producer's recency-resolved status BEFORE
// the switch, then seedCronExecutionsLocked walks the runs in LIST order.
// StartExecution stamps Job.Status=running on every allocation, so the
// LAST-processed run leaves the headline. The ObsKindTask branch already
// re-asserts the producer status afterwards, with a comment naming exactly this
// hazard (reconciler_bridge.go:208-212); the ObsKindCron branch does not. A cron
// whose newest run is still in flight therefore reads `running` in the store
// even when an older run failed — the group inherits it and reads `running`.
//
// DEFECT B — an already-recorded run is never terminated.
// seedCronExecutionsLocked dedupes by run name and leaves an already-recorded
// run's Execution untouched. A run first observed IN FLIGHT is written by
// StartExecution as running/FinishedAt=nil; when that same run later fails, the
// dedupe skips it and the Execution reads `running, finishedAt: null` forever.
// That is the hw292 observation verbatim: Job syft-grype-...-29763030 Failed
// with BackoffLimitExceeded, while its leaf execution carried the IDENTICAL
// startedAt and read running, and the reconcilers group reported 0 failed
// leaves out of 183.
//
// WHY THE OBVIOUS TEST PROVES NOTHING. Feeding ONE terminal run through in a
// single poll passes on the unfixed code — StartExecution then FinishExecution
// both land in the same call. Every case below therefore models the real shape
// (a failure collapsed with a run that has not terminated, or a run observed
// twice across polls), and each carries a CONTROL that answers the other way so
// the suite cannot be "simplified" back into a vacuous pass.
package jobs

import (
	"testing"
	"time"
)

// Live timings from the hw292 pair the row was measured on.
var (
	row164FailedStart  = time.Date(2026, 8, 3, 20, 9, 12, 0, time.UTC)
	row164FailedFinish = time.Date(2026, 8, 3, 20, 11, 32, 0, time.UTC)
	row164RunningStart = time.Date(2026, 8, 8, 18, 34, 3, 0, time.UTC)
)

// execByRunName returns the Execution whose seed log line tags the given run
// name, so a case can assert on ONE run rather than on list position.
func execByRunName(t *testing.T, st *Store, depID, jobName, runName string) Execution {
	t.Helper()
	_, execs, err := st.GetJob(depID, jobName)
	if err != nil {
		t.Fatalf("GetJob %q: %v", jobName, err)
	}
	for _, e := range execs {
		page, perr := st.PageLogs(depID, e.ID, 1, 1)
		if perr != nil || len(page.Lines) == 0 {
			continue
		}
		if runNameFromLogLine(page.Lines[0].Message) == runName {
			return e
		}
	}
	t.Fatalf("no execution for run %q on leaf %q (%d executions present)", runName, jobName, len(execs))
	return Execution{}
}

// TestReconcilerBridge_CronHeadlineNotBuriedByUnterminatedRun is DEFECT A.
//
// The producer has already resolved the headline to `failed` (that is #5931's
// job). The runs arrive failed-first, in-flight-second — the order the
// apiserver returned them on hw292 — so the in-flight run is processed LAST and
// its StartExecution stamp is what survives on the unfixed code.
func TestReconcilerBridge_CronHeadlineNotBuriedByUnterminatedRun(t *testing.T) {
	b, st := newTestBridge(t)

	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindCron, Name: "openbao-snapshot-save", Namespace: "openbao",
		// Producer verdict, recency-resolved upstream by adoptRunHeadline.
		Status: HelmStateFailed,
		Executions: []ReconcilerExecutionObservation{
			{
				Name: "openbao-snapshot-save-29763030", Status: HelmStateFailed,
				StartedAt: row164FailedStart, FinishedAt: row164FailedFinish,
				Message: "BackoffLimitExceeded",
			},
			{
				// Still in flight — no FinishedAt. Processed LAST.
				Name: "openbao-snapshot-save-29770230", Status: HelmStateInstalling,
				StartedAt: row164RunningStart,
				Message:   "run in flight",
			},
		},
	}}); err != nil {
		t.Fatalf("seed cron: %v", err)
	}

	all, err := st.ListJobs("dep-recon")
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	leaf, ok := findByName(all, "cron-openbao-snapshot-save")
	if !ok {
		t.Fatal("cron-openbao-snapshot-save missing")
	}
	if leaf.Status != StatusFailed {
		t.Errorf("cron leaf status: want %q (an in-flight run must not bury a recorded failure), got %q",
			StatusFailed, leaf.Status)
	}

	grp, ok := findByName(all, GroupReconcilers)
	if !ok {
		t.Fatal("reconcilers group missing")
	}
	if grp.Status != StatusFailed {
		t.Errorf("reconcilers group status: want %q (a group with a failed child reads failed), got %q",
			StatusFailed, grp.Status)
	}
}

// TestReconcilerBridge_TaskHeadlineNotBuriedByUnterminatedRun is the CONTROL
// for DEFECT A: the IDENTICAL run shape on a task leaf already passes, because
// the ObsKindTask branch re-asserts the producer status. It locates the defect
// in the cron branch specifically, and fails loudly if someone "simplifies" the
// task branch's re-assert away.
func TestReconcilerBridge_TaskHeadlineNotBuriedByUnterminatedRun(t *testing.T) {
	b, st := newTestBridge(t)

	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindTask, Name: "syft-sbom", Namespace: "syft-grype",
		Status: HelmStateFailed,
		Executions: []ReconcilerExecutionObservation{
			{
				Name: "syft-grype-bp-syft-grype-29763030", Status: HelmStateFailed,
				StartedAt: row164FailedStart, FinishedAt: row164FailedFinish,
				Message: "BackoffLimitExceeded",
			},
			{
				Name: "syft-grype-bp-syft-grype-29770230", Status: HelmStateInstalling,
				StartedAt: row164RunningStart,
				Message:   "run in flight",
			},
		},
	}}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	all, _ := st.ListJobs("dep-recon")
	leaf, ok := findByName(all, "task-syft-sbom")
	if !ok {
		t.Fatal("task-syft-sbom missing")
	}
	if leaf.Status != StatusFailed {
		t.Errorf("task leaf status: want %q, got %q (the ObsKindTask re-assert regressed)",
			StatusFailed, leaf.Status)
	}
}

// TestReconcilerBridge_RecordedRunTerminatesOnLaterPoll is DEFECT B — the
// row-164 observation verbatim. Poll 1 sees the run in flight; poll 2 sees the
// SAME run failed. The dedupe must not treat "already recorded" as "done".
func TestReconcilerBridge_RecordedRunTerminatesOnLaterPoll(t *testing.T) {
	b, st := newTestBridge(t)
	const runName = "syft-grype-bp-syft-grype-29763030"

	// Poll 1 — in flight.
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindCron, Name: "syft-grype-bp-syft-grype", Namespace: "syft-grype",
		Status: HelmStateInstalling,
		Executions: []ReconcilerExecutionObservation{{
			Name: runName, Status: HelmStateInstalling,
			StartedAt: row164FailedStart, Message: "run in flight",
		}},
	}}); err != nil {
		t.Fatalf("poll 1: %v", err)
	}

	// Poll 2 — the SAME run, now terminally failed.
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindCron, Name: "syft-grype-bp-syft-grype", Namespace: "syft-grype",
		Status: HelmStateFailed,
		Executions: []ReconcilerExecutionObservation{{
			Name: runName, Status: HelmStateFailed,
			StartedAt: row164FailedStart, FinishedAt: row164FailedFinish,
			Message: "BackoffLimitExceeded",
		}},
	}}); err != nil {
		t.Fatalf("poll 2: %v", err)
	}

	// Exactly one Execution — re-observing a run must not duplicate it.
	_, execs, err := st.GetJob("dep-recon", "cron-syft-grype-bp-syft-grype")
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("executions: want 1 (re-observation must not duplicate the run), got %d", len(execs))
	}

	run := execByRunName(t, st, "dep-recon", "cron-syft-grype-bp-syft-grype", runName)
	if run.Status != StatusFailed {
		t.Errorf("execution status: want %q, got %q — a run that terminated between polls is never terminated in the store",
			StatusFailed, run.Status)
	}
	if run.FinishedAt == nil {
		t.Errorf("execution finishedAt: want %s, got nil — the failed run reads as still in flight forever",
			row164FailedFinish.Format(time.RFC3339))
	} else if !run.FinishedAt.Equal(row164FailedFinish) {
		t.Errorf("execution finishedAt: want %s, got %s",
			row164FailedFinish.Format(time.RFC3339), run.FinishedAt.Format(time.RFC3339))
	}

	all, _ := st.ListJobs("dep-recon")
	grp, ok := findByName(all, GroupReconcilers)
	if !ok {
		t.Fatal("reconcilers group missing")
	}
	if grp.Status != StatusFailed {
		t.Errorf("reconcilers group status: want %q, got %q", StatusFailed, grp.Status)
	}
}

// TestReconcilerBridge_RecordedRunStaysRunningWhileInFlight is the CONTROL for
// DEFECT B, answering the other way: a run observed twice that has NOT
// terminated must still read running with a nil FinishedAt. It proves the fix
// terminates on the run's real verdict rather than terminating everything it
// re-sees.
func TestReconcilerBridge_RecordedRunStaysRunningWhileInFlight(t *testing.T) {
	b, st := newTestBridge(t)
	const runName = "syft-grype-bp-syft-grype-29770230"

	obs := ReconcilerObservation{
		Kind: ObsKindCron, Name: "syft-grype-bp-syft-grype", Namespace: "syft-grype",
		Status: HelmStateInstalling,
		Executions: []ReconcilerExecutionObservation{{
			Name: runName, Status: HelmStateInstalling,
			StartedAt: row164RunningStart, Message: "run in flight",
		}},
	}
	for i, poll := range []string{"poll 1", "poll 2"} {
		if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{obs}); err != nil {
			t.Fatalf("%s (i=%d): %v", poll, i, err)
		}
	}

	run := execByRunName(t, st, "dep-recon", "cron-syft-grype-bp-syft-grype", runName)
	if run.Status != StatusRunning {
		t.Errorf("execution status: want %q for a run still in flight, got %q", StatusRunning, run.Status)
	}
	if run.FinishedAt != nil {
		t.Errorf("execution finishedAt: want nil for a run still in flight, got %s",
			run.FinishedAt.Format(time.RFC3339))
	}
}

// TestReconcilerBridge_RecordedRunTerminatesSucceeded pins that the
// terminate-on-later-poll path carries the run's REAL verdict rather than
// hardcoding failure — a success observed after an in-flight sighting must land
// as succeeded.
func TestReconcilerBridge_RecordedRunTerminatesSucceeded(t *testing.T) {
	b, st := newTestBridge(t)
	const runName = "openbao-snapshot-save-29770230"

	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindCron, Name: "openbao-snapshot-save", Namespace: "openbao",
		Status: HelmStateInstalling,
		Executions: []ReconcilerExecutionObservation{{
			Name: runName, Status: HelmStateInstalling,
			StartedAt: row164RunningStart, Message: "run in flight",
		}},
	}}); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindCron, Name: "openbao-snapshot-save", Namespace: "openbao",
		Status: HelmStateInstalled,
		Executions: []ReconcilerExecutionObservation{{
			Name: runName, Status: HelmStateInstalled,
			StartedAt: row164RunningStart, FinishedAt: row164RunningStart.Add(90 * time.Second),
			Message: "snapshot written",
		}},
	}}); err != nil {
		t.Fatalf("poll 2: %v", err)
	}

	run := execByRunName(t, st, "dep-recon", "cron-openbao-snapshot-save", runName)
	if run.Status != StatusSucceeded {
		t.Errorf("execution status: want %q, got %q", StatusSucceeded, run.Status)
	}
	if run.FinishedAt == nil {
		t.Fatal("execution finishedAt: want non-nil for a terminated run, got nil")
	}

	all, _ := st.ListJobs("dep-recon")
	leaf, ok := findByName(all, "cron-openbao-snapshot-save")
	if !ok {
		t.Fatal("cron-openbao-snapshot-save missing")
	}
	if leaf.Status != StatusSucceeded {
		t.Errorf("cron leaf status: want %q, got %q", StatusSucceeded, leaf.Status)
	}
}
