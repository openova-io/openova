// Package store holds the records of the operations-records service and the interface every
// store implements.
//
// The records are seven tables of one database schema, "ops": signals, incidents,
// incident_events, problems, judgements, feedback and outbox. Every row carries the slug of
// its Organization. Two implementations satisfy the interface: the in-memory store of this
// package (memory.go) and a PostgreSQL store. Both must pass the same contract test
// (contract_test.go); the contract test is the definition of the behaviour described here.
//
// Rules every implementation keeps:
//
//   - Organization. Every row has a non-empty Organization. A read whose Organization argument
//     is empty reads every Organization (the sovereign-admin's view); a non-empty one reads
//     that Organization only. A row of another Organization is reported as ErrNotFound.
//     A write names the Organization of the row it writes.
//   - Identifiers. A signal, judgement or outbox row has a ULID; an incident "INC-" and a
//     ULID; a problem "PRB-" and a ULID (package ids). Identifiers are unique across
//     Organizations: inserting an identifier that exists returns ErrConflict. Incident events
//     and feedback get a rising number from the store; the ID given on append is ignored.
//   - References. Signal.IncidentID, Incident.ParentID, IncidentEvent.IncidentID and
//     Feedback.IncidentID name an incident, Incident.ProblemID a problem, of the same
//     Organization. A reference to a row that does not exist there returns ErrNotFound.
//     An empty optional reference means none.
//   - Values. A field with a fixed set of values (the constants below) accepts only those.
//     A row that breaks a rule of Validate is refused with an error that wraps ErrInvalid,
//     and nothing is written.
//   - JSON. A JSON field holds one valid JSON value. An empty required JSON field (Body, Data,
//     Offered, RuleResult, Payload) is stored as the object {}. An empty Answer means null.
//     Compare JSON by meaning: a store may change spacing and key order.
//   - Time. Times are kept to the microsecond and returned in UTC. A required time that is
//     zero is refused. A nil optional time means none.
//   - Copies. A row returned by the store is a copy: changing it changes nothing stored.
//   - Transactions. Every write happens inside InTx. When the function given to InTx returns
//     an error, nothing it wrote is kept. Reads inside the transaction see its writes; reads
//     outside see them only after InTx returned nil. An outbox row is written in the same
//     transaction as the business row it reports (the producer rule of the event contract).
//   - Order. Lists come in the order each method states. Rows with equal times are ordered
//     by identifier in the same direction: oldest first puts the smaller identifier first,
//     newest first puts the greater identifier first.
//     A limit of zero or less means DefaultLimit; a limit above MaxLimit means MaxLimit.
//   - Context. A call with a context that is already done returns the context's error and
//     changes nothing.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
)

// Errors of every store. Compare with errors.Is.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: identifier exists")
	ErrInvalid  = errors.New("store: invalid record")
	ErrTxDone   = errors.New("store: transaction is finished")
)

// Limits of the list methods.
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// Signal sources.
const (
	SourceAlert    = "alert"
	SourceSecurity = "security"
	SourceAccess   = "access"
)

// Signal states.
const (
	SignalOpen    = "open"
	SignalCleared = "cleared"
)

// Incident states.
const (
	IncidentOpen      = "open"
	IncidentMitigated = "mitigated"
	IncidentResolved  = "resolved"
)

// Who set a severity, opened an incident or acted in a timeline.
const (
	ByRules  = "rules"
	ByAgent  = "agent"
	ByPerson = "person"
	BySystem = "system" // timeline actors only
)

// Problem states.
const (
	ProblemOpen   = "open"
	ProblemKnown  = "known"
	ProblemClosed = "closed"
)

// Judgement outcomes. OutcomeUndecided is the outcome of a judgement that waits for its
// answer or its deadline.
const (
	OutcomeUndecided       = ""
	OutcomeApplied         = "applied"
	OutcomeRejectedInvalid = "rejected_invalid"
	OutcomeLateApplied     = "late_applied"
	OutcomeLateRejected    = "late_rejected"
	OutcomeTimeout         = "timeout"
	OutcomeUnavailable     = "unavailable"
)

// Feedback kinds.
const (
	FeedbackMerge      = "merge"
	FeedbackDetach     = "detach"
	FeedbackSeverity   = "severity"
	FeedbackNotRelated = "not_related"
)

// Severities run from 1 (most urgent) to 4.
const (
	SeverityHighest = 1
	SeverityLowest  = 4
)

// Signal is one row of ops.signals: something an alert, a security finding or an access
// event reported about one entity.
type Signal struct {
	ID            string          // ULID
	Organization  string          // slug
	Source        string          // SourceAlert, SourceSecurity or SourceAccess
	Kind          string          // for example "pod-crash-loop"; not empty
	EntityKind    string          // for example "deployment"; not empty
	EntityRef     string          // for example "acme/web"; not empty
	Cluster       string          // may be empty
	SeverityFloor int             // 1..4: no incident of this signal is less urgent
	Title         string          // not empty
	Body          json.RawMessage // the payload as received
	Fingerprint   string          // the same problem reported again has the same fingerprint; not empty
	State         string          // SignalOpen or SignalCleared
	FirstSeen     time.Time
	LastSeen      time.Time // not before FirstSeen
	IncidentID    string    // the incident the signal belongs to; empty for none
}

// Incident is one row of ops.incidents.
type Incident struct {
	ID            string // "INC-" + ULID
	Organization  string
	Title         string // not empty
	Severity      int    // 1..4
	SeveritySetBy string // ByRules, ByAgent or ByPerson
	State         string // IncidentOpen, IncidentMitigated or IncidentResolved
	OpenedBy      string // ByRules or ByAgent
	OpenedAt      time.Time
	ResolvedAt    *time.Time // set exactly when State is IncidentResolved
	ParentID      string     // the incident this one was merged into; empty for none; never itself
	ProblemID     string     // empty for none
}

// IncidentEvent is one entry of an incident's timeline (ops.incident_events). Append only.
type IncidentEvent struct {
	ID           int64 // given by the store
	Organization string
	IncidentID   string
	At           time.Time
	ActorKind    string // ByRules, ByAgent, ByPerson or BySystem
	Actor        string // who: a rule name, an agent name, a person's e-mail; not empty
	Kind         string // for example "opened", "note", "severity"; not empty
	Data         json.RawMessage
}

// Problem is one row of ops.problems.
type Problem struct {
	ID           string // "PRB-" + ULID
	Organization string
	Title        string // not empty
	Statement    string
	State        string // ProblemOpen, ProblemKnown or ProblemClosed
	CreatedAt    time.Time
}

// Judgement is one row of ops.judgements: one question put to a judge, its rule result and
// its answer.
type Judgement struct {
	ID           string // ULID
	Organization string
	Judge        string          // for example "correlator"; not empty
	SubjectKind  string          // for example "signal_group" or "incident"; not empty
	SubjectID    string          // not empty
	Offered      json.RawMessage // the document offered to the judge
	RuleResult   json.RawMessage // what the rule function decided
	Answer       json.RawMessage // the judge's answer; empty for none
	Outcome      string          // one of the Outcome constants
	AskedAt      time.Time
	DeadlineAt   time.Time  // not before AskedAt
	AnsweredAt   *time.Time // set when Answer is set; not before AskedAt
}

// Feedback is one row of ops.feedback: a person's correction of an incident.
type Feedback struct {
	ID           int64 // given by the store
	Organization string
	IncidentID   string
	Person       string // the person's e-mail; not empty
	Kind         string // one of the Feedback constants
	Data         json.RawMessage
	At           time.Time
}

// OutboxRow is one row of ops.outbox: an event waiting to be published.
type OutboxRow struct {
	ID           string // ULID; sent as the message identifier
	Organization string
	Subject      string // catalyst.<domain>.<event>.v<major>
	Payload      json.RawMessage
	CreatedAt    time.Time
	PublishedAt  *time.Time // set by MarkPublished
}

// SignalFilter selects signals. An empty field does not filter.
type SignalFilter struct {
	Organization string
	IncidentID   string
	State        string
	Fingerprint  string
	Limit        int
}

// IncidentFilter selects incidents. An empty field does not filter.
type IncidentFilter struct {
	Organization string
	State        string
	Limit        int
}

// JudgementFilter selects judgements. An empty field does not filter. Undecided selects
// only judgements whose outcome is OutcomeUndecided, whatever Outcome holds.
type JudgementFilter struct {
	Organization string
	Judge        string
	SubjectKind  string
	SubjectID    string
	Outcome      string
	Undecided    bool
	Limit        int
}

// Reader is what can be read, inside and outside a transaction.
type Reader interface {
	GetSignal(ctx context.Context, organization, id string) (Signal, error)
	// ListSignals returns signals by FirstSeen, oldest first.
	ListSignals(ctx context.Context, f SignalFilter) ([]Signal, error)

	GetIncident(ctx context.Context, organization, id string) (Incident, error)
	// ListIncidents returns incidents by OpenedAt, newest first.
	ListIncidents(ctx context.Context, f IncidentFilter) ([]Incident, error)
	// ListIncidentEvents returns the whole timeline of one incident, in the order it was
	// appended. An incident that does not exist returns ErrNotFound.
	ListIncidentEvents(ctx context.Context, organization, incidentID string) ([]IncidentEvent, error)

	GetProblem(ctx context.Context, organization, id string) (Problem, error)
	// SearchProblems returns problems whose Title or Statement contains query, ignoring
	// letter case (an empty query matches every problem), by CreatedAt, newest first.
	SearchProblems(ctx context.Context, organization, query string, limit int) ([]Problem, error)

	GetJudgement(ctx context.Context, organization, id string) (Judgement, error)
	// ListJudgements returns judgements by AskedAt, newest first.
	ListJudgements(ctx context.Context, f JudgementFilter) ([]Judgement, error)

	// ListFeedback returns the feedback on one incident, in the order it was added.
	// An incident that does not exist returns ErrNotFound.
	ListFeedback(ctx context.Context, organization, incidentID string) ([]Feedback, error)
}

// Writer is what can be written. It exists only inside a transaction.
type Writer interface {
	InsertSignal(ctx context.Context, s Signal) error
	// UpdateSignal replaces the signal with the same Organization and ID.
	UpdateSignal(ctx context.Context, s Signal) error

	InsertIncident(ctx context.Context, in Incident) error
	// UpdateIncident replaces the incident with the same Organization and ID.
	UpdateIncident(ctx context.Context, in Incident) error
	// AppendIncidentEvent adds one timeline entry and returns its number.
	AppendIncidentEvent(ctx context.Context, e IncidentEvent) (int64, error)

	InsertProblem(ctx context.Context, p Problem) error
	// UpdateProblem replaces the problem with the same Organization and ID.
	UpdateProblem(ctx context.Context, p Problem) error

	InsertJudgement(ctx context.Context, j Judgement) error
	// UpdateJudgement replaces the judgement with the same Organization and ID.
	UpdateJudgement(ctx context.Context, j Judgement) error

	// AddFeedback adds one correction and returns its number.
	AddFeedback(ctx context.Context, f Feedback) (int64, error)

	// Enqueue writes an outbox row. Its PublishedAt is ignored and stored as none.
	Enqueue(ctx context.Context, o OutboxRow) error
}

// Tx is a transaction: the reads see its own writes.
type Tx interface {
	Reader
	Writer
}

// Store is one database of operations records.
type Store interface {
	Reader

	// InTx runs fn in one transaction. When fn returns nil the writes are kept; when it
	// returns an error, or panics, none is kept and InTx returns that error (or panics
	// again). tx must not be used after fn returned: its methods then return ErrTxDone.
	InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error

	// PendingOutbox returns up to limit outbox rows that are not published, in the order
	// they were enqueued (not the order of their identifiers or times).
	PendingOutbox(ctx context.Context, limit int) ([]OutboxRow, error)
	// MarkPublished records that the outbox row id was published at the time given. A row
	// that is published already keeps its first time. An unknown id returns ErrNotFound.
	MarkPublished(ctx context.Context, id string, at time.Time) error

	// Ping returns nil when the store can serve.
	Ping(ctx context.Context) error
	// Close releases the store. Calls after Close return an error.
	Close() error
}

// subjectPattern is the form of an event subject of the event contract.
var subjectPattern = regexp.MustCompile(`^catalyst\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.v[0-9]+$`)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func severityOK(n int) bool { return n >= SeverityHighest && n <= SeverityLowest }

func jsonOK(name string, raw json.RawMessage) error {
	if len(raw) > 0 && !json.Valid(raw) {
		return invalid("%s is not valid JSON", name)
	}
	return nil
}

func need(fields ...string) error {
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i+1] == "" {
			return invalid("%s is empty", fields[i])
		}
	}
	return nil
}

// ValidateSignal returns an error wrapping ErrInvalid when s breaks a rule of a signal.
func ValidateSignal(s Signal) error {
	if err := need("organization", s.Organization, "kind", s.Kind, "entity kind", s.EntityKind,
		"entity ref", s.EntityRef, "title", s.Title, "fingerprint", s.Fingerprint); err != nil {
		return err
	}
	switch {
	case !ids.Valid(s.ID):
		return invalid("signal id %q is not a ULID", s.ID)
	case !oneOf(s.Source, SourceAlert, SourceSecurity, SourceAccess):
		return invalid("signal source %q", s.Source)
	case !oneOf(s.State, SignalOpen, SignalCleared):
		return invalid("signal state %q", s.State)
	case !severityOK(s.SeverityFloor):
		return invalid("severity floor %d", s.SeverityFloor)
	case s.FirstSeen.IsZero() || s.LastSeen.IsZero():
		return invalid("first seen and last seen are required")
	case s.LastSeen.Before(s.FirstSeen):
		return invalid("last seen is before first seen")
	case s.IncidentID != "" && !ids.ValidIncident(s.IncidentID):
		return invalid("incident id %q", s.IncidentID)
	}
	return jsonOK("body", s.Body)
}

// ValidateIncident returns an error wrapping ErrInvalid when in breaks a rule of an incident.
func ValidateIncident(in Incident) error {
	if err := need("organization", in.Organization, "title", in.Title); err != nil {
		return err
	}
	switch {
	case !ids.ValidIncident(in.ID):
		return invalid("incident id %q", in.ID)
	case !severityOK(in.Severity):
		return invalid("severity %d", in.Severity)
	case !oneOf(in.SeveritySetBy, ByRules, ByAgent, ByPerson):
		return invalid("severity set by %q", in.SeveritySetBy)
	case !oneOf(in.State, IncidentOpen, IncidentMitigated, IncidentResolved):
		return invalid("incident state %q", in.State)
	case !oneOf(in.OpenedBy, ByRules, ByAgent):
		return invalid("opened by %q", in.OpenedBy)
	case in.OpenedAt.IsZero():
		return invalid("opened at is required")
	case (in.State == IncidentResolved) != (in.ResolvedAt != nil):
		return invalid("resolved at is set exactly when the state is resolved")
	case in.ResolvedAt != nil && in.ResolvedAt.Before(in.OpenedAt):
		return invalid("resolved at is before opened at")
	case in.ParentID != "" && !ids.ValidIncident(in.ParentID):
		return invalid("parent id %q", in.ParentID)
	case in.ParentID == in.ID:
		return invalid("an incident cannot be its own parent")
	case in.ProblemID != "" && !ids.ValidProblem(in.ProblemID):
		return invalid("problem id %q", in.ProblemID)
	}
	return nil
}

// ValidateIncidentEvent returns an error wrapping ErrInvalid when e breaks a rule of a
// timeline entry.
func ValidateIncidentEvent(e IncidentEvent) error {
	if err := need("organization", e.Organization, "actor", e.Actor, "kind", e.Kind); err != nil {
		return err
	}
	switch {
	case !ids.ValidIncident(e.IncidentID):
		return invalid("incident id %q", e.IncidentID)
	case !oneOf(e.ActorKind, ByRules, ByAgent, ByPerson, BySystem):
		return invalid("actor kind %q", e.ActorKind)
	case e.At.IsZero():
		return invalid("at is required")
	}
	return jsonOK("data", e.Data)
}

// ValidateProblem returns an error wrapping ErrInvalid when p breaks a rule of a problem.
func ValidateProblem(p Problem) error {
	if err := need("organization", p.Organization, "title", p.Title); err != nil {
		return err
	}
	switch {
	case !ids.ValidProblem(p.ID):
		return invalid("problem id %q", p.ID)
	case !oneOf(p.State, ProblemOpen, ProblemKnown, ProblemClosed):
		return invalid("problem state %q", p.State)
	case p.CreatedAt.IsZero():
		return invalid("created at is required")
	}
	return nil
}

// ValidateJudgement returns an error wrapping ErrInvalid when j breaks a rule of a judgement.
func ValidateJudgement(j Judgement) error {
	if err := need("organization", j.Organization, "judge", j.Judge, "subject kind", j.SubjectKind,
		"subject id", j.SubjectID); err != nil {
		return err
	}
	switch {
	case !ids.Valid(j.ID):
		return invalid("judgement id %q is not a ULID", j.ID)
	case !oneOf(j.Outcome, OutcomeUndecided, OutcomeApplied, OutcomeRejectedInvalid, OutcomeLateApplied,
		OutcomeLateRejected, OutcomeTimeout, OutcomeUnavailable):
		return invalid("outcome %q", j.Outcome)
	case j.AskedAt.IsZero() || j.DeadlineAt.IsZero():
		return invalid("asked at and deadline at are required")
	case j.DeadlineAt.Before(j.AskedAt):
		return invalid("deadline at is before asked at")
	case (len(j.Answer) > 0) != (j.AnsweredAt != nil):
		return invalid("answered at is set exactly when there is an answer")
	case j.AnsweredAt != nil && j.AnsweredAt.Before(j.AskedAt):
		return invalid("answered at is before asked at")
	}
	for _, f := range []struct {
		name string
		raw  json.RawMessage
	}{{"offered", j.Offered}, {"rule result", j.RuleResult}, {"answer", j.Answer}} {
		if err := jsonOK(f.name, f.raw); err != nil {
			return err
		}
	}
	return nil
}

// ValidateFeedback returns an error wrapping ErrInvalid when f breaks a rule of feedback.
func ValidateFeedback(f Feedback) error {
	if err := need("organization", f.Organization, "person", f.Person); err != nil {
		return err
	}
	switch {
	case !ids.ValidIncident(f.IncidentID):
		return invalid("incident id %q", f.IncidentID)
	case !oneOf(f.Kind, FeedbackMerge, FeedbackDetach, FeedbackSeverity, FeedbackNotRelated):
		return invalid("feedback kind %q", f.Kind)
	case f.At.IsZero():
		return invalid("at is required")
	}
	return jsonOK("data", f.Data)
}

// ValidateOutboxRow returns an error wrapping ErrInvalid when o breaks a rule of an outbox row.
func ValidateOutboxRow(o OutboxRow) error {
	if err := need("organization", o.Organization); err != nil {
		return err
	}
	switch {
	case !ids.Valid(o.ID):
		return invalid("outbox id %q is not a ULID", o.ID)
	case !subjectPattern.MatchString(o.Subject):
		return invalid("subject %q is not catalyst.<domain>.<event>.v<major>", o.Subject)
	case o.CreatedAt.IsZero():
		return invalid("created at is required")
	}
	return jsonOK("payload", o.Payload)
}

// clampLimit applies DefaultLimit and MaxLimit.
func clampLimit(n int) int {
	if n <= 0 {
		return DefaultLimit
	}
	if n > MaxLimit {
		return MaxLimit
	}
	return n
}

// orgMatches reports whether a row of organization rowOrg is visible to a read for org.
func orgMatches(org, rowOrg string) bool { return org == "" || org == rowOrg }

// Normal forms: what a store keeps for a value it is given.

func normTime(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

func normTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	n := normTime(*t)
	return &n
}

func normJSON(raw json.RawMessage, required bool) json.RawMessage {
	if len(raw) == 0 {
		if required {
			return json.RawMessage(`{}`)
		}
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func normSignal(s Signal) Signal {
	s.Body = normJSON(s.Body, true)
	s.FirstSeen, s.LastSeen = normTime(s.FirstSeen), normTime(s.LastSeen)
	return s
}

func normIncident(in Incident) Incident {
	in.OpenedAt = normTime(in.OpenedAt)
	in.ResolvedAt = normTimePtr(in.ResolvedAt)
	return in
}

func normIncidentEvent(e IncidentEvent) IncidentEvent {
	e.Data = normJSON(e.Data, true)
	e.At = normTime(e.At)
	return e
}

func normProblem(p Problem) Problem {
	p.CreatedAt = normTime(p.CreatedAt)
	return p
}

func normJudgement(j Judgement) Judgement {
	j.Offered = normJSON(j.Offered, true)
	j.RuleResult = normJSON(j.RuleResult, true)
	j.Answer = normJSON(j.Answer, false)
	j.AskedAt, j.DeadlineAt = normTime(j.AskedAt), normTime(j.DeadlineAt)
	j.AnsweredAt = normTimePtr(j.AnsweredAt)
	return j
}

func normFeedback(f Feedback) Feedback {
	f.Data = normJSON(f.Data, true)
	f.At = normTime(f.At)
	return f
}

func normOutboxRow(o OutboxRow) OutboxRow {
	o.Payload = normJSON(o.Payload, true)
	o.CreatedAt = normTime(o.CreatedAt)
	o.PublishedAt = normTimePtr(o.PublishedAt)
	return o
}
