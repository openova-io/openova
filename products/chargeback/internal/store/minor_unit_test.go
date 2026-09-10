package store

import (
	"math/big"
	"testing"
	"time"
)

func mustTime(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

// The minor-unit rule a payment is judged by (DESIGN.md §8.6). Money is
// added at six decimals; it moves at the currency's minor unit, and the
// overpayment check and the paid transition must agree with what a bank can
// carry. No database: the rule is pure arithmetic.

func TestMinorUnitDigitsTable(t *testing.T) {
	for cur, want := range map[string]int{
		"OMR": 3, "BHD": 3, "KWD": 3, "JOD": 3, "IQD": 3, "LYD": 3, "TND": 3,
		"omr": 3, " omr ": 3,
		"USD": 2, "EUR": 2, "AED": 2, "SAR": 2, "GBP": 2, "": 2, "XYZ": 2,
	} {
		if got := minorUnitDigits(cur); got != want {
			t.Errorf("minorUnitDigits(%q) = %d, want %d", cur, got, want)
		}
	}
	if got := minorUnitTolerance("OMR"); got.Cmp(big.NewRat(5, 10000)) != 0 {
		t.Errorf("minorUnitTolerance(OMR) = %s, want 0.0005", got.FloatString(6))
	}
	if got := minorUnitTolerance("USD"); got.Cmp(big.NewRat(5, 1000)) != 0 {
		t.Errorf("minorUnitTolerance(USD) = %s, want 0.005", got.FloatString(6))
	}
}

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic(s)
	}
	return r
}

// The hw307 case: 14.856782 owed, 10.000 paid, then the 4.857 the dialog
// prefills. Over by 0.000218 — less than half a baisa — settles; 4.858, over
// by 0.001218, is an overpayment.
func TestSettlementIsJudgedAtTheMinorUnit(t *testing.T) {
	cases := []struct {
		remaining, currency string
		settles, overpays   bool
	}{
		{"0", "OMR", true, false},
		{"-0.000218", "OMR", true, false}, // 4.857 against 4.856782
		{"-0.001218", "OMR", false, true}, // 4.858 against 4.856782
		{"-0.0005", "OMR", false, true},   // exactly half a baisa over: refused
		{"-0.000499", "OMR", true, false}, // just under half: settled
		{"0.000499", "OMR", true, false},  // short by less than half: settled
		{"0.0005", "OMR", false, false},   // short by half a baisa: still owed
		{"4.856782", "OMR", false, false}, // the part-paid balance
		{"-0.004", "USD", true, false},    // under half a cent over: settled
		{"-0.005", "USD", false, true},    // half a cent over: refused
		{"-0.000218", "USD", true, false},
		{"-0.004", "", true, false},       // unknown currency: two decimals
		{"-0.001218", "usd", true, false}, // case-folded, two decimals
	}
	for _, c := range cases {
		r := rat(c.remaining)
		if got := settlesAt(r, c.currency); got != c.settles {
			t.Errorf("settlesAt(%s %s) = %v, want %v", c.remaining, c.currency, got, c.settles)
		}
		if got := overpaysAt(r, c.currency); got != c.overpays {
			t.Errorf("overpaysAt(%s %s) = %v, want %v", c.remaining, c.currency, got, c.overpays)
		}
	}
}

// The balance the API reports is floored at zero: a settlement that
// overshot by less than half a unit owes nothing, never a negative amount.
func TestOutstandingNeverNegative(t *testing.T) {
	st := Statement{Currency: "OMR", Total: "14.856782", Paid: "14.857000"}
	if got := st.OutstandingAt(); got != "0.000000" {
		t.Errorf("OutstandingAt after an over-settlement = %s, want 0.000000", got)
	}
	st.Paid = "10.000000"
	if got := st.OutstandingAt(); got != "4.856782" {
		t.Errorf("OutstandingAt part-paid = %s, want 4.856782", got)
	}
	// A sent statement past its due date is not overdue when what is left
	// is zero at the minor unit.
	due := mustTime("2026-08-31T00:00:00Z")
	now := mustTime("2026-09-10T00:00:00Z")
	settled := Statement{Status: StatusSent, Currency: "OMR", Total: "14.856782", Paid: "14.857000", DueAt: &due}
	if got := settled.EffectiveStatusAt(now); got != StatusSent {
		t.Errorf("a settled statement reads %s, want sent", got)
	}
	open := Statement{Status: StatusSent, Currency: "OMR", Total: "14.856782", Paid: "10.000000", DueAt: &due}
	if got := open.EffectiveStatusAt(now); got != StatusOverdue {
		t.Errorf("an open statement past due reads %s, want overdue", got)
	}
}
