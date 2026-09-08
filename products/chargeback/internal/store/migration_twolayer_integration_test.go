package store_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The two-layer migration (DESIGN.md §4.1). This test stands a database at
// the shape BEFORE the migration, writes the rows the previous model wrote —
// customers carrying a price_book_id, the Sovereign's own Organization as a
// customer with an openova-org source holding platform-overhead usage — and
// then applies the migration and asserts every clause of it.
//
// It cannot pass on the old code: the columns it asserts do not exist there.

func openUnmigrated(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv(testdb.EnvVar)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", testdb.EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// A private schema so the pre-migration shape never touches the shared
	// public one: every other integration test runs against public.
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS twolayer_migration CASCADE`,
		`CREATE SCHEMA twolayer_migration`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, `SET search_path = twolayer_migration, public`); err != nil {
		t.Fatalf("search_path: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.ExecContext(c, `DROP SCHEMA IF EXISTS twolayer_migration CASCADE`)
	})
	return db
}

func TestIntegrationTwoLayerMigrationMovesBooksOntoSources(t *testing.T) {
	db := openUnmigrated(t)
	// One connection only: the shape lives in a session-local search_path.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	st := store.New(db)

	// 1. The schema as it was BEFORE the two-layer migration.
	if err := st.MigrateUpTo(ctx, store.MigrationTwoLayerSources-1); err != nil {
		t.Fatalf("migrate to the pre-change shape: %v", err)
	}
	var hasLayer bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'twolayer_migration' AND table_name = 'cost_sources' AND column_name = 'layer')`).Scan(&hasLayer); err != nil {
		t.Fatal(err)
	}
	if hasLayer {
		t.Fatal("cost_sources.layer already exists before the migration under test — the test proves nothing")
	}

	// 2. The rows the previous model wrote.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var cloudBook, planBook, landlord, tenant string
	if err := db.QueryRowContext(ctx, `INSERT INTO price_books (name, currency, annual_divisor, bill_stopped) VALUES ('NC list', 'OMR', 8760, 'compute') RETURNING id`).Scan(&cloudBook); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO price_books (name, currency, annual_divisor, bill_stopped) VALUES ($1, 'OMR', 8760, 'compute') RETURNING id`, store.PlanBookName).Scan(&planBook); err != nil {
		t.Fatal(err)
	}
	// The landlord: the Sovereign's own Organization, synced as a customer,
	// holding two Huawei projects AND an openova-org source whose k8s rows
	// showed as "unpriced" under the cloud book — exactly the hw307 shape.
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (slug, name, admin_email, kind, org_slug, price_book_id, billing_mode, status, plan_slug)
		VALUES ('hw307-omani-works', 'Omantel', 'ops@omantel.example', 'organization', 'hw307-omani-works', $1, 'showback', 'active', 's') RETURNING id`, cloudBook).Scan(&landlord); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `INSERT INTO customers (slug, name, admin_email, kind, org_slug, price_book_id, billing_mode, status, plan_slug)
		VALUES ('acme', 'Acme', 'a@acme.example', 'organization', 'acme', $1, 'chargeback', 'active', 'm') RETURNING id`, planBook).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	newSource := func(customerID, kind, region, project string) string {
		t.Helper()
		var id string
		if err := db.QueryRowContext(ctx, `INSERT INTO cost_sources (customer_id, kind, region, project_id, status) VALUES ($1, $2, $3, $4, 'verified') RETURNING id`,
			customerID, kind, region, project).Scan(&id); err != nil {
			t.Fatalf("insert source %s/%s: %v", kind, project, err)
		}
		return id
	}
	landlordCloudA := newSource(landlord, "huawei-project", "me-east-215-a", "proj-a")
	landlordCloudB := newSource(landlord, "huawei-project", "me-east-215-b", "proj-b")
	landlordOrg := newSource(landlord, "openova-org", "", "hw307-omani-works")
	tenantOrg := newSource(tenant, "openova-org", "", "acme")
	tenantCloud := newSource(tenant, "huawei-project", "me-east-215-a", "proj-acme")

	at := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	exec(`INSERT INTO usage_records (customer_id, source_id, resource_id, resource_kind, sku, quantity, unit, window_start, window_end, labels)
		VALUES ($1, $2, 'gitea/pod-1', 'k8s-pod', 'k8s.vcpu', 4, 'vcpu-hour', $3, $4, '{"tier":"platform-overhead","namespace":"gitea"}'::jsonb)`,
		landlord, landlordOrg, at, at.Add(time.Hour))
	exec(`INSERT INTO usage_records (customer_id, source_id, resource_id, resource_kind, sku, quantity, unit, window_start, window_end, labels)
		VALUES ($1, $2, 'vm-1', 'ecs', 'ecs.m7n.xlarge.8', 1, 'instance-hour', $3, $4, '{"status":"ACTIVE"}'::jsonb)`,
		landlord, landlordCloudA, at, at.Add(time.Hour))
	exec(`INSERT INTO usage_records (customer_id, source_id, resource_id, resource_kind, sku, quantity, unit, window_start, window_end, labels)
		VALUES ($1, $2, 'plan/m', 'plan', 'plan.m', 1, 'plan-hour', $3, $4, '{"plan":"m"}'::jsonb)`,
		tenant, tenantOrg, at, at.Add(time.Hour))

	// 3. The migration under test.
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("apply the two-layer migration: %v", err)
	}

	// 4a. Layers are derived from the kind.
	layers := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT id::text, layer FROM cost_sources`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, layer string
		if err := rows.Scan(&id, &layer); err != nil {
			t.Fatal(err)
		}
		layers[id] = layer
	}
	rows.Close()
	for id, want := range map[string]string{
		landlordCloudA: store.LayerCloud, landlordCloudB: store.LayerCloud, tenantCloud: store.LayerCloud,
		landlordOrg: store.LayerPlatform, tenantOrg: store.LayerPlatform,
	} {
		if layers[id] != want {
			t.Fatalf("source %s layer = %q, want %q", id, layers[id], want)
		}
	}

	// 4b. The plans book is platform-scoped; every other book is cloud.
	scopes := map[string]string{}
	rows, err = db.QueryContext(ctx, `SELECT id::text, scope FROM price_books`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, scope string
		if err := rows.Scan(&id, &scope); err != nil {
			t.Fatal(err)
		}
		scopes[id] = scope
	}
	rows.Close()
	if scopes[planBook] != store.LayerPlatform || scopes[cloudBook] != store.LayerCloud {
		t.Fatalf("book scopes = %v", scopes)
	}

	// 4c. The Sovereign's own Organization is no longer a customer of the
	// platform: it is a plain external customer, its cloud sources still
	// its own, and its openova-org source is now THE internal source with
	// no customer — the mixing the founder rejected is gone.
	var kind, planSlug string
	var orgSlug sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT kind, org_slug, plan_slug FROM customers WHERE id = $1`, landlord).Scan(&kind, &orgSlug, &planSlug); err != nil {
		t.Fatal(err)
	}
	if kind != "external" || orgSlug.Valid || planSlug != "" {
		t.Fatalf("landlord customer after migration = kind %q org_slug %v plan %q", kind, orgSlug, planSlug)
	}
	var srcKind string
	var internal bool
	var srcCustomer, srcBook sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT kind, internal, customer_id, price_book_id FROM cost_sources WHERE id = $1`, landlordOrg).Scan(&srcKind, &internal, &srcCustomer, &srcBook); err != nil {
		t.Fatal(err)
	}
	if srcKind != store.SourceKindPlatform || !internal || srcCustomer.Valid || srcBook.Valid {
		t.Fatalf("the landlord's openova-org source = kind %q internal=%v customer %v book %v", srcKind, internal, srcCustomer, srcBook)
	}
	// Its overhead usage moved with it: no customer any more.
	var overheadCustomer sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT customer_id FROM usage_records WHERE source_id = $1`, landlordOrg).Scan(&overheadCustomer); err != nil {
		t.Fatal(err)
	}
	if overheadCustomer.Valid {
		t.Fatalf("platform-overhead usage still attributed to a customer: %v", overheadCustomer)
	}

	// 4d. The customer's book moved onto the sources whose layer matches its
	// scope; platform sources without one got the plans book.
	books := map[string]string{}
	rows, err = db.QueryContext(ctx, `SELECT id::text, COALESCE(price_book_id::text, '') FROM cost_sources`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, book string
		if err := rows.Scan(&id, &book); err != nil {
			t.Fatal(err)
		}
		books[id] = book
	}
	rows.Close()
	if books[landlordCloudA] != cloudBook || books[landlordCloudB] != cloudBook {
		t.Fatalf("the landlord's cloud sources = %v, want the cloud book %s", books, cloudBook)
	}
	if books[tenantOrg] != planBook {
		t.Fatalf("the Organization's platform source = %q, want the plans book %s", books[tenantOrg], planBook)
	}
	// The tenant's book was the PLATFORM plans book, so it could not move
	// onto its cloud source; that source stays bookless rather than being
	// rated by a book of the wrong layer.
	if books[tenantCloud] != "" {
		t.Fatalf("a platform book was put on a cloud source: %q", books[tenantCloud])
	}

	// 4e. customers.price_book_id survives as a deprecated column: the
	// migration reads it and never clears it, so a rollback loses nothing.
	var kept sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT price_book_id FROM customers WHERE id = $1`, tenant).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if !kept.Valid || kept.String != planBook {
		t.Fatalf("customers.price_book_id = %v, want the deprecated value kept", kept)
	}

	// 4f. The migration is idempotent.
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}
