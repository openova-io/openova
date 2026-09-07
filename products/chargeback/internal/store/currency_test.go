package store

import "testing"

// Rate math (#6867 follow-up). per_base is units of the BOOK currency per
// one reporting unit, so converting a book amount DIVIDES by it. The first
// case discriminates the two directions: 26 USD at USD 2.6 per OMR is
// 10 OMR; multiplying would print 67.6, and a build that does must fail.
func TestToBaseDividesByPerBase(t *testing.T) {
	got, ok := ToBase("26", "2.6")
	if !ok || got != "10.000000" {
		t.Fatalf("26 USD / 2.6 = %q ok=%v, want 10.000000 (multiplying would give 67.600000)", got, ok)
	}
	if got == "67.600000" {
		t.Fatal("ToBase multiplied by the rate")
	}
	// A non-terminating quotient is exact until the final 6-decimal render:
	// 84 / 2.6 = 32.307692307…, not the float64 neighbour of it.
	if got, ok := ToBase("84", "2.6"); !ok || got != "32.307692" {
		t.Fatalf("84 / 2.6 = %q ok=%v", got, ok)
	}
	// A rate below 1 scales up: 67.2 EUR at 0.42 EUR per OMR is 160 OMR.
	if got, ok := ToBase("67.2", "0.42"); !ok || got != "160.000000" {
		t.Fatalf("67.2 / 0.42 = %q ok=%v", got, ok)
	}
	// The reporting currency's rate is 1: the amount is unchanged.
	if got, ok := ToBase("84.5", "1"); !ok || got != "84.500000" {
		t.Fatalf("84.5 / 1 = %q ok=%v", got, ok)
	}
	// Zero converts to zero at any rate.
	if got, ok := ToBase("0", "2.6"); !ok || got != "0.000000" {
		t.Fatalf("0 / 2.6 = %q ok=%v", got, ok)
	}
	// No rate ("" — what RateToBase returns for an unconverted currency),
	// a zero or a negative rate: not converted, amount handed back as-is.
	for _, bad := range []Decimal{"", "0", "-2.6", "abc"} {
		if got, ok := ToBase("26", bad); ok || got != "26" {
			t.Fatalf("ToBase(26, %q) = %q ok=%v, want unchanged and !ok", bad, got, ok)
		}
	}
}

func TestNormalizeCurrencyCode(t *testing.T) {
	for in, want := range map[string]string{" usd ": "USD", "Omr": "OMR", "EUR": "EUR"} {
		if got, ok := NormalizeCurrencyCode(in); !ok || got != want {
			t.Fatalf("NormalizeCurrencyCode(%q) = %q ok=%v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "US", "USDX", "US1", "U$D", "омр"} {
		if _, ok := NormalizeCurrencyCode(bad); ok {
			t.Fatalf("%q must not be a currency code", bad)
		}
	}
}

// The SQL side must divide too. The expression is text, so the shape is
// pinned here: cost over the rate, never cost times it, and the reporting
// currency short-circuits to 1 before the stored rate is consulted.
func TestCostBaseExprDividesByRate(t *testing.T) {
	if !containsAll(costBaseExpr, ") / (", "x.per_base", "THEN 1 ELSE") {
		t.Fatalf("costBaseExpr = %s", costBaseExpr)
	}
	if containsAll(costBaseExpr, ") * (") {
		t.Fatal("costBaseExpr multiplies by the rate")
	}
	if !containsAll(costPriceJoinSQL, "LEFT JOIN currency_rates x ON x.code = b.currency") {
		t.Fatalf("costPriceJoinSQL lacks the rate join: %s", costPriceJoinSQL)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
