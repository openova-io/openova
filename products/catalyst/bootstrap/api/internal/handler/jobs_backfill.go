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
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/dynamic"

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
// In addition to the one-shot seed hook, the bridge is ALSO
// subscribed to the Watcher's runtime event stream via Subscribe so
// every per-component HelmRelease transition observed AFTER the
// initial-list sync drives bridge.OnProvisionerEvent. This is the
// load-bearing path for issue #695: without it the per-Job state map
// stayed pinned to whatever the initial-list snapshot saw, so the
// wizard's /jobs page rendered Install rows as "running"/"pending"
// even after kubectl showed every bp-* HelmRelease at Ready=True
// (verified on otech48/49/50/52, 2026-05-03).
//
// A nil jobs store (CI runner, in-memory test handler) makes this a
// no-op — every test that doesn't wire a jobs store needs the path
// to silently skip the seeding.
func (h *Handler) attachBridgeSeederHook(dep *Deployment, watcher *helmwatch.Watcher) {
	if h.jobs == nil || watcher == nil {
		return
	}
	depID := dep.ID

	// Materialise the bridge eagerly so the runtime Subscribe path can
	// reference the same bridge instance as the seeder hook. The
	// bridge is goroutine-safe (Store.mu + Bridge.mu) so concurrent
	// dispatch of seed-hook + runtime-event paths is safe.
	dep.mu.Lock()
	bridge := dep.jobsBridge
	if bridge == nil {
		bridge = jobs.NewBridge(h.jobs, depID)
		dep.jobsBridge = bridge
	}
	dep.mu.Unlock()

	watcher.OnInitialListSynced(func(snap []helmwatch.ComponentSnapshot) {
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

	// Subscribe the bridge to the Watcher's runtime event stream so
	// every per-component HelmRelease transition (state changes
	// observed AFTER the initial-list snapshot) drives the per-Job
	// state map. The bridge dedups duplicate transitions internally
	// (Bridge.lastState), so receiving the same event via both the
	// emitWatchEvent path and this Subscribe path is a no-op for the
	// second arrival — issue #695. Errors are logged at warn so an
	// operator can spot persistent drift; they never abort the
	// Watcher loop.
	watcher.Subscribe(func(ev provisioner.Event) {
		if err := bridge.OnProvisionerEvent(ev); err != nil {
			h.log.Warn("jobs bridge: runtime event forward failed",
				"id", depID,
				"phase", ev.Phase,
				"component", ev.Component,
				"err", err,
			)
		}
	})
}

// snapshotsToSeeds converts the helmwatch.ComponentSnapshot wire
// shape into the jobs.InformerSeed shape the bridge consumes. Pulled
// out so the runPhase1Watch attach path and the /refresh-watch path
// both produce identical seeds for the same cache contents.
func snapshotsToSeeds(snap []helmwatch.ComponentSnapshot) []jobs.InformerSeed {
	return snapshotsToSeedsForRegion(snap, "")
}

// snapshotsToSeedsForRegion is the region-aware variant. When region is
// non-empty, each emitted seed's Component and DependsOn entries are
// prefixed with `<region>/` so the resulting Job rows materialise as
// `install-<region>/<chart>` with intra-region DependsOn links. The
// secondary helmwatch.Watchers spawned by spawnSecondaryRegionWatchers
// use this variant so their seed call doesn't collide with the
// primary's bare-named install-* Job rows and so the canvas snapshot's
// dep-edge derivation in flow_snapshot_local.go finds the right
// sibling-in-region entries to wire finish-to-start arrows between.
// Caught on prov #73: secondary regions' install-* Jobs were created
// via the per-event OnHelmReleaseEvent path (with DependsOn=[]) only,
// never seeded, so the canvas rendered all 45 secondary leaves
// disconnected from each other.
func snapshotsToSeedsForRegion(snap []helmwatch.ComponentSnapshot, region string) []jobs.InformerSeed {
	out := make([]jobs.InformerSeed, 0, len(snap))
	prefix := ""
	if region != "" {
		// ":" separator (NOT "/") — see phase1_watch.go for the URL-
		// routing rationale. JobName ends up as
		// "install-<region>:<chart>" which renders as
		// /jobs/install-<region>:<chart> in the SPA without TanStack
		// Router splitting the path.
		prefix = region + ":"
	}
	for _, s := range snap {
		regionDeps := s.DependsOn
		if prefix != "" && len(regionDeps) > 0 {
			rescoped := make([]string, 0, len(regionDeps))
			for _, d := range regionDeps {
				rescoped = append(rescoped, prefix+d)
			}
			regionDeps = rescoped
		}
		// Anti-flap (issue #3916): a HelmRelease whose Ready condition is
		// transiently *Failed but whose Flux remediation.retries are NOT yet
		// exhausted (Stalled==false) is STILL RECONCILING — Flux will flip it
		// back installing→installed. The chroot /jobs page re-reads live
		// state on every poll, so projecting that transient `failed` as a
		// terminal Failed leaf made the row flap Failed↔Succeeded while the
		// prov converged green. Downgrade a not-yet-stalled `failed` to
		// `installing` (in-progress) here on the READ-side seed only; the
		// live Watcher's own DeriveState still reports StateFailed so its
		// late-poll/recovery machinery (issue #910) is untouched. A STABLY
		// stalled HR (Stalled==true) keeps its terminal Failed leaf.
		state := s.Status
		if state == helmwatch.StateFailed && !s.Stalled {
			state = helmwatch.StateInstalling
		}
		out = append(out, jobs.InformerSeed{
			Component:  prefix + s.AppID,
			State:      state,
			Message:    s.Message,
			ObservedAt: s.LastTransitionAt,
			DependsOn:  regionDeps,
		})
	}
	return out
}

// attachSecondaryBridgeSeederHook is the region-aware variant of
// attachBridgeSeederHook. Wires the secondary helmwatch.Watcher's
// OnInitialListSynced callback to feed region-prefixed seeds into the
// SAME Bridge instance used by the primary watcher — so all 135
// install-* Jobs (45 primary + 2×45 secondaries) live in one store
// and share idempotency cursors. Without this, secondary install Jobs
// are auto-materialised only by the per-event OnHelmReleaseEvent path
// (which writes DependsOn=[]) so the canvas dep graph is permanently
// flat under secondary region groups.
func (h *Handler) attachSecondaryBridgeSeederHook(dep *Deployment, watcher *helmwatch.Watcher, region string) {
	if h.jobs == nil || watcher == nil || region == "" {
		return
	}
	depID := dep.ID
	dep.mu.Lock()
	bridge := dep.jobsBridge
	if bridge == nil {
		bridge = jobs.NewBridge(h.jobs, depID)
		dep.jobsBridge = bridge
	}
	dep.mu.Unlock()

	watcher.OnInitialListSynced(func(snap []helmwatch.ComponentSnapshot) {
		seeds := snapshotsToSeedsForRegion(snap, region)
		jobsCount, execsSeeded, err := bridge.SeedJobsFromInformerList(seeds)
		if err != nil {
			h.log.Warn("jobs bridge: secondary informer initial-list seed failed",
				"id", depID,
				"region", region,
				"snapshotCount", len(snap),
				"err", err,
			)
			return
		}
		h.log.Info("jobs bridge: seeded secondary region from informer initial-list",
			"id", depID,
			"region", region,
			"snapshotCount", len(snap),
			"jobsWritten", jobsCount,
			"executionsSeeded", execsSeeded,
		)
	})
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

	// Disk-fallback — when the Pod restarted between PutKubeconfig
	// writing the file AND the next Result.Save() persisting the path
	// field, dep.Result.KubeconfigPath comes back empty even though
	// the file exists at the canonical convention <kubeconfigsDir>/
	// <deploymentID>.yaml. RefreshWatch is supposed to RESUME a watch
	// after a Pod restart, so failing here would leave the canvas
	// frozen until the next cloud-init PUT (which only fires once per
	// deployment). Fall back to the canonical path and patch the
	// record so subsequent endpoints see a populated KubeconfigPath.
	if kubeconfigPath == "" && h.kubeconfigsDir != "" {
		candidate := filepath.Join(h.kubeconfigsDir, depID+".yaml")
		if _, err := os.Stat(candidate); err == nil {
			kubeconfigPath = candidate
			dep.mu.Lock()
			if dep.Result == nil {
				dep.Result = &provisioner.Result{}
			}
			if dep.Result.KubeconfigPath == "" {
				dep.Result.KubeconfigPath = candidate
			}
			dep.mu.Unlock()
		}
	}

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

	// Materialise the bridge before registering the seeder + runtime
	// hooks so both reference the same instance.
	dep.mu.Lock()
	bridge := dep.jobsBridge
	if bridge == nil {
		bridge = jobs.NewBridge(h.jobs, depID2)
		dep.jobsBridge = bridge
	}
	dep.mu.Unlock()

	watcher.OnInitialListSynced(func(snap []helmwatch.ComponentSnapshot) {
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

	// Subscribe the bridge to the runtime event stream — issue #695.
	// Same reasoning as attachBridgeSeederHook: every per-component
	// transition observed AFTER the seed must drive the per-Job state
	// map so the wizard's /jobs page advances past the initial-list
	// snapshot.
	watcher.Subscribe(func(ev provisioner.Event) {
		if err := bridge.OnProvisionerEvent(ev); err != nil {
			h.log.Warn("jobs bridge: refresh-watch runtime event forward failed",
				"id", depID2,
				"phase", ev.Phase,
				"component", ev.Component,
				"err", err,
			)
		}
	})

	// Stash the live watcher BEFORE launching the goroutine so a
	// concurrent /refresh-watch sees alreadyActive=true.
	dep.mu.Lock()
	dep.liveWatcher = watcher
	dep.mu.Unlock()

	// Also respawn secondary-region watchers (multi-region) — without
	// this, /refresh-watch only restores PRIMARY's HR.spec.dependsOn
	// into Job.DependsOn, leaving the secondaries' 90 install Jobs
	// permanently flat. spawnSecondaryRegionWatchers reads kubeconfig
	// files from <kubeconfigsDir>/<id>-<region>.yaml and wires the
	// region-aware seeder hook (attachSecondaryBridgeSeederHook). It
	// is idempotent — re-spawning watchers for regions that are
	// already up is a no-op because the spawn() inner func short-
	// circuits when stopWatchers[region] is already set. Caught on
	// prov #75 (2026-05-14): /refresh-watch fixed fsn1's 71 edges but
	// hel1-2 + nbg1-1 stayed at 0 edges until this fan-out was added.
	h.spawnSecondaryRegionWatchers(dep)

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

	// No live watcher — the finite Phase-1 bootstrap watch has ended.
	// FALSE-FAILED RECOVERY (issue #3687 / #910 tail): the persisted
	// dep.Result.ComponentStates is a FROZEN snapshot from the moment
	// the watch terminated. A bp-* HelmRelease that hit a transient
	// InstallFailed during the bootstrap window and then recovered to
	// Ready=True AFTER the watch returned still shows its stale "failed"
	// chip in that frozen map — nothing re-read the live HR, so a UAT
	// walk done post-bootstrap counts a false ❌. The live HR Ready
	// condition is the source of truth, so attempt a stateless ONE-SHOT
	// live re-read of the cluster's bp-* HelmReleases (same projection
	// SnapshotComponents uses, via DeriveState) and serve THAT. Only
	// fall back to the frozen persisted map when the kubeconfig is
	// unresolvable or the live list errors (Sovereign post-handover /
	// wiped / unreachable) — never fabricate a green, just re-derive
	// from whatever the cluster actually reports right now.
	if live, ok := h.liveComponentsSnapshot(r.Context(), dep); ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"watching":   false,
			"live":       true,
			"components": live,
		})
		return
	}

	// Live re-read unavailable — synthesise rows from the persisted final
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

// liveComponentsSnapshot performs a stateless ONE-SHOT live read of the
// deployment's bp-* HelmReleases and projects them through the same
// DeriveState path SnapshotComponents uses. Returns (snapshot, true) on
// success; (nil, false) when no live read is possible (no resolvable
// kubeconfig on the PVC, dynamic-client build failure, or the list call
// errors — e.g. the Sovereign is post-handover, wiped, or unreachable).
//
// This is the live-HR-truth re-read that heals a stale FALSE-FAILED chip
// after the finite Phase-1 watch has ended (issue #3687 / #910 tail): a
// HelmRelease that recovered to Ready=True post-bootstrap re-derives to
// `installed` here instead of echoing the frozen persisted snapshot.
//
// READ-ONLY: it resolves the kubeconfig via resolvePrimaryKubeconfigPath
// (which never mutates dep.Result, avoiding the -race against the
// Deployment.State() marshal) and never writes the deployment record.
func (h *Handler) liveComponentsSnapshot(ctx context.Context, dep *Deployment) ([]helmwatch.ComponentSnapshot, bool) {
	kubeconfigPath, ok := h.resolvePrimaryKubeconfigPath(dep)
	if !ok {
		return nil, false
	}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, false
	}
	// Honour the test dynamicFactory injection (same pattern as
	// tryDynamicClientLocked / sovereignDynamicClient) so handler tests
	// can drive a fake.NewSimpleDynamicClient; production builds the
	// real client from the posted-back kubeconfig.
	var dyn dynamic.Interface
	if h.dynamicFactory != nil {
		dyn, err = h.dynamicFactory(string(raw))
	} else {
		dyn, err = helmwatch.NewDynamicClientFromKubeconfig(string(raw))
	}
	if err != nil {
		return nil, false
	}
	// Bound the live list so a slow/unreachable apiserver cannot hang the
	// stateless read — fall back to the persisted map instead.
	listCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	snap, err := helmwatch.ListAndSnapshotHelmReleases(listCtx, dyn)
	if err != nil {
		return nil, false
	}
	return snap, true
}
