package handler

import (
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// newReadyDeployment builds a minimal ready Deployment with no live
// Phase-1 watchers attached, so State()/regionHealthForStateLocked take
// the persisted-snapshot path (Result.Regions) — the post-handover shape.
func newReadyDeployment(id string, result *provisioner.Result) *Deployment {
	dep := &Deployment{
		ID:        id,
		Status:    "ready",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "omantel.omani.works",
		},
		Result: result,
	}
	close(dep.eventsCh)
	close(dep.done)
	return dep
}

// TestState_SurfacesDegradedSecondary is the #3611 contract: a "ready"
// multi-region deployment whose secondary region is degraded lifts the
// per-region breakdown + secondaryDegraded=true to the top-level State()
// shape, so the console stops rendering a flat green "ready". Mirrors the
// hw145 evidence (region-a 60/63, region-b 48/63).
func TestState_SurfacesDegradedSecondary(t *testing.T) {
	dep := newReadyDeployment("test-degraded-secondary", &provisioner.Result{
		Regions: []provisioner.RegionHealth{
			{Region: "me-east-215-a", Primary: true, HRReady: 60, HRTotal: 63, Degraded: false},
			{Region: "me-east-215-b", Primary: false, HRReady: 48, HRTotal: 63, Degraded: true},
		},
		SecondaryDegraded: true,
	})

	out := dep.State()

	// status is intentionally still "ready" — surface, not gate.
	if out["status"] != "ready" {
		t.Fatalf("status = %v, want ready (surface-not-gate)", out["status"])
	}

	deg, ok := out["secondaryDegraded"].(bool)
	if !ok || !deg {
		t.Fatalf("secondaryDegraded = %v (ok=%v), want true at top level", out["secondaryDegraded"], ok)
	}

	regions, ok := out["regions"].([]provisioner.RegionHealth)
	if !ok {
		t.Fatalf("regions missing or wrong type at top level: %T %v", out["regions"], out["regions"])
	}
	if len(regions) != 2 {
		t.Fatalf("want 2 regions, got %d: %+v", len(regions), regions)
	}
	if !regions[0].Primary || regions[0].Degraded {
		t.Errorf("region[0] should be the non-degraded primary; got %+v", regions[0])
	}
	if regions[1].Primary || !regions[1].Degraded {
		t.Errorf("region[1] should be the degraded secondary; got %+v", regions[1])
	}
	if regions[1].HRReady != 48 || regions[1].HRTotal != 63 {
		t.Errorf("secondary census = %d/%d, want 48/63", regions[1].HRReady, regions[1].HRTotal)
	}
}

// TestState_BothRegionsReady_NoBadge confirms a healthy 2-region prov
// surfaces the per-region breakdown but secondaryDegraded=false (no badge).
func TestState_BothRegionsReady_NoBadge(t *testing.T) {
	dep := newReadyDeployment("test-both-ready", &provisioner.Result{
		Regions: []provisioner.RegionHealth{
			{Region: "me-east-215-a", Primary: true, HRReady: 63, HRTotal: 63, Degraded: false},
			{Region: "me-east-215-b", Primary: false, HRReady: 63, HRTotal: 63, Degraded: false},
		},
		SecondaryDegraded: false,
	})

	out := dep.State()

	deg, ok := out["secondaryDegraded"].(bool)
	if !ok {
		t.Fatalf("secondaryDegraded missing; want present=false")
	}
	if deg {
		t.Errorf("secondaryDegraded = true, want false for a healthy 2-region prov")
	}
	if regions, ok := out["regions"].([]provisioner.RegionHealth); !ok || len(regions) != 2 {
		t.Errorf("want 2 regions surfaced, got %T len=%d", out["regions"], len(regions))
	}
}

// TestState_SingleRegion_NoRegionsKey confirms a single-region prov (no
// Result.Regions persisted, no live watchers) does NOT emit a regions key
// or a secondaryDegraded key — the badge is multi-region-only and a UI
// client that pre-dates the field never sees a stray key.
func TestState_SingleRegion_NoRegionsKey(t *testing.T) {
	dep := newReadyDeployment("test-single-region", &provisioner.Result{
		// No Regions populated (single-region prov), Phase-1 fields only.
		ComponentStates: map[string]string{"bp-cilium": "installed"},
	})

	out := dep.State()

	if _, ok := out["regions"]; ok {
		t.Errorf("regions key should be absent for a single-region prov; got %v", out["regions"])
	}
	if _, ok := out["secondaryDegraded"]; ok {
		t.Errorf("secondaryDegraded key should be absent for a single-region prov; got %v", out["secondaryDegraded"])
	}
}

// TestState_NilResult_NoRegionHealth guards the nil-Result path (Phase 0
// failed before a Result was attached): State() must not panic and must
// not emit the region-health keys.
func TestState_NilResult_NoRegionHealth(t *testing.T) {
	dep := newReadyDeployment("test-nil-result", nil)

	out := dep.State()

	if _, ok := out["regions"]; ok {
		t.Errorf("regions key should be absent when Result is nil; got %v", out["regions"])
	}
	if _, ok := out["secondaryDegraded"]; ok {
		t.Errorf("secondaryDegraded key should be absent when Result is nil; got %v", out["secondaryDegraded"])
	}
}
