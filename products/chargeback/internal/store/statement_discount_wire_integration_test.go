package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A statement must carry what the discounts took off and which ones applied
// (#6867 statement view: list → discount → net → tax → total). Before this,
// the columns were written at rating time and never read back.
func TestIntegrationStatementExposesDiscountOnTheWire(t *testing.T) {
	st := testdb.Open(t)
	s := seedLedger(t, st)
	ctx := context.Background()
	if _, err := st.CreateDiscount(ctx, store.DiscountInput{CustomerID: &s.a.ID, Name: "Launch 10%", Kind: "percent", Value: "10"}); err != nil {
		t.Fatal(err)
	}
	results, err := rating.Run(ctx, st, "2026-09", s.a.ID)
	if err != nil || len(results) != 1 || results[0].Error != "" {
		t.Fatalf("run = %+v err=%v", results, err)
	}
	for _, get := range []func() (store.Statement, error){
		func() (store.Statement, error) {
			return st.GetStatement(ctx, store.OperatorScope, results[0].StatementID)
		},
		func() (store.Statement, error) {
			list, err := st.ListStatements(ctx, store.OperatorScope, s.a.ID)
			if err != nil || len(list) != 1 {
				t.Fatalf("list = %+v err=%v", list, err)
			}
			return list[0], nil
		},
	} {
		stmt, err := get()
		if err != nil {
			t.Fatal(err)
		}
		// Gross 100.8 (ECS 84 + EVS 16.8) → 10 % off = 10.08; net 90.72.
		if string(stmt.DiscountTotal) != "10.080000" || string(stmt.Subtotal) != "90.720000" {
			t.Fatalf("discount_total=%s subtotal=%s", stmt.DiscountTotal, stmt.Subtotal)
		}
		var detail []map[string]any
		if err := json.Unmarshal(stmt.DiscountDetail, &detail); err != nil || len(detail) != 1 || detail[0]["name"] != "Launch 10%" || detail[0]["amount"] != 10.08 {
			t.Fatalf("discount_detail = %s (err %v)", stmt.DiscountDetail, err)
		}
	}
	// A statement without discounts reads 0 and no detail, not an error.
	resB, err := rating.Run(ctx, st, "2026-09", s.b.ID)
	if err != nil || len(resB) != 1 {
		t.Fatal(err)
	}
	stmt, err := st.GetStatement(ctx, store.OperatorScope, resB[0].StatementID)
	if err != nil || (string(stmt.DiscountTotal) != "0" && string(stmt.DiscountTotal) != "0.000000") || stmt.DiscountDetail != nil {
		t.Fatalf("undiscounted statement: total=%s detail=%s err=%v", stmt.DiscountTotal, stmt.DiscountDetail, err)
	}
}
