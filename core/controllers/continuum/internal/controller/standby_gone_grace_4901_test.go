// Tests for the #4901 follow-up — resolveStandbyPosture closes the
// residual false-green the first #4901 fix (#5094) left open:
//
//   - GONE standby: replica Cluster CR deleted outright → FindPair
//     fails → the raw observation read "no determination", so the
//     prior StandbyAvailable=True condition was preserved FOREVER.
//     A previously-seen replica that vanishes must degrade with
//     reason StandbyAbsent (after the grace window).
//   - Flap-grace: a single unavailable observation must NOT flip the
//     CR Degraded; only continuous unavailability past
//     spec.standbyGraceSeconds does. Recovery surfaces immediately.
//   - Never-seen pair (provisioning in flight) still makes no
//     determination — no false alarms.
//   - Restart-seed: memory seeds from status.standbyReplicaCluster so
//     a controller restart mid-outage neither forgets the vanished
//     replica nor flips a degraded CR back green.

package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// registerGoroutine registers an (idle) per-CR goroutine entry so
// resolveStandbyPosture has cross-tick memory to work with, exactly as
// runPerCR does. Returns the entry for direct memory assertions.
func registerGoroutine(r *ContinuumReconciler, nn types.NamespacedName) *continuumGoroutine {
	g := &continuumGoroutine{cancel: func() {}}
	r.activeContinuumsMu.Lock()
	r.activeContinuums[nn.String()] = g
	r.activeContinuumsMu.Unlock()
	return g
}

// fakeClock is a controllable r.Now for grace-window tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) Now() time.Time          { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func standbySpec(graceSeconds int) ContinuumSpec {
	return ContinuumSpec{StandbyGraceSeconds: graceSeconds}
}

// TestResolveStandbyPosture_GoneAfterGrace — the residual #4901
// false-green: replica seen once, then its Cluster CR vanishes
// (resolveFailed). Within grace: no determination (prior condition
// preserved). Past grace: Known/unavailable/GONE determination.
func TestResolveStandbyPosture_GoneAfterGrace(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("cnpg", "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	clock := &fakeClock{t: time.Now()}
	r.Now = clock.Now
	nn := types.NamespacedName{Namespace: "cnpg", Name: "dr"}
	registerGoroutine(r, nn)
	spec := standbySpec(30)

	// Tick 1: replica resolved + available — passes through, seeds memory.
	avail := cnpg.StandbyObservation{Known: true, Available: true, ReplicaCluster: "demo-replica"}
	if got := r.resolveStandbyPosture(nn, cr, spec, avail, false); !got.Known || !got.Available {
		t.Fatalf("available observation must pass through, got %+v", got)
	}

	// Tick 2: replica Cluster CR deleted → resolveFailed. Within grace
	// → no determination.
	clock.Advance(10 * time.Second)
	if got := r.resolveStandbyPosture(nn, cr, spec, cnpg.StandbyObservation{}, true); got.Known {
		t.Fatalf("gone-within-grace must make no determination, got %+v", got)
	}

	// Tick 3: still gone, grace elapsed → GONE determination carrying
	// the remembered replica name.
	clock.Advance(30 * time.Second)
	got := r.resolveStandbyPosture(nn, cr, spec, cnpg.StandbyObservation{}, true)
	if !got.Known || got.Available || !got.Gone {
		t.Fatalf("gone-past-grace must determine Known/unavailable/Gone, got %+v", got)
	}
	if got.ReplicaCluster != "demo-replica" {
		t.Errorf("gone determination must carry the last-seen replica name, got %q", got.ReplicaCluster)
	}

	// The determination lands on the CR as Degraded + StandbyAbsent.
	parsed, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if err := r.patchStatusFromCR(context.Background(), cr, parsed, heldLease("hw-a"), cnpg.Status{}, cnpg.Status{}, got, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	r.HoldingRegion = "hw-a"
	phase, condStatus, condReason, availField, absentField := statusFields(t, r, "cnpg", "dr")
	if phase != PhaseDegraded {
		t.Errorf("phase = %q, want Degraded (gone standby must degrade)", phase)
	}
	if condStatus["StandbyAvailable"] != "False" {
		t.Errorf("StandbyAvailable condition = %q, want False", condStatus["StandbyAvailable"])
	}
	if condReason["StandbyAvailable"] != "StandbyAbsent" {
		t.Errorf("StandbyAvailable reason = %q, want StandbyAbsent (gone, not merely unreachable)", condReason["StandbyAvailable"])
	}
	if v, ok := availField.(bool); !ok || v {
		t.Errorf("status.standbyAvailable = %v, want false", availField)
	}
	if v, ok := absentField.(bool); !ok || !v {
		t.Errorf("status.hotStandbyAbsent = %v, want true", absentField)
	}
}

// TestResolveStandbyPosture_FlapGrace — a single unavailable
// observation (replica present but not Ready) makes no determination;
// recovery passes through immediately and resets the episode; only a
// CONTINUOUS unavailable episode past the grace window degrades.
func TestResolveStandbyPosture_FlapGrace(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("cnpg", "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	clock := &fakeClock{t: time.Now()}
	r.Now = clock.Now
	nn := types.NamespacedName{Namespace: "cnpg", Name: "dr"}
	g := registerGoroutine(r, nn)
	spec := standbySpec(30)

	unavail := cnpg.StandbyObservation{Known: true, Available: false, ReplicaCluster: "demo-replica"}
	avail := cnpg.StandbyObservation{Known: true, Available: true, ReplicaCluster: "demo-replica"}

	// Flap: one unavailable tick → no determination (prior condition
	// preserved — the CR does NOT flip Degraded).
	if got := r.resolveStandbyPosture(nn, cr, spec, unavail, false); got.Known {
		t.Fatalf("first unavailable tick within grace must make no determination, got %+v", got)
	}
	// Recovery 10s later → passes through immediately + resets episode.
	clock.Advance(10 * time.Second)
	if got := r.resolveStandbyPosture(nn, cr, spec, avail, false); !got.Known || !got.Available {
		t.Fatalf("recovery must surface immediately, got %+v", got)
	}
	if !g.standbyUnavailableSince.IsZero() {
		t.Errorf("recovery must reset the unavailable episode clock")
	}

	// Fresh episode: continuously unavailable past grace → degrades
	// with the PRESENT-but-unreachable posture (not Gone).
	clock.Advance(10 * time.Second)
	if got := r.resolveStandbyPosture(nn, cr, spec, unavail, false); got.Known {
		t.Fatalf("new episode within grace must make no determination, got %+v", got)
	}
	clock.Advance(31 * time.Second)
	got := r.resolveStandbyPosture(nn, cr, spec, unavail, false)
	if !got.Known || got.Available {
		t.Fatalf("unavailable past grace must determine Known/unavailable, got %+v", got)
	}
	if got.Gone {
		t.Errorf("present-but-not-Ready replica must NOT read Gone (reason stays StandbyUnreachable)")
	}
}

// TestResolveStandbyPosture_ZeroGrace_DegradesImmediately — explicit
// spec.standbyGraceSeconds=0 keeps the #5094 degrade-on-first-
// observation behavior.
func TestResolveStandbyPosture_ZeroGrace_DegradesImmediately(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("cnpg", "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	nn := types.NamespacedName{Namespace: "cnpg", Name: "dr"}
	registerGoroutine(r, nn)

	unavail := cnpg.StandbyObservation{Known: true, Available: false, ReplicaCluster: "demo-replica"}
	if got := r.resolveStandbyPosture(nn, cr, standbySpec(0), unavail, false); !got.Known || got.Available {
		t.Fatalf("zero grace must pass the unavailable observation through immediately, got %+v", got)
	}
}

// TestResolveStandbyPosture_NeverSeen_NoFalseAlarm — a pair that never
// resolved (provisioning in flight) keeps making no determination even
// when resolution fails past any window: absence of history is not
// evidence of loss.
func TestResolveStandbyPosture_NeverSeen_NoFalseAlarm(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("cnpg", "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	clock := &fakeClock{t: time.Now()}
	r.Now = clock.Now
	nn := types.NamespacedName{Namespace: "cnpg", Name: "dr"}
	registerGoroutine(r, nn)
	spec := standbySpec(30)

	for i := 0; i < 10; i++ {
		if got := r.resolveStandbyPosture(nn, cr, spec, cnpg.StandbyObservation{}, true); got.Known {
			t.Fatalf("never-seen pair must never produce a determination, got %+v on tick %d", got, i)
		}
		clock.Advance(10 * time.Second)
	}
}

// TestResolveStandbyPosture_RestartSeedsFromStatus — after a
// controller restart the fresh goroutine has no memory, but the CR's
// stored status carries standbyReplicaCluster + standbyAvailable=false
// from the previous incarnation: the vanished replica must be
// recognized as previously-seen AND the grace treated as already
// served — a restart never flips a degraded CR back green.
func TestResolveStandbyPosture_RestartSeedsFromStatus(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("cnpg", "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	_ = unstructured.SetNestedField(cr.Object, "demo-replica", "status", "standbyReplicaCluster")
	_ = unstructured.SetNestedField(cr.Object, false, "status", "standbyAvailable")
	r, _, _ := newReconciler(t, cr)
	nn := types.NamespacedName{Namespace: "cnpg", Name: "dr"}
	registerGoroutine(r, nn)

	got := r.resolveStandbyPosture(nn, cr, standbySpec(30), cnpg.StandbyObservation{}, true)
	if !got.Known || got.Available || !got.Gone {
		t.Fatalf("restart over a degraded CR with a vanished replica must immediately re-determine Gone, got %+v", got)
	}
	if got.ReplicaCluster != "demo-replica" {
		t.Errorf("seeded determination must carry the stored replica name, got %q", got.ReplicaCluster)
	}
}

// TestResolveStandbyPosture_RestartSeed_GreenStatusGetsFullGrace — the
// counterpart seed case: the stored status says the standby WAS
// available; after the restart a resolve failure starts a FRESH grace
// window (no immediate degrade off stale knowledge).
func TestResolveStandbyPosture_RestartSeed_GreenStatusGetsFullGrace(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("cnpg", "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	_ = unstructured.SetNestedField(cr.Object, "demo-replica", "status", "standbyReplicaCluster")
	_ = unstructured.SetNestedField(cr.Object, true, "status", "standbyAvailable")
	r, _, _ := newReconciler(t, cr)
	clock := &fakeClock{t: time.Now()}
	r.Now = clock.Now
	nn := types.NamespacedName{Namespace: "cnpg", Name: "dr"}
	registerGoroutine(r, nn)
	spec := standbySpec(30)

	if got := r.resolveStandbyPosture(nn, cr, spec, cnpg.StandbyObservation{}, true); got.Known {
		t.Fatalf("first post-restart resolve failure over a green status must open a grace window, got %+v", got)
	}
	clock.Advance(31 * time.Second)
	got := r.resolveStandbyPosture(nn, cr, spec, cnpg.StandbyObservation{}, true)
	if !got.Known || !got.Gone {
		t.Fatalf("persisting resolve failure past grace must determine Gone, got %+v", got)
	}
}

// TestRunPerCRPath_ReplicaDeleted_FindPairFails_ThenGone exercises the
// same wiring runPerCR uses against a REAL deleted replica: FindPair
// on the fake dynamic client fails once the replica Cluster CR is
// deleted, and resolveStandbyPosture turns that into the Gone
// determination past grace.
func TestRunPerCRPath_ReplicaDeleted_FindPairFails_ThenGone(t *testing.T) {
	t.Parallel()
	const ns, pair = "cnpg", "demo"
	cr := newTestContinuumCR(ns, "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	objs := append([]runtime.Object{cr}, newTestClusterPair(ns, pair, 0)...)
	r, _, _ := newReconciler(t, objs...)
	clock := &fakeClock{t: time.Now()}
	r.Now = clock.Now
	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	registerGoroutine(r, nn)
	spec := standbySpec(30)
	reader := r.cnpgReader()

	// Make the replica available so the first tick seeds the memory.
	_, replica, err := reader.FindPair(context.Background(), ns, pair)
	if err != nil {
		t.Fatalf("FindPair: %v", err)
	}
	_ = unstructured.SetNestedSlice(replica.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	if _, err := r.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Update(context.Background(), replica, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update replica: %v", err)
	}
	replicaStatus, _, err := reader.Get(context.Background(), replica.GetNamespace(), replica.GetName())
	if err != nil {
		t.Fatalf("Get replica: %v", err)
	}
	raw := cnpg.StandbyObservation{Known: true, Available: cnpg.StandbyAvailable(replicaStatus), ReplicaCluster: replica.GetName()}
	if got := r.resolveStandbyPosture(nn, cr, spec, raw, false); !got.Known || !got.Available {
		t.Fatalf("seed tick must pass available through, got %+v", got)
	}

	// Delete the replica Cluster CR — the #4901 "standby cluster gone"
	// scenario. FindPair must now FAIL (pair incomplete).
	if err := r.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Delete(context.Background(), replica.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Fatalf("delete replica: %v", err)
	}
	if _, _, err := reader.FindPair(context.Background(), ns, pair); err == nil {
		t.Fatal("FindPair must fail once the replica Cluster CR is deleted")
	}

	// Same fold runPerCR performs on a FindPair failure.
	clock.Advance(10 * time.Second)
	if got := r.resolveStandbyPosture(nn, cr, spec, cnpg.StandbyObservation{}, true); got.Known {
		t.Fatalf("within grace: no determination, got %+v", got)
	}
	clock.Advance(31 * time.Second)
	got := r.resolveStandbyPosture(nn, cr, spec, cnpg.StandbyObservation{}, true)
	if !got.Known || got.Available || !got.Gone || got.ReplicaCluster != replica.GetName() {
		t.Fatalf("deleted replica past grace must determine Gone for %q, got %+v", replica.GetName(), got)
	}
}

// TestParseSpec_StandbyGraceSeconds — absent = default; explicit value
// wins; explicit 0 sticks (degrade-on-first-observation).
func TestParseSpec_StandbyGraceSeconds(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("cnpg", "dr", "hw-a", []string{"hw-b"}, "k8s-lease")
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.StandbyGraceSeconds != DefaultStandbyGraceSeconds {
		t.Errorf("absent spec.standbyGraceSeconds = %d, want default %d", spec.StandbyGraceSeconds, DefaultStandbyGraceSeconds)
	}

	_ = unstructured.SetNestedField(cr.Object, int64(90), "spec", "standbyGraceSeconds")
	spec, err = parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.StandbyGraceSeconds != 90 {
		t.Errorf("spec.standbyGraceSeconds=90 parsed as %d", spec.StandbyGraceSeconds)
	}

	_ = unstructured.SetNestedField(cr.Object, int64(0), "spec", "standbyGraceSeconds")
	spec, err = parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.StandbyGraceSeconds != 0 {
		t.Errorf("explicit spec.standbyGraceSeconds=0 parsed as %d, want 0", spec.StandbyGraceSeconds)
	}
}
