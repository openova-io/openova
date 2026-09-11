package store

import (
	"encoding/json"
	"math/big"
	"testing"
)

// The tax engine (DESIGN.md §17) freezes its per-rule summary onto the
// statement as TaxSnapshot.Lines, keyed `rule_id` / `rule_name`. The journal
// (§18) reads that snapshot to book tax. These two were built independently,
// and a reader that only knew the shorter `code` / `name` keys still BALANCED
// — it just posted every tax line anonymously, which is invisible in any test
// that checks totals. This pins the join itself: the rule identity has to
// survive the round trip, not merely the amount.
func TestJournalReadsTheRuleIdentityTheTaxEngineFroze(t *testing.T) {
	snap := TaxSnapshot{
		Rate: "0.05",
		Lines: []TaxLine{
			{RuleID: "om-vat-standard", RuleName: "Oman VAT 5%", Kind: "standard", Category: "compute", Rate: "0.05", Base: "800.000000", Tax: "40.000000"},
			{RuleID: "om-vat-zero", RuleName: "Oman VAT zero-rated", Kind: "zero_rated", Category: "export", Rate: "0", Base: "200.000000", Tax: "0.000000"},
		},
	}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := taxRulesOf(raw, "40.000000")
	if len(got) != 2 {
		t.Fatalf("want 2 tax lines, got %d: %+v", len(got), got)
	}
	for i, want := range snap.Lines {
		if got[i].RuleID != want.RuleID {
			t.Errorf("line %d rule id = %q, want %q — the journal would post this tax anonymously", i, got[i].RuleID, want.RuleID)
		}
		if got[i].RuleName != want.RuleName {
			t.Errorf("line %d rule name = %q, want %q", i, got[i].RuleName, want.RuleName)
		}
		if got[i].Kind != want.Kind {
			t.Errorf("line %d kind = %q, want %q — zero-rated and exempt are different lines on a return", i, got[i].Kind, want.Kind)
		}
		if got[i].Category != want.Category {
			t.Errorf("line %d category = %q, want %q", i, got[i].Category, want.Category)
		}
	}

	// Whatever it names them, the lines must still account for exactly the
	// tax the invoice froze.
	sum := new(big.Rat)
	for _, l := range got {
		sum.Add(sum, ratOf(l.Tax))
	}
	if sum.Cmp(ratOf("40.000000")) != 0 {
		t.Errorf("tax lines sum to %s, want 40.000000", sum.FloatString(6))
	}
}

// A snapshot written with the shorter key names still names its rules, so an
// invoice frozen by an earlier build does not regress to anonymous postings.
func TestJournalStillReadsTheShorterSnapshotKeys(t *testing.T) {
	raw := []byte(`{"rate":"0.05","lines":[{"code":"VAT-5","name":"VAT standard","rate":"0.05","amount":"40.000000"}]}`)
	got := taxRulesOf(raw, "40.000000")
	if len(got) != 1 {
		t.Fatalf("want 1 line, got %d", len(got))
	}
	if got[0].RuleID != "VAT-5" || got[0].RuleName != "VAT standard" {
		t.Errorf("id/name = %q/%q, want VAT-5/VAT standard", got[0].RuleID, got[0].RuleName)
	}
	if ratOf(got[0].Tax).Cmp(ratOf("40.000000")) != 0 {
		t.Errorf("tax = %s, want 40.000000 (read from `amount`)", got[0].Tax)
	}
}
