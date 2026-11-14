package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
)

// The contract test. Every store of this package runs runContract with a function that
// opens a new, empty store. The in-memory store runs it below; a PostgreSQL store adds its
// own Test function that calls runContract the same way.

func TestMemoryContract(t *testing.T) {
	runContract(t, func(t *testing.T) Store {
		m := NewMemory()
		t.Cleanup(func() { _ = m.Close() })
		return m
	})
}

// opener returns a new, empty store for one subtest.
type opener func(t *testing.T) Store

func runContract(t *testing.T, open opener) {
	cases := []struct {
		name string
		run  func(t *testing.T, s Store)
	}{
		{"signal round trip in normal form", testSignalRoundTrip},
		{"empty JSON is an object and an empty answer is none", testEmptyJSON},
		{"returned rows are copies", testCopies},
		{"identifiers are unique across Organizations", testConflict},
		{"invalid rows are refused and nothing is written", testInvalid},
		{"Organization scope of reads", testOrganizationScope},
		{"references stay inside the Organization", testReferences},
		{"updates replace the row of the same Organization", testUpdates},
		{"signal filters and order", testListSignals},
		{"incident order, filter and limits", testListIncidents},
		{"timeline is append only and in order", testTimeline},
		{"problem search", testSearchProblems},
		{"judgement filters and order", testJudgements},
		{"feedback", testFeedback},
		{"a failed transaction keeps nothing", testRollback},
		{"a panicking transaction keeps nothing", testPanic},
		{"a finished transaction refuses calls", testTxDone},
		{"outbox order and publication", testOutbox},
		{"a done context changes nothing", testContextDone},
		{"concurrent transactions all land", testConcurrent},
		{"ping and close", testPingClose},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { c.run(t, open(t)) })
	}
}

var (
	ctx  = context.Background()
	t0   = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	zone = time.FixedZone("plus4", 4*3600)
)

// at returns t0 plus n minutes.
func at(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }

func ptr(t time.Time) *time.Time { return &t }

// canonical renders a row so that two rows with the same meaning render the same: JSON by
// meaning, times as RFC 3339 with their zone.
func canonical(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func same(t *testing.T, what string, got, want any) {
	t.Helper()
	if g, w := canonical(t, got), canonical(t, want); g != w {
		t.Fatalf("%s:\n got  %s\n want %s", what, g, w)
	}
}

func is(t *testing.T, what string, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("%s: err = %v, want %v", what, err, want)
	}
}

func ok(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// write runs fn in one transaction and fails the test when it does not commit.
func write(t *testing.T, s Store, fn func(tx Tx) error) {
	t.Helper()
	ok(t, "transaction", s.InTx(ctx, func(_ context.Context, tx Tx) error { return fn(tx) }))
}

// tryWrite runs fn in one transaction and returns its error.
func tryWrite(s Store, fn func(tx Tx) error) error {
	return s.InTx(ctx, func(_ context.Context, tx Tx) error { return fn(tx) })
}

func newSignal(org string, n int) Signal {
	return Signal{
		ID: ids.New(), Organization: org, Source: SourceAlert, Kind: "pod-crash-loop",
		EntityKind: "deployment", EntityRef: org + "/web", Cluster: "c1", SeverityFloor: 3,
		Title: fmt.Sprintf("web restarts %d", n), Body: json.RawMessage(`{"restarts": 5, "pod": "web-1"}`),
		Fingerprint: fmt.Sprintf("fp-%d", n), State: SignalOpen, FirstSeen: at(n), LastSeen: at(n + 1),
	}
}

func newIncident(org string, n int) Incident {
	return Incident{
		ID: ids.NewIncident(), Organization: org, Title: fmt.Sprintf("database unreachable %d", n),
		Severity: 2, SeveritySetBy: ByRules, State: IncidentOpen, OpenedBy: ByRules, OpenedAt: at(n),
	}
}

func newProblem(org string, n int, title, statement string) Problem {
	return Problem{ID: ids.NewProblem(), Organization: org, Title: title, Statement: statement,
		State: ProblemOpen, CreatedAt: at(n)}
}

func newJudgement(org string, n int, judge, subject string) Judgement {
	return Judgement{
		ID: ids.New(), Organization: org, Judge: judge, SubjectKind: "signal_group", SubjectID: subject,
		Offered: json.RawMessage(`{"task":"correlate"}`), RuleResult: json.RawMessage(`{"decisions":[]}`),
		AskedAt: at(n), DeadlineAt: at(n + 3),
	}
}

func newOutbox(org string) OutboxRow {
	return OutboxRow{ID: ids.New(), Organization: org, Subject: "catalyst.ops.incident_opened.v1",
		Payload: json.RawMessage(`{"incidentId":"x"}`), CreatedAt: t0}
}

func testSignalRoundTrip(t *testing.T, s Store) {
	in := newSignal("acme", 0)
	// A time in another zone with nanoseconds: stored to the microsecond, returned in UTC.
	in.FirstSeen = time.Date(2026, 12, 1, 13, 0, 0, 123_456_789, zone)
	in.LastSeen = in.FirstSeen.Add(time.Minute)
	in.Body = json.RawMessage("{\n  \"pod\": \"web-1\",\n  \"restarts\": 5\n}")
	write(t, s, func(tx Tx) error { return tx.InsertSignal(ctx, in) })

	want := in
	want.FirstSeen = time.Date(2026, 12, 1, 9, 0, 0, 123_456_000, time.UTC)
	want.LastSeen = want.FirstSeen.Add(time.Minute)
	got, err := s.GetSignal(ctx, "acme", in.ID)
	ok(t, "get", err)
	same(t, "signal", got, want)
	if got.FirstSeen.Location().String() != "UTC" {
		t.Fatalf("first seen is in %s, want UTC", got.FirstSeen.Location())
	}
}

func testEmptyJSON(t *testing.T, s Store) {
	sig := newSignal("acme", 0)
	sig.Body = nil
	inc := newIncident("acme", 0)
	j := newJudgement("acme", 0, "correlator", "g1")
	j.Offered, j.RuleResult = nil, nil
	write(t, s, func(tx Tx) error {
		ok(t, "signal", tx.InsertSignal(ctx, sig))
		ok(t, "incident", tx.InsertIncident(ctx, inc))
		_, err := tx.AppendIncidentEvent(ctx, IncidentEvent{Organization: "acme", IncidentID: inc.ID, At: t0,
			ActorKind: BySystem, Actor: "ops-records", Kind: "opened"})
		ok(t, "event", err)
		return tx.InsertJudgement(ctx, j)
	})
	gotSig, err := s.GetSignal(ctx, "acme", sig.ID)
	ok(t, "get signal", err)
	same(t, "empty body", gotSig.Body, json.RawMessage(`{}`))
	events, err := s.ListIncidentEvents(ctx, "acme", inc.ID)
	ok(t, "events", err)
	if len(events) != 1 {
		t.Fatalf("%d events", len(events))
	}
	same(t, "empty data", events[0].Data, json.RawMessage(`{}`))
	gotJ, err := s.GetJudgement(ctx, "acme", j.ID)
	ok(t, "get judgement", err)
	same(t, "empty offered", gotJ.Offered, json.RawMessage(`{}`))
	same(t, "empty rule result", gotJ.RuleResult, json.RawMessage(`{}`))
	if len(gotJ.Answer) != 0 || gotJ.AnsweredAt != nil || gotJ.Outcome != OutcomeUndecided {
		t.Fatalf("an undecided judgement came back with answer %q, answered at %v, outcome %q",
			gotJ.Answer, gotJ.AnsweredAt, gotJ.Outcome)
	}
}

func testCopies(t *testing.T, s Store) {
	inc := newIncident("acme", 0)
	inc.State, inc.ResolvedAt = IncidentResolved, ptr(at(5))
	sig := newSignal("acme", 0)
	write(t, s, func(tx Tx) error {
		ok(t, "incident", tx.InsertIncident(ctx, inc))
		return tx.InsertSignal(ctx, sig)
	})
	// Changing what was given changes nothing stored.
	sig.Body[1] = 'X'
	got, err := s.GetSignal(ctx, "acme", sig.ID)
	ok(t, "get", err)
	same(t, "body after the caller changed its own copy", got.Body, json.RawMessage(`{"restarts":5,"pod":"web-1"}`))
	// Changing what was returned changes nothing stored.
	got.Body[2] = 'Y'
	gotInc, err := s.GetIncident(ctx, "acme", inc.ID)
	ok(t, "get incident", err)
	*gotInc.ResolvedAt = at(99)
	again, err := s.GetSignal(ctx, "acme", sig.ID)
	ok(t, "get again", err)
	same(t, "body after the caller changed a returned row", again.Body, json.RawMessage(`{"restarts":5,"pod":"web-1"}`))
	againInc, err := s.GetIncident(ctx, "acme", inc.ID)
	ok(t, "get incident again", err)
	if !againInc.ResolvedAt.Equal(at(5)) {
		t.Fatalf("resolved at changed to %v", againInc.ResolvedAt)
	}
}

func testConflict(t *testing.T, s Store) {
	sig := newSignal("acme", 0)
	inc := newIncident("acme", 0)
	prb := newProblem("acme", 0, "disk full", "")
	j := newJudgement("acme", 0, "correlator", "g1")
	o := newOutbox("acme")
	write(t, s, func(tx Tx) error {
		ok(t, "signal", tx.InsertSignal(ctx, sig))
		ok(t, "incident", tx.InsertIncident(ctx, inc))
		ok(t, "problem", tx.InsertProblem(ctx, prb))
		ok(t, "judgement", tx.InsertJudgement(ctx, j))
		return tx.Enqueue(ctx, o)
	})
	sig.Organization, inc.Organization, prb.Organization, j.Organization, o.Organization = "beta", "beta", "beta", "beta", "beta"
	is(t, "signal again", tryWrite(s, func(tx Tx) error { return tx.InsertSignal(ctx, sig) }), ErrConflict)
	is(t, "incident again", tryWrite(s, func(tx Tx) error { return tx.InsertIncident(ctx, inc) }), ErrConflict)
	is(t, "problem again", tryWrite(s, func(tx Tx) error { return tx.InsertProblem(ctx, prb) }), ErrConflict)
	is(t, "judgement again", tryWrite(s, func(tx Tx) error { return tx.InsertJudgement(ctx, j) }), ErrConflict)
	is(t, "outbox again", tryWrite(s, func(tx Tx) error { return tx.Enqueue(ctx, o) }), ErrConflict)
}

func testInvalid(t *testing.T, s Store) {
	type mutate struct {
		name string
		row  any
	}
	sig := func(f func(*Signal)) Signal { r := newSignal("acme", 0); f(&r); return r }
	inc := func(f func(*Incident)) Incident { r := newIncident("acme", 0); f(&r); return r }
	prb := func(f func(*Problem)) Problem { r := newProblem("acme", 0, "t", ""); f(&r); return r }
	jdg := func(f func(*Judgement)) Judgement { r := newJudgement("acme", 0, "correlator", "g1"); f(&r); return r }
	out := func(f func(*OutboxRow)) OutboxRow { r := newOutbox("acme"); f(&r); return r }
	cases := []mutate{
		{"signal without Organization", sig(func(r *Signal) { r.Organization = "" })},
		{"signal id not a ULID", sig(func(r *Signal) { r.ID = "sig-1" })},
		{"signal source", sig(func(r *Signal) { r.Source = "metrics" })},
		{"signal state", sig(func(r *Signal) { r.State = "closed" })},
		{"signal severity floor 0", sig(func(r *Signal) { r.SeverityFloor = 0 })},
		{"signal severity floor 5", sig(func(r *Signal) { r.SeverityFloor = 5 })},
		{"signal without kind", sig(func(r *Signal) { r.Kind = "" })},
		{"signal without entity kind", sig(func(r *Signal) { r.EntityKind = "" })},
		{"signal without entity ref", sig(func(r *Signal) { r.EntityRef = "" })},
		{"signal without title", sig(func(r *Signal) { r.Title = "" })},
		{"signal without fingerprint", sig(func(r *Signal) { r.Fingerprint = "" })},
		{"signal without first seen", sig(func(r *Signal) { r.FirstSeen = time.Time{} })},
		{"signal last seen before first seen", sig(func(r *Signal) { r.LastSeen = r.FirstSeen.Add(-time.Second) })},
		{"signal body not JSON", sig(func(r *Signal) { r.Body = json.RawMessage(`{"a":`) })},
		{"signal incident id malformed", sig(func(r *Signal) { r.IncidentID = "INC-1" })},
		{"incident id without prefix", inc(func(r *Incident) { r.ID = ids.New() })},
		{"incident without title", inc(func(r *Incident) { r.Title = "" })},
		{"incident severity 0", inc(func(r *Incident) { r.Severity = 0 })},
		{"incident severity set by", inc(func(r *Incident) { r.SeveritySetBy = "system" })},
		{"incident state", inc(func(r *Incident) { r.State = "closed" })},
		{"incident opened by person", inc(func(r *Incident) { r.OpenedBy = ByPerson })},
		{"incident without opened at", inc(func(r *Incident) { r.OpenedAt = time.Time{} })},
		{"incident resolved without resolved at", inc(func(r *Incident) { r.State = IncidentResolved })},
		{"incident open with resolved at", inc(func(r *Incident) { r.ResolvedAt = ptr(at(1)) })},
		{"incident resolved before opened", inc(func(r *Incident) { r.State, r.ResolvedAt = IncidentResolved, ptr(at(-1)) })},
		{"incident is its own parent", inc(func(r *Incident) { r.ParentID = r.ID })},
		{"incident problem id malformed", inc(func(r *Incident) { r.ProblemID = "PRB-x" })},
		{"problem id without prefix", prb(func(r *Problem) { r.ID = ids.NewIncident() })},
		{"problem without title", prb(func(r *Problem) { r.Title = "" })},
		{"problem state", prb(func(r *Problem) { r.State = "solved" })},
		{"problem without created at", prb(func(r *Problem) { r.CreatedAt = time.Time{} })},
		{"judgement id", jdg(func(r *Judgement) { r.ID = "j1" })},
		{"judgement without judge", jdg(func(r *Judgement) { r.Judge = "" })},
		{"judgement without subject kind", jdg(func(r *Judgement) { r.SubjectKind = "" })},
		{"judgement without subject id", jdg(func(r *Judgement) { r.SubjectID = "" })},
		{"judgement outcome", jdg(func(r *Judgement) { r.Outcome = "accepted" })},
		{"judgement deadline before asked", jdg(func(r *Judgement) { r.DeadlineAt = r.AskedAt.Add(-time.Second) })},
		{"judgement answer without answered at", jdg(func(r *Judgement) { r.Answer = json.RawMessage(`{}`) })},
		{"judgement answered at without answer", jdg(func(r *Judgement) { r.AnsweredAt = ptr(at(1)) })},
		{"judgement answered before asked", jdg(func(r *Judgement) { r.Answer, r.AnsweredAt = json.RawMessage(`{}`), ptr(at(-1)) })},
		{"judgement answer not JSON", jdg(func(r *Judgement) { r.Answer, r.AnsweredAt = json.RawMessage(`[`), ptr(at(1)) })},
		{"outbox id", out(func(r *OutboxRow) { r.ID = "o1" })},
		{"outbox without Organization", out(func(r *OutboxRow) { r.Organization = "" })},
		{"outbox subject without version", out(func(r *OutboxRow) { r.Subject = "catalyst.ops.incident_opened" })},
		{"outbox subject outside catalyst", out(func(r *OutboxRow) { r.Subject = "ops.incident.opened.v1" })},
		{"outbox without created at", out(func(r *OutboxRow) { r.CreatedAt = time.Time{} })},
	}
	for _, c := range cases {
		err := tryWrite(s, func(tx Tx) error {
			switch r := c.row.(type) {
			case Signal:
				return tx.InsertSignal(ctx, r)
			case Incident:
				return tx.InsertIncident(ctx, r)
			case Problem:
				return tx.InsertProblem(ctx, r)
			case Judgement:
				return tx.InsertJudgement(ctx, r)
			case OutboxRow:
				return tx.Enqueue(ctx, r)
			}
			return nil
		})
		is(t, c.name, err, ErrInvalid)
	}
	// Timeline entries and feedback, against an incident that exists.
	parent := newIncident("acme", 0)
	write(t, s, func(tx Tx) error { return tx.InsertIncident(ctx, parent) })
	good := IncidentEvent{Organization: "acme", IncidentID: parent.ID, At: t0, ActorKind: ByRules, Actor: "r1", Kind: "opened"}
	evs := map[string]func(*IncidentEvent){
		"event without Organization": func(e *IncidentEvent) { e.Organization = "" },
		"event actor kind":           func(e *IncidentEvent) { e.ActorKind = "robot" },
		"event without actor":        func(e *IncidentEvent) { e.Actor = "" },
		"event without kind":         func(e *IncidentEvent) { e.Kind = "" },
		"event without at":           func(e *IncidentEvent) { e.At = time.Time{} },
		"event data not JSON":        func(e *IncidentEvent) { e.Data = json.RawMessage(`x`) },
		"event incident id":          func(e *IncidentEvent) { e.IncidentID = "x" },
	}
	for name, f := range evs {
		e := good
		f(&e)
		is(t, name, tryWrite(s, func(tx Tx) error { _, err := tx.AppendIncidentEvent(ctx, e); return err }), ErrInvalid)
	}
	fb := Feedback{Organization: "acme", IncidentID: parent.ID, Person: "a@acme.test", Kind: FeedbackMerge, At: t0}
	fbs := map[string]func(*Feedback){
		"feedback kind":           func(f *Feedback) { f.Kind = "like" },
		"feedback without person": func(f *Feedback) { f.Person = "" },
		"feedback without at":     func(f *Feedback) { f.At = time.Time{} },
		"feedback data not JSON":  func(f *Feedback) { f.Data = json.RawMessage(`{`) },
	}
	for name, f := range fbs {
		x := fb
		f(&x)
		is(t, name, tryWrite(s, func(tx Tx) error { _, err := tx.AddFeedback(ctx, x); return err }), ErrInvalid)
	}
	// Nothing of the refused rows was written.
	sigs, err := s.ListSignals(ctx, SignalFilter{})
	ok(t, "list signals", err)
	incs, err := s.ListIncidents(ctx, IncidentFilter{})
	ok(t, "list incidents", err)
	prbs, err := s.SearchProblems(ctx, "", "", 0)
	ok(t, "list problems", err)
	jdgs, err := s.ListJudgements(ctx, JudgementFilter{})
	ok(t, "list judgements", err)
	pend, err := s.PendingOutbox(ctx, 0)
	ok(t, "pending", err)
	events, err := s.ListIncidentEvents(ctx, "acme", parent.ID)
	ok(t, "events", err)
	feedback, err := s.ListFeedback(ctx, "acme", parent.ID)
	ok(t, "feedback", err)
	if len(sigs)+len(prbs)+len(jdgs)+len(pend)+len(events)+len(feedback) != 0 || len(incs) != 1 {
		t.Fatalf("refused rows were written: %d signals, %d incidents, %d problems, %d judgements, %d outbox, %d events, %d feedback",
			len(sigs), len(incs), len(prbs), len(jdgs), len(pend), len(events), len(feedback))
	}
}

func testOrganizationScope(t *testing.T, s Store) {
	a, b := newSignal("acme", 0), newSignal("beta", 1)
	write(t, s, func(tx Tx) error {
		ok(t, "a", tx.InsertSignal(ctx, a))
		return tx.InsertSignal(ctx, b)
	})
	_, err := s.GetSignal(ctx, "beta", a.ID)
	is(t, "another Organization's signal", err, ErrNotFound)
	_, err = s.GetSignal(ctx, "acme", ids.New())
	is(t, "unknown signal", err, ErrNotFound)
	got, err := s.GetSignal(ctx, "", a.ID)
	ok(t, "sovereign-admin read", err)
	if got.Organization != "acme" {
		t.Fatalf("Organization %q", got.Organization)
	}
	only, err := s.ListSignals(ctx, SignalFilter{Organization: "acme"})
	ok(t, "list acme", err)
	if len(only) != 1 || only[0].ID != a.ID {
		t.Fatalf("acme sees %d signals", len(only))
	}
	all, err := s.ListSignals(ctx, SignalFilter{})
	ok(t, "list all", err)
	if len(all) != 2 {
		t.Fatalf("sovereign-admin sees %d signals, want 2", len(all))
	}
	// The same holds inside a transaction.
	write(t, s, func(tx Tx) error {
		_, err := tx.GetSignal(ctx, "beta", a.ID)
		is(t, "another Organization's signal in a transaction", err, ErrNotFound)
		return nil
	})
}

func testReferences(t *testing.T, s Store) {
	acmeInc, betaInc := newIncident("acme", 0), newIncident("beta", 0)
	betaPrb := newProblem("beta", 0, "p", "")
	write(t, s, func(tx Tx) error {
		ok(t, "acme incident", tx.InsertIncident(ctx, acmeInc))
		ok(t, "beta incident", tx.InsertIncident(ctx, betaInc))
		return tx.InsertProblem(ctx, betaPrb)
	})
	sig := newSignal("acme", 0)
	sig.IncidentID = betaInc.ID
	is(t, "signal to another Organization's incident", tryWrite(s, func(tx Tx) error { return tx.InsertSignal(ctx, sig) }), ErrNotFound)
	sig.IncidentID = ids.NewIncident()
	is(t, "signal to an unknown incident", tryWrite(s, func(tx Tx) error { return tx.InsertSignal(ctx, sig) }), ErrNotFound)
	sig.IncidentID = acmeInc.ID
	write(t, s, func(tx Tx) error { return tx.InsertSignal(ctx, sig) })

	child := newIncident("acme", 1)
	child.ParentID = betaInc.ID
	is(t, "parent in another Organization", tryWrite(s, func(tx Tx) error { return tx.InsertIncident(ctx, child) }), ErrNotFound)
	child.ParentID = ""
	child.ProblemID = betaPrb.ID
	is(t, "problem in another Organization", tryWrite(s, func(tx Tx) error { return tx.InsertIncident(ctx, child) }), ErrNotFound)
	acmePrb := newProblem("acme", 0, "p", "")
	child.ParentID, child.ProblemID = acmeInc.ID, acmePrb.ID
	write(t, s, func(tx Tx) error {
		ok(t, "problem", tx.InsertProblem(ctx, acmePrb))
		return tx.InsertIncident(ctx, child)
	})

	ev := IncidentEvent{Organization: "acme", IncidentID: betaInc.ID, At: t0, ActorKind: ByRules, Actor: "r", Kind: "opened"}
	is(t, "event on another Organization's incident", tryWrite(s, func(tx Tx) error { _, err := tx.AppendIncidentEvent(ctx, ev); return err }), ErrNotFound)
	fb := Feedback{Organization: "acme", IncidentID: betaInc.ID, Person: "a@acme.test", Kind: FeedbackDetach, At: t0}
	is(t, "feedback on another Organization's incident", tryWrite(s, func(tx Tx) error { _, err := tx.AddFeedback(ctx, fb); return err }), ErrNotFound)
	_, err := s.ListIncidentEvents(ctx, "acme", betaInc.ID)
	is(t, "timeline of another Organization's incident", err, ErrNotFound)
	_, err = s.ListFeedback(ctx, "acme", betaInc.ID)
	is(t, "feedback of another Organization's incident", err, ErrNotFound)
}

func testUpdates(t *testing.T, s Store) {
	inc := newIncident("acme", 0)
	sig := newSignal("acme", 0)
	prb := newProblem("acme", 0, "disk full", "")
	j := newJudgement("acme", 0, "correlator", "g1")
	write(t, s, func(tx Tx) error {
		ok(t, "incident", tx.InsertIncident(ctx, inc))
		ok(t, "signal", tx.InsertSignal(ctx, sig))
		ok(t, "problem", tx.InsertProblem(ctx, prb))
		return tx.InsertJudgement(ctx, j)
	})
	sig.State, sig.LastSeen, sig.IncidentID = SignalCleared, at(30), inc.ID
	inc.Severity, inc.SeveritySetBy, inc.State, inc.ResolvedAt, inc.ProblemID = 1, ByPerson, IncidentResolved, ptr(at(40)), prb.ID
	prb.State, prb.Statement = ProblemKnown, "the volume fills every night"
	j.Answer, j.AnsweredAt, j.Outcome = json.RawMessage(`{"decisions":[{"groupId":"g1"}]}`), ptr(at(1)), OutcomeApplied
	write(t, s, func(tx Tx) error {
		ok(t, "signal", tx.UpdateSignal(ctx, sig))
		ok(t, "incident", tx.UpdateIncident(ctx, inc))
		ok(t, "problem", tx.UpdateProblem(ctx, prb))
		return tx.UpdateJudgement(ctx, j)
	})
	gotSig, err := s.GetSignal(ctx, "acme", sig.ID)
	ok(t, "signal", err)
	same(t, "updated signal", gotSig, sig)
	gotInc, err := s.GetIncident(ctx, "acme", inc.ID)
	ok(t, "incident", err)
	same(t, "updated incident", gotInc, inc)
	gotPrb, err := s.GetProblem(ctx, "acme", prb.ID)
	ok(t, "problem", err)
	same(t, "updated problem", gotPrb, prb)
	gotJ, err := s.GetJudgement(ctx, "acme", j.ID)
	ok(t, "judgement", err)
	same(t, "updated judgement", gotJ, j)

	// An update in another Organization, or of an unknown row, finds nothing.
	for name, fn := range map[string]func(tx Tx) error{
		"signal of another Organization": func(tx Tx) error { x := sig; x.Organization = "beta"; return tx.UpdateSignal(ctx, x) },
		"incident of another Organization": func(tx Tx) error {
			x := inc
			x.Organization = "beta"
			x.ProblemID = ""
			return tx.UpdateIncident(ctx, x)
		},
		"problem of another Organization": func(tx Tx) error { x := prb; x.Organization = "beta"; return tx.UpdateProblem(ctx, x) },
		"judgement of another Organization": func(tx Tx) error {
			x := j
			x.Organization = "beta"
			return tx.UpdateJudgement(ctx, x)
		},
		"unknown signal":    func(tx Tx) error { return tx.UpdateSignal(ctx, newSignal("acme", 1)) },
		"unknown incident":  func(tx Tx) error { return tx.UpdateIncident(ctx, newIncident("acme", 1)) },
		"unknown problem":   func(tx Tx) error { return tx.UpdateProblem(ctx, newProblem("acme", 1, "x", "")) },
		"unknown judgement": func(tx Tx) error { return tx.UpdateJudgement(ctx, newJudgement("acme", 1, "x", "y")) },
	} {
		is(t, name, tryWrite(s, fn), ErrNotFound)
	}
	// An invalid update is refused and the row keeps its values.
	bad := inc
	bad.Severity = 9
	is(t, "invalid update", tryWrite(s, func(tx Tx) error { return tx.UpdateIncident(ctx, bad) }), ErrInvalid)
	still, err := s.GetIncident(ctx, "acme", inc.ID)
	ok(t, "incident after invalid update", err)
	same(t, "incident after invalid update", still, inc)
}

func testListSignals(t *testing.T, s Store) {
	inc := newIncident("acme", 0)
	var made []Signal
	write(t, s, func(tx Tx) error {
		ok(t, "incident", tx.InsertIncident(ctx, inc))
		for i, n := range []int{5, 1, 3, 2, 4} {
			sig := newSignal("acme", n)
			if i%2 == 0 {
				sig.IncidentID = inc.ID
			}
			if n == 3 {
				sig.State = SignalCleared
			}
			ok(t, "signal", tx.InsertSignal(ctx, sig))
			made = append(made, sig)
		}
		// Two signals with the same first seen: ordered by identifier.
		tie := newSignal("acme", 1)
		tie.Fingerprint = "fp-tie"
		made = append(made, tie)
		return tx.InsertSignal(ctx, tie)
	})
	all, err := s.ListSignals(ctx, SignalFilter{Organization: "acme"})
	ok(t, "list", err)
	var order []string
	for _, sig := range all {
		order = append(order, sig.Fingerprint)
	}
	first, second := "fp-1", "fp-tie"
	if made[5].ID < made[1].ID {
		first, second = "fp-tie", "fp-1"
	}
	same(t, "order by first seen, then identifier", order, []string{first, second, "fp-2", "fp-3", "fp-4", "fp-5"})

	byIncident, err := s.ListSignals(ctx, SignalFilter{Organization: "acme", IncidentID: inc.ID})
	ok(t, "by incident", err)
	if len(byIncident) != 3 {
		t.Fatalf("by incident: %d, want 3", len(byIncident))
	}
	cleared, err := s.ListSignals(ctx, SignalFilter{State: SignalCleared})
	ok(t, "by state", err)
	if len(cleared) != 1 || cleared[0].Fingerprint != "fp-3" {
		t.Fatalf("cleared: %d", len(cleared))
	}
	byPrint, err := s.ListSignals(ctx, SignalFilter{Organization: "acme", Fingerprint: "fp-4"})
	ok(t, "by fingerprint", err)
	if len(byPrint) != 1 || byPrint[0].ID != made[4].ID {
		t.Fatalf("by fingerprint: %d", len(byPrint))
	}
	two, err := s.ListSignals(ctx, SignalFilter{Limit: 2})
	ok(t, "limit", err)
	if len(two) != 2 || two[0].Fingerprint != first {
		t.Fatalf("limit 2: %d", len(two))
	}
}

func testListIncidents(t *testing.T, s Store) {
	var newest Incident
	write(t, s, func(tx Tx) error {
		for i := 0; i < MaxLimit+1; i++ {
			in := newIncident("acme", i)
			if i%2 == 1 {
				in.State = IncidentMitigated
			}
			ok(t, "incident", tx.InsertIncident(ctx, in))
			newest = in
		}
		return tx.InsertIncident(ctx, newIncident("beta", 5000))
	})
	def, err := s.ListIncidents(ctx, IncidentFilter{Organization: "acme"})
	ok(t, "default limit", err)
	if len(def) != DefaultLimit || def[0].ID != newest.ID {
		t.Fatalf("default limit: %d rows, first %s", len(def), def[0].ID)
	}
	for i := 1; i < len(def); i++ {
		if def[i].OpenedAt.After(def[i-1].OpenedAt) {
			t.Fatalf("row %d is newer than the row before it", i)
		}
	}
	most, err := s.ListIncidents(ctx, IncidentFilter{Limit: MaxLimit * 5})
	ok(t, "most", err)
	if len(most) != MaxLimit {
		t.Fatalf("limit above the maximum: %d rows, want %d", len(most), MaxLimit)
	}
	if most[0].Organization != "beta" {
		t.Fatal("the newest incident of all Organizations is not first")
	}
	mitigated, err := s.ListIncidents(ctx, IncidentFilter{Organization: "acme", State: IncidentMitigated, Limit: MaxLimit})
	ok(t, "by state", err)
	if len(mitigated) != MaxLimit/2 {
		t.Fatalf("mitigated: %d, want %d", len(mitigated), MaxLimit/2)
	}
	// Equal opening times: ordered by identifier, the greater first.
	s2a, s2b := newIncident("gamma", 0), newIncident("gamma", 0)
	write(t, s, func(tx Tx) error {
		ok(t, "a", tx.InsertIncident(ctx, s2a))
		return tx.InsertIncident(ctx, s2b)
	})
	tie, err := s.ListIncidents(ctx, IncidentFilter{Organization: "gamma"})
	ok(t, "tie", err)
	if len(tie) != 2 || tie[0].ID < tie[1].ID {
		t.Fatal("equal opening times are not ordered by identifier, greater first")
	}
}

func testTimeline(t *testing.T, s Store) {
	inc := newIncident("acme", 0)
	write(t, s, func(tx Tx) error { return tx.InsertIncident(ctx, inc) })
	var numbers []int64
	kinds := []string{"opened", "note", "severity", "note"}
	for i, k := range kinds {
		write(t, s, func(tx Tx) error {
			// An earlier time than the entry before: the order is the order of appending.
			n, err := tx.AppendIncidentEvent(ctx, IncidentEvent{ID: 999, Organization: "acme", IncidentID: inc.ID,
				At: at(10 - i), ActorKind: ByAgent, Actor: "scribe", Kind: k, Data: json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))})
			numbers = append(numbers, n)
			return err
		})
	}
	for i := 1; i < len(numbers); i++ {
		if numbers[i] <= numbers[i-1] {
			t.Fatalf("numbers do not rise: %v", numbers)
		}
	}
	events, err := s.ListIncidentEvents(ctx, "", inc.ID)
	ok(t, "timeline", err)
	if len(events) != len(kinds) {
		t.Fatalf("%d entries, want %d", len(events), len(kinds))
	}
	for i, e := range events {
		if e.ID != numbers[i] || e.Kind != kinds[i] || e.IncidentID != inc.ID {
			t.Fatalf("entry %d: %+v", i, e)
		}
		same(t, "entry data", e.Data, json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)))
	}
	unknown := ids.NewIncident()
	is(t, "append to an unknown incident", tryWrite(s, func(tx Tx) error {
		_, err := tx.AppendIncidentEvent(ctx, IncidentEvent{Organization: "acme", IncidentID: unknown, At: t0,
			ActorKind: ByRules, Actor: "r", Kind: "opened"})
		return err
	}), ErrNotFound)
	_, err = s.ListIncidentEvents(ctx, "acme", unknown)
	is(t, "timeline of an unknown incident", err, ErrNotFound)
	empty := newIncident("acme", 1)
	write(t, s, func(tx Tx) error { return tx.InsertIncident(ctx, empty) })
	none, err := s.ListIncidentEvents(ctx, "acme", empty.ID)
	ok(t, "empty timeline", err)
	if len(none) != 0 {
		t.Fatalf("another incident's timeline has %d entries", len(none))
	}
}

func testSearchProblems(t *testing.T, s Store) {
	p1 := newProblem("acme", 1, "Disk full on db", "")
	p2 := newProblem("acme", 2, "certificate expiry", "The DISK of the proxy is fine")
	p3 := newProblem("acme", 3, "slow queries", "index missing")
	p4 := newProblem("beta", 4, "disk full", "")
	write(t, s, func(tx Tx) error {
		for _, p := range []Problem{p1, p2, p3, p4} {
			ok(t, "problem", tx.InsertProblem(ctx, p))
		}
		return nil
	})
	idsOf := func(ps []Problem) []string {
		var out []string
		for _, p := range ps {
			out = append(out, p.ID)
		}
		return out
	}
	got, err := s.SearchProblems(ctx, "acme", "disk", 0)
	ok(t, "search", err)
	same(t, "title or statement, any case, newest first", idsOf(got), []string{p2.ID, p1.ID})
	got, err = s.SearchProblems(ctx, "", "DISK FULL", 0)
	ok(t, "search all", err)
	same(t, "every Organization", idsOf(got), []string{p4.ID, p1.ID})
	got, err = s.SearchProblems(ctx, "acme", "", 2)
	ok(t, "empty query", err)
	same(t, "empty query with limit", idsOf(got), []string{p3.ID, p2.ID})
	got, err = s.SearchProblems(ctx, "acme", "nothing matches", 0)
	ok(t, "no match", err)
	if len(got) != 0 {
		t.Fatalf("%d matches", len(got))
	}
	_, err = s.GetProblem(ctx, "beta", p1.ID)
	is(t, "another Organization's problem", err, ErrNotFound)
}

func testJudgements(t *testing.T, s Store) {
	undecided := newJudgement("acme", 1, "correlator", "g1")
	timeout := newJudgement("acme", 2, "correlator", "g2")
	timeout.Outcome = OutcomeTimeout
	applied := newJudgement("acme", 3, "investigator", "INC-x")
	applied.SubjectKind, applied.Outcome = "incident", OutcomeApplied
	applied.Answer, applied.AnsweredAt = json.RawMessage(`{"severity":2}`), ptr(at(4))
	other := newJudgement("beta", 4, "correlator", "g1")
	write(t, s, func(tx Tx) error {
		for _, j := range []Judgement{undecided, timeout, applied, other} {
			ok(t, "judgement", tx.InsertJudgement(ctx, j))
		}
		return nil
	})
	list := func(f JudgementFilter) []string {
		t.Helper()
		got, err := s.ListJudgements(ctx, f)
		ok(t, "list", err)
		var out []string
		for _, j := range got {
			out = append(out, j.ID)
		}
		return out
	}
	same(t, "acme, newest first", list(JudgementFilter{Organization: "acme"}), []string{applied.ID, timeout.ID, undecided.ID})
	same(t, "by judge", list(JudgementFilter{Organization: "acme", Judge: "correlator"}), []string{timeout.ID, undecided.ID})
	same(t, "by outcome", list(JudgementFilter{Outcome: OutcomeTimeout}), []string{timeout.ID})
	same(t, "undecided", list(JudgementFilter{Undecided: true, Outcome: OutcomeApplied}), []string{other.ID, undecided.ID})
	same(t, "by subject", list(JudgementFilter{SubjectKind: "signal_group", SubjectID: "g1"}), []string{other.ID, undecided.ID})
	same(t, "limit", list(JudgementFilter{Limit: 1}), []string{other.ID})
	got, err := s.GetJudgement(ctx, "acme", applied.ID)
	ok(t, "get", err)
	same(t, "judgement", got, applied)
}

func testFeedback(t *testing.T, s Store) {
	inc := newIncident("acme", 0)
	write(t, s, func(tx Tx) error { return tx.InsertIncident(ctx, inc) })
	var nums []int64
	for _, k := range []string{FeedbackSeverity, FeedbackNotRelated, FeedbackMerge, FeedbackDetach} {
		write(t, s, func(tx Tx) error {
			n, err := tx.AddFeedback(ctx, Feedback{ID: 7, Organization: "acme", IncidentID: inc.ID, Person: "a@acme.test",
				Kind: k, Data: json.RawMessage(`{"k":"` + k + `"}`), At: at(1)})
			nums = append(nums, n)
			return err
		})
	}
	got, err := s.ListFeedback(ctx, "acme", inc.ID)
	ok(t, "list", err)
	if len(got) != 4 {
		t.Fatalf("%d rows", len(got))
	}
	for i, f := range got {
		if f.ID != nums[i] || (i > 0 && f.ID <= got[i-1].ID) {
			t.Fatalf("row %d has number %d, numbers given %v", i, f.ID, nums)
		}
	}
	if got[0].Kind != FeedbackSeverity || got[3].Kind != FeedbackDetach {
		t.Fatal("feedback is not in the order it was added")
	}
}

func testRollback(t *testing.T, s Store) {
	sig := newSignal("acme", 0)
	o := newOutbox("acme")
	boom := errors.New("boom")
	err := s.InTx(ctx, func(ctx context.Context, tx Tx) error {
		ok(t, "signal", tx.InsertSignal(ctx, sig))
		ok(t, "outbox", tx.Enqueue(ctx, o))
		// The transaction reads its own writes.
		got, err := tx.GetSignal(ctx, "acme", sig.ID)
		ok(t, "read own write", err)
		if got.ID != sig.ID {
			t.Fatal("read own write returned another row")
		}
		listed, err := tx.ListSignals(ctx, SignalFilter{Organization: "acme"})
		ok(t, "list own write", err)
		if len(listed) != 1 {
			t.Fatalf("the transaction lists %d of its own signals", len(listed))
		}
		return boom
	})
	is(t, "InTx returns the function's error", err, boom)
	_, err = s.GetSignal(ctx, "acme", sig.ID)
	is(t, "signal after rollback", err, ErrNotFound)
	pend, err := s.PendingOutbox(ctx, 0)
	ok(t, "pending", err)
	if len(pend) != 0 {
		t.Fatal("the outbox row of a failed transaction was kept")
	}
	// A refused write fails the whole transaction when the function returns its error.
	good := newSignal("acme", 1)
	bad := newSignal("acme", 2)
	bad.Source = "nowhere"
	err = tryWrite(s, func(tx Tx) error {
		ok(t, "good", tx.InsertSignal(ctx, good))
		return tx.InsertSignal(ctx, bad)
	})
	is(t, "second write refused", err, ErrInvalid)
	_, err = s.GetSignal(ctx, "acme", good.ID)
	is(t, "first write of a failed transaction", err, ErrNotFound)
}

func testPanic(t *testing.T, s Store) {
	sig := newSignal("acme", 0)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not reach the caller")
			}
		}()
		_ = s.InTx(ctx, func(ctx context.Context, tx Tx) error {
			ok(t, "signal", tx.InsertSignal(ctx, sig))
			panic("stop")
		})
	}()
	_, err := s.GetSignal(ctx, "acme", sig.ID)
	is(t, "signal after panic", err, ErrNotFound)
	// The store still works.
	write(t, s, func(tx Tx) error { return tx.InsertSignal(ctx, sig) })
}

func testTxDone(t *testing.T, s Store) {
	var kept Tx
	write(t, s, func(tx Tx) error { kept = tx; return nil })
	is(t, "write after commit", kept.InsertSignal(ctx, newSignal("acme", 0)), ErrTxDone)
	_, err := kept.GetSignal(ctx, "acme", ids.New())
	is(t, "read after commit", err, ErrTxDone)
	_ = s.InTx(ctx, func(_ context.Context, tx Tx) error { kept = tx; return errors.New("no") })
	_, err = kept.ListIncidents(ctx, IncidentFilter{})
	is(t, "read after rollback", err, ErrTxDone)
}

func testOutbox(t *testing.T, s Store) {
	// Identifiers made in falling order, all at the same time: pending rows come in the order
	// they were enqueued, not by identifier or time.
	var rows []OutboxRow
	for i := 0; i < 5; i++ {
		rows = append(rows, newOutbox("acme"))
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	rows[2].PublishedAt = ptr(at(1)) // ignored on enqueue
	inc := newIncident("acme", 0)
	write(t, s, func(tx Tx) error {
		ok(t, "incident", tx.InsertIncident(ctx, inc))
		for _, o := range rows[:3] {
			ok(t, "enqueue", tx.Enqueue(ctx, o))
		}
		return nil
	})
	write(t, s, func(tx Tx) error {
		for _, o := range rows[3:] {
			ok(t, "enqueue", tx.Enqueue(ctx, o))
		}
		return nil
	})
	pend, err := s.PendingOutbox(ctx, 0)
	ok(t, "pending", err)
	var got, want []string
	for _, o := range pend {
		got = append(got, o.ID)
		if o.PublishedAt != nil {
			t.Fatal("a pending row has a publication time")
		}
	}
	for _, o := range rows {
		want = append(want, o.ID)
	}
	same(t, "insertion order", got, want)
	same(t, "payload", pend[0].Payload, json.RawMessage(`{"incidentId":"x"}`))

	ok(t, "mark", s.MarkPublished(ctx, rows[0].ID, at(2)))
	ok(t, "mark again", s.MarkPublished(ctx, rows[0].ID, at(3)))
	ok(t, "mark third", s.MarkPublished(ctx, rows[3].ID, at(2)))
	is(t, "mark unknown", s.MarkPublished(ctx, ids.New(), at(2)), ErrNotFound)
	pend, err = s.PendingOutbox(ctx, 2)
	ok(t, "pending after publish", err)
	got = nil
	for _, o := range pend {
		got = append(got, o.ID)
	}
	same(t, "pending after publish, limit 2", got, []string{rows[1].ID, rows[2].ID})
}

func testContextDone(t *testing.T, s Store) {
	done, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	err := s.InTx(done, func(context.Context, Tx) error { called = true; return nil })
	is(t, "InTx", err, context.Canceled)
	if called {
		t.Fatal("InTx ran its function on a done context")
	}
	_, err = s.GetSignal(done, "acme", ids.New())
	is(t, "GetSignal", err, context.Canceled)
	_, err = s.ListIncidents(done, IncidentFilter{})
	is(t, "ListIncidents", err, context.Canceled)
	_, err = s.PendingOutbox(done, 0)
	is(t, "PendingOutbox", err, context.Canceled)
	is(t, "MarkPublished", s.MarkPublished(done, ids.New(), t0), context.Canceled)
	is(t, "Ping", s.Ping(done), context.Canceled)

	// A context that ends inside the transaction: nothing is kept.
	inner, stop := context.WithCancel(ctx)
	sig := newSignal("acme", 0)
	err = s.InTx(inner, func(c context.Context, tx Tx) error {
		ok(t, "signal", tx.InsertSignal(c, sig))
		stop()
		return nil
	})
	if err == nil {
		t.Fatal("a transaction whose context ended before the commit reported success")
	}
	_, err = s.GetSignal(ctx, "acme", sig.ID)
	is(t, "signal of a transaction whose context ended", err, ErrNotFound)
}

func testConcurrent(t *testing.T, s Store) {
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- tryWrite(s, func(tx Tx) error { return tx.InsertSignal(ctx, newSignal("acme", i)) })
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		ok(t, "concurrent insert", err)
	}
	all, err := s.ListSignals(ctx, SignalFilter{Organization: "acme"})
	ok(t, "list", err)
	if len(all) != n {
		t.Fatalf("%d signals, want %d", len(all), n)
	}
}

func testPingClose(t *testing.T, s Store) {
	ok(t, "ping", s.Ping(ctx))
	ok(t, "close", s.Close())
	if s.Ping(ctx) == nil {
		t.Fatal("ping after close reported success")
	}
}
