package events

// tenant_created_commerce_test.go — #6971 item 8. The purchase fields on the
// ONE shared `tenant.created` wire shape: a producer that never sets them
// emits the pre-#6971 bytes (no new keys), a producer that sets them emits
// four flat snake_case keys the provisioning consumer decodes into the same
// struct, and the add-on list reaching the Organization CR carries BSS SKUs
// only — never the catalog add-on ids the storefront mixes into the cart.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBSSAddonSKUs_KeepsOnlyBSSSKUsNormalisedAndDeduped(t *testing.T) {
	in := []string{
		"waf",                                  // catalog add-on id → dropped
		"c1f7d3a2-0b4e-4d6f-9a8b-7c6d5e4f3a2b", // catalog add-on UUID → dropped
		" Addon.Backup ",                       // trimmed + lower-cased
		"addon.backup",                         // duplicate of the above → once
		"addon.",                               // bare prefix is not a SKU → dropped
		"",                                     // empty → dropped
		"addon.dedicated-ip",
	}
	got := BSSAddonSKUs(in)
	want := []string{"addon.backup", "addon.dedicated-ip"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BSSAddonSKUs(%q) = %q, want %q", in, got, want)
	}
}

func TestBSSAddonSKUs_NoSKUsIsNil(t *testing.T) {
	// nil, and a cart of catalog ids alone, both yield nil — so `omitempty`
	// on the payload keeps the wire bytes legacy-identical.
	if got := BSSAddonSKUs(nil); got != nil {
		t.Fatalf("BSSAddonSKUs(nil) = %v, want nil", got)
	}
	if got := BSSAddonSKUs([]string{"waf", "umami"}); got != nil {
		t.Fatalf("BSSAddonSKUs(catalog ids) = %v, want nil", got)
	}
}

func TestTenantCreatedPayload_WithoutCommerce_EmitsNoNewKeys(t *testing.T) {
	p := NewTenantCreatedPayload("tid", "acme", "ACME", "u1", "o@acme.example", "plan-m", "", "", "omani.homes")
	// A legacy producer passes an empty TenantCommerce (whatever the record
	// holds) — the payload must still carry none of the four keys.
	p = p.WithCommerce(TenantCommerce{Addons: []string{"waf"}})
	if p.HasCommerce() {
		t.Fatalf("HasCommerce on a catalog-ids-only cart = true, want false")
	}
	wire, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"package_sku"`, `"addons"`, `"price_source"`, `"order_id"`} {
		if strings.Contains(string(wire), key) {
			t.Errorf("legacy payload leaked %s onto the wire: %s", key, wire)
		}
	}
}

func TestTenantCreatedPayload_WithCommerce_RoundTripsFlatSnakeCaseKeys(t *testing.T) {
	p := NewTenantCreatedPayload("tid", "acme", "ACME", "u1", "o@acme.example", "plan-m", "", "", "omani.homes").
		WithCommerce(TenantCommerce{
			PackageSKU:  " Plan.M ",
			Addons:      []string{"waf", "addon.backup", "ADDON.BACKUP", "addon.dedicated-ip"},
			PriceSource: " bss:OpenOva plans@2026-09-11 ",
			OrderID:     " 4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c ",
		})
	if !p.HasCommerce() {
		t.Fatal("HasCommerce = false on a payload with a package")
	}
	wire, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	// Flat siblings of the existing fields — the consumer decodes the same
	// struct, no nesting, no camelCase on the wire.
	for _, frag := range []string{
		`"package_sku":"plan.m"`,
		`"addons":["addon.backup","addon.dedicated-ip"]`,
		`"price_source":"bss:OpenOva plans@2026-09-11"`,
		`"order_id":"4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c"`,
	} {
		if !strings.Contains(string(wire), frag) {
			t.Errorf("wire missing %s: %s", frag, wire)
		}
	}
	if strings.Contains(string(wire), `"commerce"`) {
		t.Errorf("wire nests a commerce object — the payload is flat: %s", wire)
	}

	var got TenantCreatedPayload
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatalf("consumer decode: %v", err)
	}
	if got.PackageSKU != "plan.m" || got.PriceSource != "bss:OpenOva plans@2026-09-11" ||
		got.OrderID != "4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c" ||
		!reflect.DeepEqual(got.Addons, []string{"addon.backup", "addon.dedicated-ip"}) {
		t.Errorf("round-trip lost a field: %+v", got)
	}
	// The pre-existing fields are untouched by WithCommerce.
	if got.Slug != "acme" || got.OwnerEmail != "o@acme.example" || got.ParentDomain != "omani.homes" {
		t.Errorf("WithCommerce disturbed a pre-existing field: %+v", got)
	}
}
