package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// sourcesSQL lists the sources in which a principal of $1 holds a grant, or reads a document
// through its own reader list. Row-level security trims the two counts to the rows the caller
// reads; the chunk count binds the principals again, as SearchSQL does.
const sourcesSQL = `SELECT s.id, s.title, s.state, s.last_sync_at, s.last_error,
  (SELECT count(*) FROM document d WHERE d.source_id = s.id),
  (SELECT count(*) FROM chunk c WHERE c.source_id = s.id AND c.readers && $1::text[])
FROM source s
WHERE EXISTS (SELECT 1 FROM source_acl a WHERE a.source_id = s.id AND a.principal = ANY ($1::text[]))
   OR EXISTS (SELECT 1 FROM document d JOIN document_acl x ON x.document_id = d.id
               WHERE d.source_id = s.id AND x.principal = ANY ($1::text[]))
ORDER BY s.id`

// documentsSQL reads the documents $1; row-level security leaves out those the caller may not read.
const documentsSQL = `SELECT d.id::text, d.source_id, d.title, d.uri, d.state, d.updated_at
FROM document d WHERE d.id = ANY ($1::uuid[]) ORDER BY d.id`

// CheckPrincipal refuses p when the store does not take it as a principal. The parser of the
// definitions applies it, so a version the store would refuse is refused there, once.
func CheckPrincipal(p string) error { return checkPrincipals([]string{p}) }

// Sources returns the sources the principals read, with what they read of each.
func (p *Postgres) Sources(ctx context.Context, principals []string) ([]SourceStatus, error) {
	var out []SourceStatus
	err := p.trimmed(ctx, principals, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sourcesSQL, principals)
		if err != nil {
			return fmt.Errorf("store: sources: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (SourceStatus, error) {
			var s SourceStatus
			err := r.Scan(&s.ID, &s.Title, &s.State, &s.LastSyncAt, &s.LastError, &s.Documents, &s.Chunks)
			return s, err
		})
		if err != nil {
			return fmt.Errorf("store: sources: %w", err)
		}
		return nil
	})
	return out, err
}

// Documents returns the documents of ids the principals read.
func (p *Postgres) Documents(ctx context.Context, principals []string, ids []string) ([]Document, error) {
	var out []Document
	err := p.trimmed(ctx, principals, func(tx pgx.Tx) error {
		if len(ids) == 0 {
			return nil
		}
		rows, err := tx.Query(ctx, documentsSQL, ids)
		if err != nil {
			return fmt.Errorf("store: documents: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Document, error) {
			var d Document
			err := r.Scan(&d.ID, &d.SourceID, &d.Title, &d.URI, &d.State, &d.UpdatedAt)
			return d, err
		})
		if err != nil {
			return fmt.Errorf("store: documents: %w", err)
		}
		return nil
	})
	return out, err
}

// Ping reports whether the database answers.
func (p *Postgres) Ping(ctx context.Context) error {
	if err := p.pool.Ping(ctx); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return nil
}
