// Tests for #5125 Defect-2 — step-8 re-clone-on-divergence.

package switchover

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
)

// readyStatus + notReadyStatus are small builders keeping the table
// tests below readable.
func readyStatus(replica bool, timeline int) cnpg.Status {
	return cnpg.Status{Ready: true, IsReplicaCluster: replica, TimelineID: timeline, HasTimelineID: timeline > 0}
}

func notReadyStatus(replica bool, timeline int) cnpg.Status {
	return cnpg.Status{Ready: false, IsReplicaCluster: replica, TimelineID: timeline, HasTimelineID: true}
}

func TestDetectDivergence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name         string
		a            cnpg.Status
		b            cnpg.Status
		wantDiverged bool
		wantTarget   string
	}{
		{
			name:         "healthy steady state — standby Ready streaming",
			a:            cnpg.Status{IsReplicaCluster: false, Ready: true, HasTimelineID: true, TimelineID: 2},
			b:            readyStatus(true, 2),
			wantDiverged: false,
		},
		{
			name:         "clean initial failover — standby behind, not yet Ready",
			a:            notReadyStatus(true, 1), // old primary, now standby, stale TL1
			b:            cnpg.Status{IsReplicaCluster: false, Ready: true, HasTimelineID: true, TimelineID: 2},
			wantDiverged: false,
		},
		{
			name:         "ordinary catch-up — standby not ready, timeline EQUAL",
			a:            notReadyStatus(true, 2),
			b:            cnpg.Status{IsReplicaCluster: false, Ready: true, HasTimelineID: true, TimelineID: 2},
			wantDiverged: false,
		},
		{
			name:         "#5125 Defect-2 — standby not ready, timeline STRICTLY AHEAD of primary",
			a:            notReadyStatus(true, 3), // returning region-a, recovery TL3
			b:            cnpg.Status{IsReplicaCluster: false, Ready: true, HasTimelineID: true, TimelineID: 2},
			wantDiverged: true,
			wantTarget:   "a",
		},
		{
			name:         "divergence on the OTHER physical side (role flip)",
			a:            cnpg.Status{IsReplicaCluster: false, Ready: true, HasTimelineID: true, TimelineID: 2},
			b:            notReadyStatus(true, 3),
			wantDiverged: true,
			wantTarget:   "b",
		},
		{
			name:         "both acting as standby (mid-switchover/malformed) — never guess",
			a:            notReadyStatus(true, 3),
			b:            notReadyStatus(true, 1),
			wantDiverged: false,
		},
		{
			name:         "both acting as primary — never guess",
			a:            readyStatus(false, 2),
			b:            readyStatus(false, 2),
			wantDiverged: false,
		},
		{
			name:         "missing TimelineID data — never guess",
			a:            cnpg.Status{IsReplicaCluster: true, Ready: false},
			b:            cnpg.Status{IsReplicaCluster: false, Ready: true},
			wantDiverged: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target, diverged, reason := DetectDivergence("a", tc.a, "b", tc.b)
			if diverged != tc.wantDiverged {
				t.Fatalf("diverged = %v, want %v (reason=%q)", diverged, tc.wantDiverged, reason)
			}
			if diverged && target != tc.wantTarget {
				t.Errorf("target = %q, want %q", target, tc.wantTarget)
			}
			if diverged && reason == "" {
				t.Errorf("expected non-empty reason when diverged")
			}
		})
	}
}

// newCheckRejoinSequencer builds a minimal Sequencer wired to a fake
// CNPG dynamic client containing one cluster-pair — enough for
// CheckRejoin, which only touches s.CNPG + s.Now.
func newCheckRejoinSequencer(t *testing.T, objs ...runtime.Object) (*Sequencer, *cnpg.Reader) {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), objs...)
	reader := cnpg.NewReader(dyn)
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	seq := &Sequencer{
		CNPG: reader,
		Now:  func() time.Time { return now },
	}
	return seq, reader
}

// newDivergedReplicaCluster builds a cnpg-pair replica Cluster CR that
// is un-Ready and reports a TimelineID strictly ahead of the primary —
// the #5125 Defect-2 divergence shape.
func newDivergedReplicaCluster(ns, pair string, timeline int) *unstructured.Unstructured {
	replica := &unstructured.Unstructured{}
	replica.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	replica.SetNamespace(ns)
	replica.SetName(pair + "-replica")
	replica.SetLabels(map[string]string{cnpg.PairLabel: pair, cnpg.PairRoleLabel: cnpg.RoleReplica})
	_ = unstructured.SetNestedField(replica.Object, true, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(replica.Object, int64(timeline), "status", "timelineID")
	// No Ready condition — un-Ready.
	return replica
}

func newHealthyPrimaryCluster(ns, pair string, timeline int) *unstructured.Unstructured {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	primary.SetNamespace(ns)
	primary.SetName(pair + "-primary")
	primary.SetLabels(map[string]string{cnpg.PairLabel: pair, cnpg.PairRoleLabel: cnpg.RolePrimary})
	_ = unstructured.SetNestedField(primary.Object, int64(timeline), "status", "timelineID")
	_ = unstructured.SetNestedSlice(primary.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	return primary
}

// TestCheckRejoin_DivergenceDetected_IssuesReclone — case (a) from the
// dispatch: divergence detected on the FIRST tick issues an immediate
// re-clone attempt (RecloneCluster called, Attempts=1).
func TestCheckRejoin_DivergenceDetected_IssuesReclone(t *testing.T) {
	t.Parallel()
	const ns, pair = "ns", "demo"
	replica := newDivergedReplicaCluster(ns, pair, 3)
	primary := newHealthyPrimaryCluster(ns, pair, 2)
	seq, reader := newCheckRejoinSequencer(t, primary, replica)

	primaryStatus, _, err := reader.Get(context.Background(), ns, pair+"-primary")
	if err != nil {
		t.Fatalf("Get primary: %v", err)
	}
	replicaStatus, _, err := reader.Get(context.Background(), ns, pair+"-replica")
	if err != nil {
		t.Fatalf("Get replica: %v", err)
	}

	next, result := seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, RejoinState{}, RejoinOptions{})

	if !result.Diverged {
		t.Fatalf("expected Diverged=true, got result=%+v", result)
	}
	if !result.Attempted {
		t.Fatalf("expected Attempted=true (re-clone issued on first detection), got result=%+v", result)
	}
	if result.Bounded {
		t.Fatalf("expected Bounded=false on first attempt, got result=%+v", result)
	}
	if next.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", next.Attempts)
	}
	if next.Target != pair+"-replica" {
		t.Errorf("Target = %q, want %q", next.Target, pair+"-replica")
	}

	// Confirm the reclone actually happened — the Cluster CR's status
	// should be wiped (fresh object).
	got, err := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Get(context.Background(), pair+"-replica", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get post-reclone: %v", err)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(got.Object, "status", "timelineID"); found {
		t.Errorf("expected status wiped by reclone, still present: %v", got.Object["status"])
	}
	// spec.replica.enabled must survive (still meant to be the standby).
	en, _, _ := unstructured.NestedBool(got.Object, "spec", "replica", "enabled")
	if !en {
		t.Errorf("expected spec.replica.enabled=true preserved across reclone")
	}
}

// TestCheckRejoin_HealthyRejoin_NoOp — case (b): a Ready standby (even
// with the SAME or a divergent-looking timeline) must NEVER trigger a
// reclone. Confirms the Cluster CR is untouched.
func TestCheckRejoin_HealthyRejoin_NoOp(t *testing.T) {
	t.Parallel()
	const ns, pair = "ns", "demo"
	replica := &unstructured.Unstructured{}
	replica.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	replica.SetNamespace(ns)
	replica.SetName(pair + "-replica")
	replica.SetLabels(map[string]string{cnpg.PairLabel: pair, cnpg.PairRoleLabel: cnpg.RoleReplica})
	_ = unstructured.SetNestedField(replica.Object, true, "spec", "replica", "enabled")
	_ = unstructured.SetNestedField(replica.Object, int64(3), "status", "timelineID")
	_ = unstructured.SetNestedSlice(replica.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	primary := newHealthyPrimaryCluster(ns, pair, 2)

	seq, reader := newCheckRejoinSequencer(t, primary, replica)
	primaryStatus, _, _ := reader.Get(context.Background(), ns, pair+"-primary")
	replicaStatus, _, _ := reader.Get(context.Background(), ns, pair+"-replica")

	next, result := seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, RejoinState{}, RejoinOptions{})

	if result.Diverged {
		t.Fatalf("expected Diverged=false for a Ready (healthy) standby, got result=%+v", result)
	}
	if result.Attempted {
		t.Fatalf("expected no reclone attempt on a healthy rejoin, got result=%+v", result)
	}
	if next != (RejoinState{}) {
		t.Errorf("expected zero-value RejoinState on no-divergence, got %+v", next)
	}

	// Cluster CR must be completely untouched — same resourceVersion.
	got, err := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Get(context.Background(), pair+"-replica", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetResourceVersion() != replica.GetResourceVersion() {
		t.Errorf("Cluster CR resourceVersion changed (%q -> %q) — must be untouched on healthy rejoin", replica.GetResourceVersion(), got.GetResourceVersion())
	}
}

// TestCheckRejoin_OrdinaryLag_NoOp — an un-Ready standby whose timeline
// is BEHIND or EQUAL to the primary (ordinary catch-up / transient
// reconnect) must not trigger a reclone either.
func TestCheckRejoin_OrdinaryLag_NoOp(t *testing.T) {
	t.Parallel()
	const ns, pair = "ns", "demo"
	replica := newDivergedReplicaCluster(ns, pair, 2) // NOT ahead — equal to primary
	primary := newHealthyPrimaryCluster(ns, pair, 2)
	seq, reader := newCheckRejoinSequencer(t, primary, replica)
	primaryStatus, _, _ := reader.Get(context.Background(), ns, pair+"-primary")
	replicaStatus, _, _ := reader.Get(context.Background(), ns, pair+"-replica")

	_, result := seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, RejoinState{}, RejoinOptions{})
	if result.Diverged || result.Attempted {
		t.Fatalf("expected no-op for a standby whose timeline is not AHEAD of the primary, got result=%+v", result)
	}
}

// TestCheckRejoin_Bounded_CapsAttemptsAndFailsLoud — case (c): a
// standby that NEVER converges (still diverging after every reclone —
// e.g. re-clone succeeds but the underlying cause persists and status
// somehow keeps re-reporting the SAME divergence, or simply the
// operator hasn't fixed the root cause) must be capped at MaxAttempts
// and then fail loud (Bounded=true, Err set) WITHOUT issuing more
// reclone calls.
func TestCheckRejoin_Bounded_CapsAttemptsAndFailsLoud(t *testing.T) {
	t.Parallel()
	const ns, pair = "ns", "demo"
	opts := RejoinOptions{MaxAttempts: 2, Cooldown: time.Minute}

	replica := newDivergedReplicaCluster(ns, pair, 5)
	primary := newHealthyPrimaryCluster(ns, pair, 2)
	seq, reader := newCheckRejoinSequencer(t, primary, replica)
	primaryStatus, _, _ := reader.Get(context.Background(), ns, pair+"-primary")
	replicaStatus, _, _ := reader.Get(context.Background(), ns, pair+"-replica")

	// Attempt 1 — immediate.
	state, result := seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, RejoinState{}, opts)
	if !result.Attempted || state.Attempts != 1 {
		t.Fatalf("attempt 1: expected Attempted=true Attempts=1, got result=%+v state=%+v", result, state)
	}

	// Simulate the reclone not having fixed anything: re-populate the
	// SAME divergence signature directly (in reality a fresh clone's
	// status would reset; here we force the persistent-failure path).
	if err := unstructured.SetNestedField(replica.Object, int64(5), "status", "timelineID"); err != nil {
		t.Fatalf("re-set timelineID: %v", err)
	}
	if _, err := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Update(context.Background(), replica, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update replica: %v", err)
	}
	replicaStatus, _, _ = reader.Get(context.Background(), ns, pair+"-replica")

	// Advance the clock past the attempt-1 backoff window.
	seq.Now = func() time.Time { return time.Date(2026, 7, 18, 0, 5, 0, 0, time.UTC) }

	// Attempt 2 — still within MaxAttempts.
	state, result = seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, state, opts)
	if !result.Attempted || state.Attempts != 2 {
		t.Fatalf("attempt 2: expected Attempted=true Attempts=2, got result=%+v state=%+v", result, state)
	}

	if err := unstructured.SetNestedField(replica.Object, int64(5), "status", "timelineID"); err != nil {
		t.Fatalf("re-set timelineID: %v", err)
	}
	if _, err := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Update(context.Background(), replica, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update replica: %v", err)
	}
	replicaStatus, _, _ = reader.Get(context.Background(), ns, pair+"-replica")
	seq.Now = func() time.Time { return time.Date(2026, 7, 18, 0, 10, 0, 0, time.UTC) }

	// Attempt 3 would exceed MaxAttempts=2 — must bound + fail loud,
	// WITHOUT issuing another reclone call. Capture the resourceVersion
	// via a fresh Get (not the stale local `replica` var) so the
	// post-check comparison is against the ACTUAL current state.
	preBoundCR, _ := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Get(context.Background(), pair+"-replica", metav1.GetOptions{})
	beforeRV := preBoundCR.GetResourceVersion()
	state, result = seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, state, opts)
	if !state.Bounded || !result.Bounded {
		t.Fatalf("expected Bounded=true after exceeding MaxAttempts, got state=%+v result=%+v", state, result)
	}
	if result.Err == nil {
		t.Errorf("expected non-nil Err on bounded fail-loud")
	}
	if result.Attempted {
		t.Errorf("expected NO reclone attempt once bounded")
	}
	got, _ := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Get(context.Background(), pair+"-replica", metav1.GetOptions{})
	if got.GetResourceVersion() != beforeRV {
		t.Errorf("Cluster CR mutated after bounding — resourceVersion changed %q -> %q", beforeRV, got.GetResourceVersion())
	}

	// A SUBSEQUENT tick (still diverged) must keep reporting bounded
	// fail-loud without ever touching the cluster again.
	_, result = seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, state, opts)
	if !result.Bounded || result.Attempted {
		t.Fatalf("expected persistent bounded fail-loud on later ticks, got result=%+v", result)
	}
}

// TestCheckRejoin_WithinCooldown_NoOp — a divergence detected again
// before the backoff window elapses must not issue a second reclone.
func TestCheckRejoin_WithinCooldown_NoOp(t *testing.T) {
	t.Parallel()
	const ns, pair = "ns", "demo"
	opts := RejoinOptions{MaxAttempts: 3, Cooldown: 10 * time.Minute}
	replica := newDivergedReplicaCluster(ns, pair, 5)
	primary := newHealthyPrimaryCluster(ns, pair, 2)
	seq, reader := newCheckRejoinSequencer(t, primary, replica)
	primaryStatus, _, _ := reader.Get(context.Background(), ns, pair+"-primary")
	replicaStatus, _, _ := reader.Get(context.Background(), ns, pair+"-replica")

	state, result := seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, RejoinState{}, opts)
	if !result.Attempted {
		t.Fatalf("attempt 1 should fire immediately, got %+v", result)
	}

	// Re-fetch the (now reset/recreated) replica status and advance the
	// clock by only 1 minute — well within the 10-minute cooldown.
	replicaStatus, _, _ = reader.Get(context.Background(), ns, pair+"-replica")
	// Force the divergence signature back (simulating the recreate not
	// yet having converged, still un-Ready + ahead).
	got, _ := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Get(context.Background(), pair+"-replica", metav1.GetOptions{})
	_ = unstructured.SetNestedField(got.Object, int64(5), "status", "timelineID")
	if _, err := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Update(context.Background(), got, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	replicaStatus, _, _ = reader.Get(context.Background(), ns, pair+"-replica")

	seq.Now = func() time.Time { return time.Date(2026, 7, 18, 0, 1, 0, 0, time.UTC) }
	rvBefore := got.GetResourceVersion()

	_, result2 := seq.CheckRejoin(context.Background(), ns, pair+"-primary", primaryStatus, pair+"-replica", replicaStatus, state, opts)
	if result2.Attempted {
		t.Fatalf("expected no second attempt within the cooldown window, got %+v", result2)
	}
	if !result2.Diverged {
		t.Errorf("expected Diverged=true to still be reported within cooldown (informational)")
	}

	afterCR, _ := reader.Dyn.Resource(cnpg.ClusterGVR).Namespace(ns).Get(context.Background(), pair+"-replica", metav1.GetOptions{})
	if afterCR.GetResourceVersion() != rvBefore {
		t.Errorf("Cluster CR mutated within the cooldown window — resourceVersion changed %q -> %q", rvBefore, afterCR.GetResourceVersion())
	}
}

// TestCheckRejoin_NilCNPGReader_ErrorsGracefully — a Sequencer with no
// CNPG reader wired must return a clear error rather than panic.
func TestCheckRejoin_NilCNPGReader_ErrorsGracefully(t *testing.T) {
	t.Parallel()
	seq := &Sequencer{Now: time.Now}
	standby := notReadyStatus(true, 3)
	primary := cnpg.Status{IsReplicaCluster: false, Ready: true, HasTimelineID: true, TimelineID: 2}
	_, result := seq.CheckRejoin(context.Background(), "ns", "demo-primary", primary, "demo-replica", standby, RejoinState{}, RejoinOptions{})
	if result.Err == nil {
		t.Fatal("expected error when Sequencer.CNPG is nil")
	}
}

// TestRejoinOptions_Defaults confirms the documented defaults.
func TestRejoinOptions_Defaults(t *testing.T) {
	t.Parallel()
	got := RejoinOptions{}.Defaults()
	if got.MaxAttempts != DefaultRejoinMaxAttempts {
		t.Errorf("MaxAttempts = %d want %d", got.MaxAttempts, DefaultRejoinMaxAttempts)
	}
	if got.Cooldown != DefaultRejoinCooldown {
		t.Errorf("Cooldown = %v want %v", got.Cooldown, DefaultRejoinCooldown)
	}
}
