package api

import (
	"testing"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// statementsInPeriod narrows a customer's list to one YYYY-MM when the
// operator gives both `customer_id` and `period` (the per-customer store
// query has no period argument).
func TestStatementsInPeriod(t *testing.T) {
	list := []store.Statement{
		{ID: "aug", PeriodStart: "2026-08-01", PeriodEnd: "2026-08-31"},
		{ID: "jul", PeriodStart: "2026-07-01", PeriodEnd: "2026-07-31"},
		{ID: "aug-25", PeriodStart: "2025-08-01", PeriodEnd: "2025-08-31"},
	}
	ids := func(l []store.Statement) []string {
		out := []string{}
		for _, s := range l {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(statementsInPeriod(list, "")); len(got) != 3 {
		t.Fatalf("no period = %v", got)
	}
	if got := ids(statementsInPeriod(list, "2026-08")); len(got) != 1 || got[0] != "aug" {
		t.Fatalf("2026-08 = %v", got)
	}
	if got := ids(statementsInPeriod(list, "2026-07")); len(got) != 1 || got[0] != "jul" {
		t.Fatalf("2026-07 = %v", got)
	}
	// A period nothing matches is an empty list, never nil — the wire shape
	// is `"statements": []`.
	if got := statementsInPeriod(list, "2026-06"); got == nil || len(got) != 0 {
		t.Fatalf("2026-06 = %v", got)
	}
	// The month is matched whole: "2026-0" is not a period, and a year-only
	// prefix must not sweep every month of that year in.
	if got := ids(statementsInPeriod(list, "2026")); len(got) != 0 {
		t.Fatalf("bare year = %v", got)
	}
}
