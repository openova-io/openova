package correlate

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/store"
)

var t0 = time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)

// ulid returns a valid ULID that sorts by n.
func ulid(n int) string { return fmt.Sprintf("01JCF0R1K2M3N4P5Q6R7S8%04d", n) }

// incID returns a valid incident identifier that sorts by n.
func incID(n int) string { return "INC-" + ulid(n) }

// sig returns an open, pending signal of Organization acme in cluster c1.
func sig(n int, kind, ref string, floor int, first time.Duration) store.Signal {
	return store.Signal{ID: ulid(n), Organization: "acme", Source: store.SourceAlert, Kind: "alert-" + ref,
		EntityKind: kind, EntityRef: ref, Cluster: "c1", SeverityFloor: floor, Title: "title " + ref,
		Fingerprint: fmt.Sprintf("alert/c1/a%d", n), State: store.SignalOpen,
		FirstSeen: t0.Add(first), LastSeen: t0.Add(first)}
}

// view returns an open incident of acme, opened at t0 + opened, with its signals.
func view(n int, severity int, opened time.Duration, signals ...store.Signal) IncidentView {
	in := store.Incident{ID: incID(n), Organization: "acme", Title: fmt.Sprintf("incident %d", n), Severity: severity,
		SeveritySetBy: store.ByRules, State: store.IncidentOpen, OpenedBy: store.ByRules, OpenedAt: t0.Add(opened)}
	for i := range signals {
		signals[i].IncidentID = in.ID
	}
	return IncidentView{Incident: in, Signals: signals}
}

func with(s store.Signal, f func(*store.Signal)) store.Signal { f(&s); return s }

var policy = Policy{Batch: 60 * time.Second, UrgentBatch: 15 * time.Second}

func TestPolicyValidate(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
		ok   bool
	}{
		{"the shipped waits", policy, true},
		{"equal waits", Policy{Batch: time.Minute, UrgentBatch: time.Minute}, true},
		{"a zero wait", Policy{Batch: time.Minute}, false},
		{"the urgent wait is longer", Policy{Batch: time.Second, UrgentBatch: time.Minute}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.p.Validate(); (err == nil) != c.ok {
				t.Fatalf("Validate() = %v, want ok %v", err, c.ok)
			}
		})
	}
}

func TestPending(t *testing.T) {
	a := sig(1, "deployment", "acme/web", 3, 0)
	b := with(sig(2, "deployment", "acme/db", 3, -time.Minute), func(s *store.Signal) { s.Cluster = "c2" })
	cleared := with(sig(3, "deployment", "acme/x", 3, 0), func(s *store.Signal) { s.State = store.SignalCleared })
	attached := with(sig(4, "deployment", "acme/y", 3, 0), func(s *store.Signal) { s.IncidentID = incID(1) })
	other := with(sig(5, "deployment", "beta/web", 3, 0), func(s *store.Signal) { s.Organization = "beta" })
	later := sig(6, "deployment", "acme/api", 3, -time.Hour)
	cases := []struct {
		name string
		in   []store.Signal
		want []string // "<org>/<cluster>:<ids>"
	}{
		{"cleared and attached signals are not pending", []store.Signal{a, cleared, attached}, []string{"acme/c1:" + a.ID}},
		{"one batch per Organization and cluster, in order", []store.Signal{other, b, a},
			[]string{"acme/c1:" + a.ID, "acme/c2:" + b.ID, "beta/c1:" + other.ID}},
		{"signals of a batch come oldest first", []store.Signal{a, later}, []string{"acme/c1:" + later.ID + "," + a.ID}},
		{"nothing pending", []store.Signal{cleared}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, bt := range Pending(c.in) {
				ids := ""
				for i, s := range bt.Signals {
					if i > 0 {
						ids += ","
					}
					ids += s.ID
				}
				got = append(got, bt.Key()+":"+ids)
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("Pending = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDueAt(t *testing.T) {
	warn := sig(1, "deployment", "acme/web", 3, 0)
	urgent := sig(2, "deployment", "acme/db", 2, 30*time.Second)
	cases := []struct {
		name string
		b    Batch
		last time.Time
		want time.Time
	}{
		{"a batch waits the wait after its oldest signal", Batch{Signals: []store.Signal{warn}}, time.Time{}, t0.Add(60 * time.Second)},
		{"a floor of 2 shortens the wait", Batch{Signals: []store.Signal{warn, urgent}}, time.Time{}, t0.Add(15 * time.Second)},
		{"the wait starts at the earliest last seen time", Batch{Signals: []store.Signal{urgent,
			with(warn, func(s *store.Signal) { s.LastSeen = t0.Add(-time.Minute) })}}, time.Time{}, t0.Add(-45 * time.Second)},
		{"a later decision restarts the wait", Batch{Signals: []store.Signal{warn}}, t0.Add(10 * time.Minute), t0.Add(11 * time.Minute)},
		{"an earlier decision changes nothing", Batch{Signals: []store.Signal{warn}}, t0.Add(-10 * time.Minute), t0.Add(60 * time.Second)},
		{"an empty batch is never due", Batch{}, time.Time{}, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DueAt(c.b, c.last, policy); !got.Equal(c.want) {
				t.Fatalf("DueAt = %s, want %s", got, c.want)
			}
		})
	}
}

func TestGroups(t *testing.T) {
	web := sig(1, "deployment", "acme/web", 3, 0)
	db := sig(2, "statefulset", "acme/db", 2, time.Minute)
	node := sig(3, "node", "worker-1", 4, -time.Minute)
	node2 := sig(4, "node", "worker-2", 1, 2*time.Minute)
	billing := sig(5, "deployment", "billing/api", 3, 0)
	cases := []struct {
		name string
		in   []store.Signal
		want string
	}{
		{"one namespace is one group with the most urgent floor", []store.Signal{web, db}, "[g1 acme 2 [" + web.ID + " " + db.ID + "]]"},
		{"a signal without a namespace is grouped by its entity", []store.Signal{node, node2},
			"[g1 node/worker-1 4 [" + node.ID + "] g2 node/worker-2 1 [" + node2.ID + "]]"},
		{"groups are named in the order of their first signal", []store.Signal{web, node, db},
			"[g1 node/worker-1 4 [" + node.ID + "] g2 acme 2 [" + web.ID + " " + db.ID + "]]"},
		{"equal first times are ordered by key", []store.Signal{web, billing},
			"[g1 acme 3 [" + web.ID + "] g2 billing 3 [" + billing.ID + "]]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, g := range Groups(Batch{Organization: "acme", Cluster: "c1", Signals: c.in}) {
				var ids []string
				for _, s := range g.Signals {
					ids = append(ids, s.ID)
				}
				got = append(got, fmt.Sprintf("%s %s %d %v", g.ID, g.Key, g.Floor, ids))
			}
			if s := fmt.Sprint(got); s != c.want {
				t.Fatalf("Groups = %s, want %s", s, c.want)
			}
		})
	}
}

func TestScore(t *testing.T) {
	web := sig(1, "deployment", "acme/web", 3, 0)
	group := Group{ID: "g1", Key: "acme", Floor: 3, Signals: []store.Signal{web}}
	sameAlert := func(s store.Signal) store.Signal {
		return with(s, func(x *store.Signal) { x.Fingerprint = web.Fingerprint })
	}
	cases := []struct {
		name string
		iv   IncidentView
		want float64
	}{
		{"the same alert scores 1 however old", view(1, 3, -72*time.Hour, sameAlert(sig(10, "deployment", "acme/web", 3, -72*time.Hour))), 1.0},
		{"the same entity within 2 hours scores 0.9", view(1, 3, -time.Hour, sig(10, "deployment", "acme/web", 3, -time.Hour)), 0.9},
		{"the same entity within 24 hours scores 0.68", view(1, 3, -6*time.Hour, sig(10, "deployment", "acme/web", 3, -6*time.Hour)), 0.68},
		{"the same entity older than 24 hours scores 0.45", view(1, 3, -30*time.Hour, sig(10, "deployment", "acme/web", 3, -30*time.Hour)), 0.45},
		{"the same namespace scores 0.5", view(1, 3, -time.Hour, sig(10, "deployment", "acme/db", 3, -time.Hour)), 0.5},
		{"the same namespace within 24 hours scores 0.38", view(1, 3, -3*time.Hour, sig(10, "deployment", "acme/db", 3, -3*time.Hour)), 0.38},
		{"the same cluster only scores 0.25", view(1, 3, -time.Hour, sig(10, "node", "worker-1", 3, -time.Hour)), 0.25},
		{"another cluster is no candidate", view(1, 3, -time.Hour, with(sig(10, "deployment", "acme/web", 3, -time.Hour),
			func(s *store.Signal) { s.Cluster = "c2" })), 0},
		{"an incident without signals is no candidate", view(1, 3, -time.Hour), 0},
		{"the same fingerprint in another cluster is no candidate", view(1, 3, -time.Hour,
			with(sameAlert(sig(10, "node", "worker-1", 3, -time.Hour)), func(s *store.Signal) { s.Cluster = "c2" })), 0},
		{"the best of several signals counts", view(1, 3, -time.Hour, sig(10, "node", "worker-1", 3, -time.Hour),
			sig(11, "deployment", "acme/web", 3, -time.Hour)), 0.9},
		{"recent activity on an old incident counts as recent", view(1, 3, -48*time.Hour,
			with(sig(10, "deployment", "acme/web", 3, -48*time.Hour), func(s *store.Signal) { s.LastSeen = t0.Add(-time.Hour) })), 0.9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got, _ := Score(group, c.iv); got != c.want {
				t.Fatalf("Score = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCandidates(t *testing.T) {
	web := sig(1, "deployment", "acme/web", 3, 0)
	group := Group{ID: "g1", Key: "acme", Floor: 3, Signals: []store.Signal{web}}
	entity := func(n int) store.Signal { return sig(100+n, "deployment", "acme/web", 3, -time.Hour) }
	resolved := view(1, 3, -time.Hour, entity(1))
	resolved.Incident.State, resolved.Incident.ResolvedAt = store.IncidentResolved, &t0
	merged := view(2, 3, -time.Hour, entity(2))
	merged.Incident.ParentID = incID(9)
	mitigated := view(3, 3, -time.Hour, entity(3))
	mitigated.Incident.State = store.IncidentMitigated
	other := view(4, 3, -time.Hour, with(entity(4), func(s *store.Signal) { s.Organization = "beta" }))
	other.Incident.Organization = "beta"
	cases := []struct {
		name string
		in   []IncidentView
		want []string
	}{
		{"resolved, merged and other Organizations are left out; mitigated is offered",
			[]IncidentView{resolved, merged, mitigated, other}, []string{incID(3) + "=0.9"}},
		{"best score first, then the newest", []IncidentView{
			view(5, 3, -2*time.Hour, sig(105, "node", "n", 3, -2*time.Hour)),
			view(6, 3, -90*time.Minute, entity(6)),
			view(7, 3, -30*time.Minute, entity(7)),
		}, []string{incID(7) + "=0.9", incID(6) + "=0.9", incID(5) + "=0.25"}},
		{"equal score and time: the greater identifier first", []IncidentView{
			view(8, 3, -time.Hour, entity(8)), view(9, 3, -time.Hour, entity(9)),
		}, []string{incID(9) + "=0.9", incID(8) + "=0.9"}},
		{"at most five are offered", []IncidentView{
			view(11, 3, -time.Hour, entity(11)), view(12, 3, -time.Hour, entity(12)), view(13, 3, -time.Hour, entity(13)),
			view(14, 3, -time.Hour, entity(14)), view(15, 3, -time.Hour, entity(15)), view(16, 3, -time.Hour, entity(16)),
		}, []string{incID(16) + "=0.9", incID(15) + "=0.9", incID(14) + "=0.9", incID(13) + "=0.9", incID(12) + "=0.9"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, cd := range Candidates(group, c.in) {
				got = append(got, fmt.Sprintf("%s=%v", cd.IncidentID, cd.Score))
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("Candidates = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRule(t *testing.T) {
	g := Group{ID: "g1", Key: "acme", Floor: 3}
	cases := []struct {
		name  string
		g     Group
		cands []Candidate
		want  Decision
	}{
		{"a score of 0.9 attaches", g, []Candidate{{IncidentID: incID(1), Severity: 2, Score: 0.9, reason: "the same entity deployment/acme/web reports again"}},
			Decision{GroupID: "g1", Decision: Attach, IncidentID: incID(1), CausalChain: "rules: the same entity deployment/acme/web reports again", Confidence: 0.9, Severity: 2}},
		{"a score of exactly 0.6 attaches", g, []Candidate{{IncidentID: incID(1), Severity: 4, Score: 0.6, reason: "x"}},
			Decision{GroupID: "g1", Decision: Attach, IncidentID: incID(1), CausalChain: "rules: x", Confidence: 0.6, Severity: 3}},
		{"a best score below 0.6 opens a new incident", g, []Candidate{{IncidentID: incID(1), Severity: 2, Score: 0.5}},
			Decision{GroupID: "g1", Decision: New, CausalChain: "rules: no open incident scores 0.60 or more", Confidence: 0.5, Severity: 3}},
		{"no candidate opens a new incident with confidence 1", Group{ID: "g2", Floor: 1}, nil,
			Decision{GroupID: "g2", Decision: New, CausalChain: "rules: no open incident scores 0.60 or more", Confidence: 1, Severity: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Rule(c.g, c.cands); got != c.want {
				t.Fatalf("Rule = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestPrepare(t *testing.T) {
	web := sig(1, "deployment", "acme/web", 3, 0)
	node := sig(2, "node", "worker-1", 2, time.Minute)
	db := view(1, 2, -5*time.Minute, sig(10, "statefulset", "acme/db", 2, -5*time.Minute))
	cases := []struct {
		name       string
		incidents  []IncidentView
		wantReq    string
		wantResult string
	}{
		{"with a candidate", []IncidentView{db},
			`{"task":"correlate","organization":"acme","groups":[` +
				`{"groupId":"g1","signals":[{"signalId":"` + web.ID + `","kind":"alert-acme/web","entity":"deployment/acme/web","title":"title acme/web","firstSeen":"2026-12-01T09:00:00Z","severityFloor":3}]},` +
				`{"groupId":"g2","signals":[{"signalId":"` + node.ID + `","kind":"alert-worker-1","entity":"node/worker-1","title":"title worker-1","firstSeen":"2026-12-01T09:01:00Z","severityFloor":2}]}],` +
				`"candidates":[{"incidentId":"` + incID(1) + `","title":"incident 1","severity":2,"entities":["statefulset/acme/db"],"openedAt":"2026-12-01T08:55:00Z","score":0.5}],` +
				`"allowed":["attach","new","watch"]}`,
			`{"decisions":[{"groupId":"g1","decision":"new","causalChain":"rules: no open incident scores 0.60 or more","confidence":0.5,"severity":3},` +
				`{"groupId":"g2","decision":"new","causalChain":"rules: no open incident scores 0.60 or more","confidence":0.75,"severity":2}]}`},
		{"without a candidate attach is not allowed", nil,
			`{"task":"correlate","organization":"acme","groups":[` +
				`{"groupId":"g1","signals":[{"signalId":"` + web.ID + `","kind":"alert-acme/web","entity":"deployment/acme/web","title":"title acme/web","firstSeen":"2026-12-01T09:00:00Z","severityFloor":3}]},` +
				`{"groupId":"g2","signals":[{"signalId":"` + node.ID + `","kind":"alert-worker-1","entity":"node/worker-1","title":"title worker-1","firstSeen":"2026-12-01T09:01:00Z","severityFloor":2}]}],` +
				`"candidates":[],"allowed":["new","watch"]}`,
			`{"decisions":[{"groupId":"g1","decision":"new","causalChain":"rules: no open incident scores 0.60 or more","confidence":1,"severity":3},` +
				`{"groupId":"g2","decision":"new","causalChain":"rules: no open incident scores 0.60 or more","confidence":1,"severity":2}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Prepare(Batch{Organization: "acme", Cluster: "c1", Signals: []store.Signal{web, node}}, c.incidents)
			req, err := json.Marshal(p.Request)
			if err != nil {
				t.Fatal(err)
			}
			if string(req) != c.wantReq {
				t.Fatalf("request\n got %s\nwant %s", req, c.wantReq)
			}
			res, err := json.Marshal(p.RuleResult)
			if err != nil {
				t.Fatal(err)
			}
			if string(res) != c.wantResult {
				t.Fatalf("rule result\n got %s\nwant %s", res, c.wantResult)
			}
		})
	}
}
