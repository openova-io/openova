package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// pendingInstallReconcileInterval is how often the self-heal loop re-attempts
// parked day-2 cart installs. The per-Org Gitea org/repo is created by
// organization-controller seconds-to-minutes after the Org CR; a parked install
// is one whose in-line commit budget (#4404, ~3 min) was exhausted before the
// repo appeared. Re-attempting every 30s drains the queue within a cadence of
// the repo finally existing, while keeping steady-state cost negligible (one
// commit attempt per parked install per tick, and the queue is empty in the
// common case). Var (not const) so the test can drive it to near-zero.
var pendingInstallReconcileInterval = 30 * time.Second

// pendingInstallMaxAge bounds how long a parked install keeps being re-attempted
// before it is given up as a genuine failure (a permanently-missing Org/repo,
// not just a slow create). The org-controller creates the per-Org repo well
// within a few minutes even on a busy Sovereign, so 30 minutes is a wide margin
// that recovers every realistic latency yet still surfaces a truly broken Org as
// a failed Job rather than retrying forever. Var so the test can shorten it.
var pendingInstallMaxAge = 30 * time.Minute

// StartPendingInstallReconciler drains the pending-install registry: it
// periodically re-attempts the step-0 commit for every day-2 cart install whose
// in-line retry budget exhausted while the per-Org Gitea org/repo did not exist
// yet (#4404). Once the org-controller has created the repo, the re-attempt's
// commit lands, the wait tail is launched, and the purchased app installs —
// entirely zero-touch, robust to ANY per-Org-create latency.
//
// This is the durable half of the #4404 fix. The in-line widened budget covers
// the common (fast-create) case without ever parking; this loop covers the slow
// tail (and a pod restart between dispatch and create) so no latency can drop
// the purchased app forever. Modeled on StartKubeconfigReconciler (#104) /
// StartPodTruthReconciler (#115): cheap, stateless, ctx-stoppable.
func (h *Handler) StartPendingInstallReconciler(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(pendingInstallReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				slog.Info("pending-install reconciler stopping")
				return
			case <-ticker.C:
				h.reconcilePendingInstalls(ctx)
			}
		}
	}()
	slog.Info("pending-install reconciler started")
}

// reconcilePendingInstalls is one pass of the #4404 self-heal loop. Exposed
// (unexported to the package) so tests can invoke it directly.
func (h *Handler) reconcilePendingInstalls(ctx context.Context) {
	parked := h.pendingInstalls.Snapshot()
	if len(parked) == 0 {
		return
	}
	committed := 0
	for _, p := range parked {
		if ctx.Err() != nil {
			return
		}
		data := p.data
		job := p.job

		// Age-out: a parked install that has been re-attempted past the max age
		// is a genuinely broken Org (repo never appeared), not a slow create.
		// Fail its Job so it surfaces instead of retrying forever.
		if time.Since(p.enqueuedAt) > pendingInstallMaxAge {
			msg := fmt.Sprintf("per-Org Gitea repo never became ready after %s — giving up (#4404)", pendingInstallMaxAge)
			if p.lastErr != "" {
				msg = msg + ": " + p.lastErr
			}
			slog.Error("pending-install reconciler: parked install aged out — failing job",
				"tenant", data.TenantSlug, "app_slug", data.AppSlug, "age", time.Since(p.enqueuedAt).String())
			h.pendingInstalls.Remove(data)
			h.markJobStep(ctx, job, 0, "failed", msg)
			h.finalizeJob(ctx, job, "failed", msg)
			h.publishEvent(ctx, "provision.app_failed", data.TenantID, map[string]any{
				"app_slug":   data.AppSlug,
				"app_id":     data.AppID,
				"deploy_ids": data.DeployIDs,
				"action":     "install",
				"error":      msg,
			})
			// #5234 — the purchased app can never deploy from this dispatch;
			// paint the funnel timeline red now instead of leaving the
			// "Deploying <app>" step running.
			h.failActiveProvisionForCommitError(ctx, data, fmt.Errorf("%s", msg))
			continue
		}

		// Re-attempt the commit. Use a SINGLE applyTenantChange (not the in-line
		// budgeted retry) per tick — the tick cadence IS the backoff between
		// attempts, so we don't want to hold the loop for minutes on one record.
		err := h.applyTenantChange(ctx, data, "install")
		if err == nil {
			// Commit landed — the per-Org repo finally exists. Mark step-0 done
			// and launch the same async wait tail the synchronous path uses so
			// the Job drives to a terminal state and the app_ready event fires.
			slog.Info("pending-install reconciler: parked install committed — self-heal succeeded (#4404)",
				"tenant", data.TenantSlug, "app_slug", data.AppSlug)
			h.pendingInstalls.Remove(data)
			h.markJobStep(ctx, job, 0, "completed", "ok (self-healed after Gitea repo became ready)")
			h.launchInstallWaitTail(data, job)
			committed++
			continue
		}
		if isGiteaNotReadyError(err) {
			// Still racing the org-controller — keep the record parked and try
			// again next tick.
			h.pendingInstalls.MarkAttempt(data, err.Error())
			slog.Debug("pending-install reconciler: per-Org Gitea repo still not ready — re-parking (#4404)",
				"tenant", data.TenantSlug, "app_slug", data.AppSlug, "error", err.Error())
			continue
		}
		// A permanent error (bad manifest, auth, malformed slug) appeared on the
		// re-attempt — fail the Job; retrying won't help.
		slog.Error("pending-install reconciler: parked install hit a permanent error — failing job",
			"tenant", data.TenantSlug, "app_slug", data.AppSlug, "error", err.Error())
		h.pendingInstalls.Remove(data)
		h.markJobStep(ctx, job, 0, "failed", err.Error())
		h.finalizeJob(ctx, job, "failed", err.Error())
		h.publishEvent(ctx, "provision.app_failed", data.TenantID, map[string]any{
			"app_slug":   data.AppSlug,
			"app_id":     data.AppID,
			"deploy_ids": data.DeployIDs,
			"action":     "install",
			"error":      err.Error(),
		})
		// #5234 — same terminal-state propagation as the synchronous path:
		// fail the in-flight funnel provision immediately (red step +
		// provision.failed → customer status "failed").
		h.failActiveProvisionForCommitError(ctx, data, err)
	}
	if committed > 0 {
		slog.Info("pending-install reconciler: self-healed parked installs", "count", committed)
	}
}
