package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/provisioning/store"
)

// StartPodTruthReconciler periodically walks every non-terminal provision
// record, looks at the actual pod state for each tenant via the host's
// synced pod list, and advances stuck step records + emits the
// provision.app_ready events that onAppReady wires into tenant.app_states.
//
// Why this exists: the provisioning workflow is a long-running goroutine.
// If the pod dies mid-provision (CI deploy, OOM, node drain), the workflow
// is orphaned — provision.steps stays "running"/"pending" forever even
// though the apps actually came up via Flux's independent reconciliation.
// The result was a UI that showed "INSTALLING" long after pods were
// Ready — exactly the stale-truth complaint in issue #114.
//
// Cadence: every 30s. Cheap: one LIST per active tenant namespace, a few
// $set updates on provisions/tenants when drift is detected. Issue #115.
func (h *Handler) StartPodTruthReconciler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		h.reconcilePodTruth(ctx) // run one pass at startup
		for {
			select {
			case <-ctx.Done():
				slog.Info("pod-truth reconciler stopping")
				return
			case <-ticker.C:
				h.reconcilePodTruth(ctx)
			}
		}
	}()
	slog.Info("pod-truth reconciler started")
}

// reconcilePodTruth is one pass. Exposed so tests can drive it.
func (h *Handler) reconcilePodTruth(ctx context.Context) {
	// Scan recent provisions. The store doesn't have a ListByStatus helper
	// yet; the active set is small so we can filter in-memory cheaply.
	all, err := h.Store.ListProvisions(ctx, 0, 200)
	if err != nil {
		slog.Debug("pod-truth: list provisions failed", "error", err)
		return
	}
	for i := range all {
		p := &all[i]
		// Ignore terminal states — failed is user-visible and intentionally
		// frozen; completed has nothing to advance.
		if p.Status != "provisioning" {
			continue
		}
		if p.Subdomain == "" {
			continue
		}
		h.reconcileOneProvision(ctx, p)
	}
}

// reconcileOneProvision looks at pod state for a single tenant and advances
// its provision record + publishes app_ready events for any apps that have
// gone Ready but whose step still says pending/running.
func (h *Handler) reconcileOneProvision(ctx context.Context, p *store.Provision) {
	hostNS := "tenant-" + p.Subdomain
	// Slug→ID map so we can emit provision.app_ready with the `app_id`
	// field onAppReady expects (it ignores payloads without an id).
	slugToID := map[string]string{}
	if apps, ok := h.fetchCatalogApps(ctx); ok {
		for _, a := range apps {
			slugToID[a.Slug] = a.ID
		}
	}
	body, err := h.k8sGet("/api/v1/namespaces/" + hostNS + "/pods")
	if err != nil {
		// Namespace gone or not yet up — skip. We do NOT fail the
		// provision here; a short-lived 404 during initial namespace
		// creation shouldn't terminate a provision the main goroutine
		// is still working on.
		return
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Phase      string `json:"phase"`
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return
	}

	// Map app-slug → ready. vCluster syncer names synced pods
	// "<pod-name>-x-<inner-ns>-x-<vcluster-name>". For our tenants
	// the inner-ns happens to be the tenant host ns itself and the
	// vcluster-name is "vcluster", so the observed pattern is:
	//   wordpress-abcdef-xyz-x-tenant-<slug>-x-vcluster
	// We don't care what the inner-ns is — the only thing that matters
	// is extracting the leading "<app-slug>-<deployHash>-<podHash>"
	// portion, then stripping the two trailing hash segments to get
	// the slug. App slugs may contain hyphens (uptime-kuma, cal-com).
	const vcSuffix = "-x-vcluster"
	ready := map[string]bool{}
	for _, pod := range list.Items {
		name := pod.Metadata.Name
		if !strings.HasSuffix(name, vcSuffix) {
			continue
		}
		// Strip trailing "-x-<inner-ns>-x-vcluster": find the last
		// "-x-" inside the pod name (minus the vcluster suffix).
		core := strings.TrimSuffix(name, vcSuffix)
		idx := strings.LastIndex(core, "-x-")
		if idx < 0 {
			continue
		}
		podPart := core[:idx] // "<slug>-<deployHash>-<podHash>"
		// Skip infra pods (coredns, vcluster-0 — don't have the
		// <deployHash>-<podHash> shape).
		if strings.HasPrefix(podPart, "coredns") || strings.HasPrefix(podPart, "vcluster") {
			continue
		}
		if pod.Status.Phase != "Running" {
			continue
		}
		isReady := false
		for _, c := range pod.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				isReady = true
				break
			}
		}
		if !isReady {
			continue
		}
		parts := strings.Split(podPart, "-")
		if len(parts) < 3 {
			continue
		}
		slug := strings.Join(parts[:len(parts)-2], "-")
		ready[slug] = true
	}
	if len(ready) == 0 {
		return
	}

	// Walk each step. When it matches "Deploying <app>" or "Installing <svc>
	// (dependency)" AND the app/svc is Ready, mark completed.
	// advanced is referenced in slog contexts below to signal a "first
	// heal" vs "steady-state" tick. Not used for flow control after #119.
	advanced := false
	_ = advanced
	stepAdvanced := map[int]bool{}
	for i, step := range p.Steps {
		if step.Status == "completed" || step.Status == "failed" {
			continue
		}
		slug := slugFromStepName(step.Name)
		if slug == "" || !ready[slug] {
			continue
		}
		slog.Warn("pod-truth: advancing stuck step — pod is Ready",
			"tenant", p.Subdomain, "step", step.Name, "slug", slug)
		h.markStep(ctx, p.ID, i, step.Name, "running")  // running first if was pending
		h.completeStep(ctx, p.ID, p.TenantID, i, step.Name, len(p.Steps))
		stepAdvanced[i] = true
		advanced = true
		// Publish provision.app_ready so the tenant service clears the
		// app_states "installing" marker for this app. The tenant
		// consumer already handles this event idempotently — a duplicate
		// arriving after the main goroutine's own publish is a no-op.
		h.publishEvent(ctx, "provision.app_ready", p.TenantID, map[string]any{
			"app_slug":   slug,
			"app_id":     slugToID[slug],
			"deploy_ids": []string{slugToID[slug]},
			"action":     "install",
			"source":     "pod-truth-reconciler",
		})
	}

	// Infrastructure tail steps ("Configuring TLS certificates", "Running
	// health checks") don't map to a specific app slug, so the per-app
	// loop above skipped them. When every app step is already completed
	// AND every expected app pod is Ready, it's safe to auto-complete
	// these too — otherwise the overall provision.status sits at
	// 'provisioning' forever and the UI shows a stale banner.
	allAppStepsCompleted := true
	for _, s := range p.Steps {
		if slugFromStepName(s.Name) != "" && s.Status != "completed" {
			allAppStepsCompleted = false
			break
		}
	}
	if allAppStepsCompleted && len(ready) > 0 {
		tailNames := map[string]bool{
			"Configuring TLS certificates": true,
			"Running health checks":        true,
			"Provisioning vCluster":        true, // in case orphan left this mid-way
		}
		for i, step := range p.Steps {
			if step.Status == "completed" || step.Status == "failed" {
				continue
			}
			if !tailNames[step.Name] {
				continue
			}
			slog.Warn("pod-truth: auto-completing tail step — all apps Ready",
				"tenant", p.Subdomain, "step", step.Name)
			h.markStep(ctx, p.ID, i, step.Name, "running")
			h.completeStep(ctx, p.ID, p.TenantID, i, step.Name, len(p.Steps))
			advanced = true
		}
	}

	// Day-2 installs don't show up as provision steps (each fires its own
	// runInstallJob record). If their goroutine was orphaned by a pod
	// rollout, app_states.<id>=installing stays set forever even when
	// the pod is Ready. Emit provision.app_ready for every Ready app that
	// WASN'T already handled via a step above so onAppReady clears it.
	for slug := range ready {
		if _, hadStep := stepAdvanced[-1]; hadStep { /* prevent unused-var false positive */ }
		// A Ready app has either been matched by a step (already
		// published) or it's a day-2 install (no step). Check whether
		// any step name maps to this slug — if so, skip.
		handledByStep := false
		for _, step := range p.Steps {
			if slugFromStepName(step.Name) == slug {
				handledByStep = true
				break
			}
		}
		if handledByStep {
			continue
		}
		id := slugToID[slug]
		if id == "" {
			continue // can't clear without an id; onAppReady keys on ids
		}
		slog.Info("pod-truth: emitting app_ready for day-2 app with no step entry",
			"tenant", p.Subdomain, "slug", slug, "app_id", id)
		h.publishEvent(ctx, "provision.app_ready", p.TenantID, map[string]any{
			"app_slug":   slug,
			"app_id":     id,
			"deploy_ids": []string{id},
			"action":     "install",
			"source":     "pod-truth-reconciler",
		})
		advanced = true
	}

	// Day-2 jobs (store.Job records) stay in 'pending' forever if their
	// goroutine was orphaned by a pod rollout. Two cases:
	//   A) Pod is still Running → install clearly succeeded, mark job succeeded.
	//   B) Pod is gone but there's a newer SUCCEEDED uninstall for the
	//      same app → the app installed, user later removed it; the install
	//      must have succeeded at some point. Back-fill as succeeded.
	jobs, err := h.Store.ListJobsByTenant(ctx, p.TenantID, 200)
	if err == nil {
		now := time.Now().UTC()
		// First pass: collect succeeded uninstalls per slug so we can
		// decide case B below without an O(n^2) nested scan.
		uninstalled := map[string]bool{}
		for i := range jobs {
			j := &jobs[i]
			if j.Kind == "uninstall" && j.Status == "succeeded" {
				uninstalled[j.AppSlug] = true
			}
		}
		for i := range jobs {
			j := &jobs[i]
			if j.Status == "succeeded" || j.Status == "failed" {
				continue
			}
			if j.Kind != "install" {
				continue
			}
			healReason := ""
			if ready[j.AppSlug] {
				healReason = "pod is Ready"
			} else if uninstalled[j.AppSlug] {
				healReason = "app later uninstalled successfully — install must have completed"
			} else {
				continue
			}
			slog.Warn("pod-truth: advancing stuck day-2 install job",
				"tenant", p.Subdomain, "slug", j.AppSlug,
				"job_id", j.ID, "reason", healReason)
			for k := range j.Steps {
				if j.Steps[k].Status == "completed" || j.Steps[k].Status == "failed" {
					continue
				}
				j.Steps[k].Status = "completed"
				j.Steps[k].DoneAt = now
				if j.Steps[k].Message == "" {
					j.Steps[k].Message = healReason
				}
			}
			j.Status = "succeeded"
			j.Progress = 100
			j.UpdatedAt = now
			if uerr := h.Store.UpdateJob(ctx, j.ID, j); uerr != nil {
				slog.Warn("pod-truth: update job failed", "job_id", j.ID, "error", uerr)
				continue
			}
			advanced = true
		}
	}

	// If every step is already completed, mark provision overall as
	// completed too — regardless of whether this pass advanced anything.
	// The main goroutine can finish the last step OK but fail to flip
	// the overall record's status=completed if the pod rolls during the
	// finalize. Without this unconditional check the status would stay
	// 'provisioning' forever and the UI shows 'Running 9/9' (exactly
	// what the user saw on emrah5). Issue #119.
	allDone := true
	for _, s := range p.Steps {
		if s.Status != "completed" {
			allDone = false
			break
		}
	}
	if allDone {
		slog.Info("pod-truth: all steps completed — marking provision succeeded",
			"tenant", p.Subdomain, "provision_id", p.ID)
		p.Status = "completed"
		p.Progress = 100
		if err := h.Store.UpdateProvision(ctx, p.ID, p); err == nil {
			h.publishEvent(ctx, "provision.completed", p.TenantID, map[string]any{
				"tenant_id": p.TenantID,
				"subdomain": p.Subdomain,
			})
		}
	}
}

// slugFromStepName extracts the app slug from step names like:
//
//	"Deploying WordPress"              -> "wordpress"
//	"Deploying Uptime Kuma"            -> "uptime-kuma"
//	"Installing mysql (dependency)"    -> "mysql"
//	"Creating tenant"                  -> ""
//
// Returns "" for non-app steps (namespace, vcluster, TLS, health checks).
func slugFromStepName(name string) string {
	switch {
	case strings.HasPrefix(name, "Deploying "):
		return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, "Deploying "), " ", "-"))
	case strings.HasPrefix(name, "Installing "):
		// e.g. "Installing mysql (dependency)"
		rest := strings.TrimPrefix(name, "Installing ")
		if idx := strings.Index(rest, " "); idx > 0 {
			rest = rest[:idx]
		}
		return strings.ToLower(rest)
	}
	return ""
}
