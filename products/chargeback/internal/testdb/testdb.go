// Package testdb opens the integration-test database named by
// CHARGEBACK_TEST_DATABASE_URL, migrates it and wipes every table between
// tests. Tests that call Open are skipped when the variable is unset, so the
// default `go test ./...` never needs a database.
package testdb

import (
	"context"
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
	t.Cleanup(func() { db.Close() })
	st := store.New(db)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE TABLE audit_log, sessions, pins, invites, rated_lines, statements, usage_records, resource_inventory, cost_sources, credentials, customer_users, customers, price_items, price_books RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return st
}
