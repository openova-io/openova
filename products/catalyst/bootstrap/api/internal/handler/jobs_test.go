// jobs_test.go — httptest-driven handler tests for the Jobs/Executions
// REST surface. Each test seeds a fresh in-memory store via
// NewWithJobsStore(t.TempDir()), wires the chi router the production
// main.go does, then asserts on the JSON shape end-to-end.
package handler

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// newJobsAPIRouter wires the same chi routes main.go does, but only
// for the 4 endpoints under test. Avoids spinning up the full HTTP
// surface for these unit tests.
func newJobsAPIRouter(t *testing.T) (*chi.Mux, *jobs.Store, *Handler) {
	t.Helper()
	js, err := jobs.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), js)
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)
	r.Get("/api/v1/deployments/{depId}/jobs/{jobId}", h.GetJob)
	r.Get("/api/v1/actions/executions/{execId}/logs", h.GetExecutionLogs)
	return r, js, h
}

func decodeJSON(t *testing.T, body io.Reader, into any) {
	t.Helper()
	if err := json.NewDecoder(body).Decode(into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

func TestHandler_ListJobs_Empty(t *testing.T) {
	r, _, _ := newJobsAPIRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/dep-empty/jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if resp.Jobs == nil {
		t.Fatal("jobs must be empty slice not null")
	}
	if len(resp.Jobs) != 0 {
		t.Errorf("expected 0 jobs, got %d", len(resp.Jobs))
	}
	// Verify the `jobs` key is `[]` not `null` in the raw body.
	if !strings.Contains(body, `"jobs"`) {
		t.Errorf("missing jobs key: %s", body)
	}
	if !strings.Contains(body, `[]`) {
		t.Errorf("expected empty array `[]`: %s", body)
	}
}

// TestHandler_ListJobs_Populated asserts the /jobs view selector (#3996
// follow-up + #5019 install lens): the open-ended reconciler leaves
// (KindReconcile / KindReconciler) are dropped from /jobs and surface only
// on the Cloud Reconciliation lens, while the bootstrap-kit HelmRelease
// install leaves (KindInstall, issue #5019) AND genuinely finite work
// (provision steps, batch Jobs, CronJob runs, Day-2 mutations) render.
// Groups with surviving children keep their rolled-up status + ChildIDs;
// the reconcilers group, left with only reconcile/reconciler leaves, is
// pruned entirely.
func TestHandler_ListJobs_Populated(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-populated"

	t0 := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	// bootstrap-kit group holds install leaves — kept on /jobs (#5019).
	bkParent := jobs.JobID(depID, jobs.GroupBootstrapKit)
	// provisioner group holds finite work (provisioner steps, a batch
	// task) that MUST still render on /jobs.
	finiteParent := jobs.JobID(depID, jobs.GroupProvisioner)
	// reconcilers group holds ONLY open-ended reconcile/reconciler leaves
	// — it must be pruned from /jobs after filtering.
	reconParent := jobs.JobID(depID, jobs.GroupReconcilers)
	jobsToSeed := []jobs.Job{
		// bootstrap-kit group + its install leaves → all kept (#5019).
		{DeploymentID: depID, JobName: jobs.GroupBootstrapKit, DisplayName: jobs.GroupBootstrapKitDisplay, Type: jobs.JobTypeGroup, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "install-cilium", AppID: "cilium", Kind: jobs.KindInstall, Type: jobs.JobTypeInstall, ParentID: bkParent, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: ptrTime(t0.Add(20 * time.Second))},
		{DeploymentID: depID, JobName: "install-flux", AppID: "flux", Kind: jobs.KindInstall, Type: jobs.JobTypeInstall, ParentID: bkParent, Status: jobs.StatusRunning, StartedAt: ptrTime(t0.Add(time.Minute))},
		// Finite-work group + surviving children (a provision step + a
		// batch task) → kept on /jobs.
		{DeploymentID: depID, JobName: jobs.GroupProvisioner, Type: jobs.JobTypeGroup, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "provisioner-step-tofu-init", Kind: jobs.KindStep, Type: jobs.JobTypeInstall, ParentID: finiteParent, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: ptrTime(t0.Add(10 * time.Second))},
		{DeploymentID: depID, JobName: "task-snapshot-once", Kind: jobs.KindTask, Type: jobs.JobTypeInstall, ParentID: finiteParent, Status: jobs.StatusRunning, StartedAt: ptrTime(t0.Add(time.Minute))},
		// Reconcilers group + its open-ended leaves → all dropped.
		{DeploymentID: depID, JobName: jobs.GroupReconcilers, DisplayName: jobs.GroupReconcilersDisplay, Type: jobs.JobTypeGroup, Status: jobs.StatusPending},
		{DeploymentID: depID, JobName: "reconcile-apps", Kind: jobs.KindReconcile, Type: jobs.JobTypeInstall, ParentID: reconParent, Status: jobs.StatusRunning},
		{DeploymentID: depID, JobName: "reconciler-sso-bridge", Kind: jobs.KindReconciler, Type: jobs.JobTypeInstall, ParentID: reconParent, Status: jobs.StatusHealthy},
	}
	for _, j := range jobsToSeed {
		if err := st.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+depID+"/jobs", nil))
	if rec.Code != 200 {
		t.Fatalf("code: %d", rec.Code)
	}
	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	decodeJSON(t, rec.Body, &resp)

	// Expect bootstrap-kit group + 2 install leaves + finite group + its 2
	// finite leaves = 6; the reconcilers group + its 2 open-ended leaves
	// are gone.
	if len(resp.Jobs) != 6 {
		t.Fatalf("expected 6 jobs (bk group + 2 installs + finite group + 2 finite leaves), got %d: %+v", len(resp.Jobs), resp.Jobs)
	}
	byName := map[string]*jobs.Job{}
	for i := range resp.Jobs {
		j := &resp.Jobs[i]
		if jobs.IsContinuousReconciler(j.Kind) {
			t.Errorf("continuous reconciler leaked into /jobs: %q (kind=%q)", j.JobName, j.Kind)
		}
		if j.JobName == jobs.GroupReconcilers {
			t.Errorf("empty reconcilers group should have been pruned, still present")
		}
		byName[j.JobName] = j
	}
	// #5019 — the install rows render on /jobs with the typed install Kind
	// so the JobsTable Kind filter offers the install lens.
	for _, want := range []string{"install-cilium", "install-flux"} {
		ij := byName[want]
		if ij == nil {
			t.Fatalf("install row %q missing from /jobs (#5019): %+v", want, resp.Jobs)
		}
		if ij.Kind != jobs.KindInstall {
			t.Errorf("install row %q Kind: want %q, got %q", want, jobs.KindInstall, ij.Kind)
		}
	}
	// The bootstrap-kit group survives with both install children and a
	// running rollup (succeeded + running → running).
	bk := byName[jobs.GroupBootstrapKit]
	if bk == nil {
		t.Fatalf("bootstrap-kit group missing in response (#5019): %+v", resp.Jobs)
	}
	if len(bk.ChildIDs) != 2 {
		t.Errorf("bootstrap-kit ChildIDs: want 2, got %d (%v)", len(bk.ChildIDs), bk.ChildIDs)
	}
	if bk.Status != jobs.StatusRunning {
		t.Errorf("bootstrap-kit rolled-up Status: want running, got %q", bk.Status)
	}
	// The finite group survives with both finite children + a running rollup.
	group := byName[jobs.GroupProvisioner]
	if group == nil {
		t.Fatalf("finite group job missing in response: %+v", resp.Jobs)
	}
	// Children: succeeded step + running task → rolled-up running.
	if group.Status != jobs.StatusRunning {
		t.Errorf("group rolled-up Status: want running, got %q", group.Status)
	}
	if len(group.ChildIDs) != 2 {
		t.Errorf("group ChildIDs: want 2, got %d (%v)", len(group.ChildIDs), group.ChildIDs)
	}
}

func TestHandler_GetJob_FoundAndNotFound(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-getjob"

	if err := st.UpsertJob(jobs.Job{
		DeploymentID: depID,
		JobName:      "install-cilium",
		AppID:        "cilium",
		Type:         jobs.JobTypeInstall,
		ParentID:     jobs.JobID(depID, jobs.GroupBootstrapKit),
		Status:       jobs.StatusPending,
		DependsOn:    []string{"install-flux"},
	}); err != nil {
		t.Fatal(err)
	}
	exec, err := st.StartExecution(depID, "install-cilium", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	url := "/api/v1/deployments/" + depID + "/jobs/" + jobs.JobID(depID, "install-cilium")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != 200 {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Job        jobs.Job         `json:"job"`
		Executions []jobs.Execution `json:"executions"`
	}
	decodeJSON(t, rec.Body, &resp)
	if resp.Job.AppID != "cilium" {
		t.Errorf("appId: %q", resp.Job.AppID)
	}
	if len(resp.Job.DependsOn) != 1 || resp.Job.DependsOn[0] != "install-flux" {
		t.Errorf("dependsOn: %+v", resp.Job.DependsOn)
	}
	if len(resp.Executions) != 1 || resp.Executions[0].ID != exec.ID {
		t.Errorf("executions: %+v", resp.Executions)
	}

	// 404 path
	rec404 := httptest.NewRecorder()
	r.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+depID+"/jobs/"+jobs.JobID(depID, "install-missing"), nil))
	if rec404.Code != http.StatusNotFound {
		t.Errorf("404 expected, got %d", rec404.Code)
	}
}

func TestHandler_GetExecutionLogs_Pagination(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-logs"
	if err := st.UpsertJob(jobs.Job{DeploymentID: depID, JobName: "install-x"}); err != nil {
		t.Fatal(err)
	}
	exec, err := st.StartExecution(depID, "install-x", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]jobs.LogLine, 50)
	for i := range lines {
		lines[i] = jobs.LogLine{Level: jobs.LevelInfo, Message: "log"}
	}
	if err := st.AppendLogLines(depID, exec.ID, lines); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/actions/executions/"+exec.ID+"/logs?fromLine=10&limit=5", nil))
	if rec.Code != 200 {
		t.Fatalf("status: %d body=%s", rec.Code, rec.Body.String())
	}
	var resp jobs.LogPage
	decodeJSON(t, rec.Body, &resp)
	if len(resp.Lines) != 5 {
		t.Fatalf("lines: %d", len(resp.Lines))
	}
	if resp.Lines[0].LineNumber != 10 || resp.Lines[4].LineNumber != 14 {
		t.Errorf("LineNumbers: %+v", resp.Lines)
	}
	if resp.Total != 50 {
		t.Errorf("total: %d", resp.Total)
	}
	if resp.ExecutionFinished {
		t.Errorf("ExecutionFinished should be false while running")
	}
}

func TestHandler_GetExecutionLogs_NotFound(t *testing.T) {
	r, _, _ := newJobsAPIRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/actions/executions/no-such/logs", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_NoStore_503(t *testing.T) {
	h := NewWithJobsStore(slog.New(slog.NewJSONHandler(io.Discard, nil)), nil)
	r := chi.NewRouter()
	r.Get("/api/v1/deployments/{depId}/jobs", h.ListJobs)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/d/jobs", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d", rec.Code)
	}
}
