package rating

import (
	"math/big"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The partner waterfall is decided by the ONE discount engine (DESIGN.md
// §Partners): a tier is a set of percent discounts off list, combined by the
// operator's combination rule exactly as a customer's discounts are. These
// tests pin that — no second pricing path, and the same rule table.

func tierPct(name, value, sku string) store.Discount {
	return store.Discount{ID: name, Name: name, Kind: "percent", Value: store.Decimal(value), SKU: sku, Active: true}
}

func unitLine(sku, amount string) store.RatedLine {
	return store.RatedLine{SKU: sku, Quantity: "1", Unit: "instance-hour", UnitPrice: store.Decimal(amount), Amount: store.Decimal(amount)}
}

// DiscountBySKU allocates the SAME total ApplyDiscounts computes, per meter —
// which is what makes a per-line buy and net possible without a second engine.
func TestDiscountBySKUAllocatesTheSameTotalPerMeter(t *testing.T) {
	lines := []store.RatedLine{unitLine("ecs.a", "50"), unitLine("evs.b", "50")}
	discounts := []store.Discount{tierPct("global", "10", ""), tierPct("on-a", "20", "ecs.a")}
	for _, tc := range []struct {
		rule        string
		total, a, b string
	}{
		// The DESIGN.md §2.11 table, per SKU: 10 % global + 20 % on A over
		// a 100 bill split 50/50.
		{store.DiscountRuleMostSpecific, "15", "10", "5"},
		{store.DiscountRuleHighest, "15", "10", "5"},
		{store.DiscountRuleStack, "20", "15", "5"},
		{store.DiscountRuleCompound, "19", "14", "5"},
	} {
		t.Run(tc.rule, func(t *testing.T) {
			total, perSKU, err := DiscountBySKU(lines, discounts, tc.rule)
			if err != nil {
				t.Fatal(err)
			}
			if got := roundRat(total, 6); got != dec(tc.total) {
				t.Fatalf("total = %s, want %s", got, tc.total)
			}
			if got := roundRat(perSKU["ecs.a"], 6); got != dec(tc.a) {
				t.Fatalf("ecs.a = %s, want %s", got, tc.a)
			}
			if got := roundRat(perSKU["evs.b"], 6); got != dec(tc.b) {
				t.Fatalf("evs.b = %s, want %s", got, tc.b)
			}
			// The parts always sum to the whole.
			sum := new(big.Rat).Add(perSKU["ecs.a"], perSKU["evs.b"])
			if sum.Cmp(total) != 0 {
				t.Fatalf("parts %s do not sum to the total %s", roundRat(sum, 6), roundRat(total, 6))
			}
			// And the total is exactly what ApplyDiscounts reports.
			applied, _, err := ApplyDiscounts(lines, discounts, tc.rule)
			if err != nil {
				t.Fatal(err)
			}
			if string(applied) != roundRat(total, 6) {
				t.Fatalf("ApplyDiscounts = %s, DiscountBySKU = %s", applied, roundRat(total, 6))
			}
		})
	}
}

func dec(s string) string {
	r, _ := new(big.Rat).SetString(s)
	return roundRat(r, 6)
}

// A tier is percent discounts off list, and the combination rule decides how
// two of them meet — the founder's rule, applied to the buy price.
func TestTierDiscountsObeyTheCombinationRule(t *testing.T) {
	list := []store.RatedLine{unitLine("ecs.a", "100")}
	tier := []store.Discount{tierPct("tier-base", "30", ""), tierPct("tier-on-a", "20", "ecs.a")}
	// most-specific: the SKU-scoped 20 % wins over the whole-bill 30 %, so
	// the buy is 80 — a narrower tier rule beats a broader one.
	total, _, err := DiscountBySKU(list, tier, store.DiscountRuleMostSpecific)
	if err != nil {
		t.Fatal(err)
	}
	if got := roundRat(total, 6); got != dec("20") {
		t.Fatalf("most-specific tier = %s, want 20", got)
	}
	// highest: 30 % wins wherever it is scoped.
	if total, _, err = DiscountBySKU(list, tier, store.DiscountRuleHighest); err != nil {
		t.Fatal(err)
	}
	if got := roundRat(total, 6); got != dec("30") {
		t.Fatalf("highest tier = %s, want 30", got)
	}
	// stack: 50 % off list.
	if total, _, err = DiscountBySKU(list, tier, store.DiscountRuleStack); err != nil {
		t.Fatal(err)
	}
	if got := roundRat(total, 6); got != dec("50") {
		t.Fatalf("stacked tier = %s, want 50", got)
	}
	// compound: 1 − 0.8 × 0.7 = 44 %.
	if total, _, err = DiscountBySKU(list, tier, store.DiscountRuleCompound); err != nil {
		t.Fatal(err)
	}
	if got := roundRat(total, 6); got != dec("44") {
		t.Fatalf("compound tier = %s, want 44", got)
	}
}

// The derived retail book: buy = list − tier, retail = base × (1 + markup),
// and a retail price below the buy price is REPORTED, never refused.
func TestDeriveItemsBaseBuyMarkupAndBelowBuyWarning(t *testing.T) {
	list := store.PriceBook{
		ID: "b1", Name: "list", Scope: store.LayerCloud, Currency: "OMR", AnnualDivisor: 8760, BillStopped: "compute",
		Items: []store.PriceItem{{SKU: "ecs.a", Unit: "instance-hour", UnitPrice: "100"}},
	}
	tier := []store.Discount{tierPct("tier", "30", "")}

	// base = buy (70) with a 5 % markup → 73.5, above the buy price.
	items, below, err := deriveItems(list, tier, store.RetailRule{Base: store.RetailBaseBuy, MarkupPct: "5"}, store.DiscountRuleMostSpecific)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || string(items[0].UnitPrice) != "73.50000000" {
		t.Fatalf("retail = %+v, want 73.50000000", items)
	}
	if len(below) != 0 {
		t.Fatalf("73.5 is above the buy price of 70, but it was reported below buy: %+v", below)
	}

	// base = list (100) with a 5 % markup → 105.
	items, _, err = deriveItems(list, tier, store.RetailRule{Base: store.RetailBaseList, MarkupPct: "5"}, store.DiscountRuleMostSpecific)
	if err != nil {
		t.Fatal(err)
	}
	if string(items[0].UnitPrice) != "105.00000000" {
		t.Fatalf("retail off list = %s, want 105.00000000", items[0].UnitPrice)
	}

	// A markup negative enough to price BELOW the buy price: base = buy,
	// −10 % → 63, under the 70 the partner pays us. Derived anyway, with
	// the warning: the rule is the partner's to set.
	items, below, err = deriveItems(list, tier, store.RetailRule{Base: store.RetailBaseBuy, MarkupPct: "-10"}, store.DiscountRuleMostSpecific)
	if err != nil {
		t.Fatal(err)
	}
	if string(items[0].UnitPrice) != "63.00000000" {
		t.Fatalf("retail = %s, want 63.00000000", items[0].UnitPrice)
	}
	if len(below) != 1 || below[0].SKU != "ecs.a" || string(below[0].Buy) != "70.00000000" || string(below[0].Retail) != "63.00000000" {
		t.Fatalf("below-buy = %+v, want ecs.a retail 63 under buy 70", below)
	}

	// An override is MOST SPECIFIC first: the SKU beats the service beats
	// the rule's default.
	rule := store.RetailRule{Base: store.RetailBaseBuy, MarkupPct: "5", Overrides: []store.RetailOverride{
		{Scope: "service", Key: "ecs", MarkupPct: "20"},
		{Scope: "sku", Key: "ecs.a", MarkupPct: "50"},
	}}
	items, _, err = deriveItems(list, tier, rule, store.DiscountRuleMostSpecific)
	if err != nil {
		t.Fatal(err)
	}
	if string(items[0].UnitPrice) != "105.00000000" { // 70 × 1.5
		t.Fatalf("sku override = %s, want 105.00000000", items[0].UnitPrice)
	}
	rule.Overrides = rule.Overrides[:1] // service only
	items, _, err = deriveItems(list, tier, rule, store.DiscountRuleMostSpecific)
	if err != nil {
		t.Fatal(err)
	}
	if string(items[0].UnitPrice) != "84.00000000" { // 70 × 1.2
		t.Fatalf("service override = %s, want 84.00000000", items[0].UnitPrice)
	}
}

func TestServiceOfSKU(t *testing.T) {
	for sku, want := range map[string]string{"ecs.s6.large.2": "ecs", "evs.ssd.gb": "evs", "plan.s": "plan", "eip": "eip", "k8s.vcpu": "k8s"} {
		if got := ServiceOf(sku); got != want {
			t.Errorf("ServiceOf(%q) = %q, want %q", sku, got, want)
		}
	}
}

// A retail rule is validated before it can derive anything.
func TestValidateRetailRule(t *testing.T) {
	if _, err := store.ValidateRetailRule(store.RetailRule{Base: "cost"}); err == nil {
		t.Fatal("an unknown base was accepted")
	}
	if _, err := store.ValidateRetailRule(store.RetailRule{Base: store.RetailBaseBuy, MarkupPct: "abc"}); err == nil {
		t.Fatal("a non-numeric markup was accepted")
	}
	if _, err := store.ValidateRetailRule(store.RetailRule{Base: store.RetailBaseList, MarkupPct: "-150"}); err == nil {
		t.Fatal("a markup below -100 % was accepted")
	}
	r, err := store.ValidateRetailRule(store.RetailRule{Base: store.RetailBaseList, Overrides: []store.RetailOverride{{Scope: "SKU", Key: " ecs.a ", MarkupPct: "10"}}})
	if err != nil {
		t.Fatal(err)
	}
	if string(r.MarkupPct) != "0" || r.Overrides[0].Scope != "sku" || r.Overrides[0].Key != "ecs.a" {
		t.Fatalf("normalised rule = %+v", r)
	}
	if _, err := store.ValidateRetailRule(store.RetailRule{Base: store.RetailBaseList, Overrides: []store.RetailOverride{
		{Scope: "sku", Key: "a", MarkupPct: "1"}, {Scope: "sku", Key: "a", MarkupPct: "2"},
	}}); err == nil {
		t.Fatal("a duplicate override was accepted")
	}
}

// allocate splits a SKU's figure across its lines so the parts sum EXACTLY to
// the whole — money never gains or loses a unit in the split.
func TestAllocateSplitsExactly(t *testing.T) {
	lines := []store.RatedLine{unitLine("a", "1"), unitLine("a", "1"), unitLine("a", "1")}
	parts := allocateBySKU(lines, map[string]*big.Rat{"a": big.NewRat(10, 1)}, func(l store.RatedLine) store.Decimal { return l.Amount })
	sum := new(big.Rat)
	for _, p := range parts {
		r, _ := new(big.Rat).SetString(string(p))
		sum.Add(sum, r)
	}
	if sum.Cmp(big.NewRat(10, 1)) != 0 {
		t.Fatalf("parts %v sum to %s, want 10", parts, sum.FloatString(6))
	}
}
