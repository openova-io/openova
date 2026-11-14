package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/openova-io/openova/products/ops-records/internal/ids"
)

// The PostgreSQL tests need a database: OPS_RECORDS_TEST_DSN names it, for example
// postgres://ops:ops@localhost:5432/ops_test?sslmode=disable. The user must be allowed to
// create schema ops. The tests drop schema ops in that database and create it again.
// Without the variable they are skipped; the CI job that runs them sets it and fails on a
// skipped test (.github/workflows/test-ungated-trees.yaml, job ops-records).
const testDSNVariable = "OPS_RECORDS_TEST_DSN"

// opsTables are the seven tables of migration 0001.
var opsTables = []string{"signals", "incidents", "incident_events", "problems", "judgements", "feedback", "outbox"}

func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(testDSNVariable)
	if dsn == "" {
		t.Skip(testDSNVariable + " is not set: this test needs a PostgreSQL database")
	}
	return dsn
}

// dropSchema removes schema ops with everything in it, so that a test starts from a database
// that has never seen this program, whatever an earlier run left.
func dropSchema(t *testing.T, p *Postgres) {
	t.Helper()
	if _, err := p.pool.Exec(ctx, `DROP SCHEMA IF EXISTS ops CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func openTestPostgres(t *testing.T, dsn string) *Postgres {
	t.Helper()
	p, err := OpenPostgres(ctx, PostgresConfig{URL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// TestPostgresContract runs the contract test on the PostgreSQL store. Each case gets the
// schema at the newest version with empty tables.
func TestPostgresContract(t *testing.T) {
	dsn := testDSN(t)
	dropSchema(t, openTestPostgres(t, dsn))
	runContract(t, func(t *testing.T) Store {
		p := openTestPostgres(t, dsn)
		if _, err := p.MigrateUp(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := p.pool.Exec(ctx, `TRUNCATE ops.outbox, ops.feedback, ops.judgements, ops.incident_events,
			ops.signals, ops.incidents, ops.problems RESTART IDENTITY`); err != nil {
			t.Fatal(err)
		}
		return p
	})
}

// tablesPresent returns how many of the seven tables exist, and whether change_links exists.
func tablesPresent(t *testing.T, p *Postgres) (int, bool) {
	t.Helper()
	n := 0
	for _, name := range append(append([]string(nil), opsTables...), "change_links") {
		var present bool
		if err := p.pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "ops."+name).Scan(&present); err != nil {
			t.Fatal(err)
		}
		if name == "change_links" {
			return n, present
		}
		if present {
			n++
		}
	}
	return n, false
}

func wantVersion(t *testing.T, p *Postgres, want int) {
	t.Helper()
	got, err := p.SchemaVersion(ctx)
	ok(t, "schema version", err)
	if got != want {
		t.Fatalf("schema version %d, want %d", got, want)
	}
}

func sameInts(t *testing.T, what string, got []int, want ...int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s: %v, want %v", what, got, want)
		}
	}
}

// TestPostgresMigrations: the migrations go up, down and up again, each step once.
func TestPostgresMigrations(t *testing.T) {
	p := openTestPostgres(t, testDSN(t))
	dropSchema(t, p)
	wantVersion(t, p, 0)

	applied, err := p.MigrateUp(ctx)
	ok(t, "up", err)
	sameInts(t, "applied", applied, 1)
	wantVersion(t, p, 1)
	n, links := tablesPresent(t, p)
	if n != len(opsTables) || links {
		t.Fatalf("after up: %d of %d tables, change_links present %v", n, len(opsTables), links)
	}
	applied, err = p.MigrateUp(ctx)
	ok(t, "up again", err)
	sameInts(t, "applied by a second up", applied)

	// A row written now is gone after down and up.
	prb := newProblem("acme", 0, "disk full", "")
	write(t, p, func(tx Tx) error { return tx.InsertProblem(ctx, prb) })

	reverted, err := p.MigrateDown(ctx, 1)
	ok(t, "down", err)
	sameInts(t, "reverted", reverted, 1)
	wantVersion(t, p, 0)
	if n, _ := tablesPresent(t, p); n != 0 {
		t.Fatalf("after down: %d tables are left", n)
	}
	reverted, err = p.MigrateDown(ctx, 1)
	ok(t, "down again", err)
	sameInts(t, "reverted by a second down", reverted)

	applied, err = p.MigrateUp(ctx)
	ok(t, "up after down", err)
	sameInts(t, "applied after down", applied, 1)
	wantVersion(t, p, 1)
	if n, _ := tablesPresent(t, p); n != len(opsTables) {
		t.Fatalf("after up again: %d of %d tables", n, len(opsTables))
	}
	_, err = p.GetProblem(ctx, "acme", prb.ID)
	is(t, "a row from before down", err, ErrNotFound)
	write(t, p, func(tx Tx) error { return tx.InsertProblem(ctx, prb) })
	got, err := p.GetProblem(ctx, "acme", prb.ID)
	ok(t, "a row after up again", err)
	same(t, "problem", got, prb)
}

// TestPostgresMigrateConcurrently: two programs that start at the same time apply each
// migration once.
func TestPostgresMigrateConcurrently(t *testing.T) {
	dsn := testDSN(t)
	first, second := openTestPostgres(t, dsn), openTestPostgres(t, dsn)
	migs, err := Migrations()
	ok(t, "migrations", err)
	dropSchema(t, first)

	var wg sync.WaitGroup
	results := make([][]int, 2)
	errs := make([]error, 2)
	for i, p := range []*Postgres{first, second} {
		wg.Add(1)
		go func(i int, p *Postgres) {
			defer wg.Done()
			results[i], errs[i] = p.MigrateUp(ctx)
		}(i, p)
	}
	wg.Wait()
	ok(t, "first", errs[0])
	ok(t, "second", errs[1])
	if total := len(results[0]) + len(results[1]); total != len(migs) {
		t.Fatalf("applied %v and %v: %d migrations in all, want %d", results[0], results[1], total, len(migs))
	}
	wantVersion(t, first, len(migs))
}

// TestPostgresSchemaNewer: a database with a version this program does not know is left alone.
func TestPostgresSchemaNewer(t *testing.T) {
	p := openTestPostgres(t, testDSN(t))
	_, err := p.MigrateUp(ctx)
	ok(t, "up", err)
	_, err = p.pool.Exec(ctx, `INSERT INTO ops.schema_migrations (version, name) VALUES (9999, 'from_a_newer_program')`)
	ok(t, "a newer version", err)
	t.Cleanup(func() {
		_, _ = p.pool.Exec(context.Background(), `DELETE FROM ops.schema_migrations WHERE version = 9999`)
	})
	_, err = p.MigrateUp(ctx)
	is(t, "up on a newer schema", err, ErrSchemaNewer)
	_, err = p.MigrateDown(ctx, 1)
	is(t, "down on a newer schema", err, ErrSchemaNewer)
	if n, _ := tablesPresent(t, p); n != len(opsTables) {
		t.Fatalf("a refused migration changed the schema: %d of %d tables", n, len(opsTables))
	}
}

// TestPostgresCredentials: the credentials function is asked for every new connection, its
// answer is the login, and its error stops the connection.
func TestPostgresCredentials(t *testing.T) {
	dsn := testDSN(t)
	probe := openTestPostgres(t, dsn)
	var user string
	ok(t, "current user", probe.pool.QueryRow(ctx, `SELECT current_user`).Scan(&user))
	cfg := probe.pool.Config().ConnConfig
	password := cfg.Password

	asked := 0
	var mu sync.Mutex
	p, err := OpenPostgres(ctx, PostgresConfig{URL: dsn, MaxConns: 1, Credentials: func(context.Context) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked++
		return user, password, nil
	}})
	ok(t, "open", err)
	t.Cleanup(func() { _ = p.Close() })
	ok(t, "ping", p.Ping(ctx))
	if asked == 0 {
		t.Fatal("the credentials function was not asked")
	}

	// The function's answer replaces the password of the URL: a wrong one is refused by the
	// database. (The database of the CI job checks passwords.)
	wrong, err := OpenPostgres(ctx, PostgresConfig{URL: dsn, Credentials: func(context.Context) (string, string, error) {
		return user, "wrong-" + password, nil
	}})
	ok(t, "open", err)
	t.Cleanup(func() { _ = wrong.Close() })
	if err := wrong.Ping(ctx); err == nil {
		t.Fatal("a wrong password from the credentials function was not used")
	}

	refused, err := OpenPostgres(ctx, PostgresConfig{URL: dsn, Credentials: func(context.Context) (string, string, error) {
		return "", "", errors.New("no credentials mounted")
	}})
	ok(t, "open", err)
	t.Cleanup(func() { _ = refused.Close() })
	if err := refused.Ping(ctx); err == nil || !strings.Contains(err.Error(), "no credentials mounted") {
		t.Fatalf("ping without credentials: %v", err)
	}
}

// The tests below need no database.

func TestMigrationFiles(t *testing.T) {
	migs, err := Migrations()
	ok(t, "migrations", err)
	if len(migs) != 1 || migs[0].Version != 1 || migs[0].Name != "ops_records" {
		t.Fatalf("migrations: %+v", migs)
	}
	for _, name := range opsTables {
		if !strings.Contains(migs[0].Up, "CREATE TABLE ops."+name+" (") {
			t.Errorf("migration 0001 does not create ops.%s", name)
		}
		if !strings.Contains(migs[0].Down, "DROP TABLE ops."+name+";") {
			t.Errorf("migration 0001 does not drop ops.%s", name)
		}
	}
	if strings.Contains(migs[0].Up, "CREATE TABLE ops.change_links") {
		t.Error("migration 0001 creates change_links")
	}
}

func TestParseMigrations(t *testing.T) {
	file := func(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }
	good := fstest.MapFS{
		"0001_a.up.sql": file("up 1"), "0001_a.down.sql": file("down 1"),
		"0002_b.up.sql": file("up 2"), "0002_b.down.sql": file("down 2"),
	}
	migs, err := parseMigrations(good)
	ok(t, "good", err)
	if len(migs) != 2 || migs[0].Version != 1 || migs[1].Name != "b" || migs[1].Down != "down 2" {
		t.Fatalf("parsed %+v", migs)
	}
	for name, fsys := range map[string]fstest.MapFS{
		"no files":           {},
		"a gap":              {"0001_a.up.sql": file("u"), "0001_a.down.sql": file("d"), "0003_c.up.sql": file("u"), "0003_c.down.sql": file("d")},
		"no down file":       {"0001_a.up.sql": file("u")},
		"no up file":         {"0001_a.down.sql": file("d")},
		"two names":          {"0001_a.up.sql": file("u"), "0001_b.down.sql": file("d")},
		"a name not allowed": {"0001_a.up.sql": file("u"), "0001_a.down.sql": file("d"), "notes.txt": file("x")},
		"an empty file":      {"0001_a.up.sql": file(""), "0001_a.down.sql": file("d")},
		"not from 0001":      {"0002_a.up.sql": file("u"), "0002_a.down.sql": file("d")},
	} {
		if _, err := parseMigrations(fsys); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOpenPostgresRefusesABadURL(t *testing.T) {
	if _, err := OpenPostgres(ctx, PostgresConfig{URL: "postgres://%zz"}); err == nil {
		t.Fatal("a URL that cannot be read was accepted")
	}
}

// The identifiers the schema checks are the identifiers the ids package makes.
func TestSchemaIdentifierForms(t *testing.T) {
	migs, err := Migrations()
	ok(t, "migrations", err)
	for _, want := range []string{
		`'^PRB-[0-7][0-9A-HJKMNP-TV-Z]{25}$'`, `'^INC-[0-7][0-9A-HJKMNP-TV-Z]{25}$'`, `'^[0-7][0-9A-HJKMNP-TV-Z]{25}$'`,
	} {
		if !strings.Contains(migs[0].Up, want) {
			t.Errorf("migration 0001 does not check the form %s", want)
		}
	}
	for _, id := range []string{ids.New(), ids.NewIncident(), ids.NewProblem()} {
		body := strings.TrimPrefix(strings.TrimPrefix(id, ids.IncidentPrefix), ids.ProblemPrefix)
		if body[0] > '7' || strings.ContainsAny(body, "ILOU") {
			t.Errorf("identifier %s does not fit the form the schema checks", id)
		}
	}
}
