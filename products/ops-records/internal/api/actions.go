package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// The actions of POST /api/v1/ops/incidents/{id}/actions.
const (
	// ActionAcknowledge writes the timeline entry "acknowledged". The state does not change.
	ActionAcknowledge = "acknowledge"
	// ActionSetSeverity sets the severity (field severity, 1..4) as set by a person, which rules
	// and agents never change again. Timeline "severity", feedback "severity".
	ActionSetSeverity = "set_severity"
	// ActionMerge merges the incident into the incident otherIncidentId, followed to the one it
	// was merged into: the signals move there, the incident's parent is set, and the target
	// becomes as urgent as the incident unless a person set its severity. Timeline "merged" on
	// the incident and "signals_attached" on the target, feedback "merge".
	ActionMerge = "merge"
	// ActionDetach takes the signal signalId out of the incident; the signal is pending again
	// and is decided anew. Timeline "signals_detached", feedback "detach".
	ActionDetach = "detach"
	// ActionResolve resolves the incident. Timeline "resolved".
	ActionResolve = "resolve"
)

// Timeline entries the actions write that no other package writes.
const (
	EventAcknowledged = "acknowledged"
	EventResolved     = "resolved"
	EventMerged       = "merged"
	EventAttached     = "signals_attached"
	EventDetached     = "signals_detached"
	EventSeverity     = "severity"
)

// maxHops is how far an action follows merged incidents to the one they were merged into.
const maxHops = 10

// maxActionBody is the largest request body of the actions route.
const maxActionBody = 16 << 10

// ActionRequest is the body of the actions route. Unknown fields are refused.
type ActionRequest struct {
	Action          string `json:"action"`
	Severity        int    `json:"severity,omitempty"`
	OtherIncidentID string `json:"otherIncidentId,omitempty"`
	SignalID        string `json:"signalId,omitempty"`
}

// conflict is an action the state of the records does not allow.
type conflict string

func (c conflict) Error() string { return string(c) }

func (s *Server) act(w http.ResponseWriter, r *http.Request, c caller) {
	var req ActionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxActionBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", "the body is not an action: "+err.Error())
		return
	}
	if msg := checkRequest(req); msg != "" {
		fail(w, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	var touched []store.Incident
	err := s.Store.InTx(r.Context(), func(ctx context.Context, tx store.Tx) error {
		var err error
		touched, err = s.apply(ctx, tx, c, r.PathValue("id"), req)
		return err
	})
	var cf conflict
	switch {
	case errors.As(err, &cf):
		fail(w, http.StatusConflict, "invalid_state", cf.Error())
		return
	case err != nil:
		s.storeFailed(w, err)
		return
	}
	for _, in := range touched {
		if s.Hub != nil {
			s.Hub.Publish(Change{Organization: in.Organization, IncidentID: in.ID, Kind: req.Action})
		}
	}
	if s.Wake != nil {
		s.Wake()
	}
	d, err := ReadDetail(r.Context(), s.Store, c.org, touched[0].ID)
	if err != nil {
		s.storeFailed(w, err)
		return
	}
	write(w, http.StatusOK, d)
}

// checkRequest returns why a request is not an action, or "".
func checkRequest(req ActionRequest) string {
	switch req.Action {
	case ActionAcknowledge, ActionResolve:
	case ActionSetSeverity:
		if req.Severity < store.SeverityHighest || req.Severity > store.SeverityLowest {
			return "set_severity needs a severity from 1 to 4"
		}
	case ActionMerge:
		if !ids.ValidIncident(req.OtherIncidentID) {
			return "merge needs otherIncidentId, an incident identifier"
		}
	case ActionDetach:
		if !ids.Valid(req.SignalID) {
			return "detach needs signalId, a signal identifier"
		}
	default:
		return fmt.Sprintf("action %q is not one of acknowledge, set_severity, merge, detach, resolve", req.Action)
	}
	return ""
}

// apply writes one action in tx and returns the incidents it changed, the acted-on one first.
func (s *Server) apply(ctx context.Context, tx store.Tx, c caller, id string, req ActionRequest) ([]store.Incident, error) {
	in, err := tx.GetIncident(ctx, c.org, id)
	if err != nil {
		return nil, err
	}
	switch {
	case in.ParentID != "":
		return nil, conflict("the incident was merged into " + in.ParentID + "; act on that one")
	case in.State == store.IncidentResolved:
		return nil, conflict("the incident is resolved")
	}
	now := s.now()
	ev := func(target store.Incident, kind string, data map[string]any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = tx.AppendIncidentEvent(ctx, store.IncidentEvent{Organization: target.Organization, IncidentID: target.ID,
			At: now, ActorKind: store.ByPerson, Actor: c.user, Kind: kind, Data: raw})
		return err
	}
	feedback := func(kind string, data map[string]any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_, err = tx.AddFeedback(ctx, store.Feedback{Organization: in.Organization, IncidentID: in.ID, Person: c.user,
			Kind: kind, Data: raw, At: now})
		return err
	}
	touched := []store.Incident{in}
	switch req.Action {
	case ActionAcknowledge:
		err = ev(in, EventAcknowledged, map[string]any{})
	case ActionResolve:
		in.State, in.ResolvedAt = store.IncidentResolved, &now
		if err = tx.UpdateIncident(ctx, in); err == nil {
			err = ev(in, EventResolved, map[string]any{})
		}
	case ActionSetSeverity:
		from := in.Severity
		in.Severity, in.SeveritySetBy = req.Severity, store.ByPerson
		if err = tx.UpdateIncident(ctx, in); err == nil {
			if err = ev(in, EventSeverity, map[string]any{"from": from, "to": req.Severity}); err == nil {
				err = feedback(store.FeedbackSeverity, map[string]any{"from": from, "to": req.Severity})
			}
		}
	case ActionDetach:
		var sg store.Signal
		if sg, err = tx.GetSignal(ctx, in.Organization, req.SignalID); err != nil {
			return nil, err
		}
		if sg.IncidentID != in.ID {
			return nil, conflict("the signal " + sg.ID + " does not belong to the incident")
		}
		sg.IncidentID = ""
		if err = tx.UpdateSignal(ctx, sg); err == nil {
			if err = ev(in, EventDetached, map[string]any{"signals": []string{sg.ID}}); err == nil {
				err = feedback(store.FeedbackDetach, map[string]any{"signalId": sg.ID})
			}
		}
	case ActionMerge:
		var into store.Incident
		if into, err = merge(ctx, tx, in, req.OtherIncidentID, ev); err == nil {
			touched = append(touched, into)
			err = feedback(store.FeedbackMerge, map[string]any{"into": into.ID})
		}
	}
	return touched, err
}

// merge merges in into the incident other, followed to the one it was merged into, and
// returns that incident as it is after the merge.
func merge(ctx context.Context, tx store.Tx, in store.Incident, other string,
	ev func(store.Incident, string, map[string]any) error) (store.Incident, error) {
	into, err := tx.GetIncident(ctx, in.Organization, other)
	for i := 0; err == nil && into.ParentID != ""; i++ {
		if i == maxHops {
			return into, conflict(fmt.Sprintf("the incident %s is merged more than %d times deep", other, maxHops))
		}
		into, err = tx.GetIncident(ctx, in.Organization, into.ParentID)
	}
	switch {
	case err != nil:
		return into, err
	case into.ID == in.ID:
		return into, conflict("an incident cannot be merged into itself")
	case into.State == store.IncidentResolved:
		return into, conflict("the incident " + into.ID + " is resolved")
	}
	moved := []string{}
	for {
		page, err := tx.ListSignals(ctx, store.SignalFilter{Organization: in.Organization, IncidentID: in.ID, Limit: store.MaxLimit})
		if err != nil {
			return into, err
		}
		if len(page) == 0 {
			break
		}
		for _, sg := range page {
			sg.IncidentID = into.ID
			if err := tx.UpdateSignal(ctx, sg); err != nil {
				return into, err
			}
			moved = append(moved, sg.ID)
		}
	}
	in.ParentID = into.ID
	if err := tx.UpdateIncident(ctx, in); err != nil {
		return into, err
	}
	if err := ev(in, EventMerged, map[string]any{"into": into.ID, "signals": moved}); err != nil {
		return into, err
	}
	if err := ev(into, EventAttached, map[string]any{"from": in.ID, "signals": moved}); err != nil {
		return into, err
	}
	if into.SeveritySetBy == store.ByPerson || in.Severity >= into.Severity {
		return into, nil
	}
	from := into.Severity
	into.Severity = in.Severity
	if err := tx.UpdateIncident(ctx, into); err != nil {
		return into, err
	}
	return into, ev(into, EventSeverity, map[string]any{"from": from, "to": into.Severity})
}
