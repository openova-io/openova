package switchover

import (
	"context"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

func TestDryRun_HappyPath(t *testing.T) {
	t.Parallel()
	seq, _, rec, _, _, _ := makeSequencer(t)
	ctx := context.Background()
	preCount := rec.Len()

	rep, err := seq.DryRun(ctx, defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}

	// Read-only invariant: NO audit emits during dry-run.
	if rec.Len() != preCount {
		t.Errorf("DryRun mutated audit recorder: pre=%d post=%d", preCount, rec.Len())
	}

	if got := len(rep.Steps); got != 7 {
		t.Errorf("Steps = %d want 7", got)
	}
	if rep.PlanFingerprint == "" {
		t.Error("PlanFingerprint is empty")
	}
	if len(rep.PlanFingerprint) != 16 {
		t.Errorf("PlanFingerprint len = %d want 16", len(rep.PlanFingerprint))
	}
	if rep.GeneratedAt.IsZero() {
		t.Error("GeneratedAt is zero")
	}
	if len(rep.Blockers) != 0 {
		t.Errorf("happy path Blockers should be empty, got %v", rep.Blockers)
	}
	for i, s := range rep.Steps {
		if !s.PreconditionsMet {
			t.Errorf("step %d (%s) preconditions not met: %v", i+1, s.Name, s.Notes)
		}
	}
	// Estimated durations populated.
	if rep.EstimatedDurationSeconds <= 0 {
		t.Errorf("EstimatedDurationSeconds = %d want > 0", rep.EstimatedDurationSeconds)
	}
	if rep.EstimatedWriteDisruptionSeconds <= 0 {
		t.Errorf("EstimatedWriteDisruptionSeconds = %d want > 0", rep.EstimatedWriteDisruptionSeconds)
	}
}

func TestDryRun_FingerprintIdempotent(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	plan := defaultPlan()
	r1, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun#1: %v", err)
	}
	r2, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun#2: %v", err)
	}
	if r1.PlanFingerprint != r2.PlanFingerprint {
		t.Errorf("PlanFingerprint not idempotent: %q vs %q", r1.PlanFingerprint, r2.PlanFingerprint)
	}
}

func TestDryRun_FingerprintChangesOnTargetRegion(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	plan := defaultPlan()
	r1, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun#1: %v", err)
	}
	plan.ToRegion = "ber"
	plan.SynthParams.RegionToIPs["ber"] = []string{"5.7.7.7"}
	r2, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun#2: %v", err)
	}
	if r1.PlanFingerprint == r2.PlanFingerprint {
		t.Errorf("PlanFingerprint should differ when ToRegion changes; both = %q", r1.PlanFingerprint)
	}
}

func TestDryRun_LeaseHeldByThirdParty_Blocks(t *testing.T) {
	t.Parallel()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	if _, err := w.Acquire(context.Background(), "rogue", time.Hour); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seq := mkSeqFromWitness(t, w, events.NewRecorder())
	rep, err := seq.DryRun(context.Background(), defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !rep.Steps[0].PreconditionsMet && !rep.Steps[4].PreconditionsMet {
		// step-1 should have flagged the third-party holder
	}
	if !containsContains(rep.Blockers, "step-1") {
		t.Errorf("expected step-1 blocker, got %v", rep.Blockers)
	}
	// Step 5 also flags the third-party holder via its own read.
	if !containsContains(rep.Blockers, "step-5") {
		t.Errorf("expected step-5 blocker (lease swap), got %v", rep.Blockers)
	}
}

func TestDryRun_NoCNPGPair_BlocksStep2(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	plan := defaultPlan()
	plan.CNPGPair = "missing-pair"
	rep, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !containsContains(rep.Blockers, "step-2") {
		t.Errorf("expected step-2 blocker for missing pair, got %v", rep.Blockers)
	}
}

func TestDryRun_NoHTTPRoute_NoBlockerNoDisruption(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	plan := defaultPlan()
	plan.HTTPRouteName = ""
	rep, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	// Step-3 should be marked PreconditionsMet=true with 0s duration
	if !rep.Steps[2].PreconditionsMet {
		t.Errorf("step-3 should be no-op happy when HTTPRouteName empty")
	}
	if rep.Steps[2].EstimatedDurationSeconds != 0 {
		t.Errorf("step-3 duration should be 0 when no HTTPRoute, got %d", rep.Steps[2].EstimatedDurationSeconds)
	}
}

func TestDryRun_MissingToRegionIPs_BlocksStep4(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	plan := defaultPlan()
	delete(plan.SynthParams.RegionToIPs, plan.ToRegion)
	rep, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !containsContains(rep.Blockers, "step-4") {
		t.Errorf("expected step-4 blocker for missing ToRegion IPs, got %v", rep.Blockers)
	}
}

func TestDryRun_NilWitness_BlocksStep1(t *testing.T) {
	t.Parallel()
	seq := &Sequencer{
		Audit:     events.NewRecorder(),
		PDMCommit: func(ctx context.Context, _ []dns.Record) error { return nil },
	}
	rep, err := seq.DryRun(context.Background(), defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !containsContains(rep.Blockers, "step-1") {
		t.Errorf("expected step-1 blocker for nil Witness, got %v", rep.Blockers)
	}
}

func TestDryRun_NilPDMCommit_BlocksStep4(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	seq.PDMCommit = nil
	rep, err := seq.DryRun(context.Background(), defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !containsContains(rep.Blockers, "step-4") {
		t.Errorf("expected step-4 blocker for nil PDMCommit, got %v", rep.Blockers)
	}
}

func TestDryRun_NilAudit_BlocksStep7(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	seq.Audit = nil
	rep, err := seq.DryRun(context.Background(), defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !containsContains(rep.Blockers, "step-7") {
		t.Errorf("expected step-7 blocker for nil Audit, got %v", rep.Blockers)
	}
}

func TestDryRun_InvalidPlan_ReturnsError(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	plan := defaultPlan()
	plan.FromRegion = plan.ToRegion // From == To
	_, err := seq.DryRun(context.Background(), plan)
	if err == nil {
		t.Fatal("expected validation error from DryRun")
	}
	if !strings.Contains(err.Error(), "dry-run") {
		t.Errorf("error should mention dry-run: %v", err)
	}
}

func TestDryRun_CtxCancel(t *testing.T) {
	t.Parallel()
	seq, _, _, _, _, _ := makeSequencer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := seq.DryRun(ctx, defaultPlan())
	if err == nil {
		t.Fatal("expected ctx.Err propagation")
	}
}

func TestDryRun_LeaseAlreadyOnToRegion_Warning(t *testing.T) {
	t.Parallel()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	if _, err := w.Acquire(context.Background(), "hel", time.Hour); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seq := mkSeqFromWitness(t, w, events.NewRecorder())
	rep, err := seq.DryRun(context.Background(), defaultPlan())
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	// Should be a Warning, not a Blocker.
	if !containsContains(rep.Warnings, "redundant") {
		t.Errorf("expected redundant-switchover warning, got %v", rep.Warnings)
	}
	// step-1 still PreconditionsMet=true (warning, not blocker).
	if !rep.Steps[0].PreconditionsMet {
		t.Errorf("step-1 should pass with warning, not block: %v", rep.Steps[0].Notes)
	}
}

// mkSeqFromWitness — helper for tests that need a custom witness state.
func mkSeqFromWitness(t *testing.T, w witness.Client, rec *events.Recorder) *Sequencer {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), newClusterPair("ns", "demo")...)
	return &Sequencer{
		CNPG:      cnpg.NewReader(dyn),
		Witness:   w,
		HTTPRoute: &fakeHTTPRoute{priorReturned: []int{100, 0}},
		Audit:     rec,
		Sleep:     func(time.Duration) {},
		PDMCommit: func(ctx context.Context, _ []dns.Record) error { return nil },
	}
}

// containsContains reports whether any string in s contains substr.
// Helper because Blockers/Warnings are full sentences with the
// "step-N:" prefix mixed in.
func containsContains(s []string, substr string) bool {
	for _, v := range s {
		if strings.Contains(v, substr) {
			return true
		}
	}
	return false
}
