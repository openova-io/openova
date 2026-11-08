package controller

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// TestD7_NeverObservedStandby_IsUnknownNotHealthy — ADR-0015 D7 item 13.
// acmewalk307/bp-wordpress-tenant read Healthy for 58 days while its
// replica never came up: a lease held + no standby observation was enough.
func TestD7_NeverObservedStandby_IsUnknownNotHealthy(t *testing.T) {
	t.Parallel()
	const primary = "hw-me-east-215-a-rtz-prod"
	cr := newTestContinuumCR("cnpg", "dr", primary, []string{"hw-me-east-215-b-rtz-prod"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	r.HoldingRegion = ""
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.CNPGPair == "" {
		t.Fatalf("fixture must guard a cnpg pair")
	}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, heldLease(primary), cnpg.Status{}, cnpg.Status{}, cnpg.StandbyObservation{}, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("cnpg").Get(context.Background(), "dr", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != PhaseUnknown {
		t.Fatalf("phase = %q, want %q", phase, PhaseUnknown)
	}
	ready := d7CondByType(t, got, "Ready")
	if ready["status"] != "Unknown" || ready["reason"] != "StandbyNotObserved" {
		t.Fatalf("Ready = %v, want Unknown/StandbyNotObserved", ready)
	}
}

// TestD7_LastTransitionTime_OnlyOnStatusChange — two passes with the same
// verdict keep the first lastTransitionTime; a status change restamps it.
func TestD7_LastTransitionTime_OnlyOnStatusChange(t *testing.T) {
	t.Parallel()
	const primary = "hw-me-east-215-a-rtz-prod"
	cr := newTestContinuumCR("cnpg", "dr", primary, []string{"hw-me-east-215-b-rtz-prod"}, "k8s-lease")
	r, _, _ := newReconciler(t, cr)
	r.HoldingRegion = ""
	clock := time.Date(2026, 11, 8, 6, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return clock }
	spec, _ := parseSpec(cr)
	ok := cnpg.StandbyObservation{Known: true, Available: true, ReplicaCluster: "demo-replica"}
	pass := func(obs cnpg.StandbyObservation) map[string]interface{} {
		t.Helper()
		lease := heldLease(primary)
		lease.ExpiresAt = clock.Add(time.Hour)
		if err := r.patchStatusFromCR(context.Background(), cr, spec, lease, cnpg.Status{}, cnpg.Status{}, obs, false, ""); err != nil {
			t.Fatalf("patchStatusFromCR: %v", err)
		}
		got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("cnpg").Get(context.Background(), "dr", metav1.GetOptions{})
		return d7CondByType(t, got, "Ready")
	}
	first := pass(ok)
	clock = clock.Add(10 * time.Minute)
	second := pass(ok)
	if first["lastTransitionTime"] != second["lastTransitionTime"] {
		t.Fatalf("Ready lastTransitionTime moved without a status change: %v -> %v", first["lastTransitionTime"], second["lastTransitionTime"])
	}
	clock = clock.Add(10 * time.Minute)
	third := pass(cnpg.StandbyObservation{Known: true, Available: false, ReplicaCluster: "demo-replica"})
	if third["status"] == second["status"] || third["lastTransitionTime"] == second["lastTransitionTime"] {
		t.Fatalf("status change must restamp lastTransitionTime: %v -> %v", second, third)
	}
}

func d7CondByType(t *testing.T, obj *unstructured.Unstructured, typ string) map[string]interface{} {
	t.Helper()
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		if cm, ok := c.(map[string]interface{}); ok && cm["type"] == typ {
			return cm
		}
	}
	t.Fatalf("condition %q absent", typ)
	return nil
}

var _ = metav1.GetOptions{}
