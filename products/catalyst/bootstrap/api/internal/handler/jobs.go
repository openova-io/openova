// Package handler — jobs.go: REST surface for the Jobs/Executions
// data model the Sovereign Admin's table-view UX (epic #204) reads.
//
// Four endpoints, all read-only — every mutation flows through the
// helmwatch bridge in internal/jobs.Bridge, which the Phase-1 watch
// goroutine wires up.
//
//   - GET /api/v1/deployments/{depId}/jobs               — list Jobs
//   - GET /api/v1/deployments/{depId}/jobs/{jobId}       — one Job +
//     executions
//   - GET /api/v1/actions/executions/{execId}/logs       — paginated
//     LogLines
//   - GET /api/v1/deployments/{depId}/jobs/batches       — per-batch
//     progress
//
// Backwards compat: the existing `/api/v1/deployments/{id}/events`
// SSE feed is not modified. Both feeds live in parallel; the wizard
// reads SSE for live banner state and the new table-view UX reads
// these endpoints.
package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
)

// jobsStore returns the Handler's jobs.Store. Returns nil when
// persistence is disabled (CI runners without write access to
// /var/lib). Handlers map a nil store onto HTTP 503 so the operator
// can tell "no jobs yet" (200 with empty list) apart from "store is
// down" (503 with retry-after).
func (h *Handler) jobsStore() *jobs.Store {
	return h.jobs
}

// ListJobs handles GET /api/v1/deployments/{depId}/jobs.
//
// Returns `{ "jobs": [...] }` — the slice is sorted started-at DESC
// with pending Jobs (no StartedAt) bucketed last. Empty deployment →
// empty slice (not null) so the JSON shape never breaks the
// frontend's render loop.
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	st := h.jobsStore()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "jobs-store-unavailable",
			"detail": "catalyst-api is running with persistence disabled — see Pod logs",
		})
		return
	}
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	if depID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "missing-depId",
			"detail": "deployment id path segment is required",
		})
		return
	}
	out, err := st.ListJobs(depID)
	if err != nil {
		h.log.Error("ListJobs: load index failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "store-read-failed",
		})
		return
	}
	if out == nil {
		out = []jobs.Job{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs": out,
	})
}

// GetJob handles GET /api/v1/deployments/{depId}/jobs/{jobId}.
//
// jobId is the "<deploymentId>:<jobName>" stable id. Chi routes a
// colon as a literal so the parameter arrives intact; a stray segment
// is rejected before hitting the store.
//
// Returns `{ "job": {...}, "executions": [...] }`. The executions
// slice is sorted startedAt DESC so the most-recent attempt is index
// 0 — matches the wire spec in #205 and the GitLab-CI runner
// convention.
func (h *Handler) GetJob(w http.ResponseWriter, r *http.Request) {
	st := h.jobsStore()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "jobs-store-unavailable",
		})
		return
	}
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	jobID := strings.TrimSpace(chi.URLParam(r, "jobId"))
	if depID == "" || jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-path-params",
		})
		return
	}
	job, execs, err := st.GetJob(depID, jobID)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "job-not-found",
			})
			return
		}
		h.log.Error("GetJob: load failed", "depId", depID, "jobId", jobID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "store-read-failed",
		})
		return
	}
	if execs == nil {
		execs = []jobs.Execution{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job":        job,
		"executions": execs,
	})
}

// ListBatches handles GET /api/v1/deployments/{depId}/jobs/batches.
//
// Returns `{ "batches": [...] }` — one row per BatchID with progress
// counts. Empty deployment → empty slice. The current implementation
// always emits at most one batch ("bootstrap-kit") since Phase-1 is
// the only Job source; future Day-2 batches will appear automatically
// as the bridge writes them.
func (h *Handler) ListBatches(w http.ResponseWriter, r *http.Request) {
	st := h.jobsStore()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "jobs-store-unavailable",
		})
		return
	}
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	if depID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-depId",
		})
		return
	}
	out, err := st.SummarizeBatches(depID)
	if err != nil {
		h.log.Error("ListBatches: summarize failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "store-read-failed",
		})
		return
	}
	if out == nil {
		out = []jobs.BatchSummary{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"batches": out,
	})
}

// GetExecutionLogs handles GET
// /api/v1/actions/executions/{execId}/logs?fromLine=N&limit=M.
//
// Returns `{ "lines": [...], "total": N, "executionFinished": bool }`.
// Pagination contract:
//
//   - fromLine — 1-indexed, default 1 (omitted / non-positive ⇒ 1).
//   - limit    — default DefaultLogPageSize (500), max MaxLogPageSize
//     (5000). Out-of-range values are clamped silently —
//     the frontend's polling loop never has to retry on
//     422.
//
// The endpoint deliberately omits the deploymentId from the URL path —
// the spec in #205 wants a flat /actions/executions/{id}/logs surface
// the GitLab-style viewer can deep-link to without juggling the
// parent deployment id. The store walks every deployment subdir to
// resolve the executionId.
func (h *Handler) GetExecutionLogs(w http.ResponseWriter, r *http.Request) {
	st := h.jobsStore()
	if st == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "jobs-store-unavailable",
		})
		return
	}
	execID := strings.TrimSpace(chi.URLParam(r, "execId"))
	if execID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-execId",
		})
		return
	}
	q := r.URL.Query()
	fromLine, _ := strconv.Atoi(strings.TrimSpace(q.Get("fromLine")))
	limit, _ := strconv.Atoi(strings.TrimSpace(q.Get("limit")))

	// Resolve the deploymentID by scanning the store. The Bridge
	// guarantees executionId uniqueness (16-byte hex) so first match
	// wins.
	exec, err := st.FindExecutionAcrossDeployments(execID)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "execution-not-found",
			})
			return
		}
		h.log.Error("GetExecutionLogs: lookup failed", "execId", execID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "store-read-failed",
		})
		return
	}
	page, err := st.PageLogs(exec.DeploymentID, execID, fromLine, limit)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error": "execution-not-found",
			})
			return
		}
		h.log.Error("GetExecutionLogs: page failed", "execId", execID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "store-read-failed",
		})
		return
	}
	if page.Lines == nil {
		page.Lines = []jobs.LogLine{}
	}
	writeJSON(w, http.StatusOK, page)
}
