package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedRuns seeds one reconciler observation through the bridge, the way
// the handler's seed does, and returns the store.
func seedRuns(t *testing.T, o ReconcilerObservation) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := NewBridge(st, "dep-1").SeedReconcilerObservations([]ReconcilerObservation{o}); err != nil {
		t.Fatal(err)
	}
	return st
}

// leaf returns the leaf Job and its Executions.
func leaf(t *testing.T, st *Store, jobName string) (Job, []Execution) {
	t.Helper()
	j, execs, err := st.GetJob("dep-1", JobID("dep-1", jobName))
	if err != nil {
		t.Fatal(err)
	}
	return j, execs
}

func TestCloseJobRunClosesAVanishedTaskRun(t *testing.T) {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	st := seedRuns(t, ReconcilerObservation{
		Kind: ObsKindTask, Name: "wp-admin-user", Namespace: "org-a", Status: HelmStateInstalling,
		Executions: []ReconcilerExecutionObservation{{Name: "wp-admin-user", Status: HelmStateInstalling, StartedAt: start, UID: "uid-a"}},
	})
	if j, execs := leaf(t, st, TaskJobPrefix+"wp-admin-user"); j.Status != StatusRunning || len(execs) != 1 || execs[0].JobUID != "uid-a" {
		t.Fatalf("seeded leaf %+v runs %+v", j, execs)
	}
	end := start.Add(2 * time.Minute)
	closed, err := st.CloseJobRun("dep-1", "uid-a", StatusSucceeded, end)
	if err != nil || !closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
	j, execs := leaf(t, st, TaskJobPrefix+"wp-admin-user")
	if execs[0].Status != StatusSucceeded || execs[0].FinishedAt == nil || !execs[0].FinishedAt.Equal(end) {
		t.Fatalf("run %+v", execs[0])
	}
	if j.Status != StatusSucceeded {
		t.Fatalf("leaf status %q, want succeeded", j.Status)
	}
	// A second close of the same run changes nothing.
	if closed, err := st.CloseJobRun("dep-1", "uid-a", StatusFailed, end); err != nil || closed {
		t.Fatalf("second close: closed=%v err=%v", closed, err)
	}
}

func TestCloseJobRunKeysByUID(t *testing.T) {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	st := seedRuns(t, ReconcilerObservation{
		Kind: ObsKindCron, Name: "snapshot", Namespace: "openbao", Status: HelmStateInstalling,
		Executions: []ReconcilerExecutionObservation{
			{Name: "snapshot-1", Status: HelmStateInstalling, StartedAt: start, UID: "uid-1"},
			{Name: "snapshot-2", Status: HelmStateInstalling, StartedAt: start.Add(time.Hour), UID: "uid-2"},
		},
	})
	if closed, err := st.CloseJobRun("dep-1", "uid-1", StatusFailed, start.Add(time.Minute)); err != nil || !closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
	_, execs := leaf(t, st, CronJobPrefix+"snapshot")
	for _, e := range execs {
		switch e.JobUID {
		case "uid-1":
			if e.Status != StatusFailed {
				t.Fatalf("closed run %+v", e)
			}
		case "uid-2":
			if e.Status != StatusRunning || e.FinishedAt != nil {
				t.Fatalf("other run changed: %+v", e)
			}
		default:
			t.Fatalf("run without the expected uid: %+v", e)
		}
	}
}

func TestCloseJobRunCronHeadlineIsTheNewestRun(t *testing.T) {
	start := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	st := seedRuns(t, ReconcilerObservation{
		Kind: ObsKindCron, Name: "backup", Namespace: "cnpg", Status: HelmStateInstalled,
		Executions: []ReconcilerExecutionObservation{
			{Name: "backup-1", Status: HelmStateInstalling, StartedAt: start, UID: "uid-old"},
			{Name: "backup-2", Status: HelmStateInstalled, StartedAt: start.Add(time.Hour), FinishedAt: start.Add(time.Hour + time.Minute), UID: "uid-new"},
		},
	})
	// The old run ends Failed before the newer run finished: the headline
	// stays on the newer run, it does not take the closed run's status.
	if closed, err := st.CloseJobRun("dep-1", "uid-old", StatusFailed, start.Add(time.Minute)); err != nil || !closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
	if j, _ := leaf(t, st, CronJobPrefix+"backup"); j.Status != StatusSucceeded {
		t.Fatalf("leaf %q", j.Status)
	}
}

func TestCloseJobRunKeepsAnInstallLeafStatus(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertJob(Job{DeploymentID: "dep-1", JobName: JobNamePrefix + "openbao", Status: StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.StartJobRun("dep-1", JobNamePrefix+"openbao", "uid-hook", time.Now()); err != nil {
		t.Fatal(err)
	}
	// StartJobRun marks the leaf running, as StartExecution always has.
	if err := st.UpsertJob(Job{DeploymentID: "dep-1", JobName: JobNamePrefix + "openbao", Status: StatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	if closed, err := st.CloseJobRun("dep-1", "uid-hook", StatusFailed, time.Now()); err != nil || !closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
	j, execs := leaf(t, st, JobNamePrefix+"openbao")
	if execs[0].Status != StatusFailed {
		t.Fatalf("hook run %+v", execs[0])
	}
	if j.Status != StatusSucceeded {
		t.Fatalf("install leaf %q, want it unchanged", j.Status)
	}
}

func TestCloseJobRunChangesNothingWithoutAMatch(t *testing.T) {
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if closed, err := st.CloseJobRun("dep-unknown", "uid-a", StatusSucceeded, time.Now()); err != nil || closed {
		t.Fatalf("unknown deployment: closed=%v err=%v", closed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dep-unknown")); !os.IsNotExist(err) {
		t.Fatalf("a directory was created for an unknown deployment: %v", err)
	}
	if err := st.UpsertJob(Job{DeploymentID: "dep-1", JobName: "task-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.StartExecution("dep-1", "task-a", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{"", "uid-other"} {
		if closed, err := st.CloseJobRun("dep-1", uid, StatusSucceeded, time.Now()); err != nil || closed {
			t.Fatalf("uid %q: closed=%v err=%v", uid, closed, err)
		}
	}
	if _, err := st.CloseJobRun("dep-1", "uid-a", StatusRunning, time.Now()); err == nil {
		t.Fatal("a non-terminal status was accepted")
	}
}

func TestCloseJobRunPublishesExecutionFinished(t *testing.T) {
	st := seedRuns(t, ReconcilerObservation{
		Kind: ObsKindTask, Name: "migrate", Namespace: "org-a", Status: HelmStateInstalling,
		Executions: []ReconcilerExecutionObservation{{Name: "migrate", Status: HelmStateInstalling, StartedAt: time.Now(), UID: "uid-m"}},
	})
	ch, cancel := st.Subscribe("dep-1")
	defer cancel()
	if closed, err := st.CloseJobRun("dep-1", "uid-m", StatusFailed, time.Now()); err != nil || !closed {
		t.Fatalf("closed=%v err=%v", closed, err)
	}
	if c := nextChange(t, ch); c.Kind != ExecutionFinished || c.JobID != JobID("dep-1", TaskJobPrefix+"migrate") || c.ExecutionID == "" {
		t.Fatalf("got %+v", c)
	}
}
