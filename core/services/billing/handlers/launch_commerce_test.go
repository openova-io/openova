package handlers

// launch_commerce_test.go — #6971 item 8, the billing half of the hand-over.
// The settled order is THE source for what the Organization carries, so the
// settlement launch POST must hand core/services/tenant the order's purchase:
// order id, package sku, price provenance, and the BSS add-on SKUs (not the
// catalog add-on ids that share the cart list). A legacy order (no package)
// hands over only what it has.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/core/services/billing/store"
)

func TestCheckoutRequest_DecodesPackageSKU(t *testing.T) {
	var with checkoutRequest
	if err := json.Unmarshal([]byte(`{"plan_id":"m","tenant_id":"tid","package_sku":"plan.m","addons":["waf","addon.backup"]}`), &with); err != nil {
		t.Fatal(err)
	}
	if with.PackageSKU != "plan.m" || !reflect.DeepEqual(with.Addons, []string{"waf", "addon.backup"}) {
		t.Errorf("checkout decode: %+v", with)
	}
	var legacy checkoutRequest
	if err := json.Unmarshal([]byte(`{"plan_id":"m","tenant_id":"tid","addons":["waf"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.PackageSKU != "" {
		t.Errorf("legacy checkout body must decode to an empty package_sku, got %q", legacy.PackageSKU)
	}
}

func TestSettlementLaunchBody_CarriesTheOrdersPurchaseWithBSSSKUsOnly(t *testing.T) {
	order := &store.Order{
		ID: "ord-1", TenantID: "tid", PlanID: "m",
		PackageSKU:  " Plan.M ",
		PriceSource: store.PriceSourceCatalog,
		Addons:      json.RawMessage(`["waf","addon.backup","ADDON.BACKUP","addon.dedicated-ip"]`),
	}
	b := settlementLaunchBody(order)
	if b.OrderID != "ord-1" || b.PackageSKU != "plan.m" || b.PriceSource != "catalog" {
		t.Errorf("launch body scalars: %+v", b)
	}
	if want := []string{"addon.backup", "addon.dedicated-ip"}; !reflect.DeepEqual(b.Addons, want) {
		t.Errorf("launch body addons = %v, want %v (BSS SKUs only, deduped)", b.Addons, want)
	}
}

func TestSettlementLaunchBody_LegacyOrderHandsOverOnlyWhatItHas(t *testing.T) {
	// A legacy-deck order: no package, catalog add-on ids only, provenance
	// from the migration backfill.
	b := settlementLaunchBody(&store.Order{ID: "ord-0", TenantID: "tid", PriceSource: "catalog", Addons: json.RawMessage(`["waf"]`)})
	wire, _ := json.Marshal(b)
	if b.OrderID != "ord-0" || b.PackageSKU != "" || len(b.Addons) != 0 {
		t.Errorf("legacy launch body: %+v", b)
	}
	for _, key := range []string{`"package_sku"`, `"addons"`} {
		if strings.Contains(string(wire), key) {
			t.Errorf("legacy launch body leaked %s: %s", key, wire)
		}
	}
	// A malformed addons column yields no SKUs, never a failed launch.
	bad := settlementLaunchBody(&store.Order{ID: "ord-x", TenantID: "tid", Addons: json.RawMessage(`{"not":"an array"}`)})
	if len(bad.Addons) != 0 || bad.OrderID != "ord-x" {
		t.Errorf("malformed addons column: %+v", bad)
	}
}

// TestDispatchOrderPlaced_LaunchPOSTCarriesTheOrdersPurchase drives the REAL
// settlement call site (dispatchOrderPlaced → launchTenant) against a stand-in
// core/services/tenant and asserts the launch body that service decodes
// (launchRequest) — AND that order.placed now carries the same provenance.
func TestDispatchOrderPlaced_LaunchPOSTCarriesTheOrdersPurchase(t *testing.T) {
	const tenantID = "tid-6971"
	var (
		mu          sync.Mutex
		launchBody  []byte
		contentType string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/subdomain"):
			_, _ = w.Write([]byte(`{"id":"` + tenantID + `","subdomain":"acme"}`))
		case strings.HasSuffix(r.URL.Path, "/app-configs"):
			_, _ = w.Write([]byte(`{"id":"` + tenantID + `","app_configs":{}}`))
		case strings.HasSuffix(r.URL.Path, "/launch"):
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			launchBody = body
			contentType = r.Header.Get("Content-Type")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"` + tenantID + `","launched":true,"status":"provisioning"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	prod := &capturingProducer{}
	h := &Handler{Producer: prod, TenantURL: srv.URL}
	order := &store.Order{
		ID: "ord-1", CustomerID: "cust-1", TenantID: tenantID, PlanID: "m",
		Addons:      json.RawMessage(`["waf","addon.backup"]`),
		PackageSKU:  "plan.m",
		PriceSource: store.PriceSourceCatalog,
		Status:      "completed",
	}
	h.dispatchOrderPlaced(tenantID, order)

	mu.Lock()
	defer mu.Unlock()
	if contentType != "application/json" {
		t.Errorf("launch Content-Type = %q, want application/json", contentType)
	}
	// Decode exactly as core/services/tenant does (its launchRequest shape).
	var got struct {
		OrderID     string   `json:"order_id"`
		PackageSKU  string   `json:"package_sku"`
		PriceSource string   `json:"price_source"`
		Addons      []string `json:"addons"`
	}
	if err := json.Unmarshal(launchBody, &got); err != nil {
		t.Fatalf("launch body is not core/services/tenant's launchRequest shape: %v\nbody=%s", err, launchBody)
	}
	if got.OrderID != "ord-1" || got.PackageSKU != "plan.m" || got.PriceSource != "catalog" {
		t.Errorf("launch body scalars: %+v", got)
	}
	if !reflect.DeepEqual(got.Addons, []string{"addon.backup"}) {
		t.Errorf("launch body addons = %v, want [addon.backup] (the catalog id waf must not be handed over)", got.Addons)
	}

	// order.placed carries the same provenance beside the money.
	for _, e := range prod.published {
		if e.Type != "order.placed" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p["package_sku"] != "plan.m" || p["price_source"] != "catalog" {
			t.Errorf("order.placed provenance: package_sku=%v price_source=%v", p["package_sku"], p["price_source"])
		}
		return
	}
	t.Fatalf("no order.placed published; got %v", prod.types())
}
