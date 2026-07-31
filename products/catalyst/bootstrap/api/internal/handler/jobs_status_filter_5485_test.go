// jobs_status_filter_5485_test.go — server-side ?status= filtering on
// GET /api/v1/deployments/{depId}/jobs (#5485 defect 5).
//
// Live-observed on hw291 (2026-07-30): the endpoint returned all 172
// rows for ?status=failed, ?status=succeeded and ?status=running alike.
// Invisible in the console only because the JobsPage filters
// client-side; any API consumer trusting the parameter got everything.
//
// Contract pinned here, both directions:
//   - ?status=<v> for v ∈ {pending, running, succeeded, failed} returns
//     ONLY rows whose wire Status equals v (case-insensitive input).
//   - no ?status= param → the full (finite-filtered) list, unchanged.
//   - unknown ?status= value → 400 invalid-status (the handler's
//     existing bad-param idiom: {"error", "detail"} JSON).
//
// Removing the filterJobsByStatus call in ListJobs makes the filtered
// sub-tests fail (every seeded row comes back regardless of the
// parameter) — that is the pre-fix behaviour this test refutes.
package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// seedStatusFilterJobs seeds one finite leaf per store status so every
// filter value has exactly one matching row. All leaves are finite
// kinds (step/task/install) so FilterFiniteJobs keeps them and the
// filter under test is the only row-count variable.
func seedStatusFilterJobs(t *testing.T, st *jobs.Store, depID string) {
	t.Helper()
	t0 := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	rows := []jobs.Job{
		{DeploymentID: depID, JobName: "provisioner-step-tofu-apply", Kind: jobs.KindStep, Type: jobs.JobTypeInstall, Status: jobs.StatusFailed, StartedAt: &t0, FinishedAt: ptrTime(t0.Add(30 * time.Second))},
		{DeploymentID: depID, JobName: "install-keycloak", AppID: "keycloak", Kind: jobs.KindInstall, Type: jobs.JobTypeInstall, Status: jobs.StatusSucceeded, StartedAt: ptrTime(t0.Add(time.Minute)), FinishedAt: ptrTime(t0.Add(2 * time.Minute))},
		{DeploymentID: depID, JobName: "task-snapshot-once", Kind: jobs.KindTask, Type: jobs.JobTypeInstall, Status: jobs.StatusRunning, StartedAt: ptrTime(t0.Add(3 * time.Minute))},
		{DeploymentID: depID, JobName: "task-cert-rotate", Kind: jobs.KindTask, Type: jobs.JobTypeInstall, Status: jobs.StatusPending},
	}
	for _, j := range rows {
		if err := st.UpsertJob(j); err != nil {
			t.Fatal(err)
		}
	}
}

func listJobsWithQuery(t *testing.T, r http.Handler, depID, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+depID+"/jobs"+query, nil))
	return rec
}

// Direction 1 — ?status=<valid> returns ONLY the matching rows.
func TestHandler_ListJobs_StatusFilter_FiltersServerSide(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-status-filter"
	seedStatusFilterJobs(t, st, depID)

	cases := []struct {
		status   string
		wantName string
	}{
		{jobs.StatusFailed, "provisioner-step-tofu-apply"},
		{jobs.StatusSucceeded, "install-keycloak"},
		{jobs.StatusRunning, "task-snapshot-once"},
		{jobs.StatusPending, "task-cert-rotate"},
	}
	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			rec := listJobsWithQuery(t, r, depID, "?status="+c.status)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%s: code %d body=%s", c.status, rec.Code, rec.Body.String())
			}
			var resp struct {
				Jobs []jobs.Job `json:"jobs"`
			}
			decodeJSON(t, rec.Body, &resp)
			if len(resp.Jobs) != 1 {
				t.Fatalf("?status=%s: want exactly 1 row, got %d: %+v", c.status, len(resp.Jobs), resp.Jobs)
			}
			if resp.Jobs[0].JobName != c.wantName {
				t.Errorf("?status=%s: want row %q, got %q", c.status, c.wantName, resp.Jobs[0].JobName)
			}
			if resp.Jobs[0].Status != c.status {
				t.Errorf("?status=%s: returned row carries Status %q", c.status, resp.Jobs[0].Status)
			}
		})
	}
}

// The parameter is case-insensitive — ?status=FAILED filters like failed.
func TestHandler_ListJobs_StatusFilter_CaseInsensitive(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-status-case"
	seedStatusFilterJobs(t, st, depID)

	rec := listJobsWithQuery(t, r, depID, "?status=FAILED")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	decodeJSON(t, rec.Body, &resp)
	if len(resp.Jobs) != 1 || resp.Jobs[0].Status != jobs.StatusFailed {
		t.Fatalf("?status=FAILED: want the 1 failed row, got %+v", resp.Jobs)
	}
}

// Direction 2 — no ?status= param returns the full list, unchanged.
func TestHandler_ListJobs_StatusFilter_AbsentReturnsAll(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-status-all"
	seedStatusFilterJobs(t, st, depID)

	rec := listJobsWithQuery(t, r, depID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	decodeJSON(t, rec.Body, &resp)
	if len(resp.Jobs) != 4 {
		t.Fatalf("no param: want all 4 rows, got %d: %+v", len(resp.Jobs), resp.Jobs)
	}
}

// A ?status= value outside the store vocabulary is a 400 with the
// handler's existing {"error","detail"} idiom — not a silent
// empty-or-everything response.
func TestHandler_ListJobs_StatusFilter_UnknownValue400(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-status-bad"
	seedStatusFilterJobs(t, st, depID)

	rec := listJobsWithQuery(t, r, depID, "?status=exploded")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("?status=exploded: want 400, got %d body=%s", rec.Code, rec.Body.String())
	}
	// writeJSON enriches ≥400 bodies with a numeric `status` field, so
	// decode loosely and assert the error key.
	var resp map[string]any
	decodeJSON(t, rec.Body, &resp)
	if resp["error"] != "invalid-status" {
		t.Errorf("error key: want invalid-status, got %v (body=%v)", resp["error"], resp)
	}
}

// A valid ?status= with zero matching rows returns an empty slice —
// `[]`, never null, and never the unfiltered set.
func TestHandler_ListJobs_StatusFilter_NoMatchesEmptySlice(t *testing.T) {
	r, st, _ := newJobsAPIRouter(t)
	depID := "dep-status-none"
	// Seed only succeeded rows so ?status=failed matches nothing.
	t0 := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	if err := st.UpsertJob(jobs.Job{DeploymentID: depID, JobName: "install-gitea", AppID: "gitea", Kind: jobs.KindInstall, Type: jobs.JobTypeInstall, Status: jobs.StatusSucceeded, StartedAt: &t0, FinishedAt: ptrTime(t0.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}

	rec := listJobsWithQuery(t, r, depID, "?status=failed")
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Jobs []jobs.Job `json:"jobs"`
	}
	decodeJSON(t, rec.Body, &resp)
	if resp.Jobs == nil {
		t.Fatal("jobs must be [] not null when the filter matches nothing")
	}
	if len(resp.Jobs) != 0 {
		t.Fatalf("?status=failed on succeeded-only store: want 0 rows, got %d: %+v", len(resp.Jobs), resp.Jobs)
	}
}
