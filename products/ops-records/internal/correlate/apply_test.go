package correlate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// seed writes incidents (without their signals' incident) and then signals into a new store.
func seed(t *testing.T, incidents []store.Incident, signals []store.Signal) *store.Memory {
	t.Helper()
	st := store.NewMemory()
	t.Cleanup(func() { _ = st.Close() })
	err := st.InTx(context.Background(), func(ctx context.Context, tx store.Tx) error {
		for _, in := range incidents {
			if err := tx.InsertIncident(ctx, in); err != nil {
				return err
			}
		}
		for _, s := range signals {
			if err := tx.InsertSignal(ctx, s); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func incident(n, severity int, mutate func(*store.Incident)) store.Incident {
	in := store.Incident{ID: incID(n), Organization: "acme", Title: fmt.Sprintf("incident %d", n), Severity: severity,
		SeveritySetBy: store.ByRules, State: store.IncidentOpen, OpenedBy: store.ByRules, OpenedAt: t0.Add(-time.Hour)}
	if mutate != nil {
		mutate(&in)
	}
	return in
}

// counter makes incident identifiers 900, 901, ...
func counter() func() string {
	n := 899
	return func() string { n++; return incID(n) }
}

func TestApply(t *testing.T) {
	web := sig(1, "deployment", "acme/web", 3, 0)
	db := sig(2, "statefulset", "acme/db", 2, time.Minute)
	old := with(sig(3, "deployment", "acme/web", 3, -time.Hour), func(s *store.Signal) { s.IncidentID = incID(1) })
	urgentOld := with(sig(4, "deployment", "acme/api", 1, -time.Hour), func(s *store.Signal) { s.IncidentID = incID(1) })
	g1 := Group{ID: "g1", Key: "acme", Floor: 2, Signals: []store.Signal{web, db}}
	attach := func(id string, sev int) Decision {
		return Decision{GroupID: "g1", Decision: Attach, IncidentID: id, CausalChain: "rules: x", Confidence: 0.9, Severity: sev}
	}
	newDecision := Decision{GroupID: "g1", Decision: New, CausalChain: "rules: none", Confidence: 1, Severity: 2}
	type state struct {
		incidents []store.Incident
		signals   []store.Signal
		events    map[string][]string // incident ID -> kinds
	}
	cases := []struct {
		name      string
		incidents []store.Incident
		signals   []store.Signal
		groups    []Group
		decisions []Decision
		by        Actor
		wantErr   error
		want      string // fmt of the results
		check     func(t *testing.T, s state)
	}{
		{"new opens an incident with every free signal", nil, []store.Signal{web, db}, []Group{g1}, []Decision{newDecision}, Rules, nil,
			"[{g1 opened " + incID(900) + " [" + web.ID + " " + db.ID + "] }]", func(t *testing.T, s state) {
				in := s.incidents[0]
				if len(s.incidents) != 1 || in.Title != db.Title || in.Severity != 2 || in.SeveritySetBy != store.ByRules ||
					in.OpenedBy != store.ByRules || in.State != store.IncidentOpen || !in.OpenedAt.Equal(t0) {
					t.Fatalf("incidents %+v", s.incidents)
				}
				if fmt.Sprint(s.events[in.ID]) != "[opened]" {
					t.Fatalf("events %v", s.events)
				}
			}},
		{"new never rates below the floor", nil, []store.Signal{web, db}, []Group{g1},
			[]Decision{{GroupID: "g1", Decision: New, Confidence: 1, Severity: 4}}, Rules, nil,
			"[{g1 opened " + incID(900) + " [" + web.ID + " " + db.ID + "] }]", func(t *testing.T, s state) {
				if s.incidents[0].Severity != 2 {
					t.Fatalf("severity %d", s.incidents[0].Severity)
				}
			}},
		{"attach joins the incident and raises its severity to the floor",
			[]store.Incident{incident(1, 3, nil)}, []store.Signal{old, web, db}, []Group{g1}, []Decision{attach(incID(1), 3)}, Rules, nil,
			"[{g1 attached " + incID(1) + " [" + web.ID + " " + db.ID + "] }]", func(t *testing.T, s state) {
				in := s.incidents[0]
				if in.Severity != 2 || in.SeveritySetBy != store.ByRules {
					t.Fatalf("incident %+v", in)
				}
				if fmt.Sprint(s.events[in.ID]) != "[signals_attached severity]" {
					t.Fatalf("events %v", s.events)
				}
			}},
		{"attach keeps a severity that is urgent enough", []store.Incident{incident(1, 1, nil)}, []store.Signal{old, web, db},
			[]Group{g1}, []Decision{attach(incID(1), 1)}, Rules, nil,
			"[{g1 attached " + incID(1) + " [" + web.ID + " " + db.ID + "] }]", func(t *testing.T, s state) {
				if s.incidents[0].Severity != 1 || fmt.Sprint(s.events[incID(1)]) != "[signals_attached]" {
					t.Fatalf("incident %+v events %v", s.incidents[0], s.events)
				}
			}},
		{"a judge may lower the urgency, but not below a floor of the incident",
			[]store.Incident{incident(1, 1, nil)}, []store.Signal{old, web, db}, []Group{g1}, []Decision{attach(incID(1), 3)},
			Actor{Kind: store.ByAgent, Name: "correlator"}, nil,
			"[{g1 attached " + incID(1) + " [" + web.ID + " " + db.ID + "] }]", func(t *testing.T, s state) {
				if s.incidents[0].Severity != 2 || s.incidents[0].SeveritySetBy != store.ByAgent {
					t.Fatalf("incident %+v", s.incidents[0])
				}
			}},
		{"an old signal's floor holds the severity", []store.Incident{incident(1, 1, nil)}, []store.Signal{urgentOld, web, db},
			[]Group{g1}, []Decision{attach(incID(1), 3)}, Actor{Kind: store.ByAgent, Name: "correlator"}, nil,
			"[{g1 attached " + incID(1) + " [" + web.ID + " " + db.ID + "] }]", func(t *testing.T, s state) {
				if s.incidents[0].Severity != 1 {
					t.Fatalf("severity %d", s.incidents[0].Severity)
				}
			}},
		{"a severity set by a person is never changed",
			[]store.Incident{incident(1, 4, func(in *store.Incident) { in.SeveritySetBy = store.ByPerson })},
			[]store.Signal{web, db}, []Group{g1}, []Decision{attach(incID(1), 2)}, Rules, nil,
			"[{g1 attached " + incID(1) + " [" + web.ID + " " + db.ID + "] }]", func(t *testing.T, s state) {
				if s.incidents[0].Severity != 4 || s.incidents[0].SeveritySetBy != store.ByPerson {
					t.Fatalf("incident %+v", s.incidents[0])
				}
			}},
		{"attach to a merged incident joins the one it was merged into",
			[]store.Incident{incident(2, 2, nil), incident(1, 2, func(in *store.Incident) { in.ParentID = incID(2) })},
			[]store.Signal{web, db}, []Group{g1}, []Decision{attach(incID(1), 2)}, Rules, nil,
			"[{g1 attached " + incID(2) + " [" + web.ID + " " + db.ID + "] " + incID(1) + " was merged into " + incID(2) + "}]", nil},
		{"attach to a resolved incident opens a new one",
			[]store.Incident{incident(1, 2, func(in *store.Incident) { in.State, in.ResolvedAt = store.IncidentResolved, &t0 })},
			[]store.Signal{web, db}, []Group{g1}, []Decision{attach(incID(1), 2)}, Rules, nil,
			"[{g1 opened " + incID(900) + " [" + web.ID + " " + db.ID + "] " + incID(1) + " is resolved; a new incident was opened}]", nil},
		{"watch writes nothing", nil, []store.Signal{web, db}, []Group{g1},
			[]Decision{{GroupID: "g1", Decision: Watch, Confidence: 0.4, Severity: 2}}, Rules, nil,
			"[{g1 watching  [] }]", func(t *testing.T, s state) {
				if len(s.incidents) != 0 || s.signals[0].IncidentID != "" || s.signals[1].IncidentID != "" {
					t.Fatalf("state %+v", s)
				}
			}},
		{"a signal that joined an incident meanwhile is not moved",
			[]store.Incident{incident(1, 2, nil)}, []store.Signal{with(web, func(s *store.Signal) { s.IncidentID = incID(1) }), db},
			[]Group{g1}, []Decision{newDecision}, Rules, nil,
			"[{g1 opened " + incID(900) + " [" + db.ID + "] }]", nil},
		{"a group whose signals all joined meanwhile does nothing",
			[]store.Incident{incident(1, 2, nil)}, []store.Signal{with(web, func(s *store.Signal) { s.IncidentID = incID(1) }),
				with(db, func(s *store.Signal) { s.IncidentID = incID(1) })},
			[]Group{g1}, []Decision{newDecision}, Rules, nil, "[{g1 nothing  [] }]", nil},
		{"a group without a decision is refused", nil, []store.Signal{web, db}, []Group{g1}, nil, Rules, ErrBadDecision, "", nil},
		{"a decision for a group not offered is refused", nil, []store.Signal{web, db}, []Group{g1},
			[]Decision{newDecision, {GroupID: "g9", Decision: New, Severity: 2}}, Rules, ErrBadDecision, "", nil},
		{"a group decided twice is refused", nil, []store.Signal{web, db}, []Group{g1}, []Decision{newDecision, newDecision}, Rules, ErrBadDecision, "", nil},
		{"an unknown decision is refused", nil, []store.Signal{web, db}, []Group{g1},
			[]Decision{{GroupID: "g1", Decision: "merge", Severity: 2}}, Rules, ErrBadDecision, "", nil},
		{"attach without an incident is refused", nil, []store.Signal{web, db}, []Group{g1}, []Decision{attach("", 2)}, Rules, ErrBadDecision, "", nil},
		{"a severity of 0 is refused", nil, []store.Signal{web, db}, []Group{g1},
			[]Decision{{GroupID: "g1", Decision: New, Severity: 0}}, Rules, ErrBadDecision, "", nil},
		{"a person is not an actor of the rule path", nil, []store.Signal{web, db}, []Group{g1}, []Decision{newDecision},
			Actor{Kind: store.ByPerson, Name: "a@example.org"}, ErrBadDecision, "", nil},
		{"attach to an incident of another Organization is not found",
			[]store.Incident{incident(1, 2, func(in *store.Incident) { in.Organization = "beta" })},
			[]store.Signal{web, db}, []Group{g1}, []Decision{attach(incID(1), 2)}, Rules, store.ErrNotFound, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st := seed(t, c.incidents, c.signals)
			var got []GroupResult
			err := st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
				var err error
				got, err = Applier{NewIncidentID: counter()}.Apply(ctx, tx, c.groups, c.decisions, c.by, t0)
				return err
			})
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("error %v, want %v", err, c.wantErr)
				}
				after, _ := st.ListIncidents(ctx, store.IncidentFilter{})
				if len(after) != len(c.incidents) {
					t.Fatalf("an error wrote %d incidents", len(after)-len(c.incidents))
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s := fmt.Sprint(got); s != c.want {
				t.Fatalf("results %s, want %s", s, c.want)
			}
			if c.check == nil {
				return
			}
			var s state
			s.incidents, _ = st.ListIncidents(ctx, store.IncidentFilter{})
			s.signals, _ = st.ListSignals(ctx, store.SignalFilter{})
			s.events = map[string][]string{}
			for _, in := range s.incidents {
				evs, err := st.ListIncidentEvents(ctx, "acme", in.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range evs {
					s.events[in.ID] = append(s.events[in.ID], e.Kind)
				}
			}
			c.check(t, s)
		})
	}
}

// TestApplyTimeline: the opened entry names the actor and holds the decision.
func TestApplyTimeline(t *testing.T) {
	ctx := context.Background()
	web := sig(1, "deployment", "acme/web", 3, 0)
	st := seed(t, nil, []store.Signal{web})
	g := Group{ID: "g1", Key: "acme", Floor: 3, Signals: []store.Signal{web}}
	d := Decision{GroupID: "g1", Decision: New, CausalChain: "web restarts on its own", Confidence: 0.8, Severity: 3}
	if err := st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
		_, err := Applier{NewIncidentID: counter()}.Apply(ctx, tx, []Group{g}, []Decision{d},
			Actor{Kind: store.ByAgent, Name: "correlator"}, t0)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	evs, err := st.ListIncidentEvents(ctx, "acme", incID(900))
	if err != nil || len(evs) != 1 {
		t.Fatalf("events %+v %v", evs, err)
	}
	e := evs[0]
	var data map[string]any
	if err := json.Unmarshal(e.Data, &data); err != nil {
		t.Fatal(err)
	}
	if e.ActorKind != store.ByAgent || e.Actor != "correlator" || e.Kind != EventOpened || !e.At.Equal(t0) ||
		data["groupId"] != "g1" || data["causalChain"] != "web restarts on its own" || data["confidence"] != 0.8 ||
		fmt.Sprint(data["signals"]) != "["+web.ID+"]" {
		t.Fatalf("event %+v data %v", e, data)
	}
	in, err := st.GetIncident(ctx, "acme", incID(900))
	if err != nil || in.OpenedBy != store.ByAgent || in.SeveritySetBy != store.ByAgent {
		t.Fatalf("incident %+v %v", in, err)
	}
}

func TestEngineTick(t *testing.T) {
	type tick struct {
		at   time.Duration // the clock, after t0
		add  []store.Signal
		want string // fmt of the batch results
	}
	web := sig(1, "deployment", "acme/web", 3, 0)
	webAgain := with(sig(2, "deployment", "acme/web", 3, 10*time.Minute), func(s *store.Signal) { s.Fingerprint = "alert/c1/other" })
	urgent := sig(3, "statefulset", "acme/db", 1, 0)
	beta := with(sig(4, "deployment", "beta/web", 3, 0), func(s *store.Signal) { s.Organization = "beta" })
	other := with(sig(5, "deployment", "acme/api", 3, 0), func(s *store.Signal) { s.Cluster = "c2" })
	cases := []struct {
		name  string
		ticks []tick
	}{
		{"a batch is not decided before its wait", []tick{{59 * time.Second, []store.Signal{web}, "[]"}}},
		{"after its wait the rules open an incident", []tick{{59 * time.Second, []store.Signal{web}, "[]"},
			{60 * time.Second, nil, "[{acme c1 [{g1 opened " + incID(900) + " [" + web.ID + "] }]}]"}}},
		{"an urgent batch waits the urgent wait", []tick{{15 * time.Second, []store.Signal{urgent},
			"[{acme c1 [{g1 opened " + incID(900) + " [" + urgent.ID + "] }]}]"}}},
		{"the same entity later joins the incident", []tick{
			{time.Minute, []store.Signal{web}, "[{acme c1 [{g1 opened " + incID(900) + " [" + web.ID + "] }]}]"},
			{11 * time.Minute, []store.Signal{webAgain}, "[{acme c1 [{g1 attached " + incID(900) + " [" + webAgain.ID + "] }]}]"}}},
		{"Organizations and clusters are decided apart", []tick{{time.Minute, []store.Signal{web, beta, other},
			"[{acme c1 [{g1 opened " + incID(900) + " [" + web.ID + "] }]} {acme c2 [{g1 opened " + incID(901) + " [" + other.ID + "] }]}" +
				" {beta c1 [{g1 opened " + incID(902) + " [" + beta.ID + "] }]}]"}}},
		{"nothing pending decides nothing", []tick{{time.Hour, nil, "[]"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st := seed(t, nil, nil)
			now := t0
			e := &Engine{Store: st, Policy: policy, Applier: Applier{NewIncidentID: counter()}, Now: func() time.Time { return now }}
			for i, tk := range c.ticks {
				if err := st.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
					for _, s := range tk.add {
						if err := tx.InsertSignal(ctx, s); err != nil {
							return err
						}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				now = t0.Add(tk.at)
				got, err := e.Tick(ctx)
				if err != nil {
					t.Fatalf("tick %d: %v", i, err)
				}
				var s []string
				for _, b := range got {
					s = append(s, fmt.Sprintf("{%s %s %v}", b.Organization, b.Cluster, b.Groups))
				}
				if g := "[" + joinSpace(s) + "]"; g != tk.want {
					t.Fatalf("tick %d: %s, want %s", i, g, tk.want)
				}
			}
		})
	}
}

func TestEngineRefusesABadPolicy(t *testing.T) {
	e := &Engine{Store: store.NewMemory(), Policy: Policy{}}
	if _, err := e.Tick(context.Background()); err == nil {
		t.Fatal("a zero policy was accepted")
	}
}

func joinSpace(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}
