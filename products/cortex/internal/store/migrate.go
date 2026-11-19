package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The migrations of cortex, embedded and applied at start:
//
//	migrations/<NNNN>_<name>.sql
//
// Versions start at 0001 and have no gap. A released migration is never changed; a change
// of the schema is a new version. The text {{dimensions}} in a migration is replaced by the
// vector length before it runs.
//
// The migrator keeps the applied versions in the table schema_migrations. Each migration
// runs in its own transaction, together with the row that records it, under a
// transaction-scoped advisory lock, so two processes that start at the same time apply each
// migration once. It runs as the database owner. The first run creates the roles
// cortex_query and cortex_index, so the owner needs CREATEROLE then, unless both roles
// exist already and the owner may grant them.

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migration is one version of the schema.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// ErrSchemaNewer is returned when the database holds a version this program does not know.
var ErrSchemaNewer = errors.New("store: the database schema is newer than this program")

// migrateLock is the key of the advisory lock the migrator takes in each transaction.
const migrateLock int64 = 0x636f727465782d6d // "cortex-m"

var migrationName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.sql$`)

// Migrations returns the migrations of this program, oldest first.
func Migrations() ([]Migration, error) {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	return parseMigrations(sub)
}

// parseMigrations reads the migration files of the top directory of fsys.
func parseMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("store: migrations: %w", err)
	}
	var out []Migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			return nil, fmt.Errorf("store: migrations: %q is not <NNNN>_<name>.sql", e.Name())
		}
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: migrations: %w", err)
		}
		version, _ := strconv.Atoi(m[1])
		out = append(out, Migration{Version: version, Name: m[2], SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, mig := range out {
		if mig.Version != i+1 {
			return nil, fmt.Errorf("store: migrations: version %04d is missing or doubled", i+1)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("store: migrations: none found")
	}
	return out, nil
}

// render writes the vector length into a migration.
func render(sql string, dimensions int) (string, error) {
	if err := checkDimensions(dimensions); err != nil {
		return "", err
	}
	return strings.ReplaceAll(sql, "{{dimensions}}", strconv.Itoa(dimensions)), nil
}

const migrationsTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version    integer     PRIMARY KEY,
    name       text        NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now())`

// Migrate connects to url as the database owner, applies every migration the database does
// not have yet, oldest first, and returns the versions it applied. dimensions is the vector
// length of the embedding model. It returns ErrSchemaNewer, and changes nothing, when the
// database has a version this program does not know.
func Migrate(ctx context.Context, url string, dimensions int) ([]int, error) {
	migs, err := Migrations()
	if err != nil {
		return nil, err
	}
	if err := checkDimensions(dimensions); err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	var applied []int
	for _, mig := range migs {
		done, err := migrateOne(ctx, conn, mig, len(migs), dimensions)
		if err != nil {
			return applied, fmt.Errorf("store: migration %04d: %w", mig.Version, err)
		}
		if done {
			applied = append(applied, mig.Version)
		}
	}
	return applied, nil
}

// migrateOne applies mig in one transaction when the database is older than it.
func migrateOne(ctx context.Context, conn *pgx.Conn, mig Migration, known, dimensions int) (bool, error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLock); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, migrationsTable); err != nil {
		return false, err
	}
	var current int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return false, err
	}
	if current > known {
		return false, fmt.Errorf("%w: version %d, this program knows %d", ErrSchemaNewer, current, known)
	}
	if current >= mig.Version {
		return false, nil
	}
	sql, err := render(mig.SQL, dimensions)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, mig.Version, mig.Name); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
