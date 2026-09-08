package rating

import (
	"strings"
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
func stackable(d store.Discount) store.Discount {
	d.Stackable = true
	return d
}

// apply folds the error check in; rule "" is the default.
func apply(t *testing.T, rule string, lines []store.RatedLine, ds ...store.Discount) (string, []AppliedDiscount) {
	t.Helper()
	got, applied, err := ApplyDiscounts(lines, ds, rule)
	if err != nil {
		t.Fatalf("rule %q: %v", rule, err)
	}
	return string(got), applied
}

// amountOf returns the breakdown amount of one discount, "" when absent.
func amountOf(applied []AppliedDiscount, id string) string {
	for _, a := range applied {
		if a.DiscountID == id {
			return string(a.Amount)
		}
	}
	return ""
}

func entryOf(applied []AppliedDiscount, id string) *AppliedDiscount {
	for i := range applied {
		if applied[i].DiscountID == id {
			return &applied[i]
		}
	}
	return nil
}

func TestPercentDiscountOnWholeBill(t *testing.T) {
	got, applied := apply(t, "", []store.RatedLine{line("a", "100"), line("b", "300")}, pct("q1", "15", ""))
	if got != "60.000000" {
		t.Fatalf("15%% of 400 = %s, want 60", got)
	}
	if len(applied) != 1 || applied[0].Name != "q1" || applied[0].SupersededBy != "" {
		t.Fatalf("applied = %+v", applied)
	}
}

// A scoped discount must touch only its meter, or discounting compute quietly
// discounts storage too.
func TestScopedDiscountOnlyHitsItsSKU(t *testing.T) {
	got, _ := apply(t, "", []store.RatedLine{line("compute", "200"), line("storage", "800")}, pct("c", "50", "compute"))
	if got != "100.000000" {
		t.Fatalf("50%% of compute(200) = %s, want 100 — storage must be untouched", got)
	}
}

// Under the stack rule percentages are computed on the UNTOUCHED base, so
// they do not compound: two 10 % discounts are 20 % off, not 19 %. This was
// the only behaviour before the rule became a setting (DESIGN.md §2.11).
func TestStackRuleSumsPercentages(t *testing.T) {
	got, _ := apply(t, store.DiscountRuleStack, []store.RatedLine{line("a", "1000")}, pct("p1", "10", ""), pct("p2", "10", ""))
	// Both against the untouched 1000: 100 + 100 = 200. Compounding would
	// give 190 (10 % of 1000, then 10 % of 900).
	if got != "200.000000" {
		t.Fatalf("two 10%% discounts under stack = %s, want 200", got)
	}
	// And a fixed amount still comes off the remainder afterwards.
	got2, _ := apply(t, store.DiscountRuleStack, []store.RatedLine{line("a", "1000")}, fixed("f", "100", ""), pct("p", "10", ""))
	if got2 != "200.000000" {
		t.Fatalf("10%% + fixed 100 = %s, want 200", got2)
	}
}

// The discriminating fixture (DESIGN.md §2.11): SKU A 50, SKU B 50; a global
// 10 % and a SKU-A 20 %. Every rule gives a different answer except the two
// winner-takes-the-line rules, which agree here and are separated below.
func TestCombinationRulesOnTheDiscriminatingFixture(t *testing.T) {
	lines := []store.RatedLine{line("A", "50"), line("B", "50")}
	global, skuA := pct("global10", "10", ""), pct("skuA20", "20", "A")
	cases := []struct {
		rule                 string
		total, wantA, wantGl string
	}{
		// A: 20 % (10); B: 10 % (5).
		{store.DiscountRuleMostSpecific, "15.000000", "10.000000", "5.000000"},
		{store.DiscountRuleHighest, "15.000000", "10.000000", "5.000000"},
		// A: 30 % (15); B: 10 % (5). The global takes 5 on A and 5 on B.
		{store.DiscountRuleStack, "20.000000", "10.000000", "10.000000"},
		// A: 1 − 0.9 × 0.8 = 28 % (14) — 20 % of 50 = 10, then 10 % of 40 = 4;
		// B: 10 % (5). The global is attributed 4 + 5.
		{store.DiscountRuleCompound, "19.000000", "10.000000", "9.000000"},
	}
	for _, c := range cases {
		got, applied := apply(t, c.rule, lines, global, skuA)
		if got != c.total {
			t.Errorf("%s: total %s, want %s", c.rule, got, c.total)
		}
		if a, g := amountOf(applied, skuA.ID), amountOf(applied, global.ID); a != c.wantA || g != c.wantGl {
			t.Errorf("%s: skuA=%s global=%s, want %s/%s (%+v)", c.rule, a, g, c.wantA, c.wantGl, applied)
		}
		// Nothing is superseded: the global still applied on B.
		for _, a := range applied {
			if a.SupersededBy != "" {
				t.Errorf("%s: %s marked superseded by %s although it applied", c.rule, a.Name, a.SupersededBy)
			}
		}
		// The rule is applied to the discounts, not to their order.
		if rev, _ := apply(t, c.rule, lines, skuA, global); rev != got {
			t.Errorf("%s: reversing the discount order changed the total %s → %s", c.rule, got, rev)
		}
	}
	// The default is most-specific.
	if def, _ := apply(t, "", lines, global, skuA); def != "15.000000" {
		t.Fatalf("default rule total = %s, want the most-specific 15", def)
	}
}

// Global 30 % against SKU-A 20 %: the two winner rules disagree. highest
// takes 30 % on both lines and supersedes the SKU discount; most-specific
// keeps the SKU discount on A even though it is smaller.
func TestHighestAndMostSpecificDisagreeWhenTheGlobalIsBigger(t *testing.T) {
	lines := []store.RatedLine{line("A", "50"), line("B", "50")}
	global, skuA := pct("global30", "30", ""), pct("skuA20", "20", "A")

	got, applied := apply(t, store.DiscountRuleHighest, lines, global, skuA)
	if got != "30.000000" || amountOf(applied, global.ID) != "30.000000" {
		t.Fatalf("highest: total %s, global %s — want 30 on both lines", got, amountOf(applied, global.ID))
	}
	e := entryOf(applied, skuA.ID)
	if e == nil || e.SupersededBy != global.ID || string(e.Amount) != "0.000000" {
		t.Fatalf("highest: the SKU discount must be on the bill as superseded by %s with amount 0: %+v", global.ID, e)
	}

	got, applied = apply(t, store.DiscountRuleMostSpecific, lines, global, skuA)
	// A: 20 % (10); B: 30 % (15).
	if got != "25.000000" || amountOf(applied, skuA.ID) != "10.000000" || amountOf(applied, global.ID) != "15.000000" {
		t.Fatalf("most-specific: total %s, skuA %s, global %s", got, amountOf(applied, skuA.ID), amountOf(applied, global.ID))
	}
	if e := entryOf(applied, skuA.ID); e.SupersededBy != "" {
		t.Fatalf("most-specific: the SKU discount won its line, yet reads superseded by %s", e.SupersededBy)
	}

	if got, _ := apply(t, store.DiscountRuleStack, lines, global, skuA); got != "40.000000" {
		t.Fatalf("stack: %s, want 25 + 15 = 40", got)
	}
	// A: 1 − 0.8 × 0.7 = 44 % (22); B: 30 % (15).
	if got, _ := apply(t, store.DiscountRuleCompound, lines, global, skuA); got != "37.000000" {
		t.Fatalf("compound: %s, want 22 + 15 = 37", got)
	}
}

// Ties: at the same scope the higher percent wins under most-specific; at
// the same percent the narrower scope wins under highest. A full tie keeps
// the first, and the loser is on the bill as superseded.
func TestTieBreaksNameTheWinner(t *testing.T) {
	lines := []store.RatedLine{line("A", "100")}
	small, big := pct("small10", "10", ""), pct("big25", "25", "")
	got, applied := apply(t, store.DiscountRuleMostSpecific, lines, small, big)
	if got != "25.000000" || entryOf(applied, small.ID) == nil || entryOf(applied, small.ID).SupersededBy != big.ID {
		t.Fatalf("most-specific tie on scope: total %s, %+v", got, applied)
	}
	sku, global := pct("sku20", "20", "A"), pct("global20", "20", "")
	got, applied = apply(t, store.DiscountRuleHighest, lines, global, sku)
	if got != "20.000000" || amountOf(applied, sku.ID) != "20.000000" || entryOf(applied, global.ID).SupersededBy != sku.ID {
		t.Fatalf("highest tie on percent: total %s, %+v", got, applied)
	}
	twinA, twinB := pct("twinA", "15", ""), pct("twinB", "15", "")
	got, applied = apply(t, store.DiscountRuleHighest, lines, twinA, twinB)
	if got != "15.000000" || amountOf(applied, twinA.ID) != "15.000000" || entryOf(applied, twinB.ID).SupersededBy != twinA.ID {
		t.Fatalf("full tie: total %s, %+v", got, applied)
	}
}

// A stackable discount adds on top of the winner under most-specific and
// highest (the campaign on top of the contract); under stack and compound
// the flag changes nothing.
func TestStackableAddsOnTopOfTheWinner(t *testing.T) {
	lines := []store.RatedLine{line("A", "50"), line("B", "50")}
	global, skuA := pct("global10", "10", ""), stackable(pct("skuA20", "20", "A"))
	for _, rule := range []string{store.DiscountRuleMostSpecific, store.DiscountRuleHighest} {
		got, applied := apply(t, rule, lines, global, skuA)
		// A: 10 % + 20 % = 15; B: 10 % = 5.
		if got != "20.000000" || amountOf(applied, skuA.ID) != "10.000000" || amountOf(applied, global.ID) != "10.000000" {
			t.Fatalf("%s with a stackable SKU discount: total %s, %+v", rule, got, applied)
		}
		if e := entryOf(applied, skuA.ID); !e.Stackable {
			t.Fatalf("%s: the breakdown must say the discount was stackable: %+v", rule, e)
		}
	}
	if got, _ := apply(t, store.DiscountRuleStack, lines, global, skuA); got != "20.000000" {
		t.Fatalf("stack ignores the flag: %s", got)
	}
	if got, _ := apply(t, store.DiscountRuleCompound, lines, global, skuA); got != "19.000000" {
		t.Fatalf("compound ignores the flag: %s", got)
	}
	// Two stackable discounts and no contract: they simply add.
	if got, _ := apply(t, store.DiscountRuleMostSpecific, lines, stackable(global), skuA); got != "20.000000" {
		t.Fatalf("only stackable discounts: %s, want 20", got)
	}
}

// Fixed amounts come off what remains after the percentages, in every rule,
// and never take the bill below zero.
func TestFixedAfterPercentAndClamped(t *testing.T) {
	lines := []store.RatedLine{line("A", "50"), line("B", "50")}
	global, skuA, credit := pct("global10", "10", ""), pct("skuA20", "20", "A"), fixed("credit", "100", "")
	for rule, pctTotal := range map[string]string{
		store.DiscountRuleMostSpecific: "15.000000",
		store.DiscountRuleHighest:      "15.000000",
		store.DiscountRuleStack:        "20.000000",
		store.DiscountRuleCompound:     "19.000000",
	} {
		got, applied := apply(t, rule, lines, global, skuA, credit)
		// 100 gross − percentages leaves less than the 100 credit: the credit
		// is clamped to the remainder and the bill goes to exactly zero.
		if got != "100.000000" {
			t.Fatalf("%s: total %s, want the whole 100", rule, got)
		}
		want := map[string]string{"15.000000": "85.000000", "20.000000": "80.000000", "19.000000": "81.000000"}[pctTotal]
		if amountOf(applied, credit.ID) != want {
			t.Fatalf("%s: credit took %s, want %s (the remainder after %s)", rule, amountOf(applied, credit.ID), want, pctTotal)
		}
	}
	// A small credit comes off in full, after the percentages.
	if got, _ := apply(t, store.DiscountRuleMostSpecific, lines, global, skuA, fixed("small", "7", "")); got != "22.000000" {
		t.Fatalf("15 + 7 = %s", got)
	}
}

func TestUnknownRuleIsAnError(t *testing.T) {
	_, _, err := ApplyDiscounts([]store.RatedLine{line("a", "100")}, []store.Discount{pct("p", "10", "")}, "average")
	if err == nil || !strings.Contains(err.Error(), "average") {
		t.Fatalf("unknown rule must be refused by name, got %v", err)
	}
}

// An over-generous campaign takes the bill to zero, never below. A negative
// invoice reads as money owed to the customer.
func TestDiscountNeverExceedsTheBill(t *testing.T) {
	got, _ := apply(t, "", []store.RatedLine{line("a", "50")}, fixed("f1", "500", ""), fixed("f2", "500", ""))
	if got != "50.000000" {
		t.Fatalf("got %s, want 50 — the bill must clamp at zero, not go negative", got)
	}
}

// Money must be exact. A float implementation drifts here.
func TestDiscountArithmeticIsExact(t *testing.T) {
	got, _ := apply(t, "", []store.RatedLine{line("a", "0.1"), line("a", "0.2")}, pct("p", "100", ""))
	if got != "0.300000" {
		t.Fatalf("100%% of 0.1+0.2 = %s, want exactly 0.300000 (float would drift)", got)
	}
	// Compound stays exact too: 1 − 0.9 × 0.8 of 0.3 is 0.084.
	got, _ = apply(t, store.DiscountRuleCompound, []store.RatedLine{line("a", "0.1"), line("a", "0.2")}, pct("p", "10", ""), pct("q", "20", ""))
	if got != "0.084000" {
		t.Fatalf("compound 28%% of 0.3 = %s, want 0.084000", got)
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
	got, applied, err := ApplyDiscounts([]store.RatedLine{line("a", "100")}, nil, store.DiscountRuleStack)
	if err != nil || string(got) != "0" || applied != nil {
		t.Fatalf("got %s %v %v", got, applied, err)
	}
}
