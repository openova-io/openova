package switchover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

func newTestApp(hostnames []string) *unstructured.Unstructured {
	app := &unstructured.Unstructured{}
	app.Object = map[string]interface{}{
		"apiVersion": "apps.openova.io/v1",
		"kind":       "Application",
		"metadata":   map[string]interface{}{"name": "demo-app", "namespace": "demo"},
		"spec":       map[string]interface{}{},
	}
	if len(hostnames) > 0 {
		hns := make([]interface{}, 0, len(hostnames))
		for _, h := range hostnames {
			hns = append(hns, h)
		}
		app.Object["spec"].(map[string]interface{})["routes"] = []interface{}{
			map[string]interface{}{"hostnames": hns},
		}
	}
	return app
}

// fakeHTTPRoute is a hand-rolled HTTPRouteDrainer for tests.
type fakeHTTPRoute struct {
	mu             sync.Mutex
	setCalls       []string // region per call
	restoreCalls   [][]int
	failOnSet      bool
	failOnRestore  bool
	priorReturned  []int
}

func (f *fakeHTTPRoute) SetWeightZero(ctx context.Context, ns, name, region string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setCalls = append(f.setCalls, region)
	if f.failOnSet {
		return nil, errors.New("fake: SetWeightZero failure")
	}
	return f.priorReturned, nil
}
func (f *fakeHTTPRoute) RestoreWeights(ctx context.Context, ns, name string, weights []int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restoreCalls = append(f.restoreCalls, weights)
	if f.failOnRestore {
		return errors.New("fake: RestoreWeights failure")
	}
	return nil
}

func gvrListMap() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		cnpg.ClusterGVR: "ClusterList",
	}
}

func newClusterPair(ns, pair string) []runtime.Object {
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

	return []runtime.Object{primary, replica}
}

func makeSequencer(t *testing.T) (*Sequencer, *witness.InMemoryClient, *events.Recorder, *fakeHTTPRoute, *cnpg.Reader, []dns.Record) {
	t.Helper()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	// Pre-populate lease with FromRegion holding it (typical
	// start-of-switchover state).
	if _, err := w.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	rec := events.NewRecorder()
	httpFake := &fakeHTTPRoute{priorReturned: []int{100, 0}}
	scheme := runtime.NewScheme()
	objs := newClusterPair("ns", "demo")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), objs...)
	cnpgR := cnpg.NewReader(dyn)

	committed := []dns.Record{}
	var mu sync.Mutex
	seq := &Sequencer{
		CNPG:      cnpgR,
		Witness:   w,
		HTTPRoute: httpFake,
		Audit:     rec,
		Sleep:     func(time.Duration) {},
		PDMCommit: func(ctx context.Context, records []dns.Record) error {
			mu.Lock()
			defer mu.Unlock()
			committed = append(committed, records...)
			return nil
		},
	}
	return seq, w, rec, httpFake, cnpgR, committed
}

func defaultPlan() SwitchoverPlan {
	return SwitchoverPlan{
		ContinuumName:      "ns/cr",
		ApplicationName:    "demo/demo-app",
		FromRegion:         "fsn",
		ToRegion:           "hel",
		CNPGPair:           "demo",
		CNPGNamespace:     "ns",
		HTTPRouteName:      "demo-app",
		HTTPRouteNamespace: "demo",
		PDMZone:            "example.com",
		Application:        newTestApp([]string{"a.example.com"}),
		SynthParams: dns.SynthParams{
			RegionToIPs: map[string][]string{
				"fsn": {"5.1.2.3"},
				"hel": {"5.5.6.7"},
			},
			HealthCheckURL: "https://probe-fsn.example.com/healthz",
			Hostnames:      []string{"a.example.com"},
		},
		InitiatedBy: "alice@example.com",
	}
}

func TestExecute_HappyPath(t *testing.T) {
	t.Parallel()
	seq, w, rec, httpFake, cnpgR, _ := makeSequencer(t)

	plan := defaultPlan()
	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("Execute: %v (failed at step %d)", res.Err, res.FailedAtStep)
	}
	if got, want := len(res.StepsCompleted), 7; got != want {
		t.Errorf("StepsCompleted = %d want %d", got, want)
	}
	// Lease moved to ToRegion.
	st, _ := w.Read(context.Background())
	if st.Holder != "hel" {
		t.Errorf("lease holder = %q want hel", st.Holder)
	}
	// HTTPRoute drain was called for FromRegion.
	if len(httpFake.setCalls) != 1 || httpFake.setCalls[0] != "fsn" {
		t.Errorf("HTTPRoute setCalls = %v want [fsn]", httpFake.setCalls)
	}
	// Switchover audit emitted.
	if got := rec.EventsByType(events.TypeSwitchover); len(got) != 1 {
		t.Errorf("expected exactly 1 TypeSwitchover audit, got %d", len(got))
	}
	// CNPG: replica.enabled flipped — primary CR (now becoming
	// replica) should have replica.enabled=true.
	_, primaryCR, _ := cnpgR.Get(context.Background(), "ns", "demo-primary")
	en, _, _ := unstructured.NestedBool(primaryCR.Object, "spec", "replica", "enabled")
	if !en {
		t.Errorf("expected primary CR replica.enabled=true post-switchover")
	}
	_, replicaCR, _ := cnpgR.Get(context.Background(), "ns", "demo-replica")
	en, _, _ = unstructured.NestedBool(replicaCR.Object, "spec", "replica", "enabled")
	if en {
		t.Errorf("expected replica CR replica.enabled=false post-switchover")
	}
}

func TestExecute_StepHookFires(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	hookCalls := []int{}
	seq.StepHook = func(step int, name string) {
		hookCalls = append(hookCalls, step)
	}
	res := seq.Execute(context.Background(), defaultPlan())
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(hookCalls) != 7 {
		t.Errorf("hookCalls = %d want 7 (steps 1..7)", len(hookCalls))
	}
}

func TestExecute_Validate_RejectsSameFromTo(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	plan := defaultPlan()
	plan.ToRegion = plan.FromRegion
	res := seq.Execute(context.Background(), plan)
	if res.Err == nil {
		t.Fatal("expected error when From==To")
	}
}

func TestExecute_LeaseBlockedByThirdParty(t *testing.T) {
	t.Parallel()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	if _, err := w.Acquire(context.Background(), "rogue-region", time.Hour); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := events.NewRecorder()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), newClusterPair("ns", "demo")...)
	seq := &Sequencer{
		CNPG:      cnpg.NewReader(dyn),
		Witness:   w,
		Audit:     rec,
		Sleep:     func(time.Duration) {},
		PDMCommit: func(ctx context.Context, _ []dns.Record) error { return nil },
	}
	plan := defaultPlan()
	plan.HTTPRouteName = ""
	res := seq.Execute(context.Background(), plan)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "validate-lease") {
		t.Fatalf("expected lease-validate failure, got %v", res.Err)
	}
	if res.FailedAtStep != 1 {
		t.Errorf("FailedAtStep = %d want 1", res.FailedAtStep)
	}
	if got := rec.EventsByType(events.TypeError); len(got) == 0 {
		t.Errorf("expected error audit emit")
	}
}

func TestExecute_Step3FailureRollsBackPriorSteps(t *testing.T) {
	t.Parallel()
	seq, _, rec, httpFake, cnpgR, _ := makeSequencer(t)
	httpFake.failOnSet = true

	res := seq.Execute(context.Background(), defaultPlan())
	if res.Err == nil {
		t.Fatal("expected step-3 failure")
	}
	if res.FailedAtStep != 3 {
		t.Errorf("FailedAtStep = %d want 3", res.FailedAtStep)
	}
	// Step 2 should be rolled back: cordon annotation should be cleared.
	_, primary, _ := cnpgR.Get(context.Background(), "ns", "demo-primary")
	if _, ok := primary.GetAnnotations()[cnpg.PrimaryAnnotation]; ok {
		t.Errorf("expected cordon annotation cleared after rollback")
	}
	// Audit error event was emitted.
	if got := rec.EventsByType(events.TypeError); len(got) == 0 {
		t.Errorf("expected error audit emit")
	}
}

func TestExecute_Step4DNSFailureRollsBackHTTPRouteAndCordon(t *testing.T) {
	t.Parallel()
	seq, _, _, httpFake, cnpgR, _ := makeSequencer(t)
	seq.PDMCommit = func(ctx context.Context, _ []dns.Record) error {
		return errors.New("pdm: simulated network error")
	}
	res := seq.Execute(context.Background(), defaultPlan())
	if res.Err == nil {
		t.Fatal("expected step-4 failure")
	}
	if res.FailedAtStep != 4 {
		t.Errorf("FailedAtStep = %d want 4", res.FailedAtStep)
	}
	// Rollback: HTTPRoute weights restored.
	if len(httpFake.restoreCalls) != 1 {
		t.Errorf("expected 1 RestoreWeights call, got %d", len(httpFake.restoreCalls))
	}
	// Cordon cleared.
	_, primary, _ := cnpgR.Get(context.Background(), "ns", "demo-primary")
	if _, ok := primary.GetAnnotations()[cnpg.PrimaryAnnotation]; ok {
		t.Errorf("expected cordon annotation cleared after rollback")
	}
}

func TestExecute_NoHTTPRouteIsNoOpStep3(t *testing.T) {
	t.Parallel()
	seq, _, _, httpFake, _, _ := makeSequencer(t)
	plan := defaultPlan()
	plan.HTTPRouteName = ""
	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(httpFake.setCalls) != 0 {
		t.Errorf("step-3 should be no-op when HTTPRouteName is empty")
	}
}

func TestExecute_ContextCancel(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := seq.Execute(ctx, defaultPlan())
	if res.Err == nil {
		t.Fatal("expected ctx-cancel failure")
	}
}

func TestRequestFailback_NoApprovalGate(t *testing.T) {
	t.Parallel()
	seq, _, rec, _, _, _ := makeSequencer(t)
	res := seq.RequestFailback(context.Background(), defaultPlan(), FailbackOptions{RequireApproval: false})
	if res.Err != nil {
		t.Fatalf("RequestFailback: %v", res.Err)
	}
	if got := rec.EventsByType(events.TypeFailbackPending); len(got) != 1 {
		t.Errorf("expected 1 FailbackPending event, got %d", len(got))
	}
	if got := rec.EventsByType(events.TypeFailbackCompleted); len(got) != 1 {
		t.Errorf("expected 1 FailbackCompleted event, got %d", len(got))
	}
}

func TestRequestFailback_ApprovalGate_Approved(t *testing.T) {
	t.Parallel()
	seq, _, rec, _, _, _ := makeSequencer(t)
	approved := make(chan struct{})
	done := make(chan Result, 1)
	go func() {
		done <- seq.RequestFailback(context.Background(), defaultPlan(), FailbackOptions{
			RequireApproval: true,
			ApprovalCh:      approved,
			ApprovalTimeout: 5 * time.Second,
		})
	}()
	close(approved)
	res := <-done
	if res.Err != nil {
		t.Fatalf("RequestFailback (approved): %v", res.Err)
	}
	if got := rec.EventsByType(events.TypeFailbackCompleted); len(got) != 1 {
		t.Errorf("expected FailbackCompleted, got %d", len(got))
	}
}

func TestRequestFailback_ApprovalGate_Timeout(t *testing.T) {
	t.Parallel()
	seq, _, rec, _, _, _ := makeSequencer(t)
	approved := make(chan struct{})
	res := seq.RequestFailback(context.Background(), defaultPlan(), FailbackOptions{
		RequireApproval: true,
		ApprovalCh:      approved,
		ApprovalTimeout: 10 * time.Millisecond,
	})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "approval timeout") {
		t.Fatalf("expected timeout error, got %v", res.Err)
	}
	// FailbackPending was emitted, but FailbackCompleted was NOT.
	if got := rec.EventsByType(events.TypeFailbackPending); len(got) != 1 {
		t.Errorf("expected FailbackPending event")
	}
	if got := rec.EventsByType(events.TypeFailbackCompleted); len(got) != 0 {
		t.Errorf("FailbackCompleted should not fire on timeout, got %d", len(got))
	}
}

func TestRequestFailback_RequireApprovalNilCh(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	res := seq.RequestFailback(context.Background(), defaultPlan(), FailbackOptions{RequireApproval: true, ApprovalCh: nil})
	if res.Err == nil {
		t.Fatal("expected error when RequireApproval=true and ApprovalCh=nil")
	}
}

func TestSwitchoverPlan_Defaults(t *testing.T) {
	t.Parallel()
	p := SwitchoverPlan{}.Defaults()
	if p.DrainSeconds != 10 {
		t.Errorf("DrainSeconds = %d want 10", p.DrainSeconds)
	}
	if p.LeaseTTL != 30*time.Second {
		t.Errorf("LeaseTTL = %v want 30s", p.LeaseTTL)
	}
	if p.Reason != "operator-requested" {
		t.Errorf("Reason = %q", p.Reason)
	}
}

func TestSwitchoverPlan_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    SwitchoverPlan
		ok   bool
	}{
		{"happy", SwitchoverPlan{ContinuumName: "x", FromRegion: "a", ToRegion: "b", CNPGPair: "p", CNPGNamespace: "n"}, true},
		{"missing-name", SwitchoverPlan{FromRegion: "a", ToRegion: "b", CNPGPair: "p", CNPGNamespace: "n"}, false},
		{"missing-from", SwitchoverPlan{ContinuumName: "x", ToRegion: "b", CNPGPair: "p", CNPGNamespace: "n"}, false},
		{"missing-to", SwitchoverPlan{ContinuumName: "x", FromRegion: "a", CNPGPair: "p", CNPGNamespace: "n"}, false},
		{"same", SwitchoverPlan{ContinuumName: "x", FromRegion: "a", ToRegion: "a", CNPGPair: "p", CNPGNamespace: "n"}, false},
		{"missing-pair", SwitchoverPlan{ContinuumName: "x", FromRegion: "a", ToRegion: "b"}, false},
	}
	for _, c := range cases {
		got := c.p.Validate() == nil
		if got != c.ok {
			t.Errorf("%s: Validate ok=%v want %v", c.name, got, c.ok)
		}
	}
}

func TestExecute_StepNumberInError(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	seq.PDMCommit = func(ctx context.Context, _ []dns.Record) error {
		return errors.New("simulated")
	}
	res := seq.Execute(context.Background(), defaultPlan())
	if res.Err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(res.Err.Error(), "step-4") {
		t.Errorf("error should name step-4: %v", res.Err)
	}
}
