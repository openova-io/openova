package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// Memory is the in-memory store. It keeps the rules of the package documentation and is
// used by the tests of every package of the service. It keeps nothing after the process
// ends.
//
// A transaction holds the write lock for its whole length and works on a copy of the data;
// the copy replaces the data when the transaction's function returns nil.
type Memory struct {
	mu     sync.RWMutex
	data   *memData
	closed bool
}

type memData struct {
	signals    map[string]Signal
	incidents  map[string]Incident
	events     []IncidentEvent // in the order appended
	problems   map[string]Problem
	judgements map[string]Judgement
	feedback   []Feedback  // in the order added
	outbox     []OutboxRow // in the order enqueued
	nextEvent  int64
	nextFeed   int64
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{data: &memData{
		signals:    map[string]Signal{},
		incidents:  map[string]Incident{},
		problems:   map[string]Problem{},
		judgements: map[string]Judgement{},
	}}
}

var errClosed = errors.New("store: closed")

func (d *memData) clone() *memData {
	c := &memData{
		signals:    make(map[string]Signal, len(d.signals)),
		incidents:  make(map[string]Incident, len(d.incidents)),
		events:     make([]IncidentEvent, len(d.events)),
		problems:   make(map[string]Problem, len(d.problems)),
		judgements: make(map[string]Judgement, len(d.judgements)),
		feedback:   make([]Feedback, len(d.feedback)),
		outbox:     make([]OutboxRow, len(d.outbox)),
		nextEvent:  d.nextEvent,
		nextFeed:   d.nextFeed,
	}
	// Rows are stored in normal form, and a transaction replaces a row instead of changing
	// it, so a shallow copy of each row is safe. MarkPublished changes a row in place, but
	// only under the write lock, when no transaction holds a copy.
	for k, v := range d.signals {
		c.signals[k] = v
	}
	for k, v := range d.incidents {
		c.incidents[k] = v
	}
	for k, v := range d.problems {
		c.problems[k] = v
	}
	for k, v := range d.judgements {
		c.judgements[k] = v
	}
	copy(c.events, d.events)
	copy(c.feedback, d.feedback)
	copy(c.outbox, d.outbox)
	return c
}

// read runs fn on the committed data under the read lock.
func (m *Memory) read(ctx context.Context, fn func(d *memData) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return errClosed
	}
	return fn(m.data)
}

// InTx runs fn in one transaction.
func (m *Memory) InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errClosed
	}
	tx := &memTx{d: m.data.clone()}
	defer func() { tx.done = true }()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.data = tx.d
	return nil
}

// PendingOutbox returns unpublished outbox rows in the order they were enqueued.
func (m *Memory) PendingOutbox(ctx context.Context, limit int) ([]OutboxRow, error) {
	var out []OutboxRow
	err := m.read(ctx, func(d *memData) error {
		n := clampLimit(limit)
		for _, o := range d.outbox {
			if o.PublishedAt == nil {
				out = append(out, normOutboxRow(o))
				if len(out) == n {
					break
				}
			}
		}
		return nil
	})
	return out, err
}

// MarkPublished records the publication of one outbox row.
func (m *Memory) MarkPublished(ctx context.Context, id string, at time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if at.IsZero() {
		return invalid("published at is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return errClosed
	}
	for i := range m.data.outbox {
		if m.data.outbox[i].ID != id {
			continue
		}
		if m.data.outbox[i].PublishedAt == nil {
			t := normTime(at)
			m.data.outbox[i].PublishedAt = &t
		}
		return nil
	}
	return ErrNotFound
}

// Ping returns nil while the store is open.
func (m *Memory) Ping(ctx context.Context) error {
	return m.read(ctx, func(*memData) error { return nil })
}

// Close closes the store.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

// Reads outside a transaction.

func (m *Memory) GetSignal(ctx context.Context, org, id string) (s Signal, err error) {
	err = m.read(ctx, func(d *memData) error { s, err = d.getSignal(org, id); return err })
	return s, err
}

func (m *Memory) ListSignals(ctx context.Context, f SignalFilter) (out []Signal, err error) {
	err = m.read(ctx, func(d *memData) error { out = d.listSignals(f); return nil })
	return out, err
}

func (m *Memory) GetIncident(ctx context.Context, org, id string) (in Incident, err error) {
	err = m.read(ctx, func(d *memData) error { in, err = d.getIncident(org, id); return err })
	return in, err
}

func (m *Memory) ListIncidents(ctx context.Context, f IncidentFilter) (out []Incident, err error) {
	err = m.read(ctx, func(d *memData) error { out = d.listIncidents(f); return nil })
	return out, err
}

func (m *Memory) ListIncidentEvents(ctx context.Context, org, incidentID string) (out []IncidentEvent, err error) {
	err = m.read(ctx, func(d *memData) error { out, err = d.listEvents(org, incidentID); return err })
	return out, err
}

func (m *Memory) GetProblem(ctx context.Context, org, id string) (p Problem, err error) {
	err = m.read(ctx, func(d *memData) error { p, err = d.getProblem(org, id); return err })
	return p, err
}

func (m *Memory) SearchProblems(ctx context.Context, org, query string, limit int) (out []Problem, err error) {
	err = m.read(ctx, func(d *memData) error { out = d.searchProblems(org, query, limit); return nil })
	return out, err
}

func (m *Memory) GetJudgement(ctx context.Context, org, id string) (j Judgement, err error) {
	err = m.read(ctx, func(d *memData) error { j, err = d.getJudgement(org, id); return err })
	return j, err
}

func (m *Memory) ListJudgements(ctx context.Context, f JudgementFilter) (out []Judgement, err error) {
	err = m.read(ctx, func(d *memData) error { out = d.listJudgements(f); return nil })
	return out, err
}

func (m *Memory) ListFeedback(ctx context.Context, org, incidentID string) (out []Feedback, err error) {
	err = m.read(ctx, func(d *memData) error { out, err = d.listFeedback(org, incidentID); return err })
	return out, err
}

// memTx is a transaction of the in-memory store.
type memTx struct {
	d    *memData
	done bool
}

func (t *memTx) check(ctx context.Context) error {
	if t.done {
		return ErrTxDone
	}
	return ctx.Err()
}

func (t *memTx) GetSignal(ctx context.Context, org, id string) (Signal, error) {
	if err := t.check(ctx); err != nil {
		return Signal{}, err
	}
	return t.d.getSignal(org, id)
}

func (t *memTx) ListSignals(ctx context.Context, f SignalFilter) ([]Signal, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return t.d.listSignals(f), nil
}

func (t *memTx) GetIncident(ctx context.Context, org, id string) (Incident, error) {
	if err := t.check(ctx); err != nil {
		return Incident{}, err
	}
	return t.d.getIncident(org, id)
}

func (t *memTx) ListIncidents(ctx context.Context, f IncidentFilter) ([]Incident, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return t.d.listIncidents(f), nil
}

func (t *memTx) ListIncidentEvents(ctx context.Context, org, incidentID string) ([]IncidentEvent, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return t.d.listEvents(org, incidentID)
}

func (t *memTx) GetProblem(ctx context.Context, org, id string) (Problem, error) {
	if err := t.check(ctx); err != nil {
		return Problem{}, err
	}
	return t.d.getProblem(org, id)
}

func (t *memTx) SearchProblems(ctx context.Context, org, query string, limit int) ([]Problem, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return t.d.searchProblems(org, query, limit), nil
}

func (t *memTx) GetJudgement(ctx context.Context, org, id string) (Judgement, error) {
	if err := t.check(ctx); err != nil {
		return Judgement{}, err
	}
	return t.d.getJudgement(org, id)
}

func (t *memTx) ListJudgements(ctx context.Context, f JudgementFilter) ([]Judgement, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return t.d.listJudgements(f), nil
}

func (t *memTx) ListFeedback(ctx context.Context, org, incidentID string) ([]Feedback, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return t.d.listFeedback(org, incidentID)
}

func (t *memTx) InsertSignal(ctx context.Context, s Signal) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateSignal(s); err != nil {
		return err
	}
	if _, ok := t.d.signals[s.ID]; ok {
		return ErrConflict
	}
	if err := t.d.incidentRef(s.Organization, s.IncidentID); err != nil {
		return err
	}
	t.d.signals[s.ID] = normSignal(s)
	return nil
}

func (t *memTx) UpdateSignal(ctx context.Context, s Signal) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateSignal(s); err != nil {
		return err
	}
	if _, err := t.d.getSignal(s.Organization, s.ID); err != nil {
		return err
	}
	if err := t.d.incidentRef(s.Organization, s.IncidentID); err != nil {
		return err
	}
	t.d.signals[s.ID] = normSignal(s)
	return nil
}

func (t *memTx) InsertIncident(ctx context.Context, in Incident) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateIncident(in); err != nil {
		return err
	}
	if _, ok := t.d.incidents[in.ID]; ok {
		return ErrConflict
	}
	if err := t.d.incidentRefs(in); err != nil {
		return err
	}
	t.d.incidents[in.ID] = normIncident(in)
	return nil
}

func (t *memTx) UpdateIncident(ctx context.Context, in Incident) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateIncident(in); err != nil {
		return err
	}
	if _, err := t.d.getIncident(in.Organization, in.ID); err != nil {
		return err
	}
	if err := t.d.incidentRefs(in); err != nil {
		return err
	}
	t.d.incidents[in.ID] = normIncident(in)
	return nil
}

func (t *memTx) AppendIncidentEvent(ctx context.Context, e IncidentEvent) (int64, error) {
	if err := t.check(ctx); err != nil {
		return 0, err
	}
	if err := ValidateIncidentEvent(e); err != nil {
		return 0, err
	}
	if err := t.d.incidentRef(e.Organization, e.IncidentID); err != nil {
		return 0, err
	}
	t.d.nextEvent++
	e.ID = t.d.nextEvent
	t.d.events = append(t.d.events, normIncidentEvent(e))
	return e.ID, nil
}

func (t *memTx) InsertProblem(ctx context.Context, p Problem) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateProblem(p); err != nil {
		return err
	}
	if _, ok := t.d.problems[p.ID]; ok {
		return ErrConflict
	}
	t.d.problems[p.ID] = normProblem(p)
	return nil
}

func (t *memTx) UpdateProblem(ctx context.Context, p Problem) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateProblem(p); err != nil {
		return err
	}
	if _, err := t.d.getProblem(p.Organization, p.ID); err != nil {
		return err
	}
	t.d.problems[p.ID] = normProblem(p)
	return nil
}

func (t *memTx) InsertJudgement(ctx context.Context, j Judgement) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateJudgement(j); err != nil {
		return err
	}
	if _, ok := t.d.judgements[j.ID]; ok {
		return ErrConflict
	}
	t.d.judgements[j.ID] = normJudgement(j)
	return nil
}

func (t *memTx) UpdateJudgement(ctx context.Context, j Judgement) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateJudgement(j); err != nil {
		return err
	}
	if _, err := t.d.getJudgement(j.Organization, j.ID); err != nil {
		return err
	}
	t.d.judgements[j.ID] = normJudgement(j)
	return nil
}

func (t *memTx) AddFeedback(ctx context.Context, f Feedback) (int64, error) {
	if err := t.check(ctx); err != nil {
		return 0, err
	}
	if err := ValidateFeedback(f); err != nil {
		return 0, err
	}
	if err := t.d.incidentRef(f.Organization, f.IncidentID); err != nil {
		return 0, err
	}
	t.d.nextFeed++
	f.ID = t.d.nextFeed
	t.d.feedback = append(t.d.feedback, normFeedback(f))
	return f.ID, nil
}

func (t *memTx) Enqueue(ctx context.Context, o OutboxRow) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateOutboxRow(o); err != nil {
		return err
	}
	for _, have := range t.d.outbox {
		if have.ID == o.ID {
			return ErrConflict
		}
	}
	o.PublishedAt = nil
	t.d.outbox = append(t.d.outbox, normOutboxRow(o))
	return nil
}

// Helpers on the data. They return copies in normal form.

func (d *memData) incidentRef(org, id string) error {
	if id == "" {
		return nil
	}
	if in, ok := d.incidents[id]; !ok || in.Organization != org {
		return ErrNotFound
	}
	return nil
}

func (d *memData) incidentRefs(in Incident) error {
	if err := d.incidentRef(in.Organization, in.ParentID); err != nil {
		return err
	}
	if in.ProblemID != "" {
		if p, ok := d.problems[in.ProblemID]; !ok || p.Organization != in.Organization {
			return ErrNotFound
		}
	}
	return nil
}

func (d *memData) getSignal(org, id string) (Signal, error) {
	s, ok := d.signals[id]
	if !ok || !orgMatches(org, s.Organization) {
		return Signal{}, ErrNotFound
	}
	return normSignal(s), nil
}

func (d *memData) listSignals(f SignalFilter) []Signal {
	var out []Signal
	for _, s := range d.signals {
		if orgMatches(f.Organization, s.Organization) &&
			(f.IncidentID == "" || s.IncidentID == f.IncidentID) &&
			(f.State == "" || s.State == f.State) &&
			(f.Fingerprint == "" || s.Fingerprint == f.Fingerprint) {
			out = append(out, normSignal(s))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.Before(out[j].FirstSeen)
		}
		return out[i].ID < out[j].ID
	})
	return limited(out, f.Limit)
}

func (d *memData) getIncident(org, id string) (Incident, error) {
	in, ok := d.incidents[id]
	if !ok || !orgMatches(org, in.Organization) {
		return Incident{}, ErrNotFound
	}
	return normIncident(in), nil
}

func (d *memData) listIncidents(f IncidentFilter) []Incident {
	var out []Incident
	for _, in := range d.incidents {
		if orgMatches(f.Organization, in.Organization) && (f.State == "" || in.State == f.State) {
			out = append(out, normIncident(in))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OpenedAt.Equal(out[j].OpenedAt) {
			return out[i].OpenedAt.After(out[j].OpenedAt)
		}
		return out[i].ID > out[j].ID
	})
	return limited(out, f.Limit)
}

func (d *memData) listEvents(org, incidentID string) ([]IncidentEvent, error) {
	if _, err := d.getIncident(org, incidentID); err != nil {
		return nil, err
	}
	var out []IncidentEvent
	for _, e := range d.events {
		if e.IncidentID == incidentID {
			out = append(out, normIncidentEvent(e))
		}
	}
	return out, nil
}

func (d *memData) getProblem(org, id string) (Problem, error) {
	p, ok := d.problems[id]
	if !ok || !orgMatches(org, p.Organization) {
		return Problem{}, ErrNotFound
	}
	return normProblem(p), nil
}

func (d *memData) searchProblems(org, query string, limit int) []Problem {
	q := strings.ToLower(query)
	var out []Problem
	for _, p := range d.problems {
		if orgMatches(org, p.Organization) &&
			(strings.Contains(strings.ToLower(p.Title), q) || strings.Contains(strings.ToLower(p.Statement), q)) {
			out = append(out, normProblem(p))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return limited(out, limit)
}

func (d *memData) getJudgement(org, id string) (Judgement, error) {
	j, ok := d.judgements[id]
	if !ok || !orgMatches(org, j.Organization) {
		return Judgement{}, ErrNotFound
	}
	return normJudgement(j), nil
}

func (d *memData) listJudgements(f JudgementFilter) []Judgement {
	var out []Judgement
	for _, j := range d.judgements {
		if !orgMatches(f.Organization, j.Organization) ||
			(f.Judge != "" && j.Judge != f.Judge) ||
			(f.SubjectKind != "" && j.SubjectKind != f.SubjectKind) ||
			(f.SubjectID != "" && j.SubjectID != f.SubjectID) {
			continue
		}
		if f.Undecided {
			if j.Outcome != OutcomeUndecided {
				continue
			}
		} else if f.Outcome != "" && j.Outcome != f.Outcome {
			continue
		}
		out = append(out, normJudgement(j))
	}
	sort.Slice(out, func(i, k int) bool {
		if !out[i].AskedAt.Equal(out[k].AskedAt) {
			return out[i].AskedAt.After(out[k].AskedAt)
		}
		return out[i].ID > out[k].ID
	})
	return limited(out, f.Limit)
}

func (d *memData) listFeedback(org, incidentID string) ([]Feedback, error) {
	if _, err := d.getIncident(org, incidentID); err != nil {
		return nil, err
	}
	var out []Feedback
	for _, f := range d.feedback {
		if f.IncidentID == incidentID {
			out = append(out, normFeedback(f))
		}
	}
	return out, nil
}

func limited[T any](rows []T, limit int) []T {
	if n := clampLimit(limit); len(rows) > n {
		return rows[:n]
	}
	return rows
}

// Memory implements Store.
var _ Store = (*Memory)(nil)
