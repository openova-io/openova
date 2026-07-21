// Tests for #5311 — on a TRUE 2-region Sovereign the region-a
// controller cannot list the region-b replica Cluster CR (separate
// control plane), so FindPair returns "pair incomplete" every tick and
// the #4901 CR-based standby observation is structurally blind (the CR
// reads false-green through a real standby outage). observeCNPGStandby
// falls back to the acting primary's pg_stat_replication (r.StandbyProbe)
// on that topology, which surfaces the standby present/absent + lag
// regardless of the region-b control plane.
//
// Semantics under test:
//   - streaming standby present → Healthy + real lag surfaced.
//   - zero connected standbys past grace → Degraded / StandbyUnreachable.
//   - probe error → NO determination (never false-degrade), even over a
//     prior available sighting.
//   - single-API topology (both halves local) → CR-based observation is
//     used, the probe is NOT consulted (no kom4dc regression).
//
// Anti-theater: reverting the 2-region probe branch (so the topology
// falls back to resolveFailed/no-determination) makes the present/absent
// assertions below fail — the probe wiring is load-bearing.

package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/pgprobe"
)

// fakeProber records its calls and returns a canned Posture/error.
type fakeProber struct {
	calls       int
	posture     pgprobe.Posture
	err         error
	lastNS      string
	lastPrimary string
	lastApp     string
}

func (f *fakeProber) Observe(_ context.Context, ns, primaryName, expectedApp string) (pgprobe.Posture, error) {
	f.calls++
	f.lastNS, f.lastPrimary, f.lastApp = ns, primaryName, expectedApp
	return f.posture, f.err
}

// primaryOnlyCluster builds ONLY the primary half of a cnpg-pair (the
// TRUE 2-region shape: the replica Cluster CR lives in region-b's
// separate control plane and is absent from this API).
func primaryOnlyCluster(ns, pair string) *unstructured.Unstructured {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	primary.SetNamespace(ns)
	primary.SetName(pair + "-primary")
	primary.SetLabels(map[string]string{
		cnpg.PairLabel:     pair,
		cnpg.PairRoleLabel: cnpg.RolePrimary,
	})
	return primary
}

func lagSecondsOf(t *testing.T, r *ContinuumReconciler, ns, name string) int {
	t.Helper()
	got, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR: %v", err)
	}
	v, _, _ := unstructured.NestedInt64(got.Object, "status", "replicationLagSeconds")
	return int(v)
}

// TestObserveCNPGStandby_TwoRegion_StreamingStandby_StaysHealthy — the
// region-a controller can't see the region-b replica CR, but the
// primary's pg_stat_replication reports a streaming standby with lag.
// The CR must stay Healthy, mark standbyAvailable=true, and surface the
// probe's replay lag.
func TestObserveCNPGStandby_TwoRegion_StreamingStandby_StaysHealthy(t *testing.T) {
	t.Parallel()
	const ns, pair, primary = "cnpg", "demo", "hw-a"
	cr := newTestContinuumCR(ns, "dr", primary, []string{"hw-b"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr, primaryOnlyCluster(ns, pair))
	r.HoldingRegion = primary
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	registerGoroutine(r, nn)

	fp := &fakeProber{posture: pgprobe.Posture{
		StandbyPresent: true, Streaming: true, SyncStandbyPresent: true,
		ReplayLagSeconds: 4, AppName: "demo-replica",
	}}
	r.StandbyProbe = fp

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	primaryStatus, replicaStatus, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)
	_ = primaryStatus

	// The probe MUST have been consulted with the derived pair coordinates.
	if fp.calls != 1 {
		t.Fatalf("StandbyProbe.Observe calls = %d, want 1 (2-region path must probe the primary)", fp.calls)
	}
	if fp.lastNS != ns || fp.lastPrimary != "demo-primary" || fp.lastApp != "demo-replica" {
		t.Errorf("probe called with ns=%q primary=%q app=%q, want cnpg/demo-primary/demo-replica", fp.lastNS, fp.lastPrimary, fp.lastApp)
	}
	if !standby.Known || !standby.Available {
		t.Fatalf("streaming standby must read Known+Available, got %+v", standby)
	}
	if replicaStatus.LagSeconds != 4 {
		t.Errorf("probe replay lag not surfaced: replicaStatus.LagSeconds = %d, want 4", replicaStatus.LagSeconds)
	}

	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), primaryStatus, replicaStatus, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	phase, condStatus, condReason, avail, absent := statusFields(t, r, ns, "dr")
	if phase != PhaseHealthy {
		t.Errorf("phase = %q, want Healthy", phase)
	}
	if condStatus["StandbyAvailable"] != "True" || condReason["StandbyAvailable"] != "StandbyReachable" {
		t.Errorf("StandbyAvailable = %q/%q, want True/StandbyReachable", condStatus["StandbyAvailable"], condReason["StandbyAvailable"])
	}
	if v, ok := avail.(bool); !ok || !v {
		t.Errorf("status.standbyAvailable = %v, want true", avail)
	}
	if v, ok := absent.(bool); !ok || v {
		t.Errorf("status.hotStandbyAbsent = %v, want false", absent)
	}
	if got := lagSecondsOf(t, r, ns, "dr"); got != 4 {
		t.Errorf("status.replicationLagSeconds = %d, want 4 (from the primary's pg_stat_replication)", got)
	}
}

// TestObserveCNPGStandby_TwoRegion_ZeroStandbys_DegradesAfterGrace — the
// region-b standby is genuinely gone (region-kill): it drops out of the
// primary's pg_stat_replication (zero rows). Within grace: no
// determination. Past grace: Degraded / StandbyUnreachable — the exact
// #4901 surface that was false-green on 2-region before this fix.
func TestObserveCNPGStandby_TwoRegion_ZeroStandbys_DegradesAfterGrace(t *testing.T) {
	t.Parallel()
	const ns, pair, primary = "cnpg", "demo", "hw-a"
	cr := newTestContinuumCR(ns, "dr", primary, []string{"hw-b"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr, primaryOnlyCluster(ns, pair))
	r.HoldingRegion = primary
	clock := &fakeClock{t: time.Now()}
	r.Now = clock.Now
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	registerGoroutine(r, nn)

	fp := &fakeProber{posture: pgprobe.Posture{StandbyPresent: false}} // zero connected standbys
	r.StandbyProbe = fp
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}

	// Tick 1 — within grace: no determination (prior condition preserved).
	if _, _, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec); standby.Known {
		t.Fatalf("zero-standbys within grace must make no determination, got %+v", standby)
	}

	// Past grace — degrade.
	clock.Advance(time.Duration(spec.StandbyGraceSeconds)*time.Second + time.Second)
	primaryStatus, replicaStatus, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)
	if !standby.Known || standby.Available {
		t.Fatalf("zero-standbys past grace must determine Known/unavailable, got %+v", standby)
	}
	if standby.Gone {
		t.Errorf("probe cannot prove the replica CR was deleted — reason must be StandbyUnreachable, not Gone")
	}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), primaryStatus, replicaStatus, standby, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	phase, condStatus, condReason, avail, absent := statusFields(t, r, ns, "dr")
	if phase != PhaseDegraded {
		t.Errorf("phase = %q, want Degraded (a genuinely-gone 2-region standby must degrade, not read false-green)", phase)
	}
	if condStatus["StandbyAvailable"] != "False" || condReason["StandbyAvailable"] != "StandbyUnreachable" {
		t.Errorf("StandbyAvailable = %q/%q, want False/StandbyUnreachable", condStatus["StandbyAvailable"], condReason["StandbyAvailable"])
	}
	if v, ok := avail.(bool); !ok || v {
		t.Errorf("status.standbyAvailable = %v, want false", avail)
	}
	if v, ok := absent.(bool); !ok || !v {
		t.Errorf("status.hotStandbyAbsent = %v, want true", absent)
	}
	// Lease tracking stays correct — the degradation is standby-driven.
	if condStatus["LeaseHeld"] != "True" {
		t.Errorf("LeaseHeld = %q, want True", condStatus["LeaseHeld"])
	}
}

// TestObserveCNPGStandby_TwoRegion_ProbeError_NoDetermination — a probe
// error (credential missing / connection refused / query blip) must be
// treated as no-determination, NEVER as standby-absent, so a transient
// DB hiccup does not false-degrade a healthy pair — even over a prior
// available sighting.
func TestObserveCNPGStandby_TwoRegion_ProbeError_NoDetermination(t *testing.T) {
	t.Parallel()
	const ns, pair, primary = "cnpg", "demo", "hw-a"
	cr := newTestContinuumCR(ns, "dr", primary, []string{"hw-b"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr, primaryOnlyCluster(ns, pair))
	r.HoldingRegion = primary
	clock := &fakeClock{t: time.Now()}
	r.Now = clock.Now
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	registerGoroutine(r, nn)
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}

	// Seed a prior available sighting so the memory holds a last-seen replica.
	seed := &fakeProber{posture: pgprobe.Posture{StandbyPresent: true, Streaming: true, AppName: "demo-replica"}}
	r.StandbyProbe = seed
	if _, _, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec); !standby.Known || !standby.Available {
		t.Fatalf("seed available tick must pass through, got %+v", standby)
	}

	// Now the probe errors on every subsequent tick, well past grace.
	fp := &fakeProber{err: errors.New("connect cnpg: connection refused")}
	r.StandbyProbe = fp
	for i := 0; i < 5; i++ {
		_, _, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)
		if standby.Known {
			t.Fatalf("probe error must make NO determination (tick %d), got %+v", i, standby)
		}
		clock.Advance(time.Duration(spec.StandbyGraceSeconds)*time.Second + time.Second)
	}
	if fp.calls == 0 {
		t.Fatal("probe was never consulted on the 2-region path (wiring reverted?)")
	}
}

// TestObserveCNPGStandby_SingleAPI_UsesCRObservation — when BOTH pair
// halves are visible in the local API (single-API server / kom4dc 2-VPC
// mimic) the CR-based #4901 observation is authoritative and the probe
// is NOT consulted. Guards against a regression of the topology #4901's
// tests already cover.
func TestObserveCNPGStandby_SingleAPI_UsesCRObservation(t *testing.T) {
	t.Parallel()
	const ns, pair, primary = "cnpg", "demo", "hw-a"
	cr := newTestContinuumCR(ns, "dr", primary, []string{"hw-b"}, "k8s-lease")
	// Zero grace so the not-Ready replica degrades on the first
	// observation — the flap-grace window is exercised by the #4901
	// tests; here we only assert the CR observation is the SOURCE.
	_ = unstructured.SetNestedField(cr.Object, int64(0), "spec", "standbyGraceSeconds")
	objs := append([]runtime.Object{cr}, newTestClusterPair(ns, pair, 0)...) // replica NOT Ready
	r, _, _ := newReconciler(t, objs...)
	r.HoldingRegion = primary
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	registerGoroutine(r, nn)

	fp := &fakeProber{posture: pgprobe.Posture{StandbyPresent: true}} // would falsely say "present"
	r.StandbyProbe = fp
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}

	_, _, standby := r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)
	if fp.calls != 0 {
		t.Errorf("probe consulted on a single-API topology (calls=%d) — CR observation must win, no kom4dc regression", fp.calls)
	}
	// The replica CR has no Ready condition → CR observation reads absent.
	if !standby.Known || standby.Available {
		t.Fatalf("single-API CR observation must read the not-Ready replica as absent, got %+v", standby)
	}
	if standby.ReplicaCluster != "demo-replica" {
		t.Errorf("ReplicaCluster = %q, want demo-replica (from the local CR)", standby.ReplicaCluster)
	}

	// Flip the replica Ready + readyInstances=1 → CR observation flips available.
	_, replica, err := r.cnpgReader().FindPair(context.Background(), ns, pair)
	if err != nil {
		t.Fatalf("FindPair: %v", err)
	}
	_ = unstructured.SetNestedSlice(replica.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	_ = unstructured.SetNestedField(replica.Object, int64(1), "status", "readyInstances")
	if _, err := r.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Update(context.Background(), replica, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update replica: %v", err)
	}
	_, _, standby = r.observeCNPGStandby(context.Background(), r.cnpgReader(), nn, cr, spec)
	if !standby.Known || !standby.Available {
		t.Fatalf("Ready replica must read available via the CR observation, got %+v", standby)
	}
	if fp.calls != 0 {
		t.Errorf("probe still must not be consulted on single-API topology (calls=%d)", fp.calls)
	}
}
