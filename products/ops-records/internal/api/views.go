package api

import (
	"encoding/json"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/store"
)

// The JSON forms of the records, which the console routes and the tool endpoint answer with.

// Incident is the JSON form of an incident.
type Incident struct {
	ID            string     `json:"id"`
	Organization  string     `json:"organization"`
	Title         string     `json:"title"`
	Severity      int        `json:"severity"`
	SeveritySetBy string     `json:"severitySetBy"`
	State         string     `json:"state"`
	OpenedBy      string     `json:"openedBy"`
	OpenedAt      time.Time  `json:"openedAt"`
	ResolvedAt    *time.Time `json:"resolvedAt,omitempty"`
	ParentID      string     `json:"parentId,omitempty"`
	ProblemID     string     `json:"problemId,omitempty"`
}

// Event is the JSON form of a timeline entry.
type Event struct {
	ID        int64           `json:"id"`
	At        time.Time       `json:"at"`
	ActorKind string          `json:"actorKind"`
	Actor     string          `json:"actor"`
	Kind      string          `json:"kind"`
	Data      json.RawMessage `json:"data"`
}

// Signal is the JSON form of a signal.
type Signal struct {
	ID            string          `json:"id"`
	Organization  string          `json:"organization"`
	Source        string          `json:"source"`
	Kind          string          `json:"kind"`
	Entity        string          `json:"entity"`
	Cluster       string          `json:"cluster,omitempty"`
	SeverityFloor int             `json:"severityFloor"`
	Title         string          `json:"title"`
	Body          json.RawMessage `json:"body"`
	State         string          `json:"state"`
	FirstSeen     time.Time       `json:"firstSeen"`
	LastSeen      time.Time       `json:"lastSeen"`
	IncidentID    string          `json:"incidentId,omitempty"`
}

// Problem is the JSON form of a problem.
type Problem struct {
	ID           string    `json:"id"`
	Organization string    `json:"organization"`
	Title        string    `json:"title"`
	Statement    string    `json:"statement"`
	State        string    `json:"state"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Judgement is the JSON form of a judgement.
type Judgement struct {
	ID           string          `json:"id"`
	Organization string          `json:"organization"`
	Judge        string          `json:"judge"`
	SubjectKind  string          `json:"subjectKind"`
	SubjectID    string          `json:"subjectId"`
	Offered      json.RawMessage `json:"offered"`
	RuleResult   json.RawMessage `json:"ruleResult"`
	Answer       json.RawMessage `json:"answer,omitempty"`
	Outcome      string          `json:"outcome"`
	AskedAt      time.Time       `json:"askedAt"`
	DeadlineAt   time.Time       `json:"deadlineAt"`
	AnsweredAt   *time.Time      `json:"answeredAt,omitempty"`
}

// Detail is an incident with its timeline and its signals.
type Detail struct {
	Incident
	Events  []Event  `json:"events"`
	Signals []Signal `json:"signals"`
}

// IncidentOf returns the JSON form of in.
func IncidentOf(in store.Incident) Incident {
	return Incident{ID: in.ID, Organization: in.Organization, Title: in.Title, Severity: in.Severity,
		SeveritySetBy: in.SeveritySetBy, State: in.State, OpenedBy: in.OpenedBy, OpenedAt: in.OpenedAt,
		ResolvedAt: in.ResolvedAt, ParentID: in.ParentID, ProblemID: in.ProblemID}
}

// EventOf returns the JSON form of e.
func EventOf(e store.IncidentEvent) Event {
	return Event{ID: e.ID, At: e.At, ActorKind: e.ActorKind, Actor: e.Actor, Kind: e.Kind, Data: e.Data}
}

// SignalOf returns the JSON form of s. Its entity is "<kind>/<reference>".
func SignalOf(s store.Signal) Signal {
	return Signal{ID: s.ID, Organization: s.Organization, Source: s.Source, Kind: s.Kind,
		Entity: s.EntityKind + "/" + s.EntityRef, Cluster: s.Cluster, SeverityFloor: s.SeverityFloor, Title: s.Title,
		Body: s.Body, State: s.State, FirstSeen: s.FirstSeen, LastSeen: s.LastSeen, IncidentID: s.IncidentID}
}

// ProblemOf returns the JSON form of p.
func ProblemOf(p store.Problem) Problem {
	return Problem{ID: p.ID, Organization: p.Organization, Title: p.Title, Statement: p.Statement, State: p.State,
		CreatedAt: p.CreatedAt}
}

// JudgementOf returns the JSON form of j. Its outcome is "undecided" while it has none.
func JudgementOf(j store.Judgement) Judgement {
	out := Judgement{ID: j.ID, Organization: j.Organization, Judge: j.Judge, SubjectKind: j.SubjectKind,
		SubjectID: j.SubjectID, Offered: j.Offered, RuleResult: j.RuleResult, Answer: j.Answer, Outcome: j.Outcome,
		AskedAt: j.AskedAt, DeadlineAt: j.DeadlineAt, AnsweredAt: j.AnsweredAt}
	if out.Outcome == store.OutcomeUndecided {
		out.Outcome = "undecided"
	}
	return out
}
