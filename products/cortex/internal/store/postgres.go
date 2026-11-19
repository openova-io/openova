package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres is the store on PostgreSQL with the vector extension. It connects as the database
// owner and takes one role on every connection with SET ROLE.
type Postgres struct {
	pool       *pgxpool.Pool
	role       Role
	dimensions int
}

var _ Store = (*Postgres)(nil)

// SearchSQL is the trimmed query. $1 is the query vector, $2 the sources asked for (already
// intersected with the readable ones), $3 the caller's principals, $4 the limit. The
// principals are bound here and again, through the setting cortex.principals, in the row
// policy: bound twice, the planner takes the exact scan of the reader's rows for a narrow
// reader under a custom plan and a generic plan alike (spike PRD-061).
const SearchSQL = `SELECT c.id, c.document_id::text, c.source_id, coalesce(c.page, 0), c.text,
       1 - (c.embedding <=> $1::vector) AS score
FROM chunk c
WHERE c.source_id = ANY ($2::text[])
  AND c.readers && $3::text[]
ORDER BY c.embedding <=> $1::vector
LIMIT $4`

// readersOf is the readers rule for the document row d: the principals in source_acl of its
// source when it inherits, else only the managers there, and every principal in document_acl
// for it. It is the document policy of migration 0001, written as a list.
const readersOf = `ARRAY(
  SELECT s.principal FROM source_acl s
   WHERE s.source_id = d.source_id AND (d.acl_inherit OR s.role = 'manager')
  UNION
  SELECT a.principal FROM document_acl a WHERE a.document_id = d.id
  ORDER BY 1)`

// rewriteSQL makes readers of every chunk of the source $1 equal to the rule. It rewrites
// only the chunks whose readers differ.
const rewriteSQL = `UPDATE chunk c SET readers = r.readers
FROM (SELECT d.id, ` + readersOf + ` AS readers FROM document d WHERE d.source_id = $1) r
WHERE c.document_id = r.id AND c.readers IS DISTINCT FROM r.readers`

// Open connects to url, takes role on every connection, and checks that the vector extension
// is 0.8.0 or later and that the embedding column holds vectors of length dimensions.
// Migrate must have run before.
func Open(ctx context.Context, url string, role Role, dimensions int) (*Postgres, error) {
	if role != RoleQuery && role != RoleIndex {
		return nil, fmt.Errorf("store: unknown role %q", role)
	}
	if err := checkDimensions(dimensions); err != nil {
		return nil, err
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE "+pgx.Identifier{string(role)}.Sanitize())
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	p := &Postgres{pool: pool, role: role, dimensions: dimensions}
	if err := p.check(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

// Close closes the connections.
func (p *Postgres) Close() { p.pool.Close() }

// check reads the role in use, the extension version and the embedding column's type.
func (p *Postgres) check(ctx context.Context) error {
	var user, version, column string
	err := p.pool.QueryRow(ctx, `SELECT current_user,
	  coalesce((SELECT extversion FROM pg_extension WHERE extname = 'vector'), ''),
	  coalesce((SELECT format_type(a.atttypid, a.atttypmod) FROM pg_attribute a
	             WHERE a.attrelid = to_regclass('chunk') AND a.attname = 'embedding'), '')`).Scan(&user, &version, &column)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if user != string(p.role) {
		return fmt.Errorf("store: the connection runs as %q, want %q", user, p.role)
	}
	if !versionAtLeast(version, 0, 8) {
		return fmt.Errorf("%w: found %q", ErrExtension, version)
	}
	if want := fmt.Sprintf("vector(%d)", p.dimensions); column != want {
		if column == "" {
			return errors.New("store: the table chunk is missing; run Migrate first")
		}
		return fmt.Errorf("%w: the database holds %s, the model gives %s", ErrDimensions, column, want)
	}
	return nil
}

// versionAtLeast reports whether the version "<major>.<minor>[.<patch>]" is major.minor or later.
func versionAtLeast(version string, major, minor int) bool {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(parts[0])
	mi, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return ma > major || (ma == major && mi >= minor)
}

// vectorLiteral writes v in the text form of the vector type.
func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// trimmed runs fn in a read-only transaction whose setting cortex.principals holds principals.
func (p *Postgres) trimmed(ctx context.Context, principals []string, fn func(pgx.Tx) error) error {
	if err := checkPrincipals(principals); err != nil {
		return err
	}
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('cortex.principals', $1::text[]::text, true)`, principals); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Search runs SearchSQL for the caller's principals and returns the hits by score, best first.
func (p *Postgres) Search(ctx context.Context, principals []string, sources []string, vector []float32, limit int) ([]Hit, error) {
	if limit < 1 || limit > MaxLimit {
		return nil, fmt.Errorf("store: the limit %d is not between 1 and %d", limit, MaxLimit)
	}
	if len(vector) != p.dimensions {
		return nil, fmt.Errorf("%w: the query vector has %d values, want %d", ErrDimensions, len(vector), p.dimensions)
	}
	var hits []Hit
	err := p.trimmed(ctx, principals, func(tx pgx.Tx) error {
		if len(sources) == 0 {
			return nil
		}
		// The HNSW scan stays in use for broad readers; without this it returns too few rows.
		if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
			return fmt.Errorf("store: %w", err)
		}
		rows, err := tx.Query(ctx, SearchSQL, vectorLiteral(vector), sources, principals, limit)
		if err != nil {
			return fmt.Errorf("store: search: %w", err)
		}
		hits, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (Hit, error) {
			var h Hit
			err := r.Scan(&h.ChunkID, &h.DocumentID, &h.SourceID, &h.Page, &h.Text, &h.Score)
			return h, err
		})
		if err != nil {
			return fmt.Errorf("store: search: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortHits(hits)
	return hits, nil
}

// sortHits orders hits by score, best first, then by chunk id. relaxed_order may return rows
// out of distance order, so the store sorts again.
func sortHits(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ChunkID < hits[j].ChunkID
	})
}

// ReadableSources returns, sorted, the sources in which a principal holds a grant in
// source_acl, or a document_acl entry on a document the caller may read.
func (p *Postgres) ReadableSources(ctx context.Context, principals []string) ([]string, error) {
	var ids []string
	err := p.trimmed(ctx, principals, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.source_id FROM source_acl s WHERE s.principal = ANY ($1::text[])
UNION
SELECT d.source_id FROM document_acl a JOIN document d ON d.id = a.document_id WHERE a.principal = ANY ($1::text[])
ORDER BY 1`, principals)
		if err != nil {
			return fmt.Errorf("store: readable sources: %w", err)
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("store: readable sources: %w", err)
		}
		return nil
	})
	return ids, err
}

// ReplaceChunks deletes the document's chunks and writes chunks in their place, each with
// the document's readers. When readers is not nil, the document gets exactly that reader
// list and stops inheriting the readers of its source; its managers still read it.
func (p *Postgres) ReplaceChunks(ctx context.Context, documentID string, chunks []Chunk, readers []string) error {
	if len(readers) > 0 {
		if err := checkPrincipals(readers); err != nil {
			return err
		}
	}
	for _, c := range chunks {
		if len(c.Embedding) != p.dimensions {
			return fmt.Errorf("%w: chunk %d has %d values, want %d", ErrDimensions, c.Ordinal, len(c.Embedding), p.dimensions)
		}
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// Wait for an ApplyDefinitions in flight and hold it off until this commits: the readers
	// written below are then those of the committed grants, never of grants a revoke replaces
	// meanwhile. The lock comes first, as in ApplyDefinitions, so the two never deadlock.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, applyLock); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	var sourceID string
	err = tx.QueryRow(ctx, `SELECT source_id FROM document WHERE id = $1::uuid FOR UPDATE`, documentID).Scan(&sourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: document %s", ErrNotFound, documentID)
	}
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if readers != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM document_acl WHERE document_id = $1::uuid`, documentID); err != nil {
			return fmt.Errorf("store: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_acl (document_id, principal)
SELECT $1::uuid, p FROM unnest($2::text[]) AS p ON CONFLICT DO NOTHING`, documentID, readers); err != nil {
			return fmt.Errorf("store: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE document SET acl_inherit = false WHERE id = $1::uuid`, documentID); err != nil {
			return fmt.Errorf("store: %w", err)
		}
	}
	var list []string
	if err := tx.QueryRow(ctx, `SELECT `+readersOf+` FROM document d WHERE d.id = $1::uuid`, documentID).Scan(&list); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM chunk WHERE document_id = $1::uuid`, documentID); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	batch := &pgx.Batch{}
	for _, c := range chunks {
		var page any
		if c.Page > 0 {
			page = c.Page
		}
		batch.Queue(`INSERT INTO chunk (document_id, source_id, ordinal, page, text, readers, embedding)
VALUES ($1::uuid, $2, $3, $4, $5, $6::text[], $7::vector)`, documentID, sourceID, c.Ordinal, page, c.Text, list, vectorLiteral(c.Embedding))
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	return tx.Commit(ctx)
}

// ApplyDefinitions makes source and source_acl equal to defs in one transaction. A source not
// in defs is deleted with its documents and chunks. When the grants of a source change,
// readers of every chunk of that source is rewritten in the same transaction, so a grant is
// visible to search exactly when it is committed. Run it from the index process, never from
// a request: a grant change writes every chunk it covers.
func (p *Postgres) ApplyDefinitions(ctx context.Context, defs []SourceDefinition) error {
	ids := make([]string, 0, len(defs))
	seen := map[string]bool{}
	for _, d := range defs {
		if d.ID == "" || d.Title == "" || d.Digest == "" {
			return fmt.Errorf("store: source %q needs an id, a title and a digest", d.ID)
		}
		if err := checkSource(d); err != nil {
			return err
		}
		if seen[d.ID] {
			return fmt.Errorf("store: source %q is defined twice", d.ID)
		}
		seen[d.ID] = true
		for _, list := range [][]string{d.Readers, d.Managers} {
			if len(list) > 0 {
				if err := checkPrincipals(list); err != nil {
					return fmt.Errorf("source %q: %w", d.ID, err)
				}
			}
		}
		ids = append(ids, d.ID)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, applyLock); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	// The originals of the documents of a removed source are deleted by a job each, queued in
	// this transaction, so none is left when the source goes.
	if _, err := tx.Exec(ctx, `INSERT INTO job (id, kind, source_id, document_id, state, item)
SELECT gen_random_uuid(), 'delete', d.source_id, d.id, 'queued', jsonb_build_object('object', d.object_key)
FROM document d WHERE d.source_id <> ALL ($1::text[])`, ids); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM source WHERE id <> ALL ($1::text[])`, ids); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	for _, d := range defs {
		if _, err := tx.Exec(ctx, `INSERT INTO source (id, title, definition_sha256, connector, chunk_tokens, chunk_overlap)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, definition_sha256 = EXCLUDED.definition_sha256,
  connector = EXCLUDED.connector, chunk_tokens = EXCLUDED.chunk_tokens, chunk_overlap = EXCLUDED.chunk_overlap
WHERE (source.title, source.definition_sha256, source.connector, source.chunk_tokens, source.chunk_overlap)
  IS DISTINCT FROM (EXCLUDED.title, EXCLUDED.definition_sha256, EXCLUDED.connector, EXCLUDED.chunk_tokens, EXCLUDED.chunk_overlap)`,
			d.ID, d.Title, d.Digest, d.Connector, d.Chunk.Tokens, d.Chunk.Overlap); err != nil {
			return fmt.Errorf("store: source %q: %w", d.ID, err)
		}
		principals := append(append([]string{}, d.Readers...), d.Managers...)
		roles := make([]string, 0, len(principals))
		for range d.Readers {
			roles = append(roles, "reader")
		}
		for range d.Managers {
			roles = append(roles, "manager")
		}
		var changed int64
		if err := tx.QueryRow(ctx, `WITH want AS (
  SELECT DISTINCT p, r FROM unnest($2::text[], $3::text[]) AS w(p, r)),
gone AS (
  DELETE FROM source_acl s WHERE s.source_id = $1
     AND NOT EXISTS (SELECT 1 FROM want WHERE want.p = s.principal AND want.r = s.role) RETURNING 1),
added AS (
  INSERT INTO source_acl (source_id, principal, role) SELECT $1, p, r FROM want
  ON CONFLICT DO NOTHING RETURNING 1)
SELECT (SELECT count(*) FROM gone) + (SELECT count(*) FROM added)`, d.ID, principals, roles).Scan(&changed); err != nil {
			return fmt.Errorf("store: source %q: %w", d.ID, err)
		}
		if changed > 0 {
			if _, err := tx.Exec(ctx, rewriteSQL, d.ID); err != nil {
				return fmt.Errorf("store: source %q: rewrite readers: %w", d.ID, err)
			}
		}
	}
	return tx.Commit(ctx)
}

// applyLock is the key of the advisory lock ApplyDefinitions takes alone and ReplaceChunks
// takes shared, so two index processes apply the definitions one after the other and no
// chunk is committed with the readers of grants that changed while it was written.
const applyLock int64 = 0x636f727465782d64 // "cortex-d"
