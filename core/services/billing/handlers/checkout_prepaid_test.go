package handlers

// checkout_prepaid_test.go — what the checkout collected for the PACKAGE and
// its BSS add-on lines (#6971), handed to the Organization as
// spec.commerce.prepaidAmount so BSS credits it on the first statement and
// never charges the package the checkout collected twice.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/services/billing/store"
)

func TestPrepaidBaisa_PackageAndBSSAddonsOnly(t *testing.T) {
	cases := []struct {
		name string
		p    *pricedOrder
		want int64
	}{
		// M 4.490 + backup 1.500 = 5.990; the 2.000 topology surcharge and the
		// 3.000 catalog add-on are not BSS lines, so they are not credited.
		{"package, BSS add-on, surcharge and catalog add-on", &pricedOrder{
			PackageSKU: "plan.m", PlanBaisa: 4490, TopologyBaisa: 2000,
			Lines: []store.OrderLine{
				{SKU: "addon.backup", AmountBaisa: 1500},
				{SKU: "waf", AmountBaisa: 3000},
			},
			TotalBaisa: 4490 + 2000 + 1500 + 3000,
		}, 5990},
		// A redundant add-on (XL includes backup) is 0 and never credited.
		{"redundant add-on", &pricedOrder{
			PackageSKU: "plan.xl", PlanBaisa: 13990,
			Lines:      []store.OrderLine{{SKU: "addon.backup", AmountBaisa: 0, Redundant: true}},
			TotalBaisa: 13990,
		}, 13990},
		// Never more than the order total.
		{"capped at the total", &pricedOrder{PackageSKU: "plan.s", PlanBaisa: 2490, TotalBaisa: 2000}, 2000},
		// A catalog-priced order prepays nothing BSS bills.
		{"catalog order", &pricedOrder{PlanBaisa: 9000, TotalBaisa: 9000}, 0},
		{"nil", nil, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := prepaidBaisa(c.p); got != c.want {
				t.Errorf("prepaidBaisa = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSettlementLaunchBody_PrepaidAmount(t *testing.T) {
	// M 4.490 + backup 1.500 = 5.990 → "5.990".
	b := settlementLaunchBody(&store.Order{ID: "o1", TenantID: "t", PackageSKU: "plan.m", AmountBaisa: 5990, PrepaidBaisa: 5990})
	if b.PrepaidAmount != "5.990" {
		t.Errorf("prepaid_amount = %q, want 5.990", b.PrepaidAmount)
	}
	wire, _ := json.Marshal(b)
	if !strings.Contains(string(wire), `"prepaid_amount":"5.990"`) {
		t.Errorf("wire = %s", wire)
	}
	// A row whose prepaid figure exceeds what was collected hands over the
	// collected total: 2.490 recorded, 2.000 collected → "2.000".
	if got := settlementLaunchBody(&store.Order{ID: "o2", PackageSKU: "plan.s", AmountBaisa: 2000, PrepaidBaisa: 2490}).PrepaidAmount; got != "2.000" {
		t.Errorf("capped prepaid = %q, want 2.000", got)
	}
	// A package row that predates the column (0) and a catalog order hand
	// over no key at all.
	for _, o := range []*store.Order{
		{ID: "o3", PackageSKU: "plan.m", AmountBaisa: 4490},
		{ID: "o4", PriceSource: "catalog", AmountBaisa: 9000, PrepaidBaisa: 9000},
	} {
		w, _ := json.Marshal(settlementLaunchBody(o))
		if strings.Contains(string(w), "prepaid_amount") {
			t.Errorf("order %s leaked prepaid_amount: %s", o.ID, w)
		}
	}
}
