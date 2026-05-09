package switchover

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/core/controllers/continuum/internal/cnpg"
	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
)

// fakeResolver is a deterministic Resolver for tests.
type fakeResolver struct {
	name    string
	answers map[string][]string
	err     error
}

func (f *fakeResolver) Name() string { return f.name }
func (f *fakeResolver) LookupHost(ctx context.Context, host string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	if v, ok := f.answers[host]; ok {
		return v, nil
	}
	return nil, errors.New("no answer")
}

// fakeAuditTail is a deterministic AuditTail for tests.
type fakeAuditTail struct {
	events []AuditTailEvent
	err    error
}

func (f *fakeAuditTail) Recent(ctx context.Context, since time.Time, auditType string) ([]AuditTailEvent, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []AuditTailEvent{}
	for _, e := range f.events {
		if !e.Timestamp.After(since.Add(-time.Second)) {
			continue
		}
		if auditType != "" && e.Type != auditType {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}

// makeHealthSeq builds a sequencer + a CNPG fake where both halves
// are Ready=true (the post-switchover happy state).
func makeHealthSeq(t *testing.T) *Sequencer {
	t.Helper()
	scheme := runtime.NewScheme()
	objs := newReadyClusterPair("ns", "demo")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), objs...)
	return &Sequencer{
		CNPG:  cnpg.NewReader(dyn),
		Audit: events.NewRecorder(),
		Sleep: func(time.Duration) {},
		PDMCommit: func(ctx context.Context, _ []dns.Record) error {
			return nil
		},
	}
}

// newReadyClusterPair seeds a cluster-pair with Ready=true conditions
// on both halves — the post-switchover happy state.
func newReadyClusterPair(ns, pair string) []runtime.Object {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	primary.SetNamespace(ns)
	primary.SetName(pair + "-primary")
	primary.SetLabels(map[string]string{
		cnpg.PairLabel:     pair,
		cnpg.PairRoleLabel: cnpg.RolePrimary,
	})
	// After switchover the original-primary becomes the replica:
	// replica.enabled=true.
	_ = unstructured.SetNestedField(primary.Object, true, "spec", "replica", "enabled")
	_ = unstructured.SetNestedSlice(primary.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")

	replica := &unstructured.Unstructured{}
	replica.SetGroupVersionKind(schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"})
	replica.SetNamespace(ns)
	replica.SetName(pair + "-replica")
	replica.SetLabels(map[string]string{
		cnpg.PairLabel:     pair,
		cnpg.PairRoleLabel: cnpg.RoleReplica,
	})
	// After switchover the original-replica becomes the new primary:
	// replica.enabled=false.
	_ = unstructured.SetNestedField(replica.Object, false, "spec", "replica", "enabled")
	_ = unstructured.SetNestedSlice(replica.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "True"},
	}, "status", "conditions")
	return []runtime.Object{primary, replica}
}

func newDegradedClusterPair(ns, pair string) []runtime.Object {
	objs := newReadyClusterPair(ns, pair)
	// Strip Ready condition off the new-primary half (replica.GetName).
	r := objs[1].(*unstructured.Unstructured)
	_ = unstructured.SetNestedSlice(r.Object, []interface{}{
		map[string]interface{}{"type": "Ready", "status": "False"},
	}, "status", "conditions")
	return objs
}

func TestPostSwitchoverHealth_HappyPath(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	plan := defaultPlan()
	// Audit tail with a matching switchover event.
	tail := &fakeAuditTail{events: []AuditTailEvent{
		{
			Type:          "continuum-switchover",
			ContinuumName: plan.ContinuumName,
			FromPrimary:   plan.FromRegion,
			ToPrimary:     plan.ToRegion,
			Timestamp:     time.Now().Add(-10 * time.Second),
		},
	}}
	res := &fakeResolver{
		name: "fake-A",
		answers: map[string][]string{
			"a.example.com": {"5.5.6.7"}, // hel IP from defaultPlan
		},
	}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res} },
		AuditTail: tail,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	if rep.NewPrimaryRegion != plan.ToRegion {
		t.Errorf("NewPrimaryRegion = %q want %q", rep.NewPrimaryRegion, plan.ToRegion)
	}
	if !rep.OverallHealthy {
		t.Errorf("OverallHealthy = false; checks=%+v", rep.Checks)
	}
	if got := len(rep.Checks); got != 4 {
		t.Errorf("Checks = %d want 4", got)
	}
	// Replicas + DNS + Audit should pass; Latency is deferred.
	gotByName := map[string]HealthCheck{}
	for _, c := range rep.Checks {
		gotByName[c.Name] = c
	}
	if !gotByName[CheckReplicasHealthy].Passed {
		t.Errorf("replicas check failed: %s", gotByName[CheckReplicasHealthy].Detail)
	}
	if !gotByName[CheckDNSProbes].Passed {
		t.Errorf("dns check failed: %s", gotByName[CheckDNSProbes].Detail)
	}
	if !gotByName[CheckAuditPosted].Passed {
		t.Errorf("audit check failed: %s", gotByName[CheckAuditPosted].Detail)
	}
	if !gotByName[CheckLatencyNormal].Deferred {
		t.Errorf("latency should be deferred: %+v", gotByName[CheckLatencyNormal])
	}
}

func TestPostSwitchoverHealth_ReplicasNotReady(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrListMap(), newDegradedClusterPair("ns", "demo")...)
	seq := &Sequencer{
		CNPG:  cnpg.NewReader(dyn),
		Audit: events.NewRecorder(),
	}
	plan := defaultPlan()
	tail := &fakeAuditTail{events: []AuditTailEvent{{
		Type: "continuum-switchover", ContinuumName: plan.ContinuumName,
		FromPrimary: plan.FromRegion, ToPrimary: plan.ToRegion,
		Timestamp: time.Now(),
	}}}
	res := &fakeResolver{name: "fake", answers: map[string][]string{"a.example.com": {"5.5.6.7"}}}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res} },
		AuditTail: tail,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	if rep.OverallHealthy {
		t.Errorf("OverallHealthy = true but replicas should fail")
	}
	for _, c := range rep.Checks {
		if c.Name == CheckReplicasHealthy {
			if c.Passed {
				t.Errorf("replicas check should fail; detail=%s", c.Detail)
			}
			if !strings.Contains(c.Detail, "not Ready") {
				t.Errorf("replicas detail should mention not Ready, got %q", c.Detail)
			}
		}
	}
}

func TestPostSwitchoverHealth_DNSMismatch(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	plan := defaultPlan()
	// Resolver returns the OLD primary's IP — should fail.
	res := &fakeResolver{name: "fake", answers: map[string][]string{"a.example.com": {"5.1.2.3"}}}
	tail := &fakeAuditTail{events: []AuditTailEvent{{
		Type: "continuum-switchover", ContinuumName: plan.ContinuumName,
		FromPrimary: plan.FromRegion, ToPrimary: plan.ToRegion,
		Timestamp: time.Now(),
	}}}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res} },
		AuditTail: tail,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	if rep.OverallHealthy {
		t.Errorf("OverallHealthy = true but DNS should fail")
	}
	for _, c := range rep.Checks {
		if c.Name == CheckDNSProbes {
			if c.Passed {
				t.Errorf("DNS check should fail")
			}
			if !strings.Contains(c.Detail, "none in expected set") {
				t.Errorf("DNS detail should mention mismatch, got %q", c.Detail)
			}
		}
	}
}

func TestPostSwitchoverHealth_DNSResolverError(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	plan := defaultPlan()
	res := &fakeResolver{name: "fake", err: errors.New("simulated DNS failure")}
	tail := &fakeAuditTail{events: []AuditTailEvent{{
		Type: "continuum-switchover", ContinuumName: plan.ContinuumName,
		FromPrimary: plan.FromRegion, ToPrimary: plan.ToRegion,
		Timestamp: time.Now(),
	}}}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res} },
		AuditTail: tail,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == CheckDNSProbes && c.Passed {
			t.Errorf("DNS check should fail on resolver error")
		}
	}
}

func TestPostSwitchoverHealth_AuditMissing(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	plan := defaultPlan()
	res := &fakeResolver{name: "fake", answers: map[string][]string{"a.example.com": {"5.5.6.7"}}}
	// No matching audit event in tail.
	tail := &fakeAuditTail{events: []AuditTailEvent{}}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res} },
		AuditTail: tail,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	if rep.OverallHealthy {
		t.Errorf("OverallHealthy=true but audit check should fail")
	}
}

func TestPostSwitchoverHealth_NoResolvers_DefersDNS(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	plan := defaultPlan()
	tail := &fakeAuditTail{events: []AuditTailEvent{{
		Type: "continuum-switchover", ContinuumName: plan.ContinuumName,
		FromPrimary: plan.FromRegion, ToPrimary: plan.ToRegion,
		Timestamp: time.Now(),
	}}}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: nil,
		AuditTail: tail,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == CheckDNSProbes {
			if !c.Deferred {
				t.Errorf("DNS check should be deferred when Resolvers nil")
			}
		}
	}
	// Deferred should NOT block OverallHealthy.
	if !rep.OverallHealthy {
		t.Errorf("OverallHealthy should be true (only DNS+latency deferred)")
	}
}

func TestPostSwitchoverHealth_NoAuditTail_DefersAudit(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	plan := defaultPlan()
	res := &fakeResolver{name: "fake", answers: map[string][]string{"a.example.com": {"5.5.6.7"}}}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res} },
		AuditTail: nil,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == CheckAuditPosted {
			if !c.Deferred {
				t.Errorf("audit check should be deferred when AuditTail nil")
			}
		}
	}
}

func TestPostSwitchoverHealth_CtxCancel(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := seq.PostSwitchoverHealth(ctx, defaultPlan(), HealthOptions{})
	if err == nil {
		t.Fatal("expected ctx-cancel error")
	}
}

func TestPostSwitchoverHealth_AlwaysFourChecks(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	rep, err := seq.PostSwitchoverHealth(context.Background(), defaultPlan(), HealthOptions{})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	if got := len(rep.Checks); got != 4 {
		t.Errorf("Checks count = %d want 4", got)
	}
	wantNames := map[string]bool{
		CheckReplicasHealthy: false,
		CheckDNSProbes:       false,
		CheckLatencyNormal:   false,
		CheckAuditPosted:     false,
	}
	for _, c := range rep.Checks {
		if _, ok := wantNames[c.Name]; !ok {
			t.Errorf("unexpected check name %q", c.Name)
		}
		wantNames[c.Name] = true
	}
	for n, present := range wantNames {
		if !present {
			t.Errorf("missing required check %q", n)
		}
	}
}

func TestDefaultMultiResolverDial_ReturnsThree(t *testing.T) {
	t.Parallel()
	rs := DefaultMultiResolverDial()
	if got := len(rs); got != 3 {
		t.Errorf("DefaultMultiResolverDial = %d want 3", got)
	}
	wantNames := map[string]bool{"8.8.8.8": false, "1.1.1.1": false, "9.9.9.9": false}
	for _, r := range rs {
		if _, ok := wantNames[r.Name()]; !ok {
			t.Errorf("unexpected resolver %q", r.Name())
		}
		wantNames[r.Name()] = true
	}
	for n, present := range wantNames {
		if !present {
			t.Errorf("missing default resolver %q", n)
		}
	}
}

func TestPostSwitchoverHealth_AuditTailError(t *testing.T) {
	t.Parallel()
	seq := makeHealthSeq(t)
	plan := defaultPlan()
	res := &fakeResolver{name: "fake", answers: map[string][]string{"a.example.com": {"5.5.6.7"}}}
	tail := &fakeAuditTail{err: errors.New("simulated tail failure")}
	rep, err := seq.PostSwitchoverHealth(context.Background(), plan, HealthOptions{
		Resolvers: func() []Resolver { return []Resolver{res} },
		AuditTail: tail,
	})
	if err != nil {
		t.Fatalf("PostSwitchoverHealth: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == CheckAuditPosted {
			if c.Passed {
				t.Errorf("audit check should fail on tail error")
			}
			if !strings.Contains(c.Detail, "AuditTail.Recent") {
				t.Errorf("detail should mention tail error, got %q", c.Detail)
			}
		}
	}
}
