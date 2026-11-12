// reconciler_bridge_vanished_runs_test.go — the store half of the "Jobs page
// says Running forever" defect (hw307, 2026-11-12).
//
// A WordPress install was Ready in 2m41s, yet the Jobs page showed its
// admin-user and initdb tasks "Running" half an hour later. Both Jobs had
// completed and been deleted (Helm hook-succeeded; CloudNativePG cleanup), so
// every later poll simply stopped seeing them and the in-flight Execution the
// first poll wrote was never terminated.
//
// Every case below runs TWO polls — first the run in flight, then the run gone
// — because one poll that already carries a terminal status passes on the
// unfixed code and proves nothing. Each verdict rule has a control that must
// NOT fire.
package jobs

import (
	"strings"
	"testing"
	"time"
)

var (
	vanStart = time.Date(2026, 11, 12, 8, 10, 40, 0, time.UTC)
	vanEnd   = time.Date(2026, 11, 12, 8, 11, 35, 0, time.UTC)
	vanNow   = time.Date(2026, 11, 12, 8, 40, 0, 0, time.UTC)
)

// pollInFlight is poll 1: the Job exists and is running.
func pollInFlight(t *testing.T, b *Bridge, kind, leaf, run string, removedOnSuccess bool) {
	t.Helper()
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: kind, Name: leaf, Namespace: "hw307-omani-works",
		Status: HelmStateInstalling,
		Executions: []ReconcilerExecutionObservation{{
			Name: run, Status: HelmStateInstalling, StartedAt: vanStart,
			Message: "run " + run + ": installing", RemovedOnSuccess: removedOnSuccess,
		}},
	}}); err != nil {
		t.Fatalf("poll 1: %v", err)
	}
}

func leafStatus(t *testing.T, st *Store, jobName string) string {
	t.Helper()
	j, _, err := st.GetJob("dep-recon", jobName)
	if err != nil {
		t.Fatalf("GetJob %s: %v", jobName, err)
	}
	return j.Status
}

func lastLogLine(t *testing.T, st *Store, e Execution) string {
	t.Helper()
	page, err := st.PageLogs("dep-recon", e.ID, 1, 100)
	if err != nil || len(page.Lines) == 0 {
		t.Fatalf("logs for %s: %v", e.ID, err)
	}
	return page.Lines[len(page.Lines)-1].Message
}

// The live case: the run is gone, its Completed event is on record.
func TestCloseVanishedRuns_CompletedEventClosesTheRun(t *testing.T) {
	b, st := newTestBridge(t)
	run := "wp-bp-wordpress-tenant-admin-user"
	pollInFlight(t, b, ObsKindTask, run, run, false)
	if got := leafStatus(t, st, "task-"+run); got != StatusRunning {
		t.Fatalf("after poll 1 the leaf must be running (the reproduction), got %q", got)
	}

	n, err := b.CloseVanishedRuns(RunLedger{
		Listed: true, Present: map[string]bool{}, EventsListed: true,
		Ended: map[string]RunEnd{run: {Status: StatusSucceeded, At: vanEnd, Reason: "Completed"}},
	}, vanNow)
	if err != nil || n != 1 {
		t.Fatalf("CloseVanishedRuns = %d, %v; want 1 closed", n, err)
	}
	if got := leafStatus(t, st, "task-"+run); got != StatusSucceeded {
		t.Errorf("leaf = %q, want succeeded", got)
	}
	e := execByRunName(t, st, "dep-recon", "task-"+run, run)
	if e.Status != StatusSucceeded || e.FinishedAt == nil || !e.FinishedAt.Equal(vanEnd) {
		t.Errorf("execution = %s finished %v; want succeeded at the event time %v", e.Status, e.FinishedAt, vanEnd)
	}
	if l := lastLogLine(t, st, e); !strings.Contains(l, "Completed") {
		t.Errorf("closing log line must name the evidence, got %q", l)
	}
}

// CONTROL: a run that is still listed is never touched, whatever the events say.
func TestCloseVanishedRuns_PresentRunUntouched(t *testing.T) {
	b, st := newTestBridge(t)
	run := "wordpress-db-replica-1-pgbasebackup"
	pollInFlight(t, b, ObsKindTask, run, run, false)
	n, _ := b.CloseVanishedRuns(RunLedger{
		Listed: true, Present: map[string]bool{run: true}, EventsListed: true,
		Ended: map[string]RunEnd{run: {Status: StatusFailed, At: vanEnd, Reason: "BackoffLimitExceeded"}},
	}, vanNow)
	if n != 0 || leafStatus(t, st, "task-"+run) != StatusRunning {
		t.Errorf("a listed run must stay as observed: closed=%d status=%s", n, leafStatus(t, st, "task-"+run))
	}
}

// CONTROL: when the Job LIST failed, absence proves nothing.
func TestCloseVanishedRuns_NothingConcludedWithoutAJobList(t *testing.T) {
	b, st := newTestBridge(t)
	pollInFlight(t, b, ObsKindTask, "x", "x", false)
	if n, _ := b.CloseVanishedRuns(RunLedger{Listed: false, EventsListed: true}, vanNow); n != 0 {
		t.Errorf("closed %d runs without a Job LIST", n)
	}
	if leafStatus(t, st, "task-x") != StatusRunning {
		t.Error("leaf changed without a Job LIST")
	}
}

// A completion event from an EARLIER run of the same Job name (hooks reuse
// names) is not evidence about this run.
func TestCloseVanishedRuns_EventOlderThanTheRunIsIgnored(t *testing.T) {
	b, st := newTestBridge(t)
	run := "wp-bp-wordpress-tenant-oidc-config"
	pollInFlight(t, b, ObsKindTask, run, run, false)
	n, _ := b.CloseVanishedRuns(RunLedger{
		Listed: true, Present: map[string]bool{}, EventsListed: false,
		Ended: map[string]RunEnd{run: {Status: StatusSucceeded, At: vanStart.Add(-2 * time.Hour), Reason: "Completed"}},
	}, vanNow)
	if n != 0 || leafStatus(t, st, "task-"+run) != StatusRunning {
		t.Errorf("a stale event closed the run: closed=%d status=%s", n, leafStatus(t, st, "task-"+run))
	}
}

// Events expire after about an hour; a Helm hook that deletes itself only on
// success still carries its verdict in the run's seed line.
func TestCloseVanishedRuns_HelmDeletedOnSuccessWithoutEvent(t *testing.T) {
	b, st := newTestBridge(t)
	run := "accept-1794392753-bp-wordpress-tenant-oidc-config"
	pollInFlight(t, b, ObsKindTask, run, run, true)
	n, err := b.CloseVanishedRuns(RunLedger{Listed: true, Present: map[string]bool{}, EventsListed: true}, vanNow)
	if err != nil || n != 1 {
		t.Fatalf("closed=%d err=%v; want 1", n, err)
	}
	if got := leafStatus(t, st, "task-"+run); got != StatusSucceeded {
		t.Errorf("leaf = %q, want succeeded (Helm deletes this Job only on success)", got)
	}
}

// No event and no hook evidence: the run did not verifiably finish. It must
// stop reading Running, and say why — but only when the event read worked.
func TestCloseVanishedRuns_NoEvidenceClosesFailedOnlyWhenEventsWereRead(t *testing.T) {
	b, st := newTestBridge(t)
	run := "accept-1794381045-rtz-a-bp-wordpress-tenant-oidc-config"
	pollInFlight(t, b, ObsKindTask, run, run, false)

	// CONTROL: event read failed → leave it.
	if n, _ := b.CloseVanishedRuns(RunLedger{Listed: true, Present: map[string]bool{}, EventsListed: false}, vanNow); n != 0 {
		t.Fatalf("closed %d runs while the event LIST had failed", n)
	}
	if leafStatus(t, st, "task-"+run) != StatusRunning {
		t.Fatal("leaf changed while the event LIST had failed")
	}

	n, err := b.CloseVanishedRuns(RunLedger{Listed: true, Present: map[string]bool{}, EventsListed: true}, vanNow)
	if err != nil || n != 1 {
		t.Fatalf("closed=%d err=%v; want 1", n, err)
	}
	if got := leafStatus(t, st, "task-"+run); got != StatusFailed {
		t.Errorf("leaf = %q, want failed", got)
	}
	e := execByRunName(t, st, "dep-recon", "task-"+run, run)
	if l := lastLogLine(t, st, e); !strings.Contains(l, "no completion event") {
		t.Errorf("log line must say why, got %q", l)
	}
	// Idempotent: a second sweep finds nothing left to close.
	if n, _ := b.CloseVanishedRuns(RunLedger{Listed: true, Present: map[string]bool{}, EventsListed: true}, vanNow); n != 0 {
		t.Errorf("second sweep closed %d runs again", n)
	}
}

// A CronJob prunes old runs. A pruned run that never got seen terminating is
// closed, and the leaf headline is re-resolved from ALL runs (newest wins),
// not left on whichever run the sweep closed last.
func TestCloseVanishedRuns_CronHeadlineReResolved(t *testing.T) {
	b, st := newTestBridge(t)
	older, newer := "snap-29907000", "snap-29907060"
	if _, _, err := b.SeedReconcilerObservations([]ReconcilerObservation{{
		Kind: ObsKindCron, Name: "snap", Namespace: "openbao", Status: HelmStateInstalled,
		Executions: []ReconcilerExecutionObservation{
			{Name: older, Status: HelmStateInstalling, StartedAt: vanStart},
			{Name: newer, Status: HelmStateInstalled, StartedAt: vanStart.Add(time.Hour), FinishedAt: vanStart.Add(time.Hour + time.Minute)},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	n, err := b.CloseVanishedRuns(RunLedger{
		Listed: true, Present: map[string]bool{newer: true}, EventsListed: true,
		Ended: map[string]RunEnd{older: {Status: StatusFailed, At: vanEnd, Reason: "BackoffLimitExceeded"}},
	}, vanNow)
	if err != nil || n != 1 {
		t.Fatalf("closed=%d err=%v; want 1", n, err)
	}
	if e := execByRunName(t, st, "dep-recon", "cron-snap", older); e.Status != StatusFailed {
		t.Errorf("pruned run = %s, want failed (its event says BackoffLimitExceeded)", e.Status)
	}
	if got := leafStatus(t, st, "cron-snap"); got != StatusSucceeded {
		t.Errorf("cron headline = %q, want succeeded (the newest run succeeded after the old failure)", got)
	}
}
