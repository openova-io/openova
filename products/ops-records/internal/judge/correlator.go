package judge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/openova-io/openova/products/ops-records/internal/correlate"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// The correlator judge.
const (
	Correlator   = "correlator"   // its name in Policy.Judges and in timelines
	SubjectBatch = "signal_batch" // the subject kind of its questions
)

// An attach whose causal chain has fewer than MinCausalChain characters, or whose confidence is
// below MinConfidence, is changed to new.
const (
	MinCausalChain = 12
	MinConfidence  = 0.5
)

// Timeline entries a late revision writes.
const (
	EventLate     = "judge_late"
	EventDetached = "signals_detached"
	EventMerged   = "merged"
)

// maxHops is how far a late revision follows merged incidents.
const maxHops = 10

// ErrInvalidAnswer is wrapped by every error of answer validation.
var ErrInvalidAnswer = errors.New("judge: invalid answer")

var decisionKeys = map[string]bool{"groupId": true, "decision": true, "incidentId": true, "causalChain": true,
	"confidence": true, "severity": true}

// ValidateCorrelation checks an answer of the correlator against the request it was offered, in
// this order; the first failure rejects the whole answer:
//
//  1. The answer is one JSON object with the one key "decisions", a list of objects with the
//     keys groupId, decision, confidence and severity and the optional keys incidentId and
//     causalChain, and no other key. groupId, incidentId and causalChain are strings; decision
//     is attach, new or watch; confidence is a number from 0 to 1; severity an integer from 1 to 4.
//  2. Every offered group appears exactly once, and no other group appears.
//  3. Every decision is one of the request's allowed decisions.
//  4. An attach names one of the offered candidates. When its causal chain has fewer than
//     MinCausalChain characters or its confidence is below MinConfidence it is changed to new.
//     A decision that is not attach keeps no incident.
//  5. A severity is not less urgent than the most urgent severity floor of its group's signals.
//
// It returns the decisions to apply, in the order of the answer.
func ValidateCorrelation(req correlate.Request, answer json.RawMessage) ([]correlate.Decision, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalidAnswer}, args...)...)
	}
	top, err := object(answer)
	if err != nil || len(top) != 1 || top["decisions"] == nil {
		return nil, bad("the answer is not an object with the one key decisions")
	}
	var items []json.RawMessage
	if !bytes.HasPrefix(top["decisions"], []byte("[")) || json.Unmarshal(top["decisions"], &items) != nil {
		return nil, bad("decisions is not a list")
	}
	out := make([]correlate.Decision, 0, len(items))
	for i, item := range items {
		d, err := decision(item)
		if err != nil {
			return nil, bad("decision %d: %v", i, err)
		}
		out = append(out, d)
	}
	floors := map[string]int{}
	for _, g := range req.Groups {
		floors[g.GroupID] = store.SeverityLowest
		for _, s := range g.Signals {
			floors[g.GroupID] = min(floors[g.GroupID], s.SeverityFloor)
		}
	}
	seen := map[string]bool{}
	for _, d := range out {
		if _, offered := floors[d.GroupID]; !offered || seen[d.GroupID] {
			return nil, bad("group %q is not offered or is decided twice", d.GroupID)
		}
		seen[d.GroupID] = true
	}
	if len(seen) != len(floors) {
		return nil, bad("%d of %d offered groups are decided", len(seen), len(floors))
	}
	allowed := map[string]bool{}
	for _, a := range req.Allowed {
		allowed[a] = true
	}
	for _, d := range out {
		if !allowed[d.Decision] {
			return nil, bad("group %s: decision %s is not allowed", d.GroupID, d.Decision)
		}
	}
	candidates := map[string]bool{}
	for _, c := range req.Candidates {
		candidates[c.IncidentID] = true
	}
	for i := range out {
		d := &out[i]
		if d.Decision != correlate.Attach {
			d.IncidentID = ""
			continue
		}
		if !candidates[d.IncidentID] {
			return nil, bad("group %s: incident %q is not an offered candidate", d.GroupID, d.IncidentID)
		}
		if utf8.RuneCountInString(d.CausalChain) < MinCausalChain || d.Confidence < MinConfidence {
			d.Decision, d.IncidentID = correlate.New, ""
		}
	}
	for _, d := range out {
		if d.Severity > floors[d.GroupID] {
			return nil, bad("group %s: severity %d is less urgent than its floor %d", d.GroupID, d.Severity, floors[d.GroupID])
		}
	}
	return out, nil
}

// object decodes one JSON object into its keys.
func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return nil, errors.New("not an object")
	}
	var m map[string]json.RawMessage
	return m, json.Unmarshal(raw, &m)
}

func decision(raw json.RawMessage) (correlate.Decision, error) {
	var d correlate.Decision
	m, err := object(raw)
	if err != nil {
		return d, err
	}
	for k := range m {
		if !decisionKeys[k] {
			return d, fmt.Errorf("unknown key %q", k)
		}
	}
	for _, k := range []string{"groupId", "decision", "confidence", "severity"} {
		if m[k] == nil {
			return d, fmt.Errorf("%s is missing", k)
		}
	}
	for k, v := range m {
		if string(bytes.TrimSpace(v)) == "null" {
			return d, fmt.Errorf("%s is null", k)
		}
	}
	strs := map[string]*string{"groupId": &d.GroupID, "decision": &d.Decision, "incidentId": &d.IncidentID,
		"causalChain": &d.CausalChain}
	for k, p := range strs {
		if v := m[k]; v != nil && json.Unmarshal(v, p) != nil {
			return d, fmt.Errorf("%s is not a string", k)
		}
	}
	if json.Unmarshal(m["confidence"], &d.Confidence) != nil || d.Confidence < 0 || d.Confidence > 1 {
		return d, errors.New("confidence is not a number from 0 to 1")
	}
	if json.Unmarshal(m["severity"], &d.Severity) != nil || d.Severity < store.SeverityHighest || d.Severity > store.SeverityLowest {
		return d, errors.New("severity is not an integer from 1 to 4")
	}
	if d.Decision != correlate.Attach && d.Decision != correlate.New && d.Decision != correlate.Watch {
		return d, fmt.Errorf("decision %q is not attach, new or watch", d.Decision)
	}
	return d, nil
}

// BatchSubject is the subject identifier of the questions about a batch.
func BatchSubject(b correlate.Batch) string { return "cluster/" + b.Cluster }

// Correlation is the question for the correlator about one prepared batch. The rules write the
// rule result through a; a valid answer is written through a with the actor agent correlator.
// The question is urgent when a signal of the batch has a severity floor of 2 or more urgent.
func Correlation(p correlate.Prepared, a correlate.Applier) Question {
	urgent := false
	for _, g := range p.Groups {
		urgent = urgent || g.Floor <= 2
	}
	agent := correlate.Actor{Kind: store.ByAgent, Name: Correlator}
	return Question{Judge: Correlator, Organization: p.Batch.Organization, SubjectKind: SubjectBatch,
		SubjectID: BatchSubject(p.Batch), Urgent: urgent, Offered: p.Request, RuleResult: p.RuleResult,
		Validate: func(answer json.RawMessage) (any, error) { return ValidateCorrelation(p.Request, answer) },
		Rule: func(ctx context.Context, tx store.Tx, now time.Time) (any, error) {
			return a.Apply(ctx, tx, p.Groups, p.RuleResult.Decisions, correlate.Rules, now)
		},
		Apply: func(ctx context.Context, tx store.Tx, answer any, now time.Time) error {
			_, err := a.Apply(ctx, tx, p.Groups, answer.([]correlate.Decision), agent, now)
			return err
		},
		Revise: func(ctx context.Context, tx store.Tx, written, answer any, now time.Time) error {
			return Revise(ctx, tx, a, p.Groups, written.([]correlate.GroupResult), answer.([]correlate.Decision), agent, now)
		},
	}
}

// Revise writes a late answer of the correlator over what the rules wrote, group by group:
//
//   - A group the rules moved no signal of is left alone.
//   - watch: an entry judge_late on the rules' incident; nothing moves.
//   - When the rules' incident is no longer open, was merged, or has a severity a person set, a
//     person has acted: only an entry judge_late is written.
//   - The same place (attach to the rules' incident, followed through merges, or new when the
//     rules opened an incident for the group): the incident is re-rated, never less urgent
//     than it is, and gets an entry judge_late.
//   - Otherwise the group's signals that are still in the rules' incident leave it (entry
//     signals_detached) and the answer's decision is applied to them through a. When the rules
//     opened that incident for the group and it has no signal left, it is merged into the
//     incident the signals went to (entry merged).
//
// A written group that the answer does not decide, or that is not one of groups, is an error:
// validation decides every offered group, and the rules wrote the same groups.
func Revise(ctx context.Context, tx store.Tx, a correlate.Applier, groups []correlate.Group,
	written []correlate.GroupResult, answer []correlate.Decision, by correlate.Actor, now time.Time) error {
	decided := map[string]correlate.Decision{}
	for _, d := range answer {
		decided[d.GroupID] = d
	}
	for _, r := range written {
		d, ok := decided[r.GroupID]
		if !ok {
			return fmt.Errorf("judge: revise: the rules wrote group %q and the answer does not decide it", r.GroupID)
		}
		if r.IncidentID == "" {
			continue
		}
		var g correlate.Group
		for _, x := range groups {
			if x.ID == r.GroupID {
				g = x
			}
		}
		if len(g.Signals) == 0 {
			return fmt.Errorf("judge: revise: the rules wrote group %q and it is not an offered group with signals", r.GroupID)
		}
		org := g.Signals[0].Organization
		ruled, err := tx.GetIncident(ctx, org, r.IncidentID)
		if err != nil {
			return err
		}
		note := func(in store.Incident, how string) error {
			return event(ctx, tx, in, now, by, EventLate, map[string]any{"groupId": d.GroupID, "decision": d.Decision,
				"incidentId": d.IncidentID, "causalChain": d.CausalChain, "confidence": d.Confidence,
				"severity": d.Severity, "revision": how})
		}
		same := d.Decision == correlate.New && r.Applied == correlate.AppliedNew
		if d.Decision == correlate.Attach {
			target, err := follow(ctx, tx, org, d.IncidentID)
			if err != nil {
				return err
			}
			same = target.ID == ruled.ID
		}
		switch {
		case d.Decision == correlate.Watch:
			err = note(ruled, "kept")
		case ruled.State != store.IncidentOpen || ruled.ParentID != "" || ruled.SeveritySetBy == store.ByPerson:
			err = note(ruled, "kept: a person acted on the incident")
		case same:
			if err = rate(ctx, tx, ruled, d.Severity, by, now); err == nil {
				err = note(ruled, "rated")
			}
		default:
			err = move(ctx, tx, a, g, r, ruled, d, by, now)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func move(ctx context.Context, tx store.Tx, a correlate.Applier, g correlate.Group, r correlate.GroupResult,
	ruled store.Incident, d correlate.Decision, by correlate.Actor, now time.Time) error {
	var freed []store.Signal
	var freedIDs []string
	for _, id := range r.SignalIDs {
		s, err := tx.GetSignal(ctx, ruled.Organization, id)
		if err != nil {
			return err
		}
		if s.IncidentID != ruled.ID {
			continue
		}
		s.IncidentID = ""
		if err := tx.UpdateSignal(ctx, s); err != nil {
			return err
		}
		freed, freedIDs = append(freed, s), append(freedIDs, s.ID)
	}
	if len(freed) == 0 {
		return event(ctx, tx, ruled, now, by, EventLate, map[string]any{"groupId": d.GroupID, "decision": d.Decision,
			"revision": "kept: no signal of the group is left in the incident"})
	}
	res, err := a.Apply(ctx, tx, []correlate.Group{{ID: g.ID, Key: g.Key, Floor: g.Floor, Signals: freed}},
		[]correlate.Decision{d}, by, now)
	if err != nil {
		return err
	}
	to := res[0].IncidentID
	if err := event(ctx, tx, ruled, now, by, EventDetached, map[string]any{"groupId": d.GroupID, "signals": freedIDs,
		"to": to, "causalChain": d.CausalChain, "confidence": d.Confidence}); err != nil {
		return err
	}
	if r.Applied != correlate.AppliedNew || to == "" || to == ruled.ID {
		return nil
	}
	left, err := tx.ListSignals(ctx, store.SignalFilter{Organization: ruled.Organization, IncidentID: ruled.ID, Limit: 1})
	if err != nil || len(left) > 0 {
		return err
	}
	ruled.ParentID = to
	if err := tx.UpdateIncident(ctx, ruled); err != nil {
		return err
	}
	return event(ctx, tx, ruled, now, by, EventMerged, map[string]any{"into": to})
}

// rate makes an incident as urgent as want and every floor of its signals, never less urgent
// than it is, unless a person set its severity.
func rate(ctx context.Context, tx store.Tx, in store.Incident, want int, by correlate.Actor, now time.Time) error {
	if in.SeveritySetBy == store.ByPerson {
		return nil
	}
	signals, err := tx.ListSignals(ctx, store.SignalFilter{Organization: in.Organization, IncidentID: in.ID, Limit: store.MaxLimit})
	if err != nil {
		return err
	}
	want = min(want, in.Severity)
	for _, s := range signals {
		want = min(want, s.SeverityFloor)
	}
	if want == in.Severity {
		return nil
	}
	from := in.Severity
	in.Severity, in.SeveritySetBy = want, by.Kind
	if err := tx.UpdateIncident(ctx, in); err != nil {
		return err
	}
	return event(ctx, tx, in, now, by, correlate.EventSeverity, map[string]any{"from": from, "to": want})
}

// follow returns the incident id, or the one it was merged into.
func follow(ctx context.Context, r store.Reader, org, id string) (store.Incident, error) {
	in, err := r.GetIncident(ctx, org, id)
	for i := 0; err == nil && in.ParentID != ""; i++ {
		if i == maxHops {
			return in, fmt.Errorf("judge: incident %s is merged more than %d times deep", id, maxHops)
		}
		in, err = r.GetIncident(ctx, org, in.ParentID)
	}
	return in, err
}

func event(ctx context.Context, tx store.Tx, in store.Incident, at time.Time, by correlate.Actor, kind string, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.AppendIncidentEvent(ctx, store.IncidentEvent{Organization: in.Organization, IncidentID: in.ID, At: at,
		ActorKind: by.Kind, Actor: by.Name, Kind: kind, Data: raw})
	return err
}

// CorrelationTicker asks the correlator about every batch that is due.
type CorrelationTicker struct {
	Store      store.Store
	Dispatcher *Dispatcher
	Batches    correlate.Policy // the waits; Policy.Batches
	Applier    correlate.Applier
	Now        func() time.Time // nil means time.Now
}

// Tick asks about every pending batch that is due and has no question in flight, and returns
// the identifiers of the judgements it started. A batch's last decision is the time its newest
// judgement was answered, or else asked. A batch that fails does not stop the batches after
// it: Tick goes on and returns the errors of every failed batch, joined.
func (c *CorrelationTicker) Tick(ctx context.Context) ([]string, error) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	batches, views, err := correlate.Load(ctx, c.Store, "")
	if err != nil {
		return nil, err
	}
	var asked []string
	var errs []error
	for _, b := range batches {
		subject := BatchSubject(b)
		if c.Dispatcher.Busy(b.Organization, SubjectBatch, subject) {
			continue
		}
		last, err := c.Store.ListJudgements(ctx, store.JudgementFilter{Organization: b.Organization, Judge: Correlator,
			SubjectKind: SubjectBatch, SubjectID: subject, Limit: 1})
		if err != nil {
			errs = append(errs, fmt.Errorf("judge: batch %s: %w", b.Key(), err))
			continue
		}
		var lastDecision time.Time
		if len(last) == 1 {
			lastDecision = last[0].AskedAt
			if last[0].AnsweredAt != nil {
				lastDecision = *last[0].AnsweredAt
			}
		}
		if correlate.DueAt(b, lastDecision, c.Batches).After(now) {
			continue
		}
		id, err := c.Dispatcher.Ask(ctx, Correlation(correlate.Prepare(b, views[b.Organization]), c.Applier))
		if errors.Is(err, ErrBusy) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("judge: batch %s: %w", b.Key(), err))
			continue
		}
		asked = append(asked, id)
	}
	return asked, errors.Join(errs...)
}
