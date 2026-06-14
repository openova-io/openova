package switchover

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// fakeExec records every Exec call so tests can assert the exact bao
// commands the raft promotion ran.
type fakeExec struct {
	mu    sync.Mutex
	calls [][]string // command vector per call
	// failOn, when non-empty, makes Exec fail for the first command whose
	// joined string contains failOn (simulates a bao non-zero exit).
	failOn string
}

func (f *fakeExec) Exec(ctx context.Context, ns, pod, container string, command []string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, command)
	joined := strings.Join(command, " ")
	if f.failOn != "" && strings.Contains(joined, f.failOn) {
		return "boom: " + f.failOn, errors.New("command exited 1")
	}
	return "ok: " + joined, nil
}

func (f *fakeExec) callStrings() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// fakeLister returns a fixed Ready-pod set.
type fakeLister struct {
	pods []string
	err  error
}

func (l *fakeLister) ReadyPods(ctx context.Context, ns, selector string) ([]string, error) {
	return l.pods, l.err
}

// raftPlan returns a valid raft-transition SwitchoverPlan.
func raftPlan() SwitchoverPlan {
	return SwitchoverPlan{
		ContinuumName:   "openbao/cont-openbao",
		ApplicationName: "openbao/openbao",
		FromRegion:      "me-east-215-a-1",
		ToRegion:        "me-east-215-b-1",
		Mechanism:       MechanismRaftTransition,
		RaftTransition: RaftTransitionTarget{
			Namespace:    "openbao",
			Pod:          "openbao-0",
			SnapshotPath: "/snapshots/latest.snap",
		},
		PDMZone: "t99.omani.works",
		SynthParams: dns.SynthParams{
			Hostnames: []string{"bao.t99.omani.works"},
			RegionToIPs: map[string][]string{
				"me-east-215-a-1": {"203.0.113.1"},
				"me-east-215-b-1": {"203.0.113.9"},
			},
			HealthCheckURL: "https://bao.t99.omani.works/v1/sys/health",
		},
	}
}

// raftSequencer builds a Sequencer wired with a raft promoter + the
// mechanism-agnostic deps (witness/PDM/audit). CNPG is intentionally nil
// to prove the raft path never touches it.
func raftSequencer(t *testing.T, exec PodExecutor, lister PodLister) (*Sequencer, *events.Recorder) {
	t.Helper()
	store := witness.NewInMemoryStore()
	w := store.Client("openbao/cont-openbao")
	if _, err := w.Acquire(context.Background(), "me-east-215-a-1", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	rec := events.NewRecorder()
	seq := &Sequencer{
		// CNPG deliberately nil — raft-transition must not use it.
		RaftPromoter: &RaftExecPromoter{Exec: exec, Lister: lister},
		Witness:      w,
		Audit:        rec,
		Sleep:        func(time.Duration) {},
		PDMCommit:    func(ctx context.Context, records []dns.Record) error { return nil },
	}
	return seq, rec
}

func TestRaftTransition_FullSequence_RestoreThenTransition(t *testing.T) {
	t.Parallel()
	exec := &fakeExec{}
	seq, rec := raftSequencer(t, exec, nil)

	res := seq.Execute(context.Background(), raftPlan())
	if res.Err != nil {
		t.Fatalf("raft-transition sequence failed: %v (failedAt=%d)", res.Err, res.FailedAtStep)
	}
	if len(res.StepsCompleted) != 7 {
		t.Fatalf("want 7 steps completed, got %d (%v)", len(res.StepsCompleted), res.StepsCompleted)
	}

	calls := exec.callStrings()
	// Exactly two execs: restore then transition-to-primary, in order.
	if len(calls) != 2 {
		t.Fatalf("want 2 exec calls (restore, transition), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "raft snapshot restore /snapshots/latest.snap") {
		t.Errorf("first exec should be the snapshot restore, got %q", calls[0])
	}
	if !strings.Contains(calls[1], "raft transition-to-primary") {
		t.Errorf("second exec should be transition-to-primary, got %q", calls[1])
	}
	// Restore MUST precede transition.
	if strings.Contains(calls[0], "transition-to-primary") {
		t.Errorf("transition ran before restore: %v", calls)
	}

	// The audit switchover event fired with the right from/to.
	sw := rec.EventsByType(events.TypeSwitchover)
	if len(sw) != 1 {
		t.Fatalf("want 1 switchover audit, got %d", len(sw))
	}
	if sw[0].FromPrimary != "me-east-215-a-1" || sw[0].ToPrimary != "me-east-215-b-1" {
		t.Errorf("audit from/to wrong: from=%s to=%s", sw[0].FromPrimary, sw[0].ToPrimary)
	}
}

func TestRaftTransition_PodSelectorResolution(t *testing.T) {
	t.Parallel()
	exec := &fakeExec{}
	// Two Ready pods; lister returns them lexically — promoter picks [0].
	lister := &fakeLister{pods: []string{"openbao-0", "openbao-1"}}
	seq, _ := raftSequencer(t, exec, lister)

	plan := raftPlan()
	plan.RaftTransition.Pod = "" // force selector resolution
	plan.RaftTransition.PodSelector = "app.kubernetes.io/name=openbao"

	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("sequence failed: %v", res.Err)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("want 2 execs, got %d", len(exec.calls))
	}
	// (Pod name isn't echoed by the fake, but resolution succeeding +
	// execs firing proves the selector path ran.)
}

func TestRaftTransition_NoSnapshotPath_SkipsRestore(t *testing.T) {
	t.Parallel()
	exec := &fakeExec{}
	seq, _ := raftSequencer(t, exec, nil)

	plan := raftPlan()
	plan.RaftTransition.SnapshotPath = "" // no staged snapshot → restore skipped

	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("sequence failed: %v", res.Err)
	}
	calls := exec.callStrings()
	if len(calls) != 1 {
		t.Fatalf("want 1 exec (transition only), got %d: %v", len(calls), calls)
	}
	if !strings.Contains(calls[0], "transition-to-primary") {
		t.Errorf("the single exec should be transition-to-primary, got %q", calls[0])
	}
}

func TestRaftTransition_RestoreFailure_FailsSwitchover(t *testing.T) {
	t.Parallel()
	exec := &fakeExec{failOn: "snapshot restore"}
	seq, _ := raftSequencer(t, exec, nil)

	res := seq.Execute(context.Background(), raftPlan())
	if res.Err == nil {
		t.Fatalf("expected switchover to fail when restore errors")
	}
	if res.FailedAtStep != 6 {
		t.Errorf("restore failure should fail at step 6 (promote), got %d", res.FailedAtStep)
	}
	// transition-to-primary must NOT have run after a failed restore.
	for _, c := range exec.callStrings() {
		if strings.Contains(c, "transition-to-primary") {
			t.Errorf("transition ran despite restore failure: %v", exec.callStrings())
		}
	}
}

func TestRaftTransition_NoPromoterWired_FailsCleanly(t *testing.T) {
	t.Parallel()
	store := witness.NewInMemoryStore()
	w := store.Client("openbao/cont-openbao")
	_, _ = w.Acquire(context.Background(), "me-east-215-a-1", time.Hour)
	seq := &Sequencer{
		// No RaftPromoter wired.
		Witness:   w,
		Audit:     events.NewRecorder(),
		Sleep:     func(time.Duration) {},
		PDMCommit: func(ctx context.Context, r []dns.Record) error { return nil },
	}
	res := seq.Execute(context.Background(), raftPlan())
	if res.Err == nil {
		t.Fatalf("expected failure when no RaftPromoter is wired")
	}
	if !strings.Contains(res.Err.Error(), "RaftPromoter") {
		t.Errorf("error should name the missing RaftPromoter, got: %v", res.Err)
	}
	// It should fail at the cordon step (step 2), before any traffic/DNS move.
	if res.FailedAtStep != 2 {
		t.Errorf("want failure at step 2 (cordon), got %d", res.FailedAtStep)
	}
}

func TestRaftTransition_Validate(t *testing.T) {
	t.Parallel()
	// Missing namespace.
	p := raftPlan()
	p.RaftTransition.Namespace = ""
	if err := p.Validate(); err == nil {
		t.Errorf("expected validate error for missing raftTransition.namespace")
	}
	// Missing both pod + selector.
	p = raftPlan()
	p.RaftTransition.Pod = ""
	p.RaftTransition.PodSelector = ""
	if err := p.Validate(); err == nil {
		t.Errorf("expected validate error for missing pod/podSelector")
	}
	// raft-transition does NOT require CNPGPair.
	p = raftPlan()
	p.CNPGPair = ""
	p.CNPGNamespace = ""
	if err := p.Validate(); err != nil {
		t.Errorf("raft-transition should not require CNPGPair: %v", err)
	}
}

// TestCNPGPair_StillDefaultMechanism guards the byte-identical default:
// an empty Mechanism resolves to cnpg-pair and runs the cnpg promotion
// (cordon + replica flip) against the Cluster-CR pair, NEVER the raft path.
func TestCNPGPair_StillDefaultMechanism(t *testing.T) {
	t.Parallel()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	if _, err := w.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	rec := events.NewRecorder()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{cnpg.ClusterGVR: "ClusterList"},
		newClusterPair("ns", "demo")...)
	cnpgR := cnpg.NewReader(dyn)

	// RaftPromoter is wired but must NOT be used for an empty-mechanism plan.
	raftCalled := false
	seq := &Sequencer{
		CNPG:         cnpgR,
		RaftPromoter: &recordingPromoter{onCall: func() { raftCalled = true }},
		Witness:      w,
		Audit:        rec,
		Sleep:        func(time.Duration) {},
		PDMCommit:    func(ctx context.Context, r []dns.Record) error { return nil },
	}
	plan := SwitchoverPlan{
		ContinuumName: "ns/cr",
		FromRegion:    "fsn",
		ToRegion:      "hel",
		// Mechanism deliberately empty → must default to cnpg-pair.
		CNPGPair:      "demo",
		CNPGNamespace: "ns",
		SynthParams: dns.SynthParams{
			Hostnames: []string{"x.example"},
			RegionToIPs: map[string][]string{
				"fsn": {"5.1.2.3"},
				"hel": {"5.5.6.7"},
			},
			HealthCheckURL: "https://probe.example/healthz",
		},
	}
	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("default-mechanism (cnpg-pair) sequence failed: %v", res.Err)
	}
	if raftCalled {
		t.Errorf("raft promoter was invoked for an empty-mechanism (cnpg-pair) plan")
	}
	// The cnpg pair's replica half should now be the primary (enabled=false).
	replica, _, err := cnpgR.Get(context.Background(), "ns", "demo-replica")
	if err != nil {
		t.Fatalf("get replica: %v", err)
	}
	if replica.IsReplicaCluster {
		t.Errorf("after cnpg-pair switchover the replica half should be promoted (replica.enabled=false)")
	}
}

// recordingPromoter is a Promoter that records whether it was called.
type recordingPromoter struct{ onCall func() }

func (p *recordingPromoter) Cordon(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p.onCall != nil {
		p.onCall()
	}
	return nil, nil
}
func (p *recordingPromoter) Promote(ctx context.Context, plan SwitchoverPlan) (func(ctx context.Context) error, error) {
	if p.onCall != nil {
		p.onCall()
	}
	return nil, nil
}
