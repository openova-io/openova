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
//   - Skipped when dep.Result.Kubeconfig is empty. The Sovereign Admin
//     surfaces the missing-kubeconfig case via a single warn event so
//     the operator can fall back to docs/RUNBOOK-PROVISIONING.md
//     §"Fetch kubeconfig via SSH" and retry.
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

// runPhase1Watch builds a helmwatch.Watcher and runs it to completion.
// All emit goes through h.emitWatchEvent so the durable buffer + SSE
// channel get every per-component event.
//
// The watch runs synchronously in the calling goroutine —
// runProvisioning waits here before closing dep.done. This keeps the
// "deployment finished" semantics consistent: a deployment is done
// only when both Phase 0 AND Phase 1 watch have terminated.
func (h *Handler) runPhase1Watch(dep *Deployment) {
	dep.mu.Lock()
	kubeconfig := ""
	if dep.Result != nil {
		kubeconfig = dep.Result.Kubeconfig
	}
	dep.mu.Unlock()

	if kubeconfig == "" {
		h.emitWatchEvent(dep, provisioner.Event{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Phase:   helmwatch.PhaseComponent,
			Level:   "warn",
			Message: "Phase-1 watch skipped: no kubeconfig is available on the catalyst-api side. Operator must fetch the kubeconfig via SSH (see docs/RUNBOOK-PROVISIONING.md §Fetch kubeconfig via SSH) and re-run the deployment with the kubeconfig pre-populated to observe per-component install state.",
		})
		h.markPhase1Done(dep, nil)
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
		h.markPhase1Done(dep, nil)
		return
	}

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
	h.markPhase1Done(dep, finalStates)
}

// phase1WatchConfigForDeployment — builds the helmwatch.Config the
// runPhase1Watch entry point uses. Pulled out so tests can call it
// to verify env-var parse + factory wiring without standing up a
// real cluster.
//
// h.phase1WatchTimeout is a test-only override; production reads the
// env var unmodified.
func (h *Handler) phase1WatchConfigForDeployment(dep *Deployment, kubeconfig string) helmwatch.Config {
	timeout := h.phase1WatchTimeout
	if timeout == 0 {
		timeout = helmwatch.CompileWatchTimeout(envOrEmpty(phase1WatchTimeoutEnv))
	}

	cfg := helmwatch.Config{
		KubeconfigYAML: kubeconfig,
		WatchTimeout:   timeout,
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
func (h *Handler) markPhase1Done(dep *Deployment, finalStates map[string]string) {
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

	failed := 0
	for _, s := range finalStates {
		if s == helmwatch.StateFailed {
			failed++
		}
	}

	dep.FinishedAt = time.Now()
	switch {
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
	)
}

// envOrEmpty — small helper so the tests don't have to set every
// env var the package reads. Returns "" if unset.
func envOrEmpty(key string) string {
	return os.Getenv(key)
}
