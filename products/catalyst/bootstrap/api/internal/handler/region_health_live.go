// region_health_live.go — #5600: the per-region HelmRelease census must
// reflect the cluster NOW, not the cluster as it was when Phase 1 ended.
//
// ROOT CAUSE (one-line grep). `Result.ComponentStates` is written in exactly
// ONE place in the whole API — phase1_watch.go markPhase1Done — and the
// per-region roll-up `Result.Regions` / `Result.SecondaryDegraded` is stamped
// in the same statement block. Nothing ever refreshes either. Since
// provisioner.ComputeRegionHealth is a PURE function over those maps, the
// console's region census froze at Phase-1-terminal time and stayed frozen for
// the entire post-handover lifetime of the Sovereign.
//
// Both symptoms filed on #5600 are that one defect:
//
//   - "suspended counted as not-ready" — at snapshot time the by-design-
//     suspended secondary HRs (bp-catalyst-platform, bp-continuum,
//     bp-openova-mcp, bp-self-sovereign-cutover, bp-velero) were not yet
//     suspended and not yet Ready, so they froze as non-installed. They are
//     NOT being mis-classified today: helmwatch already coerces
//     `spec.suspend: true` → StateInstalled on live observation (Wave 5.103 /
//     #2447, and again in ListAndSnapshotHelmReleases). The stored map simply
//     predates the suspends.
//   - "stale denominator 65 vs live 67" — same cause, directly: the 2 HRs the
//     cutover installed arrived after the snapshot.
//
// WHY A CENSUS FILTER CANNOT FIX IT. `Result.ComponentStates` is
// map[componentID]stateString. It carries no suspend information and never
// will. Adding a `spec.suspend` exclusion to ComputeRegionHealth would pass a
// synthetic unit test and leave the false Degraded exactly where it is.
// ComputeRegionHealth needs NO change — it is pure and its ratio math is sound
// (regionDegraded requires BOTH an absolute shortfall floor AND the 9/10 ratio
// gate). The defect is entirely in WHAT IT IS FED.
//
// THE FIX. Re-derive the census from live cluster state and prefer it over the
// Phase-1 snapshot. Source preference, best first:
//
//	(1) live-watchers   — an attached helmwatch informer cache (the pre-handover
//	                     window). Free, no I/O; already implemented.
//	(2) live-list       — a stateless ONE-SHOT list of every region's bp-* HRs
//	                     via helmwatch.ListAndSnapshotHelmReleases, using the
//	                     kubeconfigs already on the PVC. THIS FILE.
//	(3) phase1-snapshot — the frozen Result.Regions. Last resort ONLY, and the
//	                     payload says so (regionCensusSource + regionCensusStale)
//	                     instead of presenting stale data as current.
//
// This is a SOVEREIGN-SIDE recompute by construction: post-handover the chroot
// catalyst-api is the process that serves GET /deployments/{id} to the console,
// and it reads its own cluster (plus each secondary via the kubeconfigs the
// secondary control-planes deposited). No mothership re-poll is introduced, so
// ADR-0002 / Principle #11 are untouched — on the mothership the live read
// simply fails after cutover and the payload falls back, honestly labelled.
//
// The real-fault guarantee (region_health.go:69) is PRESERVED, and in fact
// strengthened: a genuinely non-Ready, un-suspended HelmRelease is read LIVE
// and still degrades the region. Nothing is added to
// providerInapplicableComponents.
package handler

import (
	"context"
	"os"
	"time"

	"k8s.io/client-go/dynamic"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// regionCensusSource values surfaced on the deployment payload so an operator
// (and a UAT walker) can tell a live number from a Phase-1 relic.
const (
	// regionCensusSourceWatchers — recomputed from the in-memory helmwatch
	// informer caches of the still-attached Phase-1 watchers.
	regionCensusSourceWatchers = "live-watchers"
	// regionCensusSourceLiveList — re-derived by a one-shot live list of
	// every region's bp-* HelmReleases (post-handover path, #5600).
	regionCensusSourceLiveList = "live-list"
	// regionCensusSourcePhase1Snapshot — the FROZEN Phase-1-terminal census.
	// Always reported as stale: by construction it describes the cluster as
	// it was when Phase 1 ended, which post-cutover is provably not now.
	regionCensusSourcePhase1Snapshot = "phase1-snapshot"
)

// liveRegionCensusTTL throttles the live re-derivation to at most one ATTEMPT
// per deployment per window. The console polls GET /deployments/{id} every few
// seconds; without this every poll would fan out one HelmRelease List per
// region. 30s is well inside "operator perceives it as current" while keeping
// the apiserver load to ~2 list calls/minute on a 2-region Sovereign.
const liveRegionCensusTTL = 30 * time.Second

// liveRegionCensusStaleAfter is how old a successfully-derived live census may
// get before the payload starts flagging it stale. It is deliberately several
// TTLs so a single failed refresh does not flap the flag — but a cluster that
// has been unreadable for minutes stops claiming its numbers are current.
const liveRegionCensusStaleAfter = 5 * time.Minute

// liveRegionCensusListTimeout bounds ONE region's HelmRelease list. A slow or
// unreachable apiserver must degrade to "could not refresh", never hang the
// synchronous GET path.
const liveRegionCensusListTimeout = 3 * time.Second

// liveRegionCensusBudget bounds the WHOLE refresh (primary + every secondary)
// on the synchronous GET path. Worst case a GET /deployments/{id} pays this
// once per liveRegionCensusTTL; every other poll in the window is free.
const liveRegionCensusBudget = 8 * time.Second

// regionCensusReadable reports whether a live re-derivation is even meaningful
// for dep: it is, exactly when Phase 1 has TERMINATED (so the watchers are
// gone and Result.Regions is frozen) and a Result exists to fall back to.
// While Phase 1 is still running the watcher path already serves live numbers.
// The CALLER MUST NOT hold dep.mu.
func regionCensusRefreshApplicable(dep *Deployment) bool {
	if dep == nil {
		return false
	}
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Result == nil || dep.Result.Phase1FinishedAt == nil {
		return false
	}
	// A still-attached watcher is a strictly better source — regionHealth-
	// ForStateLocked prefers it and no list is needed.
	if dep.liveWatcher != nil || len(dep.secondaryWatchers) > 0 {
		return false
	}
	return true
}

// claimLiveRegionCensusRefresh is the TTL + single-flight gate. It returns true
// exactly once per liveRegionCensusTTL per deployment, and never concurrently;
// the winner MUST call releaseLiveRegionCensusRefresh when done.
//
// The stamp is taken on the ATTEMPT, not on success, so a Sovereign whose
// apiserver is unreachable costs at most one bounded round trip per window
// rather than one per poll.
func claimLiveRegionCensusRefresh(dep *Deployment, force bool) bool {
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.liveRegionCensusRefreshing {
		return false
	}
	if !force && !dep.liveRegionCensusAttemptAt.IsZero() &&
		time.Since(dep.liveRegionCensusAttemptAt) < liveRegionCensusTTL {
		return false
	}
	dep.liveRegionCensusRefreshing = true
	dep.liveRegionCensusAttemptAt = time.Now()
	return true
}

func releaseLiveRegionCensusRefresh(dep *Deployment) {
	dep.mu.Lock()
	dep.liveRegionCensusRefreshing = false
	dep.mu.Unlock()
}

// invalidateLiveRegionCensusLocked drops any cached live census. Called on wipe
// so a re-provisioned deployment id can never serve the previous cluster's
// numbers. The CALLER MUST hold dep.mu.
func invalidateLiveRegionCensusLocked(dep *Deployment) {
	dep.liveRegionCensus = nil
	dep.liveRegionCensusDegraded = false
	dep.liveRegionCensusAt = time.Time{}
	dep.liveRegionCensusAttemptAt = time.Time{}
}

// refreshLiveRegionCensusIfStale is the SYNCHRONOUS entry point used by
// GET /deployments/{id} — the endpoint whose payload drives the console's
// readiness pill. TTL-gated, so at most one poll per window pays for the
// re-derivation and it is bounded by liveRegionCensusBudget.
//
// Returns true when a fresh live census was published.
func (h *Handler) refreshLiveRegionCensusIfStale(ctx context.Context, dep *Deployment) bool {
	if !regionCensusRefreshApplicable(dep) {
		return false
	}
	if !claimLiveRegionCensusRefresh(dep, false) {
		return false
	}
	defer releaseLiveRegionCensusRefresh(dep)
	budgetCtx, cancel := context.WithTimeout(ctx, liveRegionCensusBudget)
	defer cancel()
	return h.refreshLiveRegionCensus(budgetCtx, dep)
}

// kickLiveRegionCensusRefresh is the ASYNCHRONOUS entry point used by
// GET /deployments (the list). The list Ranges over every deployment, so a
// synchronous refresh there would multiply the budget by the fleet size. The
// kick returns immediately; the NEXT list poll serves the refreshed roll-up.
func (h *Handler) kickLiveRegionCensusRefresh(dep *Deployment) {
	if !regionCensusRefreshApplicable(dep) {
		return
	}
	if !claimLiveRegionCensusRefresh(dep, false) {
		return
	}
	go func() {
		defer releaseLiveRegionCensusRefresh(dep)
		ctx, cancel := context.WithTimeout(context.Background(), liveRegionCensusBudget)
		defer cancel()
		h.refreshLiveRegionCensus(ctx, dep)
	}()
}

// forceRefreshLiveRegionCensus bypasses the TTL. Fired when an operation is
// KNOWN to have churned HelmReleases in both regions — today that is cutover
// completion, which is precisely the event that suspends the by-design
// secondary HRs and installs the 2 extra HRs #5600 saw missing from the
// denominator. Without this hook the first post-cutover console load would
// still show the Phase-1 relic until the next poll's TTL expiry.
func (h *Handler) forceRefreshLiveRegionCensus(ctx context.Context, dep *Deployment) bool {
	if !regionCensusRefreshApplicable(dep) {
		return false
	}
	if !claimLiveRegionCensusRefresh(dep, true) {
		return false
	}
	defer releaseLiveRegionCensusRefresh(dep)
	return h.refreshLiveRegionCensus(ctx, dep)
}

// refreshLiveRegionCensus re-derives the per-region census from LIVE cluster
// state and publishes it onto dep. Returns true on publish.
//
// Completeness contract — the refresh publishes ONLY when every region the
// deployment declares was actually read:
//
//   - the primary MUST be readable (an empty primary map would give priReady=0,
//     which makes regionDegraded structurally incapable of flagging anything —
//     a fabricated all-green);
//   - the number of resolvable secondary kubeconfigs MUST cover the declared
//     secondary count, and every one of them MUST list successfully.
//
// A partial read is discarded, NOT published: half a census is a lie in both
// directions (it can hide a real fault and it can invent one). The caller then
// serves the previous live census (if any) or the Phase-1 snapshot, and the
// payload flags the source — the operator sees "this number is not current"
// rather than a confident wrong number.
//
// READ-ONLY with respect to the persisted record: it never writes
// dep.Result.Regions / dep.Result.ComponentStates. Deployment.State() hands the
// *Result pointer to writeJSON, which marshals it OUTSIDE dep.mu, so a write
// here would race that marshal (the same -race contract
// resolvePrimaryKubeconfigPath documents). The live census lives in its own
// in-memory fields instead.
func (h *Handler) refreshLiveRegionCensus(ctx context.Context, dep *Deployment) bool {
	dep.mu.Lock()
	provider := dep.Request.Provider
	primaryRegion := primaryRegionKey(&dep.Request)
	dep.mu.Unlock()

	primaryStates, ok := h.liveRegionStates(ctx, "", dep)
	if !ok {
		h.log.Debug("region census: live re-derivation skipped — primary HelmRelease list unavailable; serving the previous census (#5600)",
			"id", dep.ID)
		return false
	}

	// Secondary kubeconfig discovery reuses the SAME union the cutover's
	// region-B legs (#5359) and the ClusterMesh establish trust: the
	// process-local dep.secondaryKubeconfigPaths map ∪ the on-disk
	// `<depID>-<regionKey>.yaml` files. That union is immune to the
	// in-memory loss a catalyst-api restart causes (#4000/#5488), which
	// matters here because the post-handover chroot is exactly the process
	// that restarts most often.
	res := secondaryKubeconfigsForCutover(dep)
	if res.expected > len(res.paths) {
		h.log.Debug("region census: live re-derivation skipped — fewer secondary kubeconfigs resolvable than the spec declares (#5600)",
			"id", dep.ID, "expected", res.expected, "resolved", len(res.paths))
		return false
	}

	secondaryStates := make(map[string]map[string]string, len(res.paths))
	for region, path := range res.paths {
		states, sok := h.liveRegionStates(ctx, path, nil)
		if !sok {
			h.log.Debug("region census: live re-derivation skipped — a declared secondary region could not be listed; refusing to publish a partial census (#5600)",
				"id", dep.ID, "region", region)
			return false
		}
		secondaryStates[region] = states
	}

	regions, secondaryDegraded := provisioner.ComputeRegionHealth(
		provider, primaryRegion, primaryStates, secondaryStates)

	dep.mu.Lock()
	dep.liveRegionCensus = regions
	dep.liveRegionCensusDegraded = secondaryDegraded
	dep.liveRegionCensusAt = time.Now()
	dep.mu.Unlock()
	return true
}

// liveRegionStates lists one region's bp-* HelmReleases and projects them into
// the componentID → helmwatch-state map ComputeRegionHealth consumes.
//
// Exactly one of the two selectors is used: a non-empty kubeconfigPath reads
// that file (the secondary-region path); an empty one resolves dep's PRIMARY
// kubeconfig through resolvePrimaryKubeconfigPath (which carries the #3153
// conventional fallback and the #5131 chroot self-materialize, so a chroot
// whose primary kubeconfig was never posted back still resolves).
//
// The projection goes through helmwatch.ListAndSnapshotHelmReleases, which is
// where the `spec.suspend: true` → StateInstalled coercion lives (Wave 5.103 /
// #2447). That is what makes a by-design-suspended secondary read as converged
// instead of as a shortfall — the coercion was always correct, it just never
// reached the census because the census never re-read the cluster.
func (h *Handler) liveRegionStates(ctx context.Context, kubeconfigPath string, dep *Deployment) (map[string]string, bool) {
	if kubeconfigPath == "" {
		if dep == nil {
			return nil, false
		}
		resolved, ok := h.resolvePrimaryKubeconfigPath(dep)
		if !ok {
			return nil, false
		}
		kubeconfigPath = resolved
	}
	raw, err := os.ReadFile(kubeconfigPath)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	// Honour the test dynamicFactory injection (same pattern as
	// liveComponentsSnapshot / sovereignDynamicClient); production builds the
	// real client from the kubeconfig on the PVC.
	var dyn dynamic.Interface
	if h.dynamicFactory != nil {
		dyn, err = h.dynamicFactory(string(raw))
	} else {
		dyn, err = helmwatch.NewDynamicClientFromKubeconfig(string(raw))
	}
	if err != nil || dyn == nil {
		return nil, false
	}
	listCtx, cancel := context.WithTimeout(ctx, liveRegionCensusListTimeout)
	defer cancel()
	snap, err := helmwatch.ListAndSnapshotHelmReleases(listCtx, dyn)
	if err != nil {
		return nil, false
	}
	// An EMPTY list is not a successful read of a converged cluster — it is
	// what an apiserver that answers but has no Flux CRD content returns, and
	// feeding 0/0 into the census would silently zero the primary baseline.
	// Treat it as "no live truth available" and let the caller fall back.
	if len(snap) == 0 {
		return nil, false
	}
	states := make(map[string]string, len(snap))
	for _, cs := range snap {
		states[cs.AppID] = cs.Status
	}
	return states, true
}
