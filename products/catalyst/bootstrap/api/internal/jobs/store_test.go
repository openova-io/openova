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

	parentID := JobID(depID, GroupBootstrapKit)
	j := Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		Type:         JobTypeInstall,
		ParentID:     parentID,
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
	// Persisted leaf + synthesized bootstrap-kit parent group
	// (deriveTreeView synthesizes a group row in-memory whenever a
	// leaf points at a ParentID with no on-disk peer — see the
	// legacy migration path #351).
	if len(got) != 2 {
		t.Fatalf("expected 2 jobs (leaf + synthesized parent), got %d (%+v)", len(got), got)
	}
	var leaf, group *Job
	for i := range got {
		if got[i].ID == JobID(depID, "install-cilium") {
			leaf = &got[i]
		}
		if got[i].ID == parentID {
			group = &got[i]
		}
	}
	if leaf == nil {
		t.Fatalf("install-cilium leaf missing in %+v", got)
	}
	if leaf.AppID != "cilium" || leaf.ParentID != parentID || leaf.Type != JobTypeInstall {
		t.Fatalf("leaf metadata mismatch: %+v", leaf)
	}
	if len(leaf.DependsOn) != 1 || leaf.DependsOn[0] != "install-flux" {
		t.Fatalf("dependsOn mismatch: %+v", leaf.DependsOn)
	}
	if group == nil || group.Type != JobTypeGroup {
		t.Fatalf("synthesized bootstrap-kit group missing: %+v", got)
	}
	if len(group.ChildIDs) != 1 || group.ChildIDs[0] != leaf.ID {
		t.Fatalf("synthesized group ChildIDs: want [%s], got %v", leaf.ID, group.ChildIDs)
	}
}

// TestStore_LegacyBatchID_HoistedToParentID locks in the #351 legacy
// migration contract: pre-refactor indexes persisted the deprecated
// `batchId` JSON field; on read, the store hoists any non-empty
// legacy value into ParentID and synthesizes a parent group Job
// in-memory so the recursive Job tree renders correctly even before
// the next bridge write rewrites the on-disk record.
func TestStore_LegacyBatchID_HoistedToParentID(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-legacy"

	depDir := filepath.Join(st.Dir(), depID)
	if err := os.MkdirAll(depDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{
	  "deploymentId": "dep-legacy",
	  "jobs": [
	    {"id":"dep-legacy:install-cilium","deploymentId":"dep-legacy","jobName":"install-cilium","appId":"cilium","batchId":"bootstrap-kit","dependsOn":[],"status":"succeeded"},
	    {"id":"dep-legacy:install-cert-manager","deploymentId":"dep-legacy","jobName":"install-cert-manager","appId":"cert-manager","batchId":"bootstrap-kit","dependsOn":["install-cilium"],"status":"running"}
	  ],
	  "executions": []
	}`
	if err := os.WriteFile(filepath.Join(depDir, "index.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 jobs (2 leaves + 1 synthesized group), got %d (%+v)", len(got), got)
	}
	parentID := JobID(depID, GroupBootstrapKit)
	for _, j := range got {
		switch j.Type {
		case JobTypeInstall:
			if j.ParentID != parentID {
				t.Errorf("leaf %s: ParentID hoist failed — want %q, got %q", j.JobName, parentID, j.ParentID)
			}
			if j.LegacyBatchID != "" {
				t.Errorf("leaf %s: LegacyBatchID NOT cleared on read: %q", j.JobName, j.LegacyBatchID)
			}
		case JobTypeGroup:
			if j.ID != parentID {
				t.Errorf("synthesized group: id mismatch — want %q, got %q", parentID, j.ID)
			}
			if j.DisplayName != GroupBootstrapKitDisplay {
				t.Errorf("synthesized group: DisplayName — want %q, got %q", GroupBootstrapKitDisplay, j.DisplayName)
			}
			if len(j.ChildIDs) != 2 {
				t.Errorf("synthesized group: ChildIDs — want 2, got %d (%v)", len(j.ChildIDs), j.ChildIDs)
			}
			if j.Status != StatusRunning {
				t.Errorf("synthesized group: rolled-up Status — want running, got %q", j.Status)
			}
		default:
			t.Errorf("unexpected Type %q on %s", j.Type, j.JobName)
		}
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
		Type:         JobTypeInstall,
		ParentID:     JobID(depID, GroupBootstrapKit),
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

// TestStore_RunCount_CountsExecutionsPerLeaf pins the #3925 run-history
// depth: ListJobs + GetJob derive RunCount from the flat Execution index, so
// a collapsed identity row (one Job, many runs) reports its full run count.
func TestStore_RunCount_CountsExecutionsPerLeaf(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-runcount"

	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      "task-trivy-security-scan",
		AppID:        "trivy-security-scan",
		Type:         JobTypeInstall,
		Kind:         KindTask,
		ParentID:     JobID(depID, GroupReconcilers),
		Status:       StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	// A one-shot job with a single run, to prove RunCount=1 there.
	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      "task-openbao-init",
		AppID:        "openbao-init",
		Type:         JobTypeInstall,
		Kind:         KindTask,
		ParentID:     JobID(depID, GroupReconcilers),
		Status:       StatusPending,
	}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().UTC()
	const scanRuns = 600
	for i := 0; i < scanRuns; i++ {
		e, err := st.StartExecution(depID, "task-trivy-security-scan", t0.Add(time.Duration(i)*time.Second))
		if err != nil {
			t.Fatalf("StartExecution[%d]: %v", i, err)
		}
		if err := st.FinishExecution(depID, e.ID, StatusSucceeded, t0.Add(time.Duration(i)*time.Second+time.Second)); err != nil {
			t.Fatalf("FinishExecution[%d]: %v", i, err)
		}
	}
	oneShot, err := st.StartExecution(depID, "task-openbao-init", t0)
	if err != nil {
		t.Fatalf("StartExecution(one-shot): %v", err)
	}
	if err := st.FinishExecution(depID, oneShot.ID, StatusSucceeded, t0.Add(time.Second)); err != nil {
		t.Fatalf("FinishExecution(one-shot): %v", err)
	}

	// ListJobs: the collapsed scanner row reports all 600 runs; the one-shot 1.
	jobsList, err := st.ListJobs(depID)
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	byName := map[string]Job{}
	for _, j := range jobsList {
		byName[j.JobName] = j
	}
	if got := byName["task-trivy-security-scan"].RunCount; got != scanRuns {
		t.Errorf("collapsed scanner RunCount: want %d, got %d", scanRuns, got)
	}
	if got := byName["task-openbao-init"].RunCount; got != 1 {
		t.Errorf("one-shot RunCount: want 1, got %d", got)
	}
	// The Reconcilers group row owns no Executions of its own → RunCount 0.
	if grp, ok := byName[GroupReconcilers]; ok && grp.RunCount != 0 {
		t.Errorf("group RunCount should be 0, got %d", grp.RunCount)
	}

	// GetJob mirrors ListJobs.
	j, execs, err := st.GetJob(depID, JobID(depID, "task-trivy-security-scan"))
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if j.RunCount != scanRuns || len(execs) != scanRuns {
		t.Errorf("GetJob RunCount/execs: want %d/%d, got %d/%d", scanRuns, scanRuns, j.RunCount, len(execs))
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

func TestStore_DeriveTreeView_RollsUpGroupStatus(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-tree"
	parentID := JobID(depID, GroupBootstrapKit)

	// Materialise the parent group + 5 leaves spanning every status
	// bucket. The persisted group Status is StatusPending — the
	// rolled-up value must reach the wire as StatusFailed (because at
	// least one child failed).
	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      GroupBootstrapKit,
		DisplayName:  GroupBootstrapKitDisplay,
		Type:         JobTypeGroup,
		Status:       StatusPending,
	}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().UTC().Truncate(time.Second)
	tEnd := t0.Add(2 * time.Minute)
	cases := []struct {
		name     string
		status   string
		start    *time.Time
		finish   *time.Time
	}{
		{"install-a", StatusSucceeded, &t0, &tEnd},
		{"install-b", StatusFailed, &t0, &tEnd},
		{"install-c", StatusRunning, &t0, nil},
		{"install-d", StatusPending, nil, nil},
		{"install-e", StatusSucceeded, &t0, &tEnd},
	}
	for _, c := range cases {
		if err := st.UpsertJob(Job{
			DeploymentID: depID,
			JobName:      c.name,
			Type:         JobTypeInstall,
			ParentID:     parentID,
			Status:       c.status,
			StartedAt:    c.start,
			FinishedAt:   c.finish,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListJobs(depID)
	if err != nil {
		t.Fatal(err)
	}
	var group *Job
	for i := range got {
		if got[i].ID == parentID {
			group = &got[i]
			break
		}
	}
	if group == nil {
		t.Fatalf("parent group missing: %+v", got)
	}
	if group.Status != StatusFailed {
		t.Errorf("rolled-up Status: want failed, got %q", group.Status)
	}
	if len(group.ChildIDs) != 5 {
		t.Errorf("ChildIDs: want 5, got %d (%v)", len(group.ChildIDs), group.ChildIDs)
	}
	if group.StartedAt == nil || !group.StartedAt.Equal(t0) {
		t.Errorf("StartedAt rollup: want %v, got %v", t0, group.StartedAt)
	}
	// Not all descendants are terminal (running + pending exist) so
	// FinishedAt MUST stay nil.
	if group.FinishedAt != nil {
		t.Errorf("FinishedAt rollup: want nil while running, got %v", group.FinishedAt)
	}
}

func TestStore_DeriveTreeView_AllSucceededRollsUp(t *testing.T) {
	st := newTestStore(t)
	depID := "dep-tree-done"
	parentID := JobID(depID, GroupBootstrapKit)

	if err := st.UpsertJob(Job{
		DeploymentID: depID,
		JobName:      GroupBootstrapKit,
		DisplayName:  GroupBootstrapKitDisplay,
		Type:         JobTypeGroup,
		Status:       StatusPending,
	}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Now().UTC().Truncate(time.Second)
	t1 := t0.Add(time.Minute)
	for _, n := range []string{"install-a", "install-b"} {
		if err := st.UpsertJob(Job{
			DeploymentID: depID,
			JobName:      n,
			Type:         JobTypeInstall,
			ParentID:     parentID,
			Status:       StatusSucceeded,
			StartedAt:    &t0,
			FinishedAt:   &t1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := st.ListJobs(depID)
	var group *Job
	for i := range got {
		if got[i].ID == parentID {
			group = &got[i]
			break
		}
	}
	if group == nil {
		t.Fatalf("group missing")
	}
	if group.Status != StatusSucceeded {
		t.Errorf("Status: want succeeded, got %q", group.Status)
	}
	if group.FinishedAt == nil || !group.FinishedAt.Equal(t1) {
		t.Errorf("FinishedAt: want %v, got %v", t1, group.FinishedAt)
	}
	if group.DurationMs != 60_000 {
		t.Errorf("DurationMs: want 60000, got %d", group.DurationMs)
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
