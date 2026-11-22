package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
	"github.com/openova-io/openova/products/chargeback/internal/testdb"
)

// TestIntegrationPrepaidForReadsTheListAmount: a resell partner's customer is
// billed the partner's retail price, so October's advance line for November
// carries the retail 4.041 (M 4.490 at a −10 % markup) in amount and the list
// 4.490 in list_amount. The package lines November is rated with are list
// lines (the partner pass marks them up after), so the paid figure they are
// compared with must be the list 4.490. On main PrepaidFor read the retail
// 4.041, and every November the Source that stayed on M was billed an
// "Upgrade M → M" of 0.449; under a markup a real upgrade was billed short.
func TestIntegrationPrepaidForReadsTheListAmount(t *testing.T) {
	st := testdb.Open(t)
	ctx := context.Background()
	c := mustCustomer(t, st, "nizwa", "Nizwa Dates")
	src, _, err := st.UpsertSource(ctx, c.ID, store.SourceKindOrg, "", "nizwa")
	if err != nil {
		t.Fatal(err)
	}
	oct := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	nov := oct.AddDate(0, 1, 0)
	novEnd := nov.AddDate(0, 1, -1)
	list := store.Decimal("4.490000")
	listUP := store.Decimal("4.49000000")
	if _, err := st.WriteDraftStatement(ctx, store.StatementDraft{
		CustomerID: c.ID, PeriodStart: oct, PeriodEnd: nov.AddDate(0, 0, -1), Currency: "OMR",
		Subtotal: "4.041000", TaxRate: "0", Tax: "0", Total: "4.041000",
		Lines: []store.RatedLine{{
			SourceID: &src.ID, SKU: "plan.m", Quantity: "1", Unit: "month", UnitPrice: "4.04100000", Amount: "4.041000",
			ListUnitPrice: &listUP, ListAmount: &list,
			Description: "M package — November 2026, billed in advance", ServiceFrom: &nov, ServiceTo: &novEnd,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	p, err := st.PrepaidFor(ctx, src.ID, nov)
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || p.PlanSlug != "m" || string(p.Amount) != "4.490000" {
		t.Fatalf("PrepaidFor = %+v, want plan m paid at the list 4.490000", p)
	}

	// A line with no list figure (a direct customer) is read as it was.
	d, _, err := st.UpsertSource(ctx, c.ID, store.SourceKindOrg, "", "nizwa-direct")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.WriteDraftStatement(ctx, store.StatementDraft{
		CustomerID: c.ID, PeriodStart: oct, PeriodEnd: nov.AddDate(0, 0, -1), Currency: "OMR",
		Subtotal: "8.531000", TaxRate: "0", Tax: "0", Total: "8.531000",
		Lines: []store.RatedLine{
			{SourceID: &src.ID, SKU: "plan.m", Quantity: "1", Unit: "month", UnitPrice: "4.04100000", Amount: "4.041000",
				ListUnitPrice: &listUP, ListAmount: &list,
				Description: "M package — November 2026, billed in advance", ServiceFrom: &nov, ServiceTo: &novEnd},
			{SourceID: &d.ID, SKU: "plan.m", Quantity: "1", Unit: "month", UnitPrice: "4.49000000", Amount: "4.490000",
				Description: "M package — November 2026, billed in advance", ServiceFrom: &nov, ServiceTo: &novEnd},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if p, err = st.PrepaidFor(ctx, d.ID, nov); err != nil {
		t.Fatal(err)
	}
	if p == nil || string(p.Amount) != "4.490000" {
		t.Fatalf("PrepaidFor (direct) = %+v, want 4.490000", p)
	}
}
