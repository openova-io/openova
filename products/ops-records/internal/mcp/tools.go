package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openova-io/openova/products/ops-records/internal/agentguard"
	"github.com/openova-io/openova/products/ops-records/internal/api"
	"github.com/openova-io/openova/products/ops-records/internal/ids"
	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// MaxNote is the longest note add_incident_note writes, in characters.
const MaxNote = 2000

// EventNote is the timeline entry add_incident_note writes.
const EventNote = "note"

// errNotFound is the answer for a record that does not exist in the token's Organization.
var errNotFound = errors.New("not found")

type tool struct {
	name, description, minTier, schema string
	run                                func(ctx context.Context, s *Server, who Identity, args json.RawMessage) (any, error)
}

const (
	idSchema    = `{"type":"string","maxLength":64}`
	limitSchema = `{"type":"integer","minimum":1,"maximum":1000}`
)

var tools = []tool{
	{name: "list_incidents", minTier: TierViewer,
		description: "List the incidents of your Organization, newest first.",
		schema: `{"type":"object","additionalProperties":false,"properties":{"state":{"enum":["open","mitigated","resolved"]},` +
			`"limit":` + limitSchema + `}}`,
		run: listIncidents},
	{name: "get_incident", minTier: TierViewer,
		description: "Read one incident with its signals.",
		schema:      `{"type":"object","additionalProperties":false,"required":["incidentId"],"properties":{"incidentId":` + idSchema + `}}`,
		run:         getIncident},
	{name: "get_incident_timeline", minTier: TierViewer,
		description: "Read the timeline of one incident, oldest entry first.",
		schema:      `{"type":"object","additionalProperties":false,"required":["incidentId"],"properties":{"incidentId":` + idSchema + `}}`,
		run:         getTimeline},
	{name: "list_signals", minTier: TierViewer,
		description: "List the signals of your Organization, oldest first, optionally of one incident or one state.",
		schema: `{"type":"object","additionalProperties":false,"properties":{"incidentId":` + idSchema + `,` +
			`"state":{"enum":["open","cleared"]},"limit":` + limitSchema + `}}`,
		run: listSignals},
	{name: "get_problem", minTier: TierViewer,
		description: "Read one problem record.",
		schema:      `{"type":"object","additionalProperties":false,"required":["problemId"],"properties":{"problemId":` + idSchema + `}}`,
		run:         getProblem},
	{name: "search_problems", minTier: TierViewer,
		description: "Find the problem records whose title or statement contains the query, newest first.",
		schema: `{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","maxLength":200},` +
			`"limit":` + limitSchema + `}}`,
		run: searchProblems},
	{name: "add_incident_note", minTier: TierViewer,
		description: "Add a note to the timeline of one incident. It is a record only and changes nothing else.",
		schema: `{"type":"object","additionalProperties":false,"required":["incidentId","text"],"properties":{"incidentId":` +
			idSchema + `,"text":{"type":"string","minLength":1,"maxLength":2000}}}`,
		run: addNote},
}

// args decodes arguments into v; an unknown field is refused.
func args(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// found turns ErrNotFound into the answer "not found" and hides every other store error.
func found(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return errNotFound
	}
	return errors.New("the records cannot be read now")
}

func checkLimit(n int) error {
	if n < 0 || n > store.MaxLimit {
		return fmt.Errorf("invalid arguments: limit is 1 to %d", store.MaxLimit)
	}
	return nil
}

func listIncidents(ctx context.Context, s *Server, who Identity, raw json.RawMessage) (any, error) {
	var a struct {
		State string `json:"state"`
		Limit int    `json:"limit"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	if err := checkLimit(a.Limit); err != nil {
		return nil, err
	}
	if a.State != "" && a.State != store.IncidentOpen && a.State != store.IncidentMitigated && a.State != store.IncidentResolved {
		return nil, errors.New("invalid arguments: state is open, mitigated or resolved")
	}
	rows, err := s.Store.ListIncidents(ctx, store.IncidentFilter{Organization: who.Organization, State: a.State, Limit: a.Limit})
	if err != nil {
		return nil, found(err)
	}
	items := make([]api.Incident, 0, len(rows))
	for _, in := range rows {
		items = append(items, api.IncidentOf(in))
	}
	return map[string]any{"items": items}, nil
}

type incidentArgs struct {
	IncidentID string `json:"incidentId"`
}

func getIncident(ctx context.Context, s *Server, who Identity, raw json.RawMessage) (any, error) {
	var a incidentArgs
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	d, err := api.ReadDetail(ctx, s.Store, who.Organization, a.IncidentID)
	if err != nil {
		return nil, found(err)
	}
	return map[string]any{"incident": d.Incident, "signals": d.Signals}, nil
}

func getTimeline(ctx context.Context, s *Server, who Identity, raw json.RawMessage) (any, error) {
	var a incidentArgs
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	rows, err := s.Store.ListIncidentEvents(ctx, who.Organization, a.IncidentID)
	if err != nil {
		return nil, found(err)
	}
	events := make([]api.Event, 0, len(rows))
	for _, e := range rows {
		events = append(events, api.EventOf(e))
	}
	return map[string]any{"incidentId": a.IncidentID, "events": events}, nil
}

func listSignals(ctx context.Context, s *Server, who Identity, raw json.RawMessage) (any, error) {
	var a struct {
		IncidentID string `json:"incidentId"`
		State      string `json:"state"`
		Limit      int    `json:"limit"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	if err := checkLimit(a.Limit); err != nil {
		return nil, err
	}
	if a.State != "" && a.State != store.SignalOpen && a.State != store.SignalCleared {
		return nil, errors.New("invalid arguments: state is open or cleared")
	}
	if a.IncidentID != "" {
		if _, err := s.Store.GetIncident(ctx, who.Organization, a.IncidentID); err != nil {
			return nil, found(err)
		}
	}
	rows, err := s.Store.ListSignals(ctx, store.SignalFilter{Organization: who.Organization, IncidentID: a.IncidentID,
		State: a.State, Limit: a.Limit})
	if err != nil {
		return nil, found(err)
	}
	items := make([]api.Signal, 0, len(rows))
	for _, sg := range rows {
		items = append(items, api.SignalOf(sg))
	}
	return map[string]any{"items": items}, nil
}

func getProblem(ctx context.Context, s *Server, who Identity, raw json.RawMessage) (any, error) {
	var a struct {
		ProblemID string `json:"problemId"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	p, err := s.Store.GetProblem(ctx, who.Organization, a.ProblemID)
	if err != nil {
		return nil, found(err)
	}
	return api.ProblemOf(p), nil
}

func searchProblems(ctx context.Context, s *Server, who Identity, raw json.RawMessage) (any, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	if err := checkLimit(a.Limit); err != nil {
		return nil, err
	}
	rows, err := s.Store.SearchProblems(ctx, who.Organization, a.Query, a.Limit)
	if err != nil {
		return nil, found(err)
	}
	items := make([]api.Problem, 0, len(rows))
	for _, p := range rows {
		items = append(items, api.ProblemOf(p))
	}
	return map[string]any{"items": items}, nil
}

// addNote writes one timeline entry "note", by the actor kind agent named by the token, with
// the text redacted.
func addNote(ctx context.Context, s *Server, who Identity, raw json.RawMessage) (any, error) {
	var a struct {
		IncidentID string `json:"incidentId"`
		Text       string `json:"text"`
	}
	if err := args(raw, &a); err != nil {
		return nil, err
	}
	text := strings.TrimSpace(a.Text)
	if text == "" || utf8.RuneCountInString(text) > MaxNote || !ids.ValidIncident(a.IncidentID) {
		return nil, fmt.Errorf("invalid arguments: incidentId is an incident and text has 1 to %d characters", MaxNote)
	}
	data, err := json.Marshal(map[string]string{"text": agentguard.Redact(text)})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	var id int64
	err = s.Store.InTx(ctx, func(ctx context.Context, tx store.Tx) error {
		if _, err := tx.GetIncident(ctx, who.Organization, a.IncidentID); err != nil {
			return err
		}
		var err error
		id, err = tx.AppendIncidentEvent(ctx, store.IncidentEvent{Organization: who.Organization, IncidentID: a.IncidentID,
			At: now, ActorKind: store.ByAgent, Actor: who.Agent, Kind: EventNote, Data: data})
		return err
	})
	if err != nil {
		return nil, found(err)
	}
	if s.Hub != nil {
		s.Hub.Publish(api.Change{Organization: who.Organization, IncidentID: a.IncidentID, Kind: EventNote})
	}
	return map[string]any{"incidentId": a.IncidentID, "eventId": id}, nil
}
