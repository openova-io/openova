// store_test.go — round-trip + pagination + atomic-write tests for the
// Jobs/Executions store. Tests use t.TempDir() so they run without a
// PVC and clean themselves up.
package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

func TestStore_UpsertJob_RoundTrip(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-1"

	j := Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		BatchID:      BatchBootstrapKit,
		DependsOn:    []string{"install-flux"},
		Status:       StatusPending,
	}
	if err := st.UpsertJob(j); err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 job, got %d", len(got))
	}
	if got[0].ID != JobID(depID, "install-cilium") {
		t.Fatalf("ID mismatch: %q", got[0].ID)
	}
	if got[0].AppID != "cilium" || got[0].BatchID != BatchBootstrapKit {
		t.Fatalf("metadata mismatch: %+v", got[0])
	}
	if len(got[0].DependsOn) != 1 || got[0].DependsOn[0] != "install-flux" {
		t.Fatalf("dependsOn mismatch: %+v", got[0].DependsOn)
	}
}

func TestStore_UpsertJob_MergesMonotonicTimestamps(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-2"

	started := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      "install-foo",
		StartedAt:    &started,
		Status:       StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	// Re-emit without StartedAt — the merge must preserve the prior value.
	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      "install-foo",
		Status:       StatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := st.ListJobs(depID)
	if got[0].StartedAt == nil || !got[0].StartedAt.Equal(started) {
		t.Fatalf("StartedAt clobbered: %+v", got[0].StartedAt)
	}
}

func TestStore_StartAndFinishExecution(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-3"

	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      "install-foo",
		AppID:        "foo",
		BatchID:      BatchBootstrapKit,
		Status:       StatusPending,
	}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().UTC()
	exec, err := st.StartExecution(depID, "install-foo", t0)
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	if exec.ID == "" || exec.Status != StatusRunning {
		t.Fatalf("bad exec: %+v", exec)
	}

	job, execs, err := st.GetJob(depID, JobID(depID, "install-foo"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if job.Status != StatusRunning {
		t.Errorf("Job.Status: want running, got %q", job.Status)
	}
	if job.LatestExecutionID != exec.ID {
		t.Errorf("LatestExecutionID: want %q, got %q", exec.ID, job.LatestExecutionID)
	}
	if len(execs) != 1 {
		t.Fatalf("expected 1 exec, got %d", len(execs))
	}

	t1 := t0.Add(5 * time.Second)
	if err := st.FinishExecution(depID, exec.ID, StatusSucceeded, t1); err != nil {
		t.Fatalf("FinishExecution: %v", err)
	}

	job, _, _ = st.GetJob(depID, JobID(depID, "install-foo"))
	if job.Status != StatusSucceeded {
		t.Errorf("Job.Status: want succeeded, got %q", job.Status)
	}
	if job.FinishedAt == nil {
		t.Fatalf("Job.FinishedAt nil")
	}
	if job.DurationMs != 5000 {
		t.Errorf("DurationMs: want 5000, got %d", job.DurationMs)
	}
}

func TestStore_FinishExecution_RejectsNonTerminal(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertJob(Job{DeploymentID: "d", JobName: "install-x"}); err != nil {
		t.Fatal(err)
	}
	exec, err := st.StartExecution("d", "install-x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishExecution("d", exec.ID, StatusRunning, time.Now()); err == nil {
		t.Fatal("expected error finishing with non-terminal status")
	}
}

func TestStore_FinishExecution_NotFound(t *testing.T) {
	st := newTestStore(t)
	err := st.FinishExecution("d", "no-such-exec", StatusSucceeded, time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStore_AppendLogLines_Pagination(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-pag"
	if err := st.UpsertJob(Job{DeploymentID: depID, JobName: "install-x"}); err != nil {
		t.Fatal(err)
	}
	exec, err := st.StartExecution(depID, "install-x", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// Append 100 lines.
	lines := make([]LogLine, 100)
	for i := range lines {
		lines[i] = LogLine{
			Level:   LevelInfo,
			Message: "line-" + strings.Repeat(".", i%5),
		}
	}
	if err := st.AppendLogLines(depID, exec.ID, lines); err != nil {
		t.Fatal(err)
	}

	page, err := st.PageLogs(depID, exec.ID, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Lines) != 10 || page.Total != 100 {
		t.Fatalf("page1: %+v", page)
	}
	if page.Lines[0].LineNumber != 1 || page.Lines[9].LineNumber != 10 {
		t.Fatalf("LineNumber stamping: %+v", page.Lines)
	}
	if page.ExecutionFinished {
		t.Errorf("ExecutionFinished: want false (still running)")
	}

	// Page 11..20.
	page2, err := st.PageLogs(depID, exec.ID, 11, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Lines) != 10 || page2.Lines[0].LineNumber != 11 {
		t.Fatalf("page2: %+v", page2.Lines)
	}

	// fromLine past total → empty page, executionFinished still false.
	pageEmpty, _ := st.PageLogs(depID, exec.ID, 200, 10)
	if len(pageEmpty.Lines) != 0 {
		t.Errorf("expected empty page, got %d", len(pageEmpty.Lines))
	}

	// Limit > MaxLogPageSize is clamped.
	pageBig, _ := st.PageLogs(depID, exec.ID, 1, 99999)
	if len(pageBig.Lines) > MaxLogPageSize {
		t.Errorf("limit not clamped: got %d", len(pageBig.Lines))
	}

	// Finish exec, executionFinished flips true.
	if err := st.FinishExecution(depID, exec.ID, StatusSucceeded, time.Now()); err != nil {
		t.Fatal(err)
	}
	pageDone, _ := st.PageLogs(depID, exec.ID, 1, 5)
	if !pageDone.ExecutionFinished {
		t.Errorf("ExecutionFinished: want true after FinishExecution")
	}
}

func TestStore_ListJobs_SortStartedAtDescPendingLast(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-sort"

	t0 := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Second)
	t2 := t0.Add(20 * time.Second)

	mkJob := func(name string, start *time.Time, status string) Job {
		return Job{
			DeploymentID: depID,
			JobName:      name,
			Status:       status,
			StartedAt:    start,
		}
	}

	jobs := []Job{
		mkJob("install-a", &t0, StatusSucceeded),
		mkJob("install-b", &t2, StatusRunning),
		mkJob("install-c", nil, StatusPending),
		mkJob("install-d", &t1, StatusFailed),
	}
	for _, j := range jobs {
		if err := st.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"install-b", "install-d", "install-a", "install-c"}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: %d vs %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].JobName != w {
			t.Errorf("position %d: got %q, want %q", i, got[i].JobName, w)
		}
	}
}

func TestStore_GetJob_NotFound(t *testing.T) {
	st := newTestStore(t)
	_, _, err := st.GetJob("dep-x", JobID("dep-x", "install-missing"))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStore_SummarizeBatches(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-batch"

	t0 := time.Now().UTC()
	cases := []struct {
		name   string
		status string
		start  *time.Time
	}{
		{"install-a", StatusSucceeded, &t0},
		{"install-b", StatusFailed, &t0},
		{"install-c", StatusRunning, &t0},
		{"install-d", StatusPending, nil},
		{"install-e", StatusSucceeded, &t0},
	}
	for _, c := range cases {
		if err := st.UpsertJob(Job{
			DeploymentID: depID,
			JobName:      c.name,
			BatchID:      BatchBootstrapKit,
			Status:       c.status,
			StartedAt:    c.start,
		}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := st.SummarizeBatches(depID)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].BatchID != BatchBootstrapKit {
		t.Fatalf("expected one batch, got %+v", out)
	}
	bs := out[0]
	if bs.Total != 5 || bs.Succeeded != 2 || bs.Failed != 1 || bs.Running != 1 || bs.Pending != 1 || bs.Finished != 3 {
		t.Errorf("counts: %+v", bs)
	}
}

func TestStore_AtomicIndexWrite_NoTempLeftBehind(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-atomic"

	for i := 0; i < 50; i++ {
		j := Job{
			DeploymentID: depID,
			JobName:      "install-x",
			Status:       StatusRunning,
		}
		if err := st.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}
	depDir := filepath.Join(st.Dir(), depID)
	entries, err := os.ReadDir(depDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestStore_RaceFreeConcurrentAppends(t *testing.T) {
	// Concurrent writers across N executions must not corrupt the
	// per-execution NDJSON files. Each writer appends K lines to its
	// own execution; we then assert each file has exactly K
	// well-formed lines and the index reports the right LineCount.
	st := newTestStore(t)
	depID := "dep-race"

	const N = 4
	const K = 100
	execIDs := make([]string, N)
	for i := 0; i < N; i++ {
		jobName := "install-" + string(rune('a'+i))
		if err := st.UpsertJob(Job{
			DeploymentID: depID,
			JobName:      jobName,
		}); err != nil {
			t.Fatal(err)
		}
		exec, err := st.StartExecution(depID, jobName, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		execIDs[i] = exec.ID
	}

	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for k := 0; k < K; k++ {
				if err := st.AppendLogLines(depID, execIDs[idx], []LogLine{{
					Level:   LevelInfo,
					Message: "k=", // tiny payload
				}}); err != nil {
					t.Errorf("AppendLogLines: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	for i := 0; i < N; i++ {
		page, err := st.PageLogs(depID, execIDs[i], 1, MaxLogPageSize)
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != K {
			t.Errorf("exec %d: total want %d, got %d", i, K, page.Total)
		}
		if len(page.Lines) != K {
			t.Errorf("exec %d: lines want %d, got %d", i, K, len(page.Lines))
		}
		// LineNumbers must be 1..K monotonic.
		for j, ll := range page.Lines {
			if ll.LineNumber != j+1 {
				t.Errorf("exec %d line %d: LineNumber want %d, got %d", i, j, j+1, ll.LineNumber)
				break
			}
		}
	}
}

func TestStore_FindExecutionAcrossDeployments(t *testing.T) {
	st := newTestStore(t)

	for _, depID := range []string{"dep-a", "dep-b", "dep-c"} {
		if err := st.UpsertJob(Job{DeploymentID: depID, JobName: "install-x"}); err != nil {
			t.Fatal(err)
		}
	}
	exec, err := st.StartExecution("dep-b", "install-x", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.FindExecutionAcrossDeployments(exec.ID)
	if err != nil {
		t.Fatalf("FindExecutionAcrossDeployments: %v", err)
	}
	if got.DeploymentID != "dep-b" {
		t.Errorf("DeploymentID: want dep-b, got %q", got.DeploymentID)
	}

	_, err = st.FindExecutionAcrossDeployments("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestStore_DeploymentDir_RejectsPathTraversal(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertJob(Job{DeploymentID: "../etc/passwd", JobName: "install-x"}); err == nil {
		t.Fatal("expected error for path-traversal id")
	}
}

func TestStore_LogsForMissingExec(t *testing.T) {
	st := newTestStore(t)
	if err := st.UpsertJob(Job{DeploymentID: "d", JobName: "install-x"}); err != nil {
		t.Fatal(err)
	}
	_, err := st.PageLogs("d", "no-such", 1, 10)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
