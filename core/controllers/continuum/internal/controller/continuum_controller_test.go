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
		ContinuumGVR:                                   "ContinuumList",
		cnpg.ClusterGVR:                                "ClusterList",
		switchover.HTTPRouteGVR:                        "HTTPRouteList",
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
		Dyn:             dyn,
		WitnessSelector: sel,
		HoldingRegion:   "fsn",
		Audit:           rec,
		Drainer:         &fakeDrainer{},
		Sleep:           func(time.Duration) {},
		Now:             time.Now,
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

func TestParseSpec_ErrorPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mut  func(*unstructured.Unstructured)
	}{
		{"missing applicationRef", func(c *unstructured.Unstructured) { _ = unstructured.SetNestedField(c.Object, "", "spec", "applicationRef") }},
		{"missing primaryRegion", func(c *unstructured.Unstructured) { _ = unstructured.SetNestedField(c.Object, "", "spec", "primaryRegion") }},
		{"empty hotStandbyRegions", func(c *unstructured.Unstructured) { unstructured.RemoveNestedField(c.Object, "spec", "hotStandbyRegions") }},
		{"missing leaseClient.kind", func(c *unstructured.Unstructured) { _ = unstructured.SetNestedField(c.Object, "", "spec", "leaseClient", "kind") }},
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
