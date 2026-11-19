package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The job table and the documents of ingestion. The query process reads trimmed and queues
// jobs; the index process claims them and writes the documents.

// uuidForm is the text form of a document or job id.
var uuidForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// sourceManager and documentManager are true when a principal of $2 manages the source s, or
// the source of the document d.
const (
	sourceManager   = `EXISTS (SELECT 1 FROM source_acl a WHERE a.source_id = s.id AND a.role = 'manager' AND a.principal = ANY ($2::text[]))`
	documentManager = `EXISTS (SELECT 1 FROM source_acl a WHERE a.source_id = d.source_id AND a.role = 'manager' AND a.principal = ANY ($2::text[]))`
)

const jobColumns = `id::text, kind, coalesce(source_id, ''), coalesce(document_id::text, ''), state, attempts, item, updated_at`

// Source returns the connector type of the source id and whether a principal manages it.
func (p *Postgres) Source(ctx context.Context, principals []string, id string) (SourceAccess, error) {
	var s SourceAccess
	err := p.trimmed(ctx, principals, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT coalesce(s.connector, ''), `+sourceManager+`
FROM source s WHERE s.id = $1`, id, principals).Scan(&s.Connector, &s.Manager)
		return notFound(err, "source "+id)
	})
	return s, err
}

// Document returns the document id when a principal may read it; row-level security decides.
func (p *Postgres) Document(ctx context.Context, principals []string, id string) (DocumentAccess, error) {
	var d DocumentAccess
	if !uuidForm.MatchString(id) {
		return d, fmt.Errorf("%w: document %q", ErrNotFound, id)
	}
	err := p.trimmed(ctx, principals, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT d.id::text, d.source_id, d.title, d.uri, d.state, d.updated_at, d.object_key, `+
			documentManager+` FROM document d WHERE d.id = $1::uuid`, id, principals).
			Scan(&d.ID, &d.SourceID, &d.Title, &d.URI, &d.State, &d.UpdatedAt, &d.ObjectKey, &d.Manager)
		return notFound(err, "document "+id)
	})
	return d, err
}

// MayWatch reports whether a principal reads the document documentID or manages the source;
// without a document, whether it reads or manages the source.
func (p *Postgres) MayWatch(ctx context.Context, principals []string, sourceID, documentID string) (bool, error) {
	if documentID != "" && !uuidForm.MatchString(documentID) {
		return false, nil
	}
	var ok bool
	err := p.trimmed(ctx, principals, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM source_acl a WHERE a.source_id = $1
    AND a.principal = ANY ($2::text[]) AND (a.role = 'manager' OR $3 = ''))
  OR EXISTS (SELECT 1 FROM document d WHERE d.id = nullif($3, '')::uuid)`, sourceID, principals, documentID).Scan(&ok)
	})
	return ok, err
}

// Enqueue inserts job as queued and returns its id. The insert notifies cortex_job.
func (p *Postgres) Enqueue(ctx context.Context, job Job) (string, error) {
	var item any
	if len(job.Item) > 0 {
		item = string(job.Item)
	}
	var id string
	err := p.pool.QueryRow(ctx, `INSERT INTO job (id, kind, source_id, document_id, state, item)
VALUES (gen_random_uuid(), $1, nullif($2, ''), nullif($3, '')::uuid, 'queued', $4::jsonb) RETURNING id::text`,
		job.Kind, job.SourceID, job.DocumentID, item).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("store: enqueue: %w", err)
	}
	return id, nil
}

// Job returns the job id.
func (p *Postgres) Job(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(p.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM job WHERE id = $1::uuid`, id))
	return j, notFound(err, "job "+id)
}

// Claim takes the oldest job that is due, sets it running with one more attempt, and holds it
// for lease: a job whose process ended while it ran is due again when the lease runs out. It
// returns nil when no job is due.
func (p *Postgres) Claim(ctx context.Context, lease time.Duration) (*Job, error) {
	j, err := scanJob(p.pool.QueryRow(ctx, `WITH next AS (
  SELECT id FROM job WHERE state IN ('queued','failed','running') AND not_before <= now()
  ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1)
UPDATE job SET state = 'running', attempts = job.attempts + 1, not_before = now() + $1 * interval '1 second', updated_at = now()
FROM next WHERE job.id = next.id
RETURNING job.`+jobColumns, lease.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: claim: %w", err)
	}
	return &j, nil
}

// NextDue returns how long, by the database's clock, until the next queued, failed or running
// job is due (zero or less: it is due); ok is false when there is none.
func (p *Postgres) NextDue(ctx context.Context) (in time.Duration, ok bool, err error) {
	var s *float64
	if err := p.pool.QueryRow(ctx, `SELECT extract(epoch FROM min(not_before) - now())::float8 FROM job
WHERE state IN ('queued','failed','running')`).Scan(&s); err != nil {
		return 0, false, wrap(err)
	}
	if s == nil {
		return 0, false, nil
	}
	return time.Duration(*s * float64(time.Second)), true, nil
}

// Finish sets the running job id done.
func (p *Postgres) Finish(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `UPDATE job SET state = 'done', error = NULL, updated_at = now() WHERE id = $1::uuid AND state = 'running'`, id)
	return wrap(err)
}

// Fail sets the job id failed, due again after retry, or dead when dead is true. message is
// stored as the job's error.
func (p *Postgres) Fail(ctx context.Context, id, message string, retry time.Duration, dead bool) error {
	_, err := p.pool.Exec(ctx, `UPDATE job SET state = CASE WHEN $3 THEN 'dead' ELSE 'failed' END, error = $2,
  not_before = now() + $4 * interval '1 second', updated_at = now() WHERE id = $1::uuid`, id, message, dead, retry.Seconds())
	return wrap(err)
}

// PutDocument records d as fetched, with exactly readers as its own reader list when readers is
// not empty, in one transaction, and returns the chunk settings of its source. It returns
// ErrNotFound when the source does not exist.
func (p *Postgres) PutDocument(ctx context.Context, d NewDocument, readers []string) (ChunkSettings, error) {
	var c ChunkSettings
	if len(readers) > 0 {
		if err := checkPrincipals(readers); err != nil {
			return c, err
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return c, wrap(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// As in ReplaceChunks: the document's grants are never written beside a change of its source's.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, applyLock); err != nil {
		return c, wrap(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO document (id, source_id, external_id, title, uri, content_sha256, object_key, state, acl_inherit, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, 'fetched', $8, now())
ON CONFLICT (id) DO UPDATE SET state = 'fetched', updated_at = now()`, d.ID, d.SourceID, d.ExternalID, d.Title, d.URI, d.SHA256, d.ObjectKey, len(readers) == 0)
	if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23503" {
		return c, fmt.Errorf("%w: source %s", ErrNotFound, d.SourceID)
	}
	if err != nil {
		return c, wrap(err)
	}
	if len(readers) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM document_acl WHERE document_id = $1::uuid`, d.ID); err != nil {
			return c, wrap(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_acl (document_id, principal)
SELECT $1::uuid, p FROM unnest($2::text[]) AS p ON CONFLICT DO NOTHING`, d.ID, readers); err != nil {
			return c, wrap(err)
		}
	}
	if err := tx.QueryRow(ctx, `SELECT chunk_tokens, chunk_overlap FROM source WHERE id = $1`, d.SourceID).Scan(&c.Tokens, &c.Overlap); err != nil {
		return c, wrap(err)
	}
	return c, wrap(tx.Commit(ctx))
}

// MarkDocument sets the state of the document id: fetched, indexed or failed.
func (p *Postgres) MarkDocument(ctx context.Context, id, state string) error {
	_, err := p.pool.Exec(ctx, `UPDATE document SET state = $2, updated_at = now() WHERE id = $1::uuid`, id, state)
	return wrap(err)
}

// DeleteDocument deletes the document id with its chunks and its reader list.
func (p *Postgres) DeleteDocument(ctx context.Context, id string) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM document WHERE id = $1::uuid`, id)
	return wrap(err)
}

// Listen calls fn with the payload of every notification of cortex_job, and with "" once
// LISTEN is in place, on a connection of its own, until ctx ends (nil) or the connection fails.
func (p *Postgres) Listen(ctx context.Context, fn func(jobID string)) error {
	c, err := p.pool.Acquire(ctx)
	if err != nil {
		return wrap(err)
	}
	conn := c.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, `LISTEN cortex_job`); err != nil {
		return wrap(err)
	}
	fn("")
	for {
		n, err := conn.WaitForNotification(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return wrap(err)
		}
		fn(n.Payload)
	}
}

func scanJob(row pgx.Row) (Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.Kind, &j.SourceID, &j.DocumentID, &j.State, &j.Attempts, &j.Item, &j.UpdatedAt)
	return j, err
}

// notFound turns no row into ErrNotFound.
func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return wrap(err)
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("store: %w", err)
}
