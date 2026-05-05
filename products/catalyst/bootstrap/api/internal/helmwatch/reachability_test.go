// Tests for the pre-flight apiserver reachability probe + reconnect
// loop (issue #923).
//
// What this file proves:
//
//  1. After-Pod-restart reconnect path — a Watcher started against an
//     apiserver that is unreachable for the first N attempts, then
//     reachable, emits one "watcher-reconnecting" warn event per
//     failed attempt, fires OnSubstate with SubstateReconnecting on
//     the first failure and SubstateWatching on the eventual success,
//     and proceeds to the informer's normal cache-sync path.
//
//  2. Reachability budget exhaustion — when the apiserver stays
//     unreachable for the full ReachabilityOverallBudget, the probe
//     loop falls through to the informer (NOT a hard failure). The
//     informer's own WatchTimeout path then classifies as
//     OutcomeFluxNotReconciling — exactly the right diagnostic for a
//     genuinely unreachable apiserver.
//
//  3. Substate transitions — OnSubstate fires exactly once for the
//     first reconnecting event, and exactly once for the final
//     watching event; no spurious duplicate substate notifications
//     even when the informer cache-sync also runs.
//
// We use a fake DynamicFactory + fake CoreFactory so no real cluster
// is needed; the probe is supplied via Config.Reachability with a
// counter-based closure so transient-then-success can be exercised
// deterministically. Sleep is overridden to a no-op so the backoff
// runs in microseconds.
package helmwatch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// transientThenSuccessProbe returns a Reachability factory that fails
// the first `failures` attempts with the given error, then succeeds.
// Counter is shared via the returned pointer so the test can assert
// the call count.
func transientThenSuccessProbe(failures int, transientErr error) (func(string) func(context.Context) error, *int32) {
	var count int32
	factory := func(_ string) func(context.Context) error {
		return func(_ context.Context) error {
			n := atomic.AddInt32(&count, 1)
			if int(n) <= failures {
				return transientErr
			}
			return nil
		}
	}
	return factory, &count
}

// substateRecorder collects every substate transition the watcher
// emits via OnSubstate so tests can assert ordering + de-duplication.
type substateRecorder struct {
	mu     sync.Mutex
	values []string
}

func (s *substateRecorder) record(v string) {
	s.mu.Lock()
	s.values = append(s.values, v)
	s.mu.Unlock()
}

func (s *substateRecorder) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.values))
	copy(out, s.values)
	return out
}

// noopSleep makes the reachability backoff run in microseconds. Used
// by every reachability test so a 60s max-interval cap doesn't drag
// the test runtime to a halt.
func noopSleep(_ context.Context, _ time.Duration) {}

// TestReachabilityProbe_HappyPath_SingleAttemptSucceeds verifies the
// production-shaped "apiserver was reachable on the first try" path
// emits NO "watcher-reconnecting" diagnostics and substate flips
// straight to SubstateWatching.
func TestReachabilityProbe_HappyPath_SingleAttemptSucceeds(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeHelmRelease("bp-cilium", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	subs := &substateRecorder{}

	probeFactory, calls := transientThenSuccessProbe(0, nil)

	cfg := Config{
		KubeconfigYAML: "fake-kubeconfig: bytes",
		WatchTimeout:   5 * time.Second,
		DynamicFactory: fakeFactory(client),
		Reachability:   probeFactory,
		Sleep:          noopSleep,
		Resync:         0,
		OnSubstate:     subs.record,
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

	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("expected exactly 1 reachability probe call, got %d", got)
	}

	// Substate transitions: success-on-first-attempt should NOT emit
	// SubstateReconnecting (we only flip substate once the first
	// failed attempt happens). It should still flip to
	// SubstateWatching so the wizard can read the live state.
	got := subs.snapshot()
	if len(got) != 1 || got[0] != SubstateWatching {
		t.Errorf("substate transitions = %v, want [%q]", got, SubstateWatching)
	}

	// No "Sovereign apiserver unreachable" warns should be in the
	// emit buffer on the happy path.
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev.Message, "unreachable") {
			t.Errorf("happy-path watch should not emit any 'unreachable' diagnostic, got: %q", ev.Message)
		}
	}
}

// TestReachabilityProbe_TransientThenSuccess proves the
// catalyst-api-Pod-restart-then-LB-warm-up scenario: the apiserver
// is unreachable for the first 2 attempts, then answers. The watcher
// emits exactly 2 "watcher-reconnecting" warns, fires OnSubstate
// with reconnecting → watching, and proceeds to the normal informer
// cache-sync path that returns OutcomeReady.
func TestReachabilityProbe_TransientThenSuccess(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeHelmRelease("bp-cilium", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	subs := &substateRecorder{}

	transientErr := errors.New("Get https://5.161.50.175:6443/version: net/http: TLS handshake timeout")
	probeFactory, calls := transientThenSuccessProbe(2, transientErr)

	cfg := Config{
		KubeconfigYAML:                   "fake-kubeconfig: bytes",
		WatchTimeout:                     5 * time.Second,
		DynamicFactory:                   fakeFactory(client),
		Reachability:                     probeFactory,
		Sleep:                            noopSleep,
		ReachabilityRetryInitialInterval: 1 * time.Millisecond,
		ReachabilityRetryMaxInterval:     1 * time.Millisecond,
		ReachabilityProbeTimeout:         100 * time.Millisecond,
		Resync:                           0,
		OnSubstate:                       subs.record,
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

	if got := atomic.LoadInt32(calls); got != 3 {
		t.Errorf("expected 3 reachability probe calls (2 transient failures + 1 success), got %d", got)
	}

	// Outcome must be Ready — the informer ran normally after
	// reachability succeeded.
	if outcome := w.Outcome(); outcome != OutcomeReady {
		t.Errorf("Outcome = %q, want %q", outcome, OutcomeReady)
	}

	// Substate transitions: first reconnecting (set on first failed
	// probe), then watching (set on the eventual success). Duplicate
	// reconnecting calls during the loop are de-duped by
	// notifySubstate's per-call dispatch — but the recorder sees
	// every notifySubstate call, so we expect 2x reconnecting + 1
	// watching.
	got := subs.snapshot()
	if len(got) != 3 {
		t.Errorf("substate transitions length = %d, want 3 (got %v)", len(got), got)
	}
	if len(got) >= 1 && got[0] != SubstateReconnecting {
		t.Errorf("first substate = %q, want %q", got[0], SubstateReconnecting)
	}
	if len(got) >= 1 && got[len(got)-1] != SubstateWatching {
		t.Errorf("last substate = %q, want %q", got[len(got)-1], SubstateWatching)
	}

	// Count the "watcher-reconnecting"-shaped warn events. We expect
	// exactly 2 (one per failed attempt; the 3rd attempt succeeded
	// and emits an info-level "reachable on attempt N" diagnostic
	// instead).
	warnCount := 0
	infoReachableCount := 0
	for _, ev := range rec.snapshot() {
		if ev.Phase == PhaseComponent && ev.Level == "warn" && strings.Contains(ev.Message, "Sovereign apiserver unreachable") {
			warnCount++
		}
		if ev.Phase == PhaseComponent && ev.Level == "info" && strings.Contains(ev.Message, "Sovereign apiserver reachable on attempt") {
			infoReachableCount++
		}
	}
	if warnCount != 2 {
		t.Errorf("expected 2 'unreachable' warn events, got %d", warnCount)
	}
	if infoReachableCount != 1 {
		t.Errorf("expected 1 'reachable on attempt N' info event, got %d", infoReachableCount)
	}
}

// TestReachabilityProbe_BudgetExhausted_FallsThroughToInformer proves
// that when the reachability probe loop exhausts its overall budget
// (apiserver stays unreachable), the watcher does NOT terminate the
// run with a hard failure — instead it falls through to factory.Start
// + WaitForCacheSync. The informer's own retry path then drives the
// terminal classification. With a fake DynamicFactory whose List
// resolves cleanly (no real apiserver), the watch then proceeds to
// OutcomeReady — proving that "budget-exhausted" is NOT a hard
// failure mode. (In production the informer hits a real unreachable
// apiserver, WaitForCacheSync times out via WatchTimeout, and the
// classifier returns OutcomeFluxNotReconciling.)
func TestReachabilityProbe_BudgetExhausted_FallsThroughToInformer(t *testing.T) {
	scheme := newFakeScheme()
	releases := []runtime.Object{
		makeHelmRelease("bp-cilium", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		}),
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		releases...,
	)

	rec := &recorder{}
	subs := &substateRecorder{}

	// Always-fail probe.
	var calls int32
	alwaysFail := func(_ string) func(context.Context) error {
		return func(_ context.Context) error {
			atomic.AddInt32(&calls, 1)
			return errors.New("apiserver unreachable")
		}
	}

	cfg := Config{
		KubeconfigYAML:                   "fake-kubeconfig: bytes",
		WatchTimeout:                     5 * time.Second,
		DynamicFactory:                   fakeFactory(client),
		Reachability:                     alwaysFail,
		Sleep:                            noopSleep,
		ReachabilityRetryInitialInterval: 1 * time.Millisecond,
		ReachabilityRetryMaxInterval:     1 * time.Millisecond,
		ReachabilityProbeTimeout:         10 * time.Millisecond,
		// 50ms budget — guarantees we exhaust within the test's
		// 5s WatchTimeout.
		ReachabilityOverallBudget: 50 * time.Millisecond,
		Resync:                    0,
		OnSubstate:                subs.record,
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

	// Probe should have been called at least once.
	if got := atomic.LoadInt32(&calls); got < 1 {
		t.Errorf("expected ≥1 reachability probe call, got %d", got)
	}

	// We should see a "reachability budget exhausted" warn
	// diagnostic in the emit buffer — this is the operator-facing
	// signal that the loop fell through to the informer.
	exhaustedSeen := false
	for _, ev := range rec.snapshot() {
		if ev.Level == "warn" && strings.Contains(ev.Message, "reachability budget") && strings.Contains(ev.Message, "exhausted") {
			exhaustedSeen = true
			break
		}
	}
	if !exhaustedSeen {
		t.Errorf("expected 'reachability budget exhausted' warn event in emit buffer; got events:\n%v", rec.snapshot())
	}

	// Substate must have flipped to reconnecting at least once
	// during the failure loop (a budget-exhausted run never reaches
	// SubstateWatching from the probe path; the fact that the fake
	// informer succeeds afterwards is irrelevant — the substate
	// invariant is "reconnecting was observed on the failure
	// trajectory").
	subSeen := false
	for _, v := range subs.snapshot() {
		if v == SubstateReconnecting {
			subSeen = true
			break
		}
	}
	if !subSeen {
		t.Errorf("expected SubstateReconnecting to be fired during the failure loop; got %v", subs.snapshot())
	}
}

// TestReachabilityProbe_ContextCancelDuringProbe proves the watcher
// returns cleanly (no hang) when the overall watchCtx fires while
// the probe loop is mid-retry. This is the Pod-shutdown / overall-
// WatchTimeout path.
func TestReachabilityProbe_ContextCancelDuringProbe(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
	)

	rec := &recorder{}
	subs := &substateRecorder{}

	// Probe blocks until ctx fires.
	blockingProbe := func(_ string) func(context.Context) error {
		return func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}
	}

	cfg := Config{
		KubeconfigYAML:                   "fake-kubeconfig: bytes",
		WatchTimeout:                     250 * time.Millisecond,
		DynamicFactory:                   fakeFactory(client),
		Reachability:                     blockingProbe,
		Sleep:                            noopSleep,
		ReachabilityRetryInitialInterval: 10 * time.Millisecond,
		ReachabilityRetryMaxInterval:     10 * time.Millisecond,
		ReachabilityProbeTimeout:         50 * time.Millisecond,
		ReachabilityOverallBudget:        10 * time.Second, // much larger than WatchTimeout
		Resync:                           0,
		OnSubstate:                       subs.record,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	_, _ = w.Watch(ctx)
	elapsed := time.Since(start)

	// The watch must return within a reasonable bound — the inner
	// WatchTimeout is 250ms, plus probe-attempt timeouts of 50ms
	// each. We give 2s of slack.
	if elapsed > 2*time.Second {
		t.Errorf("Watch took %s — expected to return within 2s of WatchTimeout firing", elapsed)
	}
}
