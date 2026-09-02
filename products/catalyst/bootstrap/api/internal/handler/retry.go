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

// rewatchPhases — #6795 / #6799. A Phase-1 TIMEOUT (record
// status=failed, phase1Outcome=timeout) on a Sovereign that later
// converged on its own (both clusters alive, HelmReleases Ready,
// console 200) has no honest retry path: the Phase-0 retry re-runs
// `tofu apply`, which is NOT idempotent on kom4dc once the prov-time
// NAT-EIP rotation left region B's SNAT rule + EIP outside tofu state
// (plan "2 to add" → VPC.20xx, record stays failed forever), and the
// Phase-1 advisory branch above only prints an event. `phase1-watch`
// re-runs ONLY the helmwatch observer — no tofu, no cloud writes — and
// on OutcomeReady takes the SAME terminal path as a first-launch watch
// (markPhase1Done → status=ready → fireHandover). Kept out of
// phase1Phases so the Flux-owned advisory branch stays as it is.
//
// Measured live 2026-09-02 on hw307 (dep 9a1f230f320d7ff9): region A
// 64/66 HelmReleases Ready, console 200, record failed/timeout, and
// the only offered retry would have re-applied tofu.
var rewatchPhases = map[string]bool{
	"phase1-watch": true,
}

// RetryPhase handles POST /api/v1/deployments/:id/phases/:phase/retry.
//
// Response:
//
//	200 — retry accepted, streamURL points to the (refreshed) SSE channel
//	400 — unknown phase id
//	404 — unknown deployment id
//	409 — deployment is still in-flight; can't retry while running
//	409 — `phase1-watch` only: a watch is already attached, or the
//	      watcher's inputs (Result + kubeconfig) were never produced;
//	      the body's `error` names which
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
	case rewatchPhases[phase]:
		h.retryPhase1Rewatch(w, dep, phase)
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

	// Persist the retry-init state — a Pod restart between this point
	// and the first goroutine emit otherwise leaves the on-disk record
	// at "failed" from the previous run, so the wizard's Retry click
	// would silently no-op from the user's view.
	h.persistDeployment(dep)

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
		recorded := h.recordEventAndPersist(dep, ev)
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

// retryPhase1Rewatch re-attaches ONLY the Phase-1 helmwatch observer to a
// terminal deployment (#6795 / #6799). It performs no `tofu apply` and no
// cloud write of any kind: the watcher reads HelmRelease status through
// the kubeconfig the Sovereign PUT back during its first run.
//
// State transitions:
//
//	failed (phase1Outcome=timeout|…)  ──POST phase1-watch──▶  phase1-watching
//	phase1-watching  ──watch OutcomeReady──▶  ready   (+ fireHandover, same as first launch)
//	phase1-watching  ──any other outcome──▶   failed  (Error names the outcome)
//
// Preconditions (409 with a naming `error` otherwise): no watch is
// currently attached, the record is not adopted, Phase 0 produced a
// Result, and a kubeconfig exists on disk — either the stamped
// Result.KubeconfigPath or the conventional <kubeconfigsDir>/<id>.yaml
// (#3153: the stamp is `omitempty` and can be lost across a mothership
// roll while the file survives on the PVC).
//
// The reset mirrors PutKubeconfig's relaunch-after-kubeconfig-missing
// branch and resumePhase1Watch: clear the previous terminal markers,
// clear phase1Started so the at-most-once guard admits the new launch,
// allocate a fresh eventsCh/done pair (the originals were closed when
// the first run finished), and close them ourselves when the watch
// returns because runProvisioning is not on this path.
func (h *Handler) retryPhase1Rewatch(w http.ResponseWriter, dep *Deployment, phase string) {
	// Resolve the kubeconfig BEFORE taking dep.mu — the helper takes the
	// lock itself and stats the file on disk.
	kubeconfigPath, haveKubeconfig := h.resolvePrimaryKubeconfigPath(dep)

	dep.mu.Lock()
	var refuse string
	switch {
	case dep.Status == "phase1-watching" || dep.liveWatcher != nil:
		refuse = "a Phase-1 watch is already attached to this deployment — wait for it to terminate before re-watching"
	case dep.Status == "adopted":
		refuse = "deployment is already adopted — the handover has completed; nothing to re-watch"
	case dep.Result == nil:
		refuse = "Phase 0 never produced a result for this deployment (no tofu outputs) — the watcher has nothing to observe; use a Phase 0 retry instead"
	case !haveKubeconfig:
		refuse = "no kubeconfig was ever posted for this deployment (Result.kubeconfigPath empty and no <kubeconfigsDir>/<id>.yaml on disk) — the watcher needs it; verify cloud-init completed the kubeconfig PUT"
	}
	if refuse != "" {
		dep.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{
			"id":    dep.ID,
			"phase": phase,
			"error": refuse,
		})
		return
	}

	dep.Result.KubeconfigPath = kubeconfigPath
	dep.Result.Phase1Outcome = ""
	dep.Result.Phase1FinishedAt = nil
	dep.Result.ComponentStates = nil
	dep.Status = "phase1-watching"
	dep.Error = ""
	dep.FinishedAt = time.Time{}
	dep.phase1Started = false
	dep.eventsCh = make(chan provisioner.Event, 256)
	dep.done = make(chan struct{})
	dep.mu.Unlock()

	// Record the banner into the durable buffer (this also persists the
	// phase1-watching flip, so a Pod restart mid-watch rehydrates as
	// resumable rather than as the stale "failed") and offer it to the
	// live SSE channel without blocking.
	recorded := h.recordEventAndPersist(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   phase,
		Level:   "info",
		Message: "Phase-1 re-watch initiated — no tofu, no cloud writes. Re-attaching the HelmRelease watcher through the existing kubeconfig; status flips to ready (and the handover fires) only when every component reports installed.",
	})
	select {
	case dep.eventsCh <- recorded:
	default:
	}

	h.log.Info("phase 1 re-watch initiated (no tofu, no cloud writes)",
		"id", dep.ID,
		"kubeconfigPath", kubeconfigPath,
	)

	// phase1WatchWG is the test-only WaitGroup resumePhase1Watch honours
	// so a test can await the goroutine before its TempDir is removed.
	if h.phase1WatchWG != nil {
		h.phase1WatchWG.Add(1)
	}
	go func() {
		if h.phase1WatchWG != nil {
			defer h.phase1WatchWG.Done()
		}
		h.runPhase1Watch(dep)
		// markPhase1Done flips Status terminal but does not close the
		// channels — runProvisioning owns that on the first-launch path.
		// Here we allocated them, so we close them. Same nil-check +
		// recover defence as resumePhase1Watch against a concurrent wipe
		// that nils eventsCh after closing it.
		defer func() { _ = recover() }()
		dep.mu.Lock()
		evCh := dep.eventsCh
		doneCh := dep.done
		dep.mu.Unlock()
		select {
		case <-doneCh:
		default:
			if evCh != nil {
				close(evCh)
			}
			if doneCh != nil {
				close(doneCh)
			}
		}
	}()

	writeJSON(w, http.StatusOK, map[string]string{
		"id":        dep.ID,
		"status":    "phase1-watching",
		"phase":     phase,
		"streamURL": fmt.Sprintf("/api/v1/deployments/%s/logs", dep.ID),
		"message":   "Phase-1 re-watch accepted — no tofu, no cloud writes. Reopen the SSE stream to follow convergence; the record flips to ready and the handover fires when every component is installed.",
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
			// Same persistence story as runProvisioning — every retry
			// event lands on disk so a Pod restart mid-retry still has
			// the full history (original attempt + retry banner +
			// whatever the retry produced before the kill).
			recorded := h.recordEventAndPersist(dep, ev)
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

	if err != nil {
		dep.mu.Lock()
		dep.FinishedAt = time.Now()
		dep.Status = "failed"
		dep.Error = err.Error()
		dep.mu.Unlock()
		h.log.Error("retry provision failed", "id", dep.ID, "phase", retriedPhase, "err", err)
		close(dep.done)
		h.persistDeployment(dep)
		return
	}

	// #5381 — a retry MUST run the SAME post-apply chain as
	// runProvisioning. Before this, retryPhase0 set Status="ready"
	// straight off a successful Provision() and never called
	// commitPDMWithRetry / upsertSovereignParentZoneRecordsFromResult /
	// runPhase1Watch. Those live only in runProvisioning, so the
	// operator's "Retry phase" button could stamp a deployment READY
	// with **no DNS records committed and no Phase-1 watch** — a green
	// wizard pill over a Sovereign whose console FQDN does not resolve
	// (observed on hw289: console./api./hw289.omani.works all NXDOMAIN
	// while the record claimed a terminal status). Marking ready without
	// the DNS commit is the same fabricated-verdict class as the #5381
	// region-truncation bug this ticket fixed; both make the control
	// plane lie about a Sovereign's real state.
	//
	// Order mirrors runProvisioning exactly: PDM commit (pool-allocated
	// FQDNs) → parent-zone A records (BYO parent shapes, best-effort) →
	// Phase-1 HelmRelease watch, which is what actually flips Status to
	// ready once components converge. We deliberately do NOT set
	// Status="ready" here: runPhase1Watch owns that transition, so a
	// retry can no longer out-run its own convergence proof.
	dep.mu.Lock()
	dep.Result = result
	dep.mu.Unlock()

	h.commitPDMWithRetry(dep, result)
	h.upsertSovereignParentZoneRecordsFromResult(context.Background(), dep, result)

	h.log.Info("retry provision complete — running post-apply chain (PDM commit + phase-1 watch)",
		"id", dep.ID, "phase", retriedPhase)

	h.runPhase1Watch(dep)

	dep.mu.Lock()
	dep.FinishedAt = time.Now()
	dep.mu.Unlock()
	close(dep.done)
	// Terminal persist for the retry — same reason as runProvisioning.
	h.persistDeployment(dep)
}

// validatePhaseID — exported helper for tests.
func validatePhaseID(phase string) error {
	if strings.TrimSpace(phase) == "" {
		return errors.New("phase id required")
	}
	if !phase0Phases[phase] && !phase1Phases[phase] && !rewatchPhases[phase] {
		return fmt.Errorf("unknown phase %q", phase)
	}
	return nil
}
