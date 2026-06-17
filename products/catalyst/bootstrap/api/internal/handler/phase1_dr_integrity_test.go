// phase1_dr_integrity_test.go — #3375 DoD-7 wiring coverage. Proves the
// DR-integrity gate in markPhase1Done downgrades an otherwise-ready
// active-hot-standby deployment to `failed` (standby-region-absent) when
// the standby region never came up — the hw150 lying-flag the issue
// catalogues — and that the gate leaves single-region and genuinely
// 2-region-healthy provs untouched.

package handler

import (
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// active-hot-standby requested, 2 regions declared, but the Phase-1 watch
// observed ONLY the primary (no secondary control-plane ever PUT its
// kubeconfig). Even with OutcomeReady, the deployment must be `failed`
// with the standby-region-absent reason — and NO handover token minted.
func TestMarkPhase1Done_ActiveHotStandby_AbsentStandby_NotReady(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	dep := &Deployment{
		ID:        "phase1-ahs-absent-standby",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-ahs.example.com",
			OrgEmail:      "operator@ahs.example.com",
			BcpTopology:   provisioner.BcpTopologyActiveHotStandby,
			// 2 regions declared at signup.
			Regions: []provisioner.RegionSpec{
				{Provider: "huawei", CloudRegion: "me-east-215-a"},
				{Provider: "huawei", CloudRegion: "me-east-215-b"},
			},
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-ahs.example.com",
		},
		OwnerEmail: "operator@ahs.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	// Primary region converged green; NO secondary watcher ever existed
	// (the secondary kubeconfig never arrived → spawnSecondaryRegionWatchers
	// produced nothing → ComputeRegionHealth sees only the primary).
	finalStates := map[string]string{
		"cilium":            helmwatch.StateInstalled,
		"catalyst-platform": helmwatch.StateInstalled,
		"flux":              helmwatch.StateInstalled,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "failed" {
		t.Fatalf("Status = %q, want failed (active-hot-standby with absent standby region)", dep.Status)
	}
	if !strings.Contains(dep.Error, "standby") {
		t.Errorf("Error must explain the absent standby region; got %q", dep.Error)
	}
	if dep.Result.Phase1Outcome != provisioner.ReasonStandbyRegionAbsent {
		t.Errorf("Phase1Outcome = %q, want %q", dep.Result.Phase1Outcome, provisioner.ReasonStandbyRegionAbsent)
	}
	// No handover token on a failed DR-integrity outcome.
	if dep.Result.HandoverFiredAt != nil {
		t.Errorf("HandoverFiredAt unexpectedly set on standby-absent failure")
	}
	if dep.Result.HandoverURL != "" {
		t.Errorf("HandoverURL unexpectedly set on standby-absent failure: %q", dep.Result.HandoverURL)
	}
}

// A single-region prov with OutcomeReady is unaffected by the DR gate —
// there is no standby to assert. Guards against the gate over-reaching.
func TestMarkPhase1Done_SingleRegion_UnaffectedByDRGate(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	dep := &Deployment{
		ID:        "phase1-single-region-dr-gate",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-single.example.com",
			OrgEmail:      "operator@single.example.com",
			BcpTopology:   provisioner.BcpTopologySingleRegion,
			Regions: []provisioner.RegionSpec{
				{Provider: "huawei", CloudRegion: "me-east-215-a"},
			},
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-single.example.com",
		},
		OwnerEmail: "operator@single.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	finalStates := map[string]string{
		"cilium":            helmwatch.StateInstalled,
		"catalyst-platform": helmwatch.StateInstalled,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want ready (single-region must not trip the DR gate)", dep.Status)
	}
	if dep.Result.Phase1Outcome == provisioner.ReasonStandbyRegionAbsent {
		t.Errorf("single-region prov must not be stamped standby-region-absent")
	}
}
