package main

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/synth"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// The repair moves a database an earlier run left on a duplicate rate card.
// Getting it wrong in either direction is expensive — leaving showcase rows on
// the wrong rates keeps the defect, deleting the wrong book destroys an
// operator's own numbers — so both directions are exercised here against a
// real schema rather than a mock.

func openDB(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	st := testdb.Open(t) // skips the test when no database is configured
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Open(ctx, os.Getenv(testdb.EnvVar))
	if err != nil {
		t.Fatalf("open a second handle: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return st, db
}

func mustExec(t *testing.T, db *sql.DB, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func scalar[T any](t *testing.T, db *sql.DB, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := db.QueryRow(sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// fixture: the operator's own card and the duplicate an earlier run made, a
// showcase customer on the duplicate, and a REAL customer on the duplicate too
// (the case where the repair must refuse to delete).
func repairFixture(t *testing.T, db *sql.DB) (operatorBookID, dupBookID string) {
	t.Helper()
	operatorBookID = scalar[string](t, db,
		`INSERT INTO price_books (name, scope, currency) VALUES ('National Cloud 2026 list', 'cloud', 'OMR') RETURNING id`)
	dupBookID = scalar[string](t, db,
		`INSERT INTO price_books (name, scope, currency) VALUES ($1, 'cloud', 'OMR') RETURNING id`, synth.CloudBookName)
	return
}

func addCustomer(t *testing.T, db *sql.DB, slug, bookID string) string {
	t.Helper()
	return scalar[string](t, db,
		`INSERT INTO customers (slug, name, admin_email, kind, billing_mode, status, price_book_id)
		 VALUES ($1, $1, 'x@example.invalid', 'external', 'chargeback', 'active', $2) RETURNING id`, slug, bookID)
}

func addSource(t *testing.T, db *sql.DB, customerID, projectID, bookID string) string {
	t.Helper()
	return scalar[string](t, db,
		`INSERT INTO cost_sources (customer_id, kind, region, project_id, status, price_book_id)
		 VALUES ($1, 'file', 'me-east-215-a', $2, 'verified', $3) RETURNING id`, customerID, projectID, bookID)
}

// TestRepointShowcaseMovesOnlyTheShowcase — the showcase's sources and
// customers move onto the operator's card; a real customer's source is left
// exactly where the operator put it.
func TestRepointShowcaseMovesOnlyTheShowcase(t *testing.T) {
	_, db := openDB(t)
	operatorBookID, dupBookID := repairFixture(t, db)

	showcase := addCustomer(t, db, synth.SlugPrefix+"gulf-retail", dupBookID)
	showcaseSrc := addSource(t, db, showcase, synth.SlugPrefix+"gulf-retail", dupBookID)
	real := addCustomer(t, db, "hw307-omani-works", dupBookID)
	realSrc := addSource(t, db, real, "0a1b2c3d4e5f60718293a4b5c6d7e8f9", dupBookID)

	// A bill the showcase customer already has, rated on the wrong card.
	stmt := scalar[string](t, db,
		`INSERT INTO statements (customer_id, period_start, period_end, status, total)
		 VALUES ($1, '2026-06-01', '2026-07-01', 'issued', 1417.68) RETURNING id`, showcase)
	mustExec(t, db, `INSERT INTO rated_lines (statement_id, customer_id, source_id, sku, quantity, unit, unit_price, amount)
		 VALUES ($1, $2, $3, 'nat.1', 720, 'hour', 0.11322489, 81.52)`, stmt, showcase, showcaseSrc)

	s := &seeder{db: db, ctx: context.Background()}
	moved, rerate, err := s.repointShowcase(operatorBookID)
	if err != nil {
		t.Fatalf("repoint: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved %d sources, want the one showcase source", moved)
	}
	if !rerate {
		t.Fatal("the rate card changed and the run did not flag a re-rate; the summary would report figures nobody can reproduce")
	}
	if got := scalar[string](t, db, `SELECT price_book_id FROM cost_sources WHERE id = $1`, showcaseSrc); got != operatorBookID {
		t.Errorf("the showcase source is still on %s, want the operator's card", got)
	}
	if got := scalar[string](t, db, `SELECT price_book_id FROM cost_sources WHERE id = $1`, realSrc); got != dupBookID {
		t.Errorf("a REAL customer's source was moved to %s; the repair may only touch the showcase", got)
	}
	if got := scalar[string](t, db, `SELECT price_book_id FROM customers WHERE id = $1`, showcase); got != operatorBookID {
		t.Errorf("the showcase customer still references %s", got)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM statements WHERE customer_id = $1`, showcase); n != 0 {
		t.Errorf("%d showcase statement(s) survived; a bill rated on the old card would stand as a number nothing reproduces", n)
	}
	if n := scalar[int](t, db, `SELECT count(*) FROM rated_lines WHERE statement_id = $1`, stmt); n != 0 {
		t.Errorf("%d rated line(s) of the dropped statement survived", n)
	}

	// Idempotent: a second pass has nothing to move and must not claim a
	// re-rate, or every run would tell the operator the figures changed.
	moved, rerate, err = s.repointShowcase(operatorBookID)
	if err != nil {
		t.Fatalf("second repoint: %v", err)
	}
	if moved != 0 || rerate {
		t.Fatalf("a second pass moved %d source(s) and flagged rerate=%v", moved, rerate)
	}
}

// TestBookUsageRefusesTheDeleteWhileAnythingRealIsAttached — the guard on the
// delete, exercised one attachment at a time.
func TestBookUsageRefusesTheDeleteWhileAnythingRealIsAttached(t *testing.T) {
	_, db := openDB(t)
	operatorBookID, dupBookID := repairFixture(t, db)
	ctx := context.Background()

	real := addCustomer(t, db, "hw307-omani-works", operatorBookID)
	realSrc := addSource(t, db, real, "0a1b2c3d4e5f60718293a4b5c6d7e8f9", dupBookID)

	use, err := bookUsage(ctx, db, dupBookID)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if !use.inUse() || use.Sources != 1 {
		t.Fatalf("a non-synthetic source is attached and the book reads as unused: %+v", use)
	}

	// Detach the source; now the CUSTOMER column keeps it alive.
	mustExec(t, db, `UPDATE cost_sources SET price_book_id = $1 WHERE id = $2`, operatorBookID, realSrc)
	mustExec(t, db, `UPDATE customers SET price_book_id = $1 WHERE id = $2`, dupBookID, real)
	if use, _ = bookUsage(ctx, db, dupBookID); !use.inUse() || use.Customers != 1 {
		t.Fatalf("a customer still references the book and it reads as unused: %+v", use)
	}

	// Detach the customer; now an ISSUED statement rated through a source on
	// the book keeps it alive.
	mustExec(t, db, `UPDATE customers SET price_book_id = $1 WHERE id = $2`, operatorBookID, real)
	mustExec(t, db, `UPDATE cost_sources SET price_book_id = $1 WHERE id = $2`, dupBookID, realSrc)
	stmt := scalar[string](t, db,
		`INSERT INTO statements (customer_id, period_start, period_end, status, total)
		 VALUES ($1, '2026-06-01', '2026-07-01', 'issued', 594.10) RETURNING id`, real)
	mustExec(t, db, `INSERT INTO rated_lines (statement_id, customer_id, source_id, sku, quantity, unit, unit_price, amount)
		 VALUES ($1, $2, $3, 'nat.1', 720, 'hour', 0.06037935, 43.47)`, stmt, real, realSrc)
	if use, _ = bookUsage(ctx, db, dupBookID); !use.inUse() || use.IssuedStatements != 1 {
		t.Fatalf("an issued statement was rated through this book and it reads as unused: %+v", use)
	}

	// Only with every one of them gone does the book become removable.
	mustExec(t, db, `DELETE FROM statements WHERE id = $1`, stmt)
	mustExec(t, db, `UPDATE cost_sources SET price_book_id = $1 WHERE id = $2`, operatorBookID, realSrc)
	if use, _ = bookUsage(ctx, db, dupBookID); use.inUse() {
		t.Fatalf("nothing is attached and the book still reads as in use: %+v", use)
	}
}

// TestDuplicateBooksAreNeverTheOperatorsOwn — the delete candidate list is
// built from the NAME, and only names this tool is known to write.
func TestDuplicateBooksAreNeverTheOperatorsOwn(t *testing.T) {
	_, db := openDB(t)
	operatorBookID, dupBookID := repairFixture(t, db)
	books := []apiPriceBook{
		{ID: operatorBookID, Name: "National Cloud 2026 list", Scope: "cloud"},
		{ID: dupBookID, Name: synth.CloudBookName, Scope: "cloud"},
	}
	got := duplicateCloudBooks(books, operatorBookID)
	if len(got) != 1 || got[0].ID != dupBookID {
		t.Fatalf("delete candidates %v; want exactly the card seed-history made", got)
	}
	// And with the operator's card resolved away, its own book is still safe.
	for _, b := range duplicateCloudBooks(books, dupBookID) {
		if b.ID == operatorBookID {
			t.Fatal("the operator's own rate card was marked for deletion")
		}
	}
}
