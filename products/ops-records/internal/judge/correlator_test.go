package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/correlate"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

func TestValidateCorrelation(t *testing.T) {
	req := correlate.Request{Task: "correlate", Organization: "acme",
		Groups: []correlate.RequestGroup{
			{GroupID: "g1", Signals: []correlate.RequestSignal{{SignalID: ulid(1), SeverityFloor: 3}, {SignalID: ulid(2), SeverityFloor: 2}}},
			{GroupID: "g2", Signals: []correlate.RequestSignal{{SignalID: ulid(3), SeverityFloor: 4}}}},
		Candidates: []correlate.Candidate{{IncidentID: incID(1)}},
		Allowed:    []string{correlate.Attach, correlate.New, correlate.Watch}}
	noCandidates := req
	noCandidates.Candidates, noCandidates.Allowed = nil, []string{correlate.New, correlate.Watch}
	noWatch := req
	noWatch.Allowed = []string{correlate.Attach, correlate.New}
	g2 := `{"groupId":"g2","decision":"new","confidence":1,"severity":4}`
	attach := `{"groupId":"g1","decision":"attach","incidentId":"` + incID(1) + `","causalChain":"the database is down","confidence":0.8,"severity":2}`
	two := func(first string) string { return `{"decisions":[` + first + `,` + g2 + `]}` }
	cases := []struct {
		name   string
		req    correlate.Request
		answer string
		want   string // the decisions applied, or "" when the answer is rejected
	}{
		{"valid attach and new", req, two(attach), "g1 attach " + incID(1) + " 2; g2 new  4"},
		{"watch", req, two(`{"groupId":"g1","decision":"watch","confidence":0.2,"severity":2}`), "g1 watch  2; g2 new  4"},
		{"spaces around the document", req, " \n" + two(attach) + "\n ", "g1 attach " + incID(1) + " 2; g2 new  4"},
		{"short causal chain becomes new", req, two(strings.Replace(attach, "the database is down", "db down", 1)), "g1 new  2; g2 new  4"},
		{"low confidence becomes new", req, two(strings.Replace(attach, "0.8", "0.49", 1)), "g1 new  2; g2 new  4"},
		{"confidence 0.5 stays attach", req, two(strings.Replace(attach, "0.8", "0.5", 1)), "g1 attach " + incID(1) + " 2; g2 new  4"},
		{"a new keeps no incident", req, two(`{"groupId":"g1","decision":"new","incidentId":"` + incID(1) + `","confidence":1,"severity":1}`), "g1 new  1; g2 new  4"},
		{"more urgent than the floor", req, `{"decisions":[` + attach + `,{"groupId":"g2","decision":"new","confidence":1,"severity":1}]}`, "g1 attach " + incID(1) + " 2; g2 new  1"},
		{"no candidates, new and watch", noCandidates, two(`{"groupId":"g1","decision":"new","confidence":1,"severity":2}`), "g1 new  2; g2 new  4"},
		{"less urgent than the floor", req, two(strings.Replace(attach, `"severity":2`, `"severity":3`, 1)), ""},
		{"not JSON", req, `attach g1 to the database incident`, ""},
		{"a JSON string", req, `"attach"`, ""},
		{"a list", req, `[` + attach + `]`, ""},
		{"null", req, `null`, ""},
		{"another top key", req, `{"decisions":[` + attach + `,` + g2 + `],"note":"x"}`, ""},
		{"decisions missing", req, `{}`, ""},
		{"decisions null", req, `{"decisions":null}`, ""},
		{"decisions an object", req, `{"decisions":{"g1":"attach"}}`, ""},
		{"data after the document", req, two(attach) + `{}`, ""},
		{"an unknown key", req, two(strings.Replace(attach, `"severity":2`, `"severity":2,"why":"x"`, 1)), ""},
		{"severity missing", req, two(strings.Replace(attach, `,"severity":2`, ``, 1)), ""},
		{"severity not an integer", req, two(strings.Replace(attach, `"severity":2`, `"severity":1.5`, 1)), ""},
		{"severity 0", req, two(strings.Replace(attach, `"severity":2`, `"severity":0`, 1)), ""},
		{"severity a string", req, two(strings.Replace(attach, `"severity":2`, `"severity":"2"`, 1)), ""},
		{"confidence above 1", req, two(strings.Replace(attach, "0.8", "1.5", 1)), ""},
		{"confidence a string", req, two(strings.Replace(attach, "0.8", `"high"`, 1)), ""},
		{"confidence null", req, two(strings.Replace(attach, "0.8", `null`, 1)), ""},
		{"group not a string", req, two(strings.Replace(attach, `"g1"`, `1`, 1)), ""},
		{"unknown decision", req, two(strings.Replace(attach, `"attach"`, `"resolve"`, 1)), ""},
		{"a group missing", req, `{"decisions":[` + attach + `]}`, ""},
		{"a group twice", req, `{"decisions":[` + attach + `,` + attach + `,` + g2 + `]}`, ""},
		{"a group not offered", req, `{"decisions":[` + attach + `,` + g2 + `,` + strings.Replace(g2, "g2", "g3", 1) + `]}`, ""},
		{"attach not allowed", noCandidates, two(attach), ""},
		{"watch not allowed", noWatch, two(`{"groupId":"g1","decision":"watch","confidence":0.2,"severity":2}`), ""},
		{"attach to an incident not offered", req, two(strings.Replace(attach, incID(1), incID(2), 1)), ""},
		{"attach without an incident", req, two(strings.Replace(attach, `"incidentId":"`+incID(1)+`",`, ``, 1)), ""},
		{"incident null", req, two(strings.Replace(attach, `"`+incID(1)+`"`, `null`, 1)), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ValidateCorrelation(c.req, json.RawMessage(c.answer))
			if c.want == "" {
				if !errors.Is(err, ErrInvalidAnswer) {
					t.Fatalf("accepted %s as %v (%v)", c.answer, got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected: %v", err)
			}
			var parts []string
			for _, d := range got {
				parts = append(parts, fmt.Sprintf("%s %s %s %d", d.GroupID, d.Decision, d.IncidentID, d.Severity))
			}
			if s := strings.Join(parts, "; "); s != c.want {
				t.Fatalf("got %q, want %q", s, c.want)
			}
		})
	}
}

func TestRevise(t *testing.T) {
	agent := correlate.Actor{Kind: store.ByAgent, Name: Correlator}
	sameEntity := webSignal
	sameEntity.ID, sameEntity.Fingerprint, sameEntity.IncidentID = ulid(3), "alert/c1/a3", incID(1)
	sameEntity.FirstSeen, sameEntity.LastSeen = t0.Add(-5*time.Minute), t0.Add(-5*time.Minute)
	resolved := dbIncident
	resolvedAt := t0.Add(time.Minute)
	resolved.State, resolved.ResolvedAt = store.IncidentResolved, &resolvedAt
	decision := func(kind, incident string, severity int) correlate.Decision {
		return correlate.Decision{GroupID: "g1", Decision: kind, IncidentID: incident,
			CausalChain: "web calls the database that is down", Confidence: 0.8, Severity: severity}
	}
	id := func(n int) string { // incident n; 0 is no incident
		if n == 0 {
			return ""
		}
		return incID(n)
	}
	cases := []struct {
		name      string
		signals   []store.Signal // besides webSignal
		decision  correlate.Decision
		severity  int  // the severity of incident 1 when it is not 0
		person    bool // a person sets the severity of the rules' incident before the late answer
		resolve   bool // incident 1 is resolved before the late answer
		merge     bool // a person merges the rules' incident into incident 1 before the late answer
		ruleWatch bool // the rules decide watch: they write no incident for the group
		ruled     int  // the incident the rules chose; 0 is none
		wantIn    int  // the incident webSignal ends in; 0 is none
		wantRuled string
		check     func(t *testing.T, r *rig)
	}{
		{name: "attach after the rules opened: moved and merged", signals: []store.Signal{dbSignal},
			decision: decision(correlate.Attach, incID(1), 2), ruled: ruleIncidentID, wantIn: 1,
			wantRuled: "rules:opened,agent:signals_detached,agent:merged",
			check: func(t *testing.T, r *rig) {
				if in := r.incident(t, incID(ruleIncidentID)); in.ParentID != incID(1) {
					t.Fatalf("parent %q", in.ParentID)
				}
			}},
		{name: "new after the rules opened: re-rated", signals: []store.Signal{dbSignal},
			decision: decision(correlate.New, "", 1), ruled: ruleIncidentID, wantIn: ruleIncidentID,
			wantRuled: "rules:opened,agent:severity,agent:judge_late",
			check: func(t *testing.T, r *rig) {
				if in := r.incident(t, incID(ruleIncidentID)); in.Severity != 1 || in.SeveritySetBy != store.ByAgent {
					t.Fatalf("incident %+v", in)
				}
			}},
		{name: "new at the floor after the rules opened: nothing to re-rate", signals: []store.Signal{dbSignal},
			decision: decision(correlate.New, "", 3), ruled: ruleIncidentID, wantIn: ruleIncidentID,
			wantRuled: "rules:opened,agent:judge_late"},
		{name: "watch: kept", signals: []store.Signal{dbSignal},
			decision: decision(correlate.Watch, "", 3), ruled: ruleIncidentID, wantIn: ruleIncidentID,
			wantRuled: "rules:opened,agent:judge_late"},
		{name: "a person acted: kept", signals: []store.Signal{dbSignal}, person: true,
			decision: decision(correlate.Attach, incID(1), 2), ruled: ruleIncidentID, wantIn: ruleIncidentID,
			wantRuled: "rules:opened,agent:judge_late"},
		{name: "attach to an incident resolved since: a new incident", signals: []store.Signal{dbSignal}, resolve: true,
			decision: decision(correlate.Attach, incID(1), 2), ruled: ruleIncidentID, wantIn: ruleIncidentID + 1,
			wantRuled: "rules:opened,agent:signals_detached,agent:merged"},
		{name: "new after the rules attached: detached, not merged", signals: []store.Signal{dbSignal, sameEntity},
			decision: decision(correlate.New, "", 3), ruled: 1, wantIn: ruleIncidentID,
			wantRuled: "rules:signals_attached,agent:signals_detached",
			check: func(t *testing.T, r *rig) {
				if in := r.incident(t, incID(1)); in.ParentID != "" {
					t.Fatalf("incident 1 was merged into %s", in.ParentID)
				}
				if k := r.kinds(t, incID(ruleIncidentID)); k != "agent:opened" {
					t.Fatalf("timeline of the new incident %s", k)
				}
			}},
		{name: "attach where the rules attached: re-rated", signals: []store.Signal{dbSignal, sameEntity},
			decision: decision(correlate.Attach, incID(1), 1), ruled: 1, wantIn: 1,
			wantRuled: "rules:signals_attached,agent:severity,agent:judge_late"},
		{name: "attach where the rules attached, less urgent: never lowered", signals: []store.Signal{dbSignal, sameEntity},
			severity: 1, decision: decision(correlate.Attach, incID(1), 3), ruled: 1, wantIn: 1,
			wantRuled: "rules:signals_attached,agent:judge_late",
			check: func(t *testing.T, r *rig) {
				if in := r.incident(t, incID(1)); in.Severity != 1 {
					t.Fatalf("severity lowered to %d", in.Severity)
				}
			}},
		{name: "the rules' incident resolved since: kept", signals: []store.Signal{dbSignal, sameEntity}, resolve: true,
			decision: decision(correlate.New, "", 3), ruled: 1, wantIn: 1,
			wantRuled: "rules:signals_attached,agent:judge_late",
			check: func(t *testing.T, r *rig) {
				if _, err := r.st.GetIncident(context.Background(), "acme", incID(ruleIncidentID)); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("a new incident was opened (%v)", err)
				}
			}},
		{name: "the rules' incident merged since: kept", signals: []store.Signal{dbSignal}, merge: true,
			decision: decision(correlate.New, "", 1), ruled: ruleIncidentID, wantIn: ruleIncidentID,
			wantRuled: "rules:opened,agent:judge_late",
			check: func(t *testing.T, r *rig) {
				if in := r.incident(t, incID(ruleIncidentID)); in.Severity != 3 || in.ParentID != incID(1) {
					t.Fatalf("incident %+v", in)
				}
			}},
		{name: "the rules wrote no incident: left alone", signals: []store.Signal{dbSignal}, ruleWatch: true,
			decision: decision(correlate.Attach, incID(1), 2), ruled: 0, wantIn: 0,
			check: func(t *testing.T, r *rig) {
				if k := r.kinds(t, incID(1)); k != "" {
					t.Fatalf("timeline of incident 1 %s", k)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			first := dbIncident
			if c.severity != 0 {
				first.Severity = c.severity
			}
			r := newRig(t, nil, []store.Incident{first}, append([]store.Signal{webSignal}, c.signals...))
			ctx := context.Background()
			batches, views, err := correlate.Load(ctx, r.st, "")
			if err != nil || len(batches) != 1 {
				t.Fatalf("batches %d (%v)", len(batches), err)
			}
			p := correlate.Prepare(batches[0], views["acme"])
			a := correlate.Applier{NewIncidentID: counter()}
			rules := p.RuleResult.Decisions
			if c.ruleWatch {
				rules = []correlate.Decision{{GroupID: "g1", Decision: correlate.Watch}}
			}
			var written []correlate.GroupResult
			err = r.st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
				written, err = a.Apply(ctx, tx, p.Groups, rules, correlate.Rules, t0)
				return err
			})
			if err != nil || len(written) != 1 || written[0].IncidentID != id(c.ruled) {
				t.Fatalf("rules wrote %+v (%v), want incident %d", written, err, c.ruled)
			}
			err = r.st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
				if c.merge {
					in, err := tx.GetIncident(ctx, "acme", incID(c.ruled))
					if err != nil {
						return err
					}
					in.ParentID = incID(1)
					if err := tx.UpdateIncident(ctx, in); err != nil {
						return err
					}
				}
				if c.person {
					in, err := tx.GetIncident(ctx, "acme", incID(c.ruled))
					if err != nil {
						return err
					}
					in.SeveritySetBy = store.ByPerson
					if err := tx.UpdateIncident(ctx, in); err != nil {
						return err
					}
				}
				if c.resolve {
					return tx.UpdateIncident(ctx, resolved)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = r.st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
				return Revise(ctx, tx, a, p.Groups, written, []correlate.Decision{c.decision}, agent, t0.Add(time.Hour))
			})
			if err != nil {
				t.Fatal(err)
			}
			if s := r.signal(t, webSignal.ID); s.IncidentID != id(c.wantIn) {
				t.Fatalf("signal in %q, want %q", s.IncidentID, id(c.wantIn))
			}
			if c.ruled != 0 {
				if k := r.kinds(t, incID(c.ruled)); k != c.wantRuled {
					t.Fatalf("timeline of the rules' incident %s, want %s", k, c.wantRuled)
				}
			}
			if c.check != nil {
				c.check(t, r)
			}
		})
	}
	// Validation decides every offered group and the rules write the same groups: a written
	// group that is not decided, or not offered, is a mistake of the caller.
	mistakes := []struct {
		name    string
		groups  []correlate.Group
		written []correlate.GroupResult
	}{
		{name: "a written group the answer does not decide: an error",
			groups:  []correlate.Group{{ID: "g2", Signals: []store.Signal{webSignal}}},
			written: []correlate.GroupResult{{GroupID: "g2", Applied: correlate.AppliedNew, IncidentID: incID(1)}}},
		{name: "a written group that is not offered: an error",
			written: []correlate.GroupResult{{GroupID: "g1", Applied: correlate.AppliedNew, IncidentID: incID(1)}}},
	}
	for _, m := range mistakes {
		t.Run(m.name, func(t *testing.T) {
			r := newRig(t, nil, []store.Incident{dbIncident}, []store.Signal{dbSignal, webSignal})
			err := r.st.InTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
				return Revise(ctx, tx, correlate.Applier{NewIncidentID: counter()}, m.groups, m.written,
					[]correlate.Decision{decision(correlate.New, "", 3)}, agent, t0.Add(time.Hour))
			})
			if err == nil || !strings.Contains(err.Error(), "judge: revise:") {
				t.Fatalf("Revise returned %v, want an error", err)
			}
			if k := r.kinds(t, incID(1)); k != "" {
				t.Fatalf("timeline of incident 1 %s", k)
			}
		})
	}
}
