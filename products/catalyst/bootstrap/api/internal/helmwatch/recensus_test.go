// Tests for the #5269 informer-independent periodic re-census.
//
// Root cause the ticker closes: the terminate-on-all-done machinery was
// purely informer-event-driven — `terminated` is closed only from
// processEvent, which only ran on informer callbacks. hw278 (dep
// b27c7e204802ed7f, 2026-07-19) proved a quiesced/hung informer event
// stream silently strands a converged prov: the sentinel
// bp-catalyst-platform was Ready=True for 66 minutes, a fresh informer
// event (nudge) did not re-trigger evaluation, and the deployment sat
// "phase1-watching" ~80min until a catalyst-api rollout-restart
// re-launched a fresh watch that concluded in seconds. The re-census
// ticker drives the SAME processEvent census/decision path from the
// wall clock via a direct List, so the conclusion no longer depends on
// a live event stream.
//
// These tests prove the behaviours that matter:
//
//  1. quiesced informer + sentinel converging later: the ticker List
//     observes the transition and concludes OutcomeReady — with exactly
//     ONE "ready for handover" emit (no double-fire of the downstream
//     handover chain, which keys off Watch returning once).
//  2. quiesced informer + sentinel never appearing: the ticker must NOT
//     invent a conclusion — #4746 sentinel semantics are unchanged, the
//     watch runs to WatchTimeout → OutcomeTimeout; the stale-informer
//     detector flags the dead stream via the heartbeat + a single loud
//     warn event.
//  3. healthy informer racing the ticker: both drivers observing the
//     same transition dedupe to one emitted transition and one ready
//     conclusion.
//
// The quiesced informer is modelled by prepending a watch reactor that
// returns a watch.Interface which never delivers — the informer's
// initial List syncs normally (the hw278 shape: "informer synced
// helmrelease" logged, then silence), while tracker updates remain
// visible to the re-census's direct List.
package helmwatch

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgotesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// deadenWatch makes the fake client's WATCH channel permanently silent
// while leaving LIST untouched: the informer establishes and syncs its
// initial List, then never receives another event — the hw278 quiesced
// event stream, reproduced deterministically.
func deadenWatch(client *dynamicfake.FakeDynamicClient) {
	client.PrependWatchReactor("*", func(_ clientgotesting.Action) (bool, watch.Interface, error) {
		return true, watch.NewFake(), nil
	})
}

// makeInstallingHelmRelease — Ready=Unknown maps to StateInstalling
// (non-terminal), so a sentinel seeded with this blocks the census
// until something observes it flip to Ready=True.
func makeInstallingHelmRelease(name string) *unstructured.Unstructured {
	return makeHelmRelease(name, []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionUnknown, Reason: "Progressing", Message: "Helm install in progress"},
	})
}

// heartbeatRecorder collects every Heartbeat the watcher emits for
// assertions, mirroring the event `recorder` idiom.
type heartbeatRecorder struct {
	mu  sync.Mutex
	hbs []Heartbeat
}

func (h *heartbeatRecorder) record(hb Heartbeat) {
	h.mu.Lock()
	h.hbs = append(h.hbs, hb)
	h.mu.Unlock()
}

func (h *heartbeatRecorder) snapshot() []Heartbeat {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Heartbeat, len(h.hbs))
	copy(out, h.hbs)
	return out
}

// countReadyTransitions counts the "Sovereign ready for handover"
// emits — the double-fire canary: the downstream handover auto-fire
// keys off Watch returning (once), and the operator-visible ready
// transition must appear exactly once no matter which driver (informer
// or ticker) concluded.
func countReadyTransitions(evs []provisioner.Event) int {
	n := 0
	for _, ev := range evs {
		if strings.Contains(ev.Message, "ready for handover") {
			n++
		}
	}
	return n
}

// TestWatch_TickerRecensus_QuiescedInformer_ConcludesReady is the #5269
// fix proof: the sentinel flips Ready AFTER the informer event stream
// has gone permanently silent — only the ticker re-census's direct List
// can observe it. The watch must conclude OutcomeReady (well before
// WatchTimeout), with the sentinel-gated census intact and exactly one
// ready emit.
func TestWatch_TickerRecensus_QuiescedInformer_ConcludesReady(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
		makeInstallingHelmRelease("bp-catalyst-platform"),
	)
	deadenWatch(client)

	rec := &recorder{}
	hbrec := &heartbeatRecorder{}
	cfg := Config{
		KubeconfigYAML:         "fake",
		WatchTimeout:           30 * time.Second, // must NOT be hit — the ticker concludes
		FirstSeenTimeout:       30 * time.Second,
		MinBootstrapKitHRs:     2,
		DynamicFactory:         fakeFactory(client),
		ReadySentinelComponent: "catalyst-platform",
		RecensusInterval:       50 * time.Millisecond,
		OnHeartbeat:            hbrec.record,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	// The sentinel converges 250ms after start. The informer never
	// hears about it (dead watch channel) — only the re-census List
	// can. Pre-#5269 this fixture idles to WatchTimeout.
	go func() {
		time.Sleep(250 * time.Millisecond)
		updateHR(t, client, "bp-catalyst-platform", []metav1.Condition{
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
		t.Errorf("Outcome() = %q, want %q — the ticker re-census must conclude a converged prov even with a dead informer event stream (#5269)", got, want)
	}
	if final["catalyst-platform"] != StateInstalled {
		t.Errorf("final[catalyst-platform] = %q, want %q", final["catalyst-platform"], StateInstalled)
	}
	if final["cilium"] != StateInstalled {
		t.Errorf("final[cilium] = %q, want %q", final["cilium"], StateInstalled)
	}
	if got := countReadyTransitions(rec.snapshot()); got != 1 {
		t.Errorf("ready-for-handover emits = %d, want exactly 1 (double-fire canary)", got)
	}

	// Heartbeats: at least one cycle ran, and the concluding cycle's
	// summary reflects the fresh census (sentinel installed, 2 ready,
	// all-terminal verdict true).
	hbs := hbrec.snapshot()
	if len(hbs) == 0 {
		t.Fatalf("no heartbeats emitted — one structured line per evaluation cycle is the #5269 observability contract")
	}
	last := hbs[len(hbs)-1]
	if !last.AllTerminal {
		t.Errorf("last heartbeat AllTerminal = false, want true (concluding cycle); heartbeat = %+v", last)
	}
	if last.SentinelState != StateInstalled {
		t.Errorf("last heartbeat SentinelState = %q, want %q", last.SentinelState, StateInstalled)
	}
	if last.ReadyHRs != 2 || last.ObservedHRs != 2 {
		t.Errorf("last heartbeat ReadyHRs/ObservedHRs = %d/%d, want 2/2", last.ReadyHRs, last.ObservedHRs)
	}
}

// TestWatch_TickerRecensus_SentinelNeverReady_NoFalseConcludeFlagsStale
// is the contrapositive: a dead informer must NOT let the ticker invent
// a conclusion. The #4746 sentinel gate is unchanged — with the
// sentinel never appearing, the watch runs to WatchTimeout →
// OutcomeTimeout (handler maps to failed). Meanwhile the
// stale-informer detector must surface the dead stream: heartbeats
// flag InformerStale once the quiet stretch exceeds the threshold, and
// exactly ONE loud warn event is dispatched.
func TestWatch_TickerRecensus_SentinelNeverReady_NoFalseConcludeFlagsStale(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
	)
	deadenWatch(client)

	rec := &recorder{}
	hbrec := &heartbeatRecorder{}
	cfg := Config{
		KubeconfigYAML:         "fake",
		WatchTimeout:           1 * time.Second, // sentinel never comes; time out fast
		FirstSeenTimeout:       10 * time.Second,
		MinBootstrapKitHRs:     1,
		DynamicFactory:         fakeFactory(client),
		ReadySentinelComponent: "catalyst-platform",
		RecensusInterval:       30 * time.Millisecond, // stale threshold = 4×30ms = 120ms
		OnHeartbeat:            hbrec.record,
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	if _, err := w.Watch(context.Background()); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got := w.Outcome(); got == OutcomeReady {
		t.Fatalf("Outcome() = %q — the ticker re-census must NEVER conclude ready while the #4746 sentinel is unobserved", got)
	}
	if got, want := w.Outcome(), OutcomeTimeout; got != want {
		t.Errorf("Outcome() = %q, want %q", got, want)
	}

	hbs := hbrec.snapshot()
	if len(hbs) == 0 {
		t.Fatalf("no heartbeats emitted during a 1s watch with a 30ms re-census interval")
	}
	sawStale, sawUnobservedSentinel := false, false
	for _, hb := range hbs {
		if hb.InformerStale {
			sawStale = true
		}
		if hb.SentinelState == "unobserved" {
			sawUnobservedSentinel = true
		}
		if hb.AllTerminal {
			t.Errorf("heartbeat AllTerminal = true with the sentinel unobserved — verdict must mirror the sentinel-gated census; heartbeat = %+v", hb)
		}
	}
	if !sawStale {
		t.Errorf("no heartbeat flagged InformerStale — a dead event stream must be visible in the heartbeat within %d intervals", staleInformerIntervals)
	}
	if !sawUnobservedSentinel {
		t.Errorf("no heartbeat carried SentinelState=unobserved — the sentinel state is the load-bearing forensic field")
	}

	staleWarns := 0
	for _, ev := range rec.snapshot() {
		if strings.Contains(ev.Message, "informer event stream has been quiet") {
			staleWarns++
		}
	}
	if staleWarns != 1 {
		t.Errorf("stale-informer warn events = %d, want exactly 1 (one-shot per quiet stretch, no SSE flooding)", staleWarns)
	}
}

// TestWatch_TickerRecensus_HealthyInformerRace_SingleTransitionEmit
// proves the two drivers dedupe: with a LIVE informer and a hot
// re-census ticker both observing the sentinel's install transition,
// processEvent's under-lock prev/state check must emit the transition
// once and the ready conclusion once.
func TestWatch_TickerRecensus_HealthyInformerRace_SingleTransitionEmit(t *testing.T) {
	scheme := newFakeScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		makeReadyHelmRelease("bp-cilium"),
		makeInstallingHelmRelease("bp-catalyst-platform"),
	)

	rec := &recorder{}
	cfg := Config{
		KubeconfigYAML:         "fake",
		WatchTimeout:           30 * time.Second,
		FirstSeenTimeout:       30 * time.Second,
		MinBootstrapKitHRs:     2,
		DynamicFactory:         fakeFactory(client),
		ReadySentinelComponent: "catalyst-platform",
		RecensusInterval:       10 * time.Millisecond, // hot — maximise the race window
	}
	w, err := NewWatcher(cfg, rec.emit)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	go func() {
		time.Sleep(150 * time.Millisecond)
		updateHR(t, client, "bp-catalyst-platform", []metav1.Condition{
			{Type: "Ready", Status: metav1.ConditionTrue, Reason: "ReconciliationSucceeded", Message: "Helm install succeeded"},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := w.Watch(ctx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if got, want := w.Outcome(), OutcomeReady; got != want {
		t.Errorf("Outcome() = %q, want %q", got, want)
	}
	if got := countReadyTransitions(rec.snapshot()); got != 1 {
		t.Errorf("ready-for-handover emits = %d, want exactly 1 (informer + ticker must dedupe the conclusion)", got)
	}
	installedEmits := 0
	for _, ev := range rec.componentStateEvents() {
		if ev.Component == "catalyst-platform" && ev.State == StateInstalled {
			installedEmits++
		}
	}
	if installedEmits != 1 {
		t.Errorf("catalyst-platform installed transitions emitted = %d, want exactly 1 (both drivers observed it; processEvent dedupes under w.mu)", installedEmits)
	}
}

// TestCompileRecensusInterval pins the env-parse helper's contract:
// empty / unparseable / non-positive input falls back to
// DefaultRecensusInterval; a valid duration parses through. Mirrors
// the CompileWatchTimeout coverage so a future rename or default
// drift lands as a test failure.
func TestCompileRecensusInterval(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", DefaultRecensusInterval},
		{"garbage", DefaultRecensusInterval},
		{"-30s", DefaultRecensusInterval},
		{"0s", DefaultRecensusInterval},
		{"90s", 90 * time.Second},
		{"2m", 2 * time.Minute},
	}
	for _, c := range cases {
		if got := CompileRecensusInterval(c.in); got != c.want {
			t.Errorf("CompileRecensusInterval(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if DefaultRecensusInterval != 45*time.Second {
		t.Errorf("DefaultRecensusInterval = %v, want 45s (#5269 — under-a-minute worst-case conclusion latency)", DefaultRecensusInterval)
	}
}
