// Tests for the #5012 per-SECONDARY-region Flux-CRD-absent detection — the
// mirror of the primary's #5042 probe (phase1_watch_flux_crd_absent_test.go).
//
// Background (triaged from #5012, same class as #5042 on the SECONDARY-region
// render of the shared cloudinit-control-plane.tftpl): on an intermittent
// fresh-prov bootstrap wedge a SECONDARY control-plane is HEALTHY — its
// kubeconfig is PUT back — but cloud-init's flux-install stage silently didn't
// land, so its Flux HelmRelease CRD (helmreleases.helm.toolkit.fluxcd.io) is
// ABSENT. Before this fix spawnSecondaryRegionWatchers built a helmwatch
// informer against that region DIRECTLY, which then observed zero HelmReleases
// until stopSecondaries cancelled it — an invisible 0/0 with NO named outcome
// (the primary got OutcomeFluxCRDsAbsent; the secondary got nothing).
//
// What this file proves:
//
//  1. probeFluxCRDAbsentForRegion mirrors the primary probe's three verdicts:
//     present → proceed (false), absent-past-budget → classify (true),
//     transient error → fail-open (false); plus ctx-cancellation → fail-open.
//  2. spawnSecondaryRegionWatchers, driven against an absent-CRD secondary,
//     records the region in Result.SecondaryFluxCRDAbsentRegions, registers a
//     nil census slot (so the #3611 census surfaces it degraded), emits a LOUD
//     region-tagged warn naming cloud-init's flux-install stage, spins NO real
//     watcher, and NEVER fails the deployment (Status stays phase1-watching).
//  3. spawnSecondaryRegionWatchers against a present-CRD secondary spins a
//     normal real watcher and records nothing absent — no regression.
//  4. hw255 recurrence (#5012, dep 5762118f63abef96): an absent-at-budget
//     verdict is a SURFACE, not a terminal. Under the #3129 PUT-early
//     invariant a healthy secondary installs flux MINUTES after its
//     kubeconfig lands (gateway-api CRDs + helm download + the full cilium
//     DaemonSet rollout run in between), so when the CRD lands AFTER the
//     probe budget the spawn must clear Result.SecondaryFluxCRDAbsentRegions,
//     release the nil census slot, and spin the REAL watcher — instead of
//     freezing the region's census at 0/0 degraded for the whole prov.
package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
)

// fakeDynamicFactoryHRListErrorUntil — closure that returns fake dynamic
// clients whose List against the HelmRelease GVR fails with listErr for the
// first `failCalls` List invocations ACROSS ALL clients it builds, then
// succeeds (empty list). Simulates the hw255 #5012 shape: the CRD is cleanly
// absent while the probe runs (cloud-init is still mid cilium-rollout), then
// flux-install lands and the CRD becomes servable. The counter is shared
// across client builds because the probe, the late-landing waiter, and the
// real watcher each build their own client through the same factory.
func fakeDynamicFactoryHRListErrorUntil(listErr error, failCalls int64) func(string) (dynamic.Interface, error) {
	var calls atomic.Int64
	return func(_ string) (dynamic.Interface, error) {
		scheme := newFakeSchemeForHandler()
		client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			scheme,
			map[schema.GroupVersionResource]string{helmwatch.HelmReleaseGVR: "HelmReleaseList"},
		)
		client.PrependReactor("list", "helmreleases", func(_ clienttesting.Action) (bool, runtime.Object, error) {
			if calls.Add(1) <= failCalls {
				return true, nil, listErr
			}
			return false, nil, nil // fall through to the default (empty-list) reactor
		})
		return client, nil
	}
}

// TestProbeFluxCRDAbsentForRegion covers the secondary probe verdicts in
// isolation, exactly mirroring TestProbeFluxCRDAbsent for the primary.
func TestProbeFluxCRDAbsentForRegion(t *testing.T) {
	t.Run("CRDPresentProceeds", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.dynamicFactory = fakeDynamicFactoryFromObjects() // List succeeds (empty) → servable
		h.fluxCRDProbeBudget = 2 * time.Second
		h.fluxCRDProbePollInterval = 20 * time.Millisecond
		dep := makeDeploymentWithKubeconfig(t, h, "sec-crd-present-probe", "fake-kubeconfig: yaml")

		if got := h.probeFluxCRDAbsentForRegion(context.Background(), dep, "region-b", "fake-kubeconfig: yaml"); got {
			t.Errorf("probeFluxCRDAbsentForRegion = true, want false (CRD servable → proceed to watcher)")
		}
	})

	t.Run("CRDAbsentPastBudgetClassifies", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.dynamicFactory = fakeDynamicFactoryWithHRListError(hrCRDNotFound())
		h.fluxCRDProbeBudget = 150 * time.Millisecond
		h.fluxCRDProbePollInterval = 25 * time.Millisecond
		dep := makeDeploymentWithKubeconfig(t, h, "sec-crd-absent-probe", "fake-kubeconfig: yaml")

		start := time.Now()
		got := h.probeFluxCRDAbsentForRegion(context.Background(), dep, "region-b", "fake-kubeconfig: yaml")
		elapsed := time.Since(start)

		if !got {
			t.Errorf("probeFluxCRDAbsentForRegion = false, want true (CRD absent past budget → classify)")
		}
		// Must exhaust the budget before classifying — the flux-install manifest
		// may still land within the budget.
		if elapsed < 150*time.Millisecond {
			t.Errorf("probe returned after %v — classified before the %s budget elapsed", elapsed, 150*time.Millisecond)
		}
	})

	t.Run("TransientErrorFailsOpen", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		h.dynamicFactory = fakeDynamicFactoryWithHRListError(errors.New("connection refused: dial tcp 10.0.0.1:6443"))
		h.fluxCRDProbeBudget = 150 * time.Millisecond
		h.fluxCRDProbePollInterval = 25 * time.Millisecond
		dep := makeDeploymentWithKubeconfig(t, h, "sec-crd-transient-probe", "fake-kubeconfig: yaml")

		if got := h.probeFluxCRDAbsentForRegion(context.Background(), dep, "region-b", "fake-kubeconfig: yaml"); got {
			t.Errorf("probeFluxCRDAbsentForRegion = true, want false (transient error must fail-open)")
		}
	})

	t.Run("CtxCancelledFailsOpen", func(t *testing.T) {
		h := NewWithPDM(silentLogger(), &fakePDM{})
		// CRD is absent, but the region watcher's context is cancelled before
		// the budget elapses — a cancelled probe must NEVER invent an absent
		// verdict (stopSecondaries aborting a probe is not a diagnosis).
		h.dynamicFactory = fakeDynamicFactoryWithHRListError(hrCRDNotFound())
		h.fluxCRDProbeBudget = 5 * time.Second
		h.fluxCRDProbePollInterval = 25 * time.Millisecond
		dep := makeDeploymentWithKubeconfig(t, h, "sec-crd-ctxcancel-probe", "fake-kubeconfig: yaml")

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(80 * time.Millisecond)
			cancel()
		}()
		start := time.Now()
		got := h.probeFluxCRDAbsentForRegion(ctx, dep, "region-b", "fake-kubeconfig: yaml")
		elapsed := time.Since(start)

		if got {
			t.Errorf("probeFluxCRDAbsentForRegion = true, want false (ctx cancelled must fail-open)")
		}
		// Must have returned on cancellation, not after the 5s budget.
		if elapsed > 2*time.Second {
			t.Errorf("probe returned after %v — did not honour ctx cancellation", elapsed)
		}
	})
}

// TestSpawnSecondaryRegionWatchers_FluxCRDAbsent drives the full secondary
// watcher-spawn path against a healthy-but-Flux-less secondary control-plane:
// its kubeconfig is on disk, the apiserver answers, but the HelmRelease CRD is
// absent. The spawn must record the region NAMED-degraded, register a nil
// census slot, emit a LOUD region-tagged warn, spin NO real watcher, and NEVER
// fail the deployment. #5012.
func TestSpawnSecondaryRegionWatchers_FluxCRDAbsent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryWithHRListError(hrCRDNotFound())
	h.fluxCRDProbeBudget = 150 * time.Millisecond
	h.fluxCRDProbePollInterval = 25 * time.Millisecond

	kcDir := t.TempDir()
	h.kubeconfigsDir = kcDir
	depID := "sec-flux-absent"
	region := "me-east-215-b-1"

	dep := makeDeploymentWithKubeconfig(t, h, depID, "primary-kubeconfig: yaml")
	// The secondary control-plane deposited its kubeconfig under <id>-<region>.yaml.
	if err := os.WriteFile(filepath.Join(kcDir, depID+"-"+region+".yaml"), []byte("secondary-kubeconfig: yaml"), 0o600); err != nil {
		t.Fatalf("write secondary kubeconfig: %v", err)
	}

	stop := h.spawnSecondaryRegionWatchers(dep)
	defer stop()

	// Wait until the region is recorded absent AND the loud warn has landed.
	if !waitUntil(2*time.Second, func() bool {
		dep.mu.Lock()
		recorded := containsStr(dep.Result.SecondaryFluxCRDAbsentRegions, region)
		dep.mu.Unlock()
		return recorded && hasWarnEventContaining(dep, region, "flux-install", "#5012")
	}) {
		dep.mu.Lock()
		got := append([]string(nil), dep.Result.SecondaryFluxCRDAbsentRegions...)
		dep.mu.Unlock()
		t.Fatalf("timed out waiting for region %q to be recorded flux-crd-absent + warn; SecondaryFluxCRDAbsentRegions=%v", region, got)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()

	// A nil census slot must be registered so the #3611 census surfaces the
	// region as 0/0 degraded — but NO real watcher for a doomed no-CRD region.
	sw, exists := dep.secondaryWatchers[region]
	if !exists {
		t.Errorf("secondaryWatchers has no slot for %q — the census cannot surface it", region)
	}
	if sw != nil {
		t.Errorf("secondaryWatchers[%q] = %v, want nil (a doomed no-CRD watcher must be skipped)", region, sw)
	}
	// A secondary flux-absent must NEVER fail/flip the whole deployment.
	if dep.Status == "failed" {
		t.Errorf("Status = %q — a flux-absent SECONDARY must never fail the prov (surface-not-gate)", dep.Status)
	}
	if dep.Result.Phase1Outcome != "" {
		t.Errorf("Phase1Outcome = %q — the secondary path must not touch the primary's terminal outcome", dep.Result.Phase1Outcome)
	}
}

// TestSpawnSecondaryRegionWatchers_FluxCRDPresent proves no regression: a
// secondary whose Flux CRD is present spins a normal real watcher and records
// nothing absent. #5012.
func TestSpawnSecondaryRegionWatchers_FluxCRDPresent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.dynamicFactory = fakeDynamicFactoryFromObjects(
		makeReadyHR("bp-cilium"),
		makeReadyHR("bp-cert-manager"),
		makeReadyHR("bp-flux"),
	)
	h.fluxCRDProbeBudget = 2 * time.Second
	h.fluxCRDProbePollInterval = 20 * time.Millisecond
	h.phase1WatchTimeout = 5 * time.Second

	kcDir := t.TempDir()
	h.kubeconfigsDir = kcDir
	depID := "sec-flux-present"
	region := "me-east-215-b-1"

	dep := makeDeploymentWithKubeconfig(t, h, depID, "primary-kubeconfig: yaml")
	if err := os.WriteFile(filepath.Join(kcDir, depID+"-"+region+".yaml"), []byte("secondary-kubeconfig: yaml"), 0o600); err != nil {
		t.Fatalf("write secondary kubeconfig: %v", err)
	}

	stop := h.spawnSecondaryRegionWatchers(dep)
	defer stop()

	// Wait until a REAL (non-nil) watcher is registered for the region.
	if !waitUntil(3*time.Second, func() bool {
		dep.mu.Lock()
		sw, exists := dep.secondaryWatchers[region]
		dep.mu.Unlock()
		return exists && sw != nil
	}) {
		t.Fatalf("timed out waiting for a real secondary watcher for region %q (present-CRD path must spin one)", region)
	}

	dep.mu.Lock()
	defer dep.mu.Unlock()
	if containsStr(dep.Result.SecondaryFluxCRDAbsentRegions, region) {
		t.Errorf("region %q recorded flux-crd-absent on a CRD-PRESENT prov — the probe false-positived", region)
	}
}

// TestSpawnSecondaryRegionWatchers_FluxCRDLandsLate drives the hw255 #5012
// recurrence end-to-end: the secondary's CRD is cleanly absent PAST the probe
// budget (probe classifies absent → region recorded degraded), then flux lands
// (the #3129 PUT-early gap closing). The spawn must then CLEAR the recorded
// degraded signal and register a REAL watcher so the census reports true HR
// counts — the region must not stay frozen at 0/0 degraded.
func TestSpawnSecondaryRegionWatchers_FluxCRDLandsLate(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// The probe polls ~6 times inside its 150ms budget; 30 failing List calls
	// comfortably outlast the budget (positive absent verdict) and then keep
	// the late-landing waiter polling for a few more rounds before the CRD
	// becomes servable.
	h.dynamicFactory = fakeDynamicFactoryHRListErrorUntil(hrCRDNotFound(), 30)
	h.fluxCRDProbeBudget = 150 * time.Millisecond
	h.fluxCRDProbePollInterval = 10 * time.Millisecond
	h.phase1WatchTimeout = 5 * time.Second

	kcDir := t.TempDir()
	h.kubeconfigsDir = kcDir
	depID := "sec-flux-lands-late"
	region := "me-east-215-b-1"

	dep := makeDeploymentWithKubeconfig(t, h, depID, "primary-kubeconfig: yaml")
	if err := os.WriteFile(filepath.Join(kcDir, depID+"-"+region+".yaml"), []byte("secondary-kubeconfig: yaml"), 0o600); err != nil {
		t.Fatalf("write secondary kubeconfig: %v", err)
	}

	stop := h.spawnSecondaryRegionWatchers(dep)
	defer stop()

	// Phase A — the probe must first classify absent and record the region
	// degraded (the surface fires while flux genuinely isn't there yet).
	if !waitUntil(2*time.Second, func() bool {
		dep.mu.Lock()
		recorded := containsStr(dep.Result.SecondaryFluxCRDAbsentRegions, region)
		dep.mu.Unlock()
		return recorded
	}) {
		t.Fatalf("timed out waiting for region %q to first be recorded flux-crd-absent", region)
	}

	// Phase B — once the CRD lands, the record must clear and a REAL watcher
	// must be registered.
	if !waitUntil(3*time.Second, func() bool {
		dep.mu.Lock()
		cleared := !containsStr(dep.Result.SecondaryFluxCRDAbsentRegions, region)
		sw, exists := dep.secondaryWatchers[region]
		dep.mu.Unlock()
		return cleared && exists && sw != nil
	}) {
		dep.mu.Lock()
		gotAbsent := append([]string(nil), dep.Result.SecondaryFluxCRDAbsentRegions...)
		sw, exists := dep.secondaryWatchers[region]
		dep.mu.Unlock()
		t.Fatalf("timed out waiting for late-landing recovery: SecondaryFluxCRDAbsentRegions=%v, watcherSlot(exists=%v,nil=%v) — the region stayed frozen degraded", gotAbsent, exists, sw == nil)
	}

	// The recovery must never have flipped the deployment terminal.
	dep.mu.Lock()
	defer dep.mu.Unlock()
	if dep.Status == "failed" {
		t.Errorf("Status = %q — a late-landing secondary must never fail the prov", dep.Status)
	}
	if dep.Result.Phase1Outcome != "" {
		t.Errorf("Phase1Outcome = %q — the secondary path must not touch the primary's terminal outcome", dep.Result.Phase1Outcome)
	}
}

// waitFor polls cond every 10ms until it returns true or the deadline elapses.
func waitUntil(within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// hasWarnEventContaining reports whether the deployment's durable event buffer
// holds a warn-level event whose message contains every one of subs.
func hasWarnEventContaining(dep *Deployment, subs ...string) bool {
	for _, ev := range dep.snapshotEvents() {
		if ev.Level != "warn" {
			continue
		}
		all := true
		for _, s := range subs {
			if !strings.Contains(ev.Message, s) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}
