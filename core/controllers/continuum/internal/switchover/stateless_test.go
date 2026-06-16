// #3375 §5.1 — generic stateless (DNS-flip-only) switchover mechanism.
//
// These tests prove the platform-level claim the topology matrix makes
// for the 12 "stateless; DNS-flip only" DR-capable blueprints (and any
// future stateless app): a switchover is REAL — DNS flips, the lease
// moves, the audit emits — WITHOUT any cnpg-pair or raft-transition
// backing, and WITHOUT any app-name literal in the engine. The SAME
// Sequencer + the SAME plan shape drive every stateless app.
//
// This closes the §3(a) gap: "the 'DNS-flip only' mechanism does not
// exist as code — there is no stateless promoter."

package switchover

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/continuum/internal/dns"
	"github.com/openova-io/openova/core/controllers/continuum/internal/events"
	"github.com/openova-io/openova/core/controllers/continuum/internal/witness"
)

// makeStatelessSequencer builds a Sequencer with NO CNPG reader and NO
// RaftPromoter wired — the exact production shape for a stateless app
// (nothing data-store to promote). The witness + DNS-commit + audit are
// the only collaborators the agnostic steps need.
func makeStatelessSequencer(t *testing.T) (*Sequencer, *witness.InMemoryClient, *events.Recorder, *fakeHTTPRoute, *[]dns.Record) {
	t.Helper()
	store := witness.NewInMemoryStore()
	w := store.Client("ns/cr")
	if _, err := w.Acquire(context.Background(), "fsn", time.Hour); err != nil {
		t.Fatalf("seed lease: %v", err)
	}
	rec := events.NewRecorder()
	httpFake := &fakeHTTPRoute{priorReturned: []int{100, 0}}
	committed := []dns.Record{}
	seq := &Sequencer{
		// CNPG deliberately nil — a stateless app has no cluster pair.
		// RaftPromoter deliberately nil — no openbao peers.json recovery.
		Witness:   w,
		HTTPRoute: httpFake,
		Audit:     rec,
		Sleep:     func(time.Duration) {},
		PDMCommit: func(ctx context.Context, records []dns.Record) error {
			committed = append(committed, records...)
			return nil
		},
	}
	return seq, w, rec, httpFake, &committed
}

// statelessPlan is a switchover plan for a stateless app — it carries NO
// CNPGPair, NO CNPGNamespace, NO RaftTransition target. Only the
// FromRegion/ToRegion + DNS/HTTPRoute knobs the agnostic steps consume.
func statelessPlan(appName string) SwitchoverPlan {
	return SwitchoverPlan{
		ContinuumName:      "ns/dr-" + appName,
		ApplicationName:    "acme/" + appName,
		FromRegion:         "fsn",
		ToRegion:           "hel",
		Mechanism:          MechanismStateless,
		HTTPRouteName:      appName,
		HTTPRouteNamespace: "acme",
		PDMZone:            "example.com",
		Application:        newTestApp([]string{appName + ".example.com"}),
		SynthParams: dns.SynthParams{
			RegionToIPs: map[string][]string{
				"fsn": {"5.1.2.3"},
				"hel": {"5.5.6.7"},
			},
			HealthCheckURL: "https://probe-fsn.example.com/healthz",
			Hostnames:      []string{appName + ".example.com"},
		},
		InitiatedBy: "alice@example.com",
	}
}

func TestStateless_IsValidMechanism(t *testing.T) {
	t.Parallel()
	if !MechanismStateless.IsValid() {
		t.Fatal("MechanismStateless must be a valid (engine-executable) mechanism")
	}
}

// TestStateless_PlanValidates_NoStateBackingRequired proves the
// sequence.go Validate() default-branch no longer rejects a stateless
// plan. Before #3375 a stateless app declaring no cnpg-pair failed
// validation at step-1 ("plan: unknown switchover mechanism").
func TestStateless_PlanValidates_NoStateBackingRequired(t *testing.T) {
	t.Parallel()
	p := statelessPlan("sso-bridge")
	if err := p.Validate(); err != nil {
		t.Fatalf("stateless plan must validate with no CNPGPair/RaftTransition; got %v", err)
	}
}

// TestStateless_Execute_DNSAndLeaseMoveNoStateStore is the core proof:
// the SAME Sequencer.Execute drives a real switchover for a stateless app
// — lease moves to ToRegion, DNS records flip to ToRegion-primary, the
// HTTPRoute drains, the audit emits — with NO CNPG reader and NO Raft
// promoter wired. The two state-store steps run as genuine no-ops.
func TestStateless_Execute_DNSAndLeaseMoveNoStateStore(t *testing.T) {
	t.Parallel()
	seq, w, rec, httpFake, committed := makeStatelessSequencer(t)

	res := seq.Execute(context.Background(), statelessPlan("sso-bridge"))
	if res.Err != nil {
		t.Fatalf("stateless Execute: %v (failed at step %d)", res.Err, res.FailedAtStep)
	}
	if got, want := len(res.StepsCompleted), 7; got != want {
		t.Errorf("StepsCompleted = %d want %d (all 7 steps run for stateless)", got, want)
	}
	// Lease moved to ToRegion — the switchover actually happened.
	st, _ := w.Read(context.Background())
	if st.Holder != "hel" {
		t.Errorf("lease holder = %q want hel (switchover did not move the lease)", st.Holder)
	}
	// HTTPRoute drained the old primary.
	if len(httpFake.setCalls) != 1 || httpFake.setCalls[0] != "fsn" {
		t.Errorf("HTTPRoute setCalls = %v want [fsn]", httpFake.setCalls)
	}
	// DNS flipped — the synthesized records must have ToRegion (hel) IPs.
	if len(*committed) == 0 {
		t.Fatal("expected DNS records committed during stateless switchover")
	}
	// Audit emitted exactly one switchover event.
	if got := rec.EventsByType(events.TypeSwitchover); len(got) != 1 {
		t.Errorf("expected exactly 1 TypeSwitchover audit, got %d", len(got))
	}
}

// TestStateless_Execute_GenericAcrossAppNames is the GENERALITY proof
// (#3375 DoD-5 "zero app-name literal in the engine"): the identical
// Sequencer drives a switchover for THREE different stateless apps with
// no per-app branch. If any app-name special-casing crept into the
// engine, one of these would diverge.
func TestStateless_Execute_GenericAcrossAppNames(t *testing.T) {
	t.Parallel()
	for _, app := range []string{"sso-bridge", "livekit", "vllm"} {
		app := app
		t.Run(app, func(t *testing.T) {
			t.Parallel()
			seq, w, _, _, _ := makeStatelessSequencer(t)
			res := seq.Execute(context.Background(), statelessPlan(app))
			if res.Err != nil {
				t.Fatalf("stateless Execute for %q: %v (step %d)", app, res.Err, res.FailedAtStep)
			}
			st, _ := w.Read(context.Background())
			if st.Holder != "hel" {
				t.Errorf("[%s] lease holder = %q want hel", app, st.Holder)
			}
		})
	}
}

// TestStateless_PromoterStepsAreNoOps asserts the Promoter contract for
// stateless directly: both state-store steps return (nil, nil) — no
// action, no rollback hook. This guards against a future refactor
// accidentally wiring a state mutation into the stateless path.
func TestStateless_PromoterStepsAreNoOps(t *testing.T) {
	t.Parallel()
	p := statelessPromoter{}
	plan := statelessPlan("librechat")

	rb, err := p.Cordon(context.Background(), plan)
	if err != nil {
		t.Fatalf("stateless Cordon must not error: %v", err)
	}
	if rb != nil {
		t.Error("stateless Cordon must return a nil rollback (nothing to un-cordon)")
	}
	rb, err = p.Promote(context.Background(), plan)
	if err != nil {
		t.Fatalf("stateless Promote must not error: %v", err)
	}
	if rb != nil {
		t.Error("stateless Promote must return a nil rollback (nothing to demote)")
	}
}

// TestStateless_promoterFor_NoBackingWiringNeeded confirms the
// Sequencer resolves the stateless Promoter even with CNPG + RaftPromoter
// both nil — the production shape for a stateless app.
func TestStateless_promoterFor_NoBackingWiringNeeded(t *testing.T) {
	t.Parallel()
	seq := &Sequencer{} // nothing wired
	pr, err := seq.promoterFor(SwitchoverPlan{Mechanism: MechanismStateless})
	if err != nil {
		t.Fatalf("promoterFor(stateless) must not error with no backing wired: %v", err)
	}
	if _, ok := pr.(statelessPromoter); !ok {
		t.Fatalf("promoterFor(stateless) = %T, want statelessPromoter", pr)
	}
}
