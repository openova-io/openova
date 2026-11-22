// Phase-retry endpoint for the wizard's failed-phase UX (issue #125).
//
// When a provisioning phase fails, the wizard renders a "Retry phase"
// button; this endpoint accepts that retry and re-drives the phase:
//
//  1. Phase 0 phases (tofu-init, tofu-plan, tofu-apply, tofu-output,
//     flux-bootstrap) — catalyst-api owns the OpenTofu workdir, so it
//     runs the first run's path again, runProvisioning: Phase 0 then the
//     Phase-1 watch (Phase 0 IS the catalyst-api's job, SOVEREIGN-PROVISIONING.md §3).
//  2. Phase 1 bootstrap-kit phases (cilium … bp-catalyst-platform) — Flux
//     HelmReleases on the NEW Sovereign. catalyst-api MUST NOT exec
//     kubectl/helm there (INVIOLABLE-PRINCIPLES.md #3, Lesson #24); Flux
//     retries on its own (install.remediation.retries: 3), and until the
//     Flux Receiver webhook is wired this endpoint emits an event pointing
//     the operator at the runbook's "Rollback procedures per phase".
//  3. phase1-watch — re-attaches only the Phase-1 watcher (#6795).
//
// Every accepted retry is one entry of the record's retry calls (gate
// check FND-G02, internal/retrycall). Events stream back through the
// deployment's SSE channel, re-opened after runProvisioning closed it.
package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/retrycall"
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

	dep.mu.Lock() // also in flight: a run or a watch of this process (done open, or a watch started)
	stillRunning := dep.Status == "provisioning" || dep.Status == "tofu-applying" || !dep.isDone() || dep.phase1Started && dep.Status == "phase1-watching"
	dep.mu.Unlock()
	if stillRunning {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "deployment is still in-flight — wait for the current phase to finish before retrying",
		})
		return
	}

	switch {
	case phase0Phases[phase]:
		RecordRetryCall(h, dep, "", retrycall.RoutePhase, "phase "+phase, r)
		h.retryPhase0(w, dep, phase)
	case phase1Phases[phase]:
		RecordRetryCall(h, dep, "", retrycall.RoutePhase, "phase "+phase, r)
		h.retryPhase1(w, dep, phase)
	case rewatchPhases[phase]:
		h.retryPhase1Rewatch(w, r, dep, phase)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unknown phase %q — see docs/RUNBOOK-PROVISIONING.md for the canonical phase list", phase),
		})
	}
}

// retryPhase0 runs the FULL phase 0 sequence (init → plan → apply → output →
// flux-bootstrap) again, because OpenTofu converges the whole stack to its
// declared state. It is runProvisioning, the code path of the first run: the
// same deployment id and working directory, the same 180-minute bound, the same
// post-apply chain and the same Phase-1 watch, which alone flips the record to
// ready. The previous run's Phase-1 verdict is cleared so this run's watch judges
// it, and phase1Started is cleared because no watch runs (RetryPhase refused one).
func (h *Handler) retryPhase0(w http.ResponseWriter, dep *Deployment, phase string) {
	// Re-open the events channel + done signal — the originals were closed
	// when the previous run returned. The wizard's SSE client reconnects to
	// /logs, which reads this fresh channel and replays the buffer.
	dep.mu.Lock()
	dep.eventsCh = make(chan provisioner.Event, 256)
	dep.done = make(chan struct{})
	dep.Status = "provisioning"
	dep.Error = ""
	dep.FinishedAt = time.Time{}
	dep.phase1Started = false
	dep.Result.ClearPhase1()
	dep.mu.Unlock()

	// The banner is recorded and persisted with the retry-init state, so a Pod
	// restart before the run's first event does not leave the previous "failed".
	recorded := h.recordEventAndPersist(dep, provisioner.Event{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Phase:   phase,
		Level:   "info",
		Message: fmt.Sprintf("Retry initiated for phase %q — Phase 0 runs again in the deployment's working directory (`tofu apply` converges the existing state), then the Phase-1 watch, as on the first run.", phase),
	})
	select {
	case dep.eventsCh <- recorded:
	default:
	}
	go h.runProvisioning(dep)

	writeJSON(w, http.StatusOK, map[string]string{
		"id":        dep.ID,
		"status":    "provisioning",
		"phase":     phase,
		"streamURL": fmt.Sprintf("/api/v1/deployments/%s/logs", dep.ID),
		"message":   "Phase 0 retry accepted — re-running Phase 0 and then the Phase-1 watch, as on the first run. Reopen the SSE stream to follow progress.",
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
func (h *Handler) retryPhase1Rewatch(w http.ResponseWriter, r *http.Request, dep *Deployment, phase string) {
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
	dep.Result.ClearPhase1()
	dep.Status = "phase1-watching"
	dep.Error = ""
	dep.FinishedAt = time.Time{}
	dep.phase1Started = false
	dep.eventsCh = make(chan provisioner.Event, 256)
	dep.done = make(chan struct{})
	dep.mu.Unlock()
	RecordRetryCall(h, dep, "", retrycall.RoutePhase, "phase "+phase, r)

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
