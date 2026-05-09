package switchover

import (
	"context"
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

// recorderTail adapts an events.Recorder to the AuditTail interface so
// integration tests can drive the F-3 audit-posted check from the same
// in-memory recorder the F-2 + Sequencer use for emit.
type recorderTail struct {
	rec *events.Recorder
}

func (t *recorderTail) Recent(ctx context.Context, since time.Time, auditType string) ([]AuditTailEvent, error) {
	out := []AuditTailEvent{}
	for _, e := range t.rec.Events() {
		if auditType != "" && e.Type != auditType {
			continue
		}
		out = append(out, AuditTailEvent{
			Type:          e.Type,
			ContinuumName: e.ContinuumName,
			FromPrimary:   e.FromPrimary,
			ToPrimary:     e.ToPrimary,
			Timestamp:     time.Now(), // good enough for tests
		})
	}
	return out, nil
}

// TestEndToEnd_DryRunThenSwitchoverThenHealth — the brief's mandated
// integration test. Walks the slice F-2 → Execute → slice F-3 path
// against in-memory fakes.
func TestEndToEnd_DryRunThenSwitchoverThenHealth(t *testing.T) {
	t.Parallel()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	if _, err := w.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}

	rec := events.NewRecorder()
	httpFake := &fakeHTTPRoute{priorReturned: []int{100, 0}}
	scheme := runtime.NewScheme()
	// Use the Ready-conditions cluster pair so the post-switchover
	// health check can confirm both halves Ready.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), newReadyClusterPair("ns", "demo")...)
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
	plan := defaultPlan()

	// --- Step 1: DryRun ---
	rep, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if len(rep.Blockers) != 0 {
		t.Fatalf("DryRun has blockers: %v", rep.Blockers)
	}
	dryRunFingerprint := rep.PlanFingerprint
	preExecuteAuditCount := rec.Len()
	if preExecuteAuditCount != 0 {
		t.Errorf("DryRun should not emit audits; got %d", preExecuteAuditCount)
	}

	// --- Step 2: Execute ---
	res := seq.Execute(context.Background(), plan)
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if len(res.StepsCompleted) != 7 {
		t.Errorf("StepsCompleted = %d want 7", len(res.StepsCompleted))
	}
	if got := rec.EventsByType(events.TypeSwitchover); len(got) != 1 {
		t.Errorf("expected 1 continuum-switchover after Execute, got %d", len(got))
	}

	// --- Step 3: PostSwitchoverHealth ---
	res2 := &fakeResolver{
		name:    "fake",
		answers: map[string][]string{"a.example.com": {"5.5.6.7"}},
	}
	hRep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res2} },
		AuditTail: &recorderTail{rec: rec},
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	if !hRep.OverallHealthy {
		t.Errorf("OverallHealthy=false: %+v", hRep.Checks)
	}
	if hRep.NewPrimaryRegion != "hel" {
		t.Errorf("NewPrimaryRegion = %q want hel", hRep.NewPrimaryRegion)
	}

	// Sanity: dry-run fingerprint matches a re-run on same plan.
	rep2, _ := seq.DryRun(context.Background(), plan)
	if rep2.PlanFingerprint != dryRunFingerprint {
		t.Errorf("PlanFingerprint not idempotent across e2e flow: %q vs %q",
			dryRunFingerprint, rep2.PlanFingerprint)
	}

	// Sanity: actual DNS records were committed during Execute.
	mu.Lock()
	if len(committed) == 0 {
		t.Errorf("no DNS records committed during Execute")
	}
	mu.Unlock()
}

// TestEndToEnd_DryRunBlockedSwitchoverNeverRuns — the operator
// inspects DryRun, sees blockers, and must NOT proceed. We don't
// wire the gating into the Sequencer (UI's responsibility), but we
// assert DryRun surfaces the blocker so the UI can disable Confirm.
func TestEndToEnd_DryRunBlockedSwitchoverNeverRuns(t *testing.T) {
	t.Parallel()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	if _, err := w.Acquire(context.Background(), "rogue-region", time.Hour); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := events.NewRecorder()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), newReadyClusterPair("ns", "demo")...)
	seq := &Sequencer{
		CNPG:      cnpg.NewReader(dyn),
		Witness:   w,
		Audit:     rec,
		Sleep:     func(time.Duration) {},
		PDMCommit: func(ctx context.Context, _ []dns.Record) error { return nil },
	}
	plan := defaultPlan()
	plan.HTTPRouteName = "" // disable step-3

	rep, err := seq.DryRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if len(rep.Blockers) == 0 {
		t.Fatalf("expected blockers (lease held by third-party)")
	}
	// Operator decides not to proceed → no Execute call → no
	// continuum-switchover audit.
	if got := rec.EventsByType(events.TypeSwitchover); len(got) != 0 {
		t.Errorf("DryRun should not produce switchover audit; got %d", len(got))
	}
	// Confirm the blocker is the third-party-holder one.
	matched := false
	for _, b := range rep.Blockers {
		if strings.Contains(b, "third-party") || strings.Contains(b, "rogue-region") {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("blocker should mention third-party holder: %v", rep.Blockers)
	}
}

// Compile-time check: recorderTail satisfies AuditTail.
var _ AuditTail = (*recorderTail)(nil)

// Pull in unused imports to satisfy go vet for parallel test packages.
var _ = unstructured.SetNestedField
var _ = schema.GroupVersionKind{}
