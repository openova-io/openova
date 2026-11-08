package transition

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// world is a fake Git + fake region set: region files, and per (member,
// region) the LIVE state Flux + CNPG produce from those files. apply()
// simulates the native operators: Flux applies the file; a CNPG member whose
// file stops naming it primary demotes and publishes a token; a member named
// primary promotes only with the peer's token (or with none when no peer is
// primary any more — the region-loss path).
type world struct {
	t        *testing.T
	now      time.Time
	regions  []string
	down     map[string]time.Time // region → since
	files    map[string]map[string]FileView
	live     map[string]map[string]*MemberStatus
	commits  []Commit
	stateful map[string]bool
}

func newWorld(t *testing.T, regions []string, members []Member, primary string) *world {
	w := &world{t: t, now: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC), regions: regions,
		down: map[string]time.Time{}, files: map[string]map[string]FileView{},
		live: map[string]map[string]*MemberStatus{}, stateful: map[string]bool{}}
	for _, m := range members {
		w.stateful[m.Key()] = m.Stateful
		w.files[m.Key()] = map[string]FileView{}
		w.live[m.Key()] = map[string]*MemberStatus{}
		for _, r := range m.Regions {
			w.files[m.Key()][r] = FileView{Exists: true, Desired: primary, Fields: regionfile.TransitionFields{Primary: primary}}
			ms := &MemberStatus{Observed: true, AppliedPrimary: primary, ReleaseReady: true, Serving: Unknown}
			if m.Stateful {
				ms.DB = DBStatus{Found: true, Cluster: m.App + "-db-" + r, Primary: r == primary, Ready: true, Phase: "Cluster in healthy state"}
			}
			w.live[m.Key()][r] = ms
		}
	}
	return w
}

func (w *world) setDesired(d string) {
	for k, rf := range w.files {
		for r, f := range rf {
			f.Desired = d
			w.files[k][r] = f
		}
	}
}

func (w *world) view() View {
	v := View{Now: w.now, Files: map[string]map[string]FileView{}, Regions: map[string]RegionView{}}
	for k, rf := range w.files {
		v.Files[k] = map[string]FileView{}
		for r, f := range rf {
			v.Files[k][r] = f
		}
	}
	for _, r := range w.regions {
		rv := RegionView{Reachable: true, LastSeen: w.now, Members: map[string]MemberStatus{}}
		if since, ok := w.down[r]; ok {
			rv = RegionView{Reachable: false, LastSeen: since}
		} else {
			for k := range w.live {
				if ms := w.live[k][r]; ms != nil {
					rv.Members[k] = *ms
				}
			}
		}
		v.Regions[r] = rv
	}
	return v
}

func (w *world) commit(c Commit) {
	// The machine must never promote a region while a reachable peer is
	// still a writable CNPG primary: assert the token discipline at commit.
	if w.stateful[c.Member] && c.Fields.Primary == c.Region {
		for r, ms := range w.live[c.Member] {
			if r == c.Region || w.down[r] != (time.Time{}) {
				continue
			}
			if ms.DB.Primary {
				w.t.Fatalf("committed primary=%s to %s while %s is still the CNPG primary", c.Region, c.Region, r)
			}
		}
	}
	f := w.files[c.Member][c.Region]
	f.Fields = c.Fields
	w.files[c.Member][c.Region] = f
	w.commits = append(w.commits, c)
}

// apply runs Flux + CNPG in every reachable region.
func (w *world) apply() {
	for k, rs := range w.live {
		for r, ms := range rs {
			if _, isDown := w.down[r]; isDown {
				continue
			}
			f := w.files[k][r]
			ms.AppliedPrimary = f.Fields.Primary
			if !w.stateful[k] {
				continue
			}
			wantPrimary := f.Fields.Primary == r
			switch {
			case ms.DB.Primary && !wantPrimary:
				ms.DB.Primary = false
				ms.DB.DemotionToken = "tok-" + r
			case !ms.DB.Primary && wantPrimary:
				tok := f.Fields.PromotionToken
				peerToken := ""
				peerPrimary := false
				for pr, pms := range rs {
					if pr == r {
						continue
					}
					if pms.DB.DemotionToken != "" {
						peerToken = pms.DB.DemotionToken
					}
					if _, d := w.down[pr]; !d && pms.DB.Primary {
						peerPrimary = true
					}
				}
				if peerPrimary {
					continue
				}
				if tok != "" && tok != peerToken {
					continue
				}
				ms.DB.Primary = true
				ms.DB.LastPromotionToken = tok
				ms.DB.DemotionToken = ""
			}
		}
	}
}

// run drives Plan until it stops committing and is no longer Transitioning,
// or maxTicks passes. Returns the last result.
func (w *world) run(g Group, rec *Record, p Policy, maxTicks int) (Result, *Record) {
	var res Result
	for i := 0; i < maxTicks; i++ {
		res = Plan(g, rec, w.view(), p)
		if res.Record != nil {
			rec = res.Record
		}
		if res.Commit != nil {
			w.commit(*res.Commit)
		}
		w.apply()
		w.now = w.now.Add(10 * time.Second)
		if res.Commit == nil && res.Outcome != OutcomeTransitioning {
			return res, rec
		}
	}
	return res, rec
}

var policy = Policy{RegionLoss: 2 * time.Minute, PhaseTimeout: 5 * time.Minute}

func wp(regions ...string) Member {
	return Member{Name: "dr-wp", Namespace: "acme", App: "wp", Stateful: true, Regions: regions, DependsOn: []string{"dr-db"}}
}

func db(regions ...string) Member {
	return Member{Name: "dr-db", Namespace: "acme", App: "db", Stateful: true, Regions: regions}
}

func commitsString(cs []Commit) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		s := fmt.Sprintf("%s@%s:%s", c.App, c.Region, c.Fields.Primary)
		if c.Fields.PromotionToken != "" {
			s += "+" + c.Fields.PromotionToken
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestPlannedMove_SingleStatefulMember_ThreeCommitProtocol(t *testing.T) {
	m := Member{Name: "dr-wp", Namespace: "acme", App: "wp", Stateful: true, Regions: []string{"a", "b", "c"}}
	w := newWorld(t, []string{"a", "b", "c"}, []Member{m}, "a")
	groups, errs := Groups([]Member{m})
	if len(errs) > 0 || len(groups) != 1 {
		t.Fatalf("groups: %v %v", groups, errs)
	}
	// Steady state: converged, nothing committed, no record.
	if res := Plan(groups[0], nil, w.view(), policy); res.Outcome != OutcomeConverged || res.Commit != nil || res.Record != nil {
		t.Fatalf("steady state: %+v", res)
	}
	w.setDesired("b")
	res, rec := w.run(groups[0], nil, policy, 40)
	if res.Outcome != OutcomeConverged {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Message)
	}
	if got, want := commitsString(w.commits), "wp@a:b wp@b:b+tok-a wp@c:b"; got != want {
		t.Fatalf("commit order\n got %s\nwant %s", got, want)
	}
	for _, a := range rec.Activity {
		if a.State != StateSucceeded || a.StartedAt == "" || a.FinishedAt == "" {
			t.Fatalf("activity %+v not succeeded", a)
		}
	}
	if len(rec.Activity) != 4 {
		t.Fatalf("want one entry per phase, got %d", len(rec.Activity))
	}
	if !w.live["acme/wp"]["b"].DB.Primary || w.live["acme/wp"]["a"].DB.Primary {
		t.Fatal("CNPG primary did not move")
	}
	// Converged again on the next evaluation, no further commit.
	if res := Plan(groups[0], rec, w.view(), policy); res.Outcome != OutcomeConverged || res.Commit != nil {
		t.Fatalf("after: %+v", res)
	}
}

func TestPlannedMove_WaitsForDemotionTokenBeforePromoting(t *testing.T) {
	m := Member{Name: "dr-wp", Namespace: "acme", App: "wp", Stateful: true, Regions: []string{"a", "b"}}
	w := newWorld(t, []string{"a", "b"}, []Member{m}, "a")
	g, _ := Groups([]Member{m})
	w.setDesired("b")
	// Tick 1 commits the old primary's file.
	res := Plan(g[0], nil, w.view(), policy)
	if res.Commit == nil || res.Commit.Region != "a" {
		t.Fatalf("first commit must go to the old primary: %+v", res.Commit)
	}
	w.commit(*res.Commit)
	// Flux has NOT applied yet (no w.apply()): the machine must wait, not
	// commit the new primary.
	for i := 0; i < 5; i++ {
		w.now = w.now.Add(10 * time.Second)
		res = Plan(g[0], res.Record, w.view(), policy)
		if res.Commit != nil {
			t.Fatalf("committed %+v before the demotion token was observed", res.Commit)
		}
		if res.Outcome != OutcomeTransitioning || !strings.Contains(res.Message, "traffic-off") {
			t.Fatalf("want traffic-off wait, got %s: %s", res.Outcome, res.Message)
		}
	}
}

func TestGroupMove_TrafficTopDown_StateBottomUp(t *testing.T) {
	members := []Member{wp("a", "b"), db("a", "b")}
	w := newWorld(t, []string{"a", "b"}, members, "a")
	groups, errs := Groups(members)
	if len(errs) > 0 || len(groups) != 1 || len(groups[0].Members) != 2 {
		t.Fatalf("groups: %+v %v", groups, errs)
	}
	if groups[0].Members[0].App != "db" {
		t.Fatalf("bottom-up order must put the backing service first: %+v", groups[0].Members)
	}
	w.setDesired("b")
	res, rec := w.run(groups[0], nil, policy, 60)
	if res.Outcome != OutcomeConverged {
		t.Fatalf("outcome %s: %s", res.Outcome, res.Message)
	}
	if got, want := commitsString(w.commits), "wp@a:b db@a:b db@b:b+tok-a wp@b:b+tok-a"; got != want {
		t.Fatalf("commit order\n got %s\nwant %s", got, want)
	}
	wantIDs := []string{"traffic-off-acme-wp", "traffic-off-acme-db", "data-primary-acme-db", "data-primary-acme-wp"}
	for i, id := range wantIDs {
		if rec.Activity[i].ID != id {
			t.Fatalf("activity[%d] = %s, want %s", i, rec.Activity[i].ID, id)
		}
	}
}

func TestGroupMembersDisagree_Blocked(t *testing.T) {
	members := []Member{wp("a", "b"), db("a", "b")}
	w := newWorld(t, []string{"a", "b"}, members, "a")
	g, _ := Groups(members)
	for r, f := range w.files["acme/wp"] {
		f.Desired = "b"
		w.files["acme/wp"][r] = f
	}
	res := Plan(g[0], nil, w.view(), policy)
	if res.Outcome != OutcomeBlocked || res.Commit != nil || !strings.Contains(res.Message, "different primaries") {
		t.Fatalf("want Blocked without commit, got %+v", res)
	}
}

func TestRegionLoss_PolicyOff_NeverPromotes(t *testing.T) {
	m := Member{Name: "dr-wp", Namespace: "acme", App: "wp", Stateful: true, Regions: []string{"a", "b"}}
	w := newWorld(t, []string{"a", "b"}, []Member{m}, "a")
	g, _ := Groups([]Member{m})
	w.down["a"] = w.now.Add(-10 * time.Minute)

	// Steady state with the primary region lost: reported, nothing committed.
	res := Plan(g[0], nil, w.view(), policy)
	if res.Outcome != OutcomeBlocked || res.Commit != nil || !strings.Contains(res.Message, "autoPromoteOnRegionLoss=false") {
		t.Fatalf("steady loss: %+v", res)
	}

	// Operator moves Primary to b: the old file is committed (Git desired
	// state for when a returns), the new primary is never committed without
	// a token, and the phase fails at its deadline.
	w.setDesired("b")
	res, rec := w.run(g[0], nil, policy, 60)
	if res.Outcome != OutcomeFailed {
		t.Fatalf("want Failed, got %s: %s", res.Outcome, res.Message)
	}
	for _, c := range w.commits {
		if c.Region == "b" {
			t.Fatalf("promoted b without a demotion token while autoPromoteOnRegionLoss=false: %+v", c)
		}
	}
	if rec.Activity[0].State != StateFailed {
		t.Fatalf("traffic-off must report failed: %+v", rec.Activity[0])
	}
}

func TestRegionLoss_PolicyOn_PromotesSurvivorWithoutToken(t *testing.T) {
	m := Member{Name: "dr-wp", Namespace: "acme", App: "wp", Stateful: true, Regions: []string{"a", "b"}}
	w := newWorld(t, []string{"a", "b"}, []Member{m}, "a")
	g, _ := Groups([]Member{m})
	w.down["a"] = w.now.Add(-10 * time.Minute)
	p := policy
	p.AutoPromoteOnRegionLoss = true
	res, rec := w.run(g[0], nil, p, 40)
	if res.Outcome != OutcomeConverged || !rec.LossMode || rec.To != "b" {
		t.Fatalf("want loss-mode convergence onto b: %s %s %+v", res.Outcome, res.Message, rec)
	}
	if got, want := commitsString(w.commits), "wp@a:b wp@b:b"; got != want {
		t.Fatalf("commits\n got %s\nwant %s", got, want)
	}
	// While a stays lost the loss move stands (the declared primary is a).
	if res := Plan(g[0], rec, w.view(), p); res.Commit != nil || res.Outcome != OutcomeConverged {
		t.Fatalf("loss move must stand while a is lost: %+v", res)
	}
	// a returns: the declared placement (a) is converged onto again,
	// starting with demoting b.
	delete(w.down, "a")
	res = Plan(g[0], rec, w.view(), p)
	if res.Commit == nil || res.Commit.Region != "b" || res.Commit.Fields.Primary != "a" {
		t.Fatalf("want planned move back starting at b: %+v", res)
	}
}

func TestGroups_CycleIsAnError(t *testing.T) {
	a := Member{Name: "x", Namespace: "n", App: "x", DependsOn: []string{"y"}}
	b := Member{Name: "y", Namespace: "n", App: "y", DependsOn: []string{"x"}}
	_, errs := Groups([]Member{a, b})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", errs)
	}
}

func TestGroups_UngovernedDependencyIgnored(t *testing.T) {
	a := Member{Name: "x", Namespace: "n", App: "x", DependsOn: []string{"not-governed"}}
	gs, errs := Groups([]Member{a})
	if len(errs) != 0 || len(gs) != 1 || len(gs[0].Members) != 1 {
		t.Fatalf("%+v %v", gs, errs)
	}
}

func TestStatelessMember_WaitsOnAppliedHelmRelease(t *testing.T) {
	m := Member{Name: "dr-web", Namespace: "acme", App: "web", Regions: []string{"a", "b"}}
	w := newWorld(t, []string{"a", "b"}, []Member{m}, "a")
	g, _ := Groups([]Member{m})
	w.setDesired("b")
	res, _ := w.run(g[0], nil, policy, 30)
	if res.Outcome != OutcomeConverged {
		t.Fatalf("%s: %s", res.Outcome, res.Message)
	}
	if got, want := commitsString(w.commits), "web@a:b web@b:b"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestLeaseFile(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	l := LeaseFile{Group: "g", Holder: "a", RenewedAt: ts(now), TTLSeconds: 60}
	if l.MayAct("b", now.Add(30*time.Second)) {
		t.Fatal("b must not act while a's lease is live")
	}
	if !l.MayAct("a", now.Add(30*time.Second)) || !l.MayAct("b", now.Add(61*time.Second)) {
		t.Fatal("holder keeps it; anyone takes an expired lease")
	}
	b, err := l.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseLease(b)
	if err != nil || back.Holder != "a" || back.TTLSeconds != 60 {
		t.Fatalf("%+v %v", back, err)
	}
}
