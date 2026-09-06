package rating

import (
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func line(sku, amt string) store.RatedLine {
	return store.RatedLine{SKU: sku, Amount: store.Decimal(amt)}
}
func pct(name, v, sku string) store.Discount {
	return store.Discount{ID: "d-" + name, Name: name, Kind: "percent", Value: store.Decimal(v), SKU: sku, Active: true}
}
func fixed(name, v, sku string) store.Discount {
	return store.Discount{ID: "d-" + name, Name: name, Kind: "fixed", Value: store.Decimal(v), SKU: sku, Active: true}
}

func TestPercentDiscountOnWholeBill(t *testing.T) {
	got, applied, err := ApplyDiscounts([]store.RatedLine{line("a", "100"), line("b", "300")}, []store.Discount{pct("q1", "15", "")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "60.000000" {
		t.Fatalf("15%% of 400 = %s, want 60", got)
	}
	if len(applied) != 1 || applied[0].Name != "q1" {
		t.Fatalf("applied = %+v", applied)
	}
}

// A scoped discount must touch only its meter, or discounting compute quietly
// discounts storage too.
func TestScopedDiscountOnlyHitsItsSKU(t *testing.T) {
	got, _, err := ApplyDiscounts([]store.RatedLine{line("compute", "200"), line("storage", "800")}, []store.Discount{pct("c", "50", "compute")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "100.000000" {
		t.Fatalf("50%% of compute(200) = %s, want 100 — storage must be untouched", got)
	}
}

// Percentages are computed on the UNTOUCHED base, so they do not compound and
// the evaluation order cannot change the total. This asserts the commercial
// meaning: two 10% discounts are 20% off, not 19%.
func TestPercentagesDoNotCompound(t *testing.T) {
	got, _, err := ApplyDiscounts([]store.RatedLine{line("a", "1000")},
		[]store.Discount{pct("p1", "10", ""), pct("p2", "10", "")})
	if err != nil {
		t.Fatal(err)
	}
	// Both against the untouched 1000: 100 + 100 = 200. Compounding would
	// give 190 (10% of 1000, then 10% of 900).
	if string(got) != "200.000000" {
		t.Fatalf("two 10%% discounts = %s, want 200 — %s means they compounded", got, got)
	}

	// And a fixed amount still comes off the remainder afterwards.
	got2, _, err := ApplyDiscounts([]store.RatedLine{line("a", "1000")},
		[]store.Discount{fixed("f", "100", ""), pct("p", "10", "")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got2) != "200.000000" {
		t.Fatalf("10%% + fixed 100 = %s, want 200", got2)
	}
}

// An over-generous campaign takes the bill to zero, never below. A negative
// invoice reads as money owed to the customer.
func TestDiscountNeverExceedsTheBill(t *testing.T) {
	got, _, err := ApplyDiscounts([]store.RatedLine{line("a", "50")},
		[]store.Discount{fixed("f1", "500", ""), fixed("f2", "500", "")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "50.000000" {
		t.Fatalf("got %s, want 50 — the bill must clamp at zero, not go negative", got)
	}
}

// Money must be exact. A float implementation drifts here.
func TestDiscountArithmeticIsExact(t *testing.T) {
	got, _, err := ApplyDiscounts([]store.RatedLine{line("a", "0.1"), line("a", "0.2")}, []store.Discount{pct("p", "100", "")})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "0.300000" {
		t.Fatalf("100%% of 0.1+0.2 = %s, want exactly 0.300000 (float would drift)", got)
	}
}

// A campaign outside its window must not discount.
func TestCampaignWindowIsRespected(t *testing.T) {
	now := time.Date(2026, 9, 6, 0, 0, 0, 0, time.UTC)
	past := now.AddDate(0, -2, 0)
	ended := store.Discount{Active: true, Kind: "percent", Value: "50", StartsAt: &past, EndsAt: &past}
	if ended.AppliesAt(now) {
		t.Fatal("an ended campaign still applies — customers keep getting a discount that expired")
	}
	future := now.AddDate(0, 1, 0)
	notYet := store.Discount{Active: true, Kind: "percent", Value: "50", StartsAt: &future}
	if notYet.AppliesAt(now) {
		t.Fatal("a future campaign already applies")
	}
	live := store.Discount{Active: true, Kind: "percent", Value: "50"}
	if !live.AppliesAt(now) {
		t.Fatal("an unbounded active discount should apply")
	}
	off := store.Discount{Active: false, Kind: "percent", Value: "50"}
	if off.AppliesAt(now) {
		t.Fatal("a deactivated discount still applies")
	}
}

func TestNoDiscountsIsZero(t *testing.T) {
	got, applied, err := ApplyDiscounts([]store.RatedLine{line("a", "100")}, nil)
	if err != nil || string(got) != "0" || applied != nil {
		t.Fatalf("got %s %v %v", got, applied, err)
	}
}
