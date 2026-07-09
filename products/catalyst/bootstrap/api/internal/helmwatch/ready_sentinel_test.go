// Tests for the #4746 ready-sentinel census gate.
//
// Root cause the sentinel closes: the terminate-on-all-done gate fires
// OutcomeReady as soon as the informer's initial List catches a NARROW
// set of early bp-* HelmReleases (e.g. only bp-cilium) all Ready=True and
// the MinBootstrapKitHRs floor (1) is met — long before bp-catalyst-platform
// (the console's own backend) has even been created by Flux. On a slow
// 2-region prov that premature OutcomeReady then ran the #4706 external
// console-reachability probe against a console whose backend had not begun
// installing, so a healthy, still-converging Sovereign was stamped
// status=failed.
//
// The sentinel (Config.ReadySentinelComponent = "catalyst-platform" in
// production) makes allObservedTerminal refuse OutcomeReady until the
// console backend has been OBSERVED and reached a terminal state. These
// tests prove the three behaviours that matter:
//
//   1. slow-but-healthy 2-region: an early HR is Ready at sync but the
//      sentinel appears LATER — the watch must DEFER ready until the
//      sentinel installs, and then classify OutcomeReady (no false-fail).
//   2. genuinely-broken (never installs): the sentinel never appears — the
//      watch must NOT false-ready; it runs to WatchTimeout → OutcomeTimeout
//      (which the handler maps to status=failed).
//   3. genuinely-broken (install fails): the sentinel reaches StateFailed —
//      the watch must classify OutcomeFailed, never OutcomeReady.
//
// The companion control assertion in test (1) proves the gate is
// load-bearing: with an EMPTY sentinel the exact same early-only fixture
// fires the historical premature OutcomeReady.
package helmwatch

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// createHR adds a NEW HelmRelease to the fake dynamic client AFTER the
// informer has started, so a test can model Flux materialising the
// console backend late in the reconcile. Distinct from updateHR, which
// patches an object that already exists in the initial List.
func createHR(t *testing.T, client dynamic.Interface, name string, conds []metav1.Condition) {
	t.Helper()
	_, err := client.Resource(HelmReleaseGVR).Namespace(FluxNamespace).Create(
		t.Context(), makeHelmRelease(name, conds), metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("createHR(%q): %v", name, err)
	}
}

// TestWatch_ReadySentinel_SlowConsoleBackend_DefersReadyUntilInstalled is
// the #4746 slow-but-healthy 2-region case. bp-cilium is Ready=True at
// informer-sync time, but the console backend (bp-catalyst-platform) is
// not in the cache yet; it is created Ready a moment later. With the
// sentinel armed the watch must NOT fire the historical premature ready —
// it must wait for the sentinel and then classify OutcomeReady, with the
// final state map carrying BOTH components.
func TestWatch_ReadySentinel_SlowConsoleBackend_DefersReadyUntilInstalled(t *testing.T) {
	scheme := newFakeScheme()
	// Only the early HR exists at watch-start — the console backend has
	// not been created by Flux yet (the 2-region timing window).
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:         "fake",
		WatchTimeout:           30 * time.Second, // must NOT be hit
		FirstSeenTimeout:       30 * time.Second,
		MinBootstrapKitHRs:     1,
		DynamicFactory:         fakeFactory(client),
		Resync:                 0,
		ReadySentinelComponent: "catalyst-platform",
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// The console backend converges 200ms after start — well after the
	// informer has synced with only bp-cilium. With the sentinel armed the
	// watch is BLOCKED in its select loop until this arrives, so there is
	// no race: it cannot have fired ready on the narrow early set.
	go func() {
		time.Sleep(200 * time.Millisecond)
		createHR(t, client, "bp-catalyst-platform", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	final, err := w.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got, want := w.Outcome(), OutcomeReady; got != want {
		t.Errorf("Outcome() = %q, want %q (healthy slow-console prov must converge ready, not false-fail)", got, want)
	}
	// The load-bearing proof that ready was DEFERRED: the final state map
	// must contain the sentinel installed. A premature ready on the narrow
	// early set would have terminated with only bp-cilium (len==1).
	if got, want := len(final), 2; got != want {
		t.Fatalf("final states = %d (%v), want %d — ready must have waited for the console backend, not fired on the early set", got, final, want)
	}
	if final["catalyst-platform"] != StateInstalled {
		t.Errorf("final[catalyst-platform] = %q, want %q", final["catalyst-platform"], StateInstalled)
	}

	// Control: the SAME early-only fixture with an EMPTY sentinel fires the
	// historical premature ready on bp-cilium alone (len==1). This proves
	// the sentinel — not some other change — is what defers the gate.
	t.Run("control_empty_sentinel_fires_early", func(t *testing.T) {
		cscheme := newFakeScheme()
		cclient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(cscheme,
			map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
			makeReadyHelmRelease("bp-cilium"),
		)
		crec := &recorder{}
		cw, err := NewWatcher(Config{
			KubeconfigYAML:     "fake",
			WatchTimeout:       5 * time.Second,
			FirstSeenTimeout:   5 * time.Second,
			MinBootstrapKitHRs: 1,
			DynamicFactory:     fakeFactory(cclient),
			Resync:             0,
			// ReadySentinelComponent empty — historical narrow census.
		}, crec.emit)
		if err != nil {
			t.Fatalf("NewWatcher: %v", err)
		}
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		cfinal, err := cw.Watch(cctx)
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		if got, want := cw.Outcome(), OutcomeReady; got != want {
			t.Errorf("control Outcome() = %q, want %q", got, want)
		}
		if got, want := len(cfinal), 1; got != want {
			t.Errorf("control final states = %d, want %d (premature ready on the early set is the historical behaviour the sentinel closes)", got, want)
		}
	})
}

// TestWatch_ReadySentinel_ConsoleBackendNeverInstalls_TimesOutNotReady is
// the genuinely-broken case that MUST still fail: bp-cilium is Ready but
// the console backend never appears. The sentinel must keep the gate shut
// so the watch runs to WatchTimeout → OutcomeTimeout (handler → failed),
// never a false OutcomeReady. This is the direct contrapositive of the
// fix — the sentinel does not mask a real failure.
func TestWatch_ReadySentinel_ConsoleBackendNeverInstalls_TimesOutNotReady(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:         "fake",
		WatchTimeout:           400 * time.Millisecond, // console backend never comes; time out fast
		FirstSeenTimeout:       10 * time.Second,
		MinBootstrapKitHRs:     1,
		DynamicFactory:         fakeFactory(client),
		Resync:                 0,
		ReadySentinelComponent: "catalyst-platform",
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	final, err := w.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// firstSeenAt is set (bp-cilium was observed) so this is a TIMEOUT, not
	// flux-not-reconciling — and crucially NOT ready.
	if got := w.Outcome(); got == OutcomeReady {
		t.Fatalf("Outcome() = %q — a prov whose console backend never installs must NEVER be classified ready", got)
	}
	if got, want := w.Outcome(), OutcomeTimeout; got != want {
		t.Errorf("Outcome() = %q, want %q (early HR observed, sentinel absent → timeout, handler maps to failed)", got, want)
	}
	if final["cilium"] != StateInstalled {
		t.Errorf("final[cilium] = %q, want %q (partial state preserved)", final["cilium"], StateInstalled)
	}
}

// TestWatch_ReadySentinel_ConsoleBackendFails_ClassifiesFailed proves a
// sentinel that reaches StateFailed classifies OutcomeFailed — a broken
// console surfaces a failure rather than hanging to WatchTimeout. The
// tiny LatePollTimeout exercises the failed classification in
// milliseconds (production gives Flux a 10-minute recovery window).
func TestWatch_ReadySentinel_ConsoleBackendFails_ClassifiesFailed(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
		makeFailedHelmRelease("bp-catalyst-platform"),
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:         "fake",
		WatchTimeout:           30 * time.Second,
		FirstSeenTimeout:       30 * time.Second,
		MinBootstrapKitHRs:     2,
		DynamicFactory:         fakeFactory(client),
		Resync:                 0,
		LatePollTimeout:        200 * time.Millisecond,
		LatePollInterval:       25 * time.Millisecond,
		ReadySentinelComponent: "catalyst-platform",
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	final, err := w.Watch(context.Background())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got, want := w.Outcome(), OutcomeFailed; got != want {
		t.Errorf("Outcome() = %q, want %q (failed console backend must classify failed, not ready)", got, want)
	}
	if final["catalyst-platform"] != StateFailed {
		t.Errorf("final[catalyst-platform] = %q, want %q", final["catalyst-platform"], StateFailed)
	}
}
