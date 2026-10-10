// org_plan_flexi_retired_6971_test.go — flexi retired (#6971, founder model
// 2026-10-10): the create door stops minting Organizations on flexi. Pay as
// you go is grow mode on a package; an Organization already on flexi keeps its
// CR and the org-controller renders it as S in grow mode.
package handler

import "testing"

func TestCatalogPlanSlugs_FlexiIsNoLongerSold(t *testing.T) {
	for _, s := range catalogPlanSlugs {
		if s == "flexi" {
			t.Fatal("catalogPlanSlugs still offers flexi — a new Organization could be minted on the retired plan")
		}
	}
	if isCatalogPlanSlug("flexi") {
		t.Fatal("isCatalogPlanSlug(flexi) = true; the create door would keep it instead of coercing it to s")
	}
	for _, s := range []string{"s", "m", "l", "xl"} {
		if !isCatalogPlanSlug(s) {
			t.Errorf("isCatalogPlanSlug(%q) = false; the package is no longer sold", s)
		}
	}
}
