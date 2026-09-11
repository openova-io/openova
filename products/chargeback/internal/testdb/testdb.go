// Package testdb opens the integration-test database named by
// CHARGEBACK_TEST_DATABASE_URL, migrates it and wipes every table between
// tests. Tests that call Open are skipped when the variable is unset, so the
// default `go test ./...` never needs a database.
package testdb

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// EnvVar names the connection string variable.
const EnvVar = "CHARGEBACK_TEST_DATABASE_URL"

// Open returns a migrated, empty store or skips the test.
func Open(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv(EnvVar)
	if dsn == "" {
		t.Skipf("%s not set; skipping integration test", EnvVar)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open %s: %v", EnvVar, err)
	}
	st := store.New(db)
	if err := st.Migrate(ctx); err != nil {
		db.Close()
		t.Fatalf("migrate: %v", err)
	}
	// Wipe before the test (a previous run may have died mid-way) and again
	// after it, so a shared database never keeps test customers, sources or
	// credentials around for a later run of the service against it.
	wipe(t, db)
	t.Cleanup(func() {
		wipe(t, db)
		db.Close()
	})
	return st
}

// allocation_settings references customers, so TRUNCATE ... CASCADE empties
// it too; the single row is put back with its defaults rather than updated.
// payments cascades from customers, but invoice_sequences has no foreign
// key: without truncating it the gapless per-year invoice counter would carry
// across tests and a numbering assertion would read a leftover.
//
// cost_usage_daily and cost_rollup_state carry no foreign key either (see
// store/costrollup.go for why), so CASCADE cannot reach them: named here, or
// one test's rollup would be read as another test's cost.
//
// notification_deliveries (DESIGN.md §21) carries a NULLABLE customer_id —
// a sign-in code mailed to an operator address belongs to no customer — so a
// cascade from customers leaves exactly those rows behind. Named here, or
// one test's failed delivery would be read as another test's.
const wipeSQL = `TRUNCATE TABLE audit_log, notification_deliveries, notification_preferences, sessions, pins, invites, rated_lines, cost_usage_daily, cost_rollup_state, invoice_allocations, account_entries, credit_notes, credit_note_sequences, payment_intents, collection_reminders, customer_suspensions, payments, commercial_outbox, statements, invoice_sequences, usage_records, resource_inventory, cost_sources, credentials, role_bindings, group_role_mappings, cost_centre_resources, cost_centre_rules, cost_centres, discounts, budgets, budget_alerts, saved_views, report_deliveries, report_schedules, currency_rates, customers, price_items, price_books, capacity_pool_history, capacity_pools, sku_caps, capacity_zones, capacity_regions, estimates, contract_items, contracts, partner_retail_rules, partners, partner_tiers, einvoice_documents, tax_rules, tax_categories, finance_journal_lines, finance_periods, finance_reconciliation_lines, finance_reconciliations, account_mappings RESTART IDENTITY CASCADE;
DELETE FROM sku_footprints;
INSERT INTO allocation_settings (id, weights, overhead_policy, pool, manual_amount, currency, sovereign_customer_id)
VALUES (1, '{"vcpu":1,"mem_gib":1,"pvc_gb":1}'::jsonb, 'separate', 'sovereign-cost', 0, 'OMR', NULL)
ON CONFLICT (id) DO UPDATE SET weights = EXCLUDED.weights, overhead_policy = EXCLUDED.overhead_policy, pool = EXCLUDED.pool, manual_amount = EXCLUDED.manual_amount, currency = EXCLUDED.currency, sovereign_customer_id = NULL, updated_at = now();
INSERT INTO billing_settings (id) VALUES (1)
ON CONFLICT (id) DO UPDATE SET discount_rule = DEFAULT, invoice_prefix = DEFAULT, commercial_provider = DEFAULT, external_ingest = DEFAULT, tax_rate = DEFAULT, tax_registration_number = DEFAULT, legal_name = DEFAULT, address = DEFAULT,
  credit_note_prefix = DEFAULT, reminder_days = DEFAULT, escalation_days = DEFAULT, escalation_action = DEFAULT, public_price_book_id = DEFAULT, tax_country = DEFAULT, updated_at = now()`

func wipe(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, wipeSQL); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// sku_footprints is seeded by the capacity migration; the wipe above
	// emptied it, so the seed rows go back and every test starts from them.
	if _, err := db.ExecContext(ctx, store.CapacityFootprintSeedSQL()); err != nil {
		t.Fatalf("reseed footprints: %v", err)
	}
	// account_mappings is seeded by the finance migration (DESIGN.md §18) and
	// the wipe emptied it; the defaults go back so every test starts from the
	// chart of accounts a fresh Sovereign has.
	if _, err := db.ExecContext(ctx, store.AccountMappingSeedSQL()); err != nil {
		t.Fatalf("reseed account mappings: %v", err)
	}
}
