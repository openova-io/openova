package handlers

// commerce_handover_test.go — #6971 item 8. The Organization that gets
// created must learn which package and add-ons were bought. Three seams in
// this service, each pinned here without a store:
//
//  1. POST /tenant/orgs decodes `package_sku` / `price_source` / `order_id`
//     (keeping `addons`), and a legacy body without them decodes to empties.
//  2. The settlement launch body from billing is applied onto the record —
//     the order WINS, BSS add-on SKUs merge into the cart's one list, an
//     empty or malformed body is a no-op (never a refused launch).
//  3. launchTenant emits the purchase on tenant.created from the PERSISTED
//     store.Tenant record, and a legacy record emits none of the four keys.

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/services/tenant/store"
)

func TestCreateOrgRequest_DecodesCommerceFields(t *testing.T) {
	// The storefront's POST /tenant/orgs (core/marketplace CheckoutStep.svelte):
	// package_sku beside plan_id, the cart's one addons list.
	var withSKU createOrgRequest
	if err := json.Unmarshal([]byte(`{
		"slug":"acme","name":"ACME","plan_id":"m","package_sku":"plan.m",
		"addons":["waf","addon.backup"],"price_source":"catalog",
		"order_id":"ord-1","defer_launch":true}`), &withSKU); err != nil {
		t.Fatal(err)
	}
	if withSKU.PackageSKU != "plan.m" || withSKU.PriceSource != "catalog" || withSKU.OrderID != "ord-1" {
		t.Errorf("commerce fields not decoded: %+v", withSKU)
	}
	if !reflect.DeepEqual(withSKU.AddOns, []string{"waf", "addon.backup"}) {
		t.Errorf("addons must stay the verbatim cart list, got %v", withSKU.AddOns)
	}
	if withSKU.PlanID != "m" || !withSKU.DeferLaunch {
		t.Errorf("pre-existing fields disturbed: %+v", withSKU)
	}

	// A legacy body (older storefront, direct API) carries none of them and
	// decodes to empties — behaviour exactly as today.
	var legacy createOrgRequest
	if err := json.Unmarshal([]byte(`{"slug":"acme","name":"ACME","plan_id":"m","addons":["waf"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.PackageSKU != "" || legacy.PriceSource != "" || legacy.OrderID != "" {
		t.Errorf("legacy body must decode to empty commerce fields, got %+v", legacy)
	}
}

func TestDecodeLaunchRequest_EmptyMalformedAndValid(t *testing.T) {
	isZero := func(r launchRequest) bool {
		return r.OrderID == "" && r.PackageSKU == "" && r.PriceSource == "" && len(r.Addons) == 0
	}
	// The pre-#6971 call: no body at all.
	if got := decodeLaunchRequest(nil, "tid"); !isZero(got) {
		t.Errorf("nil body → %+v, want zero", got)
	}
	if got := decodeLaunchRequest(strings.NewReader(""), "tid"); !isZero(got) {
		t.Errorf("empty body → %+v, want zero", got)
	}
	// Malformed is a loud no-op — the launch must still happen.
	if got := decodeLaunchRequest(strings.NewReader(`{not-json`), "tid"); !isZero(got) {
		t.Errorf("malformed body → %+v, want zero", got)
	}
	got := decodeLaunchRequest(strings.NewReader(`{"order_id":"ord-1","package_sku":"plan.m","price_source":"catalog","addons":["addon.backup"]}`), "tid")
	if got.OrderID != "ord-1" || got.PackageSKU != "plan.m" || got.PriceSource != "catalog" ||
		!reflect.DeepEqual(got.Addons, []string{"addon.backup"}) {
		t.Errorf("valid body decoded wrong: %+v", got)
	}
}

func TestApplyLaunchCommerce_OrderWinsAndMergesSKUs(t *testing.T) {
	// Create-time state: the storefront stamped plan.s and a mixed cart.
	tn := &store.Tenant{
		ID:         "tid",
		PackageSKU: "plan.s",
		AddOns:     []string{"waf", "addon.backup"},
	}
	delta := applyLaunchCommerce(tn, launchRequest{
		OrderID:     " ord-1 ",
		PackageSKU:  " Plan.M ",
		PriceSource: "bss:OpenOva plans@2026-09-11",
		Addons:      []string{"waf", "addon.backup", "addon.dedicated-ip"},
	})

	// The order is the source — its package replaces the create-time one.
	if tn.PackageSKU != "plan.m" || tn.PriceSource != "bss:OpenOva plans@2026-09-11" || tn.OrderID != "ord-1" {
		t.Errorf("record scalars after apply: %+v", tn)
	}
	// Cart list: catalog ids kept, the new SKU added once, no duplicates.
	if want := []string{"waf", "addon.backup", "addon.dedicated-ip"}; !reflect.DeepEqual(tn.AddOns, want) {
		t.Errorf("record AddOns = %v, want %v", tn.AddOns, want)
	}
	// The delta is what SetCommerce persists: every scalar, only the NEW SKU.
	if delta.PackageSKU != "plan.m" || delta.PriceSource != "bss:OpenOva plans@2026-09-11" || delta.OrderID != "ord-1" {
		t.Errorf("delta scalars: %+v", delta)
	}
	if want := []string{"addon.dedicated-ip"}; !reflect.DeepEqual(delta.Addons, want) {
		t.Errorf("delta.Addons = %v, want %v", delta.Addons, want)
	}
	if delta.IsZero() {
		t.Error("delta.IsZero() = true after a real apply")
	}
}

func TestApplyLaunchCommerce_EmptyBodyChangesNothing(t *testing.T) {
	tn := &store.Tenant{ID: "tid", PackageSKU: "plan.s", PriceSource: "catalog", OrderID: "ord-0", AddOns: []string{"waf"}}
	before := *tn
	before.AddOns = append([]string(nil), tn.AddOns...)

	delta := applyLaunchCommerce(tn, launchRequest{})
	if !delta.IsZero() {
		t.Errorf("empty body produced a delta: %+v", delta)
	}
	if tn.PackageSKU != before.PackageSKU || tn.PriceSource != before.PriceSource || tn.OrderID != before.OrderID ||
		!reflect.DeepEqual(tn.AddOns, before.AddOns) {
		t.Errorf("empty body mutated the record: before %+v after %+v", before, *tn)
	}
	// A nil record is tolerated.
	if d := applyLaunchCommerce(nil, launchRequest{PackageSKU: "plan.m"}); !d.IsZero() {
		t.Errorf("nil record produced a delta: %+v", d)
	}
}

func TestLaunchTenant_EmitsPurchaseFromPersistedTenant(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}
	h.launchTenant(context.Background(), &store.Tenant{
		ID: "tid", Slug: "acme", Subdomain: "acme", Name: "ACME",
		OwnerEmail:  "o@acme.example",
		PlanID:      "m",
		Apps:        []string{"wordpress"},
		AddOns:      []string{"waf", "addon.backup", "addon.dedicated-ip"},
		PackageSKU:  "plan.m",
		PriceSource: "catalog",
		OrderID:     "ord-1",
	})
	created := eventsByType(prod.published, "tenant.created")
	if len(created) != 1 {
		t.Fatalf("expected 1 tenant.created, got %d", len(created))
	}
	var p map[string]any
	if err := json.Unmarshal(created[0].Data, &p); err != nil {
		t.Fatal(err)
	}
	if p["package_sku"] != "plan.m" || p["price_source"] != "catalog" || p["order_id"] != "ord-1" {
		t.Errorf("tenant.created purchase scalars: %v", p)
	}
	// Only BSS SKUs reach the event (→ the CR); the catalog id "waf" does not.
	addons, _ := p["addons"].([]any)
	if len(addons) != 2 || addons[0] != "addon.backup" || addons[1] != "addon.dedicated-ip" {
		t.Errorf("tenant.created addons = %v, want the two BSS SKUs only", p["addons"])
	}
}

func TestLaunchTenant_LegacyTenantEmitsNoPurchaseKeys(t *testing.T) {
	prod := &recordingProducer{}
	h := &Handler{Producer: prod}
	h.launchTenant(context.Background(), &store.Tenant{
		ID: "tid", Slug: "acme", Subdomain: "acme", OwnerEmail: "o@acme.example",
		Apps: []string{"wordpress"}, AddOns: []string{"waf"},
	})
	created := eventsByType(prod.published, "tenant.created")
	if len(created) != 1 {
		t.Fatalf("expected 1 tenant.created, got %d", len(created))
	}
	for _, key := range []string{`"package_sku"`, `"addons"`, `"price_source"`, `"order_id"`} {
		if strings.Contains(string(created[0].Data), key) {
			t.Errorf("legacy tenant leaked %s onto tenant.created: %s", key, created[0].Data)
		}
	}
}
