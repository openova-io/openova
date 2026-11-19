package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The tests of this file need a PostgreSQL server with the vector extension. Give the address
// of a superuser in CORTEX_TEST_DATABASE_URL; without it they skip. They create the owner role
// cortex and a new database cortex_store_test owned by it, with the extension installed, and
// run everything else as that owner and the two roles, never as the superuser: a superuser
// passes every row policy.
const testDimensions = 64

type fixture struct {
	ctx      context.Context
	ownerURL string
	query    *Postgres
	index    *Postgres
}

func setup(t *testing.T) *fixture {
	t.Helper()
	admin := os.Getenv("CORTEX_TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("CORTEX_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'cortex') THEN
		   CREATE ROLE cortex LOGIN CREATEROLE PASSWORD 'cortex-test'; END IF; END $$`,
		`DROP DATABASE IF EXISTS cortex_store_test WITH (FORCE)`,
		`CREATE DATABASE cortex_store_test OWNER cortex`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	_ = conn.Close(ctx)
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/cortex_store_test"
	conn, err = pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	// The platform's database binding creates the extension; the owner does not.
	if _, err := conn.Exec(ctx, `CREATE EXTENSION vector`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close(ctx)
	u.User = url.UserPassword("cortex", "cortex-test")
	f := &fixture{ctx: ctx, ownerURL: u.String()}
	applied, err := Migrate(ctx, f.ownerURL, testDimensions)
	if err != nil || !reflect.DeepEqual(applied, []int{1}) {
		t.Fatalf("Migrate = %v, %v; want [1]", applied, err)
	}
	if f.query, err = Open(ctx, f.ownerURL, RoleQuery, testDimensions); err != nil {
		t.Fatal(err)
	}
	if f.index, err = Open(ctx, f.ownerURL, RoleIndex, testDimensions); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.query.Close(); f.index.Close() })
	return f
}

// unit returns a vector of testDimensions values whose direction is set by the first values.
func unit(head ...float32) []float32 {
	v := make([]float32, testDimensions)
	copy(v, head)
	v[testDimensions-1] = 0.01
	return v
}

// addDocument inserts a document row as the index role; documents come from ingestion.
func (f *fixture) addDocument(t *testing.T, id, source string) {
	t.Helper()
	if _, err := f.index.pool.Exec(f.ctx, `INSERT INTO document
	  (id, source_id, external_id, title, uri, content_sha256, object_key, state, updated_at)
	  VALUES ($1::uuid, $2, $1::text, $1::text, 'https://example.org/' || $1::text, 'sha', 'obj', 'fetched', now())`, id, source); err != nil {
		t.Fatal(err)
	}
}

// asQuery runs fn as the query role in a transaction whose principal setting is principals
// (a text[] literal), and rolls it back.
func (f *fixture) asQuery(t *testing.T, principals string, fn func(pgx.Tx)) {
	t.Helper()
	tx, err := f.query.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(f.ctx) }()
	if _, err := tx.Exec(f.ctx, `SELECT set_config('cortex.principals', $1, true)`, principals); err != nil {
		t.Fatal(err)
	}
	fn(tx)
}

// visible returns, sorted and without repeats, the document ids of the chunks a principal list
// reads with a query that has no WHERE clause: only row-level security trims it.
func (f *fixture) visible(t *testing.T, principals string) []string {
	t.Helper()
	var ids []string
	f.asQuery(t, principals, func(tx pgx.Tx) {
		rows, err := tx.Query(f.ctx, `SELECT DISTINCT document_id::text FROM chunk ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			t.Fatal(err)
		}
	})
	return ids
}

// docIDs of the fixture.
const (
	hrOpen   = "00000000-0000-0000-0000-000000000001" // inherits the readers of hr
	hrClosed = "00000000-0000-0000-0000-000000000002" // own reader list: user:alice
	boardDoc = "00000000-0000-0000-0000-000000000003" // inherits the readers of board
	hrLate   = "00000000-0000-0000-0000-000000000004" // inherits; indexed while a revoke is in flight
)

var baseDefs = []SourceDefinition{
	{ID: "hr", Title: "HR policies", Digest: "d1", Readers: []string{"group:staff"}, Managers: []string{"user:hr-admin"}},
	{ID: "board", Title: "Board papers", Digest: "d2", Readers: []string{"group:board"}, Managers: []string{"user:secretary"}},
}

func (f *fixture) load(t *testing.T) {
	t.Helper()
	if err := f.index.ApplyDefinitions(f.ctx, baseDefs); err != nil {
		t.Fatal(err)
	}
	f.addDocument(t, hrOpen, "hr")
	f.addDocument(t, hrClosed, "hr")
	f.addDocument(t, boardDoc, "board")
	write := func(doc string, readers []string, heads ...float32) {
		var chunks []Chunk
		for i, h := range heads {
			chunks = append(chunks, Chunk{Ordinal: i, Page: i + 1, Text: doc + " part", Embedding: unit(h, 1)})
		}
		if err := f.index.ReplaceChunks(f.ctx, doc, chunks, readers); err != nil {
			t.Fatal(err)
		}
	}
	write(hrOpen, nil, 1, 2, 3)
	write(hrClosed, []string{"user:alice"}, 4, 5)
	write(boardDoc, nil, 6)
}

func TestPostgresStore(t *testing.T) {
	f := setup(t)
	f.load(t)

	t.Run("two principals see different rows", func(t *testing.T) {
		staff, board := f.visible(t, "{group:staff}"), f.visible(t, "{group:board}")
		if !reflect.DeepEqual(staff, []string{hrOpen}) || !reflect.DeepEqual(board, []string{boardDoc}) {
			t.Fatalf("group:staff reads %v, group:board reads %v", staff, board)
		}
		hits, err := f.query.Search(f.ctx, []string{"group:staff"}, []string{"hr", "board"}, unit(1, 1), 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 3 {
			t.Fatalf("group:staff got %d hits, want the 3 chunks of %s", len(hits), hrOpen)
		}
		for _, h := range hits {
			if h.DocumentID != hrOpen || h.SourceID != "hr" {
				t.Fatalf("group:staff got a hit of %s in %s", h.DocumentID, h.SourceID)
			}
		}
	})

	t.Run("a document with its own reader list", func(t *testing.T) {
		for principals, want := range map[string][]string{
			"{group:staff}":    {hrOpen},
			"{user:alice}":     {hrClosed},
			"{user:hr-admin}":  {hrOpen, hrClosed},
			"{user:secretary}": {boardDoc},
		} {
			if got := f.visible(t, principals); !reflect.DeepEqual(got, want) {
				t.Errorf("%s reads %v, want %v", principals, got, want)
			}
		}
	})

	t.Run("an empty principal list sees none", func(t *testing.T) {
		if got := f.visible(t, "{}"); len(got) != 0 {
			t.Errorf("{} reads %v", got)
		}
		if _, err := f.query.Search(f.ctx, nil, []string{"hr"}, unit(1), 10); !errors.Is(err, ErrNoPrincipals) {
			t.Errorf("Search without principals gave %v, want ErrNoPrincipals", err)
		}
		if _, err := f.query.ReadableSources(f.ctx, []string{}); !errors.Is(err, ErrNoPrincipals) {
			t.Errorf("ReadableSources without principals gave %v, want ErrNoPrincipals", err)
		}
	})

	t.Run("a connection without the setting fails", func(t *testing.T) {
		conn, err := pgx.Connect(f.ctx, f.ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(f.ctx) }()
		if _, err := conn.Exec(f.ctx, `SET ROLE cortex_query`); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"chunk", "document"} {
			var n int
			err := conn.QueryRow(f.ctx, `SELECT count(*) FROM `+table).Scan(&n)
			if err == nil || !strings.Contains(err.Error(), "cortex.principals") {
				t.Errorf("%s without the setting: %d rows, error %v", table, n, err)
			}
		}
	})

	t.Run("row-level security trims a query with no WHERE clause", func(t *testing.T) {
		f.asQuery(t, "{group:staff}", func(tx pgx.Tx) {
			var chunks, documents int
			if err := tx.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM chunk), (SELECT count(*) FROM document)`).Scan(&chunks, &documents); err != nil {
				t.Fatal(err)
			}
			if chunks != 3 || documents != 1 {
				t.Errorf("group:staff reads %d chunks and %d documents, want 3 and 1", chunks, documents)
			}
		})
		// FORCE: the owner takes no role and reads nothing.
		conn, err := pgx.Connect(f.ctx, f.ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(f.ctx) }()
		var n int
		if err := conn.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM chunk) + (SELECT count(*) FROM document)`).Scan(&n); err != nil || n != 0 {
			t.Errorf("the owner reads %d rows (error %v), want 0", n, err)
		}
	})

	t.Run("the query role reads and queues jobs only", func(t *testing.T) {
		for _, sql := range []string{
			`INSERT INTO chunk (document_id, source_id, ordinal, text, readers, embedding)
			 SELECT document_id, source_id, 9, 'x', readers, embedding FROM chunk LIMIT 1`,
			`UPDATE source SET title = 'x'`,
			`DELETE FROM document_acl`,
		} {
			f.asQuery(t, "{user:hr-admin}", func(tx pgx.Tx) {
				if _, err := tx.Exec(f.ctx, sql); err == nil || !strings.Contains(err.Error(), "permission denied") {
					t.Errorf("%q as cortex_query: %v, want permission denied", strings.Fields(sql)[0], err)
				}
			})
		}
		f.asQuery(t, "{user:hr-admin}", func(tx pgx.Tx) {
			if _, err := tx.Exec(f.ctx, `INSERT INTO job (id, kind, source_id, state) VALUES (gen_random_uuid(), 'sync', 'hr', 'queued')`); err != nil {
				t.Errorf("cortex_query cannot queue a job: %v", err)
			}
		})
		if err := f.query.ApplyDefinitions(f.ctx, baseDefs); err == nil {
			t.Error("ApplyDefinitions as cortex_query passed")
		}
	})

	t.Run("search returns the exact top hits, best first", func(t *testing.T) {
		q := unit(4.5, 1)
		hits, err := f.query.Search(f.ctx, []string{"user:hr-admin"}, []string{"hr"}, q, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) != 2 || hits[0].Score < hits[1].Score {
			t.Fatalf("hits = %+v", hits)
		}
		// The two nearest of the five readable chunks are those with heads 4 and 5 (hrClosed).
		for _, h := range hits {
			if h.DocumentID != hrClosed {
				t.Errorf("hit %+v is not one of the two nearest chunks", h)
			}
			want := cosine(q, unit(float32(h.Page+3), 1))
			if math.Abs(h.Score-want) > 1e-5 {
				t.Errorf("chunk %d scored %f, want %f", h.ChunkID, h.Score, want)
			}
		}
		if hits, err := f.query.Search(f.ctx, []string{"user:hr-admin"}, nil, q, 2); err != nil || len(hits) != 0 {
			t.Errorf("no sources gave %v, %v", hits, err)
		}
		if _, err := f.query.Search(f.ctx, []string{"user:hr-admin"}, []string{"hr"}, q[:3], 2); !errors.Is(err, ErrDimensions) {
			t.Errorf("a short vector gave %v, want ErrDimensions", err)
		}
	})

	t.Run("readable sources", func(t *testing.T) {
		for principals, want := range map[string][]string{
			"group:staff": {"hr"}, "user:alice": {"hr"}, "user:secretary": {"board"}, "user:nobody": nil,
		} {
			got, err := f.query.ReadableSources(f.ctx, []string{principals})
			if err != nil || !same(got, want) {
				t.Errorf("%s: %v, %v; want %v", principals, got, err, want)
			}
		}
	})

	t.Run("sources and documents are trimmed to the caller", func(t *testing.T) {
		for principal, want := range map[string]string{
			"group:staff": "hr 1 3", "user:alice": "hr 1 2", "user:hr-admin": "hr 2 5", "user:secretary": "board 1 1", "user:nobody": "",
		} {
			got, err := f.query.Sources(f.ctx, []string{principal})
			var list []string
			for _, s := range got {
				if s.State != "idle" || s.LastSyncAt != nil || s.LastError != nil || s.Title == "" {
					t.Errorf("%s: %+v", principal, s)
				}
				list = append(list, fmt.Sprintf("%s %d %d", s.ID, s.Documents, s.Chunks))
			}
			if err != nil || strings.Join(list, ",") != want {
				t.Errorf("%s: %v, %v; want %q", principal, list, err, want)
			}
		}
		all := []string{hrOpen, hrClosed, boardDoc, "00000000-0000-0000-0000-0000000000ff"}
		for principal, want := range map[string][]string{"group:staff": {hrOpen}, "user:hr-admin": {hrOpen, hrClosed}, "user:nobody": nil} {
			got, err := f.query.Documents(f.ctx, []string{principal}, all)
			var ids []string
			for _, d := range got {
				if d.URI != "https://example.org/"+d.ID || d.State != "fetched" || d.UpdatedAt.IsZero() {
					t.Errorf("%s: %+v", principal, d)
				}
				ids = append(ids, d.ID)
			}
			if err != nil || !reflect.DeepEqual(ids, want) {
				t.Errorf("%s reads documents %v (%v), want %v", principal, ids, err, want)
			}
		}
		if _, err := f.query.Sources(f.ctx, nil); !errors.Is(err, ErrNoPrincipals) {
			t.Errorf("no principals gave %v", err)
		}
		if err := f.query.Ping(f.ctx); err != nil {
			t.Error(err)
		}
	})

	t.Run("a grant change rewrites the readers of the chunks it covers", func(t *testing.T) {
		defs := []SourceDefinition{
			{ID: "hr", Title: "HR policies", Digest: "d1b", Readers: []string{"group:staff", "group:board"}, Managers: []string{"user:hr-admin", "user:bob"}},
			baseDefs[1],
		}
		if err := f.index.ApplyDefinitions(f.ctx, defs); err != nil {
			t.Fatal(err)
		}
		if got := f.visible(t, "{group:board}"); !reflect.DeepEqual(got, []string{hrOpen, boardDoc}) {
			t.Errorf("after the grant group:board reads %v", got)
		}
		if got := f.visible(t, "{user:bob}"); !reflect.DeepEqual(got, []string{hrOpen, hrClosed}) {
			t.Errorf("after the grant the new manager reads %v", got)
		}
		if err := f.index.ApplyDefinitions(f.ctx, baseDefs); err != nil {
			t.Fatal(err)
		}
		if got := f.visible(t, "{group:board}"); !reflect.DeepEqual(got, []string{boardDoc}) {
			t.Errorf("after the revoke group:board reads %v", got)
		}
		if got := f.visible(t, "{user:bob}"); len(got) != 0 {
			t.Errorf("after the revoke user:bob reads %v", got)
		}
		// Interleaved: ApplyDefinitions revokes group:board on hr, then waits before its commit
		// for the row of board this test holds; ReplaceChunks indexes hrLate meanwhile.
		f.addDocument(t, hrLate, "hr")
		if err := f.index.ApplyDefinitions(f.ctx, defs); err != nil {
			t.Fatal(err)
		}
		owner, err := pgx.Connect(f.ctx, f.ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = owner.Close(f.ctx) }()
		hold, err := owner.Begin(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = hold.Rollback(f.ctx) }()
		if _, err := hold.Exec(f.ctx, `SELECT 1 FROM source WHERE id = 'board' FOR UPDATE`); err != nil {
			t.Fatal(err)
		}
		applied, replaced := make(chan error, 1), make(chan error, 1)
		go func() { applied <- f.index.ApplyDefinitions(f.ctx, baseDefs) }()
		waitUntil(t, "ApplyDefinitions waits for the row of board", func() bool { return f.waiters(t) >= 1 })
		go func() {
			replaced <- f.index.ReplaceChunks(f.ctx, hrLate, []Chunk{{Ordinal: 0, Text: "late", Embedding: unit(8, 1)}}, nil)
		}()
		waitUntil(t, "ReplaceChunks returns or waits", func() bool { return len(replaced) == 1 || f.waiters(t) >= 2 })
		if err := hold.Rollback(f.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-applied; err != nil {
			t.Fatal(err)
		}
		if err := <-replaced; err != nil {
			t.Fatal(err)
		}
		if got := f.visible(t, "{group:board}"); !reflect.DeepEqual(got, []string{boardDoc}) {
			t.Errorf("after the interleaved revoke group:board reads %v", got)
		}
		if hits, err := f.query.Search(f.ctx, []string{"group:board"}, []string{"hr"}, unit(8, 1), 10); err != nil || len(hits) != 0 {
			t.Errorf("after the interleaved revoke group:board finds %v (%v) in hr", hits, err)
		}
		if got := f.visible(t, "{group:staff}"); !reflect.DeepEqual(got, []string{hrOpen, hrLate}) {
			t.Errorf("group:staff reads %v, want %s and %s", got, hrOpen, hrLate)
		}
	})

	t.Run("readers equal the document policy", func(t *testing.T) {
		for _, principals := range []string{"{group:staff}", "{user:alice}", "{user:hr-admin}", "{group:board,user:alice}", "{org:members}", "{}"} {
			var viaDocument []string
			f.asQuery(t, principals, func(tx pgx.Tx) {
				rows, err := tx.Query(f.ctx, `SELECT d.id::text FROM document d ORDER BY 1`)
				if err != nil {
					t.Fatal(err)
				}
				if viaDocument, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
					t.Fatal(err)
				}
			})
			if got := f.visible(t, principals); !same(got, viaDocument) {
				t.Errorf("%s: chunks of %v, documents %v", principals, got, viaDocument)
			}
		}
	})

	t.Run("replacing chunks keeps the reader list unless readers is given", func(t *testing.T) {
		if err := f.index.ReplaceChunks(f.ctx, hrClosed, []Chunk{{Ordinal: 0, Text: "new", Embedding: unit(7, 1)}}, nil); err != nil {
			t.Fatal(err)
		}
		f.asQuery(t, "{user:alice}", func(tx pgx.Tx) {
			var texts []string
			rows, err := tx.Query(f.ctx, `SELECT text FROM chunk ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			if texts, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || !reflect.DeepEqual(texts, []string{"new"}) {
				t.Errorf("user:alice reads %v (%v), want only the new chunk", texts, err)
			}
		})
		if err := f.index.ReplaceChunks(f.ctx, hrClosed, []Chunk{{Ordinal: 0, Text: "new", Embedding: unit(7, 1)}}, []string{"user:carol"}); err != nil {
			t.Fatal(err)
		}
		if a, c := f.visible(t, "{user:alice}"), f.visible(t, "{user:carol}"); len(a) != 0 || !reflect.DeepEqual(c, []string{hrClosed}) {
			t.Errorf("after the new list user:alice reads %v and user:carol %v", a, c)
		}
		if err := f.index.ReplaceChunks(f.ctx, "00000000-0000-0000-0000-0000000000ff", nil, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("an unknown document gave %v, want ErrNotFound", err)
		}
	})

	t.Run("a removed source is deleted with its documents and chunks", func(t *testing.T) {
		if err := f.index.ApplyDefinitions(f.ctx, baseDefs[:1]); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := f.index.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM source WHERE id = 'board')
		  + (SELECT count(*) FROM document WHERE source_id = 'board') + (SELECT count(*) FROM chunk WHERE source_id = 'board')`).Scan(&n); err != nil || n != 0 {
			t.Errorf("board left %d rows (%v)", n, err)
		}
	})

	t.Run("open checks the role, the extension and the vector length", func(t *testing.T) {
		if _, err := Open(f.ctx, f.ownerURL, RoleQuery, 128); !errors.Is(err, ErrDimensions) {
			t.Errorf("Open with 128 gave %v, want ErrDimensions", err)
		}
		if _, err := Open(f.ctx, f.ownerURL, Role("cortex"), testDimensions); err == nil {
			t.Error("Open with the owner as role passed")
		}
		if applied, err := Migrate(f.ctx, f.ownerURL, testDimensions); err != nil || len(applied) != 0 {
			t.Errorf("a second Migrate applied %v (%v), want nothing", applied, err)
		}
	})

	t.Run("a narrow per-document reader gets the exact scan, not the HNSW index", func(t *testing.T) {
		plan, design := f.narrowReaderPlans(t)
		t.Logf("plan of the search:\n%s\nplan without the bound principals:\n%s", plan, design)
		if strings.Contains(plan, "chunk_embedding") || !strings.Contains(plan, "chunk_readers") {
			t.Errorf("the search of a narrow per-document reader uses the HNSW index:\n%s", plan)
		}
		// Control: without the bound principals the planner takes the HNSW index for the same
		// reader, so the assertion above can fail.
		if !strings.Contains(design, "chunk_embedding") {
			t.Errorf("control: without the bound principals the plan no longer uses the HNSW index:\n%s", design)
		}
	})
}

// narrowReaderPlans loads 200 sources with 4,000 documents of 5 chunks each; 1 % of the
// documents have the own reader list user:p2. It returns the plan of SearchSQL for user:p2
// over every source, and the plan of the same statement without the bound principals.
func (f *fixture) narrowReaderPlans(t *testing.T) (string, string) {
	t.Helper()
	defs := append([]SourceDefinition{}, baseDefs[:1]...)
	var sources []string
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("bulk-%03d", i)
		sources = append(sources, id)
		defs = append(defs, SourceDefinition{ID: id, Title: id, Digest: "d", Readers: []string{"group:g-" + id}, Managers: []string{"user:m-" + id}})
	}
	if err := f.index.ApplyDefinitions(f.ctx, defs); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`INSERT INTO document (id, source_id, external_id, title, uri, content_sha256, object_key, state, acl_inherit, updated_at)
		 SELECT gen_random_uuid(), ($1::text[])[1 + g % 200], 'e' || g, 't', 'u', 's', 'o', 'indexed', g % 100 <> 0, now()
		 FROM generate_series(1, 4000) g`,
		`INSERT INTO document_acl (document_id, principal) SELECT id, 'user:p2' FROM document WHERE NOT acl_inherit`,
	} {
		args := []any{}
		if strings.Contains(sql, "$1") {
			args = append(args, sources)
		}
		if _, err := f.index.pool.Exec(f.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.index.pool.Exec(f.ctx, `INSERT INTO chunk (document_id, source_id, ordinal, text, readers, embedding)
	  SELECT d.id, d.source_id, o, 'x', `+readersOf+`,
	         ARRAY(SELECT random() FROM generate_series(1, $1::int) WHERE o >= 0 AND d.id IS NOT NULL)::vector
	  FROM document d CROSS JOIN generate_series(1, 5) o WHERE d.source_id LIKE 'bulk-%'`, testDimensions); err != nil {
		t.Fatal(err)
	}
	owner, err := pgx.Connect(f.ctx, f.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close(f.ctx) }()
	if _, err := owner.Exec(f.ctx, `ANALYZE chunk; ANALYZE document; ANALYZE document_acl; ANALYZE source_acl`); err != nil {
		t.Fatal(err)
	}
	principals := []string{"user:p2"}
	q := vectorLiteral(unit(1, 2, 3))
	explain := func(sql string, args ...any) string {
		var lines []string
		f.asQuery(t, "{user:p2}", func(tx pgx.Tx) {
			if _, err := tx.Exec(f.ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(f.ctx, "EXPLAIN "+sql, args...)
			if err != nil {
				t.Fatal(err)
			}
			if lines, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				t.Fatal(err)
			}
		})
		return strings.Join(lines, "\n")
	}
	design := strings.Replace(SearchSQL, "AND c.readers && $3::text[]", "AND $3::text[] IS NOT NULL", 1)
	if design == SearchSQL {
		t.Fatal("SearchSQL no longer binds the principals as the control expects")
	}
	sort.Strings(sources)
	return explain(SearchSQL, q, sources, principals, 10), explain(design, q, sources, principals, 10)
}

// waiters counts the lock requests that wait in the server.
func (f *fixture) waiters(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.index.pool.QueryRow(f.ctx, `SELECT count(*) FROM pg_locks WHERE NOT granted`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitUntil checks cond every 10 ms and fails the test after 30 s.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
	}
}

// same reports whether two lists hold the same values; nil and empty are the same.
func same(a, b []string) bool {
	return len(a) == len(b) && (len(a) == 0 || reflect.DeepEqual(a, b))
}

func cosine(a, b []float32) float64 {
	var ab, aa, bb float64
	for i := range a {
		ab += float64(a[i]) * float64(b[i])
		aa += float64(a[i]) * float64(a[i])
		bb += float64(b[i]) * float64(b[i])
	}
	return ab / math.Sqrt(aa*bb)
}
