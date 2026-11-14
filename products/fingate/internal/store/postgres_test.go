package store_test

// The PostgreSQL store passes the same port tests as the in-memory store.
//
// The tests need a database: FINGATE_TEST_DATABASE_URL is its connection address, for a user
// that may create schemas. Without it each test is skipped; the job of the workflow fingate-ci
// that provides a database fails on a skipped test. Every store opened here lives in a schema of
// its own, which the test drops at its end.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/openova-io/openova/products/fingate/internal/consent"
	"github.com/openova-io/openova/products/fingate/internal/store"
	"github.com/openova-io/openova/products/fingate/internal/store/storetest"
)

// DatabaseEnv names the variable that holds the test database's connection address.
const DatabaseEnv = "FINGATE_TEST_DATABASE_URL"

// schemaURL creates an empty schema and returns the test database's address with that schema as
// its search path. The schema is dropped when the test ends.
func schemaURL(t *testing.T) string {
	t.Helper()
	base := os.Getenv(DatabaseEnv)
	if base == "" {
		t.Skipf("%s is not set: this test needs a PostgreSQL database", DatabaseEnv)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to %s: %v", DatabaseEnv, err)
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "fingate_test_" + hex.EncodeToString(b)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		_ = admin.Close(ctx)
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("%s is not an address: %v", DatabaseEnv, err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

func openPostgres(t *testing.T, now func() time.Time) *store.Postgres {
	t.Helper()
	s, err := store.OpenPostgres(context.Background(), schemaURL(t), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPostgresPassesThePortTests(t *testing.T) {
	if os.Getenv(DatabaseEnv) == "" {
		t.Skipf("%s is not set: this test needs a PostgreSQL database", DatabaseEnv)
	}
	storetest.Run(t, func(t *testing.T, now func() time.Time) store.Store { return openPostgres(t, now) })
}

// The migrations create the two consent tables and the record of migrations, nothing else, and
// a second start applies nothing again.
func TestPostgresMigrationsCreateOnlyTheConsentTables(t *testing.T) {
	u := schemaURL(t)
	ctx := context.Background()
	for range 2 {
		s, err := store.OpenPostgres(ctx, u, nil)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	conn, err := pgx.Connect(ctx, u)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, _ := conn.Query(ctx, `SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() ORDER BY table_name`)
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"consent", "consent_history", "schema_migrations"}; !slices.Equal(tables, want) {
		t.Fatalf("tables %v, want %v", tables, want)
	}
	rows, _ = conn.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	applied, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		t.Fatal(err)
	}
	want, err := store.Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(applied, want) || !slices.Equal(want, []int{1}) {
		t.Fatalf("applied %v, the package holds %v, want [1]", applied, want)
	}
}

// Two reads of an expired consent at the same time record the expiry once.
func TestPostgresExpiresOnceUnderConcurrentReads(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := t0
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	s := openPostgres(t, clock)
	ctx := context.Background()
	if _, err := s.Create(ctx, store.Consent{ID: "c1", Family: consent.AccountAccess, ClientID: "acme-pay.wallet",
		Request: []byte(`{}`), ExpiresAt: t0.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authorise(ctx, "c1", "cust-1", []string{"acc-1"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	now = t0.Add(2 * time.Hour)
	mu.Unlock()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, ok, err := s.Get(ctx, "c1")
			if err == nil && (!ok || c.State != consent.Expired) {
				err = errors.New("a read did not see the consent expired")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, _, _ := s.Get(ctx, "c1")
	if len(c.History) != 3 || c.History[2].To != consent.Expired || c.History[2].Actor != consent.System {
		t.Fatalf("history %+v, want one expiry entry after creation and authorisation", c.History)
	}
}
