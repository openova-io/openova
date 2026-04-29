// Phase-retry endpoint for the wizard's failed-phase UX (issue #125).
//
// When a provisioning phase fails, the wizard renders the failed phase
// with a "Retry phase" button. This endpoint accepts that retry and
// re-drives the phase, distinguishing two architectural cases:
//
//  1. Phase 0 phases (tofu-init, tofu-plan, tofu-apply, tofu-output,
//     flux-bootstrap) — catalyst-api owns the OpenTofu workdir directly,
//     so we re-run `tofu apply` against the same workdir. Re-runs are
//     idempotent (OpenTofu's state model). This is in-bounds: Phase 0
//     IS the catalyst-api's job per docs/SOVEREIGN-PROVISIONING.md §3.
//
//  2. Phase 1 bootstrap-kit phases (cilium, cert-manager, flux,
//     crossplane, sealed-secrets, spire, jetstream, openbao, keycloak,
//     gitea, bp-catalyst-platform) — these are Flux HelmReleases on the
//     NEW Sovereign's cluster. Per docs/INVIOLABLE-PRINCIPLES.md #3
//     ("Flux is the ONLY GitOps reconciler") and Lesson #24, the
//     catalyst-api MUST NOT exec kubectl/helm to drive Phase 1. Flux
//     itself has built-in retry (HelmRelease.spec.install.remediation.
//     retries: 3) which handles transient failures automatically.
//
//     For operator-driven retries (after the automatic retry exhausts),
//     the documented path is the Flux Receiver webhook published by
//     bp-catalyst-platform — the wizard POSTs the receiver token + the
//     specific HelmRelease name, and the new Sovereign's notification-
//     controller annotates the HelmRelease for fresh reconciliation.
//     The receiver URL + token are Phase 0 outputs that flow through
//     the OpenTofu module's flux-bootstrap step. Until the receiver is
//     wired through cloud-init (separate ticket — outside the UX scope
//     of #125), this endpoint emits a structured event pointing the
//     operator at the runbook's "Rollback procedures per phase" section
//     for manual `flux reconcile helmrelease` instructions.
//
// In both cases, the endpoint streams events back through the same
// SSE channel as the original deployment — the wizard's BootstrapProgress
// widget continues to render the live state without needing a second
// stream. We re-open the deployment.Events channel by replacing it on
// the Deployment struct (after the original channel closed when
// runProvisioning finished).
package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// phase0Phases — the OpenTofu phases this catalyst-api directly owns.
// Re-running these drives `tofu apply` against the per-deployment
// workdir, which is idempotent.
var phase0Phases = map[string]bool{
	"tofu-init":      true,
	"tofu-plan":      true,
	"tofu-apply":     true,
	"tofu-output":    true,
	"flux-bootstrap": true,
}

// phase1Phases — the bootstrap-kit HelmReleases reconciled by Flux on
// the NEW Sovereign. catalyst-api does NOT exec kubectl on these per
// architectural contract — Flux owns the retry loop.
var phase1Phases = map[string]bool{
	"cilium":               true,
	"cert-manager":         true,
	"flux":                 true,
	"crossplane":           true,
	"sealed-secrets":       true,
	"spire":                true,
	"jetstream":            true,
	"openbao":              true,
	"keycloak":             true,
	"gitea":                true,
	"bp-catalyst-platform": true,
}

// RetryPhase handles POST /api/v1/deployments/:id/phases/:phase/retry.
//
// Response:
//
//	200 — retry accepted, streamURL points to the (refreshed) SSE channel
//	400 — unknown phase id
//	404 — unknown deployment id
//	409 — deployment is still in-flight; can't retry while running
func (h *Handler) RetryPhase(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	phase := chi.URLParam(r, "phase")

	val, ok := h.deployments.Load(id)
	if !ok {
		http.Error(w, "deployment not found", http.StatusNotFound)
		return
	}
	dep := val.(*Deployment)

	dep.mu.Lock()
	stillRunning := dep.Status == "provisioning" || dep.Status == "tofu-applying"
	dep.mu.Unlock()
	if stillRunning {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "deployment is still in-flight — wait for the current phase to finish before retrying",
		})
		return
	}

	switch {
	case phase0Phases[phase]:
		h.retryPhase0(w, dep, phase)
	case phase1Phases[phase]:
		h.retryPhase1(w, dep, phase)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unknown phase %q — see docs/RUNBOOK-PROVISIONING.md for the canonical phase list", phase),
		})
	}
}

// retryPhase0 re-drives the OpenTofu workflow against the deployment's
// existing workdir. The retry runs the FULL phase 0 sequence (init →
// plan → apply → output → flux-bootstrap) because OpenTofu's plan/apply
// model is "the whole stack converges to declared state," not "re-run
// only this step." Idempotency means failed-on-apply with a transient
// error (e.g. Hetzner rate-limit) becomes a successful apply on retry.
func (h *Handler) retryPhase0(w http.ResponseWriter, dep *Deployment, phase string) {
	// Re-open the events channel + done signal — the originals were closed
	// when runProvisioning returned. The wizard's SSE client reconnects
	// to /logs which reads from this fresh channel and replays the buffer
	// (which still carries the previous attempt's events plus the retry
	// banner). Buffer eviction at EventBufferCap prevents unbounded growth
	// across many retries.
	dep.mu.Lock()
	dep.eventsCh = make(chan provisioner.Event, 256)
	dep.done = make(chan struct{})
	dep.Status = "provisioning"
	dep.Error = ""
	dep.FinishedAt = time.Time{}
	dep.mu.Unlock()

	go h.runProvisioningRetry(dep, phase)

	writeJSON(w, http.StatusOK, map[string]string{
		"id":        dep.ID,
		"status":    "provisioning",
		"phase":     phase,
		"streamURL": fmt.Sprintf("/api/v1/deployments/%s/logs", dep.ID),
		"message":   fmt.Sprintf("Phase 0 retry accepted — re-running tofu apply against the existing workdir (idempotent). Reopen the SSE stream to follow progress."),
	})
}

// retryPhase1 emits a structured event explaining that Flux owns the
// HelmRelease retry loop and pointing the operator at the runbook for
// manual reconciliation if Flux's automatic remediation has already
// exhausted (`install.remediation.retries: 3`).
//
// We do NOT exec kubectl here — that would violate Lesson #24. The
// architectural retry primitive for Phase 1 is Flux's own
// remediation, plus a notification-controller Receiver webhook on the
// new Sovereign (wired through a separate ticket).
func (h *Handler) retryPhase1(w http.ResponseWriter, dep *Deployment, phase string) {
	dep.mu.Lock()
	dep.eventsCh = make(chan provisioner.Event, 16)
	dep.done = make(chan struct{})
	dep.mu.Unlock()

	// Emit the structured event into a goroutine so the SSE client
	// reconnecting to /logs sees it immediately and can render it. We
	// record the event into the durable buffer too so a late connection
	// after `done` fires still sees the operator instructions.
	go func() {
		defer close(dep.eventsCh)
		defer close(dep.done)
		ev := provisioner.Event{
			Time:  time.Now().UTC().Format(time.RFC3339),
			Phase: phase,
			Level: "info",
			Message: "Phase 1 retry: this HelmRelease is reconciled by Flux on the new Sovereign (not by catalyst-api). " +
				"Flux applies install.remediation.retries=3 automatically; if those exhausted, the operator runs " +
				"`kubectl annotate --overwrite helmrelease/bp-" + phase + " -n flux-system reconcile.fluxcd.io/requestedAt=$(date +%s)` " +
				"on the new Sovereign's kube-context. See docs/RUNBOOK-PROVISIONING.md " +
				"§Rollback-procedures-per-phase for the full procedure.",
		}
		recorded := dep.recordEvent(ev)
		select {
		case dep.eventsCh <- recorded:
		default:
		}
	}()

	writeJSON(w, http.StatusOK, map[string]string{
		"id":        dep.ID,
		"status":    "manual-retry-required",
		"phase":     phase,
		"streamURL": fmt.Sprintf("/api/v1/deployments/%s/logs", dep.ID),
		"runbook":   "docs/RUNBOOK-PROVISIONING.md#rollback-procedures-per-phase",
		"message":   fmt.Sprintf("Phase 1 (%s) is owned by Flux on the new Sovereign — operator action required if automatic remediation exhausted.", phase),
	})
}

// runProvisioningRetry mirrors runProvisioning but re-uses the existing
// deployment workdir (no fresh fqdn check, no fresh tofu init if .terraform/
// already exists). The provisioner.Provision call itself is idempotent
// against an existing workdir.
func (h *Handler) runProvisioningRetry(dep *Deployment, retriedPhase string) {
	// Tee — same pattern as runProvisioning so the durable event buffer
	// captures the retry's events too. This is what makes a reconnect to
	// /logs after a retry has finished still render the full retry history.
	producer := make(chan provisioner.Event, 256)
	teeDone := make(chan struct{})
	go func() {
		defer close(teeDone)
		for ev := range producer {
			recorded := dep.recordEvent(ev)
			select {
			case dep.eventsCh <- recorded:
			default:
			}
		}
		close(dep.eventsCh)
	}()

	prov := provisioner.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	producer <- provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   retriedPhase,
		Level:   "info",
		Message: fmt.Sprintf("Retry initiated for phase %q — running `tofu apply` against existing workdir (idempotent).", retriedPhase),
	}

	result, err := prov.Provision(ctx, dep.Request, producer)
	close(producer)
	<-teeDone

	dep.mu.Lock()
	dep.FinishedAt = time.Now()
	if err != nil {
		dep.Status = "failed"
		dep.Error = err.Error()
		h.log.Error("retry provision failed", "id", dep.ID, "phase", retriedPhase, "err", err)
	} else {
		dep.Status = "ready"
		dep.Result = result
		h.log.Info("retry provision complete", "id", dep.ID, "phase", retriedPhase)
	}
	dep.mu.Unlock()
	close(dep.done)
}

// validatePhaseID — exported helper for tests.
func validatePhaseID(phase string) error {
	if strings.TrimSpace(phase) == "" {
		return errors.New("phase id required")
	}
	if !phase0Phases[phase] && !phase1Phases[phase] {
		return fmt.Errorf("unknown phase %q", phase)
	}
	return nil
}
