// region_health_live_5600_test.go — falsifiable guards for #5600: the
// per-region HelmRelease census served to the console must describe the
// cluster NOW, not the cluster as it was when Phase 1 terminated.
//
// ── WHY THE EXISTING TESTS MISSED THIS ────────────────────────────────────
// provisioner.ComputeRegionHealth already had thorough table tests
// (region_health_test.go) and State() already had census tests
// (deployments_region_health_test.go). Every one of them FEEDS THE CENSUS A
// SYNTHETIC MAP — either directly to the pure function, or by hand-writing
// Result.Regions onto the deployment record. Neither shape can observe
// staleness: the pure function is correct for whatever it is handed, and a
// hand-written Result.Regions is by definition in agreement with itself. So
// the whole class "the map handed to ComputeRegionHealth is a frozen Phase-1
// relic" was invisible, and stayed invisible for the entire post-handover
// lifetime of every 2-region Sovereign.
//
// The guards below therefore do the one thing those tests structurally could
// not: they put a LIVE cluster (fake dynamic client) BEHIND the deployment
// and assert the served payload tracks the cluster, not the stored snapshot.
// Each fixture's stored snapshot deliberately DISAGREES with the live cluster,
// so a regression to snapshot-serving fails loudly.
//
// ── VACUITY CHECK — BOTH DIRECTIONS ───────────────────────────────────────
// A guard that can only ever go one way proves nothing. These four cover:
//
//	A. snapshot says DEGRADED, cluster is converged  → must report NOT degraded
//	B. the literal hw292 shape (7 by-design-suspended secondary HRs)
//	                                                 → must report NOT degraded
//	C. snapshot says CONVERGED, cluster has 12 genuinely non-Ready,
//	   UN-suspended HRs on the secondary             → must report DEGRADED
//	D. no live read possible                         → must serve the snapshot
//	                                                    LABELLED as stale
//
// C is the one that matters most: it proves the fix did not simply teach the
// census to answer "false". A real fault still degrades the region, which is
// the constraint region_health.go:69 states explicitly.
//
// Refs #5600.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// hrSpec describes one HelmRelease in a fixture cluster.
type hrSpec struct {
	name string
	// ready=true  → status.conditions Ready=True             (installed)
	// suspended   → spec.suspend=true, no Ready=True         (installed, via
	//               the Wave 5.103 / #2447 coercion in ListAndSnapshotHelmReleases)
	// otherwise   → Ready=False reason=InstallFailed         (failed)
	ready     bool
	suspended bool
}

// newHRFixtureCluster builds a fake dynamic client serving the supplied
// HelmReleases in flux-system, with the same GVR/list-kind registration the
// helmwatch package's own fixtures use.
func newHRFixtureCluster(t *testing.T, hrs []hrSpec) *dynamicfake.FakeDynamicClient {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmReleaseList",
	}, &unstructured.UnstructuredList{})

	objs := make([]runtime.Object, 0, len(hrs))
	for _, hr := range hrs {
		spec := map[string]any{}
		var cond map[string]any
		switch {
		case hr.suspended:
			spec["suspend"] = true
			// A suspended HR is never marked Ready by Flux — this is the
			// exact live shape that froze as "non-installed" in the Phase-1
			// snapshot on hw292.
			cond = map[string]any{
				"type":               "Ready",
				"status":             string(metav1.ConditionFalse),
				"reason":             "InstallFailed",
				"message":            "suspended",
				"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
			}
		case hr.ready:
			cond = map[string]any{
				"type":               "Ready",
				"status":             string(metav1.ConditionTrue),
				"reason":             "ReconciliationSucceeded",
				"message":            "Helm install succeeded",
				"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
			}
		default:
			cond = map[string]any{
				"type":               "Ready",
				"status":             string(metav1.ConditionFalse),
				"reason":             "InstallFailed",
				"message":            "Helm install failed: timed out waiting for the condition",
				"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
			}
		}
		u := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "helm.toolkit.fluxcd.io/v2",
				"kind":       "HelmRelease",
				"metadata": map[string]any{
					"name":      hr.name,
					"namespace": helmwatch.FluxNamespace,
				},
				"spec":   spec,
				"status": map[string]any{"conditions": []any{cond}},
			},
		}
		u.SetGroupVersionKind(schema.GroupVersionKind{
			Group:   "helm.toolkit.fluxcd.io",
			Version: "v2",
			Kind:    "HelmRelease",
		})
		objs = append(objs, u)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{helmwatch.HelmReleaseGVR: "HelmReleaseList"},
		objs...,
	)
}

const (
	census5600PrimaryRegion   = "me-east-215-a"
	census5600SecondaryRegion = "me-east-215-b-1"
	census5600PrimaryKC       = "kubeconfig-fixture:primary"
	census5600SecondaryKC     = "kubeconfig-fixture:secondary"
)

// census5600Env wires a post-handover 2-region deployment: NO Phase-1
// watchers attached (so the census takes the post-handover path), a FROZEN
// Result.Regions snapshot, a primary kubeconfig at <dir>/<id>.yaml and a
// secondary at <dir>/<id>-<region>.yaml, and a dynamicFactory that routes each
// kubeconfig to its own fixture cluster.
//
// The two kubeconfig locations are the real ones: resolvePrimaryKubeconfigPath
// reads h.kubeconfigsDir; secondaryKubeconfigsForCutover reads the
// CATALYST_K8SCACHE_KUBECONFIGS_DIR-backed secondaryKubeconfigsDir(). Both are
// pointed at the same t.TempDir() here, exactly as they are on a live PVC.
func census5600Env(t *testing.T, depID string, snapshot *provisioner.Result, primary, secondary []hrSpec) (*Handler, *Deployment) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CATALYST_K8SCACHE_KUBECONFIGS_DIR", dir)

	if primary != nil {
		if err := os.WriteFile(filepath.Join(dir, depID+".yaml"), []byte(census5600PrimaryKC), 0o600); err != nil {
			t.Fatalf("write primary kubeconfig: %v", err)
		}
	}
	if secondary != nil {
		p := filepath.Join(dir, depID+"-"+census5600SecondaryRegion+".yaml")
		if err := os.WriteFile(p, []byte(census5600SecondaryKC), 0o600); err != nil {
			t.Fatalf("write secondary kubeconfig: %v", err)
		}
	}

	primaryDyn := newHRFixtureCluster(t, primary)
	secondaryDyn := newHRFixtureCluster(t, secondary)

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.kubeconfigsDir = dir
	h.dynamicFactory = func(kubeconfig string) (dynamic.Interface, error) {
		switch kubeconfig {
		case census5600PrimaryKC:
			return primaryDyn, nil
		case census5600SecondaryKC:
			return secondaryDyn, nil
		}
		return nil, os.ErrNotExist
	}

	dep := &Deployment{
		ID:        depID,
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "t99.omani.works",
			Provider:      "huawei",
			Region:        census5600PrimaryRegion,
			Regions: []provisioner.RegionSpec{
				{CloudRegion: census5600PrimaryRegion},
				{CloudRegion: census5600SecondaryRegion},
			},
		},
		Result: snapshot,
	}
	close(dep.eventsCh)
	close(dep.done)
	h.deployments.Store(dep.ID, dep)
	return h, dep
}

// getDeployment5600 drives the real GET /api/v1/deployments/{id} handler —
// the exact endpoint the sovereign-admin console's readiness pill reads — and
// returns the decoded payload.
func getDeployment5600(t *testing.T, h *Handler, depID string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+depID, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", depID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	h.GetDeployment(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /deployments/%s = %d, want 200; body=%s", depID, w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, w.Body.String())
	}
	return got
}

// regionRow pulls one region entry out of the decoded payload.
func regionRow(t *testing.T, payload map[string]any, idx int) map[string]any {
	t.Helper()
	raw, ok := payload["regions"].([]any)
	if !ok {
		t.Fatalf("regions missing or wrong type: %T %v", payload["regions"], payload["regions"])
	}
	if idx >= len(raw) {
		t.Fatalf("regions has %d entries, wanted index %d: %v", len(raw), idx, raw)
	}
	row, ok := raw[idx].(map[string]any)
	if !ok {
		t.Fatalf("regions[%d] wrong type: %T", idx, raw[idx])
	}
	return row
}

func regionInt(t *testing.T, row map[string]any, key string) int {
	t.Helper()
	f, ok := row[key].(float64)
	if !ok {
		t.Fatalf("region field %q missing or wrong type: %T %v", key, row[key], row[key])
	}
	return int(f)
}

// readyHRs builds n Ready HelmReleases named bp-comp-<i>.
func readyHRs(n int) []hrSpec {
	out := make([]hrSpec, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, hrSpec{name: "bp-comp-" + itoa2(i), ready: true})
	}
	return out
}

// itoa2 formats a small int with a stable 2-digit width so component ids sort
// predictably in failure output.
func itoa2(n int) string {
	const digits = "0123456789"
	if n < 10 {
		return "0" + string(digits[n])
	}
	return string(digits[(n/10)%10]) + string(digits[n%10])
}

// ── A. the staleness pin ─────────────────────────────────────────────────
//
// TestRegionCensus_IsNotFrozenAtPhase1 is the direct #5600 root-cause guard.
// The stored Phase-1 snapshot says the secondary is 55/63 and DEGRADED. The
// live cluster says both regions are fully converged. The served payload must
// follow the CLUSTER.
//
// On the pre-fix tree regionHealthForStateLocked returned d.Result.Regions
// verbatim whenever no watcher was attached — which, post-handover, is always
// — so this test fails with secondaryDegraded=true and hrReady=55.
func TestRegionCensus_IsNotFrozenAtPhase1(t *testing.T) {
	finished := time.Now().Add(-48 * time.Hour).UTC()
	snapshot := &provisioner.Result{
		SovereignFQDN:    "t99.omani.works",
		Phase1FinishedAt: &finished,
		// The frozen relic: at Phase-1-terminal time the secondary was
		// genuinely behind. Two days and one 11-step cutover later it is not.
		Regions: []provisioner.RegionHealth{
			{Region: census5600PrimaryRegion, Primary: true, HRReady: 63, HRTotal: 63},
			{Region: census5600SecondaryRegion, Primary: false, HRReady: 55, HRTotal: 63, Degraded: true},
		},
		SecondaryDegraded: true,
	}
	h, _ := census5600Env(t, "dep5600-stale", snapshot, readyHRs(20), readyHRs(20))

	got := getDeployment5600(t, h, "dep5600-stale")

	if deg, _ := got["secondaryDegraded"].(bool); deg {
		t.Errorf("secondaryDegraded = true on a live-converged 2-region Sovereign — the census is still being served from the frozen Phase-1 snapshot (#5600)")
	}
	if src, _ := got["regionCensusSource"].(string); src != "live-list" {
		t.Errorf("regionCensusSource = %q, want %q — the payload must say where the numbers came from", src, "live-list")
	}
	if stale, ok := got["regionCensusStale"]; ok {
		t.Errorf("regionCensusStale = %v on a freshly re-derived census, want the key absent", stale)
	}
	sec := regionRow(t, got, 1)
	if ready, total := regionInt(t, sec, "hrReady"), regionInt(t, sec, "hrTotal"); ready != 20 || total != 20 {
		t.Errorf("secondary census = %d/%d, want 20/20 (live) — got the frozen 55/63 shape instead", ready, total)
	}
	pri := regionRow(t, got, 0)
	if ready, total := regionInt(t, pri, "hrReady"), regionInt(t, pri, "hrTotal"); ready != 20 || total != 20 {
		t.Errorf("primary census = %d/%d, want 20/20 (live)", ready, total)
	}
}

// ── B. the hw292 shape ───────────────────────────────────────────────────
//
// TestRegionCensus_SuspendedByDesignSecondary_NotDegraded reproduces the
// filed symptom exactly: a converged, cut-over 2-region Sovereign whose
// secondary carries the seven by-design-suspended HelmReleases named in the
// issue. It must read secondaryDegraded=false.
//
// Note WHY it passes after the fix, because it is not a new exclusion rule:
// helmwatch.ListAndSnapshotHelmReleases already coerces spec.suspend=true to
// StateInstalled (Wave 5.103 / #2447). That coercion was always correct — it
// simply never reached the census, because the census never re-read the
// cluster. Nothing was added to providerInapplicableComponents and
// ComputeRegionHealth is untouched.
func TestRegionCensus_SuspendedByDesignSecondary_NotDegraded(t *testing.T) {
	finished := time.Now().Add(-72 * time.Hour).UTC()
	snapshot := &provisioner.Result{
		SovereignFQDN:    "t99.omani.works",
		Phase1FinishedAt: &finished,
		// The numbers hw292 actually served on 2026-08-03 with
		// cutoverComplete=true: "region primary=false hrReady 57/65 degraded=true".
		Regions: []provisioner.RegionHealth{
			{Region: census5600PrimaryRegion, Primary: true, HRReady: 65, HRTotal: 65},
			{Region: census5600SecondaryRegion, Primary: false, HRReady: 57, HRTotal: 65, Degraded: true},
		},
		SecondaryDegraded: true,
	}

	// Live region-a: 67 HRs, 4 suspended (2 of them the Huawei-gated hcloud
	// pair, which filterApplicable drops from the census under #4086).
	primary := append(readyHRs(63),
		hrSpec{name: "bp-cluster-autoscaler-hcloud", suspended: true},
		hrSpec{name: "bp-hcloud-ccm", suspended: true},
		hrSpec{name: "bp-velero", suspended: true},
		hrSpec{name: "bp-self-sovereign-cutover", suspended: true},
	)
	// Live region-b: 67 HRs, 60 Ready, and exactly the by-design-suspended
	// set the issue enumerates. ZERO un-suspended non-Ready HRs.
	secondary := append(readyHRs(60),
		hrSpec{name: "bp-catalyst-platform", suspended: true},
		hrSpec{name: "bp-cluster-autoscaler-hcloud", suspended: true},
		hrSpec{name: "bp-continuum", suspended: true},
		hrSpec{name: "bp-hcloud-ccm", suspended: true},
		hrSpec{name: "bp-openova-mcp", suspended: true},
		hrSpec{name: "bp-self-sovereign-cutover", suspended: true},
		hrSpec{name: "bp-velero", suspended: true},
	)

	h, _ := census5600Env(t, "dep5600-hw292", snapshot, primary, secondary)
	got := getDeployment5600(t, h, "dep5600-hw292")

	if deg, _ := got["secondaryDegraded"].(bool); deg {
		t.Errorf("secondaryDegraded = true on a converged cut-over Sovereign whose only non-Ready secondary HRs are suspended-by-design — this is the #5600 false Degraded")
	}
	sec := regionRow(t, got, 1)
	if deg, _ := sec["degraded"].(bool); deg {
		t.Errorf("regions[1].degraded = true, want false")
	}
	// 67 live HRs minus the 2 Huawei-inapplicable hcloud components = 65,
	// all of them installed once the suspend coercion is applied.
	if ready, total := regionInt(t, sec, "hrReady"), regionInt(t, sec, "hrTotal"); ready != 65 || total != 65 {
		t.Errorf("secondary census = %d/%d, want 65/65 — the frozen record said 57/65 while the cluster had ZERO un-suspended non-Ready HRs", ready, total)
	}
}

// ── C. the vacuity check — the census must still be able to say DEGRADED ──
//
// TestRegionCensus_LiveFault_StillDegrades runs the fixture in the OPPOSITE
// direction: the stored snapshot claims the secondary is fully converged,
// while the live cluster has 12 genuinely non-Ready, UN-suspended
// HelmReleases on it. The payload must flip to degraded.
//
// Without this, guard A and B would be satisfied by a census that always
// answers "false" — which would be a worse defect than the one being fixed,
// because it would mask a real secondary-region cascade (the hw145 shape
// #3611 exists to catch). This also proves the fix does NOT weaken
// providerInapplicableComponents: the 12 failures are ordinary components on
// the correct provider and they degrade the region exactly as before.
func TestRegionCensus_LiveFault_StillDegrades(t *testing.T) {
	finished := time.Now().Add(-24 * time.Hour).UTC()
	snapshot := &provisioner.Result{
		SovereignFQDN:    "t99.omani.works",
		Phase1FinishedAt: &finished,
		// The frozen relic here is optimistic — at Phase-1-terminal time
		// both regions were green. The cascade happened afterwards.
		Regions: []provisioner.RegionHealth{
			{Region: census5600PrimaryRegion, Primary: true, HRReady: 65, HRTotal: 65},
			{Region: census5600SecondaryRegion, Primary: false, HRReady: 65, HRTotal: 65},
		},
		SecondaryDegraded: false,
	}

	primary := readyHRs(65)
	secondary := readyHRs(53)
	for i := 53; i < 65; i++ {
		// Ready=False / InstallFailed, NOT suspended — a real fault.
		secondary = append(secondary, hrSpec{name: "bp-comp-" + itoa2(i)})
	}

	h, _ := census5600Env(t, "dep5600-realfault", snapshot, primary, secondary)
	got := getDeployment5600(t, h, "dep5600-realfault")

	if deg, _ := got["secondaryDegraded"].(bool); !deg {
		t.Errorf("secondaryDegraded = false while the live secondary has 12 genuinely non-Ready un-suspended HelmReleases — a live census that can only answer 'not degraded' would hide the exact hw145 cascade #3611 exists to catch")
	}
	sec := regionRow(t, got, 1)
	if ready, total := regionInt(t, sec, "hrReady"), regionInt(t, sec, "hrTotal"); ready != 53 || total != 65 {
		t.Errorf("secondary census = %d/%d, want 53/65 (live)", ready, total)
	}
	if src, _ := got["regionCensusSource"].(string); src != "live-list" {
		t.Errorf("regionCensusSource = %q, want %q", src, "live-list")
	}
}

// ── D. honest fallback ───────────────────────────────────────────────────
//
// TestRegionCensus_NoLiveRead_FallsBackLabelledStale covers the case where no
// live read is possible at all (no kubeconfig on the PVC — a mothership record
// after the Sovereign cut over, or a wiped env). The snapshot is still served
// so the console does not go blank, but it is LABELLED: regionCensusSource
// names it as the Phase-1 relic and regionCensusStale flags it.
//
// This is the difference between "we don't know" and "we know, and it's this"
// — presenting a frozen number as current is what let hw292's false Degraded
// go unchallenged for three days.
func TestRegionCensus_NoLiveRead_FallsBackLabelledStale(t *testing.T) {
	finished := time.Now().Add(-6 * time.Hour).UTC()
	snapshot := &provisioner.Result{
		SovereignFQDN:    "t99.omani.works",
		Phase1FinishedAt: &finished,
		Regions: []provisioner.RegionHealth{
			{Region: census5600PrimaryRegion, Primary: true, HRReady: 65, HRTotal: 65},
			{Region: census5600SecondaryRegion, Primary: false, HRReady: 57, HRTotal: 65, Degraded: true},
		},
		SecondaryDegraded: true,
	}
	// nil/nil → no kubeconfig files written, so every live read misses.
	h, _ := census5600Env(t, "dep5600-nolive", snapshot, nil, nil)

	got := getDeployment5600(t, h, "dep5600-nolive")

	if src, _ := got["regionCensusSource"].(string); src != "phase1-snapshot" {
		t.Errorf("regionCensusSource = %q, want %q — a served snapshot must name itself", src, "phase1-snapshot")
	}
	if stale, _ := got["regionCensusStale"].(bool); !stale {
		t.Errorf("regionCensusStale = %v, want true — the Phase-1 snapshot is by construction not current", got["regionCensusStale"])
	}
	// The numbers themselves are still surfaced (blank is worse than labelled).
	sec := regionRow(t, got, 1)
	if ready, total := regionInt(t, sec, "hrReady"), regionInt(t, sec, "hrTotal"); ready != 57 || total != 65 {
		t.Errorf("fallback census = %d/%d, want the persisted 57/65", ready, total)
	}
}

// ── E. partial reads are discarded, never published ──────────────────────
//
// TestRegionCensus_PartialRead_NotPublished proves the completeness contract:
// when a DECLARED secondary region cannot be listed, the refresh publishes
// NOTHING rather than a census missing that region. A census built from the
// primary alone would report a single fully-green region and structurally
// could not flag a shortfall — an invented all-clear, which is precisely the
// failure mode this issue is about, only inverted.
func TestRegionCensus_PartialRead_NotPublished(t *testing.T) {
	finished := time.Now().Add(-6 * time.Hour).UTC()
	snapshot := &provisioner.Result{
		SovereignFQDN:    "t99.omani.works",
		Phase1FinishedAt: &finished,
		Regions: []provisioner.RegionHealth{
			{Region: census5600PrimaryRegion, Primary: true, HRReady: 65, HRTotal: 65},
			{Region: census5600SecondaryRegion, Primary: false, HRReady: 57, HRTotal: 65, Degraded: true},
		},
		SecondaryDegraded: true,
	}
	// Primary kubeconfig present, secondary absent → the declared secondary
	// count (1) exceeds the resolvable paths (0).
	h, _ := census5600Env(t, "dep5600-partial", snapshot, readyHRs(65), nil)

	got := getDeployment5600(t, h, "dep5600-partial")

	if src, _ := got["regionCensusSource"].(string); src != "phase1-snapshot" {
		t.Errorf("regionCensusSource = %q, want %q — a half-read census must not be published", src, "phase1-snapshot")
	}
	raw, _ := got["regions"].([]any)
	if len(raw) != 2 {
		t.Errorf("regions has %d entries, want 2 — the fallback snapshot must keep BOTH declared regions visible", len(raw))
	}
}
