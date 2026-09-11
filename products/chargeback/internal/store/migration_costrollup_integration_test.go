package store_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The rollup migration (DESIGN.md §20.4). A Sovereign this is deployed over
// already holds usage the invalidation triggers never saw, and the whole
// safety of the feature rests on that usage being marked STALE rather than
// assumed absent: a (source, day) with records but no state row would be
// read as a covered day holding nothing, and the Overview would quietly
// under-count.
//
// So this test stands a database at the shape BEFORE the migration, writes a
// ledger the triggers could not have seen, applies the migration, and asserts
// the backfill marked every partition and that nothing is served from the
// rollup until it has actually been built.

func openPreRollup(t *testing.T) *sql.DB {
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
	// A private schema, like the other migration tests: the pre-migration
	// shape must never touch the public one every other test runs against.
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS rollup_migration CASCADE`,
		`CREATE SCHEMA rollup_migration`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.ExecContext(ctx, `SET search_path = rollup_migration, public`); err != nil {
		t.Fatalf("search_path: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = db.ExecContext(c, `DROP SCHEMA IF EXISTS rollup_migration CASCADE`)
	})
	return db
}

func TestIntegrationRollupMigrationMarksEveryExistingDay(t *testing.T) {
	db := openPreRollup(t)
	// One connection only: the shape lives in a session-local search_path.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	st := store.New(db)

	// 1. The schema as it was BEFORE the rollup migration: no tables, no
	// triggers, so the ledger written below is invisible to the invalidation.
	if err := st.MigrateUpTo(ctx, store.MigrationCostRollup-1); err != nil {
		t.Fatalf("migrate to the pre-change shape: %v", err)
	}
	exists := func(kind, name string) bool {
		var ok bool
		var q string
		switch kind {
		case "table":
			q = `SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'rollup_migration' AND table_name = $1)`
		default:
			q = `SELECT EXISTS (SELECT 1 FROM information_schema.triggers WHERE trigger_schema = 'rollup_migration' AND trigger_name = $1)`
		}
		if err := db.QueryRowContext(ctx, q, name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if exists("table", "cost_usage_daily") || exists("table", "cost_rollup_state") {
		t.Fatal("the rollup tables exist before the migration that creates them")
	}

	// 2. A ledger the triggers could not have marked: two sources, three days.
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760, BillStopped: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: "eip", Unit: "hour", UnitPrice: "0.02"}}, true); err != nil {
		t.Fatal(err)
	}
	cust, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := json.Marshal(map[string]any{"name": "1.2.3.4"})
	var recs []store.UsageRecord
	for s := range 2 {
		src, _, err := st.UpsertSource(ctx, cust.ID, "huawei-project", "me-east-1", "proj-"+string(rune('a'+s)))
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetSourcePriceBook(ctx, src.ID, book.ID); err != nil {
			t.Fatal(err)
		}
		for d := 1; d <= 3; d++ {
			for h := range 24 {
				at := day(2026, 9, d).Add(time.Duration(h) * time.Hour)
				recs = append(recs, store.UsageRecord{CustomerID: cust.ID, SourceID: src.ID, ResourceID: "eip-1",
					ResourceKind: "eip", SKU: "eip", Quantity: "1.000000", Unit: "hour",
					WindowStart: at, WindowEnd: at.Add(time.Hour), Region: "me-east-1", Labels: lb})
			}
		}
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}
	before, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 4), Granularity: "day", GroupBy: "none"})
	if err != nil {
		t.Fatal(err)
	}

	// 3. Apply it.
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, tr := range []string{"usage_records_rollup_insert", "usage_records_rollup_update_new", "usage_records_rollup_update_old", "usage_records_rollup_delete"} {
		if !exists("trigger", tr) {
			t.Fatalf("the migration did not create %s", tr)
		}
	}

	// 4. Every (source, day) of the pre-existing ledger is marked, and marked
	// STALE: 2 sources x 3 days, none of them built.
	parts, err := st.StaleCostRollupPartitions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 6 {
		t.Fatalf("the backfill marked %d partitions, want 6: %+v", len(parts), parts)
	}
	var rollupRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM cost_usage_daily`).Scan(&rollupRows); err != nil {
		t.Fatal(err)
	}
	if rollupRows != 0 {
		t.Fatalf("the migration built %d rollup rows; it must only MARK, so nothing is ever read from a partition that was never built", rollupRows)
	}

	// 5. The figure is unchanged across the migration — the whole window is
	// still served from the ledger — and unchanged again once built.
	after, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 4), Granularity: "day", GroupBy: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Total.Current != before.Total.Current {
		t.Fatalf("the migration moved the total: %s then %s", before.Total.Current, after.Total.Current)
	}
	built := buildRollup(t, st)
	if built != 6 {
		t.Fatalf("built %d partitions, want 6", built)
	}
	rolled, err := st.Explore(ctx, store.OperatorScope, store.CostQuery{From: day(2026, 9, 1), To: day(2026, 9, 4), Granularity: "day", GroupBy: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if rolled.Total.Current != before.Total.Current {
		t.Fatalf("the built rollup moved the total: %s then %s", before.Total.Current, rolled.Total.Current)
	}

	// 6. And the triggers are live now: a further write marks its own day.
	if _, err := st.DeleteUsageInRange(ctx, parts[0].SourceID, "eip-1", parts[0].Day, parts[0].Day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	again, err := st.StaleCostRollupPartitions(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].SourceID != parts[0].SourceID || !again[0].Day.Equal(parts[0].Day) {
		t.Fatalf("a delete after the migration marked %+v", again)
	}
}
