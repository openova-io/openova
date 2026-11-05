// Tests for the FAILED-OVER status truth (hw307, 2026-09-13 → 2026-11-06).
//
// hw307 ran failed over for 52 days: region B's `<instance>-replica`
// Clusters were the writable primaries (replica.enabled=false,
// pg_is_in_recovery()=f) and region A's named `<instance>` Clusters were
// streaming followers re-cloned by the #6149 dr-failback actor
// (replica.enabled=true, source=<instance>-replica). Every Continuum CR
// on region A still reported phase=Healthy, primaryRegion=A,
// StandbyAvailable=True/StandbyReachable — because the 2-region branch of
// observeCNPGStandby probes the LOCAL `-rw` (the demoted half's
// designated primary, which streams to its own two in-cluster standbys)
// and never reads the local CR's spec.replica.enabled.
//
// Semantics under test:
//   - 2-region, local primary half demoted → the probe is NOT consulted;
//     phase=FailedOver, status.primaryRegion = the failover target,
//     StandbyAvailable=False / PrimaryDemoted, hotStandbyAbsent=true,
//     Ready=True (the service is up, in the other region), no grace.
//   - 2-region, local primary half NOT demoted → unchanged: the probe is
//     consulted, phase=Healthy, primaryRegion = spec.primaryRegion
//     (control — the fix must not touch the healthy path).
//   - single-API, roles INVERTED (primary half replica.enabled=true, the
//     replica half promoted) → the same FailedOver truth, even though the
//     promoted replica half reads Ready (which the old code reported as
//     "standby available").
//   - a lag breach on a failed-over pair must NOT run the sequencer
//     (guarded in the reconcile loop; asserted on the status branch here).
//
// Anti-theater: reverting the IsReplicaCluster read in observeCNPGStandby
// makes the first and third tests fail on phase/primaryRegion/reason; the
// control test pins that the healthy path is byte-for-byte the old one.

package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pgprobe"
)

// demotedPrimaryOnlyCluster — the bp-postgres region-A half AFTER the
// dr-failback rejoin: a Cluster carrying the pair-role PRIMARY label whose
// spec says it is a replica cluster streaming from the promoted peer.
// Named `<instance>` with no `-primary` suffix, exactly as bp-postgres
// renders it (cluster.yaml), so ReplicaNameForPrimary yields "" and the
// replica name must come from spec.replica.source.
func demotedPrimaryOnlyCluster(ns, pair string) *unstructured.Unstructured {
	c := &unstructured.Unstructured{}
	c.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	c.SetNamespace(ns)
	c.SetName(pair)
	c.SetLabels(map[string]string{
		cnpg.PairLabel:     pair,
		cnpg.PairRoleLabel: cnpg.RolePrimary,
	})
	_ = unstructured.SetNestedField(c.Object, true, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(c.Object, pair+"-replica", "spec", "replica", "source")
	_ = unstructured.SetNestedField(c.Object, "Cluster in healthy state", "status", "phase")
	_ = unstructured.SetNestedField(c.Object, int64(3), "status", "readyInstances")
	return c
}

func primaryRegionOf(t *testing.T, r *ContinuumReconciler, ns, name string) string {
	t.Helper()
	got, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR: %v", err)
	}
	v, _, _ := unstructured.NestedString(got.Object, "status", "primaryRegion")
	return v
}

// TestObserveCNPGStandby_TwoRegion_LocalPrimaryDemoted_ReportsFailedOver —
// the hw307 shape. The local probe would have reported a "standby present"
// (the demoted half's own in-cluster standbys); it must not be consulted,
// and the CR must tell the truth: FailedOver, writer = region B.
func TestObserveCNPGStandby_TwoRegion_LocalPrimaryDemoted_ReportsFailedOver(t *testing.T) {
	t.Parallel()
	const ns, pair, primary, standbyRegion = "shared-data", "shared-pg", "hw-a", "hw-b"
	cr := newTestContinuumCR(ns, "dr-shared-pg", primary, []string{standbyRegion}, "k8s-lease")
	_ = unstructured.SetNestedField(cr.Object, pair, "spec", "cnpgPair", "name")
	r, _, _ := newReconciler(t, cr, demotedPrimaryOnlyCluster(ns, pair))
	r.HoldingRegion = primary
	nn := types.NamespacedName{Namespace: ns, Name: "dr-shared-pg"}
	registerGoroutine(r, nn)

	// The false-green the old code produced: rows from region A's own
	// standbys. If the probe is consulted at all, the fix is not in place.
	fp := &fakeProber{posture: pgprobe.Posture{
		StandbyPresent: true, Streaming: true, ReplayLagSeconds: 0, AppName: "shared-pg-3",
	}}
	r.StandbyProbe = fp

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	primaryStatus, replicaStatus, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)

	if fp.calls != 0 {
		t.Fatalf("StandbyProbe.Observe calls = %d, want 0 — the local -rw of a DEMOTED half only sees its own in-cluster standbys (hw307 false-green)", fp.calls)
	}
	if !primaryStatus.IsReplicaCluster || primaryStatus.ReplicaSource != pair+"-replica" {
		t.Fatalf("primaryStatus must read the demoted spec (IsReplicaCluster=true, source=%s-replica), got %+v", pair, primaryStatus)
	}
	if !standby.Known || standby.Available || !standby.LocalHalfDemoted {
		t.Fatalf("observation must be Known + !Available + LocalHalfDemoted with NO grace, got %+v", standby)
	}
	if standby.ReplicaCluster != pair+"-replica" {
		t.Errorf("ReplicaCluster = %q, want %s-replica (from spec.replica.source — ReplicaNameForPrimary is empty for a bp-postgres name)", standby.ReplicaCluster, pair)
	}

	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), primaryStatus, replicaStatus, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	phase, condStatus, condReason, avail, absent := statusFields(t, r, ns, "dr-shared-pg")
	if phase != PhaseFailedOver {
		t.Errorf("phase = %q, want FailedOver (the pair's writer is the standby region)", phase)
	}
	if got := primaryRegionOf(t, r, ns, "dr-shared-pg"); got != standbyRegion {
		t.Errorf("status.primaryRegion = %q, want %q (the failover target serves writes, not spec.primaryRegion)", got, standbyRegion)
	}
	if condStatus["StandbyAvailable"] != "False" || condReason["StandbyAvailable"] != "PrimaryDemoted" {
		t.Errorf("StandbyAvailable = %q/%q, want False/PrimaryDemoted", condStatus["StandbyAvailable"], condReason["StandbyAvailable"])
	}
	if condStatus["Ready"] != "True" || condReason["Ready"] != "PrimaryDemoted" {
		t.Errorf("Ready = %q/%q, want True/PrimaryDemoted (the service is up, in the other region)", condStatus["Ready"], condReason["Ready"])
	}
	if condStatus["LeaseHeld"] != "True" {
		t.Errorf("LeaseHeld = %q, want True (the lease stays with the only controller)", condStatus["LeaseHeld"])
	}
	if v, ok := avail.(bool); !ok || v {
		t.Errorf("status.standbyAvailable = %v, want false", avail)
	}
	if v, ok := absent.(bool); !ok || !v {
		t.Errorf("status.hotStandbyAbsent = %v, want true", absent)
	}
}

// TestObserveCNPGStandby_TwoRegion_LocalPrimaryNotDemoted_Control — the
// healthy 2-region path is unchanged: probe consulted once, Healthy,
// primaryRegion = spec.primaryRegion, StandbyReachable.
func TestObserveCNPGStandby_TwoRegion_LocalPrimaryNotDemoted_Control(t *testing.T) {
	t.Parallel()
	const ns, pair, primary = "cnpg", "demo", "hw-a"
	cr := newTestContinuumCR(ns, "dr", primary, []string{"hw-b"}, "k8s-lease")
	healthy := primaryOnlyCluster(ns, pair)
	_ = unstructured.SetNestedField(healthy.Object, false, "spec", "replica", "enabled")
	r, _, _ := newReconciler(t, cr, healthy)
	r.HoldingRegion = primary
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	registerGoroutine(r, nn)
	fp := &fakeProber{posture: pgprobe.Posture{StandbyPresent: true, Streaming: true, AppName: "demo-replica"}}
	r.StandbyProbe = fp

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	primaryStatus, replicaStatus, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)
	if fp.calls != 1 {
		t.Fatalf("control: StandbyProbe.Observe calls = %d, want 1", fp.calls)
	}
	if standby.LocalHalfDemoted || !standby.Available {
		t.Fatalf("control: a non-demoted primary must not read LocalHalfDemoted, got %+v", standby)
	}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), primaryStatus, replicaStatus, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	phase, condStatus, condReason, _, _ := statusFields(t, r, ns, "dr")
	if phase != PhaseHealthy {
		t.Errorf("control: phase = %q, want Healthy", phase)
	}
	if got := primaryRegionOf(t, r, ns, "dr"); got != primary {
		t.Errorf("control: status.primaryRegion = %q, want %q", got, primary)
	}
	if condStatus["StandbyAvailable"] != "True" || condReason["StandbyAvailable"] != "StandbyReachable" {
		t.Errorf("control: StandbyAvailable = %q/%q, want True/StandbyReachable", condStatus["StandbyAvailable"], condReason["StandbyAvailable"])
	}
}

// TestObserveCNPGStandby_SingleAPI_RolesInverted_ReportsFailedOver — both
// halves visible in one API (the kom4dc / test-fixture shape) with the
// roles inverted: the named primary is a replica cluster, the named
// replica is promoted and Ready. The old code read the promoted replica's
// readiness as "standby available" and reported Healthy with the declared
// primary; it must report FailedOver with the promoted region.
func TestObserveCNPGStandby_SingleAPI_RolesInverted_ReportsFailedOver(t *testing.T) {
	t.Parallel()
	const ns, pair, primary, standbyRegion = "cnpg", "demo", "hw-a", "hw-b"
	cr := newTestContinuumCR(ns, "dr", primary, []string{standbyRegion}, "k8s-lease")
	objs := newTestClusterPair(ns, pair, 0)
	prim := objs[0].(*unstructured.Unstructured)
	repl := objs[1].(*unstructured.Unstructured)
	// Invert: primary half follows the replica half, replica half promoted + Ready.
	_ = unstructured.SetNestedField(prim.Object, true, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(prim.Object, pair+"-replica", "spec", "replica", "source")
	_ = unstructured.SetNestedField(repl.Object, false, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(repl.Object, "Cluster in healthy state", "status", "phase")
	_ = unstructured.SetNestedField(repl.Object, int64(3), "status", "readyInstances")
	_ = unstructured.SetNestedField(repl.Object, []interface{}{map[string]interface{}{"type": "Ready", "status": "True"}}, "status", "conditions")
	r, _, _ := newReconciler(t, append([]runtime.Object{cr}, prim, repl)...)
	r.HoldingRegion = primary
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	registerGoroutine(r, nn)
	fp := &fakeProber{posture: pgprobe.Posture{StandbyPresent: true, Streaming: true}}
	r.StandbyProbe = fp

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	primaryStatus, replicaStatus, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)
	if fp.calls != 0 {
		t.Fatalf("single-API path must not consult the probe, calls = %d", fp.calls)
	}
	if !standby.Known || standby.Available || !standby.LocalHalfDemoted || standby.ReplicaCluster != pair+"-replica" {
		t.Fatalf("inverted roles must read Known + !Available + LocalHalfDemoted + replica=%s-replica, got %+v", pair, standby)
	}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), primaryStatus, replicaStatus, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	phase, condStatus, condReason, _, absent := statusFields(t, r, ns, "dr")
	if phase != PhaseFailedOver {
		t.Errorf("phase = %q, want FailedOver", phase)
	}
	if got := primaryRegionOf(t, r, ns, "dr"); got != standbyRegion {
		t.Errorf("status.primaryRegion = %q, want %q", got, standbyRegion)
	}
	if condStatus["StandbyAvailable"] != "False" || condReason["StandbyAvailable"] != "PrimaryDemoted" {
		t.Errorf("StandbyAvailable = %q/%q, want False/PrimaryDemoted", condStatus["StandbyAvailable"], condReason["StandbyAvailable"])
	}
	if v, ok := absent.(bool); !ok || !v {
		t.Errorf("status.hotStandbyAbsent = %v, want true", absent)
	}
}

// TestResolveStandbyPosture_LocalHalfDemoted_NoGrace — a structural read
// of the CR's spec is not a flapping probe: it passes through on the
// first tick even with a long grace window, and resets the flap clock.
func TestResolveStandbyPosture_LocalHalfDemoted_NoGrace(t *testing.T) {
	t.Parallel()
	const ns, primary = "cnpg", "hw-a"
	cr := newTestContinuumCR(ns, "dr", primary, []string{"hw-b"}, "k8s-lease")
	_ = unstructured.SetNestedField(cr.Object, int64(3600), "spec", "standbyGraceSeconds")
	r, _, _ := newReconciler(t, cr)
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	g := registerGoroutine(r, nn)
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	raw := cnpg.StandbyObservation{Known: true, Available: false, LocalHalfDemoted: true, ReplicaCluster: "demo-replica"}
	got := r.resolveStandbyPosture(nn, cr, spec, raw, false)
	if got != raw {
		t.Fatalf("LocalHalfDemoted must bypass the flap grace (%ds) and pass through unchanged, got %+v", spec.StandbyGraceSeconds, got)
	}
	if g.standbyLastSeenReplica != "demo-replica" || !g.standbyUnavailableSince.IsZero() {
		t.Errorf("memory not updated: lastSeen=%q unavailableSince=%v", g.standbyLastSeenReplica, g.standbyUnavailableSince)
	}
}
