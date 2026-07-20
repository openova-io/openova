// Progress-guard coverage (#5283).
//
// hw279 (dep 059126bb, 2026-07-20): a fresh 2-region Sovereign was
// converging HEALTHILY — the mothership phase1-watch heartbeat logged
// observedHRs:66, readyHRs:58, failedHRs:0, sentinel:pending (genuine
// forward progress, zero failed HelmReleases). An intermittent external
// xpkg.upbound.io 503 merely DELAYED the bp-crossplane →
// bp-crossplane-claims → bp-catalyst-platform chain. Yet the fixed 120m
// WatchTimeout guillotine concluded OutcomeTimeout → finalStatus=failed,
// which is terminal (#5253) and permanently suppressed the mesh/cutover
// cascade — killing a recoverable, still-progressing prov.
//
// The fix adds a progress-guard: past the SOFT WatchTimeout the watch
// concludes only on a genuine stall (readyHRs flat for ProgressStallWindow
// with no failed HR) or the absolute ProgressGuardCeiling; while readyHRs
// is still climbing it defers instead of hard-failing. These tests pin
// both sides: still-progressing must NOT conclude failed; a true stall
// still does.
package helmwatch

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// TestConvergenceProgressing_5283 exercises the progress decision directly
// (deterministic, no timers): forward progress within the window with no
// failure is "progressing"; a flat window, a failed HR, or never-installed
// are all "not progressing".
func TestConvergenceProgressing_5283(t *testing.T) {
	base := time.Date(2026, 7, 20, 2, 6, 0, 0, time.UTC)
	newW := func(states map[string]string, lastProgress time.Time) *Watcher {
		w, err := NewWatcher(Config{
			KubeconfigYAML:      "fake",
			ProgressStallWindow: 200 * time.Millisecond,
		}, (&recorder{}).emit)
		if err != nil {
			t.Fatalf("NewWatcher: %v", err)
		}
		w.states = states
		w.lastReadyProgressAt = lastProgress
		return w
	}

	t.Run("climbing within window, no failure → progressing", func(t *testing.T) {
		w := newW(map[string]string{"cilium": StateInstalled, "crossplane": StateInstalling}, base)
		if !w.convergenceProgressing(base.Add(50 * time.Millisecond)) {
			t.Fatal("readyHRs increased 50ms ago (< 200ms window) with no failure — must be progressing")
		}
	})

	t.Run("flat beyond window → stalled", func(t *testing.T) {
		w := newW(map[string]string{"cilium": StateInstalled, "crossplane": StateInstalling}, base)
		if w.convergenceProgressing(base.Add(300 * time.Millisecond)) {
			t.Fatal("readyHRs last rose 300ms ago (> 200ms window) — must be a stall, not progressing")
		}
	})

	t.Run("a failed HR present → not progressing (falls through to normal conclusion)", func(t *testing.T) {
		w := newW(map[string]string{"cilium": StateInstalled, "crossplane": StateFailed}, base)
		if w.convergenceProgressing(base.Add(10 * time.Millisecond)) {
			t.Fatal("a hard-FAILED HR is not clean forward progress — must not defer")
		}
	})

	t.Run("readyHRs never rose (all stuck installing) → not progressing", func(t *testing.T) {
		w := newW(map[string]string{"crossplane": StateInstalling}, time.Time{})
		if w.convergenceProgressing(base.Add(10 * time.Millisecond)) {
			t.Fatal("nothing ever reached installed — must not defer")
		}
	})
}

// TestApplyDefaults_ProgressGuard_5283 pins that both knobs derive from the
// resolved WatchTimeout (so short-budget tests scale down with it), that an
// explicit value wins, and that a ceiling ≤ WatchTimeout is rejected and
// replaced by the safe default (the ceiling can never precede the soft
// deadline).
func TestApplyDefaults_ProgressGuard_5283(t *testing.T) {
	t.Run("derived from WatchTimeout when unset", func(t *testing.T) {
		c := Config{WatchTimeout: 120 * time.Minute}
		c.applyDefaults()
		if want := 30 * time.Minute; c.ProgressStallWindow != want {
			t.Errorf("ProgressStallWindow = %v, want %v (WatchTimeout/%d)", c.ProgressStallWindow, want, progressGuardStallDivisor)
		}
		if want := 240 * time.Minute; c.ProgressGuardCeiling != want {
			t.Errorf("ProgressGuardCeiling = %v, want %v (WatchTimeout×%d)", c.ProgressGuardCeiling, want, progressGuardCeilingMultiple)
		}
	})

	t.Run("explicit values win", func(t *testing.T) {
		c := Config{WatchTimeout: 120 * time.Minute, ProgressStallWindow: 5 * time.Minute, ProgressGuardCeiling: 300 * time.Minute}
		c.applyDefaults()
		if c.ProgressStallWindow != 5*time.Minute {
			t.Errorf("explicit ProgressStallWindow overwritten: %v", c.ProgressStallWindow)
		}
		if c.ProgressGuardCeiling != 300*time.Minute {
			t.Errorf("explicit ProgressGuardCeiling overwritten: %v", c.ProgressGuardCeiling)
		}
	})

	t.Run("ceiling ≤ WatchTimeout is rejected (never precede the soft deadline)", func(t *testing.T) {
		c := Config{WatchTimeout: 120 * time.Minute, ProgressGuardCeiling: 90 * time.Minute}
		c.applyDefaults()
		if want := 240 * time.Minute; c.ProgressGuardCeiling != want {
			t.Errorf("ProgressGuardCeiling = %v, want %v (a ceiling below WatchTimeout must be replaced by the default)", c.ProgressGuardCeiling, want)
		}
	})
}

// TestWatch_ProgressingSlow_DefersPastWatchTimeout_5283 is the hw279 proof:
// readyHRs keeps climbing PAST the soft WatchTimeout (three HRs remain
// installing at the deadline and flip to installed only afterwards, with no
// failure). Pre-fix the fixed WatchTimeout guillotine returned
// OutcomeTimeout → failed at the deadline; the progress-guard must instead
// defer while progress is active and conclude OutcomeReady once the fleet
// genuinely converges. The watch MUST run past WatchTimeout (proving the
// deferral) and MUST NOT report OutcomeTimeout.
func TestWatch_ProgressingSlow_DefersPastWatchTimeout_5283(t *testing.T) {
	scheme := newFakeScheme()
	names := []string{"bp-c1", "bp-c2", "bp-c3", "bp-c4", "bp-c5", "bp-c6", "bp-c7", "bp-c8"}
	seed := make([]runtime.Object, 0, len(names))
	for _, n := range names {
		seed = append(seed, makeInstallingHelmRelease(n))
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		seed...,
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML: "fake",
		// Soft budget 300ms, but readyHRs keeps rising until ~480ms. A
		// generous stall window + ceiling keep the guard's decision on the
		// PROGRESS signal, not on wall-clock jitter, so the test is not
		// timing-flaky.
		WatchTimeout:         300 * time.Millisecond,
		ProgressStallWindow:  2 * time.Second,
		ProgressGuardCeiling: 30 * time.Second,
		FirstSeenTimeout:     30 * time.Second,
		MinBootstrapKitHRs:   1,
		DynamicFactory:       fakeFactory(client),
		Resync:               0,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// Flip one HR installed → installed every 60ms, ending at ~480ms —
	// well past the 300ms soft WatchTimeout. readyHRs climbs the whole
	// time and nothing fails, so the guard must keep deferring.
	go func() {
		ready := []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"}}
		for _, n := range names {
			time.Sleep(60 * time.Millisecond)
			updateHR(t, client, n, ready)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	final, err := w.Watch(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got := w.Outcome(); got == OutcomeTimeout {
		t.Fatalf("Outcome() = %q — a still-progressing prov (readyHRs climbing, failedHRs=0) must NOT be hard-failed at the WatchTimeout (#5283)", got)
	}
	if got, want := w.Outcome(), OutcomeReady; got != want {
		t.Errorf("Outcome() = %q, want %q — the fleet genuinely converged after the soft deadline", got, want)
	}
	if elapsed <= cfg.WatchTimeout {
		t.Errorf("watch concluded in %v (≤ WatchTimeout %v) — it must DEFER past the soft deadline while progress is active, not conclude at it", elapsed, cfg.WatchTimeout)
	}
	if elapsed >= cfg.ProgressGuardCeiling {
		t.Errorf("watch ran %v (≥ ceiling %v) — should have concluded on convergence well before the hard ceiling", elapsed, cfg.ProgressGuardCeiling)
	}
	for _, n := range names {
		id := ComponentIDFromHelmRelease(n)
		if final[id] != StateInstalled {
			t.Errorf("final[%q] = %q, want %q", id, final[id], StateInstalled)
		}
	}
}

// TestWatch_GenuineStall_ConcludesTimeout_5283 is the contrapositive:
// readyHRs climbs to a plateau (3 installed) then goes FLAT while one HR
// stays stuck installing and nothing further progresses. The progress-guard
// must NOT keep a genuinely stalled prov alive — once readyHRs has been flat
// for ProgressStallWindow the watch still concludes OutcomeTimeout (handler
// → failed), near the soft WatchTimeout and well before the hard ceiling.
func TestWatch_GenuineStall_ConcludesTimeout_5283(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-c1"),
		makeReadyHelmRelease("bp-c2"),
		makeReadyHelmRelease("bp-c3"),
		makeInstallingHelmRelease("bp-stuck"),
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:     "fake",
		WatchTimeout:       400 * time.Millisecond, // stall window defaults to 100ms
		FirstSeenTimeout:   30 * time.Second,
		MinBootstrapKitHRs: 4, // bp-stuck never terminal → all-done gate cannot fire
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

	if got, want := w.Outcome(), OutcomeTimeout; got != want {
		t.Errorf("Outcome() = %q, want %q — a genuinely stalled prov (readyHRs flat past the stall window) must still time out", got, want)
	}
	// Concludes near the soft budget, not deferred to the hard ceiling
	// (WatchTimeout×2 = 800ms by default) — a stall must not be kept alive.
	if elapsed >= 750*time.Millisecond {
		t.Errorf("watch ran %v — a genuine stall must conclude near WatchTimeout (%v), not defer toward the ceiling", elapsed, cfg.WatchTimeout)
	}
	if final["c1"] != StateInstalled || final["stuck"] != StateInstalling {
		t.Errorf("unexpected final states: %v", final)
	}
}
