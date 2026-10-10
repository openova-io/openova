package handlers

// organization_create_prepaid_test.go — tenant.created's prepaid_amount lands
// on the Organization CR as spec.commerce.prepaidAmount (#6971): what the
// checkout collected for the package and its BSS add-on lines, which BSS
// credits on the Organization's first statement. Absent when unset.

import (
	"encoding/json"
	"testing"
)

func TestOrganizationCommerceBlock_PrepaidAmount(t *testing.T) {
	// M 4.490 + backup 1.500 = 5.990 collected at checkout.
	var p tenantCreatedPayload
	if err := json.Unmarshal([]byte(`{"id":"tid","slug":"acme","owner_email":"o@acme.example",
		"package_sku":"plan.m","addons":["addon.backup"],"prepaid_amount":" 5.990 "}`), &p); err != nil {
		t.Fatal(err)
	}
	block, ok := organizationCommerceBlock(p)
	if !ok || block["prepaidAmount"] != "5.990" || block["packageSKU"] != "plan.m" {
		t.Fatalf("block = %#v", block)
	}

	// A prepaid amount alone still mints the block.
	if block, ok := organizationCommerceBlock(tenantCreatedPayload{Slug: "acme", PrepaidAmount: "2.490"}); !ok || block["prepaidAmount"] != "2.490" {
		t.Fatalf("prepaid alone = %#v %v", block, ok)
	}

	// No prepaid amount → no key.
	block, _ = organizationCommerceBlock(tenantCreatedPayload{Slug: "acme", PackageSKU: "plan.s"})
	if _, has := block["prepaidAmount"]; has {
		t.Errorf("prepaidAmount present without a value: %#v", block)
	}
}
