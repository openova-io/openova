// Phase-1 HelmRelease watch wiring.
//
// runPhase1Watch is the entry point runProvisioning calls after Phase 0
// ("flux-bootstrap") completes successfully. It builds an
// internal/helmwatch.Watcher against the deployment's persisted
// kubeconfig, runs the watch until termination, and writes the final
// per-component states + Phase1FinishedAt onto dep.Result.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the watch is read-only —
// internal/helmwatch never patches/applies/deletes any resource. Its
// only job is to read HelmRelease.status.conditions and turn each
// observed transition into a provisioner.Event the SSE buffer carries.
//
// Lifecycle:
//   - Skipped when dep.Result.KubeconfigPath is empty OR points at a
//     missing file. The Sovereign Admin surfaces the missing-
//     kubeconfig case via a single warn event so the operator can
//     fall back to docs/RUNBOOK-PROVISIONING.md §"Fetch kubeconfig
//     via SSH" and retry.
//   - Times out per CATALYST_PHASE1_WATCH_TIMEOUT (default 60m).
//   - On termination, dep.Status flips to "ready" if every observed
//     component reached "installed" OR there were no components and
//     the watch ran clean. If any component ended in "failed", Status
//     stays "phase1-watching" and Error captures the count — the
//     wizard's FailureCard renders the per-component breakdown.
//   - Result.ComponentStates + Result.Phase1FinishedAt get written
//     under dep.mu so a concurrent State() snapshot is consistent.
package handler

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// phase1WatchTimeoutEnv — env var override for the watch budget. The
// default DefaultWatchTimeout (60 minutes) is sized for bp-catalyst-
// platform's worst-observed install on the omantel.omani.works DoD run.
// Tests inject a much shorter value via Handler.phase1WatchTimeout.
const phase1WatchTimeoutEnv = "CATALYST_PHASE1_WATCH_TIMEOUT"

// phase1MinBootstrapKitHRsEnv — env var override for the lower bound
// on observed bp-* HelmReleases below which the terminate-on-all-done
// gate is suppressed. Default helmwatch.DefaultMinBootstrapKitHRs
// (11) tracks the canonical bootstrap-kit count. A future kit that
// ships more or fewer components only needs this env flipped on the
// catalyst-api Deployment — no code change required.
const phase1MinBootstrapKitHRsEnv = "CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS"

// phase1FirstSeenTimeoutEnv — env var override for the first-seen
// gate window. If zero bp-* HelmReleases appear within this window,
// the watcher emits a single warn event ("bootstrap-kit not
// reconciling") and CONTINUES the watch (HRs may still appear). The
// watch's overall budget is phase1WatchTimeoutEnv.
const phase1FirstSeenTimeoutEnv = "CATALYST_PHASE1_FIRST_SEEN_TIMEOUT"

// runPhase1Watch builds a helmwatch.Watcher and runs it to completion.
// All emit goes through h.emitWatchEvent so the durable buffer + SSE
// channel get every per-component event.
//
// The watch runs synchronously in the calling goroutine —
// runProvisioning waits here before closing dep.done. This keeps the
// "deployment finished" semantics consistent: a deployment is done
// only when both Phase 0 AND Phase 1 watch have terminated.
func (h *Handler) runPhase1Watch(dep *Deployment) {
	// At-most-once guard. Two callers can race to launch the watch:
	// runProvisioning (after `tofu apply`) and PutKubeconfig (after
	// the cloud-init postback). The first one through claims the
	// goroutine; the second is a no-op. Without this, a duplicate
	// run would spin up a second informer + emit a duplicate set of
	// per-component events into the SSE buffer.
	dep.mu.Lock()
	if dep.phase1Started {
		dep.mu.Unlock()
		h.log.Info("phase 1 watch already running for this deployment; skipping duplicate launch",
			"id", dep.ID,
		)
		return
	}
	dep.phase1Started = true
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()

	// Read the kubeconfig from disk. Plaintext lives only on the
	// PVC at /var/lib/catalyst/kubeconfigs/<id>.yaml (chmod 0600),
	// never in the on-disk JSON record. An empty path OR a missing
	// file are both short-circuit cases — the wizard's FailureCard
	// reads the emitted warn event so the operator can investigate
	// the cloud-init postback.
	kubeconfig := ""
	if kubeconfigPath != "" {
		raw, err := os.ReadFile(kubeconfigPath)
		if err == nil {
			kubeconfig = string(raw)
		} else {
			h.log.Warn("phase 1 watch: kubeconfig file not readable",
				"id", dep.ID,
				"path", kubeconfigPath,
				"err", err,
			)
		}
	}

	if kubeconfig == "" {
		h.emitWatchEvent(dep, provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   helmwatch.PhaseComponent,
			Level:   "warn",
			Message: "Phase-1 watch skipped: no kubeconfig is available on the catalyst-api side. The new Sovereign's cloud-init has not yet PUT its kubeconfig to /api/v1/deployments/{id}/kubeconfig — either Phase 0 is still in flight, or cloud-init failed to reach this endpoint. Operator can fetch the kubeconfig via SSH (see docs/RUNBOOK-PROVISIONING.md §Fetch kubeconfig via SSH) and re-run the deployment to observe per-component install state.",
		})
		// Short-circuit path — no watch ever ran, so outcome is empty.
		h.markPhase1Done(dep, nil, "")
		return
	}

	cfg := h.phase1WatchConfigForDeployment(dep, kubeconfig)
	watcher, err := helmwatch.NewWatcher(cfg, func(ev provisioner.Event) {
		h.emitWatchEvent(dep, ev)
	})
	if err != nil {
		h.emitWatchEvent(dep, provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   helmwatch.PhaseComponent,
			Level:   "error",
			Message: fmt.Sprintf("Phase-1 watch could not start: %v — Sovereign cluster is up (Phase 0 succeeded) but per-component state will not stream from this catalyst-api. Operator may run `kubectl get helmrelease -n flux-system` against the new Sovereign for ad-hoc diagnostics.", err),
		})
		// Short-circuit path — no watch ever ran, so outcome is empty.
		h.markPhase1Done(dep, nil, "")
		return
	}

	// Subscribe the bridge to the watcher's initial-list-synced hook
	// so a HelmRelease that has been Ready=True for an hour STILL
	// materialises a Job row + a synthetic-log-line Execution as
	// soon as the informer's first list completes. This is what
	// makes the table-view UX backfill correctly when the wizard
	// connects long after the watch's per-transition emits ran.
	//
	// Idempotency is guaranteed by SeedJobsFromInformerList itself
	// — calling it again on every helmwatch start is a no-op when
	// the Job already has a LatestExecutionID.
	h.attachBridgeSeederHook(dep, watcher)

	// Stash the live watcher on the deployment so the
	// /components/state and /refresh-watch endpoints can read its
	// in-memory informer cache without reaching into the cluster.
	dep.mu.Lock()
	dep.liveWatcher = watcher
	dep.mu.Unlock()
	defer func() {
		// Clear the live-watcher pointer so a subsequent
		// /refresh-watch invocation doesn't see a stale reference
		// to a Watcher whose Watch loop has already returned.
		dep.mu.Lock()
		if dep.liveWatcher == watcher {
			dep.liveWatcher = nil
		}
		dep.mu.Unlock()
	}()

	// Use the background context so a finished HTTP request from the
	// caller doesn't cancel a multi-minute Phase-1 watch. The watch
	// has its own configured timeout via cfg.WatchTimeout.
	finalStates, watchErr := watcher.Watch(context.Background())
	if watchErr != nil {
		h.log.Error("phase 1 watch returned error",
			"id", dep.ID,
			"err", watchErr,
		)
	}
	// Read the watch's terminal classification BEFORE markPhase1Done
	// — Outcome() must be called after Watch returns so the watcher
	// has set its final value. The Sovereign Admin's wizard banner
	// reads dep.Result.Phase1Outcome to render the right
	// operator-actionable diagnostic (e.g. "Flux on the new cluster
	// isn't reconciling the bootstrap-kit Kustomization").
	outcome := watcher.Outcome()
	h.markPhase1Done(dep, finalStates, outcome)
}

// phase1WatchConfigForDeployment — builds the helmwatch.Config the
// runPhase1Watch entry point uses. Pulled out so tests can call it
// to verify env-var parse + factory wiring without standing up a
// real cluster.
//
// h.phase1WatchTimeout / h.phase1MinBootstrapKitHRs /
// h.phase1FirstSeenTimeout are test-only overrides; production reads
// the env vars unmodified. Per docs/INVIOLABLE-PRINCIPLES.md #4 every
// knob is runtime-configurable — no constant is hardcoded into the
// build that an operator can't override at the catalyst-api
// Deployment level.
func (h *Handler) phase1WatchConfigForDeployment(dep *Deployment, kubeconfig string) helmwatch.Config {
	timeout := h.phase1WatchTimeout
	if timeout == 0 {
		timeout = helmwatch.CompileWatchTimeout(envOrEmpty(phase1WatchTimeoutEnv))
	}

	minHRs := h.phase1MinBootstrapKitHRs
	if minHRs == 0 {
		minHRs = helmwatch.CompileMinBootstrapKitHRs(envOrEmpty(phase1MinBootstrapKitHRsEnv))
	}

	firstSeen := h.phase1FirstSeenTimeout
	if firstSeen == 0 {
		firstSeen = helmwatch.CompileFirstSeenTimeout(envOrEmpty(phase1FirstSeenTimeoutEnv))
	}

	cfg := helmwatch.Config{
		KubeconfigYAML:     kubeconfig,
		WatchTimeout:       timeout,
		MinBootstrapKitHRs: minHRs,
		FirstSeenTimeout:   firstSeen,
	}
	if h.dynamicFactory != nil {
		cfg.DynamicFactory = h.dynamicFactory
	}
	if h.coreFactory != nil {
		cfg.CoreFactory = h.coreFactory
	}
	if h.phase1WatchResync > 0 {
		cfg.Resync = h.phase1WatchResync
	}
	return cfg
}

// markPhase1Done writes the watch outcome onto dep.Result and flips
// Status accordingly. Holds dep.mu for the whole transition so a
// State() snapshot from another goroutine can't observe Status=ready
// without ComponentStates yet being committed.
//
// The `outcome` argument is the watcher's terminal classification
// (helmwatch.OutcomeReady / OutcomeFailed / OutcomeTimeout /
// OutcomeFluxNotReconciling), or empty when no watch was ever run
// (kubeconfig short-circuit, NewWatcher failure). The Sovereign
// Admin's wizard banner reads dep.Result.Phase1Outcome to render the
// right operator-actionable diagnostic — in particular,
// "flux-not-reconciling" tells the operator to inspect the
// bootstrap-kit Kustomization on the new cluster instead of
// retrying provisioning.
func (h *Handler) markPhase1Done(dep *Deployment, finalStates map[string]string, outcome string) {
	now := time.Now().UTC()

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Result == nil {
		// Phase 0 already failed and runProvisioning skipped the
		// watch — markPhase1Done shouldn't have been called, but
		// defend against a future caller anyway.
		return
	}
	dep.Result.ComponentStates = finalStates
	dep.Result.Phase1FinishedAt = &now
	dep.Result.Phase1Outcome = outcome

	failed := 0
	for _, s := range finalStates {
		if s == helmwatch.StateFailed {
			failed++
		}
	}

	dep.FinishedAt = time.Now()
	switch {
	case outcome == helmwatch.OutcomeFluxNotReconciling:
		// Watch terminated because zero HelmReleases were ever
		// observed on the new Sovereign — Flux on that cluster is
		// not reconciling the bootstrap-kit Kustomization. This is
		// a hard failure; the operator must investigate
		// flux-system before any retry.
		dep.Status = "failed"
		dep.Error = "Phase 1 watch saw zero HelmReleases — the bootstrap-kit Kustomization on the new Sovereign is not reconciling. Operator: inspect `flux get kustomization -n flux-system` and `kubectl describe kustomization -n flux-system` on the new cluster (see docs/RUNBOOK-PROVISIONING.md §\"Phase 1 watch shows 0 HelmReleases\")."
	case failed > 0:
		dep.Status = "failed"
		dep.Error = fmt.Sprintf("Phase 1 finished with %d failed component(s); see ComponentStates for the per-component breakdown", failed)
	default:
		dep.Status = "ready"
	}

	h.log.Info("phase 1 watch terminated",
		"id", dep.ID,
		"componentCount", len(finalStates),
		"failedCount", failed,
		"finalStatus", dep.Status,
		"phase1Outcome", outcome,
	)
}

// envOrEmpty — small helper so the tests don't have to set every
// env var the package reads. Returns "" if unset.
func envOrEmpty(key string) string {
	return os.Getenv(key)
}
