// Tests for #4901 — the cnpg-pair Continuum CR must surface a
// standby-absent condition instead of staying phase=Healthy /
// replicationLagSeconds=0 while its REQUIRED synchronous hot-standby
// is unreachable (the G12 region-kill drill on hw232 proved the
// controller was blind to this: lease/primary tracking was correct
// but a lost standby also reads lag=0, so RPO=0 durability risk was
// invisible on the CR the controller owns).
//
// Semantics under test (mirrors the catalyst-api projection #4905 so
// the stored + observed statuses agree):
//   - replica half not Ready, or Ready with readyInstances==0
//     → phase Degraded, standbyAvailable=false, hotStandbyAbsent=true,
//     condition StandbyAvailable=False/StandbyUnreachable.
//   - replica Ready (+ readyInstances>=1 when present)
//     → phase stays Healthy, condition True/StandbyReachable.
//   - Ready-but-LAGGING standby is NOT flagged — lag rides its own field.
//   - no determination (lease-lost patch) preserves the prior condition.

package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// heldLease returns a witness lease held by the given region, unexpired.
func heldLease(holder string) witness.State {
	return witness.State{Holder: holder, ExpiresAt: time.Now().Add(30 * time.Second)}
}

// statusFields reads back phase + conditions + the #4901 fields.
func statusFields(t *testing.T, r *ContinuumReconciler, ns, name string) (phase string, condStatus, condReason map[string]string, standbyAvailable, hotStandbyAbsent interface{}) {
	t.Helper()
	got, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR: %v", err)
	}
	phase, _, _ = unstructured.NestedString(got.Object, "status", "phase")
	condStatus = map[string]string{}
	condReason = map[string]string{}
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	for _, c := range conds {
		cm, _ := c.(map[string]interface{})
		typ, _ := cm["type"].(string)
		st, _ := cm["status"].(string)
		rs, _ := cm["reason"].(string)
		condStatus[typ] = st
		condReason[typ] = rs
	}
	standbyAvailable, _, _ = unstructured.NestedFieldNoCopy(got.Object, "status", "standbyAvailable")
	hotStandbyAbsent, _, _ = unstructured.NestedFieldNoCopy(got.Object, "status", "hotStandbyAbsent")
	return phase, condStatus, condReason, standbyAvailable, hotStandbyAbsent
}

// TestPatchStatusFromCR_StandbyAbsent_SurfacesDegraded — the proven
// region-kill posture: lease held correctly by the primary, lag=0
// (nothing following), replica half NOT Ready. The CR must go
// Degraded with the explicit StandbyAvailable=False condition.
func TestPatchStatusFromCR_StandbyAbsent_SurfacesDegraded(t *testing.T) {
	t.Parallel()
	const primary = "hw-me-east-215-a-rtz-prod"
	cr := newTestContinuumCR("cnpg", "dr", primary, []string{"hw-me-east-215-b-rtz-prod"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	r.HoldingRegion = ""

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	standby := cnpg.StandbyObservation{Known: true, Available: false, ReplicaCluster: "demo-replica"}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), cnpg.Status{}, cnpg.Status{}, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}

	phase, condStatus, condReason, avail, absent := statusFields(t, r, "cnpg", "dr")
	if phase != PhaseDegraded {
		t.Errorf("phase = %q, want Degraded (standby absent must degrade even with the lease held)", phase)
	}
	if condStatus["StandbyAvailable"] != "False" {
		t.Errorf("StandbyAvailable condition = %q, want False", condStatus["StandbyAvailable"])
	}
	if condReason["StandbyAvailable"] != "StandbyUnreachable" {
		t.Errorf("StandbyAvailable reason = %q, want StandbyUnreachable", condReason["StandbyAvailable"])
	}
	if v, ok := avail.(bool); !ok || v {
		t.Errorf("status.standbyAvailable = %v, want false", avail)
	}
	if v, ok := absent.(bool); !ok || !v {
		t.Errorf("status.hotStandbyAbsent = %v, want true", absent)
	}
	// Lease tracking stays correct — the degradation is standby-driven.
	if condStatus["LeaseHeld"] != "True" {
		t.Errorf("LeaseHeld = %q, want True (lease tracking must be preserved verbatim)", condStatus["LeaseHeld"])
	}
	if condStatus["Ready"] != "False" {
		t.Errorf("Ready = %q, want False when Degraded", condStatus["Ready"])
	}
}

// TestPatchStatusFromCR_StandbyPresent_StaysHealthy — a reachable
// standby keeps phase Healthy and writes the True condition.
func TestPatchStatusFromCR_StandbyPresent_StaysHealthy(t *testing.T) {
	t.Parallel()
	const primary = "hw-me-east-215-a-rtz-prod"
	cr := newTestContinuumCR("cnpg", "dr", primary, []string{"hw-me-east-215-b-rtz-prod"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	r.HoldingRegion = ""

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	standby := cnpg.StandbyObservation{Known: true, Available: true, ReplicaCluster: "demo-replica"}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), cnpg.Status{}, cnpg.Status{}, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}

	phase, condStatus, condReason, avail, absent := statusFields(t, r, "cnpg", "dr")
	if phase != PhaseHealthy {
		t.Errorf("phase = %q, want Healthy", phase)
	}
	if condStatus["StandbyAvailable"] != "True" {
		t.Errorf("StandbyAvailable condition = %q, want True", condStatus["StandbyAvailable"])
	}
	if condReason["StandbyAvailable"] != "StandbyReachable" {
		t.Errorf("StandbyAvailable reason = %q, want StandbyReachable", condReason["StandbyAvailable"])
	}
	if v, ok := avail.(bool); !ok || !v {
		t.Errorf("status.standbyAvailable = %v, want true", avail)
	}
	if v, ok := absent.(bool); !ok || v {
		t.Errorf("status.hotStandbyAbsent = %v, want false", absent)
	}
}

// TestPatchStatusFromCR_NoDetermination_LeavesStandbyUntouched — a
// pass with no determination (dr-spine/raft continuums, or replica
// unresolvable) must not write the fields nor the condition, and a
// later no-determination pass must PRESERVE a previously-written one.
func TestPatchStatusFromCR_NoDetermination_LeavesStandbyUntouched(t *testing.T) {
	t.Parallel()
	const primary = "hw-me-east-215-a-rtz-prod"
	cr := newTestContinuumCR("cnpg", "dr", primary, []string{"hw-me-east-215-b-rtz-prod"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	r.HoldingRegion = ""

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	// Pass 1: no determination at all — nothing standby-related lands.
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), cnpg.Status{}, cnpg.Status{}, cnpg.StandbyObservation{}, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	phase, condStatus, _, avail, absent := statusFields(t, r, "cnpg", "dr")
	if phase != PhaseHealthy {
		t.Errorf("phase = %q, want Healthy", phase)
	}
	if _, present := condStatus["StandbyAvailable"]; present {
		t.Errorf("StandbyAvailable condition written with no determination — must be absent")
	}
	if avail != nil || absent != nil {
		t.Errorf("standbyAvailable/hotStandbyAbsent written with no determination: %v / %v", avail, absent)
	}

	// Pass 2: standby-absent determination lands the condition.
	standby := cnpg.StandbyObservation{Known: true, Available: false, ReplicaCluster: "demo-replica"}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), cnpg.Status{}, cnpg.Status{}, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	// Pass 3: lease-lost-style no-determination patch must preserve it.
	if err := r.patchStatusFromCR(context.Background(), cr, spec, witness.State{}, cnpg.Status{}, cnpg.Status{}, cnpg.StandbyObservation{}, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	_, condStatus, condReason, _, _ := statusFields(t, r, "cnpg", "dr")
	if condStatus["StandbyAvailable"] != "False" || condReason["StandbyAvailable"] != "StandbyUnreachable" {
		t.Errorf("prior StandbyAvailable condition not preserved across a no-determination pass: status=%q reason=%q",
			condStatus["StandbyAvailable"], condReason["StandbyAvailable"])
	}
}

// TestStandbyAvailable_Semantics — the pure availability rule: Ready
// gates first; readyInstances==0 (present) is absent; an absent
// readyInstances field falls back to Ready alone; LAG NEVER flags.
func TestStandbyAvailable_Semantics(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		st   cnpg.Status
		want bool
	}{
		{"not ready", cnpg.Status{Ready: false}, false},
		{"ready, no readyInstances field", cnpg.Status{Ready: true}, true},
		{"ready, readyInstances=0 (region-kill)", cnpg.Status{Ready: true, HasReadyInstances: true, ReadyInstances: 0}, false},
		{"ready, readyInstances=1", cnpg.Status{Ready: true, HasReadyInstances: true, ReadyInstances: 1}, true},
		{"ready but LAGGING — lag rides its own field, never flags", cnpg.Status{Ready: true, HasReadyInstances: true, ReadyInstances: 1, LagSeconds: 900}, true},
		{"not ready and lagging", cnpg.Status{Ready: false, LagSeconds: 900}, false},
	}
	for _, tc := range cases {
		if got := cnpg.StandbyAvailable(tc.st); got != tc.want {
			t.Errorf("%s: StandbyAvailable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRunPerCRPath_StandbyObservation_FromClusterPair exercises the
// same wiring runPerCR uses: FindPair + Get → StandbyObservation. The
// replica half present-but-not-Ready is exactly the proven #4901
// region-kill state and must produce Known=true / Available=false.
func TestRunPerCRPath_StandbyObservation_FromClusterPair(t *testing.T) {
	t.Parallel()
	const ns, pair = "cnpg", "demo"
	objs := newTestClusterPair(ns, pair, 0) // replica has NO Ready condition → not Ready
	r, _, _ := newReconciler(t, objs...)

	reader := r.cnpgReader()
	primary, replica, err := reader.FindPair(context.Background(), ns, pair)
	if err != nil {
		t.Fatalf("FindPair: %v", err)
	}
	if primary == nil || replica == nil {
		t.Fatal("pair halves missing")
	}
	replicaStatus, _, err := reader.Get(context.Background(), replica.GetNamespace(), replica.GetName())
	if err != nil {
		t.Fatalf("Get replica: %v", err)
	}
	if cnpg.StandbyAvailable(replicaStatus) {
		t.Error("replica without Ready condition must read standby-absent")
	}

	// Flip the replica Ready + readyInstances=1 → available.
	_ = unstructured.SetNestedSlice(replica.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	_ = unstructured.SetNestedField(replica.Object, int64(1), "status", "readyInstances")
	if _, err := r.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Update(context.Background(), replica, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update replica: %v", err)
	}
	replicaStatus, _, err = reader.Get(context.Background(), replica.GetNamespace(), replica.GetName())
	if err != nil {
		t.Fatalf("re-Get replica: %v", err)
	}
	if !cnpg.StandbyAvailable(replicaStatus) {
		t.Error("Ready replica with readyInstances=1 must read standby-available")
	}
}
