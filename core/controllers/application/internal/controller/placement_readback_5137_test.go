package controller

// placement_readback_5137_test.go — #5137 / UAT row 60: ONE Application
// object must never publish two contradictory facts about the same region.
//
// Measured live on hw292 2026-08-10, Application `uat-ahs-pg`
// (namespace hw292-omani-works, bp-postgres 0.2.16, phase Ready, 6d5h old):
//
//	status.regions[1] = {name: me-east-215-b, role: standby, replicas: 0, ready: 0}
//	status.targets[1] = {region: me-east-215-b, role: Standby, standbyType: Hot, ready: true}
//	status.placementRecon = "Reconciled"   (placementReason: "")
//	status.perCluster      = [{cluster: rtz-A, hr: uat-ahs-pg-rtz-a, role: singleton, status: Ready}]
//
// One materialised HelmRelease, TWO declared regions. Region B had no
// namespace at all on the region-B apiserver, no HelmRelease, no CNPG
// replica and no Continuum — yet the per-app Topology tab, which renders
// status.targets and status.placementRecon, painted a live armed Hot
// standby over it.
//
// The identity half of a target (region / role / standbyType) is DECLARED
// intent, resolved from spec.placement.targets[] or derived from the
// placement plan. `ready` is the only OBSERVED field on the row — and
// readbackByRegion was fanning ONE aggregate phase across every DECLARED
// plan region, so a region that no HelmRelease GET ever touched inherited
// the readiness another region earned. That is intent wearing an
// observation's name.
//
// These tests pin the invariant: the number of regions credited Ready can
// never exceed the number of deliveries actually observed Ready.

import (
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/internal/placement"
	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// hw292Row60Plan is the live active-hot-standby plan behind UAT row 60:
// primary me-east-215-a + Hot standby me-east-215-b.
func hw292Row60Plan() placement.Plan {
	return placement.Plan{
		PrimaryRegion: "me-east-215-a",
		Regions: []placement.RegionPlan{
			{Name: "me-east-215-a", Role: placement.RolePrimary, Standby: false},
			{Name: "me-east-215-b", Role: placement.RoleStandby, Standby: true},
		},
	}
}

// TestReadbackByRegion_UndeliveredStandbyIsNeverReady reproduces the exact
// hw292 contradiction: the plan declares TWO regions, the fan-out
// materialised ONE HelmRelease, that HelmRelease is Ready — so the rolled-up
// Application phase is Ready. The undelivered standby region must NOT
// inherit it.
//
// Pre-fix this whole test fails: readbackByRegion stamped ready:true on
// every plan region, so targets[1].ready was true, the recon value was
// "Reconciled" and the reason was empty.
func TestReadbackByRegion_UndeliveredStandbyIsNeverReady(t *testing.T) {
	plan := hw292Row60Plan()

	// ONE delivery observed Ready (uat-ahs-pg-rtz-a); the plan declares two
	// regions. This is the live hw292 shape.
	const observedReadyDeliveries = 1

	readback := readbackByRegion(plan, PhaseReady, observedReadyDeliveries)

	if !readback["me-east-215-a"].ready {
		t.Errorf("primary me-east-215-a has a Ready delivery and must stay ready; got %+v",
			readback["me-east-215-a"])
	}
	if readback["me-east-215-b"].ready {
		t.Errorf("me-east-215-b has NO delivery (no namespace, no HelmRelease on the live cluster) "+
			"and must never report ready; got %+v", readback["me-east-215-b"])
	}
	if readback["me-east-215-b"].degraded {
		t.Errorf("an unobserved region is Reconciling, not Degraded; got %+v", readback["me-east-215-b"])
	}

	// The full surface the Topology tab reads: status.targets[] + the ONE
	// recon value + its reason.
	targets := placementTargetsFromPlan(plan)
	obs := observedTargetsFromPlan(plan, targets, readback, false)
	status, reason, rows := reconStatusBlock(obs)

	if len(rows) != 2 {
		t.Fatalf("status.targets rows = %d, want 2", len(rows))
	}
	primaryRow := rows[0].(map[string]interface{})
	standbyRow := rows[1].(map[string]interface{})

	// The identity half stays DECLARED — the fix must not erase the
	// standby's Hot type, only stop claiming it is up.
	if standbyRow["standbyType"] != string(bpv1alpha1.StandbyHot) {
		t.Errorf("standby row must keep its declared standbyType Hot; got %+v", standbyRow)
	}
	if primaryRow["ready"] != true {
		t.Errorf("primary row must report ready:true (its delivery IS Ready); got %+v", primaryRow)
	}
	if standbyRow["ready"] != false {
		t.Errorf("status.targets for me-east-215-b reports ready:%v while status.regions for the SAME "+
			"region reports replicas:0 ready:0 — one object, two contradictory facts (#5137). Row: %+v",
			standbyRow["ready"], standbyRow)
	}

	if status != string(bpv1alpha1.ReconStatusReconciling) {
		t.Errorf("placementRecon = %q, want %q: a placement with an undelivered region is not reconciled",
			status, bpv1alpha1.ReconStatusReconciling)
	}
	if !strings.Contains(reason, "me-east-215-b") {
		t.Errorf("placementReason = %q, want it to NAME the region that has not converged", reason)
	}
}

// TestReadbackByRegion_FullyDeliveredPairStaysReady is the positive control
// that keeps the test above from passing for the trivial reason. A REAL
// 2-region pair — two deliveries, both Ready — must still report both
// regions ready and the placement Reconciled. Without this control, a fix
// that simply never reports ready would look green.
func TestReadbackByRegion_FullyDeliveredPairStaysReady(t *testing.T) {
	plan := hw292Row60Plan()

	readback := readbackByRegion(plan, PhaseReady, 2)
	if !readback["me-east-215-a"].ready || !readback["me-east-215-b"].ready {
		t.Fatalf("a genuinely delivered 2-region pair must report both regions ready; got %+v", readback)
	}

	obs := observedTargetsFromPlan(plan, placementTargetsFromPlan(plan), readback, true)
	status, reason, rows := reconStatusBlock(obs)
	if status != string(bpv1alpha1.ReconStatusReconciled) {
		t.Errorf("placementRecon = %q (reason %q), want Reconciled for a fully delivered pair", status, reason)
	}
	// §13 arming still rides on the Hot standby when a Continuum exists.
	if rows[1].(map[string]interface{})["armed"] != true {
		t.Errorf("a delivered Hot standby with a DR contract must stay armed; got %+v", rows[1])
	}
}

// TestReadbackByRegion_NeverCreditsMoreRegionsThanDeliveries pins the
// invariant itself across the shapes the fan-out can produce, so a future
// edit cannot re-introduce the any-region-implies-all-regions fallacy.
func TestReadbackByRegion_NeverCreditsMoreRegionsThanDeliveries(t *testing.T) {
	threeRegion := placement.Plan{
		PrimaryRegion: "me-east-215-a",
		Regions: []placement.RegionPlan{
			{Name: "me-east-215-a", Role: placement.RolePrimary},
			{Name: "me-east-215-b", Role: placement.RoleStandby, Standby: true},
			{Name: "me-east-215-c", Role: placement.RoleStandby, Standby: true},
		},
	}

	cases := []struct {
		name             string
		plan             placement.Plan
		phase            string
		readyDeliveries  int
		wantReadyRegions int
	}{
		{"hw292 row60: 1 delivery, 2 declared regions", hw292Row60Plan(), PhaseReady, 1, 1},
		{"zero deliveries observed Ready", hw292Row60Plan(), PhaseReady, 0, 0},
		{"3 declared, 1 delivered", threeRegion, PhaseReady, 1, 1},
		{"3 declared, 2 delivered", threeRegion, PhaseReady, 2, 2},
		{"3 declared, 3 delivered", threeRegion, PhaseReady, 3, 3},
		// A cap wider than the plan can never manufacture extra regions.
		{"cap exceeds plan", hw292Row60Plan(), PhaseReady, 9, 2},
		// Non-Ready phases are unchanged by the cap.
		{"provisioning credits nothing", hw292Row60Plan(), PhaseProvisioning, 2, 0},
		{"degraded credits nothing", hw292Row60Plan(), PhaseDegraded, 2, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readback := readbackByRegion(tc.plan, tc.phase, tc.readyDeliveries)
			got := 0
			for _, rb := range readback {
				if rb.ready {
					got++
				}
			}
			if got != tc.wantReadyRegions {
				t.Errorf("regions credited ready = %d, want %d (deliveries observed Ready = %d, "+
					"declared regions = %d)", got, tc.wantReadyRegions, tc.readyDeliveries, len(tc.plan.Regions))
			}
			if got > tc.readyDeliveries && tc.phase == PhaseReady {
				t.Errorf("credited %d regions from %d deliveries — readiness must never be fabricated",
					got, tc.readyDeliveries)
			}
		})
	}
}
