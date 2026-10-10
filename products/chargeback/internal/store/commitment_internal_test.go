package store

import (
	"strings"
	"testing"
)

// The explorer prices a package's plan-hours at the flat monthly price over
// the hours of the month (DESIGN.md §22.11) for exactly the catalog's
// packages; the SQL list is a constant, so it is held to catalogPlans here.
func TestPackagePlanSKUListMatchesTheCatalog(t *testing.T) {
	var want []string
	for _, p := range catalogPlans {
		if PlanBillable(p.Slug) {
			want = append(want, "'"+PlanSKU(p.Slug)+"'")
		}
	}
	if got := strings.Join(want, ", "); got != packagePlanSKUListSQL {
		t.Fatalf("packagePlanSKUListSQL = %s, the catalog says %s", packagePlanSKUListSQL, got)
	}
}
