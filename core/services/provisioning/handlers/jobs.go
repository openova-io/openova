package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// ListJobs handles GET /provisioning/jobs?tenant_id=X&limit=N. Returns the
// tenant's day-2 install/uninstall jobs newest-first so the console Jobs page
// can render them alongside the initial tenant provision.
func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	tenantID := r.URL.Query().Get("tenant_id")
	if tenantID == "" {
		respond.Error(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	jobs, err := h.Store.ListJobsByTenant(r.Context(), tenantID, limit)
	if err != nil {
		slog.Error("list jobs", "tenant_id", tenantID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}
	respond.OK(w, jobs)
}

// newInstallJob seeds an install Job with the three standard steps the UI
// renders: git commit, pod ready, reconciliation complete. The caller marks
// each step running/completed as it progresses.
//
// Dedup contract (issue #71): when data.IdempotencyKey is set we use
// CreateJobIfAbsent; if another writer already claimed the key we return nil
// to signal the caller to skip. An empty IdempotencyKey falls back to the
// old unbounded CreateJob for backward compat with events published before
// the tenant-service fix rolled out — the call still succeeds but does NOT
// dedup. The deploy sequence (tenant → provisioning) rolls the producer out
// before the consumer relies on the key, so empty keys should only appear
// during the transitional window.
func (h *Handler) newInstallJob(ctx context.Context, data appChangeData) *store.Job {
	appName := h.resolveSingleAppName(ctx, data.AppID, data.AppSlug)
	job := &store.Job{
		TenantID:       data.TenantID,
		TenantSlug:     data.TenantSlug,
		Kind:           "install",
		AppSlug:        data.AppSlug,
		AppID:          data.AppID,
		AppName:        appName,
		IdempotencyKey: data.IdempotencyKey,
		Status:         "pending",
		Steps: []store.JobStep{
			{Name: "Committing manifests to Git", Status: "pending"},
			{Name: fmt.Sprintf("Waiting for %s to be ready", appName), Status: "pending"},
			{Name: "Installation complete", Status: "pending"},
		},
	}
	if data.IdempotencyKey != "" {
		if err := h.Store.CreateJobIfAbsent(ctx, job); err != nil {
			if errors.Is(err, store.ErrJobExists) {
				return nil
			}
			slog.Error("create install job (dedup)", "error", err, "tenant_id", data.TenantID)
		}
		return job
	}
	if err := h.Store.CreateJob(ctx, job); err != nil {
		slog.Error("create install job", "error", err, "tenant_id", data.TenantID)
	}
	return job
}

// newUninstallJob mirrors newInstallJob for the uninstall path. purged and
// retained list which backing-service slugs will / will not be dropped.
// Same dedup contract as newInstallJob.
func (h *Handler) newUninstallJob(ctx context.Context, data appChangeData, purged, retained []string) *store.Job {
	appName := h.resolveSingleAppName(ctx, data.AppID, data.AppSlug)
	job := &store.Job{
		TenantID:         data.TenantID,
		TenantSlug:       data.TenantSlug,
		Kind:             "uninstall",
		AppSlug:          data.AppSlug,
		AppID:            data.AppID,
		AppName:          appName,
		IdempotencyKey:   data.IdempotencyKey,
		Status:           "pending",
		PurgedServices:   purged,
		RetainedServices: retained,
		Steps: []store.JobStep{
			{Name: "Removing manifests from Git", Status: "pending"},
			{Name: "Waiting for Flux to prune workloads", Status: "pending"},
			{Name: "Uninstall complete", Status: "pending"},
		},
	}
	if data.IdempotencyKey != "" {
		if err := h.Store.CreateJobIfAbsent(ctx, job); err != nil {
			if errors.Is(err, store.ErrJobExists) {
				return nil
			}
			slog.Error("create uninstall job (dedup)", "error", err, "tenant_id", data.TenantID)
		}
		return job
	}
	if err := h.Store.CreateJob(ctx, job); err != nil {
		slog.Error("create uninstall job", "error", err, "tenant_id", data.TenantID)
	}
	return job
}

// markJobStep updates one step of a Job and bumps Status / Progress. When
// terminal is true the whole job's top-level Status is set to the same value
// (succeeded / failed).
func (h *Handler) markJobStep(ctx context.Context, job *store.Job, idx int, status, message string) {
	if job == nil || job.ID == "" || idx < 0 || idx >= len(job.Steps) {
		return
	}
	step := store.JobStep{
		Name:    job.Steps[idx].Name,
		Status:  status,
		Message: message,
	}
	if status == "running" {
		step.StartedAt = time.Now().UTC()
	} else if status == "completed" || status == "failed" {
		// Preserve started_at if already set.
		if !job.Steps[idx].StartedAt.IsZero() {
			step.StartedAt = job.Steps[idx].StartedAt
		}
		step.DoneAt = time.Now().UTC()
	}
	job.Steps[idx] = step
	if err := h.Store.UpdateJobStep(ctx, job.ID, idx, step); err != nil {
		slog.Error("update job step", "job_id", job.ID, "idx", idx, "error", err)
	}
}

// finalizeJob sets the job-level Status and Progress in a single write. Pass
// "succeeded" or "failed". The caller is responsible for marking all step
// rows before calling this.
func (h *Handler) finalizeJob(ctx context.Context, job *store.Job, status, message string) {
	if job == nil || job.ID == "" {
		return
	}
	job.Status = status
	job.Progress = 100
	if status == "failed" {
		// Reflect the failure on the last running step if any.
		for i := range job.Steps {
			if job.Steps[i].Status == "running" || job.Steps[i].Status == "pending" {
				job.Steps[i].Status = "failed"
				if message != "" {
					job.Steps[i].Message = message
				}
				if job.Steps[i].StartedAt.IsZero() {
					job.Steps[i].StartedAt = time.Now().UTC()
				}
				job.Steps[i].DoneAt = time.Now().UTC()
				break
			}
		}
	}
	if err := h.Store.UpdateJob(ctx, job.ID, job); err != nil {
		slog.Error("update job", "job_id", job.ID, "error", err)
	}
}

// resolveSingleAppName returns a display name for an app, falling back to the
// slug when the catalog is unreachable.
func (h *Handler) resolveSingleAppName(ctx context.Context, appID, appSlug string) string {
	if names := h.resolveAppNames(ctx); names != nil {
		if n, ok := names[appID]; ok && n != "" {
			return n
		}
	}
	if appSlug != "" {
		return appSlug
	}
	return appID
}
