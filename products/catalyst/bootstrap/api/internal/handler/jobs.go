// Package handler — jobs.go: REST surface for the Jobs/Executions
// data model the Sovereign Admin's canvas + per-job detail pages read.
//
// Three endpoints, all read-only — every mutation flows through the
// helmwatch bridge in internal/jobs.Bridge, which the Phase-1 watch
// goroutine wires up. Batches are no longer a first-class concept;
// see issue #351 for the recursive Job model that replaced them.
//
//   - GET /api/v1/deployments/{depId}/jobs               — list Jobs
//     (each Job carries parentId + childIds; group jobs roll status
//     up from descendants at read time)
//   - GET /api/v1/deployments/{depId}/jobs/{jobId}       — one Job +
//     executions
//   - GET /api/v1/actions/executions/{execId}/logs       — paginated
//     LogLines
//
// Backwards compat: the existing `/api/v1/deployments/{id}/events`
// SSE feed is not modified. Both feeds live in parallel; the wizard
// reads SSE for live banner state and the canvas + per-job detail
// pages read these endpoints.
package handler

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// chrootEnsureDeployment — when the catalyst-api is running inside a
// Sovereign chroot (SOVEREIGN_FQDN env set) and the requested
// deployment id is not in the in-memory map, synthesise a minimal
// Deployment record so chrootSeedJobsStoreIfEmpty can fire and
// HandleK8sStream/list/sync's URL ID resolves to the chroot's single
// self-registered cluster. Cutover steps don't import the mother's
// deployment record onto the chroot — but the chroot has all the
// authority it needs (SOVEREIGN_FQDN + in-cluster RBAC) to serve the
// per-deployment views by ID alone.
//
// Returns the synthesised Deployment, or nil when not in chroot mode
// or when the synthesis isn't safe (auth failure on the future
// owner-check path is already covered by the caller's checkOwnership
// gate; we only pre-load the record here).
func (h *Handler) chrootEnsureDeployment(depID string) *Deployment {
	selfFQDN := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	if selfFQDN == "" {
		return nil
	}
	if val, ok := h.deployments.Load(depID); ok {
		return val.(*Deployment)
	}
	dep := &Deployment{
		ID: depID,
		Request: provisioner.Request{
			SovereignFQDN: selfFQDN,
		},
		Status: "ready",
	}
	h.deployments.Store(depID, dep)
	h.log.Info("chroot: synthesised in-memory deployment record",
		"depId", depID, "sovereignFQDN", selfFQDN)
	return dep
}

// chrootSeedJobsStoreIfEmpty — when the chroot Sovereign-side
// catalyst-api gets a /jobs read for an imported deployment whose
// per-deployment jobs.Store has no records, lazily seed the store
// from a one-shot live-cluster HelmRelease list. Same shape as the
// mother's Phase-1 informer initial-list seed (snapshotsToSeeds +
// Bridge.SeedJobsFromInformerList) so the read returns byte-identical
// rich Job records (deps + parent + status) just like the mother.
//
// No-op when:
//   - SOVEREIGN_FQDN env is unset (mother mode)
//   - dep.Request.SovereignFQDN doesn't match SOVEREIGN_FQDN
//   - jobs.Store already has records for this deployment
//   - sovereignDynamicClient errors (handler returns the existing
//     empty list — caller already handles that case)
func (h *Handler) chrootSeedJobsStoreIfEmpty(ctx context.Context, dep *Deployment) {
	if h.jobs == nil {
		return
	}
	selfFQDN := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	if selfFQDN == "" {
		return
	}
	if !strings.EqualFold(selfFQDN, dep.Request.SovereignFQDN) {
		return
	}
	existing, err := h.jobs.ListJobs(dep.ID)
	if err == nil && len(existing) > 0 {
		return
	}
	dyn, err := h.sovereignDynamicClient(dep)
	if err != nil {
		h.log.Debug("chroot seed: sovereignDynamicClient unavailable", "depId", dep.ID, "err", err)
		return
	}
	snap, err := helmwatch.ListAndSnapshotHelmReleases(ctx, dyn)
	if err != nil {
		h.log.Warn("chroot seed: list HelmReleases failed", "depId", dep.ID, "err", err)
		return
	}
	if len(snap) == 0 {
		return
	}
	dep.mu.Lock()
	bridge := dep.jobsBridge
	if bridge == nil {
		bridge = jobs.NewBridge(h.jobs, dep.ID)
		dep.jobsBridge = bridge
	}
	dep.mu.Unlock()
	seeds := snapshotsToSeeds(snap)
	jobsCount, execsSeeded, err := bridge.SeedJobsFromInformerList(seeds)
	if err != nil {
		h.log.Warn("chroot seed: bridge seed failed", "depId", dep.ID, "err", err)
		return
	}
	h.log.Info("chroot seed: per-deployment jobs.Store populated from live cluster",
		"depId", dep.ID,
		"helmReleases", len(snap),
		"jobsWritten", jobsCount,
		"executionsSeeded", execsSeeded,
	)
}

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
	// Issue #689 — ownership check. If the deployment is unknown the
	// in-memory map returns no entry; treat that as 404. If the entry
	// exists, the helper writes 404 on cross-tenant access.
	dep := h.chrootEnsureDeployment(depID)
	if dep == nil {
		if val, ok := h.deployments.Load(depID); ok {
			dep = val.(*Deployment)
		}
	}
	if dep != nil {
		if !h.checkOwnership(w, r, dep) {
			return
		}
		// Chroot lazy seed — populates the per-deployment jobs.Store
		// from a one-shot live HelmRelease list when running on the
		// Sovereign cluster itself. Mother behaviour unchanged.
		h.chrootSeedJobsStoreIfEmpty(r.Context(), dep)
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
	// Issue #689 — ownership check (404 on cross-tenant).
	dep := h.chrootEnsureDeployment(depID)
	if dep == nil {
		if val, ok := h.deployments.Load(depID); ok {
			dep = val.(*Deployment)
		}
	}
	if dep != nil {
		if !h.checkOwnership(w, r, dep) {
			return
		}
		// Chroot lazy seed — same path as ListJobs, ensures GetJob
		// returns the rich record on first hit even if ListJobs was
		// never called.
		h.chrootSeedJobsStoreIfEmpty(r.Context(), dep)
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
