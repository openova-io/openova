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
)

// The migrations of schema ops. Each version has one file to apply it and one to revert it:
//
//	migrations/<NNNN>_<name>.up.sql
//	migrations/<NNNN>_<name>.down.sql
//
// Versions start at 0001 and have no gap. A migration that has been released is never
// changed; a change of the schema is a new version.
//
// The migrator keeps the applied versions in the table ops.schema_migrations, which it creates
// with schema ops before it applies anything. Each migration runs in its own transaction,
// together with the row that records it, under a transaction-scoped advisory lock, so two
// programs that start at the same time apply each migration once. A down migration removes
// the tables of its version and leaves schema ops and ops.schema_migrations in place.

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migration is one version of the schema.
type Migration struct {
	Version int
	Name    string
	Up      string
	Down    string
}

// ErrSchemaNewer is returned when the database holds a version that this program does not know.
var ErrSchemaNewer = errors.New("store: the database schema is newer than this program")

// migrateLock is the key of the advisory lock that the migrator takes in each transaction.
const migrateLock int64 = 0x6f70732d72656373 // "ops-recs"

var migrationName = regexp.MustCompile(`^([0-9]{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

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
	byVersion := map[int]*Migration{}
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			return nil, fmt.Errorf("store: migrations: %q is not <NNNN>_<name>.up.sql or .down.sql", e.Name())
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("store: migrations: %w", err)
		}
		mig := byVersion[version]
		if mig == nil {
			mig = &Migration{Version: version, Name: m[2]}
			byVersion[version] = mig
		}
		if mig.Name != m[2] {
			return nil, fmt.Errorf("store: migrations: version %04d has two names, %q and %q", version, mig.Name, m[2])
		}
		if m[3] == "up" {
			mig.Up = string(body)
		} else {
			mig.Down = string(body)
		}
	}
	out := make([]Migration, 0, len(byVersion))
	for _, mig := range byVersion {
		out = append(out, *mig)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i, mig := range out {
		if mig.Version != i+1 {
			return nil, fmt.Errorf("store: migrations: version %04d is missing", i+1)
		}
		if mig.Up == "" || mig.Down == "" {
			return nil, fmt.Errorf("store: migrations: version %04d needs both an up and a down file", mig.Version)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("store: migrations: none found")
	}
	return out, nil
}

const migrationsTable = `CREATE SCHEMA IF NOT EXISTS ops;
CREATE TABLE IF NOT EXISTS ops.schema_migrations (
    version    integer     PRIMARY KEY,
    name       text        NOT NULL,
    applied_at timestamptz NOT NULL DEFAULT now()
)`

// SchemaVersion returns the newest version applied to the database, or 0.
func (p *Postgres) SchemaVersion(ctx context.Context) (int, error) {
	if err := p.ready(ctx); err != nil {
		return 0, err
	}
	var present bool
	if err := p.pool.QueryRow(ctx, `SELECT to_regclass('ops.schema_migrations') IS NOT NULL`).Scan(&present); err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	if !present {
		return 0, nil
	}
	return currentVersion(ctx, p.pool)
}

func currentVersion(ctx context.Context, q querier) (int, error) {
	var v int
	if err := q.QueryRow(ctx, `SELECT coalesce(max(version), 0) FROM ops.schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: schema version: %w", err)
	}
	return v, nil
}

// MigrateUp applies every migration the database does not have yet, oldest first, and returns
// the versions it applied. It returns ErrSchemaNewer when the database has a version this
// program does not know, and changes nothing then.
func (p *Postgres) MigrateUp(ctx context.Context) ([]int, error) {
	migs, err := Migrations()
	if err != nil {
		return nil, err
	}
	var applied []int
	for _, mig := range migs {
		done, err := p.migrateStep(ctx, func(current int) (string, bool, error) {
			if current > len(migs) {
				return "", false, fmt.Errorf("%w: version %d, this program knows %d", ErrSchemaNewer, current, len(migs))
			}
			if current >= mig.Version {
				return "", false, nil
			}
			return mig.Up, true, nil
		}, `INSERT INTO ops.schema_migrations (version, name) VALUES ($1, $2)`, mig.Version, mig.Name)
		if err != nil {
			return applied, fmt.Errorf("store: migration %04d up: %w", mig.Version, err)
		}
		if done {
			applied = append(applied, mig.Version)
		}
	}
	return applied, nil
}

// MigrateDown reverts up to steps migrations, newest first, and returns the versions it
// reverted. It stops when no migration is left. Reverting a migration deletes its tables
// and every row in them.
func (p *Postgres) MigrateDown(ctx context.Context, steps int) ([]int, error) {
	migs, err := Migrations()
	if err != nil {
		return nil, err
	}
	var reverted []int
	for i := 0; i < steps; i++ {
		var version int
		done, err := p.migrateStep(ctx, func(current int) (string, bool, error) {
			if current > len(migs) {
				return "", false, fmt.Errorf("%w: version %d, this program knows %d", ErrSchemaNewer, current, len(migs))
			}
			if current == 0 {
				return "", false, nil
			}
			version = current
			return migs[current-1].Down, true, nil
		}, `DELETE FROM ops.schema_migrations WHERE version = (SELECT max(version) FROM ops.schema_migrations)`)
		if err != nil {
			return reverted, fmt.Errorf("store: migration down: %w", err)
		}
		if !done {
			break
		}
		reverted = append(reverted, version)
	}
	return reverted, nil
}

// migrateStep runs one migration in one transaction. choose gets the current version and
// returns the SQL to run, or false when there is nothing to do. record is run after the SQL,
// in the same transaction.
func (p *Postgres) migrateStep(ctx context.Context, choose func(current int) (string, bool, error),
	record string, args ...any) (bool, error) {
	if err := p.ready(ctx); err != nil {
		return false, err
	}
	tx, err := p.pool.Begin(ctx)
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
	current, err := currentVersion(ctx, tx)
	if err != nil {
		return false, err
	}
	sql, run, err := choose(current)
	if err != nil || !run {
		return false, err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, record, args...); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
