package handlers

// organization_create_commerce_test.go — #6971 item 8. The tenant.created
// consumer stamps the purchase onto the Organization CR as `spec.commerce`
// with the field paths the chargeback OpenOva adapter reads
// (packageSKU / addons[] / priceSource / orderID), and leaves the block
// ABSENT — not `commerce: {}` — for a legacy payload, so a CR minted from a
// pre-#6971 producer is byte-identical to before.

import (
	"reflect"
	"testing"
)

func TestOrganizationCommerceBlock_PresentUsesTheAdapterFieldPaths(t *testing.T) {
	block, ok := organizationCommerceBlock(tenantCreatedPayload{
		ID: "tid", Slug: "acme", OwnerEmail: "o@acme.example", PlanID: "m",
		PackageSKU:  " Plan.M ",
		Addons:      []string{"waf", "addon.backup", "addon.backup", "addon.dedicated-ip"},
		PriceSource: " bss:OpenOva plans@2026-09-11 ",
		OrderID:     " 4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c ",
	})
	if !ok {
		t.Fatal("expected a commerce block for a payload carrying a package")
	}
	want := map[string]any{
		"packageSKU":  "plan.m",
		"addons":      []string{"addon.backup", "addon.dedicated-ip"},
		"priceSource": "bss:OpenOva plans@2026-09-11",
		"orderID":     "4f7c2a1e-9b3d-4c5e-8f6a-1d2e3f4a5b6c",
	}
	if !reflect.DeepEqual(block, want) {
		t.Errorf("spec.commerce = %#v, want %#v", block, want)
	}
}

func TestOrganizationCommerceBlock_PartialPayloadOmitsEmptyFields(t *testing.T) {
	// The storefront's own create (package sku only, no order yet).
	block, ok := organizationCommerceBlock(tenantCreatedPayload{PackageSKU: "plan.s"})
	if !ok {
		t.Fatal("expected a commerce block for a package-only payload")
	}
	if !reflect.DeepEqual(block, map[string]any{"packageSKU": "plan.s"}) {
		t.Errorf("package-only block = %#v, want only packageSKU", block)
	}
}

func TestOrganizationCommerceBlock_AbsentForLegacyPayload(t *testing.T) {
	// No purchase fields at all → no block (never `commerce: {}`).
	if block, ok := organizationCommerceBlock(tenantCreatedPayload{ID: "tid", Slug: "acme", PlanID: "m"}); ok || block != nil {
		t.Errorf("legacy payload produced a block: %#v", block)
	}
	// A cart of catalog add-on ids alone is NOT a purchase BSS can attach a
	// line to — still no block.
	if block, ok := organizationCommerceBlock(tenantCreatedPayload{Addons: []string{"waf", "umami"}}); ok || block != nil {
		t.Errorf("catalog-ids-only payload produced a block: %#v", block)
	}
}
