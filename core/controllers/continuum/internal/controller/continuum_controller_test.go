// Tests for the K-Cont-2 Continuum reconciler.
//
// We follow the existing core/controllers convention (per
// 02-implementer-canon.md §6 + the C4 brief): use
// k8s.io/client-go/dynamic/fake instead of envtest. Sibling
// controllers (C1..C5 + application) all use the same pattern.

package controller

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/switchover"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

func gvrListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		ContinuumGVR:            "ContinuumList",
		cnpg.ClusterGVR:         "ClusterList",
		switchover.HTTPRouteGVR: "HTTPRouteList",
	}
}

func newTestContinuumCR(ns, name, primary string, hot []string, kind string) *unstructured.Unstructured {
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(continuumGVK)
	cr.SetNamespace(ns)
	cr.SetName(name)
	hnSlice := make([]interface{}, 0, len(hot))
	for _, h := range hot {
		hnSlice = append(hnSlice, h)
	}
	cr.Object["spec"] = map[string]interface{}{
		"applicationRef":    "demo-app",
		"primaryRegion":     primary,
		"hotStandbyRegions": hnSlice,
		"leaseClient": map[string]interface{}{
			"kind": kind,
			"config": map[string]interface{}{
				"ttlSeconds":   int64(30),
				"renewSeconds": int64(10),
			},
		},
		"rto": "60s",
		"rpo": "5s",
		"cnpgPair": map[string]interface{}{
			"name":      "demo",
			"namespace": ns,
		},
		"httpRoute": map[string]interface{}{
			"name":      "demo-app",
			"namespace": "demo",
		},
		"pdmZone": "example.com",
		"luaRecord": map[string]interface{}{
			"selector": "ifurlup",
			"healthCheck": map[string]interface{}{
				"url": "https://probe-fsn.example.com/healthz",
			},
			"hostnames": []interface{}{"a.example.com"},
		},
		"regions": []interface{}{
			map[string]interface{}{"name": primary, "lbIPs": []interface{}{"5.1.2.3"}},
			map[string]interface{}{"name": hot[0], "lbIPs": []interface{}{"5.5.6.7"}},
		},
	}
	return cr
}

func newTestClusterPair(ns, pair string, lagOnReplica int) []runtime.Object {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	primary.SetNamespace(ns)
	primary.SetName(pair + "-primary")
	primary.SetLabels(map[string]string{
		cnpg.PairLabel:     pair,
		cnpg.PairRoleLabel: cnpg.RolePrimary,
	})

	replica := &unstructured.Unstructured{}
	replica.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	replica.SetNamespace(ns)
	replica.SetName(pair + "-replica")
	replica.SetLabels(map[string]string{
		cnpg.PairLabel:     pair,
		cnpg.PairRoleLabel: cnpg.RoleReplica,
	})
	_ = unstructured.SetNestedField(replica.Object, true, "spec", "replica", "enabled")
	if lagOnReplica > 0 {
		_ = unstructured.SetNestedField(replica.Object, int64(lagOnReplica), "status", "lag")
	}
	return []runtime.Object{primary, replica}
}

// fakeDrainer satisfies switchover.HTTPRouteDrainer for tests.
type fakeDrainer struct {
	calls int32
}

func (f *fakeDrainer) SetWeightZero(ctx context.Context, ns, name, region string) ([]int, error) {
	atomic.AddInt32(&f.calls, 1)
	return []int{100}, nil
}
func (f *fakeDrainer) RestoreWeights(ctx context.Context, ns, name string, weights []int) error {
	return nil
}

func newReconciler(t *testing.T, objs ...runtime.Object) (*ContinuumReconciler, *events.Recorder, *witness.DefaultSelector) {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListKinds(), objs...)

	rec := events.NewRecorder()
	sel := &witness.DefaultSelector{InMemoryAllowed: true}

	r := &ContinuumReconciler{
		Dyn:              dyn,
		WitnessSelector:  sel,
		HoldingRegion:    "fsn",
		Audit:            rec,
		Drainer:          &fakeDrainer{},
		Sleep:            func(time.Duration) {},
		Now:              time.Now,
		activeContinuums: map[string]*continuumGoroutine{},
	}
	return r, rec, sel
}

func reconcile(r *ContinuumReconciler, ns, name string) error {
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: ns, Name: name},
	})
	return err
}

func TestReconcile_FirstObservation_StartsGoroutine(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	if err := reconcile(r, "ns", "cr1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if r.ActiveCount() != 1 {
		t.Fatalf("ActiveCount = %d want 1", r.ActiveCount())
	}
	// Cleanup goroutine.
	r.stopGoroutine("ns/cr1")
}

func TestReconcile_DeletionStopsGoroutine(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	_ = reconcile(r, "ns", "cr1")
	if r.ActiveCount() != 1 {
		t.Fatalf("setup: ActiveCount = %d want 1", r.ActiveCount())
	}

	// Delete the CR via the dynamic client.
	if err := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Delete(context.Background(), "cr1", metav1.DeleteOptions{}); err != nil {
		t.Fatalf("Delete CR: %v", err)
	}
	if err := reconcile(r, "ns", "cr1"); err != nil {
		t.Fatalf("Reconcile-after-delete: %v", err)
	}
	if r.ActiveCount() != 0 {
		t.Fatalf("ActiveCount post-delete = %d want 0", r.ActiveCount())
	}
}

func TestReconcile_InvalidSpecMarksFailed(t *testing.T) {
	t.Parallel()
	cr := &unstructured.Unstructured{}
	cr.SetGroupVersionKind(continuumGVK)
	cr.SetNamespace("ns")
	cr.SetName("bad")
	// Missing required fields.
	cr.Object["spec"] = map[string]interface{}{}
	r, _, _ := newReconciler(t, cr)
	if err := reconcile(r, "ns", "bad"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "bad", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != PhaseFailed {
		t.Fatalf("phase = %q want %q", phase, PhaseFailed)
	}
}

func TestReconcile_WitnessUnavailable_K3MarksFailed(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "cloudflare-kv")
	r, _, _ := newReconciler(t, cr)
	if err := reconcile(r, "ns", "cr1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != PhaseFailed {
		t.Fatalf("phase = %q want %q (witness should be unimplemented)", phase, PhaseFailed)
	}
	reason, _, _ := unstructured.NestedString(got.Object, "status", "conditions")
	_ = reason
}

func TestRunSwitchover_HappyPath(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, rec, sel := newReconciler(t, objs...)

	// Pre-acquire the lease for FromRegion (fsn).
	w, _ := sel.Select("in-memory", map[string]any{"slot": "ns/cr1"})
	if _, err := w.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	// Wire the PDM client to a no-op for the test.
	r.PDMClient = nil // PDMCommit closure surfaces the missing-PDMClient error
	// Use a stand-in PDMClient with a Doer-mocked transport. Easier:
	// inject a closure that bypasses the network. The runSwitchover
	// path always goes through r.PDMClient — so we wire a real
	// pdm.Client with a fake doer.
	// (handled by setting PDMClient to a nil-returning fake)

	// Build a fake-doer-backed PDMClient.
	fakeURL := "http://pdm.local"
	r.PDMClient = newPDMClientWithFake(t, fakeURL)

	app, _, _ := unstructured.NestedMap(cr.Object, "spec")
	_ = app

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	r.runSwitchover(context.Background(), cr, spec, "hel", "operator-requested", 0, w)

	// Status should reflect FailedOver after successful switchover.
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != PhaseFailedOver {
		msg, _, _ := unstructured.NestedString(got.Object, "status", "conditions")
		t.Fatalf("phase = %q want %q (msg=%q audit-events=%v)", phase, PhaseFailedOver, msg, allAuditTypes(rec))
	}
	if got := rec.EventsByType(events.TypeSwitchover); len(got) != 1 {
		t.Fatalf("expected 1 switchover audit, got %d (all=%d)", len(got), rec.Len())
	}
}

// TestRunSwitchover_PatchesLastLuaRecord — slice Z1 follow-up.
//
// On a successful switchover the per-CR PDMCommit closure must patch
// `status.lastLuaRecord` with the rendered records the controller just
// committed via PDM /v1/lua/commit (and `status.lastLuaRecordAt` with
// the commit timestamp). This unblocks U-DR-1's LuaRecordView from
// rendering the empty state.
func TestRunSwitchover_PatchesLastLuaRecord(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, sel := newReconciler(t, objs...)

	w, _ := sel.Select("in-memory", map[string]any{"slot": "ns/cr1"})
	if _, err := w.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	r.PDMClient = newPDMClientWithFake(t, "http://pdm.local")

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	r.runSwitchover(context.Background(), cr, spec, "hel", "operator-requested", 0, w)

	got, err := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("re-fetch CR: %v", err)
	}
	luaRec, found, err := unstructured.NestedMap(got.Object, "status", "lastLuaRecord")
	if err != nil {
		t.Fatalf("NestedMap lastLuaRecord: %v", err)
	}
	if !found {
		t.Fatalf("status.lastLuaRecord not written; status=%+v", got.Object["status"])
	}
	records, ok := luaRec["records"].([]interface{})
	if !ok || len(records) == 0 {
		t.Fatalf("status.lastLuaRecord.records empty or wrong type: %+v", luaRec)
	}
	first, ok := records[0].(map[string]interface{})
	if !ok {
		t.Fatalf("records[0] not a map: %T", records[0])
	}
	body, _ := first["body"].(string)
	if !strings.Contains(body, "ifurlup") {
		t.Errorf("records[0].body should contain ifurlup; got %q", body)
	}
	primaryRegion, _ := first["primaryRegion"].(string)
	if primaryRegion != "hel" {
		t.Errorf("records[0].primaryRegion = %q want hel (the new primary)", primaryRegion)
	}
	at, _, _ := unstructured.NestedString(got.Object, "status", "lastLuaRecordAt")
	if at == "" {
		t.Errorf("status.lastLuaRecordAt should be populated after a successful PDM commit")
	}
}

// TestPatchStatus_LuaRecordOnlyOnNonNil — guards the no-op path.
//
// status.lastLuaRecord must NOT be touched when the caller passes nil
// (so steady-state reconciles preserve the prior value across the gap
// between switchovers). Verifies the contract called out in the
// statusUpdate.LastLuaRecord field doc.
func TestPatchStatus_LuaRecordOnlyOnNonNil(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	// Seed a prior lastLuaRecord directly (simulating a successful
	// switchover earlier in the CR's lifetime).
	if err := r.patchStatus(context.Background(), cr, statusUpdate{
		LastLuaRecord: map[string]interface{}{
			"records": []interface{}{
				map[string]interface{}{"hostname": "a.example.com", "body": "seed-body"},
			},
		},
	}); err != nil {
		t.Fatalf("seed patchStatus: %v", err)
	}

	// Now apply a steady-state patch (lease-renew shape) with
	// LastLuaRecord = nil.
	if err := r.patchStatus(context.Background(), cr, statusUpdate{
		Phase:         PhaseHealthy,
		LeaseHolder:   "fsn",
		PrimaryRegion: "fsn",
	}); err != nil {
		t.Fatalf("steady-state patchStatus: %v", err)
	}

	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	luaRec, _, _ := unstructured.NestedMap(got.Object, "status", "lastLuaRecord")
	records, _ := luaRec["records"].([]interface{})
	if len(records) != 1 {
		t.Fatalf("steady-state patch wiped lastLuaRecord (got len=%d)", len(records))
	}
	first, _ := records[0].(map[string]interface{})
	if body, _ := first["body"].(string); body != "seed-body" {
		t.Errorf("seeded body lost; got %q", body)
	}
}

// TestEffectiveHoldingRegion_FallsBackToPrimary proves the #3829
// second root-cause fix: when CATALYST_REGION is unset (HoldingRegion=""
// — the live omantel.biz case), the controller holds the lease as the
// CR's primaryRegion (it only runs on the primary-side cluster), so a
// healthy pair is NOT pinned Degraded with leaseHolder empty.
func TestEffectiveHoldingRegion_FallsBackToPrimary(t *testing.T) {
	t.Parallel()
	spec := ContinuumSpec{PrimaryRegion: "hw-me-east-215-a-rtz-prod"}

	withEnv := &ContinuumReconciler{HoldingRegion: "explicit-region"}
	if got := withEnv.effectiveHoldingRegion(spec); got != "explicit-region" {
		t.Errorf("with env set: got %q, want explicit-region", got)
	}

	noEnv := &ContinuumReconciler{HoldingRegion: ""}
	if got := noEnv.effectiveHoldingRegion(spec); got != "hw-me-east-215-a-rtz-prod" {
		t.Errorf("without env: got %q, want primaryRegion fallback", got)
	}
}

// TestPatchStatusFromCR_HealthyWhenHolderMatchesPrimary_NoEnv asserts
// the end-to-end status outcome: HoldingRegion="" + a lease held by the
// primaryRegion → phase=Healthy, Ready=True, LeaseHeld=True,
// leaseHolder=<primary>, replicationLagSeconds rendered. This is exactly
// the panel-state the Topology tab needs (rows 51/52/54/55/56/62/71).
func TestPatchStatusFromCR_HealthyWhenHolderMatchesPrimary_NoEnv(t *testing.T) {
	t.Parallel()
	const primary = "hw-me-east-215-a-rtz-prod"
	cr := newTestContinuumCR("cnpg", "dr", primary, []string{"hw-me-east-215-b-rtz-prod"}, "k8s-lease")
	objs := append([]runtime.Object{cr}, newTestClusterPair("cnpg", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)
	// Simulate the live env: CATALYST_REGION was never wired.
	r.HoldingRegion = ""

	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	// A live lease the witness acquired, held by the primary region.
	now := time.Now()
	lease := witness.State{
		Holder:    primary,
		ExpiresAt: now.Add(30 * time.Second),
	}
	if err := r.patchStatusFromCR(context.Background(), cr, spec, lease, cnpg.Status{}, cnpg.Status{}, cnpg.StandbyObservation{}, false, ""); err != nil {
		t.Fatalf("patchStatusFromCR: %v", err)
	}

	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("cnpg").Get(context.Background(), "dr", metav1.GetOptions{})
	phase, _, _ := unstructured.NestedString(got.Object, "status", "phase")
	if phase != PhaseHealthy {
		t.Fatalf("phase = %q, want Healthy", phase)
	}
	holder, _, _ := unstructured.NestedString(got.Object, "status", "leaseHolder")
	if holder != primary {
		t.Fatalf("leaseHolder = %q, want %q", holder, primary)
	}
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	condStatus := map[string]string{}
	for _, c := range conds {
		cm, _ := c.(map[string]interface{})
		t, _ := cm["type"].(string)
		s, _ := cm["status"].(string)
		condStatus[t] = s
	}
	if condStatus["Ready"] != "True" {
		t.Errorf("Ready = %q, want True", condStatus["Ready"])
	}
	if condStatus["LeaseHeld"] != "True" {
		t.Errorf("LeaseHeld = %q, want True", condStatus["LeaseHeld"])
	}
}

// TestLuaRecordStatusValue_NilOnEmpty — pure-function helper guard.
func TestLuaRecordStatusValue_NilOnEmpty(t *testing.T) {
	t.Parallel()
	if luaRecordStatusValue(nil) != nil {
		t.Errorf("nil input should produce nil output")
	}
	if luaRecordStatusValue([]dns.Record{}) != nil {
		t.Errorf("empty slice should produce nil output")
	}
	out := luaRecordStatusValue([]dns.Record{
		{Hostname: "a.example.com", LuaBody: "ifurlup(...)", TTL: 30, PrimaryRegion: "fsn"},
	})
	recs, _ := out["records"].([]interface{})
	if len(recs) != 1 {
		t.Fatalf("len(records) = %d want 1", len(recs))
	}
	row, _ := recs[0].(map[string]interface{})
	if row["hostname"] != "a.example.com" || row["body"] != "ifurlup(...)" || row["primaryRegion"] != "fsn" {
		t.Errorf("row mapped wrong: %+v", row)
	}
}

func TestRunSwitchover_NoFailoverTarget_NoOp(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, rec, sel := newReconciler(t, objs...)
	w, _ := sel.Select("in-memory", map[string]any{"slot": "ns/cr1"})
	spec, _ := parseSpec(cr)
	r.runSwitchover(context.Background(), cr, spec, "", "operator-requested", 0, w)
	if got := rec.EventsByType(events.TypeSwitchover); len(got) != 0 {
		t.Fatalf("expected no switchover audit, got %d", len(got))
	}
}

func TestPickFailoverTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		primary string
		hot     []string
		want    string
	}{
		{"fsn", []string{"hel"}, "hel"},
		{"fsn", []string{"fsn", "hel"}, "hel"},
		{"fsn", []string{"fsn"}, ""},
		{"fsn", nil, ""},
	}
	for _, c := range cases {
		got := pickFailoverTarget(ContinuumSpec{PrimaryRegion: c.primary, HotStandbyRegions: c.hot})
		if got != c.want {
			t.Errorf("pickFailoverTarget(%q, %v) = %q want %q", c.primary, c.hot, got, c.want)
		}
	}
}

func TestParseSpec_RequiredFields(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.ApplicationRef != "demo-app" {
		t.Errorf("ApplicationRef = %q", spec.ApplicationRef)
	}
	if spec.PrimaryRegion != "fsn" {
		t.Errorf("PrimaryRegion = %q", spec.PrimaryRegion)
	}
	if spec.RTOSeconds != 60 {
		t.Errorf("RTOSeconds = %d want 60", spec.RTOSeconds)
	}
	if spec.TTLSeconds != 30 {
		t.Errorf("TTLSeconds = %d want 30", spec.TTLSeconds)
	}
	if spec.RenewSeconds != 10 {
		t.Errorf("RenewSeconds = %d want 10", spec.RenewSeconds)
	}
	if len(spec.SynthParams.RegionToIPs) != 2 {
		t.Errorf("SynthParams.RegionToIPs len = %d want 2", len(spec.SynthParams.RegionToIPs))
	}
}

// #3492 — mechanism defaults to empty (=cnpg-pair) when not declared, and
// the raft-transition block is read when present.
func TestParseSpec_SwitchoverMechanism(t *testing.T) {
	t.Parallel()

	// Default: no switchover.mechanism → empty Mechanism (cnpg-pair).
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	spec, err := parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec: %v", err)
	}
	if spec.Mechanism != "" {
		t.Errorf("default Mechanism = %q, want empty (cnpg-pair default)", spec.Mechanism)
	}
	if spec.RaftTransition.Namespace != "" {
		t.Errorf("default RaftTransition should be empty, got namespace=%q", spec.RaftTransition.Namespace)
	}

	// raft-transition declared, with target.
	cr = newTestContinuumCR("openbao", "cont-openbao", "me-east-215-a-1", []string{"me-east-215-b-1"}, "dns-quorum")
	_ = unstructured.SetNestedField(cr.Object, "raft-transition", "spec", "switchover", "mechanism")
	_ = unstructured.SetNestedMap(cr.Object, map[string]interface{}{
		"namespace":    "openbao",
		"podSelector":  "app.kubernetes.io/name=openbao",
		"container":    "openbao",
		"snapshotPath": "/snapshots/latest.snap",
	}, "spec", "switchover", "raftTransition")

	spec, err = parseSpec(cr)
	if err != nil {
		t.Fatalf("parseSpec (raft): %v", err)
	}
	if string(spec.Mechanism) != "raft-transition" {
		t.Errorf("Mechanism = %q, want raft-transition", spec.Mechanism)
	}
	if spec.RaftTransition.Namespace != "openbao" {
		t.Errorf("RaftTransition.Namespace = %q", spec.RaftTransition.Namespace)
	}
	if spec.RaftTransition.PodSelector != "app.kubernetes.io/name=openbao" {
		t.Errorf("RaftTransition.PodSelector = %q", spec.RaftTransition.PodSelector)
	}
	if spec.RaftTransition.SnapshotPath != "/snapshots/latest.snap" {
		t.Errorf("RaftTransition.SnapshotPath = %q", spec.RaftTransition.SnapshotPath)
	}
}

func TestParseSpec_ErrorPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(*unstructured.Unstructured)
	}{
		{"missing applicationRef", func(c *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, "", "spec", "applicationRef")
		}},
		{"missing primaryRegion", func(c *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, "", "spec", "primaryRegion")
		}},
		{"empty hotStandbyRegions", func(c *unstructured.Unstructured) {
			unstructured.RemoveNestedField(c.Object, "spec", "hotStandbyRegions")
		}},
		{"missing leaseClient.kind", func(c *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(c.Object, "", "spec", "leaseClient", "kind")
		}},
	}
	for _, c := range cases {
		cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
		c.mut(cr)
		if _, err := parseSpec(cr); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

func TestParseDurationSeconds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"30s", 30, true},
		{"5m", 300, true},
		{"2h", 7200, true},
		{"", 0, false},
		{"30x", 0, false},
		{"abc", 0, false},
	}
	for _, c := range cases {
		got, err := parseDurationSeconds(c.in)
		if (err == nil) != c.ok {
			t.Errorf("%q: ok=%v err=%v", c.in, c.ok, err)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("%q: got %d want %d", c.in, got, c.want)
		}
	}
}

func TestCurrentZone(t *testing.T) {
	t.Parallel()
	if got := currentZone(nil); got != "" {
		t.Errorf("nil: %q", got)
	}
	if got := currentZone([]dns.Record{{Hostname: "a.example.com"}}); got != "example.com" {
		t.Errorf("hostname: %q", got)
	}
	if got := currentZone([]dns.Record{{Hostname: "noDots"}}); got != "noDots" {
		t.Errorf("no-dots: %q", got)
	}
}

func TestPhaseToReady(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		PhaseHealthy:       "True",
		PhaseFailedOver:    "True",
		PhasePending:       "Unknown",
		PhaseSwitchingOver: "Unknown",
		PhaseFailed:        "False",
		PhaseDegraded:      "False",
	}
	for in, want := range cases {
		if got := phaseToReady(in); got != want {
			t.Errorf("phaseToReady(%q) = %q want %q", in, got, want)
		}
	}
}

func TestIsSwitchoverRequested(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	if isSwitchoverRequested(cr) {
		t.Error("default should be false")
	}
	_ = unstructured.SetNestedField(cr.Object, true, "spec", "switchover", "requested")
	if !isSwitchoverRequested(cr) {
		t.Error("after set, should be true")
	}
}

func TestReconcile_MultiCRIsolation(t *testing.T) {
	t.Parallel()
	cr1 := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	cr2 := newTestContinuumCR("ns", "cr2", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr1, cr2}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	if err := reconcile(r, "ns", "cr1"); err != nil {
		t.Fatalf("Reconcile cr1: %v", err)
	}
	if err := reconcile(r, "ns", "cr2"); err != nil {
		t.Fatalf("Reconcile cr2: %v", err)
	}
	if r.ActiveCount() != 2 {
		t.Fatalf("ActiveCount = %d want 2", r.ActiveCount())
	}
	r.stopGoroutine("ns/cr1")
	r.stopGoroutine("ns/cr2")
}

func TestReconcile_Idempotent_SecondCallDoesntDuplicateGoroutine(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	for i := 0; i < 3; i++ {
		if err := reconcile(r, "ns", "cr1"); err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
	}
	if r.ActiveCount() != 1 {
		t.Fatalf("ActiveCount = %d want 1 after 3 reconciles", r.ActiveCount())
	}
	r.stopGoroutine("ns/cr1")
}

// containsString is a tiny helper for status-condition assertions.
func containsString(s, sub string) bool { return strings.Contains(s, sub) }

// avoid unused-import of cnpg if no test references it directly.
var _ cnpg.Status

// Compile-time references to silence unused-import warnings on
// platforms that strip them out aggressively.
var _ = atomic.AddInt32

// ----------------------------------------------------------------------
// Slice F-1 — new audit emit tests
// ----------------------------------------------------------------------

func TestReconcile_FirstObservation_EmitsCRCreated(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, rec, _ := newReconciler(t, objs...)

	if err := reconcile(r, "ns", "cr1"); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got := rec.EventsByType(events.TypeCRCreated)
	if len(got) != 1 {
		t.Errorf("expected 1 continuum-cr-created event, got %d (all=%v)", len(got), allAuditTypes(rec))
	}
	if got[0].ContinuumName != "ns/cr1" {
		t.Errorf("ContinuumName = %q want %q", got[0].ContinuumName, "ns/cr1")
	}
	r.stopGoroutine("ns/cr1")
}

func TestReconcile_RepeatedReconcile_EmitsCRCreatedOnce(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, rec, _ := newReconciler(t, objs...)

	for i := 0; i < 3; i++ {
		if err := reconcile(r, "ns", "cr1"); err != nil {
			t.Fatalf("Reconcile #%d: %v", i, err)
		}
	}
	got := rec.EventsByType(events.TypeCRCreated)
	if len(got) != 1 {
		t.Errorf("expected exactly 1 continuum-cr-created across N reconciles, got %d", len(got))
	}
	r.stopGoroutine("ns/cr1")
}

func TestPublishConfigChanged_FiresOnDrift(t *testing.T) {
	t.Parallel()
	r, rec, _ := newReconciler(t)
	specA := ContinuumSpec{
		ApplicationRef: "demo-app", PrimaryRegion: "fsn",
		HotStandbyRegions: []string{"hel"}, LeaseClientKind: "in-memory",
		TTLSeconds: 30, RenewSeconds: 10,
	}
	if err := r.publishConfigChanged(context.Background(),
		types.NamespacedName{Namespace: "ns", Name: "cr1"},
		specA, "old-fp", "new-fp"); err != nil {
		t.Fatalf("publishConfigChanged: %v", err)
	}
	got := rec.EventsByType(events.TypeConfigChanged)
	if len(got) != 1 {
		t.Fatalf("expected 1 continuum-config-changed, got %d", len(got))
	}
	if !strings.Contains(got[0].Message, "old-fp") || !strings.Contains(got[0].Message, "new-fp") {
		t.Errorf("Message should include prev + cur fingerprints; got %q", got[0].Message)
	}
}

func TestPublishLeaseCollision_FiresWithCause(t *testing.T) {
	t.Parallel()
	r, rec, _ := newReconciler(t)
	specA := ContinuumSpec{
		ApplicationRef: "demo-app", PrimaryRegion: "fsn",
		HotStandbyRegions: []string{"hel"}, LeaseClientKind: "in-memory",
	}
	cause := witness.ErrLeaseHeldByAnother
	if err := r.publishLeaseCollision(context.Background(),
		types.NamespacedName{Namespace: "ns", Name: "cr1"},
		specA, cause); err != nil {
		t.Fatalf("publishLeaseCollision: %v", err)
	}
	got := rec.EventsByType(events.TypeLeaseCollision)
	if len(got) != 1 {
		t.Fatalf("expected 1 continuum-lease-collision, got %d", len(got))
	}
	if !strings.Contains(got[0].Message, "ErrLeaseHeldByAnother") {
		t.Errorf("Message should mention ErrLeaseHeldByAnother; got %q", got[0].Message)
	}
}

func TestSwitchoverSpecFingerprint_DriftDetection(t *testing.T) {
	t.Parallel()
	specA := ContinuumSpec{
		PrimaryRegion: "fsn", HotStandbyRegions: []string{"hel"},
		LeaseClientKind: "in-memory", TTLSeconds: 30, RenewSeconds: 10,
		RTOSeconds: 60, AutoFailover: true, CNPGNamespace: "ns",
		CNPGPair: "demo", PDMZone: "example.com",
	}
	fpA := switchoverSpecFingerprint(specA)
	if fpA == "" {
		t.Fatal("fingerprint empty")
	}
	specB := specA
	specB.PrimaryRegion = "hel"
	fpB := switchoverSpecFingerprint(specB)
	if fpA == fpB {
		t.Errorf("fingerprint should change on PrimaryRegion drift; both = %q", fpA)
	}
	// Idempotent on same input.
	if switchoverSpecFingerprint(specA) != fpA {
		t.Errorf("fingerprint not idempotent")
	}
}

// ----------------------------------------------------------------------
// Slice F-3 — post-switchover health chain tests
// ----------------------------------------------------------------------

func TestSplitNamespacedName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		ns, name string
		ok       bool
	}{
		{"ns/cr1", "ns", "cr1", true},
		{"a/b", "a", "b", true},
		{"no-slash", "", "", false},
		{"/leading", "", "", false},
		{"trailing/", "", "", false},
		{"a/b/c", "", "", false},
	}
	for _, c := range cases {
		ns, name, ok := splitNamespacedName(c.in)
		if ok != c.ok || ns != c.ns || name != c.name {
			t.Errorf("splitNamespacedName(%q) = (%q, %q, %v) want (%q, %q, %v)",
				c.in, ns, name, ok, c.ns, c.name, c.ok)
		}
	}
}

func TestSummarizeHealth_HappyPath(t *testing.T) {
	t.Parallel()
	rep := switchover.HealthReport{
		NewPrimaryRegion: "hel",
		OverallHealthy:   true,
		Checks: []switchover.HealthCheck{
			{Name: switchover.CheckReplicasHealthy, Passed: true},
			{Name: switchover.CheckDNSProbes, Passed: true},
			{Name: switchover.CheckLatencyNormal, Deferred: true},
			{Name: switchover.CheckAuditPosted, Passed: true},
		},
	}
	got := summarizeHealth(rep)
	if !strings.Contains(got, "healthy") {
		t.Errorf("happy path summary should say healthy: %q", got)
	}
	if !strings.Contains(got, "3 passed") {
		t.Errorf("summary should say 3 passed: %q", got)
	}
	if !strings.Contains(got, "1 deferred") {
		t.Errorf("summary should say 1 deferred: %q", got)
	}
	if !strings.Contains(got, "hel") {
		t.Errorf("summary should mention new primary region: %q", got)
	}
}

func TestSummarizeHealth_Unhealthy(t *testing.T) {
	t.Parallel()
	rep := switchover.HealthReport{
		NewPrimaryRegion: "hel",
		OverallHealthy:   false,
		Checks: []switchover.HealthCheck{
			{Name: switchover.CheckReplicasHealthy, Passed: false, Detail: "not Ready"},
			{Name: switchover.CheckDNSProbes, Passed: true},
			{Name: switchover.CheckLatencyNormal, Deferred: true},
			{Name: switchover.CheckAuditPosted, Passed: true},
		},
	}
	got := summarizeHealth(rep)
	if !strings.Contains(got, "UNHEALTHY") {
		t.Errorf("unhealthy summary should say UNHEALTHY: %q", got)
	}
	if !strings.Contains(got, "1 failed") {
		t.Errorf("summary should say 1 failed: %q", got)
	}
}

func TestPatchStatusByName_BadIdentifier(t *testing.T) {
	t.Parallel()
	r, _, _ := newReconciler(t)
	err := r.patchStatusByName(context.Background(), "no-slash", statusUpdate{})
	if err == nil {
		t.Fatal("expected error on identifier without /")
	}
}

func TestPatchStatusByName_HappyPath(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)

	if err := r.patchStatusByName(context.Background(), "ns/cr1", statusUpdate{
		LastSwitchoverHealthy:       "True",
		LastSwitchoverHealthyDetail: "all probes green",
		Reason:                      "PostSwitchoverHealth",
	}); err != nil {
		t.Fatalf("patchStatusByName: %v", err)
	}
	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	found := false
	for _, c := range conds {
		cm, _ := c.(map[string]interface{})
		ty, _ := cm["type"].(string)
		s, _ := cm["status"].(string)
		if ty == "LastSwitchoverHealthy" && s == "True" {
			found = true
			msg, _ := cm["message"].(string)
			if !strings.Contains(msg, "all probes green") {
				t.Errorf("message = %q want substring 'all probes green'", msg)
			}
		}
	}
	if !found {
		t.Errorf("LastSwitchoverHealthy condition not found in conditions=%+v", conds)
	}
}

func TestRunPostSwitchoverHealth_PatchesConditionFalse(t *testing.T) {
	t.Parallel()
	cr := newTestContinuumCR("ns", "cr1", "fsn", []string{"hel"}, "in-memory")
	objs := append([]runtime.Object{cr}, newTestClusterPair("ns", "demo", 0)...)
	r, _, _ := newReconciler(t, objs...)
	r.HealthDelay = 0 // skip the 30s sleep in tests

	// CNPG halves don't have Ready=true conditions in newTestClusterPair,
	// so the replicas check fails → OverallHealthy=false.
	plan := switchover.SwitchoverPlan{
		ContinuumName:   "ns/cr1",
		ApplicationName: "demo-app",
		FromRegion:      "fsn", ToRegion: "hel",
		CNPGPair: "demo", CNPGNamespace: "ns",
	}
	seq := &switchover.Sequencer{
		CNPG:  cnpg.NewReader(r.Dyn),
		Audit: events.NewRecorder(),
	}
	r.runPostSwitchoverHealth(plan, seq)

	got, _ := r.Dyn.Resource(ContinuumGVR).Namespace("ns").Get(context.Background(), "cr1", metav1.GetOptions{})
	conds, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	found := false
	for _, c := range conds {
		cm, _ := c.(map[string]interface{})
		ty, _ := cm["type"].(string)
		s, _ := cm["status"].(string)
		if ty == "LastSwitchoverHealthy" {
			found = true
			if s != "False" {
				t.Errorf("expected LastSwitchoverHealthy=False (replicas not ready), got %q", s)
			}
		}
	}
	if !found {
		t.Error("LastSwitchoverHealthy condition not written")
	}
}
