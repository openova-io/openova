package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/rating"
	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// A global campaign (customer_id NULL, #6867) must actually reach the
// statement: ActiveDiscountsAt returns it for every customer and a 10 %
// campaign takes exactly 10 % off the subtotal through rating.Run — money
// arithmetic is exact, so the assertion is on the exact text, not a float.
func TestIntegrationGlobalDiscountReducesStatementSubtotal(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	book, err := st.CreatePriceBook(ctx, store.PriceBookInput{Name: "list", Currency: "OMR", AnnualDivisor: 8760})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutPriceItems(ctx, book.ID, []store.PriceItem{{SKU: "ecs.m7n.xlarge.8", Unit: "instance-hour", UnitPrice: "0.5"}}, true); err != nil {
		t.Fatal(err)
	}
	acme, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "acme", Name: "Acme", AdminEmail: "a@acme.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	bravo, err := st.CreateCustomer(ctx, store.CustomerInput{Slug: "bravo", Name: "Bravo", AdminEmail: "b@bravo.example", PriceBookID: book.ID, StartDate: "2026-08-01"})
	if err != nil {
		t.Fatal(err)
	}
	srcA := mkSource(t, st, acme.ID, "pa")
	srcB := mkSource(t, st, bravo.ID, "pb")
	aug := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var recs []store.UsageRecord
	for h := 0; h < 100; h++ { // 100 h × 0.5 = 50.000000 gross
		recs = append(recs, store.UsageRecord{CustomerID: acme.ID, SourceID: srcA.ID, ResourceID: "vm-a", ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8", Quantity: "1.000000", Unit: "instance-hour",
			WindowStart: aug.Add(time.Duration(h) * time.Hour), WindowEnd: aug.Add(time.Duration(h+1) * time.Hour), Region: "me-east-215"})
	}
	for h := 0; h < 40; h++ { // 40 h × 0.5 = 20.000000 gross
		recs = append(recs, store.UsageRecord{CustomerID: bravo.ID, SourceID: srcB.ID, ResourceID: "vm-b", ResourceKind: "ecs", SKU: "ecs.m7n.xlarge.8", Quantity: "1.000000", Unit: "instance-hour",
			WindowStart: aug.Add(time.Duration(h) * time.Hour), WindowEnd: aug.Add(time.Duration(h+1) * time.Hour), Region: "me-east-215"})
	}
	if _, err := st.UpsertUsage(ctx, recs); err != nil {
		t.Fatal(err)
	}

	global, err := st.CreateDiscount(ctx, store.DiscountInput{Name: "Launch 10%", Kind: "percent", Value: "10"})
	if err != nil {
		t.Fatal(err)
	}
	if global.CustomerID != nil || global.CustomerName != nil || !global.Active {
		t.Fatalf("global discount = %+v", global)
	}
	bravoOnly, err := st.CreateDiscount(ctx, store.DiscountInput{CustomerID: &bravo.ID, Name: "Bravo credit", Kind: "fixed", Value: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if bravoOnly.CustomerID == nil || *bravoOnly.CustomerID != bravo.ID || bravoOnly.CustomerName == nil || *bravoOnly.CustomerName != "Bravo" {
		t.Fatalf("customer discount = %+v", bravoOnly)
	}

	// ActiveDiscountsAt carries the global campaign for both customers, and
	// the per-customer one only for its customer.
	if ds, _ := st.ActiveDiscountsAt(ctx, acme.ID, aug); len(ds) != 1 || ds[0].ID != global.ID {
		t.Fatalf("acme active discounts = %+v", ds)
	}
	if ds, _ := st.ActiveDiscountsAt(ctx, bravo.ID, aug); len(ds) != 2 {
		t.Fatalf("bravo active discounts = %+v", ds)
	}

	results, err := rating.Run(ctx, st, "2026-08", "")
	if err != nil {
		t.Fatal(err)
	}
	byCustomer := map[string]rating.Result{}
	for _, r := range results {
		byCustomer[r.CustomerID] = r
	}
	stA, err := st.GetStatement(ctx, store.OperatorScope, byCustomer[acme.ID].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	// 50 gross − 10 % = 45 exactly; tax 5 % of the NET; total 47.25.
	if stA.Subtotal != "45.000000" || stA.Tax != "2.250000" || stA.Total != "47.250000" {
		t.Fatalf("acme statement with a global 10%% = subtotal %s tax %s total %s", stA.Subtotal, stA.Tax, stA.Total)
	}
	stB, err := st.GetStatement(ctx, store.OperatorScope, byCustomer[bravo.ID].StatementID)
	if err != nil {
		t.Fatal(err)
	}
	// 20 gross − 10 % (2) − 5 fixed = 13.
	if stB.Subtotal != "13.000000" {
		t.Fatalf("bravo statement with global + own discount = %s", stB.Subtotal)
	}
	var detail string
	if err := st.DB().QueryRowContext(ctx, `SELECT discount_detail::text FROM statements WHERE id = $1`, stA.ID).Scan(&detail); err != nil || detail == "" {
		t.Fatalf("frozen discount detail = %q err=%v", detail, err)
	}

	// Lists: a customer scope sees own + global, never another customer's;
	// the operator list joins the customer name.
	mine, err := st.ListDiscounts(ctx, store.CustomerScope(acme.ID), acme.ID)
	if err != nil || len(mine) != 1 || mine[0].ID != global.ID {
		t.Fatalf("acme scoped list = %+v err=%v", mine, err)
	}
	if _, err := st.ListDiscounts(ctx, store.CustomerScope(acme.ID), bravo.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-customer discounts = %v", err)
	}
	all, err := st.ListAllDiscounts(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("all discounts = %+v err=%v", all, err)
	}
	for _, d := range all {
		switch d.ID {
		case bravoOnly.ID:
			if d.CustomerName == nil || *d.CustomerName != "Bravo" {
				t.Fatalf("operator list lacks the joined customer name: %+v", d)
			}
		case global.ID:
			if d.CustomerID != nil || d.CustomerName != nil {
				t.Fatalf("global row carries a customer: %+v", d)
			}
		default:
			t.Fatalf("unexpected discount %+v", d)
		}
	}

	// Update (every field, including scope and active), then delete: the
	// next run no longer discounts.
	off := false
	upd, err := st.UpdateDiscount(ctx, global.ID, store.DiscountInput{CustomerID: &acme.ID, Name: "Acme only", Kind: "fixed", Value: "7", SKU: "ecs.m7n.xlarge.8", Active: &off})
	if err != nil || upd.CustomerID == nil || *upd.CustomerID != acme.ID || upd.Name != "Acme only" || upd.Kind != "fixed" || upd.Value != "7.000000" || upd.SKU != "ecs.m7n.xlarge.8" || upd.Active {
		t.Fatalf("update = %+v err=%v", upd, err)
	}
	if got, _ := st.GetDiscount(ctx, global.ID); got.CustomerName == nil || *got.CustomerName != "Acme" {
		t.Fatalf("get after update = %+v", got)
	}
	if _, err := st.UpdateDiscount(ctx, "00000000-0000-0000-0000-000000000000", store.DiscountInput{Name: "x", Kind: "percent", Value: "1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("update missing = %v", err)
	}
	if err := st.DeleteDiscount(ctx, global.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteDiscount(ctx, global.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	if _, err := st.GetDiscount(ctx, global.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
	if _, err := rating.Run(ctx, st, "2026-08", acme.ID); err != nil {
		t.Fatal(err)
	}
	if stA, _ = st.GetStatement(ctx, store.OperatorScope, stA.ID); stA.Subtotal != "50.000000" {
		t.Fatalf("subtotal after the campaign is deleted = %s", stA.Subtotal)
	}
}
