package rating

import (
	"math/big"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The money tests for the commercial terms (DESIGN.md §15). Every figure
// below is PINNED: a change to the engine that moves one of them is a change
// to what a customer is invoiced, and has to be argued for, not absorbed.
//
// Each shape is tested where it DISCRIMINATES — graduated against all-units
// on the same volume, an allowance exactly consumed against under- and
// over-consumed, a commitment under against over — because a test that only
// exercises the happy middle of a shape cannot fail when the shape is wrong.

func dc(s string) *store.Decimal { d := store.Decimal(s); return &d }

func tier(upTo, price string) store.PriceTier {
	t := store.PriceTier{Price: store.Decimal(price)}
	if upTo != "" {
		t.UpTo = dc(upTo)
	}
	return t
}

// objectTiers is the founder's ladder: 10 TiB at 0.010, then up to 100 TiB at
// 0.008, then everything above at 0.006.
func objectTiers() []store.PriceTier {
	return []store.PriceTier{tier("10240", "0.010"), tier("102400", "0.008"), tier("", "0.006")}
}

func rateOne(t *testing.T, it store.PriceItem, qty string, terms Terms) (store.Decimal, Breakdown) {
	t.Helper()
	lines := []store.RatedLine{{SKU: it.SKU, Quantity: store.Decimal(qty), Unit: it.Unit, UnitPrice: it.UnitPrice, Amount: "0"}}
	out, applied, err := ApplyTerms(lines, map[string]store.PriceItem{it.SKU: it}, terms)
	if err != nil {
		t.Fatalf("ApplyTerms: %v", err)
	}
	if len(applied) != 1 {
		t.Fatalf("expected one breakdown, got %d", len(applied))
	}
	return out[0].Amount, applied[0]
}

// ---------------------------------------------------------------------------
// volume tiers
// ---------------------------------------------------------------------------

// The two tier modes are DIFFERENT prices for the same volume, and the
// difference is the whole reason the mode is explicit on the item. 51,200 GiB
// reaches the middle band:
//
//	graduated: 10,240 × 0.010 + 40,960 × 0.008 = 102.40 + 327.68 = 430.08
//	all-units: 51,200 × 0.008                                    = 409.60
//
// An engine that silently picked one mode would over- or under-bill by 20.48
// on this one line and nobody could see which it had chosen.
func TestGraduatedAndAllUnitsDifferOnTheSameVolume(t *testing.T) {
	base := store.PriceItem{SKU: "object_gib", Unit: "gb-month", UnitPrice: "0.010", Tiers: objectTiers()}

	graduated := base
	graduated.TierMode = store.TierModeGraduated
	amount, br := rateOne(t, graduated, "51200", Terms{})
	if string(amount) != "430.080000" {
		t.Fatalf("graduated = %s, want 430.080000", amount)
	}
	if string(br.EffectiveUnitPrice) != "0.00840000" {
		t.Fatalf("graduated effective unit price = %s, want 0.00840000", br.EffectiveUnitPrice)
	}

	allUnits := base
	allUnits.TierMode = store.TierModeAllUnits
	amount, br = rateOne(t, allUnits, "51200", Terms{})
	if string(amount) != "409.600000" {
		t.Fatalf("all-units = %s, want 409.600000", amount)
	}
	if string(br.EffectiveUnitPrice) != "0.00800000" {
		t.Fatalf("all-units effective unit price = %s, want 0.00800000", br.EffectiveUnitPrice)
	}
}

// The bands are walked, not guessed: a volume inside the first band, exactly
// on a boundary, and above the last bound all price correctly, and the two
// modes agree ONLY while the volume stays inside the first band.
func TestTierBoundaries(t *testing.T) {
	graduated := store.PriceItem{SKU: "object_gib", Unit: "gb-month", UnitPrice: "0.010", TierMode: store.TierModeGraduated, Tiers: objectTiers()}
	allUnits := graduated
	allUnits.TierMode = store.TierModeAllUnits
	for _, tc := range []struct{ qty, grad, all string }{
		// Inside the first band the two modes are the same price.
		{"1000", "10.000000", "10.000000"},
		// Exactly ON the boundary: still the first band's rate in both.
		{"10240", "102.400000", "102.400000"},
		// One unit past it: graduated keeps the 10,240 at 0.010 and charges
		// the extra unit at 0.008; all-units reprices the lot at 0.008.
		{"10241", "102.408000", "81.928000"},
		// Above the last bound: graduated walks all three bands,
		// 102.40 + 737.28 + 600.00 = 1,439.68; all-units charges 0.006.
		{"202400", "1439.680000", "1214.400000"},
	} {
		if got, _ := rateOne(t, graduated, tc.qty, Terms{}); string(got) != tc.grad {
			t.Errorf("graduated %s = %s, want %s", tc.qty, got, tc.grad)
		}
		if got, _ := rateOne(t, allUnits, tc.qty, Terms{}); string(got) != tc.all {
			t.Errorf("all-units %s = %s, want %s", tc.qty, got, tc.all)
		}
	}
}

// An overlapping or out-of-order ladder is REFUSED, not silently resolved:
// two bands claiming the same volume have no single answer.
func TestTierLadderIsValidated(t *testing.T) {
	for name, tiers := range map[string][]store.PriceTier{
		"descending bounds":      {tier("100", "1"), tier("50", "0.5"), tier("", "0.1")},
		"repeated bound":         {tier("100", "1"), tier("100", "0.5")},
		"open band is not last":  {tier("", "1"), tier("100", "0.5")},
		"a band priced negative": {tier("100", "-1"), tier("", "0.5")},
	} {
		it := store.PriceItem{SKU: "x", TierMode: store.TierModeGraduated, Tiers: tiers}
		if _, err := ValidateTiers(it); err == nil {
			t.Errorf("%s: the ladder was accepted", name)
		}
	}
	// A ladder with no unbounded band is completed, not refused: the last
	// price carries on above the last bound rather than rating at nothing.
	it := store.PriceItem{SKU: "x", Unit: "gb", UnitPrice: "1", TierMode: store.TierModeGraduated, Tiers: []store.PriceTier{tier("100", "1"), tier("200", "0.5")}}
	if got, _ := rateOne(t, it, "300", Terms{}); string(got) != "200.000000" {
		// 100 × 1 + 100 × 0.5 + 100 × 0.5
		t.Fatalf("open-ended graduated = %s, want 200.000000", got)
	}
}

// ---------------------------------------------------------------------------
// allowances
// ---------------------------------------------------------------------------

// An allowance of 50 GB: exactly consumed, under-consumed and over-consumed.
// The three cases are the ones a plan gets wrong — an engine that charges for
// the 50th unit, or that charges nothing for the 51st, fails exactly one of
// them.
func TestAllowanceExactlyUnderAndOverConsumed(t *testing.T) {
	it := store.PriceItem{SKU: "eip.traffic_gb", Unit: "gb", UnitPrice: "0.05", Allowance: dc("50")}
	for _, tc := range []struct{ qty, amount, used string }{
		{"30", "0.000000", "30.000000"},   // under: nothing charged
		{"50", "0.000000", "50.000000"},   // exactly consumed: still nothing
		{"130", "4.000000", "50.000000"},  // over: the 80 GB above it at 0.05
		{"50.5", "0.025000", "50.000000"}, // half a unit over is half a unit charged
	} {
		amount, br := rateOne(t, it, tc.qty, Terms{})
		if string(amount) != tc.amount {
			t.Errorf("%s GB = %s, want %s", tc.qty, amount, tc.amount)
		}
		if string(br.AllowanceUsed) != tc.used {
			t.Errorf("%s GB consumed %s of the allowance, want %s", tc.qty, br.AllowanceUsed, tc.used)
		}
	}
}

// A CONTRACT allowance is on top of the plan's, never instead of it: a plan
// including 50 GB plus a negotiated 100 GB includes 150 GB. The 200th GB is
// charged, the 150th is not.
func TestContractAllowanceAddsToThePlans(t *testing.T) {
	it := store.PriceItem{SKU: "eip.traffic_gb", Unit: "gb", UnitPrice: "0.05", Allowance: dc("50")}
	terms := Terms{Contract: &store.Contract{ID: "ct1", Items: []store.ContractItem{
		{Kind: store.ContractItemAllowance, SKU: "eip.traffic_gb", Quantity: "100"},
	}}}
	if amount, br := rateOne(t, it, "150", terms); string(amount) != "0.000000" || string(br.Allowance) != "150.000000" {
		t.Fatalf("150 GB against 50+100 = %s (allowance %s), want 0.000000 / 150.000000", amount, br.Allowance)
	}
	if amount, _ := rateOne(t, it, "200", terms); string(amount) != "2.500000" {
		t.Fatalf("200 GB against 50+100 = %s, want 2.500000", amount) // 50 × 0.05
	}
}

// Carry-over is opt-in and ONE PERIOD DEEP: what the previous period left
// unused is available now, and the allowance of the period that already
// happened is not re-granted twice.
func TestAllowanceCarriedIn(t *testing.T) {
	it := store.PriceItem{SKU: "object_gib", Unit: "gb-month", UnitPrice: "0.01", Allowance: dc("100"), AllowanceRollover: true}
	// 40 GB unused last period: 140 GB included this period, so 150 GB of
	// usage charges for 10.
	terms := Terms{CarryIn: map[string]store.Decimal{"object_gib": "40"}}
	amount, br := rateOne(t, it, "150", terms)
	if string(amount) != "0.100000" || string(br.Allowance) != "140.000000" {
		t.Fatalf("150 GB against 100+40 carried = %s (allowance %s), want 0.100000 / 140.000000", amount, br.Allowance)
	}
	// Without the carry-in the same usage costs five times as much: the
	// carried allowance is doing real work, not decorating the breakdown.
	if amount, _ = rateOne(t, it, "150", Terms{}); string(amount) != "0.500000" {
		t.Fatalf("150 GB against 100 = %s, want 0.500000", amount)
	}
}

// ---------------------------------------------------------------------------
// committed use
// ---------------------------------------------------------------------------

// The founder's case: 10 × ecs.m7n.2xlarge.8 committed for 12 months at 30 %
// off. One month is 744 hours, so the commitment is 7,440 instance-hours at
// 0.35 against a list of 0.50.
//
//	under  (5,000 h): 5,000 × 0.35                    = 1,750.00
//	over  (10,000 h): 7,440 × 0.35 + 2,560 × 0.50     = 2,604.00 + 1,280.00 = 3,884.00
//
// At list the same 10,000 hours would be 5,000.00, so the commitment is worth
// 1,116.00 — a figure the customer can check against its own contract.
func TestCommitmentUnderAndOver(t *testing.T) {
	it := store.PriceItem{SKU: "ecs.m7n.2xlarge.8", Unit: "instance-hour", UnitPrice: "0.50"}
	terms := Terms{Contract: &store.Contract{ID: "ct1", Items: []store.ContractItem{
		{Kind: store.ContractItemCommitment, SKU: "ecs.m7n.2xlarge.8", Quantity: "7440", DiscountPct: dc("30")},
	}}}

	amount, br := rateOne(t, it, "5000", terms)
	if string(amount) != "1750.000000" {
		t.Fatalf("under-consumed commitment = %s, want 1750.000000", amount)
	}
	if string(br.Committed) != "5000.000000" || string(br.CommittedPrice) != "0.35000000" || string(br.Excess) != "0.000000" {
		t.Fatalf("under-consumed breakdown = %+v", br)
	}

	amount, br = rateOne(t, it, "10000", terms)
	if string(amount) != "3884.000000" {
		t.Fatalf("over-consumed commitment = %s, want 3884.000000", amount)
	}
	if string(br.Committed) != "7440.000000" || string(br.Excess) != "2560.000000" {
		t.Fatalf("over-consumed breakdown = %+v", br)
	}

	// A committed PRICE written on the line is used as written, and beats
	// deriving one from a percentage.
	terms.Contract.Items[0].CommittedPrice = dc("0.40")
	if amount, _ = rateOne(t, it, "10000", terms); string(amount) != "4256.000000" {
		t.Fatalf("explicit committed price = %s, want 4256.000000", amount) // 7440×0.40 + 2560×0.50
	}
}

// ---------------------------------------------------------------------------
// the order of operations
// ---------------------------------------------------------------------------

// THE ORDER IS NOT A DETAIL. Each case below computes what the stated order
// produces and what the plausible alternative order produces, and pins the
// stated one. They are different numbers; a reader who wants to change the
// order has to change a pinned figure and say why.
func TestOrderOfOperations(t *testing.T) {
	// ── allowance BEFORE tiers ────────────────────────────────────────────
	// Bands: the first 100 units at 1.00, everything above at 0.10. An
	// allowance of 100 against 200 units of usage.
	//
	//	stated order (allowance off the quantity, THEN the bands):
	//	    billable 100 → the first band → 100 × 1.00 = 100.00
	//	the alternative (the allowance is free but still holds its place on
	//	the ladder, so the billable volume starts at 100):
	//	    100 × 0.10 = 10.00
	//
	// A factor of ten between two defensible readings. Ours is the one the
	// console states: an included quantity resets the ladder.
	it := store.PriceItem{SKU: "x", Unit: "gb", UnitPrice: "1.00", TierMode: store.TierModeGraduated,
		Tiers: []store.PriceTier{tier("100", "1.00"), tier("", "0.10")}, Allowance: dc("100")}
	amount, _ := rateOne(t, it, "200", Terms{})
	if string(amount) != "100.000000" {
		t.Fatalf("allowance before tiers = %s, want 100.000000", amount)
	}
	// The alternative, computed here so the difference is visible rather
	// than asserted: the same bands over the interval (100, 200].
	alt := shape{unitPrice: big.NewRat(1, 1), mode: store.TierModeGraduated}
	var err error
	if alt.bands, err = bandsOf(it); err != nil {
		t.Fatal(err)
	}
	if got := roundRat(alt.tierAmount(big.NewRat(100, 1), big.NewRat(200, 1)), 6); got != "10.000000" {
		t.Fatalf("the alternative order computes %s; the test no longer discriminates", got)
	}

	// ── commitment takes the HEAD of the volume ──────────────────────────
	// The same ladder, no allowance, 200 units, 100 of them committed at
	// 0.50. A commitment covers BASELINE usage — the bottom of the ladder —
	// so the 100 committed units displace the dear band:
	//
	//	stated order:  100 × 0.50 + 100 × 0.10 =  60.00
	//	the tail instead: 100 × 1.00 + 100 × 0.50 = 150.00
	it.Allowance = nil
	terms := Terms{Contract: &store.Contract{ID: "ct1", Items: []store.ContractItem{
		{Kind: store.ContractItemCommitment, SKU: "x", Quantity: "100", CommittedPrice: dc("0.50")},
	}}}
	if amount, _ = rateOne(t, it, "200", terms); string(amount) != "60.000000" {
		t.Fatalf("commitment on the head of the volume = %s, want 60.000000", amount)
	}

	// ── discounts BEFORE the true-up ──────────────────────────────────────
	// A minimum of 1,000 against 1,100 of rated lines and a 200 discount.
	//
	//	stated order (net first): 1,100 − 200 = 900 → true-up 100 → 1,000
	//	the reverse (true-up on the gross): 1,100 ≥ 1,000, no true-up, and
	//	    the discount then takes the bill to 900 — UNDER the minimum the
	//	    same contract agreed, which defeats the minimum entirely.
	lines := []store.RatedLine{{SKU: "x", Quantity: "1", Unit: "gb", UnitPrice: "1100", Amount: "1100"}}
	line, ok, err := TrueUp(lines, "200", "1000")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || string(line.Amount) != "100.000000" || line.SKU != TrueUpSKU {
		t.Fatalf("true-up = %+v (ok=%v), want a %s line of 100.000000", line, ok, TrueUpSKU)
	}
	if _, ok, err = TrueUp(lines, "0", "1000"); err != nil || ok {
		t.Fatalf("a period of 1,100 against a minimum of 1,000 raised a true-up")
	}
}

// The true-up brings the NET subtotal to exactly the minimum, and stays away
// when the period met it. It is a LINE — named, on the invoice — not an
// adjustment of the totals.
func TestTrueUpMeetsTheMinimumExactly(t *testing.T) {
	lines := []store.RatedLine{
		{SKU: "ecs.a", Quantity: "100", Unit: "hour", UnitPrice: "4", Amount: "400"},
		{SKU: "evs.b", Quantity: "100", Unit: "gb", UnitPrice: "2.2", Amount: "220"},
	}
	line, ok, err := TrueUp(lines, "0", "1000")
	if err != nil || !ok {
		t.Fatalf("no true-up on a 620 period against a 1,000 minimum (err=%v)", err)
	}
	if string(line.Amount) != "380.000000" || line.Unit != TrueUpUnit || string(line.Quantity) != "1" {
		t.Fatalf("true-up line = %+v, want 380.000000 for one period", line)
	}
	// With the line on the statement the subtotal is the minimum, to the
	// last unit, and the tax follows it.
	withTrueUp := append(append([]store.RatedLine{}, lines...), line)
	subtotal, tax, total, err := TotalsWithDiscount(withTrueUp, "0", "0.05")
	if err != nil {
		t.Fatal(err)
	}
	if string(subtotal) != "1000.000000" || string(tax) != "50.000000" || string(total) != "1050.000000" {
		t.Fatalf("subtotal/tax/total = %s / %s / %s, want 1000.000000 / 50.000000 / 1050.000000", subtotal, tax, total)
	}
	// A period above the minimum raises nothing, and a contract with no
	// minimum never raises one.
	if _, ok, _ := TrueUp(withTrueUp, "0", "1000"); ok {
		t.Fatal("a period at the minimum raised a second true-up")
	}
	if _, ok, _ := TrueUp(lines, "0", "0"); ok {
		t.Fatal("a contract with no minimum raised a true-up")
	}
}

// ---------------------------------------------------------------------------
// the shapes meet the rest of the engine
// ---------------------------------------------------------------------------

// A SKU with no shape comes back BYTE FOR BYTE — which is every item of every
// book written before §15, and the reason this can ship without re-rating the
// past.
func TestUnshapedLinesAreUntouched(t *testing.T) {
	lines := []store.RatedLine{
		{SKU: "ecs.a", Quantity: "100", Unit: "hour", UnitPrice: "0.50", Amount: "50.000000"},
		{SKU: "evs.b", Quantity: "10", Unit: "gb", UnitPrice: "1.00", Amount: "10.000000"},
	}
	items := map[string]store.PriceItem{
		"ecs.a": {SKU: "ecs.a", Unit: "hour", UnitPrice: "0.50"},
		"evs.b": {SKU: "evs.b", Unit: "gb", UnitPrice: "1.00"},
	}
	out, applied, err := ApplyTerms(lines, items, Terms{Contract: &store.Contract{ID: "ct1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 0 {
		t.Fatalf("shapes were applied to items that carry none: %+v", applied)
	}
	for i := range lines {
		if out[i] != lines[i] {
			t.Fatalf("line %d changed: %+v → %+v", i, lines[i], out[i])
		}
	}
}

// An allowance is the CUSTOMER'S month, not each project's: two sources
// reporting the same SKU share one allowance, and the amount is split back
// across the lines in proportion to what each reported, summing exactly.
func TestShapesApplyPerSKUAcrossSources(t *testing.T) {
	a, b := "src-a", "src-b"
	lines := []store.RatedLine{
		{SourceID: &a, SKU: "eip.traffic_gb", Quantity: "60", Unit: "gb", UnitPrice: "0.05", Amount: "3.000000"},
		{SourceID: &b, SKU: "eip.traffic_gb", Quantity: "40", Unit: "gb", UnitPrice: "0.05", Amount: "2.000000"},
	}
	items := map[string]store.PriceItem{"eip.traffic_gb": {SKU: "eip.traffic_gb", Unit: "gb", UnitPrice: "0.05", Allowance: dc("50")}}
	out, applied, err := ApplyTerms(lines, items, Terms{})
	if err != nil {
		t.Fatal(err)
	}
	// One allowance of 50 against 100 GB: 50 GB at 0.05 = 2.50, never
	// 2 × (50 free) which would have charged nothing at all.
	if len(applied) != 1 || string(applied[0].Amount) != "2.500000" {
		t.Fatalf("per-SKU total = %+v, want 2.500000", applied)
	}
	sum := new(big.Rat)
	for _, l := range out {
		sum.Add(sum, ratOf(l.Amount))
	}
	if got := roundRat(sum, 6); got != "2.500000" {
		t.Fatalf("the lines sum to %s, want 2.500000", got)
	}
	// Split 60/40, and each line's own quantity × unit price still equals
	// its amount, so the invoice reads across.
	if string(out[0].Amount) != "1.500000" || string(out[1].Amount) != "1.000000" {
		t.Fatalf("split = %s / %s, want 1.500000 / 1.000000", out[0].Amount, out[1].Amount)
	}
	if string(out[0].UnitPrice) != "0.02500000" {
		t.Fatalf("effective unit price = %s, want 0.02500000", out[0].UnitPrice)
	}
}

// THE PARTNER INVARIANT. A partner's margin is buy × markup — that is the
// whole promise of the retail rule — and it must hold when the underlying
// line was reshaped by a tier. Before the waterfall scaled by the retail
// RATIO it did not: re-multiplying quantity × retail price threw the tier
// away and billed the end customer at a flat rate.
func TestPartnerMarginIsBuyTimesMarkupOnATieredLine(t *testing.T) {
	const sku = "object_gib"
	src := "src-a"
	listBook := store.PriceBook{ID: "pb-list", Currency: "OMR", Items: []store.PriceItem{
		{SKU: sku, Unit: "gb-month", UnitPrice: "0.010", TierMode: store.TierModeGraduated, Tiers: objectTiers()},
	}}
	// The tier: 20 % off list. The retail rule: base = buy, markup 25 %.
	tierDiscounts := []store.Discount{tierPct("tier", "20", "")}
	rule := store.RetailRule{Base: store.RetailBaseBuy, MarkupPct: "25"}
	retailItems, _, err := deriveItems(listBook, tierDiscounts, rule, store.DiscountRuleMostSpecific)
	if err != nil {
		t.Fatal(err)
	}
	pc := &partnerContext{
		partner: store.Partner{ID: "p1", Slug: "reseller", BillTo: store.BillToPartner},
		model:   resell{},
		tier:    tierDiscounts,
		rule:    store.DiscountRuleMostSpecific,
		retail:  map[string]store.PriceBook{listBook.ID: {ID: "pb-retail", Items: retailItems}},
	}

	// The list line, tiered: 51,200 GiB graduated = 430.08.
	lines := []store.RatedLine{{SourceID: &src, SKU: sku, Quantity: "51200", Unit: "gb-month", UnitPrice: "0.010", Amount: "0"}}
	lines, _, err = ApplyTerms(lines, map[string]store.PriceItem{sku: listBook.Items[0]}, Terms{})
	if err != nil {
		t.Fatal(err)
	}
	if string(lines[0].Amount) != "430.080000" {
		t.Fatalf("the list line is not tiered: %s", lines[0].Amount)
	}

	bookOf := map[string]string{src: listBook.ID}
	bookUnitPrice := func(bookID, s string) store.Decimal {
		if bookID == listBook.ID && s == sku {
			return listBook.Items[0].UnitPrice
		}
		return ""
	}
	customer, err := pc.customerLines(lines, bookOf, bookUnitPrice)
	if err != nil {
		t.Fatal(err)
	}
	w, err := pc.waterfall(lines, customer, nil)
	if err != nil {
		t.Fatal(err)
	}
	// buy = list − 20 % = 344.064; retail = buy × 1.25, so net = buy × 1.25
	// = 430.08 and MARGIN = buy × 0.25 = 86.016 — the markup, exactly.
	if string(w.buyTotal) != "344.064000" {
		t.Fatalf("buy = %s, want 344.064000", w.buyTotal)
	}
	if string(w.margin) != "86.016000" {
		t.Fatalf("margin = %s, want 86.016000 (buy × 25 %%)", w.margin)
	}
	buy, margin := ratOf(w.buyTotal), ratOf(w.margin)
	want := new(big.Rat).Quo(new(big.Rat).Mul(buy, big.NewRat(25, 1)), big.NewRat(100, 1))
	if roundRat(margin, 6) != roundRat(want, 6) {
		t.Fatalf("margin %s is not buy × markup (%s)", roundRat(margin, 6), roundRat(want, 6))
	}
	// And the customer's line still carries the TIER: 430.08, not 51,200 ×
	// the flat retail rate (0.0125 → 640.00), which is what re-multiplying
	// the quantity would have produced.
	if string(customer[0].Amount) != "430.080000" {
		t.Fatalf("the customer line lost its tier: %s (a flat rate would give 640.000000)", customer[0].Amount)
	}
	if string(*customer[0].ListAmount) != "430.080000" {
		t.Fatalf("list amount = %s, want 430.080000", *customer[0].ListAmount)
	}
}

// A line with NO shape still prices exactly as it did before the ratio went
// in: quantity × the derived retail rate. This is the regression guard on the
// waterfall change itself.
func TestPartnerCustomerLinesUnchangedWithoutAShape(t *testing.T) {
	src := "src-a"
	listBook := store.PriceBook{ID: "pb-list", Items: []store.PriceItem{{SKU: "ecs.a", Unit: "hour", UnitPrice: "1.00"}}}
	pc := &partnerContext{
		partner: store.Partner{ID: "p1", BillTo: store.BillToPartner}, model: resell{}, rule: store.DiscountRuleMostSpecific,
		retail: map[string]store.PriceBook{listBook.ID: {ID: "pb-retail", Items: []store.PriceItem{{SKU: "ecs.a", Unit: "hour", UnitPrice: "1.50"}}}},
	}
	lines := []store.RatedLine{{SourceID: &src, SKU: "ecs.a", Quantity: "100", Unit: "hour", UnitPrice: "1.00", Amount: "100.000000"}}
	out, err := pc.customerLines(lines, map[string]string{src: listBook.ID}, func(b, s string) store.Decimal { return "1.00" })
	if err != nil {
		t.Fatal(err)
	}
	if string(out[0].Amount) != "150.000000" || string(out[0].UnitPrice) != "1.50000000" {
		t.Fatalf("retail line = %s @ %s, want 150.000000 @ 1.50000000", out[0].Amount, out[0].UnitPrice)
	}
}

// The words under a tiered row say what the engine does. A price the console
// explains differently from the way it is charged is worse than no
// explanation at all.
func TestExplainItem(t *testing.T) {
	graduated := store.PriceItem{SKU: "object_gib", Unit: "gb-month", UnitPrice: "0.010", TierMode: store.TierModeGraduated, Tiers: objectTiers()}
	got := ExplainItem(graduated, "OMR")
	for _, want := range []string{"Each band rates at its own price", "up to 10240 at 0.01 OMR per gb-month", "10240-102400 at 0.008", "above 102400 at 0.006"} {
		if !strings.Contains(got, want) {
			t.Fatalf("graduated explanation %q is missing %q", got, want)
		}
	}
	allUnits := graduated
	allUnits.TierMode = store.TierModeAllUnits
	if !strings.Contains(ExplainItem(allUnits, "OMR"), "The WHOLE billable quantity rates at the band it reaches") {
		t.Fatalf("all-units explanation = %q", ExplainItem(allUnits, "OMR"))
	}
	allowance := store.PriceItem{SKU: "eip.traffic_gb", Unit: "gb", UnitPrice: "0.05", Allowance: dc("50.000000")}
	got = ExplainItem(allowance, "OMR")
	if !strings.Contains(got, "The first 50 gb each period are included") || !strings.Contains(got, "lapses at the end of the period") || !strings.Contains(got, "the rest at 0.05 OMR per gb") {
		t.Fatalf("allowance explanation = %q", got)
	}
	allowance.AllowanceRollover = true
	if !strings.Contains(ExplainItem(allowance, "OMR"), "carries into the next period, once") {
		t.Fatalf("rollover explanation = %q", ExplainItem(allowance, "OMR"))
	}
}
