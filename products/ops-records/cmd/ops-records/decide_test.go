package main

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/api"
	"github.com/openova-io/openova/products/ops-records/internal/correlate"
	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/judge"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

var waits = correlate.Policy{Batch: 60 * time.Second, UrgentBatch: 15 * time.Second}

// pending writes one pending signal of acme on cluster c1, last seen at seen.
func pending(t *testing.T, s *store.Memory, seen time.Time) string {
	t.Helper()
	id := ids.New()
	if err := s.InTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.InsertSignal(ctx, store.Signal{ID: id, Organization: "acme", Source: store.SourceAlert, Kind: "pod-crash-loop",
			EntityKind: "deployment", EntityRef: "acme/web", Cluster: "c1", SeverityFloor: 3, Title: "web restarts",
			Fingerprint: "alert/c1/" + id, State: store.SignalOpen, FirstSeen: seen, LastSeen: seen})
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDeciderByRulesAlone(t *testing.T) {
	s := store.NewMemory()
	cfg, err := goodServeConfig(`{"investigator":{"agent":"investigator","lane":"deciding","deadlineSeconds":300,"tokenTier":"viewer","tokenTTLSeconds":1800}}`)
	if err != nil {
		t.Fatal(err)
	}
	d, err := newDecider(s, cfg, api.NewHub(), t.Logf)
	if err != nil || d.busy != nil {
		t.Fatalf("a policy without the correlator did not choose the rules alone: %v", err)
	}
	id := pending(t, s, time.Now().Add(-2*time.Minute))
	if err := d.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	sg, _ := s.GetSignal(context.Background(), "acme", id)
	if sg.IncidentID == "" {
		t.Fatal("the rules did not decide a due batch")
	}
	if _, found, err := d.next(context.Background()); found || err != nil {
		t.Fatalf("a decided batch is still pending: %v %v", found, err)
	}
}

func TestDeciderWithTheCorrelator(t *testing.T) {
	cfg, err := goodServeConfig("")
	if err != nil {
		t.Fatal(err)
	}
	d, err := newDecider(store.NewMemory(), cfg, api.NewHub(), t.Logf)
	if err != nil || d.busy == nil {
		t.Fatalf("a policy with the correlator did not choose the judge: %v", err)
	}
	d.close()
}

func TestNextBatch(t *testing.T) {
	s := store.NewMemory()
	seen := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	d := &decider{store: s, batches: waits}
	if _, found, err := d.next(context.Background()); found || err != nil {
		t.Fatalf("no pending signal: %v %v", found, err)
	}
	pending(t, s, seen)
	if next, found, _ := d.next(context.Background()); !found || !next.Equal(seen.Add(waits.Batch)) {
		t.Fatalf("the rules: next %v %v", next, found)
	}
	// With the judge, a batch's last decision counts from its newest judgement's answer.
	answered := seen.Add(5 * time.Minute)
	if err := s.InTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		return tx.InsertJudgement(ctx, store.Judgement{ID: ids.New(), Organization: "acme", Judge: judge.Correlator,
			SubjectKind: judge.SubjectBatch, SubjectID: "cluster/c1", Outcome: store.OutcomeApplied, AskedAt: seen,
			DeadlineAt: seen.Add(time.Minute), Answer: []byte(`{"decisions":[]}`), AnsweredAt: &answered})
	}); err != nil {
		t.Fatal(err)
	}
	busy := false
	d.busy = func(org, kind, subject string) bool { return busy && org == "acme" && subject == "cluster/c1" }
	if next, found, _ := d.next(context.Background()); !found || !next.Equal(answered.Add(waits.Batch)) {
		t.Fatalf("the judge: next %v %v", next, found)
	}
	busy = true
	if _, found, _ := d.next(context.Background()); found {
		t.Fatal("a batch with a question in flight was counted")
	}
}

func TestDeciderWaits(t *testing.T) {
	s := store.NewMemory()
	now := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	ticks := make(chan struct{}, 4)
	timers := make(chan time.Duration, 4)
	d := &decider{store: s, batches: waits, woken: make(chan struct{}, 1), logf: t.Logf,
		now:   func() time.Time { return now },
		tick:  func(context.Context) error { ticks <- struct{}{}; return nil },
		after: func(wait time.Duration) <-chan time.Time { timers <- wait; return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.run(ctx); close(done) }()
	step := func(what string) {
		t.Helper()
		select {
		case <-ticks:
		case <-time.After(5 * time.Second):
			t.Fatalf("no decision round after %s", what)
		}
	}
	step("the start")
	pending(t, s, now.Add(-20*time.Second)) // due in 40 seconds
	d.wake()
	step("wake")
	if wait := <-timers; wait != 40*time.Second {
		t.Fatalf("waited %s for a batch due in 40s", wait)
	}
	pending(t, s, now.Add(-10*time.Minute)) // due already: the round did not decide it
	d.wake()
	step("the second wake")
	if wait := <-timers; wait != waits.UrgentBatch {
		t.Fatalf("waited %s for a batch the round left due, want the urgent wait", wait)
	}
	cancel()
	<-done
	if len(timers) != 0 {
		t.Fatalf("%d timers more than the rounds needed", len(timers))
	}
}

// goodServeConfig is the serve configuration of testPolicy, with its judges replaced when
// judges is not empty.
func goodServeConfig(judges string) (serveConfig, error) {
	raw := testPolicy
	if judges != "" {
		start := len(`{"operationsOrganization":"operations",`)
		raw = raw[:start] + `"agentRuntime":{"urlTemplate":"http://kagent-controller.{organization}.svc.cluster.local:8083/agents/{organization}/{agent}"},` +
			`"judges":` + judges + `,"lanes":{"deciding":2},"batchSeconds":60,"urgentBatchSeconds":15,"turnBudget":{"turns":120,"windowSeconds":3600}}`
	}
	p, err := judge.ParsePolicy([]byte(raw))
	return serveConfig{catalystAPI: "http://127.0.0.1:1", issuer: "https://auth.sovereign.test", policy: p}, err
}
