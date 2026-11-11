package transition

import (
	"fmt"
	"strings"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/regionfile"
)

// step is one (phase, member) cell of a transition.
type step struct {
	phase  string
	member Member
}

func (s step) id() string {
	return s.phase + "-" + s.member.Namespace + "-" + s.member.App
}

// steps lists every cell in execution order: traffic stops top-down (reverse
// of the bottom-up member order), state moves bottom-up, peers follow, and
// traffic is verified restored last, bottom-up.
func steps(g Group) []step {
	out := make([]step, 0, 4*len(g.Members))
	for i := len(g.Members) - 1; i >= 0; i-- {
		out = append(out, step{PhaseTrafficOff, g.Members[i]})
	}
	for _, ph := range []string{PhaseDataPrimary, PhasePeers, PhaseTrafficOn} {
		for _, m := range g.Members {
			out = append(out, step{ph, m})
		}
	}
	return out
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseTS(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, s)
	return t, err == nil
}

// desiredOf returns the one desired primary a member's files declare, or an
// explanation when they disagree / declare none.
func desiredOf(m Member, v View) (string, string) {
	d := ""
	for _, r := range m.Regions {
		f := v.Files[m.Key()][r]
		if !f.Exists || f.Desired == "" {
			continue
		}
		if d != "" && d != f.Desired {
			return "", fmt.Sprintf("%s: region files declare different desired primaries (%s, %s) — the application-controller is committing a placement change", m.App, d, f.Desired)
		}
		d = f.Desired
	}
	if d == "" {
		return "", fmt.Sprintf("%s: no region file declares a desired primary", m.App)
	}
	return d, ""
}

// committedOf returns the primary every existing file of m names ("" when
// they disagree) and whether any file exists.
func committedOf(m Member, v View) (string, bool) {
	p, any := "", false
	for _, r := range m.Regions {
		f := v.Files[m.Key()][r]
		if !f.Exists {
			continue
		}
		if any && p != f.Fields.Primary {
			return "", true
		}
		p, any = f.Fields.Primary, true
	}
	return p, any
}

func lost(v View, region string, p Policy) (bool, time.Duration) {
	rv, ok := v.Regions[region]
	if !ok || rv.Reachable {
		return false, 0
	}
	if rv.LastSeen.IsZero() {
		return false, 0
	}
	d := v.Now.Sub(rv.LastSeen)
	return d > p.RegionLoss, d
}

// Plan evaluates one group. rec is the persisted record of the group's
// current or last transition (nil when none was ever started).
func Plan(g Group, rec *Record, v View, p Policy) Result {
	res := Result{}
	if len(g.Members) == 0 {
		res.Outcome, res.Message = OutcomeBlocked, "group has no members"
		return res
	}

	// 1. The desired primary — one value across every member.
	desired := ""
	for _, m := range g.Members {
		d, why := desiredOf(m, v)
		if why != "" {
			res.Outcome, res.Message, res.Record = OutcomeBlocked, why, rec
			return res
		}
		if desired != "" && d != desired {
			res.Outcome, res.Record = OutcomeBlocked, rec
			res.Message = fmt.Sprintf("members of the group declare different primaries (%s wants %s, another member wants %s); a group moves together — set the same Primary on every member", m.App, d, desired)
			return res
		}
		desired = d
	}
	res.Desired = desired

	// 2. The primary the files currently converge on.
	committed, anyFile := "", false
	disagree := false
	for i, m := range g.Members {
		c, ok := committedOf(m, v)
		anyFile = anyFile || ok
		if !ok {
			continue
		}
		if c == "" || (i > 0 && committed != "" && c != committed) {
			disagree = true
		}
		if committed == "" {
			committed = c
		}
	}
	if disagree {
		committed = ""
	}
	res.Committed = committed
	if !anyFile {
		res.Outcome, res.Message, res.Record = OutcomePending, "no region files committed yet — nothing to move", rec
		return res
	}

	// 3. Which transition (if any) applies.
	desiredLost, _ := lost(v, desired, p)
	inFlight := rec != nil && rec.Outcome != OutcomeConverged
	switch {
	case rec != nil && rec.LossMode && desiredLost:
		// A region-loss move away from the declared primary stands while
		// that region is still lost; when it returns, the declared
		// placement is converged onto again (a planned move back).
	case inFlight && rec.To == desired:
		// Continue the recorded transition.
	case committed == desired:
		// Steady state — unless the primary's region is lost.
		if l, d := lost(v, desired, p); l {
			if !p.AutoPromoteOnRegionLoss {
				res.Outcome, res.Record = OutcomeBlocked, rec
				res.Message = fmt.Sprintf("primary region %s unreachable for %s; autoPromoteOnRegionLoss=false — nothing committed (restore the region, or change Primary in placement)", desired, d.Round(time.Second))
				return res
			}
			survivor := ""
			for _, r := range g.Members[0].Regions {
				if r != desired && v.Regions[r].Reachable {
					survivor = r
					break
				}
			}
			if survivor == "" {
				res.Outcome, res.Record = OutcomeBlocked, rec
				res.Message = fmt.Sprintf("primary region %s lost and no other region of the placement is reachable", desired)
				return res
			}
			rec = &Record{ID: fmt.Sprintf("%s-to-%s-%d", desired, survivor, v.Now.Unix()), From: desired, To: survivor, StartedAt: ts(v.Now), LossMode: true}
		} else {
			res.Outcome, res.Message, res.Record = OutcomeConverged, "every region file names "+desired, rec
			return res
		}
	default:
		from := committed
		if from == "" && rec != nil {
			// A previous transition was re-targeted mid-way: its data-primary
			// commit decides where the writer is now.
			from = rec.From
			for _, m := range g.Members {
				if v.Files[m.Key()][rec.To].Fields.Primary == rec.To {
					from = rec.To
					break
				}
			}
		}
		if from == "" {
			res.Outcome, res.Record = OutcomeBlocked, rec
			res.Message = "region files disagree on the primary and no transition record explains it — fix the files in Git"
			return res
		}
		// from == desired (re-targeted back to where the writer still is):
		// traffic-off is a no-op and the remaining cells re-commit every
		// file to it.
		rec = &Record{ID: fmt.Sprintf("%s-to-%s-%d", from, desired, v.Now.Unix()), From: from, To: desired, StartedAt: ts(v.Now)}
	}
	rec = rec.clone()
	res.Record = rec

	// 4. Walk the cells in order.
	prior := map[string]Activity{}
	for _, a := range rec.Activity {
		prior[a.ID] = a
	}
	all := steps(g)
	acts := make([]Activity, 0, len(all))
	for _, s := range all {
		a, ok := prior[s.id()]
		if !ok {
			a = Activity{ID: s.id(), Phase: s.phase, Member: s.member.App, State: StatePending}
		}
		acts = append(acts, a)
	}
	rec.Activity = acts

	ev := evaluator{g: g, rec: rec, v: v, p: p}
	for i, s := range all {
		a := &rec.Activity[i]
		if a.State == StateSucceeded {
			continue // progress is monotonic: a finished cell is never re-run
		}
		r := ev.eval(s)
		switch {
		case r.commit != nil:
			if a.StartedAt == "" {
				a.StartedAt = ts(v.Now)
			}
			a.State, a.Message = StateRunning, r.msg
			res.Commit = r.commit
			res.Outcome, res.Message = OutcomeTransitioning, fmt.Sprintf("%s %s: %s", s.phase, s.member.App, r.msg)
			rec.Outcome, rec.Message = res.Outcome, res.Message
			return res
		case r.done:
			if a.StartedAt == "" {
				a.StartedAt = ts(v.Now)
			}
			a.State, a.FinishedAt, a.Message = StateSucceeded, ts(v.Now), r.msg
		default:
			if a.StartedAt == "" {
				a.StartedAt = ts(v.Now)
			}
			started, _ := parseTS(a.StartedAt)
			if p.PhaseTimeout > 0 && v.Now.Sub(started) > p.PhaseTimeout {
				a.State = StateFailed
				a.Message = fmt.Sprintf("not reached within %s: %s", p.PhaseTimeout, r.msg)
				res.Outcome = OutcomeFailed
			} else {
				a.State, a.Message = StateRunning, r.msg
				res.Outcome = OutcomeTransitioning
			}
			res.Message = fmt.Sprintf("%s %s: %s", s.phase, s.member.App, a.Message)
			rec.Outcome, rec.Message = res.Outcome, res.Message
			return res
		}
	}
	res.Outcome = OutcomeConverged
	res.Message = fmt.Sprintf("moved from %s to %s", rec.From, rec.To)
	if rec.LossMode {
		res.Message += fmt.Sprintf(" (region %s lost; promoted without a demotion token)", rec.From)
	}
	rec.Outcome, rec.Message = res.Outcome, res.Message
	return res
}

func (r *Record) clone() *Record {
	c := *r
	c.Activity = append([]Activity(nil), r.Activity...)
	c.Tokens = map[string]string{}
	for k, v := range r.Tokens {
		c.Tokens[k] = v
	}
	return &c
}

type evalResult struct {
	commit *Commit
	done   bool
	msg    string
}

type evaluator struct {
	g   Group
	rec *Record
	v   View
	p   Policy
}

func (e evaluator) file(m Member, region string) FileView {
	return e.v.Files[m.Key()][region]
}

func (e evaluator) obs(m Member, region string) (MemberStatus, RegionView) {
	rv := e.v.Regions[region]
	return rv.Members[m.Key()], rv
}

func (e evaluator) commit(m Member, region string, f regionfile.TransitionFields, why string) evalResult {
	return evalResult{
		commit: &Commit{Member: m.Key(), App: m.App, Region: region, Fields: f, Reason: why},
		msg:    fmt.Sprintf("committing primary=%s to %s's file (%s)", f.Primary, region, why),
	}
}

func (e evaluator) eval(s step) evalResult {
	from, to := e.rec.From, e.rec.To
	m := s.member
	switch s.phase {
	case PhaseTrafficOff:
		if from == to {
			return evalResult{done: true, msg: "writer already in " + to}
		}
		f := e.file(m, from)
		if !f.Exists {
			return evalResult{done: true, msg: "no file in " + from}
		}
		if f.Fields.Primary != to {
			return e.commit(m, from, regionfile.TransitionFields{Primary: to}, "stop traffic and demote in the old primary")
		}
		if e.rec.LossMode {
			return evalResult{done: true, msg: fmt.Sprintf("region %s lost — demotion skipped; it rejoins as a replica from Git when it returns", from)}
		}
		ms, rv := e.obs(m, from)
		if !rv.Reachable {
			if l, d := lost(e.v, from, e.p); l {
				if e.p.AutoPromoteOnRegionLoss {
					e.rec.LossMode = true
					return evalResult{done: true, msg: fmt.Sprintf("region %s unreachable for %s — promoting without a demotion token (autoPromoteOnRegionLoss)", from, d.Round(time.Second))}
				}
				return evalResult{msg: fmt.Sprintf("region %s unreachable for %s; autoPromoteOnRegionLoss=false — waiting for the region", from, d.Round(time.Second))}
			}
			return evalResult{msg: fmt.Sprintf("waiting: region %s not reachable", from)}
		}
		if !ms.Observed {
			return evalResult{msg: fmt.Sprintf("waiting: HelmRelease of %s not observed in %s", m.App, from)}
		}
		if ms.AppliedPrimary != to {
			return evalResult{msg: fmt.Sprintf("waiting: Flux has not applied primary=%s in %s (live HelmRelease says %q)", to, from, ms.AppliedPrimary)}
		}
		if m.Stateful {
			if !ms.DB.Found {
				return evalResult{msg: fmt.Sprintf("waiting: CNPG Cluster of %s not found in %s", m.App, from)}
			}
			if ms.DB.DemotionToken == "" {
				return evalResult{msg: fmt.Sprintf("waiting: CNPG Cluster %s in %s has not published status.demotionToken (phase %q)", ms.DB.Cluster, from, ms.DB.Phase)}
			}
			e.rec.Tokens[m.Key()] = ms.DB.DemotionToken
			return evalResult{done: true, msg: fmt.Sprintf("CNPG Cluster %s demoted in %s; demotion token published", ms.DB.Cluster, from)}
		}
		if !ms.ReleaseReady {
			return evalResult{msg: fmt.Sprintf("waiting: HelmRelease in %s not Ready: %s", from, ms.ReleaseMessage)}
		}
		return evalResult{done: true, msg: "traffic off in " + from}

	case PhaseDataPrimary:
		f := e.file(m, to)
		if !f.Exists {
			return evalResult{msg: fmt.Sprintf("waiting: no region file for %s in %s (the application-controller commits it from placement)", m.App, to)}
		}
		tok := ""
		if m.Stateful && !e.rec.LossMode && from != to {
			tok = e.rec.Tokens[m.Key()]
			if tok == "" {
				return evalResult{msg: "waiting: no demotion token recorded for " + m.App}
			}
		}
		want := regionfile.TransitionFields{Primary: to, PromotionToken: tok}
		if f.Fields.Primary != to || (tok != "" && f.Fields.PromotionToken != tok) {
			return e.commit(m, to, want, "promote in the new primary")
		}
		ms, rv := e.obs(m, to)
		if !rv.Reachable {
			return evalResult{msg: fmt.Sprintf("waiting: region %s not reachable", to)}
		}
		if !ms.Observed || ms.AppliedPrimary != to {
			return evalResult{msg: fmt.Sprintf("waiting: Flux has not applied primary=%s in %s", to, to)}
		}
		if m.Stateful {
			if !ms.DB.Found {
				return evalResult{msg: fmt.Sprintf("waiting: CNPG Cluster of %s not found in %s", m.App, to)}
			}
			if tok != "" && ms.DB.LastPromotionToken != tok {
				return evalResult{msg: fmt.Sprintf("waiting: CNPG Cluster %s in %s has not promoted with the demotion token yet (phase %q)", ms.DB.Cluster, to, ms.DB.Phase)}
			}
			if !ms.DB.Primary || !ms.DB.Ready {
				return evalResult{msg: fmt.Sprintf("waiting: CNPG Cluster %s in %s not a Ready primary (phase %q)", ms.DB.Cluster, to, ms.DB.Phase)}
			}
		}
		if !ms.ReleaseReady {
			return evalResult{msg: fmt.Sprintf("waiting: HelmRelease in %s not Ready: %s", to, ms.ReleaseMessage)}
		}
		return evalResult{done: true, msg: "primary in " + to}

	case PhasePeers:
		var pending []string
		for _, r := range m.Regions {
			if r == from || r == to {
				continue
			}
			f := e.file(m, r)
			if !f.Exists {
				continue
			}
			if f.Fields.Primary != to {
				return e.commit(m, r, regionfile.TransitionFields{Primary: to}, "peer follows the new primary")
			}
			ms, rv := e.obs(m, r)
			if rv.Reachable && (!ms.Observed || ms.AppliedPrimary != to) {
				pending = append(pending, r)
			}
		}
		if len(pending) > 0 {
			return evalResult{msg: "waiting: Flux has not applied the new primary in " + strings.Join(pending, ", ")}
		}
		return evalResult{done: true, msg: "every peer names " + to}

	case PhaseTrafficOn:
		ms, rv := e.obs(m, to)
		if !rv.Reachable {
			return evalResult{msg: fmt.Sprintf("waiting: region %s not reachable", to)}
		}
		if !ms.ReleaseReady {
			return evalResult{msg: fmt.Sprintf("waiting: HelmRelease in %s not Ready: %s", to, ms.ReleaseMessage)}
		}
		if m.Stateful && (!ms.DB.Primary || !ms.DB.Ready) {
			return evalResult{msg: fmt.Sprintf("waiting: CNPG Cluster %s in %s not a Ready primary", ms.DB.Cluster, to)}
		}
		switch ms.Serving {
		case False:
			return evalResult{msg: fmt.Sprintf("waiting: Application Serving=False in %s", to)}
		case True:
			return evalResult{done: true, msg: "serving in " + to}
		}
		return evalResult{done: true, msg: fmt.Sprintf("HelmRelease Ready in %s (Serving not observable from %s's region)", to, to)}
	}
	return evalResult{msg: "unknown phase " + s.phase}
}
