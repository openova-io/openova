package events

// tenant_created_prepaid_test.go — prepaid_amount on the `tenant.created`
// wire shape (#6971): what the checkout collected for the package and its
// BSS add-on lines, so BSS credits it on the Organization's first statement.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTenantCreatedPayload_PrepaidAmount(t *testing.T) {
	// M 4.490 + backup 1.500 = 5.990.
	p := TenantCreatedPayload{Slug: "acme"}.WithCommerce(TenantCommerce{PackageSKU: "plan.m", PrepaidAmount: " 5.990 "})
	wire, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"prepaid_amount":"5.990"`) {
		t.Fatalf("wire = %s", wire)
	}
	if !(TenantCreatedPayload{}.WithCommerce(TenantCommerce{PrepaidAmount: "2.490"})).HasCommerce() {
		t.Fatal("a prepaid amount alone is a purchase")
	}
	if (TenantCreatedPayload{}.WithCommerce(TenantCommerce{PrepaidAmount: "  "})).HasCommerce() {
		t.Fatal("a blank prepaid amount is no purchase")
	}
}

func TestFormatBaisa(t *testing.T) {
	for in, want := range map[int64]string{0: "", -5: "", 6000: "6.000", 4490: "4.490", 13990: "13.990", 5: "0.005", 2490: "2.490"} {
		if got := FormatBaisa(in); got != want {
			t.Errorf("FormatBaisa(%d) = %q, want %q", in, got, want)
		}
	}
}
