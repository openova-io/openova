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
	// #6855 — narrow a project-scoped cost source to one deployment. NULL /
	// empty keeps the prior behaviour (bill everything in the project), so an
	// existing source does not silently stop billing on upgrade.
	`ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS scope_token TEXT NOT NULL DEFAULT '';`,
	// #6862 — discounts and campaigns. A discount never rewrites the price
	// book: list price stays intact so a statement can show list, discount and
	// net, which is what makes the number auditable.
	`CREATE TABLE IF NOT EXISTS discounts (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
		name TEXT NOT NULL,
		kind TEXT NOT NULL CHECK (kind IN ('percent','fixed')),
		value NUMERIC(20,6) NOT NULL CHECK (value >= 0),
		sku TEXT NOT NULL DEFAULT '',
		starts_at TIMESTAMPTZ,
		ends_at TIMESTAMPTZ,
		active BOOLEAN NOT NULL DEFAULT true,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		CHECK (starts_at IS NULL OR ends_at IS NULL OR starts_at < ends_at)
	);`,
	`CREATE INDEX IF NOT EXISTS discounts_customer_idx ON discounts (customer_id);`,
	// The discount actually applied to a statement, frozen at issue time. A
	// campaign that later ends must not change an issued bill.
	`ALTER TABLE statements ADD COLUMN IF NOT EXISTS discount_total NUMERIC(20,6) NOT NULL DEFAULT 0;`,
	`ALTER TABLE statements ADD COLUMN IF NOT EXISTS discount_detail JSONB;`,
	// #6867 — cost analysis. Cost is computed at query time from usage_records
	// joined to the customer's price book (no rollup table, no second source of
	// truth); these two indexes keep month-scale explorer windows in the tens of
	// milliseconds on the measured hw307 shape (72k rows).
	`CREATE INDEX IF NOT EXISTS usage_records_window_sku_idx ON usage_records (window_start, sku);`,
	`CREATE INDEX IF NOT EXISTS usage_records_customer_kind_window_idx ON usage_records (customer_id, resource_kind, window_start);`,
	// #6867 — budgets with thresholds. customer_id NULL = every customer.
	`CREATE TABLE IF NOT EXISTS budgets (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		name TEXT NOT NULL,
		customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
		amount NUMERIC(20,6) NOT NULL CHECK (amount >= 0),
		currency TEXT NOT NULL DEFAULT 'OMR',
		period TEXT NOT NULL DEFAULT 'monthly' CHECK (period IN ('monthly')),
		thresholds INT[] NOT NULL DEFAULT '{50,80,100}',
		notify_emails TEXT[] NOT NULL DEFAULT '{}',
		active BOOLEAN NOT NULL DEFAULT true,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);`,
	`CREATE INDEX IF NOT EXISTS budgets_customer_idx ON budgets (customer_id);`,
	// One alert per (budget, period, threshold): a crossing is recorded once,
	// so an hourly evaluator can never mail the same threshold twice.
	`CREATE TABLE IF NOT EXISTS budget_alerts (
		id BIGSERIAL PRIMARY KEY,
		budget_id UUID NOT NULL REFERENCES budgets(id) ON DELETE CASCADE,
		period TEXT NOT NULL,
		threshold INT NOT NULL,
		actual NUMERIC(20,6) NOT NULL,
		at TIMESTAMPTZ NOT NULL DEFAULT now(),
		UNIQUE (budget_id, period, threshold)
	);`,
	// #6867 — editable allocation basis (was a constant in store/allocation.go).
	// Single row, id = 1.
	`CREATE TABLE IF NOT EXISTS allocation_settings (
		id INT PRIMARY KEY CHECK (id = 1),
		weights JSONB NOT NULL DEFAULT '{"vcpu":1,"mem_gib":1,"pvc_gb":1}'::jsonb,
		overhead_policy TEXT NOT NULL DEFAULT 'separate' CHECK (overhead_policy IN ('separate','distribute')),
		pool TEXT NOT NULL DEFAULT 'sovereign-cost' CHECK (pool IN ('sovereign-cost','manual')),
		manual_amount NUMERIC(20,6) NOT NULL DEFAULT 0,
		currency TEXT NOT NULL DEFAULT 'OMR',
		sovereign_customer_id UUID REFERENCES customers(id) ON DELETE SET NULL,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);`,
	`INSERT INTO allocation_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;`,
	// #6867 — saved explorer views, per signed-in user.
	`CREATE TABLE IF NOT EXISTS saved_views (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		owner_email TEXT NOT NULL,
		name TEXT NOT NULL,
		page TEXT NOT NULL DEFAULT 'explore',
		params JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		UNIQUE (owner_email, page, name)
	);`,
	// #6867 — a discount with no customer is a campaign for every customer.
	`ALTER TABLE discounts ALTER COLUMN customer_id DROP NOT NULL;`,
	// #6867 follow-up — scheduled cost reports. A schedule mails a plain-text
	// cost report on a cadence; customer_id NULL is the operator's
	// Sovereign-wide report. day_of_week (0 = Sunday) is read for weekly
	// schedules, day_of_month (1..28, so every month has the day) for
	// monthly ones. next_at is the due instant the scheduler polls on.
	`CREATE TABLE IF NOT EXISTS report_schedules (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		name TEXT NOT NULL,
		customer_id UUID REFERENCES customers(id) ON DELETE CASCADE,
		cadence TEXT NOT NULL CHECK (cadence IN ('daily','weekly','monthly')),
		day_of_week INT CHECK (day_of_week IS NULL OR day_of_week BETWEEN 0 AND 6),
		day_of_month INT CHECK (day_of_month IS NULL OR day_of_month BETWEEN 1 AND 28),
		hour_utc INT NOT NULL DEFAULT 6 CHECK (hour_utc BETWEEN 0 AND 23),
		recipients TEXT[] NOT NULL DEFAULT '{}',
		sections TEXT[] NOT NULL DEFAULT '{summary,services,customers,budgets,anomalies,recommendations}',
		active BOOLEAN NOT NULL DEFAULT true,
		last_sent_at TIMESTAMPTZ,
		next_at TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);`,
	`CREATE INDEX IF NOT EXISTS report_schedules_due_idx ON report_schedules (next_at) WHERE active;`,
	`CREATE INDEX IF NOT EXISTS report_schedules_customer_idx ON report_schedules (customer_id);`,
	// One row per attempted delivery, failed ones included (ok = false with
	// the error), so the UI can show what was sent and why a run was missed.
	`CREATE TABLE IF NOT EXISTS report_deliveries (
		id BIGSERIAL PRIMARY KEY,
		schedule_id UUID NOT NULL REFERENCES report_schedules(id) ON DELETE CASCADE,
		sent_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		window_from DATE NOT NULL,
		window_to DATE NOT NULL,
		recipients TEXT[] NOT NULL DEFAULT '{}',
		subject TEXT NOT NULL DEFAULT '',
		ok BOOLEAN NOT NULL,
		error TEXT
	);`,
	`CREATE INDEX IF NOT EXISTS report_deliveries_schedule_idx ON report_deliveries (schedule_id, sent_at DESC);`,
	// #6867 follow-up — plan revenue. The catalog plan a customer pays for
	// (s, m, l, xl, flexi; '' = none). OrgSync fills it from the Organization
	// CR's spec.planSlug; the platform collector meters it as plan.<slug>.
	`ALTER TABLE customers ADD COLUMN IF NOT EXISTS plan_slug TEXT NOT NULL DEFAULT '';`,
	// #6867 follow-up — multi-currency conversion. Price books carry a
	// currency; every cost surface reports in ONE reporting currency, which
	// is allocation_settings.currency. per_base is how many units of `code`
	// one unit of the reporting currency buys (1 OMR = 2.6 USD → USD 2.6),
	// so cost_base = cost / per_base. The reporting currency itself is
	// always 1 and never stored here; a book currency without a row is
	// "unconverted" and reported as such, never silently summed.
	`CREATE TABLE IF NOT EXISTS currency_rates (
		code TEXT PRIMARY KEY CHECK (code ~ '^[A-Z]{3}$'),
		per_base NUMERIC(20,10) NOT NULL CHECK (per_base > 0),
		source TEXT NOT NULL DEFAULT 'manual',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);`,
	// #6867 — two-layer sources (DESIGN.md §2, founder direction 2026-09-08).
	// A cost source belongs to ONE layer: cloud (huawei-project, file) or
	// platform (openova-org, openova-platform, k8s-namespace), derived from
	// its kind. A price book has a scope, cloud or platform, and is assigned
	// PER SOURCE — never per customer — so a platform meter can never be
	// rated by a cloud book. The Sovereign itself is not a customer: its own
	// platform footprint lives on one internal `openova-platform` source
	// with no customer, read only by Allocation.
	twoLayerMigrationSQL,
	// A DECOMMISSIONED source (coordinator direction 2026-09-08). The status
	// CHECK allowed only pending/verified/failed, so "this source is retired"
	// could not be expressed at all. A `disabled` source collects nothing
	// more — the collector's listing skips it and it counts as neither
	// verified nor live — but its history is billing data: it still rates,
	// still shows in the explorer, and still stands on every statement
	// already issued from it (DESIGN.md §2.6 "Disabling a source").
	//
	// Its own migration rather than a line inside the one above: that one may
	// already be recorded as applied on a database this is deployed over, and
	// migrations are positional — editing an applied entry silently skips it.
	`ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_status_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_status_check CHECK (status IN ('pending','verified','failed','disabled'));`,
	// Appended AFTER the two-layer entries on purpose: migrations are
	// positional, so an entry inserted below a database's recorded version
	// is silently skipped. New migrations always go at the end.
	// #6867 follow-up — the discount combination rule (DESIGN.md §2.11). One
	// row of billing settings; the rule decides how several percent
	// discounts on one line combine. 'most-specific' is the default; 'stack'
	// is what every statement rated before this migration did.
	`CREATE TABLE IF NOT EXISTS billing_settings (
		id SMALLINT PRIMARY KEY CHECK (id = 1),
		discount_rule TEXT NOT NULL DEFAULT 'most-specific' CHECK (discount_rule IN ('most-specific','highest','stack','compound')),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	);`,
	`INSERT INTO billing_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;`,
	// A stackable discount adds on top of the winner under most-specific and
	// highest (a campaign on top of the contract); it changes nothing under
	// stack or compound.
	`ALTER TABLE discounts ADD COLUMN IF NOT EXISTS stackable BOOLEAN NOT NULL DEFAULT false;`,
	// Every statement records the rule that produced its numbers. Statements
	// rated before the rule existed were summed, so they read 'stack'; ones
	// without a discount breakdown carried no rule-dependent figure.
	`ALTER TABLE statements ADD COLUMN IF NOT EXISTS discount_rule TEXT;`,
	`UPDATE statements SET discount_rule = 'stack' WHERE discount_rule IS NULL AND discount_detail IS NOT NULL;`,
	// Pay per use for flexi Organizations (EPIC #6867, founder direction
	// 2026-09-10). APPENDED AT THE END on purpose: migrations are positional
	// - an entry inserted mid-list is silently skipped on a database that
	// already recorded that version, so a new one only ever goes last.
	paygPlatformBooksMigrationSQL(),
	// #6867 follow-up — post-paid invoicing and the settlement seam (founder
	// direction 2026-09-10). Appended at the very END of the slice because
	// migrations are positional: an entry inserted above a database's
	// recorded version is silently skipped.
	invoicingMigrationSQL,
	// DESIGN.md §9 — the customer account ledger, payment allocation, credit
	// notes, the tax profile, the collections schedule and the platform
	// suspension trail. Appended at the very END: migrations are positional.
	collectionsMigrationSQL,
}

// MigrationPAYGPlatformBooks is the schema_migrations version of the
// pay-per-use migration (the last entry of migrations); the migration test
// stands a database at the version before it and then applies it.
var MigrationPAYGPlatformBooks = func() int {
	// Located by content rather than assumed to be last: another migration
	// appended after it (the invoicing one was) must not move this version.
	want := paygPlatformBooksMigrationSQL()
	for i, m := range migrations {
		if m == want {
			return i + 1
		}
	}
	return len(migrations)
}()

// sqlQuote renders s as a SQL string literal. Every value it is used on here
// is a compile-time constant of this package, never input; it doubles quotes
// so a rate description that later gains an apostrophe cannot break a
// migration.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// paygPlatformBooksMigrationSQL is one transaction that makes the two
// platform rate cards usable as the pair they are:
//
//  1. price_books gains the `description` an operator reads a book's intent
//     from - until now the only place to write that was an item description,
//     which cannot describe a book as a whole.
//  2. "Organization PAYG" is moved to scope = platform. It shipped as a
//     `cloud` book, which is the wrong scope for k8s.* SKUs and made it
//     UNASSIGNABLE: SetSourcePriceBook refuses a book whose scope is not the
//     source's layer, so no flexi Organization could ever have been pointed
//     at it. Moved only while nothing cloud-shaped is assigned to it, so an
//     operator who did put a cloud source on it keeps a consistent book.
//  3. Both books get their derivation as a description, but only where the
//     operator has not written one.
//  4. The pay-per-use book gets the derived rates. Its shipped rates were
//     placeholders an order of magnitude out (k8s.vcpu 0.02589041 per
//     vcpu-hour is 18.90 OMR per vCPU per month, against 2.25 for the same
//     vCPU inside an M plan). They are corrected ONLY while the book rates
//     no source at all: a book that has never billed anyone can hold no
//     operator decision, and one that has is left exactly as it is.
//
// It is built from PAYGBookItems() rather than restating the numbers, so the
// book a migrated Sovereign ends up with and the book a fresh one creates can
// never be two different books.
func paygPlatformBooksMigrationSQL() string {
	var b strings.Builder
	b.WriteString("ALTER TABLE price_books ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '';\n")
	fmt.Fprintf(&b, "UPDATE price_books SET scope = '%s'\n WHERE lower(name) = lower(%s) AND scope <> '%s'\n   AND NOT EXISTS (SELECT 1 FROM cost_sources s WHERE s.price_book_id = price_books.id AND s.layer <> '%s');\n",
		LayerPlatform, sqlQuote(PAYGBookName), LayerPlatform, LayerPlatform)
	for _, d := range []struct{ name, desc string }{{PlanBookName, PlanBookDescription}, {PAYGBookName, PAYGBookDescription}} {
		fmt.Fprintf(&b, "UPDATE price_books SET description = %s WHERE lower(name) = lower(%s) AND description = '';\n", sqlQuote(d.desc), sqlQuote(d.name))
	}
	for _, it := range PAYGBookItems() {
		annual := "NULL"
		if it.AnnualPrice != nil {
			annual = string(*it.AnnualPrice)
		}
		fmt.Fprintf(&b, "INSERT INTO price_items (price_book_id, sku, unit, unit_price, annual_price, description)\n SELECT b.id, %s, %s, %s, %s, %s FROM price_books b\n  WHERE lower(b.name) = lower(%s)\n    AND NOT EXISTS (SELECT 1 FROM cost_sources s WHERE s.price_book_id = b.id)\n ON CONFLICT (price_book_id, sku) DO UPDATE SET unit = EXCLUDED.unit, unit_price = EXCLUDED.unit_price, annual_price = EXCLUDED.annual_price, description = EXCLUDED.description;\n",
			sqlQuote(it.SKU), sqlQuote(it.Unit), string(it.UnitPrice), annual, sqlQuote(it.Description), sqlQuote(PAYGBookName))
	}
	return b.String()
}

// MigrationTwoLayerSources is the schema_migrations version of the two-layer
// source migration (the last entry of migrations). The migration test
// applies every version before it, seeds the pre-change shape, and then
// applies it.
var MigrationTwoLayerSources = func() int {
	for i, m := range migrations {
		if m == twoLayerMigrationSQL {
			return i + 1
		}
	}
	return len(migrations)
}()

// twoLayerMigrationSQL is one transaction: the schema change and the data
// migration described in DESIGN.md §4.1. Every statement is idempotent
// against a database that already carries the shape.
const twoLayerMigrationSQL = `
ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_kind_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_kind_check CHECK (kind IN ('huawei-project','openova-org','openova-platform','k8s-namespace','file'));
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS layer TEXT NOT NULL GENERATED ALWAYS AS (CASE WHEN kind IN ('huawei-project','file') THEN 'cloud' ELSE 'platform' END) STORED;
ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_layer_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_layer_check CHECK (layer IN ('cloud','platform'));
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS price_book_id UUID REFERENCES price_books(id);
ALTER TABLE cost_sources ADD COLUMN IF NOT EXISTS internal BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE cost_sources ALTER COLUMN customer_id DROP NOT NULL;
ALTER TABLE cost_sources DROP CONSTRAINT IF EXISTS cost_sources_internal_check;
ALTER TABLE cost_sources ADD CONSTRAINT cost_sources_internal_check CHECK ((internal AND customer_id IS NULL AND kind = 'openova-platform') OR (NOT internal AND customer_id IS NOT NULL));
CREATE UNIQUE INDEX IF NOT EXISTS cost_sources_internal_idx ON cost_sources (kind, region, project_id) WHERE customer_id IS NULL;
CREATE INDEX IF NOT EXISTS cost_sources_price_book_idx ON cost_sources (price_book_id);
ALTER TABLE usage_records ALTER COLUMN customer_id DROP NOT NULL;
ALTER TABLE price_books ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'cloud';
ALTER TABLE price_books DROP CONSTRAINT IF EXISTS price_books_scope_check;
ALTER TABLE price_books ADD CONSTRAINT price_books_scope_check CHECK (scope IN ('cloud','platform'));

-- The "OpenOva plans" book prices platform SKUs; every other book is a cloud book.
UPDATE price_books SET scope = 'platform' WHERE lower(name) = lower('` + PlanBookName + `');

-- The Sovereign's own Organization was synced as a customer and its
-- openova-org source carried the platform-overhead records. That customer
-- becomes a plain external customer (its cloud sources stay); the source
-- becomes the internal openova-platform source with no customer.
CREATE TEMP TABLE two_layer_landlord ON COMMIT DROP AS
  SELECT DISTINCT s.customer_id
    FROM cost_sources s
   WHERE s.kind = 'openova-org' AND s.customer_id IS NOT NULL
     AND EXISTS (SELECT 1 FROM usage_records u WHERE u.source_id = s.id AND u.labels->>'tier' = 'platform-overhead');
UPDATE usage_records u SET customer_id = NULL
  FROM cost_sources s
 WHERE s.id = u.source_id AND s.kind = 'openova-org' AND s.customer_id IN (SELECT customer_id FROM two_layer_landlord);
UPDATE cost_sources SET kind = 'openova-platform', internal = true, customer_id = NULL, price_book_id = NULL
 WHERE kind = 'openova-org' AND customer_id IN (SELECT customer_id FROM two_layer_landlord);
UPDATE customers SET kind = 'external', org_slug = NULL, plan_slug = '', updated_at = now()
 WHERE id IN (SELECT customer_id FROM two_layer_landlord);

-- The customer's book moves onto each of its sources whose layer matches
-- the book's scope; platform sources still without a book get the plans book.
UPDATE cost_sources s SET price_book_id = c.price_book_id
  FROM customers c JOIN price_books b ON b.id = c.price_book_id
 WHERE s.customer_id = c.id AND s.price_book_id IS NULL AND s.layer = b.scope;
UPDATE cost_sources s SET price_book_id = b.id
  FROM price_books b
 WHERE lower(b.name) = lower('` + PlanBookName + `') AND s.layer = 'platform' AND NOT s.internal AND s.price_book_id IS NULL;
`

// Migrate applies every migration not yet recorded in schema_migrations.
func (s *Store) Migrate(ctx context.Context) error {
	return s.MigrateUpTo(ctx, len(migrations))
}

// MigrateUpTo applies every migration up to and including version that is
// not yet recorded. The migration test uses it to stand a database at the
// shape BEFORE a migration and seed it; the service always calls Migrate.
func (s *Store) MigrateUpTo(ctx context.Context, version int) error {
	if version < 0 || version > len(migrations) {
		return fmt.Errorf("migration version %d out of range 0..%d", version, len(migrations))
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	for i, sqlText := range migrations[:version] {
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
