// Package store is the Postgres persistence layer: embedded migrations applied
// at startup and plain SQL queries, every read filtered by a Scope.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// Store wraps the database handle.
type Store struct {
	db *sql.DB
}

// New returns a Store over an open connection pool.
func New(db *sql.DB) *Store { return &Store{db: db} }

// Open connects to Postgres and pings it.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// DB exposes the handle for readiness checks.
func (s *Store) DB() *sql.DB { return s.db }

// Ping checks connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// migrations are applied in order inside one transaction each; the applied
// version is recorded in schema_migrations.
var migrations = []string{
	// 1 — the domain of the spec, section 1.
	`
CREATE TABLE IF NOT EXISTS price_books (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	name TEXT NOT NULL UNIQUE,
	currency TEXT NOT NULL DEFAULT 'OMR',
	annual_divisor INT NOT NULL DEFAULT 8760 CHECK (annual_divisor > 0),
	bill_stopped TEXT NOT NULL DEFAULT 'compute' CHECK (bill_stopped IN ('compute','storage-only','none')),
	effective_from DATE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS price_items (
	price_book_id UUID NOT NULL REFERENCES price_books(id) ON DELETE CASCADE,
	sku TEXT NOT NULL,
	unit TEXT NOT NULL,
	unit_price NUMERIC(20,8) NOT NULL,
	annual_price NUMERIC(20,8),
	description TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (price_book_id, sku)
);
CREATE TABLE IF NOT EXISTS customers (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	slug TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL,
	admin_email TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'external' CHECK (kind IN ('external','organization')),
	org_slug TEXT,
	price_book_id UUID REFERENCES price_books(id),
	billing_mode TEXT NOT NULL DEFAULT 'showback' CHECK (billing_mode IN ('real','chargeback','showback')),
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','suspended')),
	start_date DATE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS customer_users (
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('admin','viewer')),
	PRIMARY KEY (customer_id, email)
);
CREATE INDEX IF NOT EXISTS customer_users_email_idx ON customer_users (email);
CREATE TABLE IF NOT EXISTS credentials (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	kind TEXT NOT NULL DEFAULT 'aksk',
	access_key TEXT NOT NULL,
	secret_key_enc BYTEA NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	rotated_at TIMESTAMPTZ,
	revoked_at TIMESTAMPTZ
);
CREATE TABLE IF NOT EXISTS cost_sources (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	kind TEXT NOT NULL CHECK (kind IN ('huawei-project','openova-org','k8s-namespace','file')),
	region TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL DEFAULT '',
	domain_id TEXT,
	credential_id UUID REFERENCES credentials(id),
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','verified','failed')),
	verified_at TIMESTAMPTZ,
	last_collected_at TIMESTAMPTZ,
	last_error TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (customer_id, kind, region, project_id)
);
CREATE TABLE IF NOT EXISTS resource_inventory (
	source_id UUID NOT NULL REFERENCES cost_sources(id) ON DELETE CASCADE,
	resource_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	name TEXT NOT NULL DEFAULT '',
	attrs JSONB NOT NULL DEFAULT '{}'::jsonb,
	first_seen TIMESTAMPTZ NOT NULL,
	last_seen TIMESTAMPTZ NOT NULL,
	deleted_at TIMESTAMPTZ,
	PRIMARY KEY (source_id, resource_id)
);
CREATE TABLE IF NOT EXISTS usage_records (
	id BIGSERIAL PRIMARY KEY,
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	source_id UUID NOT NULL REFERENCES cost_sources(id) ON DELETE CASCADE,
	resource_id TEXT NOT NULL,
	resource_kind TEXT NOT NULL,
	sku TEXT NOT NULL,
	quantity NUMERIC(20,6) NOT NULL,
	unit TEXT NOT NULL,
	window_start TIMESTAMPTZ NOT NULL,
	window_end TIMESTAMPTZ NOT NULL,
	region TEXT NOT NULL DEFAULT '',
	labels JSONB NOT NULL DEFAULT '{}'::jsonb,
	raw_ref TEXT NOT NULL DEFAULT '',
	collected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (source_id, resource_id, sku, window_start)
);
CREATE INDEX IF NOT EXISTS usage_records_customer_window_idx ON usage_records (customer_id, window_start);
CREATE TABLE IF NOT EXISTS statements (
	id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	period_start DATE NOT NULL,
	period_end DATE NOT NULL,
	currency TEXT NOT NULL DEFAULT 'OMR',
	subtotal NUMERIC(20,6) NOT NULL DEFAULT 0,
	tax_rate NUMERIC(6,4) NOT NULL DEFAULT 0.05,
	tax NUMERIC(20,6) NOT NULL DEFAULT 0,
	total NUMERIC(20,6) NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','issued')),
	issued_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (customer_id, period_start)
);
CREATE TABLE IF NOT EXISTS rated_lines (
	id BIGSERIAL PRIMARY KEY,
	statement_id UUID NOT NULL REFERENCES statements(id) ON DELETE CASCADE,
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	source_id UUID REFERENCES cost_sources(id) ON DELETE SET NULL,
	sku TEXT NOT NULL,
	quantity NUMERIC(20,6) NOT NULL,
	unit TEXT NOT NULL,
	unit_price NUMERIC(20,8) NOT NULL,
	amount NUMERIC(20,6) NOT NULL,
	resource_count INT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS rated_lines_statement_idx ON rated_lines (statement_id);
CREATE TABLE IF NOT EXISTS invites (
	token TEXT PRIMARY KEY,
	customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	used_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS audit_log (
	id BIGSERIAL PRIMARY KEY,
	customer_id UUID,
	actor TEXT NOT NULL,
	action TEXT NOT NULL,
	details JSONB NOT NULL DEFAULT '{}'::jsonb,
	at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS audit_log_customer_idx ON audit_log (customer_id, at DESC);
CREATE TABLE IF NOT EXISTS sessions (
	token TEXT PRIMARY KEY,
	email TEXT NOT NULL,
	role TEXT NOT NULL CHECK (role IN ('operator','customer-admin','customer-viewer')),
	customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
	expires_at TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS pins (
	email TEXT PRIMARY KEY,
	code_hash TEXT NOT NULL,
	expires_at TIMESTAMPTZ NOT NULL,
	attempts INT NOT NULL DEFAULT 0
);
`,
}

// Migrate applies every migration not yet recorded in schema_migrations.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	for i, sqlText := range migrations {
		version := i + 1
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %d: %w", version, err)
		}
		if exists {
			continue
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, sqlText); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %d: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func nullStr(p *string) sql.NullString {
	if p == nil || *p == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func strPtr(ns sql.NullString) *string {
	if !ns.Valid {
		return nil
	}
	v := ns.String
	return &v
}

func timePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	v := nt.Time.UTC()
	return &v
}

func nullTime(p *time.Time) sql.NullTime {
	if p == nil || p.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *p, Valid: true}
}

// datePtr renders a DATE column scanned as time as YYYY-MM-DD.
func datePtr(nt sql.NullTime) *string {
	if !nt.Valid {
		return nil
	}
	v := nt.Time.Format("2006-01-02")
	return &v
}

func jsonOrEmpty(v any) []byte {
	if v == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) == 0 || string(b) == "null" {
		return []byte("{}")
	}
	return b
}

// mapErr translates driver errors into the store's sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var pqe *pq.Error
	if errors.As(err, &pqe) {
		switch pqe.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", ErrConflict, pqe.Detail)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", ErrNotFound, pqe.Detail)
		case "23514": // check_violation
			return fmt.Errorf("%w: %s", ErrConflict, pqe.Constraint)
		case "22P02": // invalid_text_representation (bad uuid etc.)
			return ErrNotFound
		}
	}
	return err
}

// ValidDate reports whether s is YYYY-MM-DD.
func ValidDate(s string) bool {
	_, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	return err == nil
}
