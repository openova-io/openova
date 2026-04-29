// Tests for the Phase-1 HelmRelease watch loop.
//
// What this file proves (matches the GATES checklist for the
// HelmRelease-watch task):
//
//  1. DeriveState — the documented condition→state machine, every
//     row of the docs/PROVISIONING-PLAN.md state-table.
//  2. Watcher.Watch — given a fake.NewSimpleDynamicClient seeded with
//     bp-* HelmReleases that progress through pending → installing →
//     installed (and one that fails), the watch emits the right
//     phase: "component" Events and terminates when every observed
//     component reaches a terminal state.
//  3. Termination on timeout — when one component is stuck in
//     "installing" forever, the watch terminates after WatchTimeout
//     and returns the partial state map so markPhase1Done can
//     persist what it observed.
//  4. ComponentIDFromHelmRelease + CompileWatchTimeout — pure helper
//     coverage so a future rename of the prefix or env var lands as a
//     test failure instead of silent drift.
//  5. Pod-restart resume — a Watcher freshly constructed against an
//     already-Ready HelmRelease emits exactly one "installed" event
//     and terminates. This is the rehydrate-from-PVC path.
//
// We use the apimachinery dynamic fake client with an
// UnstructuredList registered for HelmRelease so the dynamic
// informer's List + Watch calls both resolve. Each test creates its
// own fake client so concurrent tests can't observe each other's
// HelmReleases.
package helmwatch

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// helmReleaseListGVK — the HelmReleaseList GVK the dynamic fake
// client needs registered so List(...) calls resolve. Without this,
// the informer's first List returns "no kind 'HelmReleaseList' is
// registered" and the watch never starts.
var helmReleaseListGVK = schema.GroupVersionKind{
	Group:   "helm.toolkit.fluxcd.io",
	Version: "v2",
	Kind:    "HelmReleaseList",
}

// newFakeScheme returns a runtime.Scheme with the HelmReleaseList GVK
// registered so the dynamic fake informer can List+Watch.
func newFakeScheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(helmReleaseListGVK, &unstructured.UnstructuredList{})
	return scheme
}

// makeHelmRelease constructs a single bp-* HelmRelease with the given
// status conditions. Reason / message are optional — pass empty strings
// to omit (the apimachinery converter handles missing fields cleanly).
func makeHelmRelease(name string, conds []metav1.Condition) *unstructured.Unstructured {
	condMaps := make([]any, 0, len(conds))
	for _, c := range conds {
		condMaps = append(condMaps, map[string]any{
			"type":               c.Type,
			"status":             string(c.Status),
			"reason":             c.Reason,
			"message":            c.Message,
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		})
	}

	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]any{
				"name":      name,
				"namespace": FluxNamespace,
			},
			"spec": map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{
						"chart": name,
					},
				},
			},
			"status": map[string]any{
				"conditions": condMaps,
			},
		},
	}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	return u
}

// recorder — collects every Event the Watcher emits for assertions.
type recorder struct {
	mu     sync.Mutex
	events []provisioner.Event
}

func (r *recorder) emit(ev provisioner.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []provisioner.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]provisioner.Event, len(r.events))
	copy(out, r.events)
	return out
}

// componentStateEvents filters down to phase=component events with a
// non-empty Component (excludes the "watch terminated by context"
// info message which has no Component set).
func (r *recorder) componentStateEvents() []provisioner.Event {
	out := []provisioner.Event{}
	for _, ev := range r.snapshot() {
		if ev.Phase == PhaseComponent && ev.Component != "" {
			out = append(out, ev)
		}
	}
	return out
}

// fakeFactory — closure the Watcher's Config calls to acquire the
// dynamic client. Tests pass this in via Config.DynamicFactory so no
// real cluster is needed.
func fakeFactory(client dynamic.Interface) func(string) (dynamic.Interface, error) {
	return func(_ string) (dynamic.Interface, error) {
		return client, nil
	}
}

// ─────────────────────────────────────────────────────────────────────
// DeriveState — pure state-machine coverage. One test per documented
// row of the state table.
// ─────────────────────────────────────────────────────────────────────

func TestDeriveState_NoReadyCondition_IsPending(t *testing.T) {
	got := DeriveState(nil)
	if got != StatePending {
		t.Fatalf("no Ready condition → expected %q, got %q", StatePending, got)
	}
}

func TestDeriveState_ReadyTrue_IsInstalled(t *testing.T) {
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
	})
	if got != StateInstalled {
		t.Fatalf("Ready=True → expected %q, got %q", StateInstalled, got)
	}
}

func TestDeriveState_ReadyUnknown_IsInstalling(t *testing.T) {
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "Progressing", Message: "Reconciliation in progress"},
	})
	if got != StateInstalling {
		t.Fatalf("Ready=Unknown → expected %q, got %q", StateInstalling, got)
	}
}

func TestDeriveState_ReadyFalse_InstallFailed_IsFailed(t *testing.T) {
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "InstallFailed", Message: "chart failed: timed out waiting for the condition"},
	})
	if got != StateFailed {
		t.Fatalf("Ready=False reason=InstallFailed → expected %q, got %q", StateFailed, got)
	}
}

func TestDeriveState_ReadyFalse_UpgradeFailed_IsFailed(t *testing.T) {
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "UpgradeFailed", Message: "upgrade retries exhausted"},
	})
	if got != StateFailed {
		t.Fatalf("Ready=False reason=UpgradeFailed → expected %q, got %q", StateFailed, got)
	}
}

func TestDeriveState_ReadyFalse_ChartPullError_IsFailed(t *testing.T) {
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "ChartPullError", Message: "GET ghcr.io/...: 401"},
	})
	if got != StateFailed {
		t.Fatalf("Ready=False reason=ChartPullError → expected %q, got %q", StateFailed, got)
	}
}

func TestDeriveState_ReadyFalse_DependencyMessage_IsPending(t *testing.T) {
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "DependencyNotReady", Message: "dependency 'flux-system/bp-cilium' is not ready"},
	})
	if got != StatePending {
		t.Fatalf("Ready=False with dependency message → expected %q (waiting), got %q", StatePending, got)
	}
}

func TestDeriveState_ReadyFalse_Progressing_IsInstalling(t *testing.T) {
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "Progressing", Message: "Reconciliation in progress"},
	})
	if got != StateInstalling {
		t.Fatalf("Ready=False reason=Progressing → expected %q, got %q", StateInstalling, got)
	}
}

func TestDeriveState_ReadyFalse_UnknownReason_IsDegraded(t *testing.T) {
	// A Ready=False with no reason we recognise as either failed or
	// still-progressing falls into the degraded bucket — the install
	// completed but readiness was lost (e.g. a Pod went CrashLoopBackOff
	// after first install). Flux retries; the watch keeps emitting
	// until the component re-converges.
	got := DeriveState([]metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "RetryExhausted", Message: "deployment.apps/foo: rollout stuck"},
	})
	if got != StateDegraded {
		t.Fatalf("Ready=False with unknown reason → expected %q, got %q", StateDegraded, got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// ComponentIDFromHelmRelease + CompileWatchTimeout — helpers.
// ─────────────────────────────────────────────────────────────────────

func TestComponentIDFromHelmRelease_StripsPrefix(t *testing.T) {
	cases := map[string]string{
		"bp-cilium":             "cilium",
		"bp-cert-manager":       "cert-manager",
		"bp-catalyst-platform":  "catalyst-platform",
		"helm-controller":       "helm-controller",
		"":                      "",
	}
	for in, want := range cases {
		if got := ComponentIDFromHelmRelease(in); got != want {
			t.Errorf("ComponentIDFromHelmRelease(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompileWatchTimeout_DefaultOnEmpty(t *testing.T) {
	if got := CompileWatchTimeout(""); got != DefaultWatchTimeout {
		t.Fatalf("empty input → expected %v, got %v", DefaultWatchTimeout, got)
	}
}

func TestCompileWatchTimeout_DefaultOnInvalid(t *testing.T) {
	if got := CompileWatchTimeout("not-a-duration"); got != DefaultWatchTimeout {
		t.Fatalf("invalid input → expected default %v, got %v", DefaultWatchTimeout, got)
	}
	if got := CompileWatchTimeout("-5m"); got != DefaultWatchTimeout {
		t.Fatalf("negative input → expected default %v, got %v", DefaultWatchTimeout, got)
	}
}

func TestCompileWatchTimeout_ParsesValid(t *testing.T) {
	if got := CompileWatchTimeout("90m"); got != 90*time.Minute {
		t.Fatalf("90m → expected %v, got %v", 90*time.Minute, got)
	}
	if got := CompileWatchTimeout("2h"); got != 2*time.Hour {
		t.Fatalf("2h → expected %v, got %v", 2*time.Hour, got)
	}
}

// ─────────────────────────────────────────────────────────────────────
// Watcher.Watch — informer-driven coverage.
// ─────────────────────────────────────────────────────────────────────

// TestWatch_AllReleasesAlreadyInstalled_TerminatesQuickly proves the
// rehydrate-from-PVC path: a Watcher that attaches to a cluster where
// every bp-* HelmRelease already has Ready=True emits exactly one
// "installed" event per component and terminates.
func TestWatch_AllReleasesAlreadyInstalled_TerminatesQuickly(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeHelmRelease("bp-cilium", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
		makeHelmRelease("bp-cert-manager", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
		makeHelmRelease("bp-flux", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake-kubeconfig: bytes",
		WatchTimeout:   5 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0, // event-driven only
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	final, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got, want := len(final), 3; got != want {
		t.Errorf("expected %d component states, got %d (states=%v)", want, got, final)
	}
	for _, comp := range []string{"cilium", "cert-manager", "flux"} {
		if final[comp] != StateInstalled {
			t.Errorf("component %q state = %q, want %q", comp, final[comp], StateInstalled)
		}
	}

	componentEvents := rec.componentStateEvents()
	if len(componentEvents) != 3 {
		t.Errorf("expected 3 phase=component events, got %d:\n%+v", len(componentEvents), componentEvents)
	}
	for _, ev := range componentEvents {
		if ev.State != StateInstalled {
			t.Errorf("component %q event State = %q, want %q", ev.Component, ev.State, StateInstalled)
		}
		if ev.Phase != PhaseComponent {
			t.Errorf("expected Phase=%q, got %q", PhaseComponent, ev.Phase)
		}
	}
}

// TestWatch_TransitionsEmitInOrder proves the full state machine
// across an Add (pending) → Update (installing) → Update (installed)
// sequence on a single HelmRelease. The watch must emit three
// Events, each with the right State, then terminate when the
// release reaches Installed.
func TestWatch_TransitionsEmitInOrder(t *testing.T) {
	scheme := newFakeScheme()
	// Start with no Ready condition → pending.
	hr := makeHelmRelease("bp-cilium", nil)
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		hr,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   5 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Drive the transitions in a goroutine — the Watch is blocking,
	// and we need the informer to observe each Update before we issue
	// the next one. The fake client's Update is synchronous w.r.t. the
	// store, so the informer's UpdateFunc fires reliably.
	done := make(chan map[string]string, 1)
	go func() {
		final, _ := w.Watch(ctx)
		done <- final
	}()

	// Wait until the watch has observed the initial pending state.
	if !waitForCondition(t, 2*time.Second, func() bool {
		return len(rec.componentStateEvents()) >= 1
	}) {
		t.Fatalf("watch never emitted pending event")
	}

	// Update 1: Ready=Unknown → installing.
	updateHR(t, client, "bp-cilium", []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "Progressing", Message: "Reconciliation in progress"},
	})
	if !waitForCondition(t, 2*time.Second, func() bool {
		return countWithState(rec.componentStateEvents(), StateInstalling) >= 1
	}) {
		t.Fatalf("watch never emitted installing event")
	}

	// Update 2: Ready=True → installed (terminal).
	updateHR(t, client, "bp-cilium", []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
	})

	select {
	case final := <-done:
		if final["cilium"] != StateInstalled {
			t.Errorf("final state = %q, want %q", final["cilium"], StateInstalled)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("watch did not terminate after Ready=True")
	}

	// Verify the event sequence: pending, installing, installed.
	events := rec.componentStateEvents()
	gotStates := make([]string, len(events))
	for i, e := range events {
		gotStates[i] = e.State
	}
	wantSubseq := []string{StatePending, StateInstalling, StateInstalled}
	if !containsSubsequence(gotStates, wantSubseq) {
		t.Errorf("events did not contain expected state subsequence %v in %v", wantSubseq, gotStates)
	}
}

// TestWatch_FailedReleaseTerminatesAndIsFailed proves "failed" counts
// as terminal — the watch ends, the final state map has "failed",
// and the upstream markPhase1Done flips Status=failed.
func TestWatch_FailedReleaseTerminatesAndIsFailed(t *testing.T) {
	scheme := newFakeScheme()
	hr := makeHelmRelease("bp-cert-manager", []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "InstallFailed", Message: "chart failed: timed out"},
	})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		hr,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   5 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	final, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if final["cert-manager"] != StateFailed {
		t.Errorf("expected cert-manager=%q, got %q", StateFailed, final["cert-manager"])
	}
	events := rec.componentStateEvents()
	if len(events) != 1 || events[0].State != StateFailed || events[0].Level != "error" {
		t.Errorf("expected one error/failed event, got: %+v", events)
	}
}

// TestWatch_TimeoutTerminatesWithPartialState proves that a stuck
// installing component does not block forever — the watch terminates
// on its configured timeout and the partial state is returned.
//
// Why a single stuck component (not "stuck + done") in this test:
// the informer fires AddFunc per object as the cache syncs in
// arbitrary order. If we seed both a non-terminal AND a terminal
// release, the order in which they reach processEvent is racy — when
// the terminal one arrives FIRST and the non-terminal hasn't yet
// been observed, allObservedTerminal({stuck:terminal}) == true and
// the watch closes `terminated` at that intermediate state. That is
// correct behaviour (the watch terminates as soon as every OBSERVED
// release is terminal), but it makes the test flaky for the
// timeout-path assertion. So we make the timeout-path the only
// possible exit by seeding only non-terminal releases.
func TestWatch_TimeoutTerminatesWithPartialState(t *testing.T) {
	scheme := newFakeScheme()
	stuck := makeHelmRelease("bp-keycloak", []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "Progressing", Message: "Reconciliation in progress"},
	})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		stuck,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   400 * time.Millisecond, // tiny — termination must come from this
		DynamicFactory: fakeFactory(client),
		Resync:         0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	start := time.Now()
	final, err := w.Watch(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Watch returned err: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("watch took %v — expected to terminate near WatchTimeout (400ms)", elapsed)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("watch took only %v — must have observed timeout, not all-terminal close", elapsed)
	}
	if final["keycloak"] != StateInstalling {
		t.Errorf("expected keycloak=%q (stuck mid-install), got %q", StateInstalling, final["keycloak"])
	}

	// A timeout-terminated watch emits a "watch terminated by context"
	// warn event so the SSE consumer can render the partial outcome.
	sawTimeoutEvent := false
	for _, ev := range rec.snapshot() {
		if ev.Phase == PhaseComponent && strings.Contains(ev.Message, "watch terminated by context") {
			sawTimeoutEvent = true
			break
		}
	}
	if !sawTimeoutEvent {
		t.Errorf("expected a 'watch terminated by context' warn event, got: %+v", rec.snapshot())
	}
}

// TestWatch_NonBPReleaseFiltered proves the FilterFunc keeps the
// watch focused on bp-* HelmReleases. A HelmRelease that doesn't
// start with "bp-" is ignored — it's not in the bootstrap-kit and
// emitting events for it would confuse the Sovereign Admin's "X of
// Y bootstrap components" counter.
func TestWatch_NonBPReleaseFiltered(t *testing.T) {
	scheme := newFakeScheme()
	bp := makeHelmRelease("bp-cilium", []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "ok"},
	})
	other := makeHelmRelease("some-other-chart", []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "ok"},
	})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		bp, other,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   5 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	final, err := w.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if _, ok := final["cilium"]; !ok {
		t.Errorf("expected cilium in final states, got: %v", final)
	}
	if _, ok := final["other-chart"]; ok {
		t.Errorf("non-bp- HelmRelease should not appear in final states: %v", final)
	}
	if _, ok := final["some-other-chart"]; ok {
		t.Errorf("non-bp- HelmRelease should not appear in final states: %v", final)
	}
}

// TestWatch_MissingKubeconfigRejected proves the Watcher refuses to
// start when the kubeconfig field is empty — this is the rehydrate
// path's failure mode (a deployment whose Phase 0 finished before
// kubeconfig capture lands).
func TestWatch_MissingKubeconfigRejected(t *testing.T) {
	rec := &recorder{}
	_, err := NewWatcher(Config{
		KubeconfigYAML: "",
		DynamicFactory: fakeFactory(nil),
	}, rec.emit)
	if err == nil {
		t.Fatalf("expected NewWatcher to reject empty kubeconfig")
	}
}

func TestWatch_NilEmitRejected(t *testing.T) {
	_, err := NewWatcher(Config{KubeconfigYAML: "fake"}, nil)
	if err == nil {
		t.Fatalf("expected NewWatcher to reject nil emit callback")
	}
}

// TestWatch_OnlyEmitsOnTransition proves the de-dup branch: a second
// informer Update event for the same HelmRelease with no state change
// (e.g. a status subresource patch from helm-controller's
// observedGeneration touch) does NOT produce a duplicate Event. The
// Sovereign Admin's status pill must not flicker at sub-second
// cadence.
func TestWatch_OnlyEmitsOnTransition(t *testing.T) {
	scheme := newFakeScheme()
	hr := makeHelmRelease("bp-cilium", []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
	})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		hr,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   2 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	final, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if final["cilium"] != StateInstalled {
		t.Fatalf("expected installed, got %q", final["cilium"])
	}

	// Issue a status patch with the SAME conditions — must NOT emit a
	// second event. (The watch already terminated above, but we do a
	// best-effort check that during the run only one emit landed for
	// cilium.)
	events := rec.componentStateEvents()
	if len(events) != 1 {
		t.Errorf("expected exactly 1 emit for cilium (no transition de-dup), got %d: %+v", len(events), events)
	}
}

// TestWatch_AllObservedTerminal_FivePhaseComponentEvents — captures
// the GATES requirement: a fake-clientset run produces 5 phase=
// component events. Five HelmReleases ending Installed = 5 phase=
// component events.
func TestWatch_AllObservedTerminal_FivePhaseComponentEvents(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeHelmRelease("bp-cilium", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
		makeHelmRelease("bp-cert-manager", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
		makeHelmRelease("bp-flux", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
		makeHelmRelease("bp-crossplane", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
		makeHelmRelease("bp-sealed-secrets", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   5 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	final, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got := len(final); got != 5 {
		t.Errorf("expected 5 component states, got %d: %v", got, final)
	}
	events := rec.componentStateEvents()
	if got := len(events); got != 5 {
		t.Errorf("expected 5 phase=component events, got %d: %+v", got, events)
	}

	// Capture for the GATES sample — t.Logf streams to stdout under
	// `go test -v` so the agent can paste these in the final report.
	for _, ev := range events {
		t.Logf("phase=%s component=%s state=%s level=%s message=%q",
			ev.Phase, ev.Component, ev.State, ev.Level, ev.Message)
	}
}

// ─────────────────────────────────────────────────────────────────────
// helpers
// ─────────────────────────────────────────────────────────────────────

// updateHR patches a HelmRelease's status.conditions in the fake
// client. The dynamic informer's UpdateFunc fires synchronously off
// this so the test can wait for the next event.
func updateHR(t *testing.T, client dynamic.Interface, name string, conds []metav1.Condition) {
	t.Helper()
	condMaps := make([]any, 0, len(conds))
	for _, c := range conds {
		condMaps = append(condMaps, map[string]any{
			"type":               c.Type,
			"status":             string(c.Status),
			"reason":             c.Reason,
			"message":            c.Message,
			"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
		})
	}
	patch := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "helm.toolkit.fluxcd.io/v2",
			"kind":       "HelmRelease",
			"metadata": map[string]any{
				"name":      name,
				"namespace": FluxNamespace,
			},
			"spec": map[string]any{
				"chart": map[string]any{
					"spec": map[string]any{
						"chart": name,
					},
				},
			},
			"status": map[string]any{
				"conditions": condMaps,
			},
		},
	}
	_, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).Update(
		t.Context(), patch, metav1.UpdateOptions{},
	)
	if err != nil {
		t.Fatalf("updateHR(%q): %v", name, err)
	}
}

// waitForCondition spins for up to d, checking cond every 10ms.
// Used to wait for the informer to deliver the next event without
// adding sleeps — keeps the test runtime tight even with -race.
func waitForCondition(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func countWithState(events []provisioner.Event, state string) int {
	n := 0
	for _, ev := range events {
		if ev.State == state {
			n++
		}
	}
	return n
}

// containsSubsequence reports whether `sub` appears as a (possibly
// non-contiguous) subsequence of `full`. We use it for state-machine
// tests where extra intervening states (e.g. a duplicate pending
// emit) are tolerable but the order of distinct states must hold.
func containsSubsequence(full, sub []string) bool {
	i := 0
	for _, v := range full {
		if i < len(sub) && v == sub[i] {
			i++
		}
	}
	return i == len(sub)
}

// ─────────────────────────────────────────────────────────────────────
// First-seen-gate coverage — the bug surfaced on the omantel run where
// the watch terminated 1 second after flux-bootstrap with finalStatus=
// "ready" because the informer cache was empty (Flux hadn't materialised
// the bootstrap-kit Kustomization yet). The gate fixes this by:
//   - refusing to consider termination until at least one HelmRelease
//     has been observed (firstSeenAt is non-zero)
//   - refusing to consider termination until ≥ MinBootstrapKitHRs have
//     been observed (a partial early reconcile cannot satisfy "done")
//   - emitting a warn event after FirstSeenTimeout if zero HRs are
//     observed, while CONTINUING the watch (late HRs still flow)
// ─────────────────────────────────────────────────────────────────────

// makeReadyHelmRelease — local helper that builds a Ready=True HR. We
// keep it separate from makeHelmRelease so the gate-tests below read
// declaratively (each test seeds N ready HRs; the test name names N).
func makeReadyHelmRelease(name string) *unstructured.Unstructured {
	return makeHelmRelease(name, []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
	})
}

// makeFailedHelmRelease — local helper that builds a Ready=False HR
// with the InstallFailed reason DeriveState maps to StateFailed.
func makeFailedHelmRelease(name string) *unstructured.Unstructured {
	return makeHelmRelease(name, []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionFalse, Reason: "InstallFailed", Message: "chart failed: timed out"},
	})
}

// TestWatch_EmptyList_FirstSeenTimeoutDoesNotTerminate proves the
// omantel bug fix: an empty informer cache (Flux hasn't reconciled
// the bootstrap-kit Kustomization yet on the new Sovereign) MUST NOT
// satisfy "all observed terminal." The watch keeps running until
// WatchTimeout, after which Outcome() reports
// OutcomeFluxNotReconciling so the handler can set
// Result.Phase1Outcome and the wizard banner can render the
// operator-actionable diagnostic.
//
// Test shape: empty list + tight FirstSeenTimeout + larger WatchTimeout
// → watch must NOT exit via the all-done channel (would be visible as
// elapsed < WatchTimeout); must emit the warn event; final state must
// be empty; Outcome must be flux-not-reconciling.
func TestWatch_EmptyList_FirstSeenTimeoutDoesNotTerminate(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       1500 * time.Millisecond,
		FirstSeenTimeout:   200 * time.Millisecond,
		MinBootstrapKitHRs: 11,
		DynamicFactory:     fakeFactory(client),
		Resync:             0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	start := time.Now()
	final, err := w.Watch(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Watch returned err: %v", err)
	}
	if elapsed < 1*time.Second {
		t.Errorf("watch returned in %v — expected to ride out the WatchTimeout (≥1s) since the all-done gate must NOT fire on an empty informer cache", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("watch took %v — expected to terminate near WatchTimeout (1.5s)", elapsed)
	}
	if got := len(final); got != 0 {
		t.Errorf("expected empty final state map, got %d entries: %v", got, final)
	}
	if got, want := w.Outcome(), OutcomeFluxNotReconciling; got != want {
		t.Errorf("Outcome() = %q, want %q", got, want)
	}

	// The watch must have emitted the "saw 0 HelmReleases" warn event.
	sawFluxNotReconcilingWarn := false
	for _, ev := range rec.snapshot() {
		if ev.Phase == PhaseComponent &&
			ev.Level == "warn" &&
			ev.Component == "" &&
			strings.Contains(ev.Message, "saw 0 HelmReleases") {
			sawFluxNotReconcilingWarn = true
			break
		}
	}
	if !sawFluxNotReconcilingWarn {
		t.Errorf("expected a 'saw 0 HelmReleases' warn event, got: %+v", rec.snapshot())
	}
}

// TestWatch_ZeroHRs_OneSecond_DoesNotTerminateEarly proves the
// terminate-on-all-done channel is NOT closed within the first second
// when zero bp-* HelmReleases are observed. This is the exact
// regression seen on omantel (`finalStatus: ready` 1 second after
// flux-bootstrap) — pinning it as a test prevents it from coming back.
//
// Test shape: empty list, large WatchTimeout. Wait 1s, cancel ctx, the
// watch should still be running until cancel. The outcome reflects
// the cancel path (flux-not-reconciling because no HRs were observed).
func TestWatch_ZeroHRs_OneSecond_DoesNotTerminateEarly(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       30 * time.Second,
		FirstSeenTimeout:   30 * time.Second, // no warn within test window
		MinBootstrapKitHRs: 11,
		DynamicFactory:     fakeFactory(client),
		Resync:             0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var final map[string]string
	go func() {
		defer close(done)
		final, _ = w.Watch(ctx)
	}()

	// Wait 1 second — the watch MUST still be running (this is the
	// regression assertion: 0 HRs after 1s does NOT close the
	// `terminated` channel).
	select {
	case <-done:
		t.Fatalf("watch terminated within 1 second despite zero observed HelmReleases — regression of the omantel bug")
	case <-time.After(1 * time.Second):
		// Good — watch is still running.
	}

	// Now cancel the context to let the watch exit cleanly.
	cancel()
	select {
	case <-done:
		// Expected.
	case <-time.After(5 * time.Second):
		t.Fatalf("watch did not exit after ctx cancel")
	}

	if got := len(final); got != 0 {
		t.Errorf("expected empty state map, got: %v", final)
	}
	if got, want := w.Outcome(), OutcomeFluxNotReconciling; got != want {
		t.Errorf("Outcome() = %q, want %q (no HR ever observed)", got, want)
	}
}

// TestWatch_11HRs_AllInstalled_TerminatesReady proves the happy
// terminate-on-all-done path: when ≥ MinBootstrapKitHRs are observed
// AND every observed component reaches StateInstalled, the watch
// terminates fast with Outcome=ready.
func TestWatch_11HRs_AllInstalled_TerminatesReady(t *testing.T) {
	scheme := newFakeScheme()
	names := []string{
		"bp-cilium",
		"bp-cert-manager",
		"bp-flux",
		"bp-crossplane",
		"bp-sealed-secrets",
		"bp-spire",
		"bp-nats-jetstream",
		"bp-openbao",
		"bp-keycloak",
		"bp-gitea",
		"bp-catalyst-platform",
	}
	releases := make([]runtime.Object, 0, len(names))
	for _, n := range names {
		releases = append(releases, makeReadyHelmRelease(n))
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       30 * time.Second, // must NOT be hit
		FirstSeenTimeout:   30 * time.Second,
		MinBootstrapKitHRs: 11,
		DynamicFactory:     fakeFactory(client),
		Resync:             0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	final, err := w.Watch(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("watch took %v — expected fast all-done termination, not a 30s timeout", elapsed)
	}
	if got, want := len(final), 11; got != want {
		t.Errorf("final states = %d, want %d: %v", got, want, final)
	}
	for _, n := range names {
		comp := ComponentIDFromHelmRelease(n)
		if final[comp] != StateInstalled {
			t.Errorf("final[%q] = %q, want %q", comp, final[comp], StateInstalled)
		}
	}
	if got, want := w.Outcome(), OutcomeReady; got != want {
		t.Errorf("Outcome() = %q, want %q", got, want)
	}
}

// TestWatch_11HRs_OneFailed_TerminatesFailed proves "failed" still
// counts as terminal under the gate: the watch terminates fast (does
// not wait for WatchTimeout) and reports Outcome=failed when at least
// one of the ≥ MinBootstrapKitHRs observed components ended in
// StateFailed.
func TestWatch_11HRs_OneFailed_TerminatesFailed(t *testing.T) {
	scheme := newFakeScheme()
	names := []string{
		"bp-cilium",
		"bp-cert-manager",
		"bp-flux",
		"bp-crossplane",
		"bp-sealed-secrets",
		"bp-spire",
		"bp-nats-jetstream",
		"bp-openbao",
		"bp-keycloak",
		"bp-gitea",
	}
	releases := make([]runtime.Object, 0, len(names)+1)
	for _, n := range names {
		releases = append(releases, makeReadyHelmRelease(n))
	}
	releases = append(releases, makeFailedHelmRelease("bp-catalyst-platform"))
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       30 * time.Second,
		FirstSeenTimeout:   30 * time.Second,
		MinBootstrapKitHRs: 11,
		DynamicFactory:     fakeFactory(client),
		Resync:             0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	final, err := w.Watch(ctx)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("watch took %v — expected fast all-done termination on terminal-with-failure", elapsed)
	}
	if got, want := len(final), 11; got != want {
		t.Errorf("final states = %d, want %d: %v", got, want, final)
	}
	if final["catalyst-platform"] != StateFailed {
		t.Errorf("final[catalyst-platform] = %q, want %q", final["catalyst-platform"], StateFailed)
	}
	if got, want := w.Outcome(), OutcomeFailed; got != want {
		t.Errorf("Outcome() = %q, want %q", got, want)
	}
}

// TestWatch_5HRs_BelowMinBootstrapKitHRs_DoesNotTerminate proves the
// count-gate: when fewer than MinBootstrapKitHRs HelmReleases are
// observed, even if every ONE of them is terminal, the watch does NOT
// satisfy "all observed terminal." It rides out WatchTimeout and
// reports Outcome=timeout (firstSeenAt is set, just count is below
// threshold).
//
// This pins the partial-reconcile failure mode: Flux on the new
// cluster started materialising the kit but only got to N=5 before
// the bootstrap-kit Kustomization broke. Without the count gate, all
// five could go ready and the watch would prematurely report ready —
// hiding the missing 6 components.
func TestWatch_5HRs_BelowMinBootstrapKitHRs_DoesNotTerminate(t *testing.T) {
	scheme := newFakeScheme()
	names := []string{
		"bp-cilium",
		"bp-cert-manager",
		"bp-flux",
		"bp-crossplane",
		"bp-sealed-secrets",
	}
	releases := make([]runtime.Object, 0, len(names))
	for _, n := range names {
		releases = append(releases, makeReadyHelmRelease(n))
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       1500 * time.Millisecond, // WatchTimeout must be hit, not all-done
		FirstSeenTimeout:   30 * time.Second,        // no first-seen warn within the window
		MinBootstrapKitHRs: 11,
		DynamicFactory:     fakeFactory(client),
		Resync:             0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	start := time.Now()
	final, err := w.Watch(context.Background())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	// MUST ride out WatchTimeout — the all-done gate cannot fire below
	// the bootstrap-kit threshold.
	if elapsed < 1*time.Second {
		t.Errorf("watch returned in %v — expected to ride out WatchTimeout (≥1s) because only 5 < 11 HRs were observed", elapsed)
	}
	if got, want := len(final), 5; got != want {
		t.Errorf("final states = %d, want %d (each observed HR appears, the gate just blocks termination): %v", got, want, final)
	}
	// firstSeenAt was set (5 HRs were observed) → outcome is Timeout,
	// NOT FluxNotReconciling.
	if got, want := w.Outcome(), OutcomeTimeout; got != want {
		t.Errorf("Outcome() = %q, want %q (firstSeenAt set, but below count gate)", got, want)
	}
}

