package rating

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The tax step of the waterfall, per rule (DESIGN.md §17).
//
// Every assertion here fails on the pre-§17 code, which had exactly one rate
// and no concept of a rule, a category, a certificate or a reverse charge.

func at(day string) time.Time {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return t
}

// twoRateEngine: compute at the standard 5 %, storage zero-rated.
func twoRateEngine() *store.TaxEngine {
	return &store.TaxEngine{
		SellerCountry: "OM",
		DefaultRate:   "0.0500",
		Rules: []store.TaxRule{
			{ID: "std", Name: "Oman VAT standard", Country: "OM", Rate: "0.0500", Kind: store.TaxKindStandard, EffectiveFrom: "2021-04-16"},
			{ID: "zero", Name: "Zero-rated storage", Country: "OM", Category: "storage", Rate: "0", Kind: store.TaxKindZeroRated,
				Note: "Zero-rated supply under the Executive Regulation.", EffectiveFrom: "2021-04-16"},
		},
		Categories: []store.TaxCategoryRule{
			{SKU: "evs.*", Category: "storage"},
			{SKU: "obs.*", Category: "storage"},
		},
	}
}

// TWO RATES ON ONE INVOICE. The statement's tax must be EXACTLY the sum of
// the per-rule amounts — not approximately, not after rounding — and the net
// subtotal exactly the sum of the per-rule bases.
func TestTwoRatesOnOneInvoiceTotalExactly(t *testing.T) {
	lines := []store.RatedLine{
		line("ecs.c7.large", "400.000000"),
		line("evs.ssd", "100.000000"),
		line("obs.standard", "20.000000"),
	}
	// A discount of 52 against a gross of 520: it must be SPLIT across the
	// rules pro rata, because taxing the list price and discounting
	// afterwards overcharges tax (#6862) and taxing only one rule's share
	// would misstate both.
	out, res, err := ApplyTax(lines, "52.000000", twoRateEngine(), store.TaxParty{Country: "OM"}, at("2026-08-31"), "0.0500")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 2 {
		t.Fatalf("tax summary has %d rows, want one per rule: %+v", len(res.Lines), res.Lines)
	}

	sumBase, sumTax := new(big.Rat), new(big.Rat)
	for _, l := range res.Lines {
		sumBase.Add(sumBase, mustRat(string(l.Base)))
		sumTax.Add(sumTax, mustRat(string(l.Tax)))
	}
	if got := store.Decimal(roundRat(sumTax, 6)); got != res.Tax {
		t.Fatalf("statement tax %s is not the sum of the per-rule amounts %s", res.Tax, got)
	}
	if got := store.Decimal(roundRat(sumBase, 6)); got != res.Subtotal {
		t.Fatalf("statement subtotal %s is not the sum of the per-rule bases %s", res.Subtotal, got)
	}
	// And the identity that makes the whole thing a waterfall: net = gross −
	// discount, total = net + tax.
	if res.Subtotal != "468.000000" {
		t.Fatalf("net subtotal = %s, want 520 − 52 = 468.000000", res.Subtotal)
	}
	if res.Total != store.Decimal(roundRat(new(big.Rat).Add(sumBase, sumTax), 6)) {
		t.Fatalf("total %s is not subtotal + tax", res.Total)
	}

	// The standard rule carries 400 of the 520 gross, so 40 of the 52
	// discount: base 360, tax 18. The zero-rated rule carries 120 gross,
	// 12 of the discount: base 108, tax 0.
	byRule := map[string]store.TaxLine{}
	for _, l := range res.Lines {
		byRule[l.RuleID] = l
	}
	if std := byRule["std"]; string(std.Base) != "360.000000" || string(std.Tax) != "18.000000" || string(std.Rate) != "0.0500" {
		t.Errorf("standard row = %+v, want base 360.000000 tax 18.000000 at 0.0500", std)
	}
	if zero := byRule["zero"]; string(zero.Base) != "108.000000" || string(zero.Tax) != "0.000000" || zero.Kind != store.TaxKindZeroRated {
		t.Errorf("zero-rated row = %+v, want base 108.000000 tax 0.000000, kind zero_rated", zero)
	}
	if res.Tax != "18.000000" {
		t.Fatalf("tax = %s, want 18.000000", res.Tax)
	}

	// Every LINE carries the category and the rule it was taxed under, which
	// is what an e-invoice needs per line.
	if out[0].TaxCategory != "" || out[0].TaxRuleID != "std" {
		t.Errorf("compute line = category %q rule %q", out[0].TaxCategory, out[0].TaxRuleID)
	}
	if out[1].TaxCategory != "storage" || out[1].TaxRuleID != "zero" {
		t.Errorf("storage line = category %q rule %q", out[1].TaxCategory, out[1].TaxRuleID)
	}
}

// REVERSE CHARGE: a registered business in another country is invoiced at
// zero, and the invoice carries the note the law requires.
func TestReverseChargeProducesAZeroLineAndItsNote(t *testing.T) {
	engine := twoRateEngine()
	engine.Rules = append(engine.Rules, store.TaxRule{
		ID: "rc-ae", Name: "UAE reverse charge", Country: "AE", Rate: "0", Kind: store.TaxKindReverseCharge,
		Note: "Reverse charge applies: the recipient accounts for the tax.", EffectiveFrom: "2021-04-16",
	})
	party := store.TaxParty{Country: "AE", RegistrationNumber: "AE100200300", Business: true}

	_, res, err := ApplyTax([]store.RatedLine{line("ecs.c7.large", "1000.000000")}, "0", engine, party, at("2026-08-31"), "0.0500")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tax != "0.000000" {
		t.Fatalf("reverse charge produced tax %s, want 0.000000", res.Tax)
	}
	if len(res.Lines) != 1 {
		t.Fatalf("summary = %+v", res.Lines)
	}
	row := res.Lines[0]
	if row.Kind != store.TaxKindReverseCharge {
		t.Errorf("kind = %q, want reverse_charge", row.Kind)
	}
	if string(row.Tax) != "0.000000" || string(row.Base) != "1000.000000" {
		t.Errorf("row = %+v — the base stands, only the tax is zero", row)
	}
	if !strings.Contains(row.Note, "recipient accounts for the tax") {
		t.Errorf("note = %q — the legally required sentence is missing", row.Note)
	}
	if len(res.Notes) != 1 || res.Notes[0] != row.Note {
		t.Errorf("document notes = %+v", res.Notes)
	}
	if len(res.Audit) == 0 || !strings.Contains(res.Audit[0], "reverse charge") {
		t.Errorf("audit = %+v — the determination must be recorded", res.Audit)
	}

	// A CONSUMER in the same country is NOT reverse-charged: the rule
	// applies to a registered business, and a rate that fell away for a
	// consumer would be tax the issuer owes and never collected.
	_, consumer, err := ApplyTax([]store.RatedLine{line("ecs.c7.large", "1000.000000")}, "0",
		engine, store.TaxParty{Country: "AE"}, at("2026-08-31"), "0.0500")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range consumer.Lines {
		if l.Kind == store.TaxKindReverseCharge {
			t.Errorf("a consumer was reverse-charged: %+v", consumer.Lines)
		}
	}
	if consumer.Tax != "50.000000" {
		t.Errorf("a consumer abroad was charged %s; with no rule for its country the issuer's own rate stands (50.000000)", consumer.Tax)
	}
}

// AN EXPIRED EXEMPTION CERTIFICATE is not an exemption. The standard rate
// stands and the audit says exactly why — the alternative is an issuer
// quietly carrying the liability.
func TestExpiredExemptionCertificateFallsBackWithAnAuditEntry(t *testing.T) {
	party := store.TaxParty{
		Country: "OM", Exempt: true, ExemptReason: "Free-zone establishment",
		ExemptionNumber: "EX-2024-117", ExemptionExpires: "2026-06-30",
	}
	_, res, err := ApplyTax([]store.RatedLine{line("ecs.c7.large", "1000.000000")}, "0", twoRateEngine(), party, at("2026-08-31"), "0.0500")
	if err != nil {
		t.Fatal(err)
	}
	if res.Tax != "50.000000" {
		t.Fatalf("tax = %s — an EXPIRED certificate must not exempt anything; want 50.000000", res.Tax)
	}
	if len(res.Lines) != 1 || res.Lines[0].Kind != store.TaxKindStandard {
		t.Fatalf("summary = %+v, want one standard row", res.Lines)
	}
	if len(res.Audit) != 1 {
		t.Fatalf("audit = %+v, want exactly one entry naming the certificate", res.Audit)
	}
	for _, want := range []string{"EX-2024-117", "2026-06-30", "NOT given"} {
		if !strings.Contains(res.Audit[0], want) {
			t.Errorf("audit entry %q does not mention %q", res.Audit[0], want)
		}
	}

	// The SAME certificate, still valid, DOES exempt — otherwise the test
	// above would pass on code that ignores exemptions entirely.
	party.ExemptionExpires = "2027-06-30"
	_, ok, err := ApplyTax([]store.RatedLine{line("ecs.c7.large", "1000.000000")}, "0", twoRateEngine(), party, at("2026-08-31"), "0.0500")
	if err != nil {
		t.Fatal(err)
	}
	if ok.Tax != "0.000000" || ok.Lines[0].Kind != store.TaxKindExempt {
		t.Fatalf("a VALID certificate did not exempt: tax %s kind %q", ok.Tax, ok.Lines[0].Kind)
	}
	if !strings.Contains(ok.Lines[0].Note, "EX-2024-117") {
		t.Errorf("the exempt row does not quote the certificate: %q", ok.Lines[0].Note)
	}
	if len(ok.Audit) != 0 {
		t.Errorf("a valid certificate wrote an audit entry it should not have: %+v", ok.Audit)
	}

	// A certificate is valid THROUGH the day it names, and lapsed the day
	// after: the column is a DATE and an expiry is announced as a day.
	party.ExemptionExpires = "2026-08-31"
	_, lastDay, err := ApplyTax([]store.RatedLine{line("ecs.c7.large", "1000.000000")}, "0", twoRateEngine(), party, at("2026-08-31"), "0.0500")
	if err != nil {
		t.Fatal(err)
	}
	if lastDay.Tax != "0.000000" {
		t.Errorf("a certificate expiring on the LAST DAY of the period did not exempt it: tax %s", lastDay.Tax)
	}
	_, dayAfter, err := ApplyTax([]store.RatedLine{line("ecs.c7.large", "1000.000000")}, "0", twoRateEngine(), party, at("2026-09-01"), "0.0500")
	if err != nil {
		t.Fatal(err)
	}
	if dayAfter.Tax != "50.000000" {
		t.Errorf("a certificate was still honoured the day after it expired: tax %s", dayAfter.Tax)
	}
}

// THE REGRESSION THAT MATTERS. A customer with no tax profile, on a
// Sovereign with no rules, must be rated at EXACTLY the figures
// TotalsWithDiscount produced before §17 existed — to the last digit, across
// a spread of rates and discounts.
func TestSingleRateBehaviourIsUnchangedWithoutAProfile(t *testing.T) {
	lines := []store.RatedLine{
		line("ecs.c7.large", "35.712000"),
		line("plan.m", "45.000000"),
		line("evs.ssd", "16.368000"),
	}
	for _, tc := range []struct{ rate, discount string }{
		{"0.05", "0"},
		{"0.05", "9.708000"},
		{"0", "0"},
		{"0.15", "12.345600"},
		{"0.0825", "0.000001"},
	} {
		wantSub, wantTax, wantTotal, err := TotalsWithDiscount(lines, store.Decimal(tc.discount), store.Decimal(tc.rate))
		if err != nil {
			t.Fatal(err)
		}
		// nil engine = no rules at all, which is what a Sovereign that has
		// never opened Configure → Tax has.
		_, got, err := ApplyTax(lines, store.Decimal(tc.discount), nil, store.TaxParty{}, at("2026-08-31"), store.Decimal(tc.rate))
		if err != nil {
			t.Fatal(err)
		}
		if got.Subtotal != wantSub || got.Tax != wantTax || got.Total != wantTotal {
			t.Errorf("rate %s discount %s: per-rule waterfall gave %s/%s/%s, the pre-§17 waterfall gives %s/%s/%s",
				tc.rate, tc.discount, got.Subtotal, got.Tax, got.Total, wantSub, wantTax, wantTotal)
		}
		// And it reports ONE rate, not a summary block of one row.
		if len(got.Lines) != 0 {
			t.Errorf("rate %s: a customer with no profile got a tax summary %+v; the single rate is the whole story", tc.rate, got.Lines)
		}
		if string(got.Rate) != string(store.Decimal(mustRat(tc.rate).FloatString(4))) {
			t.Errorf("rate %s reported as %s", tc.rate, got.Rate)
		}
	}
}

// The discount split must sum to the discount EXACTLY for any number of
// rules and any awkward figure — that is what makes sum(base) = gross −
// discount an identity rather than a rounding accident.
func TestDiscountApportionmentSumsExactly(t *testing.T) {
	engine := twoRateEngine()
	engine.Rules = append(engine.Rules, store.TaxRule{
		ID: "exempt-training", Name: "Exempt training", Country: "OM", Category: "training", Rate: "0", Kind: store.TaxKindExempt,
		Note: "Exempt supply.", EffectiveFrom: "2021-04-16",
	})
	engine.Categories = append(engine.Categories, store.TaxCategoryRule{SKU: "svc.training", Category: "training"})
	lines := []store.RatedLine{
		line("ecs.c7.large", "33.333333"),
		line("evs.ssd", "33.333333"),
		line("svc.training", "33.333334"),
	}
	for _, discount := range []string{"0.000001", "1.000000", "33.333333", "99.999999", "100.000000"} {
		_, res, err := ApplyTax(lines, store.Decimal(discount), engine, store.TaxParty{Country: "OM"}, at("2026-08-31"), "0.0500")
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Lines) != 3 {
			t.Fatalf("discount %s: %d summary rows, want 3", discount, len(res.Lines))
		}
		sum := new(big.Rat)
		for _, l := range res.Lines {
			sum.Add(sum, mustRat(string(l.Base)))
		}
		want := new(big.Rat).Sub(mustRat("100.000000"), mustRat(discount))
		if sum.Cmp(want) != 0 {
			t.Errorf("discount %s: the bases sum to %s, want gross − discount = %s", discount, sum.FloatString(6), want.FloatString(6))
		}
	}
}

// A rule that has EXPIRED does not rate a later period, and a rule that
// starts later does not rate an earlier one: validity is a date range, which
// is the whole point of recording one.
func TestRuleValidityIsRespected(t *testing.T) {
	engine := &store.TaxEngine{
		SellerCountry: "OM", DefaultRate: "0",
		Rules: []store.TaxRule{
			{ID: "old", Name: "Old rate", Country: "OM", Rate: "0.0500", Kind: store.TaxKindStandard, EffectiveFrom: "2021-04-16", EffectiveTo: "2026-09-01"},
			{ID: "new", Name: "New rate", Country: "OM", Rate: "0.1000", Kind: store.TaxKindStandard, EffectiveFrom: "2026-09-01"},
		},
	}
	for _, tc := range []struct{ day, rule, tax string }{
		{"2026-08-31", "old", "50.000000"},
		{"2026-09-01", "new", "100.000000"},
	} {
		_, res, err := ApplyTax([]store.RatedLine{line("ecs.c7.large", "1000.000000")}, "0", engine, store.TaxParty{Country: "OM"}, at(tc.day), "0")
		if err != nil {
			t.Fatal(err)
		}
		if res.Lines[0].RuleID != tc.rule || string(res.Tax) != tc.tax {
			t.Errorf("on %s the rule was %q at %s, want %q at %s", tc.day, res.Lines[0].RuleID, res.Tax, tc.rule, tc.tax)
		}
	}
}
