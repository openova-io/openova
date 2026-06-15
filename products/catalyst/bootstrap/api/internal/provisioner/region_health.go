package provisioner

// Per-region HelmRelease health breakdown (#3611).
//
// A 2-region deployment flips to status="ready" the instant the PRIMARY
// region's Phase-1 watch reaches OutcomeReady and the secondary kubeconfig
// has landed — markPhase1Done never consults the secondary regions'
// per-component state. So a degraded secondary (hw145: region-a 60/63 HRs
// Ready, region-b 48/63 after an openbao cascade) hides behind a bare green
// "ready" pill and the operator never sees it.
//
// The fix is to SURFACE the truth, not hard-gate it. Hard-gating "ready" on
// secondary convergence risks hanging provs when a secondary is legitimately
// slow (Flux keeps reconciling long past the watch budget and the doctrine
// forbids wiping such envs). Instead we compute a per-region Ready/total HR
// breakdown + a `degraded` flag and expose it on the deployment status; the
// existing `ready` gate is untouched. The console/jobs view can then render
// "region-b 48/63, degraded" instead of a flat "ready".

// hrStateInstalled is the helmwatch terminal state string for a fully
// reconciled HelmRelease (helmwatch.StateInstalled == "installed").
//
// Duplicated as a local literal rather than imported because internal/
// helmwatch imports this package (for provisioner.Event) — taking the
// reverse dependency would form an import cycle. ComputeRegionHealth
// operates on the already-derived state-string maps, so it never needs the
// helmwatch package, only this one constant.
const hrStateInstalled = "installed"

// regionDegradedAbsoluteFloor — minimum Ready-HR shortfall (primary minus
// secondary) below which a secondary is NOT flagged degraded even if it is a
// few HRs behind. A secondary that is ~1-2 HRs behind the primary is almost
// always mid-reconcile, not stuck; flagging it would make the badge flap on
// every poll during normal convergence. hw145's region-b was 12 HRs behind
// (48 vs 60) — comfortably past this floor.
const regionDegradedAbsoluteFloor = 3

// regionDegradedRatioNumerator / regionDegradedRatioDenominator express the
// "significantly short of the primary" ratio gate: a secondary is degraded
// when its Ready count is below this fraction of the primary's Ready count.
// 9/10 (90%) mirrors the convergedLateMinReady ratio idiom already used in
// phase1_converged_late.go. hw145 region-b: 48 < (9/10)*60 == 54 → degraded.
const (
	regionDegradedRatioNumerator   = 9
	regionDegradedRatioDenominator = 10
)

// RegionHealth is the per-region HelmRelease census surfaced on the
// deployment status so "ready" stops hiding a degraded secondary (#3611).
type RegionHealth struct {
	// Region is the region key. For the primary it is the declared
	// Regions[0].CloudRegion (falling back to "primary" when unknown);
	// for secondaries it is the cloud-init ?region= key the secondary
	// control-plane PUT its kubeconfig under (e.g. "hel1-2",
	// "me-east-215-b-1").
	Region string `json:"region"`

	// Primary marks the region whose Phase-1 watch drives the
	// deployment-level "ready" classification. Exactly one entry is
	// Primary=true.
	Primary bool `json:"primary"`

	// HRReady is the count of bp-* HelmReleases observed in the
	// terminal "installed" state in this region.
	HRReady int `json:"hrReady"`

	// HRTotal is the count of bp-* HelmReleases observed in this region.
	HRTotal int `json:"hrTotal"`

	// Degraded is true when this region is a SECONDARY that falls
	// significantly short of the primary's Ready-HR count (see
	// regionDegraded). The primary is never Degraded — it IS the
	// convergence baseline, and its own failure is already surfaced via
	// dep.Status/Phase1Outcome.
	Degraded bool `json:"degraded"`
}

// countInstalled returns (ready, total) for a component-state map keyed by
// component id → helmwatch state string. Ready == state "installed".
func countInstalled(states map[string]string) (ready, total int) {
	for _, s := range states {
		total++
		if s == hrStateInstalled {
			ready++
		}
	}
	return ready, total
}

// regionDegraded decides whether a SECONDARY region with secReady/secTotal
// installed HRs is degraded relative to a primary with priReady installed
// HRs. A secondary is degraded when it is not fully installed AND it is
// significantly behind the primary — significant meaning BOTH past the
// absolute shortfall floor AND below the ratio gate. Requiring both keeps a
// nearly-caught-up secondary (1-2 HRs behind, transient) from flapping the
// badge while still catching a real cascade like hw145's 48/63-vs-60/63.
//
// A secondary that has observed ZERO HRs (secTotal == 0) is treated as
// degraded only when the primary has a meaningful population — a 0/0
// secondary whose watcher just attached (no list yet) must not register as
// degraded against a 0/0 primary during early convergence.
func regionDegraded(priReady, secReady, secTotal int) bool {
	if secTotal > 0 && secReady == secTotal {
		// Secondary fully installed — never degraded regardless of how
		// the primary's count compares (a secondary can legitimately run
		// fewer HRs than the primary, e.g. a region that does not host
		// the marketplace).
		return false
	}
	shortfall := priReady - secReady
	if shortfall < regionDegradedAbsoluteFloor {
		return false
	}
	// secReady < (num/den) * priReady, written without floats.
	belowRatio := secReady*regionDegradedRatioDenominator < priReady*regionDegradedRatioNumerator
	return belowRatio
}

// ComputeRegionHealth builds the per-region HelmRelease census + the
// secondaryDegraded roll-up (#3611). It is a pure function over the
// already-derived per-region state maps so it is fully table-testable
// without a live cluster.
//
//   - primaryRegion: the declared primary region key (Regions[0].CloudRegion);
//     "" falls back to the label "primary".
//   - primaryStates: the primary watcher's terminal component-state map
//     (dep.Result.ComponentStates).
//   - secondaryStates: region key → that secondary watcher's component-state
//     map (snapshotted from dep.secondaryWatchers).
//
// Returns the per-region breakdown (primary first, secondaries in sorted key
// order for deterministic output) and secondaryDegraded = true when ANY
// secondary is flagged degraded. secondaryDegraded is surface-only — callers
// log it and expose it but MUST NOT gate "ready" on it.
func ComputeRegionHealth(primaryRegion string, primaryStates map[string]string, secondaryStates map[string]map[string]string) (regions []RegionHealth, secondaryDegraded bool) {
	priReady, priTotal := countInstalled(primaryStates)
	primaryLabel := primaryRegion
	if primaryLabel == "" {
		primaryLabel = "primary"
	}
	regions = append(regions, RegionHealth{
		Region:   primaryLabel,
		Primary:  true,
		HRReady:  priReady,
		HRTotal:  priTotal,
		Degraded: false,
	})

	// Deterministic secondary ordering — sort the region keys so the
	// emitted slice (and any JSON the API serialises from it) is stable
	// across polls and across test runs.
	keys := make([]string, 0, len(secondaryStates))
	for k := range secondaryStates {
		keys = append(keys, k)
	}
	sortStrings(keys)

	for _, region := range keys {
		secReady, secTotal := countInstalled(secondaryStates[region])
		degraded := regionDegraded(priReady, secReady, secTotal)
		if degraded {
			secondaryDegraded = true
		}
		regions = append(regions, RegionHealth{
			Region:   region,
			Primary:  false,
			HRReady:  secReady,
			HRTotal:  secTotal,
			Degraded: degraded,
		})
	}
	return regions, secondaryDegraded
}

// sortStrings is a tiny in-package insertion sort so region_health.go does
// not pull "sort" just for one deterministic ordering. The secondary-region
// count is tiny (1-3 regions in every real prov), so the O(n^2) cost is
// irrelevant.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
