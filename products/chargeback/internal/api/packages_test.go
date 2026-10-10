package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// The packages document (DESIGN.md §22) — the storefront's contract, built
// from a book, its cells and the features. Pure, so the shape is pinned
// without a database: packages by price, features in matrix order, every
// plan given a cell, money at the minor unit, included_from the cheapest
// package that includes the feature.
func TestPackagesDocumentShape(t *testing.T) {
	book := store.PriceBook{Name: store.PlanBookName, Currency: "OMR", UpdatedAt: time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)}
	// Deliberately out of price order, so the ordering is the document's.
	book.Items = []store.PriceItem{
		{SKU: "plan.xl", Unit: store.PlanUnit, UnitPrice: "0.04109589"},
		{SKU: "plan.s", Unit: store.PlanUnit, UnitPrice: "0.00684932"},
		{SKU: "plan.m", Unit: store.PlanUnit, UnitPrice: "0.01232877"},
		{SKU: "addon.backup", Unit: store.PlanUnit, UnitPrice: "0.00205479"},
		{SKU: "eip.bandwidth_mbps", Unit: "mbps-hour", UnitPrice: "0.01716667"},
		{SKU: "k8s.vcpu", Unit: "vcpu-hour", UnitPrice: "0.00273973"},
	}
	backup := store.Feature{ID: "f1", Key: "backup", Name: "Backup", Blurb: "Daily backups, kept 30 days", Kind: store.FeatureKindBoolean, AddonSKU: "addon.backup", SortOrder: 4}
	bw := store.Feature{ID: "f2", Key: "bandwidth", Name: "Bandwidth", Kind: store.FeatureKindQuantity, Unit: "Mbps", AddonSKU: "eip.bandwidth_mbps", SortOrder: 15}
	ip := store.Feature{ID: "f3", Key: "dedicated_ip", Name: "Dedicated IP address", Kind: store.FeatureKindBoolean, AddonSKU: "addon.dedicated_ip", SortOrder: 5}
	orphan := store.Feature{ID: "f4", Key: "sso", Name: "SSO", Kind: store.FeatureKindBoolean, SortOrder: 1}
	cells := []store.Entitlement{
		{PlanSKU: "plan.s", FeatureID: "f1", State: store.EntitlementOptional, Feature: backup, Note: "7-day retention on S"},
		{PlanSKU: "plan.m", FeatureID: "f1", State: store.EntitlementOptional, Feature: backup},
		{PlanSKU: "plan.xl", FeatureID: "f1", State: store.EntitlementIncluded, Feature: backup},
		{PlanSKU: "plan.s", FeatureID: "f2", State: store.EntitlementIncluded, IncludedQuantity: dc("50.000000"), Feature: bw},
		{PlanSKU: "plan.m", FeatureID: "f2", State: store.EntitlementIncluded, IncludedQuantity: dc("100"), Feature: bw},
		{PlanSKU: "plan.xl", FeatureID: "f2", State: store.EntitlementIncluded, IncludedQuantity: dc("1000"), Feature: bw},
		// Optional on every package but its add-on is not priced in the book.
		{PlanSKU: "plan.s", FeatureID: "f3", State: store.EntitlementOptional, Feature: ip},
	}
	// Features in matrix order; `orphan` has no cell in this book and is left out.
	doc, err := packagesDocument(book, []store.Feature{orphan, backup, ip, bw}, cells)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["currency"] != "OMR" || got["price_book"] != store.PlanBookName || got["prices_as_of"] != "2026-09-11" {
		t.Fatalf("header = %v", got)
	}
	pk := got["packages"].([]any)
	if len(pk) != 3 {
		t.Fatalf("packages = %v", pk)
	}
	want := []struct{ sku, name, price string }{{"plan.s", "S", "5.000"}, {"plan.m", "M", "9.000"}, {"plan.xl", "XL", "30.000"}}
	for i, w := range want {
		p := pk[i].(map[string]any)
		if p["sku"] != w.sku || p["name"] != w.name || p["price_month"] != w.price {
			t.Fatalf("package %d = %v, want %+v", i, p, w)
		}
	}
	// includes: the plan's shape plus the included bandwidth, as numbers.
	inc := pk[0].(map[string]any)["includes"].(map[string]any)
	if inc["vcpu"] != float64(2) || inc["memory_gb"] != float64(4) || inc["bandwidth_mbps"] != float64(50) {
		t.Fatalf("S includes = %v, want vcpu 2, memory_gb 4, bandwidth_mbps 50", inc)
	}
	if inc := pk[2].(map[string]any)["includes"].(map[string]any); inc["vcpu"] != float64(16) || inc["bandwidth_mbps"] != float64(1000) {
		t.Fatalf("XL includes = %v", inc)
	}
	fs := got["features"].([]any)
	if len(fs) != 3 {
		t.Fatalf("features = %d, want 3 (sso has no cell in this book)", len(fs))
	}
	f0 := fs[0].(map[string]any)
	if f0["key"] != "backup" || f0["name"] != "Backup" || f0["blurb"] != "Daily backups, kept 30 days" || f0["kind"] != "boolean" {
		t.Fatalf("feature 0 = %v", f0)
	}
	if _, hasUnit := f0["unit"]; hasUnit {
		t.Fatalf("a boolean feature carries no unit: %v", f0)
	}
	cellsOf := f0["cells"].(map[string]any)
	s := cellsOf["plan.s"].(map[string]any)
	if s["state"] != "optional" || s["addon_sku"] != "addon.backup" || s["price_month"] != "1.500" || s["included_from"] != "plan.xl" || s["note"] != "7-day retention on S" {
		t.Fatalf("backup on S = %v", s)
	}
	m := cellsOf["plan.m"].(map[string]any)
	if m["state"] != "optional" || m["price_month"] != "1.500" || m["included_from"] != "plan.xl" {
		t.Fatalf("backup on M = %v", m)
	}
	if _, has := m["note"]; has {
		t.Fatalf("an empty note is omitted: %v", m)
	}
	xl := cellsOf["plan.xl"].(map[string]any)
	if xl["state"] != "included" || len(xl) != 1 {
		t.Fatalf("backup on XL = %v, want {state: included} and nothing else", xl)
	}
	// Dedicated IP: optional, unpriced add-on → no price_month; not offered on
	// the packages with no cell; never included anywhere → no included_from.
	f1 := fs[1].(map[string]any)
	ipCells := f1["cells"].(map[string]any)
	if c := ipCells["plan.s"].(map[string]any); c["state"] != "optional" || c["addon_sku"] != "addon.dedicated_ip" || c["price_month"] != nil || c["included_from"] != nil {
		t.Fatalf("dedicated IP on S = %v", c)
	}
	if c := ipCells["plan.m"].(map[string]any); c["state"] != "not_offered" || len(c) != 1 {
		t.Fatalf("dedicated IP on M = %v, want not_offered alone", c)
	}
	// Bandwidth: a quantity feature with its unit and the quantity per cell.
	f2 := fs[2].(map[string]any)
	if f2["key"] != "bandwidth" || f2["kind"] != "quantity" || f2["unit"] != "Mbps" {
		t.Fatalf("feature 2 = %v", f2)
	}
	bwCells := f2["cells"].(map[string]any)
	if c := bwCells["plan.s"].(map[string]any); c["state"] != "included" || c["quantity"] != float64(50) {
		t.Fatalf("bandwidth on S = %v", c)
	}
	if c := bwCells["plan.m"].(map[string]any); c["quantity"] != float64(100) {
		t.Fatalf("bandwidth on M = %v", c)
	}
	// The document is byte-stable on the wire: numbers never float-render.
	if string(b) == "" || jsonHas(b, "50.000000") {
		t.Fatalf("quantities are trimmed on the wire: %s", b)
	}
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
