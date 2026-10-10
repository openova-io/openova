package rating

import (
	"encoding/json"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The contract page's reading of a period (DESIGN.md §15.10), derived from
// what the statement froze: one contract across two periods — August under
// its floor with a true-up, July above it without one — and a contract with
// no floor whose commitment the month did not fill.

func decPtr(s string) *store.Decimal { d := store.Decimal(s); return &d }

func periodContract() store.Contract {
	return store.Contract{
		ID: "ct1", CustomerID: "c1", Name: "ACME 2026", Currency: "OMR",
		Items: []store.ContractItem{
			{ID: "i1", Kind: store.ContractItemCommitment, SKU: "ecs.m7n.2xlarge.8", Unit: "instance-hour", Quantity: "1500", CommittedPrice: decPtr("0.30")},
			{ID: "i2", Kind: store.ContractItemAllowance, SKU: "eip.traffic_gb", Unit: "gb", Quantity: "100"},
			{ID: "sp1", Kind: store.ContractItemSpend, Quantity: "0", Amount: decPtr("600"), DiscountPct: decPtr("50")},
		},
	}
}

func discountDetail(t *testing.T, applied []AppliedDiscount) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestContractPeriodsAcrossTwoPeriods(t *testing.T) {
	c := periodContract()
	// August: 2,000 h — 1,500 at 0.30 + 500 at 0.50 = 700; 60 GB inside the
	// 100 GB allowance at 0; 50 % off = 350 net, trued up 250 to the 600
	// floor. The spend discount applied; a campaign it superseded did not.
	august := store.Statement{
		ID: "st-aug", PeriodStart: "2026-08-01", PeriodEnd: "2026-08-31", Currency: "OMR", Status: "sent", EffectiveStatus: "overdue",
		InvoiceNumber: "INV-2026-000007", Subtotal: "600.000000", DiscountTotal: "350.000000", Total: "630.000000",
		Lines: []store.RatedLine{
			{SKU: "ecs.m7n.2xlarge.8", Quantity: "1200", Unit: "instance-hour", Amount: "420.000000"},
			{SKU: "ecs.m7n.2xlarge.8", Quantity: "800", Unit: "instance-hour", Amount: "280.000000"},
			{SKU: "eip.traffic_gb", Quantity: "60", Unit: "gb", Amount: "0.000000"},
			{SKU: TrueUpSKU, Quantity: "1", Unit: TrueUpUnit, Amount: "250.000000"},
		},
		DiscountDetail: discountDetail(t, []AppliedDiscount{
			{DiscountID: "sp1", Name: "Spend commitment of 600 OMR a month under ACME 2026", Kind: "percent", Value: "50", Amount: "350.000000", Stackable: true},
			{DiscountID: "d9", Name: "Summer campaign", Kind: "percent", Value: "5", Amount: "0", SupersededBy: "sp1"},
		}),
	}
	// July: 5,000 h — 1,500 at 0.30 + 3,500 at 0.50 = 2,200; 160 GB, 60
	// above the allowance at 0.05 = 3; 50 % off 2,203 = 1,101.50 net, above
	// the floor: no true-up.
	july := store.Statement{
		ID: "st-jul", PeriodStart: "2026-07-01", PeriodEnd: "2026-07-31", Currency: "OMR", Status: "paid",
		Subtotal: "1101.500000", DiscountTotal: "1101.500000", Total: "1156.575000",
		Lines: []store.RatedLine{
			{SKU: "ecs.m7n.2xlarge.8", Quantity: "5000", Unit: "instance-hour", Amount: "2200.000000"},
			{SKU: "eip.traffic_gb", Quantity: "160", Unit: "gb", Amount: "3.000000"},
		},
		DiscountDetail: discountDetail(t, []AppliedDiscount{
			{DiscountID: "sp1", Name: "Spend commitment of 600 OMR a month under ACME 2026", Kind: "percent", Value: "50", Amount: "1101.500000", Stackable: true},
		}),
	}
	rows, err := ContractPeriods(c, []store.Statement{august, july})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Period != "2026-08" || rows[1].Period != "2026-07" {
		t.Fatalf("periods = %+v, want August then July as given", rows)
	}

	aug := rows[0]
	if aug.StatementID != "st-aug" || aug.InvoiceNumber != "INV-2026-000007" || aug.Status != "overdue" || aug.Currency != "OMR" {
		t.Fatalf("August header = %+v (the effective status must win over the stored one)", aug)
	}
	if aug.TrueUp != "250.000000" || aug.Net != "350.000000" || aug.Floor == nil || *aug.Floor != "600.000000" {
		t.Fatalf("August true-up %s, net %s, floor %v; want 250 / 350 / 600 — the subtotal IS the floor the period was trued up to", aug.TrueUp, aug.Net, aug.Floor)
	}
	if aug.Subtotal != "600.000000" || aug.Discount != "350.000000" || aug.Total != "630.000000" {
		t.Fatalf("August money = %s / %s / %s", aug.Subtotal, aug.Discount, aug.Total)
	}
	if len(aug.Allowances) != 1 {
		t.Fatalf("August allowances = %+v, want the one traffic line", aug.Allowances)
	}
	if a := aug.Allowances[0]; a.SKU != "eip.traffic_gb" || a.Unit != "gb" || a.Quantity != "60.000000" || a.Included != "100.000000" || a.Used != "60.000000" || a.Excess != "0.000000" {
		t.Fatalf("August traffic allowance = %+v, want 60 used of 100, no excess", a)
	}
	if len(aug.Commitments) != 1 {
		t.Fatalf("August commitments = %+v, want the one compute line", aug.Commitments)
	}
	if m := aug.Commitments[0]; m.SKU != "ecs.m7n.2xlarge.8" || m.Quantity != "2000.000000" || m.Committed != "1500.000000" || m.Delivered != "1500.000000" || m.Shortfall != "0.000000" || m.Excess != "500.000000" || m.Amount != "700.000000" || m.CommittedPrice == nil || *m.CommittedPrice != "0.30" {
		t.Fatalf("August compute commitment = %+v, want 1,500 delivered of 1,500, 500 excess, rated 700 across two sources", m)
	}
	if len(aug.Discounts) != 1 || aug.Discounts[0].DiscountID != "sp1" || !aug.Discounts[0].FromContract || aug.Discounts[0].Amount != "350.000000" {
		t.Fatalf("August discounts = %+v, want the spend commitment alone, marked as the contract's; the superseded campaign did nothing", aug.Discounts)
	}

	jul := rows[1]
	if jul.TrueUp != "0.000000" || jul.Net != "1101.500000" || jul.Floor == nil || *jul.Floor != "600" || jul.Status != "paid" {
		t.Fatalf("July true-up %s, net %s, floor %v, status %s; want none / 1,101.50 / the contract's 600 / paid", jul.TrueUp, jul.Net, jul.Floor, jul.Status)
	}
	if a := jul.Allowances[0]; a.Quantity != "160.000000" || a.Used != "100.000000" || a.Excess != "60.000000" {
		t.Fatalf("July traffic allowance = %+v, want the whole 100 used and 60 above it", a)
	}
	if m := jul.Commitments[0]; m.Delivered != "1500.000000" || m.Shortfall != "0.000000" || m.Excess != "3500.000000" || m.Amount != "2200.000000" {
		t.Fatalf("July compute commitment = %+v, want 1,500 delivered, 3,500 excess, rated 2,200", m)
	}
	if len(jul.Discounts) != 1 || !jul.Discounts[0].FromContract {
		t.Fatalf("July discounts = %+v", jul.Discounts)
	}
}

func TestContractPeriodWithNoFloorAndACommitmentNotFilled(t *testing.T) {
	// No minimum and no spend line: no floor, nothing to true up. The month
	// used 900 of the 1,500 committed hours, so 600 went undelivered — and
	// the SKU with no usage at all reads as zero, not as missing.
	c := store.Contract{
		ID: "ct2", CustomerID: "c2", Name: "Globex 2026", Currency: "OMR",
		Items: []store.ContractItem{
			{ID: "i1", Kind: store.ContractItemCommitment, SKU: "ecs.m7n.2xlarge.8", Unit: "instance-hour", Quantity: "1500", DiscountPct: decPtr("30")},
			{ID: "i2", Kind: store.ContractItemAllowance, SKU: "evs.ssd.gb", Unit: "gb-hour", Quantity: "744000"},
			// A commitment and an allowance on the SAME SKU: the allowance
			// comes off first, the commitment covers the head of the rest.
			{ID: "i3", Kind: store.ContractItemAllowance, SKU: "eip.traffic_gb", Unit: "gb", Quantity: "50"},
			{ID: "i4", Kind: store.ContractItemCommitment, SKU: "eip.traffic_gb", Unit: "gb", Quantity: "100", CommittedPrice: decPtr("0.04")},
		},
	}
	st := store.Statement{
		ID: "st-1", PeriodStart: "2026-06-01", PeriodEnd: "2026-06-30", Currency: "OMR", Status: "draft",
		Subtotal: "321.000000", DiscountTotal: "0", Total: "337.050000",
		Lines: []store.RatedLine{
			{SKU: "ecs.m7n.2xlarge.8", Quantity: "900", Unit: "instance-hour", Amount: "315.000000"},
			{SKU: "eip.traffic_gb", Quantity: "200", Unit: "gb", Amount: "6.000000"},
		},
	}
	row, err := ContractPeriodOf(c, st)
	if err != nil {
		t.Fatal(err)
	}
	if row.Period != "2026-06" || row.Floor != nil || row.TrueUp != "0.000000" || row.Net != "321.000000" || row.Discount != "0" {
		t.Fatalf("row = %+v, want no floor, no true-up, net = subtotal", row)
	}
	if len(row.Commitments) != 2 || len(row.Allowances) != 2 {
		t.Fatalf("lines = %d commitments, %d allowances, want 2 and 2", len(row.Commitments), len(row.Allowances))
	}
	if m := row.Commitments[0]; m.Delivered != "900.000000" || m.Shortfall != "600.000000" || m.Excess != "0.000000" || m.Amount != "315.000000" || m.DiscountPct == nil || *m.DiscountPct != "30" {
		t.Fatalf("compute commitment = %+v, want 900 delivered, 600 short", m)
	}
	if a := row.Allowances[0]; a.SKU != "evs.ssd.gb" || a.Quantity != "0.000000" || a.Used != "0.000000" || a.Excess != "0.000000" || a.Included != "744000.000000" {
		t.Fatalf("an allowance on a SKU with no usage = %+v, want zero used of 744,000", a)
	}
	// 200 GB: 50 included, then 100 committed, then 50 above.
	if a := row.Allowances[1]; a.Used != "50.000000" || a.Excess != "150.000000" {
		t.Fatalf("traffic allowance = %+v", a)
	}
	if m := row.Commitments[1]; m.Delivered != "100.000000" || m.Shortfall != "0.000000" || m.Excess != "50.000000" {
		t.Fatalf("traffic commitment = %+v, want the 100 head after the 50 allowance, 50 above", m)
	}
	if len(row.Discounts) != 0 {
		t.Fatalf("discounts = %+v, want none", row.Discounts)
	}
}
