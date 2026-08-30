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

const wipeSQL = `TRUNCATE TABLE audit_log, sessions, pins, invites, rated_lines, statements, usage_records, resource_inventory, cost_sources, credentials, customer_users, customers, price_items, price_books RESTART IDENTITY CASCADE`

func wipe(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, wipeSQL); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}
