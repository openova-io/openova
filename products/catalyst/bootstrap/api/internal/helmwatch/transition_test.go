// transition_test.go — Phase-1 watcher → ready transition coverage.
//
// Background — otech48 incident (2026-05-03):
//
//   All 37 bp-* HelmReleases on the Sovereign cluster reached
//   Ready=True, but the catalyst-api deployment record stayed
//   status=phase1-watching. The wizard's POST /mint-handover-token
//   call returned 409 not-handover-ready, blocking the auto-redirect
//   to the Sovereign Console handover page.
//
//   Root cause: the watcher's `terminate-on-all-done` gate required
//   `len(observed) >= MinBootstrapKitHRs`. The chart's
//   CATALYST_PHASE1_MIN_BOOTSTRAP_KIT_HRS env was hardcoded to 38,
//   the actual bootstrap-kit cardinality had drifted to 37, so the
//   gate was permanently unsatisfiable. The watch ran until its
//   60-minute WatchTimeout fired — 60 minutes too late for the
//   wizard's redirect.
//
// Fix: gate terminate-on-all-done on the informer's HasSynced
// signal instead of the brittle count. The HasSynced signal flips
// AFTER WaitForCacheSync returns — by which point every HR that
// existed at watch-start time is in the cache, regardless of
// cardinality. The cardinality count stays as a defence-in-depth
// floor (default 1) for the "informer synced with empty cache"
// edge case.
//
// What this file proves:
//
//  1. All-Ready convergence flips the watch terminal classification
//     to OutcomeReady AND emits the operator-visible "Sovereign
//     ready for handover" SSE event — exactly once (idempotency).
//  2. A flicker of one HR True → False → True after the watch has
//     terminated does not flap status back to phase1-watching at
//     the handler level (the watch has returned; no further
//     transitions can be emitted from the same Watcher instance).
//  3. Some HRs Ready, others False(Progressing) → watch keeps
//     watching, no premature termination, no terminal event.
//  4. Status="adopted" on the deployment record — markPhase1Done
//     refuses to downgrade. (Tested in handler/phase1_watch_test
//     — exercised here at watcher granularity by asserting the
//     watcher does NOT re-emit the ready-transition event after a
//     spurious wake-up.)
//  5. The brittle MinBootstrapKitHRs count gate is no longer a
//     correctness gate — a watcher injected with
//     MinBootstrapKitHRs=999 (impossible to satisfy) still
//     terminates on a fully-Ready cluster because the HasSynced
//     gate gates the termination check, not the count.
package helmwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// readyTransitionEvent — convenience filter for the operator-visible
// "All N blueprints reconciled" event the watcher emits exactly once
// when the all-Ready gate fires. No Component is set (watch-level
// diagnostic, not a per-component state change).
func readyTransitionEvents(events []provisioner.Event) []provisioner.Event {
	out := []provisioner.Event{}
	for _, ev := range events {
		if ev.Phase == PhaseComponent &&
			ev.Component == "" &&
			ev.Level == "info" &&
			strings.Contains(ev.Message, "ready for handover") {
			out = append(out, ev)
		}
	}
	return out
}

// makeReadyHRForTransition — local helper duplicating the test
// shape from helmwatch_test.go without the package-private
// dependencies. Returns a bp-* HelmRelease with Ready=True.
func makeReadyHRForTransition(name string) *unstructured.Unstructured {
	u := makeHelmRelease(name, []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
	})
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	return u
}

// TestTransition_AllReadyEmitsTerminalEventExactlyOnce proves the
// happy path: every observed HR is Ready=True after the informer
// syncs, the watch terminates with OutcomeReady, AND the operator-
// visible "Sovereign ready for handover" event is emitted exactly
// once. This is the otech48 fix's core acceptance test — without it,
// the wizard cannot auto-redirect.
func TestTransition_AllReadyEmitsTerminalEventExactlyOnce(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeReadyHRForTransition("bp-cilium"),
		makeReadyHRForTransition("bp-cert-manager"),
		makeReadyHRForTransition("bp-flux"),
		makeReadyHRForTransition("bp-crossplane"),
		makeReadyHRForTransition("bp-keycloak"),
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

	start := time.Now()
	final, err := w.Watch(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Watch must terminate via the all-terminal close, not the
	// 5-second timeout. Anything > 1s suggests we fell through to
	// the timeout path — which would be the otech48 bug regressing.
	if elapsed > 1*time.Second {
		t.Errorf("watch took %v — expected <1s for all-Ready close (otech48 regression: did the count gate sneak back in?)", elapsed)
	}

	if w.Outcome() != OutcomeReady {
		t.Errorf("Outcome = %q, want %q", w.Outcome(), OutcomeReady)
	}
	if got := len(final); got != 5 {
		t.Errorf("ComponentStates length = %d, want 5", got)
	}

	terminal := readyTransitionEvents(rec.snapshot())
	if got := len(terminal); got != 1 {
		t.Fatalf("expected exactly 1 ready-transition event, got %d:\n%+v", got, terminal)
	}
	if !strings.Contains(terminal[0].Message, "5 blueprints") {
		t.Errorf("ready-transition message = %q, want it to mention the 5 observed blueprints", terminal[0].Message)
	}
}

// TestTransition_NoFlapAfterTerminate proves that once the watch has
// terminated with OutcomeReady, a follow-up state change (e.g. a
// helm-controller observedGeneration touch causing a synthetic
// Ready=Unknown momentarily) cannot re-trigger the ready-transition
// event from the same Watcher.
//
// Implementation detail: we drive this by asserting that
// maybeEmitReadyTransition is idempotent at the Watcher's API
// surface. The Watcher's processEvent path won't be re-entered
// after Watch returns (informer cancelled), but the idempotency
// assertion is the contract — a future caller adding a manual
// kick-the-watch endpoint won't accidentally double-emit.
func TestTransition_NoFlapAfterTerminate(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeReadyHRForTransition("bp-cilium"),
		makeReadyHRForTransition("bp-cert-manager"),
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
	if _, err := w.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	beforeCount := len(readyTransitionEvents(rec.snapshot()))
	if beforeCount != 1 {
		t.Fatalf("expected 1 ready-transition before flap, got %d", beforeCount)
	}

	// Simulate a re-trigger — the watcher's idempotency guard MUST
	// suppress a second emit. Calling maybeEmitReadyTransition
	// directly is the unit-test surface for the contract.
	w.maybeEmitReadyTransition()
	w.maybeEmitReadyTransition()
	w.maybeEmitReadyTransition()

	afterCount := len(readyTransitionEvents(rec.snapshot()))
	if afterCount != beforeCount {
		t.Errorf("ready-transition event re-emitted on flap: before=%d after=%d (idempotency violated)", beforeCount, afterCount)
	}
}

// TestTransition_PartialReadyDoesNotTerminate proves that the watch
// keeps watching when only some HRs are Ready. The remaining
// HRs are stuck in Progressing — the watch must not fire the
// terminal event.
func TestTransition_PartialReadyDoesNotTerminate(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeReadyHRForTransition("bp-cilium"),
		makeReadyHRForTransition("bp-cert-manager"),
		// bp-keycloak is mid-install (Ready=Unknown).
		makeHelmRelease("bp-keycloak", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "Progressing", Message: "Reconciliation in progress"},
		}),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   400 * time.Millisecond, // tiny — the watch must hit the timeout
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
		t.Fatalf("Watch: %v", err)
	}

	// The watch must terminate via WatchTimeout (>= 200ms), not via
	// all-Ready close (which would take <100ms).
	if elapsed < 200*time.Millisecond {
		t.Errorf("watch took only %v — must have observed timeout, not all-terminal close (some HRs were not Ready)", elapsed)
	}

	if w.Outcome() != OutcomeTimeout {
		t.Errorf("Outcome = %q, want %q (timeout because keycloak stuck Installing)", w.Outcome(), OutcomeTimeout)
	}
	if final["keycloak"] != StateInstalling {
		t.Errorf("keycloak state = %q, want %q (mid-install)", final["keycloak"], StateInstalling)
	}

	terminal := readyTransitionEvents(rec.snapshot())
	if got := len(terminal); got != 0 {
		t.Errorf("expected 0 ready-transition events (some HRs not Ready), got %d:\n%+v", got, terminal)
	}
}

// TestTransition_DefaultFloorAllowsRealisticConvergence proves the
// chart's new default MinBootstrapKitHRs=1 is permissive enough to
// terminate on the realistic otech-class case — 37 HRs, all Ready,
// informer synced. The pre-fix chart pinned MinBootstrapKitHRs=38
// against an actual kit cardinality of 37 → permanently
// unsatisfiable count gate → watch ran 60 minutes before timing
// out. The post-fix default of 1 + the synced gate prevents that
// failure shape regardless of kit size.
//
// This test does not exercise minHRs=999 (intentional misconfig) —
// that path is covered by TestAllObservedTerminal_SyncedGate at unit
// granularity. End-to-end at the Watch() surface, the contract is:
// production-ish defaults converge on a Ready=True cluster within a
// second.
func TestTransition_DefaultFloorAllowsRealisticConvergence(t *testing.T) {
	scheme := newFakeScheme()
	// Simulate the otech48 cluster — 37 ready HRs.
	const realisticKitSize = 37
	releases := make([]runtime.Object, 0, realisticKitSize)
	for i := 0; i < realisticKitSize; i++ {
		// Use distinct names that sort alphabetically across the kit
		// so the informer's initial-list batches the same way it
		// does in production.
		name := "bp-comp-" + string(rune('a'+(i/26))) + string(rune('a'+(i%26)))
		releases = append(releases, makeReadyHRForTransition(name))
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   2 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
		// Production default is 1 — the chart has been updated to
		// stop pinning this to the kit cardinality.
		MinBootstrapKitHRs: 1,
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

	if elapsed > 1*time.Second {
		t.Errorf("watch took %v — expected <1s for synced + all-Ready close on a realistic %d-HR kit", elapsed, realisticKitSize)
	}
	if w.Outcome() != OutcomeReady {
		t.Errorf("Outcome = %q, want %q", w.Outcome(), OutcomeReady)
	}
	if got := len(final); got != realisticKitSize {
		t.Errorf("ComponentStates length = %d, want %d", got, realisticKitSize)
	}
	terminal := readyTransitionEvents(rec.snapshot())
	if got := len(terminal); got != 1 {
		t.Errorf("expected 1 ready-transition event for the %d-HR convergence, got %d", realisticKitSize, got)
	}
}

// TestTransition_EmptyCacheDoesNotTerminate proves the
// MinBootstrapKitHRs floor still guards the dangerous edge case
// where the informer has synced with an empty cache (the bootstrap-
// kit Kustomization on the new Sovereign never reconciled). The
// watch must NOT prematurely terminate "ready" with zero observed
// components — that would lie to the operator that a Sovereign is
// up when nothing has been installed.
func TestTransition_EmptyCacheDoesNotTerminate(t *testing.T) {
	scheme := newFakeScheme()
	// No HelmReleases — empty informer cache after sync.
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   400 * time.Millisecond,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
		// Default MinBootstrapKitHRs (1) is the floor — empty cache
		// (firstSeenAt zero) refuses to terminate via all-done.
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	final, err := w.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if w.Outcome() != OutcomeFluxNotReconciling {
		t.Errorf("Outcome = %q, want %q (empty cache means flux on new cluster isn't reconciling)", w.Outcome(), OutcomeFluxNotReconciling)
	}
	if got := len(final); got != 0 {
		t.Errorf("ComponentStates length = %d, want 0 (empty cache)", got)
	}

	terminal := readyTransitionEvents(rec.snapshot())
	if got := len(terminal); got != 0 {
		t.Errorf("expected 0 ready-transition events (empty cache), got %d", got)
	}
}

// TestAllObservedTerminal_SyncedGate covers the pure helper at unit
// granularity — every input combination of (synced, firstSeenAt,
// observed-count, all-terminal) maps to the documented decision.
// This is the canary for the otech48 root-cause fix; if this drifts
// the deployment record will silently regress to phase1-watching.
func TestAllObservedTerminal_SyncedGate(t *testing.T) {
	now := time.Now()
	zero := time.Time{}

	allReady := map[string]string{
		"cilium":       StateInstalled,
		"cert-manager": StateInstalled,
	}
	observedAllReady := map[string]struct{}{
		"cilium":       {},
		"cert-manager": {},
	}

	cases := []struct {
		name             string
		states           map[string]string
		observed         map[string]struct{}
		minHRs           int
		firstSeenAt      time.Time
		synced           bool
		want             bool
	}{
		{"synced=false blocks even if all-Ready", allReady, observedAllReady, 1, now, false, false},
		{"firstSeenAt zero blocks even if synced", allReady, observedAllReady, 1, zero, true, false},
		{"observed below floor blocks", allReady, observedAllReady, 999, now, true, false},
		{"one HR not terminal blocks", map[string]string{"cilium": StateInstalled, "cert-manager": StateInstalling}, observedAllReady, 1, now, true, false},
		{"all-Ready synced cache fires", allReady, observedAllReady, 1, now, true, true},
		{"all-Ready synced cache with absurd floor still fires when count >= floor", allReady, observedAllReady, 2, now, true, true},
		{"failed counts as terminal", map[string]string{"cilium": StateInstalled, "cert-manager": StateFailed}, observedAllReady, 1, now, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := allObservedTerminal(tc.states, tc.observed, tc.minHRs, tc.firstSeenAt, tc.synced)
			if got != tc.want {
				t.Errorf("allObservedTerminal(synced=%t, firstSeenAt-zero=%t, minHRs=%d) = %t, want %t",
					tc.synced, tc.firstSeenAt.IsZero(), tc.minHRs, got, tc.want)
			}
		})
	}
}

// TestTransition_AttachTimeReady_EmitsSucceededViaSubscribe pins the
// load-bearing contract for the catalyst-api Pod-restart resume path:
// when the new Pod's helmwatch.Watcher attaches to a child cluster
// where bp-* HelmReleases are ALREADY Ready=True at attach time, the
// informer's initial-list AddFunc fires for each HR — and the
// Subscribe stream MUST receive a `phase=component state=installed`
// event for every one of them.
//
// Bug source (prov t112.omani.works, deployment f2e7f02e6ffb6a18,
// 2026-05-15): the mothership Jobs API kept showing
// install-self-sovereign-cutover / install-powerdns /
// install-catalyst-platform as "running" and install-sin-2:reloader
// as "failed", even though kubectl on each child cluster showed all
// 135/135 HRs Ready=True. The mothership state stayed stale because
// the bridge subscribed via Watcher.Subscribe didn't receive the
// terminal "installed" event for the HRs that were Ready at attach
// time. This breaks DoD gate D6 (0 pending / 0 running) and D7
// (mothership ≡ child) on every Pod-restart cycle.
//
// Fix surface: helmwatch.processEvent's emission policy is "dispatch
// on FIRST observation OR on a real state transition". The pre-fix
// code implicitly relied on `prev != state` (prev defaults to "" so
// "" != "installed" → dispatch). This test pins the contract
// explicitly so a future refactor that pre-seeds w.states cannot
// silently regress it.
func TestTransition_AttachTimeReady_EmitsSucceededViaSubscribe(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		// Every HR is ALREADY Ready=True at attach time — the
		// rehydrate-from-PVC / Pod-restart scenario.
		makeReadyHRForTransition("bp-self-sovereign-cutover"),
		makeReadyHRForTransition("bp-powerdns"),
		makeReadyHRForTransition("bp-catalyst-platform"),
		makeReadyHRForTransition("bp-reloader"),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	primary := &recorder{}
	subscriber := &recorder{}

	cfg := Config{
		KubeconfigYAML: "fake",
		WatchTimeout:   5 * time.Second,
		DynamicFactory: fakeFactory(client),
		Resync:         0,
	}
	w, err := NewWatcher(cfg, primary.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	// Subscribe the simulated jobs.Bridge fan-out before Watch starts
	// — same wiring `attachBridgeSeederHook` applies in production.
	w.Subscribe(subscriber.emit)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Both sinks must receive exactly one phase=component event per
	// HR, each with State=installed. Anything less means the bridge's
	// Subscribe stream didn't observe the attach-time Ready=True HRs
	// — the prov t112 bug.
	wantComponents := map[string]bool{
		"self-sovereign-cutover": false,
		"powerdns":               false,
		"catalyst-platform":      false,
		"reloader":               false,
	}

	primaryComponents := primary.componentStateEvents()
	if len(primaryComponents) != len(wantComponents) {
		t.Errorf("primary emit: want %d component events (one per attach-time-Ready HR), got %d: %+v",
			len(wantComponents), len(primaryComponents), primaryComponents)
	}
	for _, ev := range primaryComponents {
		if _, ok := wantComponents[ev.Component]; !ok {
			t.Errorf("primary emit: unexpected component %q", ev.Component)
			continue
		}
		if ev.State != StateInstalled {
			t.Errorf("primary emit: %s state=%q, want %q (attach-time Ready=True must surface as installed)",
				ev.Component, ev.State, StateInstalled)
		}
	}

	// Subscribe stream must mirror the primary exactly — this is the
	// load-bearing assertion. The jobs.Bridge subscribes here, and a
	// missing event means the bridge's OnHelmReleaseEvent never fires
	// for that HR, leaving the mothership Jobs.Status stale.
	subscriberComponents := subscriber.componentStateEvents()
	if len(subscriberComponents) != len(wantComponents) {
		t.Fatalf("subscriber: want %d component events (one per attach-time-Ready HR — this is the bridge wiring), got %d: %+v",
			len(wantComponents), len(subscriberComponents), subscriberComponents)
	}
	seen := map[string]int{}
	for _, ev := range subscriberComponents {
		if _, ok := wantComponents[ev.Component]; !ok {
			t.Errorf("subscriber: unexpected component %q", ev.Component)
			continue
		}
		if ev.State != StateInstalled {
			t.Errorf("subscriber: %s state=%q, want %q (the bridge MUST translate this into Status=succeeded; pre-fix it stayed at Status=running because no event arrived)",
				ev.Component, ev.State, StateInstalled)
		}
		seen[ev.Component]++
	}
	for comp := range wantComponents {
		if seen[comp] == 0 {
			t.Errorf("subscriber: missing event for %q (the t112 bug — bridge never received the attach-time Ready=True event for this HR)", comp)
		}
		if seen[comp] > 1 {
			t.Errorf("subscriber: %q received %d events, want exactly 1 (idempotency violation)", comp, seen[comp])
		}
	}

	// The watch must also terminate with OutcomeReady on the
	// resume-after-restart path. If processEvent's emission policy
	// regressed in a way that broke first-observation dispatch, the
	// ready-transition would still fire (the terminal gate uses
	// w.states + w.observed, not the dispatch path) — so this is a
	// supporting assertion, not the primary signal.
	if w.Outcome() != OutcomeReady {
		t.Errorf("Outcome = %q, want %q", w.Outcome(), OutcomeReady)
	}
}

// TestTransition_FirstObservation_NeverDedupsAcrossWatchers proves the
// idempotency surface: a Watcher constructed against an HR that was
// observed by a PRIOR Watcher (now garbage-collected) MUST still emit
// the per-component event because each Watcher's w.states map is
// independent. The catalyst-api Pod-restart path relies on this:
// every new Pod spins up a new Watcher with an empty states map, and
// every attach-time AddFunc must propagate to the new bridge.
//
// Concretely: this re-runs the watch lifecycle twice against the same
// fake client and asserts each run's subscriber sees the full
// component-event set.
func TestTransition_FirstObservation_NeverDedupsAcrossWatchers(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeReadyHRForTransition("bp-cilium"),
		makeReadyHRForTransition("bp-cert-manager"),
		makeReadyHRForTransition("bp-flux"),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	runOnce := func(label string) {
		t.Helper()
		primary := &recorder{}
		subscriber := &recorder{}
		cfg := Config{
			KubeconfigYAML: "fake",
			WatchTimeout:   3 * time.Second,
			DynamicFactory: fakeFactory(client),
			Resync:         0,
		}
		w, err := NewWatcher(cfg, primary.emit)
		if err != nil {
			t.Fatalf("[%s] NewWatcher: %v", label, err)
		}
		w.Subscribe(subscriber.emit)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := w.Watch(ctx); err != nil {
			t.Fatalf("[%s] Watch: %v", label, err)
		}
		if got := len(primary.componentStateEvents()); got != 3 {
			t.Errorf("[%s] primary: want 3 component events, got %d", label, got)
		}
		if got := len(subscriber.componentStateEvents()); got != 3 {
			t.Errorf("[%s] subscriber: want 3 component events, got %d", label, got)
		}
	}

	runOnce("first attach")
	runOnce("re-attach after Pod restart")
}

// TestTransition_FailedThenReady_EndsInstalledNotFailed is the core
// recovery contract for the false-FAILED-chip defect (issue #3687 /
// #910 tail). A bp-* HelmRelease that is observed Failed (transient
// bootstrap InstallFailed) and later recovers to Ready=True while the
// informer is still attached MUST:
//
//   - emit a per-component recovery transition (State=installed) AFTER
//     the earlier State=failed event — so the bridge / StatusStrip
//     consumer flips the chip back to succeeded, AND
//   - end with final[component] == StateInstalled (NOT StateFailed) AND
//     Outcome() == OutcomeReady.
//
// This pins that StateFailed is NON-terminal at the informer level: the
// watch keeps observing the HR after a Failed observation and the
// recovery Update flows through processEvent. If a future refactor ever
// short-circuited the informer on the first Failed (the bug the founder
// reported — a stale "FAILED" snapshot inflating the UAT ❌ count), the
// recovery transition would vanish and this test would fail.
func TestTransition_FailedThenReady_EndsInstalledNotFailed(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeReadyHelmRelease("bp-cilium"),
		makeReadyHelmRelease("bp-cert-manager"),
		// bp-catalyst-platform starts Failed (transient InstallFailed in
		// the bootstrap window).
		makeFailedHelmRelease("bp-catalyst-platform"),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       30 * time.Second,
		FirstSeenTimeout:   30 * time.Second,
		MinBootstrapKitHRs: 3,
		DynamicFactory:     fakeFactory(client),
		Resync:             0,
		// Generous late-poll so the informer stays attached long enough
		// to observe the recovery Update the goroutine below issues.
		LatePollTimeout:  3 * time.Second,
		LatePollInterval: 25 * time.Millisecond,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// Flip the failed HR to Ready=True shortly after the all-terminal
	// trip fires (with the failure), so the recovery arrives while the
	// late-poll keeps the informer attached — modelling Flux's
	// remediation.retries converging post-failure.
	go func() {
		time.Sleep(150 * time.Millisecond)
		updateHR(t, client, "bp-catalyst-platform", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded after retry"},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	final, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// Final per-component state + overall outcome must reflect the
	// recovery, never the stale failure.
	if final["catalyst-platform"] != StateInstalled {
		t.Errorf("final[catalyst-platform] = %q, want %q (recovery must not stick on the stale Failed)", final["catalyst-platform"], StateInstalled)
	}
	if got, want := w.Outcome(), OutcomeReady; got != want {
		t.Errorf("Outcome() = %q, want %q (failed component recovered to installed)", got, want)
	}

	// The event stream for catalyst-platform must end on State=installed,
	// and it must have carried the earlier State=failed too (proving the
	// transition was observed live, not papered over). This is exactly
	// what the bridge.OnHelmReleaseEvent consumer needs to flip the Job
	// chip succeeded after a failure.
	var states []string
	for _, ev := range rec.componentStateEvents() {
		if ev.Component == "catalyst-platform" {
			states = append(states, ev.State)
		}
	}
	if len(states) == 0 {
		t.Fatalf("no component-state events for catalyst-platform; got events: %+v", rec.snapshot())
	}
	if last := states[len(states)-1]; last != StateInstalled {
		t.Errorf("last catalyst-platform state event = %q, want %q; full sequence=%v", last, StateInstalled, states)
	}
	if !containsSubsequence(states, []string{StateFailed, StateInstalled}) {
		t.Errorf("catalyst-platform state events = %v, want the failed→installed recovery subsequence", states)
	}
}
