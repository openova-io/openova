// Tests for #5125 Defect-2 — step-8 re-clone-on-divergence wiring at
// the controller level: checkRejoin must patch the CR status +
// (for a bounded/failed episode) emit a continuum-error audit event,
// and must be a complete no-op on a healthy rejoin.

package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
)

func clusterGVKForTest() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
}

// newDivergedPair builds a cnpg-pair where the replica half is
// un-Ready and reports a TimelineID strictly ahead of the (Ready)
// primary — the #5125 Defect-2 divergence shape.
func newDivergedPair(ns, pair string, replicaTimeline, primaryTimeline int) (*unstructured.Unstructured, *unstructured.Unstructured) {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(clusterGVKForTest())
	primary.SetNamespace(ns)
	primary.SetName(pair + "-primary")
	primary.SetLabels(map[string]string{cnpg.PairLabel: pair, cnpg.PairRoleLabel: cnpg.RolePrimary})
	_ = unstructured.SetNestedField(primary.Object, int64(primaryTimeline), "status", "timelineID")
	_ = unstructured.SetNestedSlice(primary.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")

	replica := &unstructured.Unstructured{}
	replica.SetGroupVersionKind(clusterGVKForTest())
	replica.SetNamespace(ns)
	replica.SetName(pair + "-replica")
	replica.SetLabels(map[string]string{cnpg.PairLabel: pair, cnpg.PairRoleLabel: cnpg.RoleReplica})
	_ = unstructured.SetNestedField(replica.Object, true, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(replica.Object, int64(replicaTimeline), "status", "timelineID")
	// No Ready condition — un-Ready (crash-looping walreceiver).

	return primary, replica
}

// TestCheckRejoin_ControllerWiring_PatchesStatusAndAudits confirms the
// controller-level plumbing: on a diverged pair, checkRejoin patches
// status.rejoinRepair + the RejoinRepair condition, and (since a
// single low MaxAttempts bounds almost immediately) eventually emits a
// continuum-error audit once bounded.
func TestCheckRejoin_ControllerWiring_PatchesStatusAndAudits(t *testing.T) {
	t.Parallel()
	const ns, pair = "cnpg", "demo"
	cr := newTestContinuumCR(ns, "dr", "hw-a", []string{"hw-b"}, "in-memory")
	primary, replicaObj := newDivergedPair(ns, pair, 3, 2)
	r, rec, _ := newReconciler(t, cr, primary, replicaObj)
	r.RejoinOptions = switchover.RejoinOptions{MaxAttempts: 1, Cooldown: time.Minute}
	// Controllable clock so we can deterministically cross the (single
	// attempt's) backoff window between tick 1 and tick 2 without a
	// real sleep.
	clock := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return clock }

	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	key := nn.String()
	r.activeContinuumsMu.Lock()
	r.activeContinuums[key] = &continuumGoroutine{}
	r.activeContinuumsMu.Unlock()

	reader := r.cnpgReader()
	primaryStatus, _, err := reader.Get(context.Background(), ns, pair+"-primary")
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	replicaStatus, _, err := reader.Get(context.Background(), ns, pair+"-replica")
	if err != nil {
		t.Fatalf("Get replica: %v", err)
	}

	// Tick 1 — issues the (only, MaxAttempts=1) reclone attempt.
	r.checkRejoin(context.Background(), nn, reader, ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus)

	got, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(context.Background(), "dr", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR: %v", err)
	}
	target, _, _ := unstructured.NestedString(got.Object, "status", "rejoinRepair", "target")
	if target != pair+"-replica" {
		t.Errorf("status.rejoinRepair.target = %q, want %q", target, pair+"-replica")
	}
	attempts, _, _ := unstructured.NestedInt64(got.Object, "status", "rejoinRepair", "attempts")
	if attempts != 1 {
		t.Errorf("status.rejoinRepair.attempts = %d, want 1", attempts)
	}

	// Tick 2 — the reclone wiped the replica's status, so a plain
	// re-Get would show no divergence. Simulate the persistent-failure
	// case (reclone didn't fix it) by re-injecting the same divergence,
	// which with MaxAttempts=1 must now bound + fail loud.
	replicaAfter, err := r.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Get(context.Background(), pair+"-replica", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get replica post-reclone: %v", err)
	}
	_ = unstructured.SetNestedField(replicaAfter.Object, int64(3), "status", "timelineID")
	if _, err := r.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Update(context.Background(), replicaAfter, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update replica: %v", err)
	}
	replicaStatus, _, err = reader.Get(context.Background(), ns, pair+"-replica")
	if err != nil {
		t.Fatalf("re-Get replica: %v", err)
	}

	// Advance the clock past the (single attempt's) backoff window
	// before the second tick, so the MaxAttempts cap — not the cooldown
	// gate — is what fires this time.
	clock = clock.Add(2 * time.Minute)

	r.checkRejoin(context.Background(), nn, reader, ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus)

	got, err = r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(context.Background(), "dr", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR (2): %v", err)
	}
	bounded, _, _ := unstructured.NestedBool(got.Object, "status", "rejoinRepair", "bounded")
	if !bounded {
		t.Errorf("status.rejoinRepair.bounded = %v, want true after MaxAttempts=1 exhausted", bounded)
	}
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	foundBoundedCond := false
	for _, c := range conds {
		cm, _ := c.(map[string]interface{})
		if cm["type"] == "RejoinRepair" {
			if cm["status"] != "False" || cm["reason"] != "RejoinRepairBounded" {
				t.Errorf("RejoinRepair condition = %+v, want status=False reason=RejoinRepairBounded", cm)
			}
			foundBoundedCond = true
		}
	}
	if !foundBoundedCond {
		t.Errorf("expected a RejoinRepair condition on the CR, got conditions=%v", conds)
	}

	// A bounded episode must audit as continuum-error (operator must
	// act), not the routine continuum-rejoin-repair type.
	errEvents := rec.EventsByType(events.TypeError)
	if len(errEvents) == 0 {
		t.Errorf("expected at least one continuum-error audit event on bounded rejoin-repair")
	}
}

// TestCheckRejoin_ControllerWiring_HealthyRejoin_NoStatusChange confirms
// a healthy (Ready) standby never touches the CR status/conditions nor
// emits any audit event.
func TestCheckRejoin_ControllerWiring_HealthyRejoin_NoStatusChange(t *testing.T) {
	t.Parallel()
	const ns, pair = "cnpg", "demo"
	cr := newTestContinuumCR(ns, "dr", "hw-a", []string{"hw-b"}, "in-memory")
	primary, replicaObj := newDivergedPair(ns, pair, 3, 2)
	// Flip the replica to Ready — a healthy rejoin, despite the (now
	// irrelevant) timeline gap.
	_ = unstructured.SetNestedSlice(replicaObj.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	r, rec, _ := newReconciler(t, cr, primary, replicaObj)

	nn := types.NamespacedName{Namespace: ns, Name: "dr"}
	key := nn.String()
	r.activeContinuumsMu.Lock()
	r.activeContinuums[key] = &continuumGoroutine{}
	r.activeContinuumsMu.Unlock()

	reader := r.cnpgReader()
	primaryStatus, _, _ := reader.Get(context.Background(), ns, pair+"-primary")
	replicaStatus, _, _ := reader.Get(context.Background(), ns, pair+"-replica")

	before, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(context.Background(), "dr", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR: %v", err)
	}
	beforeRV := before.GetResourceVersion()

	r.checkRejoin(context.Background(), nn, reader, ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus)

	after, err := r.Dyn.Resource(ContinuumGVR).Namespace(ns).Get(context.Background(), "dr", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get CR after: %v", err)
	}
	if after.GetResourceVersion() != beforeRV {
		t.Errorf("CR mutated on a healthy rejoin — resourceVersion changed %q -> %q", beforeRV, after.GetResourceVersion())
	}
	if len(rec.EventsByType(events.TypeRejoinRepair)) != 0 || len(rec.EventsByType(events.TypeError)) != 0 {
		t.Errorf("expected NO rejoin-repair or error audit events on a healthy rejoin")
	}
}
