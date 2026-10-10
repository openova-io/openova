package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The packages document (DESIGN.md §22.4) — the storefront's contract, built
// from a book, its cells, its package settings and the features. Pure, so
// the shape is pinned without a database: the groups in order, the floor
// apart from the cells, packages by price with their settings, shape and the
// STEP-UP RULE against the next (holding on one gap and failing on the
// other), features in matrix order with every plan given a cell, money at
// the minor unit, and each kind's cell — the boolean add-on with its price
// and hint, the teaser, the quantity with its overage, the level with its
// purchasable next level, the access door with its note.
func TestPackagesDocumentShape(t *testing.T) {
	book := store.PriceBook{Name: store.PlanBookName, Currency: "OMR", UpdatedAt: time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)}
	// Deliberately out of price order, so the ordering is the document's.
	// The plans at the workbook's prices: 2.490 / 4.490 / 13.990 a month.
	book.Items = []store.PriceItem{
		{SKU: "plan.xl", Unit: store.PlanUnit, UnitPrice: "0.01916438"},
		{SKU: "plan.s", Unit: store.PlanUnit, UnitPrice: "0.00341096"},
		{SKU: "plan.m", Unit: store.PlanUnit, UnitPrice: "0.00615068"},
		{SKU: "addon.backup", Unit: store.PlanUnit, UnitPrice: "0.00205479"},
		{SKU: "addon.ai_seo", Unit: store.PlanUnit, UnitPrice: "0.00273973"},
		{SKU: "addon.dr", Unit: store.PlanUnit, UnitPrice: "0.01095890"},
		{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", UnitPrice: "0.01716667"},
		{SKU: "k8s.vcpu", Unit: "vcpu-hour", UnitPrice: "0.00273973"},
	}
	ssl := store.Feature{ID: "f0", Key: "ssl", Name: "Unlimited free SSL", Blurb: "Certificates for every site", Kind: store.FeatureKindBoolean, Group: store.FeatureGroupFloor, SortOrder: 1}
	backup := store.Feature{ID: "f1", Key: "backup", Name: "Backup", Blurb: "Daily backups, kept 30 days", Kind: store.FeatureKindBoolean, Group: store.FeatureGroupResilience, AddonSKU: "addon.backup", SortOrder: 4}
	seo := store.Feature{ID: "f2", Key: "ai_seo", Name: "AI SEO ready", Kind: store.FeatureKindBoolean, Group: store.FeatureGroupFeatures, AddonSKU: "addon.ai_seo", SortOrder: 5}
	vuln := store.Feature{ID: "f3", Key: "vuln", Name: "Vulnerability dashboard", Kind: store.FeatureKindBoolean, Group: store.FeatureGroupOps, Teaser: true, SortOrder: 6}
	bw := store.Feature{ID: "f4", Key: "bandwidth", Name: "Bandwidth", Kind: store.FeatureKindQuantity, Group: store.FeatureGroupCapacity, Unit: "Mbps", AddonSKU: "eip.bandwidth_mbps", SortOrder: 2}
	dr := store.Feature{ID: "f5", Key: "dr", Name: "DR topology", Kind: store.FeatureKindLevel, Group: store.FeatureGroupResilience, AddonSKU: "addon.dr", Levels: []string{"single region", "active-passive"}, SortOrder: 7}
	gitea := store.Feature{ID: "f6", Key: "gitea_iac", Name: "Gitea + IaC", Kind: store.FeatureKindAccess, Group: store.FeatureGroupAccess, SortOrder: 3}
	orphan := store.Feature{ID: "f7", Key: "sso", Name: "SSO", Kind: store.FeatureKindBoolean, SortOrder: 8}
	lvl := func(n int) *int { return &n }
	cells := []store.Entitlement{
		{PlanSKU: "plan.s", FeatureID: "f1", State: store.EntitlementOptional, Feature: backup, Note: "7-day retention on S"},
		{PlanSKU: "plan.m", FeatureID: "f1", State: store.EntitlementOptional, Feature: backup},
		{PlanSKU: "plan.xl", FeatureID: "f1", State: store.EntitlementIncluded, Feature: backup},
		{PlanSKU: "plan.s", FeatureID: "f2", State: store.EntitlementOptional, Feature: seo},
		{PlanSKU: "plan.m", FeatureID: "f2", State: store.EntitlementIncluded, Feature: seo},
		{PlanSKU: "plan.xl", FeatureID: "f2", State: store.EntitlementIncluded, Feature: seo},
		{PlanSKU: "plan.m", FeatureID: "f3", State: store.EntitlementIncluded, Feature: vuln},
		{PlanSKU: "plan.xl", FeatureID: "f3", State: store.EntitlementIncluded, Feature: vuln},
		{PlanSKU: "plan.s", FeatureID: "f4", State: store.EntitlementIncluded, IncludedQuantity: dc("50.000000"), Overage: store.OverageHardCap, Feature: bw},
		{PlanSKU: "plan.m", FeatureID: "f4", State: store.EntitlementIncluded, IncludedQuantity: dc("100"), Overage: store.OverageMetered, Feature: bw},
		{PlanSKU: "plan.s", FeatureID: "f5", State: store.EntitlementOptional, Level: lvl(0), Feature: dr},
		{PlanSKU: "plan.m", FeatureID: "f5", State: store.EntitlementIncluded, Level: lvl(0), Feature: dr},
		{PlanSKU: "plan.xl", FeatureID: "f5", State: store.EntitlementIncluded, Level: lvl(1), Feature: dr},
		{PlanSKU: "plan.s", FeatureID: "f6", State: store.EntitlementNotOffered, Feature: gitea},
		{PlanSKU: "plan.m", FeatureID: "f6", State: store.EntitlementIncluded, Note: "read", Feature: gitea},
		{PlanSKU: "plan.xl", FeatureID: "f6", State: store.EntitlementIncluded, Feature: gitea},
	}
	settings := map[string]store.PackageSettings{
		"plan.m": {PlanSKU: "plan.m", Recommended: true, AnnualMonthsFree: 0, VCPU: dc("2"), MemoryGB: dc("4.0000"), VCPUGuaranteed: dc("0.3300"), MemoryGBGuaranteed: dc("1.33"), DiskGB: dc("50")},
	}
	// Features in matrix order; `orphan` has no cell in this book and is left out.
	doc, err := packagesDocument(book, []store.Feature{ssl, bw, gitea, backup, seo, vuln, dr, orphan}, cells, settings)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["currency"] != "OMR" || got["price_book"] != store.PlanBookName || got["prices_as_of"] != "2026-10-10" {
		t.Fatalf("header = %v", got)
	}
	// The groups, in the order a package is read.
	var groupKeys []string
	for _, g := range got["groups"].([]any) {
		groupKeys = append(groupKeys, g.(map[string]any)["key"].(string))
	}
	if want := []string{"capacity", "features", "access", "ops", "scope", "resilience", "service"}; !sameStrings(groupKeys, want) {
		t.Fatalf("groups = %v, want %v", groupKeys, want)
	}
	if got["groups"].([]any)[3].(map[string]any)["name"] != "Managed operations" {
		t.Fatalf("groups = %v", got["groups"])
	}
	// The floor: once, apart from the cells, never a feature row.
	floor := got["floor"].([]any)
	if len(floor) != 1 || floor[0].(map[string]any)["key"] != "ssl" || floor[0].(map[string]any)["name"] != "Unlimited free SSL" || floor[0].(map[string]any)["blurb"] != "Certificates for every site" {
		t.Fatalf("floor = %v", floor)
	}
	// The packages, cheapest first, priced at the minor unit.
	pk := got["packages"].([]any)
	if len(pk) != 3 {
		t.Fatalf("packages = %v", pk)
	}
	want := []struct{ sku, name, price string }{{"plan.s", "S", "2.490"}, {"plan.m", "M", "4.490"}, {"plan.xl", "XL", "13.990"}}
	for i, w := range want {
		p := pk[i].(map[string]any)
		if p["sku"] != w.sku || p["name"] != w.name || p["price_month"] != w.price {
			t.Fatalf("package %d = %v, want %+v", i, p, w)
		}
	}
	// Settings: M carries its shape, the recommended flag and the term rule;
	// S has none, so its shape falls back to the catalog's constants and the
	// guaranteed floors are absent.
	s, m, xl := pk[0].(map[string]any), pk[1].(map[string]any), pk[2].(map[string]any)
	if m["tagline"] != "" || m["recommended"] != true || m["annual_months_free"] != float64(0) {
		t.Fatalf("M settings = %v", m)
	}
	if sh := m["shape"].(map[string]any); sh["vcpu"] != float64(2) || sh["memory_gb"] != float64(4) || sh["vcpu_guaranteed"] != 0.33 || sh["memory_gb_guaranteed"] != 1.33 || sh["disk_gb"] != float64(50) {
		t.Fatalf("M shape = %v", sh)
	}
	if sh := s["shape"].(map[string]any); sh["vcpu"] != float64(2) || sh["memory_gb"] != float64(4) || sh["vcpu_guaranteed"] != nil || sh["disk_gb"] != nil || s["recommended"] != false {
		t.Fatalf("S shape = %v (recommended %v)", sh, s["recommended"])
	}
	// includes: the shape's headline plus the included bandwidth, as numbers.
	if inc := s["includes"].(map[string]any); inc["vcpu"] != float64(2) || inc["memory_gb"] != float64(4) || inc["bandwidth_mbps"] != float64(50) {
		t.Fatalf("S includes = %v", inc)
	}
	if inc := m["includes"].(map[string]any); inc["bandwidth_mbps"] != float64(100) {
		t.Fatalf("M includes = %v", inc)
	}
	// The step-up rule. S → M: gap 2.000; ai_seo is optional on S at 2.000
	// and included on M, backup is optional on both, DR's next level on S is
	// level 1 and M is at 0 — bundled = [ai_seo], 2.000 ≥ 2.000, holds.
	su := s["step_up"].(map[string]any)
	if su["next_sku"] != "plan.m" || su["next_name"] != "M" || su["gap_month"] != "2.000" || su["bundled_addons_sum_month"] != "2.000" || su["rule_holds"] != true {
		t.Fatalf("S step-up = %v", su)
	}
	if keys := su["bundled_addon_keys"].([]any); len(keys) != 1 || keys[0] != "ai_seo" {
		t.Fatalf("S bundled = %v", keys)
	}
	// M → XL: gap 9.500; only backup (1.500) is optional on M and included
	// on XL — 1.500 < 9.500, the rule does NOT hold.
	su = m["step_up"].(map[string]any)
	if su["next_sku"] != "plan.xl" || su["gap_month"] != "9.500" || su["bundled_addons_sum_month"] != "1.500" || su["rule_holds"] != false {
		t.Fatalf("M step-up = %v", su)
	}
	if keys := su["bundled_addon_keys"].([]any); len(keys) != 1 || keys[0] != "backup" {
		t.Fatalf("M bundled = %v", keys)
	}
	if _, has := xl["step_up"]; has {
		t.Fatalf("the last package has no step-up: %v", xl)
	}
	// The features, in matrix order (sort order), floor and orphan left out.
	fs := got["features"].([]any)
	var keys []string
	for _, f := range fs {
		keys = append(keys, f.(map[string]any)["key"].(string))
	}
	if want := []string{"bandwidth", "gitea_iac", "backup", "ai_seo", "vuln", "dr"}; !sameStrings(keys, want) {
		t.Fatalf("features = %v, want %v", keys, want)
	}
	byKey := map[string]map[string]any{}
	for _, f := range fs {
		m := f.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	// Boolean with an add-on: group, kind, the add-on SKU, no unit, no levels.
	bk := byKey["backup"]
	if bk["name"] != "Backup" || bk["blurb"] != "Daily backups, kept 30 days" || bk["group"] != "resilience" || bk["kind"] != "boolean" || bk["addon_sku"] != "addon.backup" || bk["teaser"] != false {
		t.Fatalf("backup = %v", bk)
	}
	if _, has := bk["unit"]; has {
		t.Fatalf("a boolean feature carries no unit: %v", bk)
	}
	if _, has := bk["levels"]; has {
		t.Fatalf("a boolean feature carries no levels: %v", bk)
	}
	bc := bk["cells"].(map[string]any)
	if c := bc["plan.s"].(map[string]any); c["state"] != "optional" || c["addon_sku"] != "addon.backup" || c["price_month"] != "1.500" || c["included_from"] != "plan.xl" || c["note"] != "7-day retention on S" {
		t.Fatalf("backup on S = %v", c)
	}
	if c := bc["plan.m"].(map[string]any); c["state"] != "optional" || c["price_month"] != "1.500" || c["included_from"] != "plan.xl" {
		t.Fatalf("backup on M = %v", c)
	}
	if _, has := bc["plan.m"].(map[string]any)["note"]; has {
		t.Fatalf("an empty note is omitted: %v", bc["plan.m"])
	}
	if c := bc["plan.xl"].(map[string]any); c["state"] != "included" || len(c) != 1 {
		t.Fatalf("backup on XL = %v, want {state: included} and nothing else", c)
	}
	// Teaser: a not-offered cell on a teaser feature is published as a
	// teaser naming the first package that includes it; the feature says so.
	vc := byKey["vuln"]
	if vc["teaser"] != true || vc["group"] != "ops" {
		t.Fatalf("vuln = %v", vc)
	}
	if c := vc["cells"].(map[string]any)["plan.s"].(map[string]any); c["state"] != "teaser" || c["included_from"] != "plan.m" || len(c) != 2 {
		t.Fatalf("vuln on S = %v, want a teaser from M", c)
	}
	// Quantity: the unit, the quantity and the overage per cell; a package
	// with no cell reads not offered, and is not a teaser (no teaser flag).
	bw2 := byKey["bandwidth"]
	if bw2["kind"] != "quantity" || bw2["unit"] != "Mbps" || bw2["group"] != "capacity" || bw2["addon_sku"] != "eip.bandwidth_mbps" {
		t.Fatalf("bandwidth = %v", bw2)
	}
	bwc := bw2["cells"].(map[string]any)
	if c := bwc["plan.s"].(map[string]any); c["state"] != "included" || c["quantity"] != float64(50) || c["overage"] != "hard_cap" {
		t.Fatalf("bandwidth on S = %v", c)
	}
	if c := bwc["plan.m"].(map[string]any); c["quantity"] != float64(100) || c["overage"] != "metered" {
		t.Fatalf("bandwidth on M = %v", c)
	}
	// No cell on XL: not offered (not a teaser — the feature does not tease),
	// with the hint naming the cheapest package that includes it.
	if c := bwc["plan.xl"].(map[string]any); c["state"] != "not_offered" || c["included_from"] != "plan.s" || len(c) != 2 {
		t.Fatalf("bandwidth on XL (no cell) = %v, want not_offered with the hint", c)
	}
	// Level: the labels on the feature; every cell included at its level;
	// the purchasable next level on S with its price; included_from is the
	// first package at a level above.
	drc := byKey["dr"]
	if drc["kind"] != "level" || drc["addon_sku"] != "addon.dr" {
		t.Fatalf("dr = %v", drc)
	}
	if lv := drc["levels"].([]any); len(lv) != 2 || lv[0] != "single region" || lv[1] != "active-passive" {
		t.Fatalf("dr levels = %v", lv)
	}
	dcells := drc["cells"].(map[string]any)
	if c := dcells["plan.s"].(map[string]any); c["state"] != "included" || c["level"] != float64(0) || c["included_from"] != "plan.xl" {
		t.Fatalf("dr on S = %v", c)
	}
	if nl := dcells["plan.s"].(map[string]any)["next_level_addon"].(map[string]any); nl["addon_sku"] != "addon.dr" || nl["price_month"] != "8.000" {
		t.Fatalf("dr next level on S = %v", nl)
	}
	if c := dcells["plan.m"].(map[string]any); c["state"] != "included" || c["level"] != float64(0) || c["included_from"] != "plan.xl" || c["next_level_addon"] != nil {
		t.Fatalf("dr on M = %v", c)
	}
	if c := dcells["plan.xl"].(map[string]any); c["state"] != "included" || c["level"] != float64(1) || c["included_from"] != nil {
		t.Fatalf("dr on XL = %v", c)
	}
	// Access: included or not offered, the note published.
	gc := byKey["gitea_iac"]
	if gc["kind"] != "access" || gc["group"] != "access" {
		t.Fatalf("gitea = %v", gc)
	}
	gcells := gc["cells"].(map[string]any)
	if c := gcells["plan.s"].(map[string]any); c["state"] != "not_offered" || c["included_from"] != "plan.m" {
		t.Fatalf("gitea on S = %v", c)
	}
	if c := gcells["plan.m"].(map[string]any); c["state"] != "included" || c["note"] != "read" {
		t.Fatalf("gitea on M = %v", c)
	}
	// The document is byte-stable on the wire: numbers never float-render.
	if string(b) == "" || jsonHas(b, "50.000000") || jsonHas(b, "0.3300") {
		t.Fatalf("quantities and shapes are trimmed on the wire: %s", b)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func jsonHas(b []byte, s string) bool { return len(b) > 0 && containsBytes(b, s) }

func containsBytes(b []byte, s string) bool {
	return len(s) <= len(b) && func() bool {
		for i := 0; i+len(s) <= len(b); i++ {
			if string(b[i:i+len(s)]) == s {
				return true
			}
		}
		return false
	}()
}

func dc(s string) *store.Decimal { d := store.Decimal(s); return &d }

// includesKey joins the feature key and its unit, lower-case, non-alphanumerics
// folded: "Mbps" → bandwidth_mbps, "GB" → storage_gb, a unit-less key alone.
func TestIncludesKey(t *testing.T) {
	for _, tc := range []struct{ key, unit, want string }{
		{"bandwidth", "Mbps", "bandwidth_mbps"},
		{"storage", "GB", "storage_gb"},
		{"mail", "accounts/month", "mail_accounts_month"},
		{"x", "", "x"},
	} {
		if got := includesKey(store.Feature{Key: tc.key, Unit: tc.unit}); got != tc.want {
			t.Fatalf("includesKey(%s, %s) = %s, want %s", tc.key, tc.unit, got, tc.want)
		}
	}
}
