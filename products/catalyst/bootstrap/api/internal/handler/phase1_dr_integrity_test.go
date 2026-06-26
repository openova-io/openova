// phase1_dr_integrity_test.go — #3375 DoD-7 wiring coverage, updated for
// the #4486 self-firing DR backbone (Refs #3375 #4212). Proves the
// DR-integrity gate in markPhase1Done:
//
//   - when the PRIMARY region converged (OutcomeReady) but the declared
//     standby is absent, keeps the deployment `ready`, FIRES the handover
//     (so the post-handover spine producer chain runs for the degraded
//     single-region primary), and records the standby absence as the
//     non-fatal SecondaryDegraded + standby-region-absent Phase1Outcome —
//     NOT a `failed` latch that would freeze the whole post-handover chain
//     off forever (the original architectural fault #4486 fixes);
//   - when the PRIMARY did NOT converge (a hard-failed component →
//     finalStatus already `failed`), stays `failed` and fires NO handover
//     (genuine-failure path preserved);
//   - leaves single-region provs untouched (no standby to assert).

package handler

import (
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/helmwatch"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// #4486 — active-hot-standby requested, 2 regions declared, primary
// converged (OutcomeReady) but the Phase-1 watch observed ONLY the primary
// (no secondary control-plane ever PUT its kubeconfig). The deployment must
// stay `ready`, FIRE the handover (HandoverFiredAt set), and flag the
// standby absence as SecondaryDegraded + standby-region-absent — so the
// converged primary's post-handover spine producer chain runs instead of
// being latched permanently inert.
func TestMarkPhase1Done_ActiveHotStandby_AbsentStandby_PrimaryReady_FiresHandover(t *testing.T) {
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

	// #4486: a converged primary stays ready (degraded single-region spine).
	if dep.Status != "ready" {
		t.Fatalf("Status = %q, want ready (converged primary must hand over even with an absent standby, #4486)", dep.Status)
	}
	// The standby absence is recorded non-fatally.
	if !dep.Result.SecondaryDegraded {
		t.Errorf("SecondaryDegraded = false, want true (absent standby must surface as degraded)")
	}
	if dep.Result.Phase1Outcome != provisioner.ReasonStandbyRegionAbsent {
		t.Errorf("Phase1Outcome = %q, want %q", dep.Result.Phase1Outcome, provisioner.ReasonStandbyRegionAbsent)
	}
	// Must NOT latch a hard Error onto a ready deployment (the wizard's
	// FailureCard would render it as a failure).
	if dep.Error != "" {
		t.Errorf("Error unexpectedly set on a ready (degraded) deployment: %q", dep.Error)
	}
	// The whole point of #4486: the handover MUST fire so the post-handover
	// producer chain (spine apps, adoption, policy-flip) is reachable.
	if dep.Result.HandoverFiredAt == nil {
		t.Errorf("HandoverFiredAt was NOT set — the post-handover chain is latched inert (the #4486 fault)")
	}
	if dep.Result.HandoverURL == "" {
		t.Errorf("HandoverURL was NOT set on the degraded-but-ready primary")
	}
}

// #4486 genuine-failure path: active-hot-standby requested, standby absent,
// AND the primary itself hard-FAILED a component (so finalStatus is already
// `failed` before the DR gate). The deployment must stay `failed` and fire
// NO handover — nothing converged to hand over. Guards against the self-fire
// fix over-reaching into the real-failure case.
func TestMarkPhase1Done_ActiveHotStandby_AbsentStandby_PrimaryFailed_StaysFailed(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetHandoverSigner(loadTestSigner(t))

	dep := &Deployment{
		ID:        "phase1-ahs-primary-failed",
		Status:    "phase1-watching",
		StartedAt: time.Now(),
		eventsCh:  make(chan provisioner.Event, 256),
		done:      make(chan struct{}),
		Request: provisioner.Request{
			SovereignFQDN: "otech-ahs-fail.example.com",
			OrgEmail:      "operator@ahs.example.com",
			BcpTopology:   provisioner.BcpTopologyActiveHotStandby,
			Regions: []provisioner.RegionSpec{
				{Provider: "huawei", CloudRegion: "me-east-215-a"},
				{Provider: "huawei", CloudRegion: "me-east-215-b"},
			},
		},
		Result: &provisioner.Result{
			SovereignFQDN: "otech-ahs-fail.example.com",
		},
		OwnerEmail: "operator@ahs.example.com",
	}
	h.deployments.Store(dep.ID, dep)

	// A hard-FAILED primary component → markPhase1Done's `failed > 0` branch
	// stamps Status=failed BEFORE the DR gate. The DR gate (which only
	// re-evaluates a `ready` dep) never runs; the deployment is a genuine
	// failure, not a degraded-but-functional spine.
	finalStates := map[string]string{
		"cilium":            helmwatch.StateInstalled,
		"catalyst-platform": helmwatch.StateFailed,
	}
	h.markPhase1Done(dep, finalStates, helmwatch.OutcomeReady)

	dep.mu.Lock()
	defer dep.mu.Unlock()

	if dep.Status != "failed" {
		t.Fatalf("Status = %q, want failed (a hard-failed primary is a genuine failure)", dep.Status)
	}
	// No handover on a genuine failure — nothing converged.
	if dep.Result.HandoverFiredAt != nil {
		t.Errorf("HandoverFiredAt unexpectedly set on a genuinely failed primary")
	}
	if dep.Result.HandoverURL != "" {
		t.Errorf("HandoverURL unexpectedly set on a genuinely failed primary: %q", dep.Result.HandoverURL)
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
