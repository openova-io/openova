// jobs_backfill.go — bridge seeding from the helmwatch informer's
// initial-list + the two new endpoints the wizard's table-view UX
// relies on for backfill:
//
//   - POST /api/v1/deployments/{depId}/refresh-watch
//   - GET  /api/v1/deployments/{depId}/components/state
//
// The fix narrative:
//
//   - The pre-existing `internal/jobs.Bridge` only writes Jobs on
//     state transitions (OnHelmReleaseEvent's lastState dedup), so a
//     HelmRelease that has been Ready=True for an hour shows up as an
//     empty /jobs response. The Sovereign Admin's table-view UX
//     renders that as "no jobs yet" — the founder's symptom report.
//
//   - The fix has three halves:
//
//       1. Bridge.SeedJobsFromInformerList — given a snapshot of the
//          informer's local cache (one entry per bp-* HelmRelease at
//          HasSynced time), the bridge writes a Job per HR plus a
//          synthetic-log-line Execution for every terminal HR. This
//          method is idempotent so it is safe to call on every
//          helmwatch start (resume-after-restart, on-demand
//          /refresh-watch, etc.).
//
//       2. helmwatch.Watcher.OnInitialListSynced — the canonical
//          subscription point the handler uses to wire (1) into
//          every Watcher it constructs. Combined with
//          SnapshotComponents(), this gives the /components/state
//          endpoint a stateless read against the in-memory cache.
//
//       3. POST /refresh-watch — explicit handshake the FE uses
//          after a Pod restart or after the wizard cleared a stale
//          "skipped" cache. 202 acks "watcher running, seed
//          fired"; 200 acks "already running, no new watcher
//          started"; 409 acks "kubeconfig missing, retry later".
//
// Backwards compat — the existing `/api/v1/deployments/{id}/events`
// SSE feed and the existing `/api/v1/deployments/{depId}/jobs` REST
// surface are NOT modified by this file; both keep their original
// contracts.
package handler

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jobs"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// defaultRefreshWatchSeedTimeout — bound on how long RefreshWatch
// blocks waiting for the bridge seeder hook to fire after a fresh
// Watcher.Watch begins. The FE wants a synchronous "seed durable"
// signal but must not be held forever if the apiserver is slow to
// list HelmReleases. 30s covers the slowest observed initial-list
// against a fully-loaded omantel-class cluster (median ≈ 200ms,
// 95p ≈ 4s); a timeout returns 504 with watching=true so the FE can
// fall back to polling /components/state.
const defaultRefreshWatchSeedTimeout = 30 * time.Second

// attachBridgeSeederHook wires the watcher's "initial-list synced"
// callback onto a per-deployment jobs.Bridge.SeedJobsFromInformerList
// invocation. Called from runPhase1Watch on every NewWatcher
// construction (initial Phase-1 watch, resume-after-restart, AND the
// on-demand /refresh-watch path).
//
// The hook fires exactly once per Watcher (helmwatch guarantees
// that). The bridge handles idempotency end-to-end so repeated
// wiring across resume / refresh paths is safe.
//
// A nil jobs store (CI runner, in-memory test handler) makes this a
// no-op — every test that doesn't wire a jobs store needs the path
// to silently skip the seeding.
func (h *Handler) attachBridgeSeederHook(dep *Deployment, watcher *helmwatch.Watcher) {
	if h.jobs == nil || watcher == nil {
		return
	}
	depID := dep.ID
	watcher.OnInitialListSynced(func(snap []helmwatch.ComponentSnapshot) {
		dep.mu.Lock()
		bridge := dep.jobsBridge
		if bridge == nil {
			bridge = jobs.NewBridge(h.jobs, depID)
			dep.jobsBridge = bridge
		}
		dep.mu.Unlock()

		seeds := snapshotsToSeeds(snap)
		jobsCount, execsSeeded, err := bridge.SeedJobsFromInformerList(seeds)
		if err != nil {
			h.log.Warn("jobs bridge: informer initial-list seed failed",
				"id", depID,
				"snapshotCount", len(snap),
				"err", err,
			)
			return
		}
		h.log.Info("jobs bridge: seeded from informer initial-list",
			"id", depID,
			"snapshotCount", len(snap),
			"jobsWritten", jobsCount,
			"executionsSeeded", execsSeeded,
		)
	})
}

// snapshotsToSeeds converts the helmwatch.ComponentSnapshot wire
// shape into the jobs.InformerSeed shape the bridge consumes. Pulled
// out so the runPhase1Watch attach path and the /refresh-watch path
// both produce identical seeds for the same cache contents.
func snapshotsToSeeds(snap []helmwatch.ComponentSnapshot) []jobs.InformerSeed {
	out := make([]jobs.InformerSeed, 0, len(snap))
	for _, s := range snap {
		out = append(out, jobs.InformerSeed{
			Component:  s.AppID,
			State:      s.Status,
			Message:    s.Message,
			ObservedAt: s.LastTransitionAt,
			DependsOn:  s.DependsOn,
		})
	}
	return out
}

// RefreshWatch handles POST /api/v1/deployments/{depId}/refresh-watch.
//
// Behaviour matrix (matches the wire contract in the issue):
//
//	┌──────────────────────────────────┬─────────────────────────────┐
//	│ deployment state                 │ response                    │
//	├──────────────────────────────────┼─────────────────────────────┤
//	│ no kubeconfigPath persisted      │ 409 watch-not-resumable     │
//	│ kubeconfig file missing on PVC   │ 409 watch-not-resumable     │
//	│ liveWatcher already running      │ 200 already-watching        │
//	│ otherwise (start fresh watcher)  │ 202 watching, seededAt set  │
//	└──────────────────────────────────┴─────────────────────────────┘
//
// The 202 path returns AFTER the bridge seeder hook has fired so the
// FE knows the seed completed before it polls /jobs. To deliver that
// guarantee without blocking on the entire (multi-minute) watch run,
// the handler kicks the watcher in a goroutine and waits on the
// OnInitialListSynced callback to complete with a short bounded
// timeout (default defaultRefreshWatchSeedTimeout). A timeout returns
// 504 — the watcher is still running, the FE can poll
// /components/state to confirm the seed completes later.
func (h *Handler) RefreshWatch(w http.ResponseWriter, r *http.Request) {
	if h.jobs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "jobs-store-unavailable",
			"detail": "catalyst-api is running with persistence disabled — see Pod logs",
		})
		return
	}
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	if depID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-depId",
		})
		return
	}
	val, ok := h.deployments.Load(depID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "deployment-not-found",
		})
		return
	}
	dep := val.(*Deployment)

	// Already-running short-circuit. We hold dep.mu for the read so a
	// concurrent /refresh-watch + the runPhase1Watch path can't both
	// observe nil and race two informers against the same cluster.
	dep.mu.Lock()
	if dep.liveWatcher != nil {
		watcher := dep.liveWatcher
		dep.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{
			"watching":      true,
			"alreadyActive": true,
			"components":    watcher.SnapshotComponents(),
		})
		return
	}
	kubeconfigPath := ""
	if dep.Result != nil {
		kubeconfigPath = dep.Result.KubeconfigPath
	}
	dep.mu.Unlock()

	if kubeconfigPath == "" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "watch-not-resumable",
			"detail": "deployment has no kubeconfigPath — Phase 0 may not have completed yet, or cloud-init never PUT the kubeconfig back",
		})
		return
	}
	if _, err := os.Stat(kubeconfigPath); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "watch-not-resumable",
			"detail": "kubeconfig file missing on PVC: " + kubeconfigPath,
		})
		return
	}
	raw, readErr := os.ReadFile(kubeconfigPath)
	if readErr != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "watch-not-resumable",
			"detail": "kubeconfig file unreadable: " + readErr.Error(),
		})
		return
	}
	kubeconfig := string(raw)

	cfg := h.phase1WatchConfigForDeployment(dep, kubeconfig)
	watcher, err := helmwatch.NewWatcher(cfg, func(ev provisioner.Event) {
		h.emitWatchEvent(dep, ev)
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "watcher-build-failed",
			"detail": err.Error(),
		})
		return
	}

	// Wire the bridge seed hook + a synchronisation channel so we
	// know when the seed finishes. The bridge's internal seed write
	// is tens of milliseconds; this gives the FE a single round-trip
	// "seed is durable, your /jobs poll will see it" signal.
	seeded := make(chan time.Time, 1)
	depID2 := dep.ID
	watcher.OnInitialListSynced(func(snap []helmwatch.ComponentSnapshot) {
		dep.mu.Lock()
		bridge := dep.jobsBridge
		if bridge == nil {
			bridge = jobs.NewBridge(h.jobs, depID2)
			dep.jobsBridge = bridge
		}
		dep.mu.Unlock()

		seeds := snapshotsToSeeds(snap)
		jobsCount, execsSeeded, seedErr := bridge.SeedJobsFromInformerList(seeds)
		if seedErr != nil {
			h.log.Warn("jobs bridge: refresh-watch seed failed",
				"id", depID2, "err", seedErr,
			)
		} else {
			h.log.Info("jobs bridge: refresh-watch seed complete",
				"id", depID2,
				"snapshotCount", len(snap),
				"jobsWritten", jobsCount,
				"executionsSeeded", execsSeeded,
			)
		}
		select {
		case seeded <- time.Now().UTC():
		default:
		}
	})

	// Stash the live watcher BEFORE launching the goroutine so a
	// concurrent /refresh-watch sees alreadyActive=true.
	dep.mu.Lock()
	dep.liveWatcher = watcher
	dep.mu.Unlock()

	go func() {
		// Background context so the HTTP request finishing does not
		// cancel the multi-minute watch. The watcher's own
		// WatchTimeout bounds the run.
		_, _ = watcher.Watch(context.Background())
		dep.mu.Lock()
		if dep.liveWatcher == watcher {
			dep.liveWatcher = nil
		}
		dep.mu.Unlock()
	}()

	// Wait for the seeder hook to complete or for the bounded
	// timeout to elapse. The FE's call is a single round-trip and
	// the bridge writes are local PVC IO, so the typical wait is
	// well under a second against the median 11-component
	// bootstrap-kit; a slow apiserver list against the new Sovereign
	// can stretch this to a few seconds.
	timeout := h.refreshWatchSeedTimeout
	if timeout <= 0 {
		timeout = defaultRefreshWatchSeedTimeout
	}
	select {
	case ts := <-seeded:
		writeJSON(w, http.StatusAccepted, map[string]any{
			"watching":   true,
			"seededAt":   ts.Format(time.RFC3339),
			"components": watcher.SnapshotComponents(),
		})
	case <-time.After(timeout):
		writeJSON(w, http.StatusGatewayTimeout, map[string]any{
			"error":    "seed-timeout",
			"detail":   "watcher started but informer initial-list did not sync within " + timeout.String(),
			"watching": true,
		})
	}
}

// GetComponentsState handles GET
// /api/v1/deployments/{depId}/components/state.
//
// Returns a snapshot of the live helmwatch informer's local cache as
// `{ "components": [...], "watching": bool }`. When no Watcher is
// running for this deployment (Phase 1 finished, no /refresh-watch
// issued) the response falls back to dep.Result.ComponentStates
// synthesised into the same shape so the FE renders consistent rows
// whether or not a live watcher is attached.
//
// This endpoint is a stateless read — no streaming, no auth beyond
// what the deployment-id path segment already provides. The wizard's
// JobsTable backfill polls this when the SSE event-log replay
// yielded stale rows; the response includes a watching:bool flag so
// the FE can decide whether to also POST /refresh-watch.
func (h *Handler) GetComponentsState(w http.ResponseWriter, r *http.Request) {
	depID := strings.TrimSpace(chi.URLParam(r, "depId"))
	if depID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-depId",
		})
		return
	}
	val, ok := h.deployments.Load(depID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "deployment-not-found",
		})
		return
	}
	dep := val.(*Deployment)

	dep.mu.Lock()
	watcher := dep.liveWatcher
	var fallbackStates map[string]string
	if dep.Result != nil {
		fallbackStates = dep.Result.ComponentStates
	}
	dep.mu.Unlock()

	if watcher != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"watching":   true,
			"components": watcher.SnapshotComponents(),
		})
		return
	}

	// No live watcher — synthesise rows from the persisted final
	// state map so the FE always gets a usable snapshot. Per-component
	// message + lastTransitionAt are unavailable on the persisted
	// side (only the state enum is captured), so they are emitted
	// as empty / zero — the FE renders those as "—".
	out := make([]helmwatch.ComponentSnapshot, 0, len(fallbackStates))
	for appID, state := range fallbackStates {
		out = append(out, helmwatch.ComponentSnapshot{
			AppID:           appID,
			Status:          state,
			HelmReleaseName: "bp-" + appID,
			Namespace:       helmwatch.FluxNamespace,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"watching":   false,
		"components": out,
	})
}
