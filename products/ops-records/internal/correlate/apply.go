package correlate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// How Apply applied the decision of one group.
const (
	AppliedAttach = "attached" // the free signals joined an incident
	AppliedNew    = "opened"   // the free signals opened a new incident
	AppliedWatch  = "watching" // nothing was written; the signals stay pending
	AppliedNone   = "nothing"  // every signal of the group already belongs to an incident
)

// Timeline entries Apply writes.
const (
	EventOpened   = "opened"
	EventAttached = "signals_attached"
	EventSeverity = "severity"
)

// RuleActor is the actor name of the rule path in a timeline.
const RuleActor = "correlate-rules"

// maxParentHops is how far Apply follows merged incidents to the one they were merged into.
const maxParentHops = 10

// ErrBadDecision is wrapped by every error about the decisions given to Apply.
var ErrBadDecision = errors.New("correlate: bad decisions")

// Actor is who decided: the rules (store.ByRules) or a judge (store.ByAgent), with a name.
type Actor struct {
	Kind string
	Name string
}

// Rules is the actor of the rule path.
var Rules = Actor{Kind: store.ByRules, Name: RuleActor}

// GroupResult is what Apply did for one group.
type GroupResult struct {
	GroupID    string
	Applied    string   // one of the Applied constants
	IncidentID string   // the incident the signals joined or opened; empty otherwise
	SignalIDs  []string // the signals that changed
	Note       string   // why the result differs from the decision; empty when it does not
}

// Applier applies decisions to the records.
type Applier struct {
	// NewIncidentID makes the identifier of a new incident. Nil means ids.NewIncident.
	NewIncidentID func() string
}

// Apply applies one decision per group in the transaction tx. Every group needs exactly one
// decision and every decision one group, or Apply returns an error wrapping ErrBadDecision and
// writes nothing. The decisions are applied in the order of the groups:
//
//   - Only the free signals of a group are moved: those that, read again in tx, belong to no
//     incident. A group without free signals gets AppliedNone.
//   - watch writes nothing.
//   - attach follows a merged incident to the incident it was merged into. When that incident
//     is resolved, the group opens a new incident instead. The signals join the incident, an
//     entry signals_attached is written, and the severity becomes the decision's severity,
//     made at least as urgent as every floor of the incident's signals, unless a person set it.
//     A changed severity writes an entry severity.
//   - new opens an incident titled after the group's most urgent free signal, with the
//     decision's severity made at least as urgent as the floor of the free signals, opened and
//     rated by the actor's kind. An entry opened is written.
func (a Applier) Apply(ctx context.Context, tx store.Tx, groups []Group, decisions []Decision, by Actor,
	now time.Time) ([]GroupResult, error) {
	if by.Kind != store.ByRules && by.Kind != store.ByAgent || by.Name == "" {
		return nil, fmt.Errorf("%w: actor %q %q", ErrBadDecision, by.Kind, by.Name)
	}
	byGroup := map[string]Decision{}
	for _, d := range decisions {
		if _, dup := byGroup[d.GroupID]; dup {
			return nil, fmt.Errorf("%w: group %q decided twice", ErrBadDecision, d.GroupID)
		}
		switch {
		case d.Decision != Attach && d.Decision != New && d.Decision != Watch:
			return nil, fmt.Errorf("%w: decision %q", ErrBadDecision, d.Decision)
		case d.Decision == Attach && d.IncidentID == "":
			return nil, fmt.Errorf("%w: attach of group %q names no incident", ErrBadDecision, d.GroupID)
		case d.Decision != Watch && (d.Severity < store.SeverityHighest || d.Severity > store.SeverityLowest):
			return nil, fmt.Errorf("%w: severity %d", ErrBadDecision, d.Severity)
		}
		byGroup[d.GroupID] = d
	}
	for _, g := range groups {
		if _, ok := byGroup[g.ID]; !ok {
			return nil, fmt.Errorf("%w: group %q has no decision", ErrBadDecision, g.ID)
		}
	}
	if len(byGroup) != len(groups) {
		return nil, fmt.Errorf("%w: a decision names a group that was not offered", ErrBadDecision)
	}
	out := make([]GroupResult, 0, len(groups))
	for _, g := range groups {
		r, err := a.applyGroup(ctx, tx, g, byGroup[g.ID], by, now)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (a Applier) applyGroup(ctx context.Context, tx store.Tx, g Group, d Decision, by Actor,
	now time.Time) (GroupResult, error) {
	res := GroupResult{GroupID: g.ID}
	var free []store.Signal
	for _, s := range g.Signals {
		cur, err := tx.GetSignal(ctx, s.Organization, s.ID)
		if err != nil {
			return res, err
		}
		if cur.IncidentID == "" {
			free = append(free, cur)
		}
	}
	if len(free) == 0 {
		res.Applied = AppliedNone
		return res, nil
	}
	if d.Decision == Watch {
		res.Applied = AppliedWatch
		return res, nil
	}
	org := free[0].Organization
	if d.Decision == Attach {
		target, err := root(ctx, tx, org, d.IncidentID)
		if err != nil {
			return res, err
		}
		if target.State != store.IncidentResolved {
			if target.ID != d.IncidentID {
				res.Note = d.IncidentID + " was merged into " + target.ID
			}
			return a.attach(ctx, tx, res, target, free, d, by, now)
		}
		res.Note = target.ID + " is resolved; a new incident was opened"
	}
	return a.open(ctx, tx, res, org, free, d, by, now)
}

func (a Applier) attach(ctx context.Context, tx store.Tx, res GroupResult, in store.Incident, free []store.Signal,
	d Decision, by Actor, now time.Time) (GroupResult, error) {
	moved, err := moveSignals(ctx, tx, free, in.ID)
	if err != nil {
		return res, err
	}
	if err := appendEvent(ctx, tx, in, now, by, EventAttached, map[string]any{"groupId": d.GroupID,
		"signals": moved, "decision": d.Decision, "causalChain": d.CausalChain, "confidence": d.Confidence}); err != nil {
		return res, err
	}
	if in.SeveritySetBy != store.ByPerson {
		all, err := tx.ListSignals(ctx, store.SignalFilter{Organization: in.Organization, IncidentID: in.ID,
			Limit: store.MaxLimit})
		if err != nil {
			return res, err
		}
		want := d.Severity
		for _, s := range all {
			want = min(want, s.SeverityFloor)
		}
		if want != in.Severity {
			from := in.Severity
			in.Severity, in.SeveritySetBy = want, by.Kind
			if err := tx.UpdateIncident(ctx, in); err != nil {
				return res, err
			}
			if err := appendEvent(ctx, tx, in, now, by, EventSeverity, map[string]any{"from": from, "to": want}); err != nil {
				return res, err
			}
		}
	}
	res.Applied, res.IncidentID, res.SignalIDs = AppliedAttach, in.ID, moved
	return res, nil
}

func (a Applier) open(ctx context.Context, tx store.Tx, res GroupResult, org string, free []store.Signal,
	d Decision, by Actor, now time.Time) (GroupResult, error) {
	newID := a.NewIncidentID
	if newID == nil {
		newID = ids.NewIncident
	}
	head := free[0]
	sev := d.Severity
	for _, s := range free {
		sev = min(sev, s.SeverityFloor)
		if s.SeverityFloor < head.SeverityFloor {
			head = s
		}
	}
	in := store.Incident{ID: newID(), Organization: org, Title: head.Title, Severity: sev,
		SeveritySetBy: by.Kind, State: store.IncidentOpen, OpenedBy: by.Kind, OpenedAt: now}
	if err := tx.InsertIncident(ctx, in); err != nil {
		return res, err
	}
	moved, err := moveSignals(ctx, tx, free, in.ID)
	if err != nil {
		return res, err
	}
	if err := appendEvent(ctx, tx, in, now, by, EventOpened, map[string]any{"groupId": d.GroupID,
		"signals": moved, "decision": d.Decision, "causalChain": d.CausalChain, "confidence": d.Confidence,
		"severity": sev}); err != nil {
		return res, err
	}
	res.Applied, res.IncidentID, res.SignalIDs = AppliedNew, in.ID, moved
	return res, nil
}

// root returns the incident id, or the incident it was merged into, following at most
// maxParentHops merges.
func root(ctx context.Context, r store.Reader, org, id string) (store.Incident, error) {
	in, err := r.GetIncident(ctx, org, id)
	if err != nil {
		return in, err
	}
	for i := 0; in.ParentID != ""; i++ {
		if i == maxParentHops {
			return in, fmt.Errorf("correlate: incident %s is merged more than %d times deep", id, maxParentHops)
		}
		if in, err = r.GetIncident(ctx, org, in.ParentID); err != nil {
			return in, err
		}
	}
	return in, nil
}

func moveSignals(ctx context.Context, tx store.Tx, free []store.Signal, incidentID string) ([]string, error) {
	out := make([]string, 0, len(free))
	for _, s := range free {
		s.IncidentID = incidentID
		if err := tx.UpdateSignal(ctx, s); err != nil {
			return nil, err
		}
		out = append(out, s.ID)
	}
	return out, nil
}

func appendEvent(ctx context.Context, tx store.Tx, in store.Incident, at time.Time, by Actor, kind string,
	data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.AppendIncidentEvent(ctx, store.IncidentEvent{Organization: in.Organization, IncidentID: in.ID,
		At: at, ActorKind: by.Kind, Actor: by.Name, Kind: kind, Data: raw})
	return err
}

// BatchResult is what one decision of a batch did.
type BatchResult struct {
	Organization string
	Cluster      string
	Groups       []GroupResult
}

// Engine is the rule path: it decides every due batch by its rule result, without any agent.
// Tick may be called from several goroutines; one call runs at a time.
type Engine struct {
	Store   store.Store
	Policy  Policy
	Applier Applier
	Now     func() time.Time // nil means time.Now

	mu sync.Mutex
}

// Tick decides every batch that is due now, each in its own transaction, and returns what
// was done, ordered by Organization and cluster. A batch that is not due yet is left alone.
// The rule result attaches or opens, so a decided batch has no pending signal left; the
// batches of one call need not see each other's incidents, because a candidate shares the
// cluster of its group.
func (e *Engine) Tick(ctx context.Context) ([]BatchResult, error) {
	if err := e.Policy.Validate(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	if e.Now != nil {
		now = e.Now()
	}
	batches, views, err := Load(ctx, e.Store, "")
	if err != nil {
		return nil, err
	}
	var out []BatchResult
	for _, b := range batches {
		if DueAt(b, time.Time{}, e.Policy).After(now) {
			continue
		}
		p := Prepare(b, views[b.Organization])
		var groups []GroupResult
		err := e.Store.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
			var err error
			groups, err = e.Applier.Apply(ctx, tx, p.Groups, p.RuleResult.Decisions, Rules, now)
			return err
		})
		if err != nil {
			return out, fmt.Errorf("correlate: batch %s: %w", b.Key(), err)
		}
		out = append(out, BatchResult{Organization: b.Organization, Cluster: b.Cluster, Groups: groups})
	}
	return out, nil
}
