// Package correlate decides, by rules alone, which incident new signals belong to.
//
// The rule path is a main path: it runs without any agent, and its result stands whenever no
// agent answers in time. A judge (the correlator agent) is offered the same groups and
// candidates and answers in the same form as the rule result.
//
// The rules:
//
//   - Pending. A signal is pending when it is open and belongs to no incident. Pending signals
//     are decided per batch: one Organization and one cluster.
//   - Due. A batch waits Policy.Batch, or Policy.UrgentBatch when one of its signals has a
//     severity floor of 2 or more urgent. The wait starts at the earliest last seen time of its
//     signals, and never before the batch's last decision.
//   - Groups. The signals of a batch are grouped by namespace (the part of EntityRef before the
//     first "/"); a signal without a namespace is grouped by its entity "<kind>/<ref>". Groups are
//     named g1, g2, ... in the order of their earliest first seen time, then of their key. A
//     group's floor is the most urgent floor of its signals.
//   - Candidates. Every incident of the batch's Organization that is open or mitigated and was
//     not merged into another. Its score for a group: 1.0 when one of its signals has the same
//     cluster and fingerprint as a signal of the group; else 0.9 when one has the same cluster
//     and entity; else 0.5 when one has the same cluster and namespace; else 0.25 when one has
//     the same cluster; else it is no candidate. A score below 1.0 is multiplied by 1.0 when the
//     incident's last activity (the latest last seen time of its signals, or its opening time) is
//     at most 2 hours before the group's earliest first seen time, by 0.75 when it is at most 24
//     hours before, and by 0.5 otherwise. Scores are rounded to two decimals. A group is offered
//     its 5 best candidates: highest score first, then the most recently opened, then the greater
//     identifier.
//   - Rule result. A group whose best candidate scores AttachScore or more is attached to it,
//     with the more urgent of the incident's severity and the group's floor. Any other group
//     opens a new incident with the group's floor as its severity. The rule path never answers
//     watch.
//   - Apply. The rules and a judge write through Applier.Apply (apply.go), which states how a
//     decision changes the records. Engine.Tick is the rule path end to end.
//
// Times in a batch: a signal's first seen time is when its alert started; its last seen time is
// when it was last reported.
package correlate

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// Decisions a group can get.
const (
	Attach = "attach"
	New    = "new"
	Watch  = "watch"
)

// AttachScore is the lowest score with which the rules attach a group to an incident.
const AttachScore = 0.6

// MaxCandidates is the number of candidates offered for one group.
const MaxCandidates = 5

// Scores before the recency factor.
const (
	scoreFingerprint = 1.0
	scoreEntity      = 0.9
	scoreNamespace   = 0.5
	scoreCluster     = 0.25
)

// Policy holds the waits of the batches.
type Policy struct {
	Batch       time.Duration // the wait of a batch
	UrgentBatch time.Duration // the wait of a batch with a floor of 2 or more urgent
}

// Validate returns an error when a wait is not positive or the urgent wait is the longer one.
func (p Policy) Validate() error {
	if p.Batch <= 0 || p.UrgentBatch <= 0 {
		return fmt.Errorf("correlate: both waits must be positive, got %s and %s", p.Batch, p.UrgentBatch)
	}
	if p.UrgentBatch > p.Batch {
		return fmt.Errorf("correlate: the urgent wait %s is longer than the wait %s", p.UrgentBatch, p.Batch)
	}
	return nil
}

// Batch is the pending signals of one Organization and one cluster, oldest first.
type Batch struct {
	Organization string
	Cluster      string
	Signals      []store.Signal
}

// Key returns "<organization>/<cluster>".
func (b Batch) Key() string { return b.Organization + "/" + b.Cluster }

// Group is the signals of a batch that are decided together.
type Group struct {
	ID      string // g1, g2, ...
	Key     string // the namespace, or the entity of a signal without one
	Floor   int    // the most urgent floor of its signals
	Signals []store.Signal
}

// IncidentView is an incident with the signals that belong to it.
type IncidentView struct {
	Incident store.Incident
	Signals  []store.Signal
}

// Candidate is an incident offered for a group, with its score.
type Candidate struct {
	IncidentID string    `json:"incidentId"`
	Title      string    `json:"title"`
	Severity   int       `json:"severity"`
	Entities   []string  `json:"entities"`
	OpenedAt   time.Time `json:"openedAt"`
	Score      float64   `json:"score"`
	reason     string
}

// Decision is the decision for one group, in the form of the correlator's answer.
type Decision struct {
	GroupID     string  `json:"groupId"`
	Decision    string  `json:"decision"`
	IncidentID  string  `json:"incidentId,omitempty"`
	CausalChain string  `json:"causalChain,omitempty"`
	Confidence  float64 `json:"confidence"`
	Severity    int     `json:"severity"`
}

// Answer is a rule result, or an answer of the correlator.
type Answer struct {
	Decisions []Decision `json:"decisions"`
}

// RequestSignal is one signal of a group in the document offered to the correlator.
type RequestSignal struct {
	SignalID      string    `json:"signalId"`
	Kind          string    `json:"kind"`
	Entity        string    `json:"entity"`
	Title         string    `json:"title"`
	FirstSeen     time.Time `json:"firstSeen"`
	SeverityFloor int       `json:"severityFloor"`
}

// RequestGroup is one group in the document offered to the correlator.
type RequestGroup struct {
	GroupID string          `json:"groupId"`
	Signals []RequestSignal `json:"signals"`
}

// Request is the document offered to the correlator.
type Request struct {
	Task         string         `json:"task"`
	Organization string         `json:"organization"`
	Groups       []RequestGroup `json:"groups"`
	Candidates   []Candidate    `json:"candidates"`
	Allowed      []string       `json:"allowed"`
}

// Prepared is everything one decision of a batch needs.
type Prepared struct {
	Batch      Batch
	Groups     []Group
	Candidates map[string][]Candidate // by group ID, best first
	Request    Request
	RuleResult Answer
}

// Pending returns the batches of the pending signals among signals, ordered by Organization
// and cluster. Signals that are cleared or belong to an incident are left out.
func Pending(signals []store.Signal) []Batch {
	byKey := map[string]*Batch{}
	for _, s := range signals {
		if s.State != store.SignalOpen || s.IncidentID != "" {
			continue
		}
		b := byKey[s.Organization+"/"+s.Cluster]
		if b == nil {
			b = &Batch{Organization: s.Organization, Cluster: s.Cluster}
			byKey[s.Organization+"/"+s.Cluster] = b
		}
		b.Signals = append(b.Signals, s)
	}
	out := make([]Batch, 0, len(byKey))
	for _, b := range byKey {
		sortSignals(b.Signals)
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Organization != out[j].Organization {
			return out[i].Organization < out[j].Organization
		}
		return out[i].Cluster < out[j].Cluster
	})
	return out
}

// DueAt returns when the batch is decided. lastDecision is the time of the batch's last
// decision, or the zero time. A batch without signals is never due: DueAt returns the zero time.
func DueAt(b Batch, lastDecision time.Time, p Policy) time.Time {
	if len(b.Signals) == 0 {
		return time.Time{}
	}
	start := b.Signals[0].LastSeen
	wait := p.Batch
	for _, s := range b.Signals {
		if s.LastSeen.Before(start) {
			start = s.LastSeen
		}
		if s.SeverityFloor <= 2 {
			wait = p.UrgentBatch
		}
	}
	if lastDecision.After(start) {
		start = lastDecision
	}
	return start.Add(wait)
}

// Namespace returns the namespace of a signal: the part of EntityRef before the first "/", or
// the empty string.
func Namespace(s store.Signal) string {
	ns, _, found := strings.Cut(s.EntityRef, "/")
	if !found {
		return ""
	}
	return ns
}

// EntityOf returns "<kind>/<ref>" of a signal.
func EntityOf(s store.Signal) string { return s.EntityKind + "/" + s.EntityRef }

// Groups returns the groups of a batch.
func Groups(b Batch) []Group {
	byKey := map[string]*Group{}
	for _, s := range b.Signals {
		key := Namespace(s)
		if key == "" {
			key = EntityOf(s)
		}
		g := byKey[key]
		if g == nil {
			g = &Group{Key: key, Floor: store.SeverityLowest}
			byKey[key] = g
		}
		g.Signals = append(g.Signals, s)
		g.Floor = min(g.Floor, s.SeverityFloor)
	}
	out := make([]Group, 0, len(byKey))
	for _, g := range byKey {
		sortSignals(g.Signals)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Signals[0].FirstSeen, out[j].Signals[0].FirstSeen
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].Key < out[j].Key
	})
	for i := range out {
		out[i].ID = fmt.Sprintf("g%d", i+1)
	}
	return out
}

// Score returns the score of an incident for a group and the reason for it. A score of 0 means
// the incident is no candidate.
func Score(g Group, iv IncidentView) (float64, string) {
	base, reason := 0.0, ""
	for _, gs := range g.Signals {
		for _, is := range iv.Signals {
			s, r := 0.0, ""
			switch {
			case gs.Cluster == is.Cluster && gs.Fingerprint == is.Fingerprint:
				s, r = scoreFingerprint, "the same alert "+gs.Kind+" is reported again"
			case gs.Cluster == is.Cluster && EntityOf(gs) == EntityOf(is):
				s, r = scoreEntity, "the same entity "+EntityOf(gs)+" reports again"
			case gs.Cluster == is.Cluster && Namespace(gs) != "" && Namespace(gs) == Namespace(is):
				s, r = scoreNamespace, "the same namespace "+Namespace(gs)
			case gs.Cluster == is.Cluster:
				s, r = scoreCluster, "the same cluster "+gs.Cluster
			}
			if s > base {
				base, reason = s, r
			}
		}
	}
	if base == 0 {
		return 0, ""
	}
	if base < scoreFingerprint {
		gap := earliest(g).Sub(lastActivity(iv))
		switch {
		case gap <= 2*time.Hour:
		case gap <= 24*time.Hour:
			base *= 0.75
		default:
			base *= 0.5
		}
	}
	return round2(base), reason
}

// Candidates returns the candidates of a group among incidents, best first, at most
// MaxCandidates.
func Candidates(g Group, incidents []IncidentView) []Candidate {
	var out []Candidate
	for _, iv := range incidents {
		in := iv.Incident
		if in.Organization != organizationOf(g) || in.ParentID != "" ||
			(in.State != store.IncidentOpen && in.State != store.IncidentMitigated) {
			continue
		}
		score, reason := Score(g, iv)
		if score == 0 {
			continue
		}
		out = append(out, Candidate{IncidentID: in.ID, Title: in.Title, Severity: in.Severity,
			Entities: entities(iv.Signals), OpenedAt: in.OpenedAt.UTC(), Score: score, reason: reason})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if !out[i].OpenedAt.Equal(out[j].OpenedAt) {
			return out[i].OpenedAt.After(out[j].OpenedAt)
		}
		return out[i].IncidentID > out[j].IncidentID
	})
	if len(out) > MaxCandidates {
		out = out[:MaxCandidates]
	}
	return out
}

// Rule returns the rule decision for a group, given its candidates best first.
func Rule(g Group, candidates []Candidate) Decision {
	if len(candidates) > 0 && candidates[0].Score >= AttachScore {
		c := candidates[0]
		return Decision{GroupID: g.ID, Decision: Attach, IncidentID: c.IncidentID,
			CausalChain: "rules: " + c.reason, Confidence: c.Score, Severity: min(c.Severity, g.Floor)}
	}
	best := 0.0
	if len(candidates) > 0 {
		best = candidates[0].Score
	}
	return Decision{GroupID: g.ID, Decision: New,
		CausalChain: fmt.Sprintf("rules: no open incident scores %.2f or more", AttachScore),
		Confidence:  round2(1 - best), Severity: g.Floor}
}

// Prepare groups a batch, scores the candidates among incidents and makes the rule result and
// the document offered to the correlator.
func Prepare(b Batch, incidents []IncidentView) Prepared {
	p := Prepared{Batch: b, Groups: Groups(b), Candidates: map[string][]Candidate{}}
	p.Request = Request{Task: "correlate", Organization: b.Organization,
		Groups: []RequestGroup{}, Candidates: []Candidate{}}
	offered := map[string]int{} // incident ID -> index in Request.Candidates
	for _, g := range p.Groups {
		cands := Candidates(g, incidents)
		p.Candidates[g.ID] = cands
		p.RuleResult.Decisions = append(p.RuleResult.Decisions, Rule(g, cands))
		rg := RequestGroup{GroupID: g.ID}
		for _, s := range g.Signals {
			rg.Signals = append(rg.Signals, RequestSignal{SignalID: s.ID, Kind: s.Kind, Entity: EntityOf(s),
				Title: s.Title, FirstSeen: s.FirstSeen.UTC(), SeverityFloor: s.SeverityFloor})
		}
		p.Request.Groups = append(p.Request.Groups, rg)
		for _, c := range cands {
			if i, ok := offered[c.IncidentID]; ok {
				if c.Score > p.Request.Candidates[i].Score {
					p.Request.Candidates[i].Score = c.Score
				}
				continue
			}
			offered[c.IncidentID] = len(p.Request.Candidates)
			p.Request.Candidates = append(p.Request.Candidates, c)
		}
	}
	p.Request.Allowed = []string{New, Watch}
	if len(p.Request.Candidates) > 0 {
		p.Request.Allowed = []string{Attach, New, Watch}
	}
	return p
}

// Load reads the pending batches of every Organization (organization empty) or of one, and
// the incidents that can be candidates. It reads at most store.MaxLimit open signals and
// store.MaxLimit open and as many mitigated incidents per call.
func Load(ctx context.Context, r store.Reader, organization string) ([]Batch, map[string][]IncidentView, error) {
	sigs, err := r.ListSignals(ctx, store.SignalFilter{Organization: organization, State: store.SignalOpen,
		Limit: store.MaxLimit})
	if err != nil {
		return nil, nil, err
	}
	batches := Pending(sigs)
	views := map[string][]IncidentView{}
	for _, b := range batches {
		if _, done := views[b.Organization]; done {
			continue
		}
		var list []IncidentView
		for _, state := range []string{store.IncidentOpen, store.IncidentMitigated} {
			ins, err := r.ListIncidents(ctx, store.IncidentFilter{Organization: b.Organization, State: state,
				Limit: store.MaxLimit})
			if err != nil {
				return nil, nil, err
			}
			for _, in := range ins {
				if in.ParentID != "" {
					continue
				}
				ss, err := r.ListSignals(ctx, store.SignalFilter{Organization: b.Organization, IncidentID: in.ID,
					Limit: store.MaxLimit})
				if err != nil {
					return nil, nil, err
				}
				list = append(list, IncidentView{Incident: in, Signals: ss})
			}
		}
		views[b.Organization] = list
	}
	return batches, views, nil
}

func organizationOf(g Group) string {
	if len(g.Signals) == 0 {
		return ""
	}
	return g.Signals[0].Organization
}

func earliest(g Group) time.Time {
	t := g.Signals[0].FirstSeen
	for _, s := range g.Signals {
		if s.FirstSeen.Before(t) {
			t = s.FirstSeen
		}
	}
	return t
}

func lastActivity(iv IncidentView) time.Time {
	t := iv.Incident.OpenedAt
	for _, s := range iv.Signals {
		if s.LastSeen.After(t) {
			t = s.LastSeen
		}
	}
	return t
}

func entities(signals []store.Signal) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range signals {
		if e := EntityOf(s); !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Strings(out)
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func sortSignals(ss []store.Signal) {
	sort.Slice(ss, func(i, j int) bool {
		if !ss[i].FirstSeen.Equal(ss[j].FirstSeen) {
			return ss[i].FirstSeen.Before(ss[j].FirstSeen)
		}
		return ss[i].ID < ss[j].ID
	})
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
