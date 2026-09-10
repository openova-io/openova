package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/openova-io/openova/products/chargeback/internal/synth"
)

// Repairing a database an earlier run already put a duplicate rate card on
// (founder, hw307, 2026-09-10). Resolving the right book from now on fixes
// tomorrow; hw307 also needs yesterday moved.
//
// Two halves, deliberately apart:
//
//   - repointShowcase runs BEFORE the statements, because moving a source onto
//     another rate card changes what its usage costs, and a bill that was
//     rated on the old card is now wrong. The showcase's own statements are
//     therefore dropped so the run regenerates them. They are synthetic bills
//     for customers that never existed; a REAL customer's issued statement is
//     a financial record and is never touched.
//   - dropDuplicateBooks runs AFTER everything, when the only rows that could
//     still point at the duplicate are rows this tool does not own.

// repointShowcase moves every showcase cloud source and showcase customer onto
// the resolved book and drops the showcase statements rated on the old one.
// moved is how many sources changed card; rerate says the summary must warn
// that historical figures shift.
func (s *seeder) repointShowcase(canonicalID string) (moved int64, rerate bool, err error) {
	if canonicalID == "" {
		return 0, false, nil
	}
	const showcase = `SELECT id FROM customers WHERE ` + synth.SQLCustomerPredicate

	// Count first: on a fresh database nothing is assigned yet and there is
	// nothing to warn about.
	var wrong int64
	if err := s.db.QueryRowContext(s.ctx,
		`SELECT count(*) FROM cost_sources
		  WHERE layer = 'cloud' AND NOT internal
		    AND customer_id IN (`+showcase+`)
		    AND price_book_id IS DISTINCT FROM $1`, canonicalID).Scan(&wrong); err != nil {
		return 0, false, fmt.Errorf("count showcase sources on the wrong rate card: %w", err)
	}
	if wrong == 0 {
		return 0, false, nil
	}

	res, err := s.db.ExecContext(s.ctx,
		`UPDATE cost_sources SET price_book_id = $1
		  WHERE layer = 'cloud' AND NOT internal
		    AND customer_id IN (`+showcase+`)
		    AND price_book_id IS DISTINCT FROM $1`, canonicalID)
	if err != nil {
		return 0, false, fmt.Errorf("re-point showcase sources: %w", err)
	}
	moved, _ = res.RowsAffected()

	// customers.price_book_id is the deprecated pre-two-layer column, still
	// read by older builds; leaving it on the duplicate would both mis-rate
	// there and block the delete below.
	if _, err := s.db.ExecContext(s.ctx,
		`UPDATE customers SET price_book_id = $1
		  WHERE `+synth.SQLCustomerPredicate+` AND price_book_id IS DISTINCT FROM $1`, canonicalID); err != nil {
		return moved, false, fmt.Errorf("re-point showcase customers: %w", err)
	}

	// The bills rated on the old card. Deleting cascades to rated_lines.
	dropped, err := s.db.ExecContext(s.ctx,
		`DELETE FROM statements WHERE customer_id IN (`+showcase+`)`)
	if err != nil {
		return moved, false, fmt.Errorf("drop showcase statements rated on the old card: %w", err)
	}
	n, _ := dropped.RowsAffected()
	s.infof("rate card changed: %d showcase source(s) re-pointed, %d showcase statement(s) dropped and about to be re-rated", moved, n)
	return moved, true, nil
}

// dropDuplicateBooks removes the cards THIS TOOL made that are not the
// resolved one.
//
// The delete is guarded three ways and refuses loudly rather than forcing:
// a source that is not one of ours still assigned, a customer still pointing
// at it, or an ISSUED statement whose lines came from a source on it. The
// product's own DELETE /pricebooks/{id} refuses on assigned sources too, so
// the last word belongs to the product, not to this command.
func (s *seeder) dropDuplicateBooks() error {
	dups := duplicateCloudBooks(s.priceBooks, s.cloudBook.ID)
	for _, b := range dups {
		use, err := bookUsage(s.ctx, s.db, b.ID)
		if err != nil {
			return fmt.Errorf("check what still uses %q: %w", b.Name, err)
		}
		if use.inUse() {
			s.infof("rate card %q (%s) left in place: %s — a book in use is never removed",
				b.Name, shortID(b.ID), use)
			continue
		}
		if err := s.api.deletePriceBook(b.ID); err != nil {
			// A 409 is the product's own assigned-sources guard; report it
			// and move on rather than fight it.
			s.infof("rate card %q (%s) left in place: %v", b.Name, shortID(b.ID), err)
			continue
		}
		s.infof("rate card %q (%s) removed: an earlier seed-history run made it, nothing uses it, and %q is the card in force",
			b.Name, shortID(b.ID), s.cloudBook.Name)
	}
	return nil
}

// bookUse counts everything that would still be priced by a rate card. It is
// the whole guard on the delete: a book with any of these is somebody's, not
// a leftover.
type bookUse struct {
	Sources          int64
	Customers        int64
	IssuedStatements int64
}

func (u bookUse) inUse() bool {
	return u.Sources > 0 || u.Customers > 0 || u.IssuedStatements > 0
}

func (u bookUse) String() string {
	return fmt.Sprintf("%d source(s), %d customer(s) and %d issued statement(s) still use it",
		u.Sources, u.Customers, u.IssuedStatements)
}

// bookUsage reads those counts. Sources and customers are counted whoever
// they belong to — by the time this runs, every row this tool owns has been
// re-pointed, so anything left is somebody else's and stops the delete. An
// issued statement is counted through the sources its lines came from, which
// is the only link the schema has between a bill and the card that rated it.
func bookUsage(ctx context.Context, db *sql.DB, bookID string) (bookUse, error) {
	var u bookUse
	err := db.QueryRowContext(ctx, `SELECT
		 (SELECT count(*) FROM cost_sources WHERE price_book_id = $1),
		 (SELECT count(*) FROM customers    WHERE price_book_id = $1),
		 (SELECT count(*) FROM statements st WHERE st.status = 'issued'
		    AND EXISTS (SELECT 1 FROM rated_lines rl
		                  JOIN cost_sources cs ON cs.id = rl.source_id
		                 WHERE rl.statement_id = st.id AND cs.price_book_id = $1))`,
		bookID).Scan(&u.Sources, &u.Customers, &u.IssuedStatements)
	return u, err
}
