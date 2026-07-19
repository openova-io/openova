package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/provisioning/store"
)

// #5234 — terminal per-Org commit failures must surface as a machine-readable
// red step IMMEDIATELY, not after the 10-minute pod wait.
//
// On hw274 the funnel's purchased-app commit to the per-Org
// `<slug>/catalyst-tenant` repo exhausted its ref-race retries ("ref-race
// persisted after 5 attempts"), but the only state that changed right away was
// the day-2 Job + a provision.app_failed event. The funnel provision timeline
// — the state the /launching interstitial and the marketplace re-visit gate
// can render — kept "Deploying WordPress" running until the pod wait timed out
// 10 minutes later, and the customer status stayed "provisioning" the whole
// time (UAT rows 86/91). The helpers here close that gap: when the step-0
// commit fails terminally, the in-flight provision for the Org is failed on
// the matching "Deploying …" step at once, and the provision.failed event
// flips the customer status to "failed" through the existing consumer chain.

// commitFailProvisionStore is the narrow store surface the terminal
// commit-failure wiring needs. *store.Store satisfies it in production; tests
// inject a fake so the red-step propagation is provable without a live Mongo
// (same idiom as provisionDedupStore, #3744).
type commitFailProvisionStore interface {
	GetInFlightProvisionByTenant(ctx context.Context, tenantID string) (*store.Provision, error)
	UpdateProvision(ctx context.Context, id string, p *store.Provision) error
}

// commitFailureStepIndex picks the funnel-timeline step to paint red when the
// per-Org git commit exhausts terminally: the app's own "Deploying <name>"
// step when one matches the failed install's slug, else the first
// "Deploying …" step, else the "Committing manifests to Git" step, else step
// 0. Pure so the selection is unit-testable without a store.
func commitFailureStepIndex(steps []store.ProvisionStep, appSlug string) int {
	slugLower := strings.ToLower(strings.TrimSpace(appSlug))
	firstDeploying := -1
	committing := -1
	for i, s := range steps {
		n := strings.ToLower(s.Name)
		if strings.HasPrefix(n, "deploying") {
			if firstDeploying < 0 {
				firstDeploying = i
			}
			if slugLower != "" && strings.Contains(n, slugLower) {
				return i
			}
		}
		if committing < 0 && strings.Contains(n, "committing manifests") {
			committing = i
		}
	}
	if firstDeploying >= 0 {
		return firstDeploying
	}
	if committing >= 0 {
		return committing
	}
	return 0
}

// failActiveProvisionForCommitError surfaces a terminally-failed per-Org git
// commit on the Org's in-flight funnel provision (#5234). No-op when there is
// no in-flight provision (a plain day-2 install on an already-active Org —
// the Job + provision.app_failed event carry the state there) or when the
// handler has no store wired.
func (h *Handler) failActiveProvisionForCommitError(ctx context.Context, data appChangeData, commitErr error) {
	if h.Store == nil || data.TenantID == "" {
		return
	}
	h.failActiveProvisionOn(ctx, h.Store, data, commitErr)
}

// failActiveProvisionOn is failActiveProvisionForCommitError against the
// narrow store surface (testable with a fake). It mirrors failProvision's
// semantics — mark the chosen step failed, fail the provision, publish
// provision.failed — but resolves the provision by the Org instead of by ID,
// because the day-2 install path does not know the funnel's provision ID.
func (h *Handler) failActiveProvisionOn(ctx context.Context, st commitFailProvisionStore, data appChangeData, commitErr error) {
	p, err := st.GetInFlightProvisionByTenant(ctx, data.TenantID)
	if err != nil {
		slog.Error("terminal commit failure: could not resolve in-flight provision (#5234)",
			"tenant_id", data.TenantID, "error", err)
		return
	}
	if p == nil {
		return
	}

	msg := fmt.Sprintf("git commit to per-Org repo failed: %s", commitErr)
	idx := commitFailureStepIndex(p.Steps, data.AppSlug)
	if idx < len(p.Steps) && p.Steps[idx].Status != "failed" {
		p.Steps[idx].Status = "failed"
		p.Steps[idx].Message = msg
		p.Steps[idx].DoneAt = time.Now().UTC()
	}
	p.Status = "failed"
	if err := st.UpdateProvision(ctx, p.ID, p); err != nil {
		slog.Error("terminal commit failure: could not mark provision failed (#5234)",
			"provision_id", p.ID, "tenant_id", data.TenantID, "error", err)
		return
	}

	// provision.failed → the downstream consumer flips the customer status to
	// "failed" (the same chain failProvision uses), so the marketplace re-visit
	// gate and the per-Org console see the terminal state immediately.
	h.publishEvent(ctx, "provision.failed", data.TenantID, map[string]string{
		"provision_id": p.ID,
		"error":        msg,
	})

	slog.Error("provision failed on terminal per-Org commit error (#5234)",
		"provision_id", p.ID, "tenant_id", data.TenantID,
		"app_slug", data.AppSlug, "step", idx, "error", commitErr.Error())
}
