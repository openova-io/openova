package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A price book's effective date is the first day its prices apply (the
// console: "leave empty to apply to every period"). A period that starts
// before it is not rated at that book's prices: the run reports the customer
// with the reason and writes no statement. On main the date was accepted,
// stored and shown, and the run rated August at a book in force from 2
// August — every line at prices not yet in force.
func TestIntegrationBookNotInForceIsNotRated(t *testing.T) {
	st := testdb.Open(t)
	s := seedTwoLayer(t, st)
	ctx := context.Background()

	if _, err := st.UpdatePriceBook(ctx, s.cloudBook.ID, store.PriceBookInput{EffectiveFrom: "2026-08-02"}); err != nil {
		t.Fatal(err)
	}
	results, err := rating.Run(ctx, st, "2026-08", s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].StatementID != "" || !strings.Contains(results[0].Error, "in force from 2026-08-02") {
		t.Fatalf("run = %+v, want the customer reported with the book's effective date and no statement", results)
	}
	stmts, err := st.ListStatements(ctx, store.OperatorScope, s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 0 {
		t.Fatalf("a statement was written at a book not in force: %+v", stmts)
	}

	// In force from the period's first day: rated as before, to the digit.
	if _, err := st.UpdatePriceBook(ctx, s.cloudBook.ID, store.PriceBookInput{EffectiveFrom: "2026-08-01"}); err != nil {
		t.Fatal(err)
	}
	results, err = rating.Run(ctx, st, "2026-08", s.cust.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Error != "" {
		t.Fatalf("run = %+v", results)
	}
	stmt, err := st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stmt.Subtotal) != "126.000000" {
		t.Fatalf("statement subtotal = %s, want 126.000000", stmt.Subtotal)
	}
}
