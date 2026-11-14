package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the PostgreSQL store. It keeps the rules of the package documentation on the
// seven tables of schema ops, which migration 0001 creates (migrate.go). It passes the same
// contract test as the in-memory store (postgres_test.go).
//
// A write that the rules refuse (ErrInvalid, ErrNotFound, ErrConflict) is refused before any
// statement can fail, so the transaction stays usable, as it does in the in-memory store.
type Postgres struct {
	pool   *pgxpool.Pool
	closed atomic.Bool
}

// PostgresConfig says how to reach the database.
type PostgresConfig struct {
	// URL is the database as a postgres:// URL or a key=value string.
	URL string
	// Credentials, when set, is called for every new connection and returns the user name
	// and the password of that connection. They replace any user and password of URL.
	Credentials func(ctx context.Context) (user, password string, err error)
	// MaxConns is the largest number of open connections; zero or less keeps the default
	// of the driver.
	MaxConns int32
}

// OpenPostgres returns a store on the database of cfg. It does not connect: the first call
// that needs the database does, and Ping reports whether it can.
func OpenPostgres(ctx context.Context, cfg PostgresConfig) (*Postgres, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("store: the database URL cannot be read: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	if cfg.Credentials != nil {
		credentials := cfg.Credentials
		pc.BeforeConnect = func(ctx context.Context, cc *pgx.ConnConfig) error {
			user, password, err := credentials(ctx)
			if err != nil {
				return fmt.Errorf("store: database credentials: %w", err)
			}
			cc.User, cc.Password = user, password
			return nil
		}
	}
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

// querier is what a pool and a transaction share.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (p *Postgres) ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.closed.Load() {
		return errClosed
	}
	return nil
}

// InTx runs fn in one transaction.
func (p *Postgres) InTx(ctx context.Context, fn func(ctx context.Context, tx Tx) error) error {
	if err := p.ready(ctx); err != nil {
		return err
	}
	ptx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	tx := &pgTx{tx: ptx}
	committed := false
	defer func() {
		tx.done.Store(true)
		if !committed {
			_ = ptx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ptx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	committed = true
	return nil
}

// PendingOutbox returns unpublished outbox rows in the order they were enqueued.
func (p *Postgres) PendingOutbox(ctx context.Context, limit int) ([]OutboxRow, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	rows, err := p.pool.Query(ctx, `SELECT id, organization, subject, payload, created_at, published_at
		FROM ops.outbox WHERE published_at IS NULL ORDER BY seq LIMIT $1`, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("store: pending outbox: %w", err)
	}
	return collect(rows, func(r pgx.Rows) (OutboxRow, error) {
		var o OutboxRow
		var payload []byte
		err := r.Scan(&o.ID, &o.Organization, &o.Subject, &payload, &o.CreatedAt, &o.PublishedAt)
		o.Payload = payload
		return normOutboxRow(o), err
	})
}

// MarkPublished records the publication of one outbox row.
func (p *Postgres) MarkPublished(ctx context.Context, id string, at time.Time) error {
	if err := p.ready(ctx); err != nil {
		return err
	}
	if at.IsZero() {
		return invalid("published at is required")
	}
	tag, err := p.pool.Exec(ctx, `UPDATE ops.outbox SET published_at = coalesce(published_at, $2) WHERE id = $1`,
		id, normTime(at))
	if err != nil {
		return fmt.Errorf("store: mark published: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Ping returns nil when the database answers.
func (p *Postgres) Ping(ctx context.Context) error {
	if err := p.ready(ctx); err != nil {
		return err
	}
	return p.pool.Ping(ctx)
}

// Close closes every connection. Calls after Close return an error.
func (p *Postgres) Close() error {
	if p.closed.CompareAndSwap(false, true) {
		p.pool.Close()
	}
	return nil
}

// Reads outside a transaction.

func (p *Postgres) GetSignal(ctx context.Context, org, id string) (Signal, error) {
	if err := p.ready(ctx); err != nil {
		return Signal{}, err
	}
	return getSignal(ctx, p.pool, org, id)
}

func (p *Postgres) ListSignals(ctx context.Context, f SignalFilter) ([]Signal, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	return listSignals(ctx, p.pool, f)
}

func (p *Postgres) GetIncident(ctx context.Context, org, id string) (Incident, error) {
	if err := p.ready(ctx); err != nil {
		return Incident{}, err
	}
	return getIncident(ctx, p.pool, org, id)
}

func (p *Postgres) ListIncidents(ctx context.Context, f IncidentFilter) ([]Incident, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	return listIncidents(ctx, p.pool, f)
}

func (p *Postgres) ListIncidentEvents(ctx context.Context, org, incidentID string) ([]IncidentEvent, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	return listEvents(ctx, p.pool, org, incidentID)
}

func (p *Postgres) GetProblem(ctx context.Context, org, id string) (Problem, error) {
	if err := p.ready(ctx); err != nil {
		return Problem{}, err
	}
	return getProblem(ctx, p.pool, org, id)
}

func (p *Postgres) SearchProblems(ctx context.Context, org, query string, limit int) ([]Problem, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	return searchProblems(ctx, p.pool, org, query, limit)
}

func (p *Postgres) GetJudgement(ctx context.Context, org, id string) (Judgement, error) {
	if err := p.ready(ctx); err != nil {
		return Judgement{}, err
	}
	return getJudgement(ctx, p.pool, org, id)
}

func (p *Postgres) ListJudgements(ctx context.Context, f JudgementFilter) ([]Judgement, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	return listJudgements(ctx, p.pool, f)
}

func (p *Postgres) ListFeedback(ctx context.Context, org, incidentID string) ([]Feedback, error) {
	if err := p.ready(ctx); err != nil {
		return nil, err
	}
	return listFeedback(ctx, p.pool, org, incidentID)
}

// pgTx is a transaction of the PostgreSQL store.
type pgTx struct {
	tx   pgx.Tx
	done atomic.Bool
}

func (t *pgTx) check(ctx context.Context) error {
	if t.done.Load() {
		return ErrTxDone
	}
	return ctx.Err()
}

func (t *pgTx) GetSignal(ctx context.Context, org, id string) (Signal, error) {
	if err := t.check(ctx); err != nil {
		return Signal{}, err
	}
	return getSignal(ctx, t.tx, org, id)
}

func (t *pgTx) ListSignals(ctx context.Context, f SignalFilter) ([]Signal, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return listSignals(ctx, t.tx, f)
}

func (t *pgTx) GetIncident(ctx context.Context, org, id string) (Incident, error) {
	if err := t.check(ctx); err != nil {
		return Incident{}, err
	}
	return getIncident(ctx, t.tx, org, id)
}

func (t *pgTx) ListIncidents(ctx context.Context, f IncidentFilter) ([]Incident, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return listIncidents(ctx, t.tx, f)
}

func (t *pgTx) ListIncidentEvents(ctx context.Context, org, incidentID string) ([]IncidentEvent, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return listEvents(ctx, t.tx, org, incidentID)
}

func (t *pgTx) GetProblem(ctx context.Context, org, id string) (Problem, error) {
	if err := t.check(ctx); err != nil {
		return Problem{}, err
	}
	return getProblem(ctx, t.tx, org, id)
}

func (t *pgTx) SearchProblems(ctx context.Context, org, query string, limit int) ([]Problem, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return searchProblems(ctx, t.tx, org, query, limit)
}

func (t *pgTx) GetJudgement(ctx context.Context, org, id string) (Judgement, error) {
	if err := t.check(ctx); err != nil {
		return Judgement{}, err
	}
	return getJudgement(ctx, t.tx, org, id)
}

func (t *pgTx) ListJudgements(ctx context.Context, f JudgementFilter) ([]Judgement, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return listJudgements(ctx, t.tx, f)
}

func (t *pgTx) ListFeedback(ctx context.Context, org, incidentID string) ([]Feedback, error) {
	if err := t.check(ctx); err != nil {
		return nil, err
	}
	return listFeedback(ctx, t.tx, org, incidentID)
}

func (t *pgTx) InsertSignal(ctx context.Context, s Signal) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateSignal(s); err != nil {
		return err
	}
	if err := refIncident(ctx, t.tx, s.Organization, s.IncidentID); err != nil {
		return err
	}
	s = normSignal(s)
	tag, err := t.tx.Exec(ctx, `INSERT INTO ops.signals (id, organization, source, kind, entity_kind, entity_ref,
		cluster, severity_floor, title, body, fingerprint, state, first_seen, last_seen, incident_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15) ON CONFLICT (id) DO NOTHING`,
		s.ID, s.Organization, s.Source, s.Kind, s.EntityKind, s.EntityRef, s.Cluster, s.SeverityFloor, s.Title,
		jsonArg(s.Body), s.Fingerprint, s.State, s.FirstSeen, s.LastSeen, nullable(s.IncidentID))
	return inserted("signal", tag, err)
}

func (t *pgTx) UpdateSignal(ctx context.Context, s Signal) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateSignal(s); err != nil {
		return err
	}
	if err := refIncident(ctx, t.tx, s.Organization, s.IncidentID); err != nil {
		return err
	}
	s = normSignal(s)
	tag, err := t.tx.Exec(ctx, `UPDATE ops.signals SET source = $3, kind = $4, entity_kind = $5, entity_ref = $6,
		cluster = $7, severity_floor = $8, title = $9, body = $10, fingerprint = $11, state = $12, first_seen = $13,
		last_seen = $14, incident_id = $15 WHERE id = $1 AND organization = $2`,
		s.ID, s.Organization, s.Source, s.Kind, s.EntityKind, s.EntityRef, s.Cluster, s.SeverityFloor, s.Title,
		jsonArg(s.Body), s.Fingerprint, s.State, s.FirstSeen, s.LastSeen, nullable(s.IncidentID))
	return updated("signal", tag, err)
}

func (t *pgTx) InsertIncident(ctx context.Context, in Incident) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateIncident(in); err != nil {
		return err
	}
	if err := refIncidentRefs(ctx, t.tx, in); err != nil {
		return err
	}
	in = normIncident(in)
	tag, err := t.tx.Exec(ctx, `INSERT INTO ops.incidents (id, organization, title, severity, severity_set_by,
		state, opened_by, opened_at, resolved_at, parent_id, problem_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) ON CONFLICT (id) DO NOTHING`,
		in.ID, in.Organization, in.Title, in.Severity, in.SeveritySetBy, in.State, in.OpenedBy, in.OpenedAt,
		in.ResolvedAt, nullable(in.ParentID), nullable(in.ProblemID))
	return inserted("incident", tag, err)
}

func (t *pgTx) UpdateIncident(ctx context.Context, in Incident) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateIncident(in); err != nil {
		return err
	}
	if err := refIncidentRefs(ctx, t.tx, in); err != nil {
		return err
	}
	in = normIncident(in)
	tag, err := t.tx.Exec(ctx, `UPDATE ops.incidents SET title = $3, severity = $4, severity_set_by = $5,
		state = $6, opened_by = $7, opened_at = $8, resolved_at = $9, parent_id = $10, problem_id = $11
		WHERE id = $1 AND organization = $2`,
		in.ID, in.Organization, in.Title, in.Severity, in.SeveritySetBy, in.State, in.OpenedBy, in.OpenedAt,
		in.ResolvedAt, nullable(in.ParentID), nullable(in.ProblemID))
	return updated("incident", tag, err)
}

func (t *pgTx) AppendIncidentEvent(ctx context.Context, e IncidentEvent) (int64, error) {
	if err := t.check(ctx); err != nil {
		return 0, err
	}
	if err := ValidateIncidentEvent(e); err != nil {
		return 0, err
	}
	if err := refIncident(ctx, t.tx, e.Organization, e.IncidentID); err != nil {
		return 0, err
	}
	e = normIncidentEvent(e)
	var id int64
	err := t.tx.QueryRow(ctx, `INSERT INTO ops.incident_events (organization, incident_id, at, actor_kind, actor,
		kind, data) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		e.Organization, e.IncidentID, e.At, e.ActorKind, e.Actor, e.Kind, jsonArg(e.Data)).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: append incident event: %w", err)
	}
	return id, nil
}

func (t *pgTx) InsertProblem(ctx context.Context, p Problem) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateProblem(p); err != nil {
		return err
	}
	p = normProblem(p)
	tag, err := t.tx.Exec(ctx, `INSERT INTO ops.problems (id, organization, title, statement, state, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		p.ID, p.Organization, p.Title, p.Statement, p.State, p.CreatedAt)
	return inserted("problem", tag, err)
}

func (t *pgTx) UpdateProblem(ctx context.Context, p Problem) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateProblem(p); err != nil {
		return err
	}
	p = normProblem(p)
	tag, err := t.tx.Exec(ctx, `UPDATE ops.problems SET title = $3, statement = $4, state = $5, created_at = $6
		WHERE id = $1 AND organization = $2`,
		p.ID, p.Organization, p.Title, p.Statement, p.State, p.CreatedAt)
	return updated("problem", tag, err)
}

func (t *pgTx) InsertJudgement(ctx context.Context, j Judgement) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateJudgement(j); err != nil {
		return err
	}
	j = normJudgement(j)
	tag, err := t.tx.Exec(ctx, `INSERT INTO ops.judgements (id, organization, judge, subject_kind, subject_id,
		offered, rule_result, answer, outcome, asked_at, deadline_at, answered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) ON CONFLICT (id) DO NOTHING`,
		j.ID, j.Organization, j.Judge, j.SubjectKind, j.SubjectID, jsonArg(j.Offered), jsonArg(j.RuleResult),
		jsonArg(j.Answer), j.Outcome, j.AskedAt, j.DeadlineAt, j.AnsweredAt)
	return inserted("judgement", tag, err)
}

func (t *pgTx) UpdateJudgement(ctx context.Context, j Judgement) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateJudgement(j); err != nil {
		return err
	}
	j = normJudgement(j)
	tag, err := t.tx.Exec(ctx, `UPDATE ops.judgements SET judge = $3, subject_kind = $4, subject_id = $5,
		offered = $6, rule_result = $7, answer = $8, outcome = $9, asked_at = $10, deadline_at = $11,
		answered_at = $12 WHERE id = $1 AND organization = $2`,
		j.ID, j.Organization, j.Judge, j.SubjectKind, j.SubjectID, jsonArg(j.Offered), jsonArg(j.RuleResult),
		jsonArg(j.Answer), j.Outcome, j.AskedAt, j.DeadlineAt, j.AnsweredAt)
	return updated("judgement", tag, err)
}

func (t *pgTx) AddFeedback(ctx context.Context, f Feedback) (int64, error) {
	if err := t.check(ctx); err != nil {
		return 0, err
	}
	if err := ValidateFeedback(f); err != nil {
		return 0, err
	}
	if err := refIncident(ctx, t.tx, f.Organization, f.IncidentID); err != nil {
		return 0, err
	}
	f = normFeedback(f)
	var id int64
	err := t.tx.QueryRow(ctx, `INSERT INTO ops.feedback (organization, incident_id, person, kind, data, at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		f.Organization, f.IncidentID, f.Person, f.Kind, jsonArg(f.Data), f.At).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("store: add feedback: %w", err)
	}
	return id, nil
}

func (t *pgTx) Enqueue(ctx context.Context, o OutboxRow) error {
	if err := t.check(ctx); err != nil {
		return err
	}
	if err := ValidateOutboxRow(o); err != nil {
		return err
	}
	o.PublishedAt = nil
	o = normOutboxRow(o)
	tag, err := t.tx.Exec(ctx, `INSERT INTO ops.outbox (id, organization, subject, payload, created_at)
		VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
		o.ID, o.Organization, o.Subject, jsonArg(o.Payload), o.CreatedAt)
	return inserted("outbox row", tag, err)
}

// Helpers. Each read returns rows in normal form; the driver gives every row its own memory,
// so a returned row is a copy.

// jsonArg is the argument of a JSON column: SQL NULL for an empty value.
func jsonArg(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

// nullable is the argument of an optional reference column: SQL NULL for an empty value.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func inserted(what string, tag pgconn.CommandTag, err error) error {
	if err != nil {
		return fmt.Errorf("store: insert %s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func updated(what string, tag pgconn.CommandTag, err error) error {
	if err != nil {
		return fmt.Errorf("store: update %s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func exists(ctx context.Context, q querier, sql string, args ...any) error {
	var found bool
	if err := q.QueryRow(ctx, sql, args...).Scan(&found); err != nil {
		return fmt.Errorf("store: reference: %w", err)
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

// refIncident returns ErrNotFound unless id is empty or names an incident of org.
func refIncident(ctx context.Context, q querier, org, id string) error {
	if id == "" {
		return nil
	}
	return exists(ctx, q, `SELECT EXISTS (SELECT 1 FROM ops.incidents WHERE id = $1 AND organization = $2)`, id, org)
}

func refIncidentRefs(ctx context.Context, q querier, in Incident) error {
	if err := refIncident(ctx, q, in.Organization, in.ParentID); err != nil {
		return err
	}
	if in.ProblemID == "" {
		return nil
	}
	return exists(ctx, q, `SELECT EXISTS (SELECT 1 FROM ops.problems WHERE id = $1 AND organization = $2)`,
		in.ProblemID, in.Organization)
}

func collect[T any](rows pgx.Rows, scan func(pgx.Rows) (T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("store: read row: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: read rows: %w", err)
	}
	return out, nil
}

// one returns the single row of rows, or ErrNotFound.
func one[T any](rows pgx.Rows, err error, scan func(pgx.Rows) (T, error)) (T, error) {
	var zero T
	if err != nil {
		return zero, fmt.Errorf("store: query: %w", err)
	}
	list, err := collect(rows, scan)
	if err != nil {
		return zero, err
	}
	if len(list) == 0 {
		return zero, ErrNotFound
	}
	return list[0], nil
}

const signalColumns = `id, organization, source, kind, entity_kind, entity_ref, cluster, severity_floor, title,
	body, fingerprint, state, first_seen, last_seen, coalesce(incident_id, '')`

func scanSignal(r pgx.Rows) (Signal, error) {
	var s Signal
	var body []byte
	err := r.Scan(&s.ID, &s.Organization, &s.Source, &s.Kind, &s.EntityKind, &s.EntityRef, &s.Cluster,
		&s.SeverityFloor, &s.Title, &body, &s.Fingerprint, &s.State, &s.FirstSeen, &s.LastSeen, &s.IncidentID)
	s.Body = body
	return normSignal(s), err
}

func getSignal(ctx context.Context, q querier, org, id string) (Signal, error) {
	rows, err := q.Query(ctx, `SELECT `+signalColumns+` FROM ops.signals
		WHERE id = $1 AND ($2::text = '' OR organization = $2)`, id, org)
	return one(rows, err, scanSignal)
}

func listSignals(ctx context.Context, q querier, f SignalFilter) ([]Signal, error) {
	rows, err := q.Query(ctx, `SELECT `+signalColumns+` FROM ops.signals
		WHERE ($1::text = '' OR organization = $1) AND ($2::text = '' OR incident_id = $2)
		AND ($3::text = '' OR state = $3) AND ($4::text = '' OR fingerprint = $4)
		ORDER BY first_seen, id COLLATE "C" LIMIT $5`,
		f.Organization, f.IncidentID, f.State, f.Fingerprint, clampLimit(f.Limit))
	if err != nil {
		return nil, fmt.Errorf("store: list signals: %w", err)
	}
	return collect(rows, scanSignal)
}

const incidentColumns = `id, organization, title, severity, severity_set_by, state, opened_by, opened_at,
	resolved_at, coalesce(parent_id, ''), coalesce(problem_id, '')`

func scanIncident(r pgx.Rows) (Incident, error) {
	var in Incident
	err := r.Scan(&in.ID, &in.Organization, &in.Title, &in.Severity, &in.SeveritySetBy, &in.State, &in.OpenedBy,
		&in.OpenedAt, &in.ResolvedAt, &in.ParentID, &in.ProblemID)
	return normIncident(in), err
}

func getIncident(ctx context.Context, q querier, org, id string) (Incident, error) {
	rows, err := q.Query(ctx, `SELECT `+incidentColumns+` FROM ops.incidents
		WHERE id = $1 AND ($2::text = '' OR organization = $2)`, id, org)
	return one(rows, err, scanIncident)
}

func listIncidents(ctx context.Context, q querier, f IncidentFilter) ([]Incident, error) {
	rows, err := q.Query(ctx, `SELECT `+incidentColumns+` FROM ops.incidents
		WHERE ($1::text = '' OR organization = $1) AND ($2::text = '' OR state = $2)
		ORDER BY opened_at DESC, id COLLATE "C" DESC LIMIT $3`,
		f.Organization, f.State, clampLimit(f.Limit))
	if err != nil {
		return nil, fmt.Errorf("store: list incidents: %w", err)
	}
	return collect(rows, scanIncident)
}

func listEvents(ctx context.Context, q querier, org, incidentID string) ([]IncidentEvent, error) {
	if _, err := getIncident(ctx, q, org, incidentID); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT id, organization, incident_id, at, actor_kind, actor, kind, data
		FROM ops.incident_events WHERE incident_id = $1 ORDER BY id`, incidentID)
	if err != nil {
		return nil, fmt.Errorf("store: list incident events: %w", err)
	}
	return collect(rows, func(r pgx.Rows) (IncidentEvent, error) {
		var e IncidentEvent
		var data []byte
		err := r.Scan(&e.ID, &e.Organization, &e.IncidentID, &e.At, &e.ActorKind, &e.Actor, &e.Kind, &data)
		e.Data = data
		return normIncidentEvent(e), err
	})
}

const problemColumns = `id, organization, title, statement, state, created_at`

func scanProblem(r pgx.Rows) (Problem, error) {
	var p Problem
	err := r.Scan(&p.ID, &p.Organization, &p.Title, &p.Statement, &p.State, &p.CreatedAt)
	return normProblem(p), err
}

func getProblem(ctx context.Context, q querier, org, id string) (Problem, error) {
	rows, err := q.Query(ctx, `SELECT `+problemColumns+` FROM ops.problems
		WHERE id = $1 AND ($2::text = '' OR organization = $2)`, id, org)
	return one(rows, err, scanProblem)
}

func searchProblems(ctx context.Context, q querier, org, query string, limit int) ([]Problem, error) {
	rows, err := q.Query(ctx, `SELECT `+problemColumns+` FROM ops.problems
		WHERE ($1::text = '' OR organization = $1)
		AND (strpos(lower(title), lower($2::text)) > 0 OR strpos(lower(statement), lower($2::text)) > 0)
		ORDER BY created_at DESC, id COLLATE "C" DESC LIMIT $3`,
		org, query, clampLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("store: search problems: %w", err)
	}
	return collect(rows, scanProblem)
}

const judgementColumns = `id, organization, judge, subject_kind, subject_id, offered, rule_result, answer,
	outcome, asked_at, deadline_at, answered_at`

func scanJudgement(r pgx.Rows) (Judgement, error) {
	var j Judgement
	var offered, ruleResult, answer []byte
	err := r.Scan(&j.ID, &j.Organization, &j.Judge, &j.SubjectKind, &j.SubjectID, &offered, &ruleResult, &answer,
		&j.Outcome, &j.AskedAt, &j.DeadlineAt, &j.AnsweredAt)
	j.Offered, j.RuleResult, j.Answer = offered, ruleResult, answer
	return normJudgement(j), err
}

func getJudgement(ctx context.Context, q querier, org, id string) (Judgement, error) {
	rows, err := q.Query(ctx, `SELECT `+judgementColumns+` FROM ops.judgements
		WHERE id = $1 AND ($2::text = '' OR organization = $2)`, id, org)
	return one(rows, err, scanJudgement)
}

func listJudgements(ctx context.Context, q querier, f JudgementFilter) ([]Judgement, error) {
	rows, err := q.Query(ctx, `SELECT `+judgementColumns+` FROM ops.judgements
		WHERE ($1::text = '' OR organization = $1) AND ($2::text = '' OR judge = $2)
		AND ($3::text = '' OR subject_kind = $3) AND ($4::text = '' OR subject_id = $4)
		AND (CASE WHEN $5::boolean THEN outcome = '' ELSE ($6::text = '' OR outcome = $6) END)
		ORDER BY asked_at DESC, id COLLATE "C" DESC LIMIT $7`,
		f.Organization, f.Judge, f.SubjectKind, f.SubjectID, f.Undecided, f.Outcome, clampLimit(f.Limit))
	if err != nil {
		return nil, fmt.Errorf("store: list judgements: %w", err)
	}
	return collect(rows, scanJudgement)
}

func listFeedback(ctx context.Context, q querier, org, incidentID string) ([]Feedback, error) {
	if _, err := getIncident(ctx, q, org, incidentID); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT id, organization, incident_id, person, kind, data, at
		FROM ops.feedback WHERE incident_id = $1 ORDER BY id`, incidentID)
	if err != nil {
		return nil, fmt.Errorf("store: list feedback: %w", err)
	}
	return collect(rows, func(r pgx.Rows) (Feedback, error) {
		var f Feedback
		var data []byte
		err := r.Scan(&f.ID, &f.Organization, &f.IncidentID, &f.Person, &f.Kind, &data, &f.At)
		f.Data = data
		return normFeedback(f), err
	})
}

// Postgres implements Store.
var _ Store = (*Postgres)(nil)
