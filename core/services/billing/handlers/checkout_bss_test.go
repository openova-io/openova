package handlers

// Tests for #6971 — an order that carries a package_sku is priced from the
// Catalyst BSS public packages document, with the storefront's own fixture
// shape served by an httptest stub:
//
//   - plan M + optional backup  → 9.000 + 1.500, lines + price_source recorded
//   - plan XL + backup          → 30.000, the add-on marked redundant
//   - plan S + a not_offered add-on → 422 naming the add-on AND the package
//   - BSS unreachable           → 503 "prices unavailable", no order
//   - no package_sku            → the catalog path, BSS never consulted
//   - POST /billing/quote       → the same lines, no store, no order
//
// The fixture numbers are the contract's (S 5.000 / M 9.000 / XL 30.000);
// the marketplace renders whatever its Sovereign publishes.

import (
	"bytes"
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/openova-io/openova/core/services/billing/packages"
	"github.com/openova-io/openova/core/services/billing/store"
)

const bssContractBody = `{
  "currency":"OMR","price_book":"OpenOva plans","prices_as_of":"2026-09-11",
  "packages":[
    {"sku":"plan.s","name":"S","price_month":"5.000","includes":{"vcpu":1,"memory_gb":2,"storage_gb":25,"bandwidth_mbps":50}},
    {"sku":"plan.m","name":"M","price_month":"9.000","includes":{"vcpu":2,"memory_gb":4,"storage_gb":50,"bandwidth_mbps":100}},
    {"sku":"plan.l","name":"L","price_month":"15.000","includes":{"vcpu":4,"memory_gb":8,"storage_gb":100,"bandwidth_mbps":250}},
    {"sku":"plan.xl","name":"XL","price_month":"30.000","includes":{"vcpu":8,"memory_gb":16,"storage_gb":200,"bandwidth_mbps":1000}}
  ],
  "features":[
    {"key":"backup","name":"Backup","kind":"boolean","cells":{
      "plan.s":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
      "plan.m":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
      "plan.l":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
      "plan.xl":{"state":"included"}}},
    {"key":"dedicated-ip","name":"Dedicated IP address","kind":"boolean","cells":{
      "plan.s":{"state":"not_offered"},
      "plan.m":{"state":"optional","addon_sku":"addon.dedicated-ip","price_month":"2.000"},
      "plan.l":{"state":"optional","addon_sku":"addon.dedicated-ip","price_month":"2.000"},
      "plan.xl":{"state":"optional","addon_sku":"addon.dedicated-ip","price_month":"2.000"}}}
  ]
}`

// fakeBSS serves the contract at packages.Path with the given status.
func fakeBSS(t *testing.T, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != packages.Path {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bssContractBody))
	}))
}

// mustNotBeCalledBSS fails the test on any request — proof the catalog path
// never consults BSS.
func mustNotBeCalledBSS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("BSS was consulted on an order without package_sku: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusTeapot)
	}))
}

// fakeCatalogWithAddons serves plans AND add-ons, for the mixed-list case.
func fakeCatalogWithAddons(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/catalog/plans", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "m", "stripe_price_id": "", "price_omr": 9}})
	})
	mux.HandleFunc("/catalog/addons", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "waf", "name": "WAF", "price_omr": 2}})
	})
	return httptest.NewServer(mux)
}

// jsonArg is a sqlmock matcher for a JSONB argument: equal as JSON, not as bytes.
type jsonArg struct{ want string }

func (j jsonArg) Match(v driver.Value) bool {
	var raw []byte
	switch x := v.(type) {
	case []byte:
		raw = x
	case string:
		raw = []byte(x)
	default:
		return false
	}
	var got, want any
	if json.Unmarshal(raw, &got) != nil || json.Unmarshal([]byte(j.want), &want) != nil {
		return false
	}
	return reflect.DeepEqual(got, want)
}

func expectCustomer(mock sqlmock.Sqlmock, userID, custID, tenantID string) {
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT id, user_id, tenant_id, stripe_customer_id, email, created_at",
	)).WithArgs(userID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "tenant_id", "stripe_customer_id", "email", "created_at",
		}).AddRow(custID, userID, tenantID, nil, "ops@t99.omani.works", time.Now()))
}

func postCheckout(t *testing.T, h *Handler, req checkoutRequest, userID string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/billing/checkout", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = withCustomerClaims(r, userID, "ops@t99.omani.works")
	rec := httptest.NewRecorder()
	h.Checkout(rec, r)
	return rec
}

// Plan M + optional backup → 9.000 + 1.500, from BSS alone: CatalogURL is
// EMPTY here, so any catalog lookup would have errored — the plan and the
// add-on are priced from one document.
func TestPriceOrder_PackageM_WithBackup_Is9000Plus1500(t *testing.T) {
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)}

	p, err := h.priceOrder(context.Background(), pricingRequest{
		PlanID: "m", PackageSKU: "plan.m", Addons: []string{"addon.backup"}, Topology: topologySingleRegion,
	})
	if err != nil {
		t.Fatalf("priceOrder: %v", err)
	}
	if p.PlanBaisa != 9000 || p.TotalBaisa != 10500 {
		t.Errorf("plan=%d total=%d, want 9000 / 10500", p.PlanBaisa, p.TotalBaisa)
	}
	if p.WholeOMR() != 11 {
		t.Errorf("WholeOMR = %d, want 11 (10.500 rounded up for the whole-OMR ledger)", p.WholeOMR())
	}
	if p.PriceSource != "bss:OpenOva plans@2026-09-11" || p.PackageSKU != "plan.m" || p.Currency != "OMR" {
		t.Errorf("provenance: %+v", p)
	}
	want := []store.OrderLine{{SKU: "addon.backup", Name: "Backup", AmountBaisa: 1500}}
	if !reflect.DeepEqual(p.Lines, want) {
		t.Errorf("lines = %+v, want %+v", p.Lines, want)
	}
	resp := p.response("m")
	if resp.AmountBaisa != 10500 || resp.AmountOMR != 11 || resp.PlanID != "m" || len(resp.Lines) != 1 {
		t.Errorf("response = %+v", resp)
	}
}

// Plan XL + backup → 30.000; the add-on is recorded as redundant, not refused.
func TestPriceOrder_PackageXL_WithBackup_IsRedundantAt30000(t *testing.T) {
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)}

	p, err := h.priceOrder(context.Background(), pricingRequest{
		PlanID: "xl", PackageSKU: "plan.xl", Addons: []string{"addon.backup"}, Topology: topologySingleRegion,
	})
	if err != nil {
		t.Fatalf("priceOrder: %v", err)
	}
	if p.TotalBaisa != 30000 {
		t.Errorf("total = %d, want 30000", p.TotalBaisa)
	}
	if len(p.Lines) != 1 || !p.Lines[0].Redundant || p.Lines[0].AmountBaisa != 0 || p.Lines[0].SKU != "addon.backup" {
		t.Errorf("lines = %+v, want one redundant zero line for addon.backup", p.Lines)
	}
}

// A BSS SKU and a catalog add-on id in one list: the SKU from the package's
// cell, the id from /catalog/addons, both as lines; the hot-standby surcharge
// stays billing's own.
func TestPriceOrder_MixedBSSAndCatalogAddons(t *testing.T) {
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	catalog := fakeCatalogWithAddons(t)
	defer catalog.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil), CatalogURL: catalog.URL}

	p, err := h.priceOrder(context.Background(), pricingRequest{
		PlanID: "m", PackageSKU: "plan.m", Addons: []string{"waf", "addon.backup"}, Topology: topologyActiveHotStandby,
	})
	if err != nil {
		t.Fatalf("priceOrder: %v", err)
	}
	// 9.000 plan + 1.500 backup + 2.000 waf + 5.000 hot-standby.
	if p.TotalBaisa != 17500 || p.TopologyBaisa != 5000 {
		t.Errorf("total=%d topology=%d, want 17500 / 5000", p.TotalBaisa, p.TopologyBaisa)
	}
	want := []store.OrderLine{
		{SKU: "addon.backup", Name: "Backup", AmountBaisa: 1500},
		{SKU: "waf", Name: "WAF", AmountBaisa: 2000},
	}
	if !reflect.DeepEqual(p.Lines, want) {
		t.Errorf("lines = %+v, want %+v", p.Lines, want)
	}
}

// package_sku present but BSS pricing not configured → 503, never the catalog.
func TestPriceOrder_PackageSKUWithoutPriceBook_IsUnavailable(t *testing.T) {
	catalog := fakeCatalogServer(t, "m", 9)
	defer catalog.Close()
	h := &Handler{CatalogURL: catalog.URL} // Packages nil

	_, err := h.priceOrder(context.Background(), pricingRequest{PlanID: "m", PackageSKU: "plan.m", Topology: topologySingleRegion})
	if !errors.Is(err, errPricesUnavailable) {
		t.Fatalf("want errPricesUnavailable, got %v", err)
	}
	if status, _ := pricingErrorResponse(err); status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", status)
	}
}

// Plan S + an add-on that is not_offered on S → 422 naming the add-on and the
// package, before any promo or order side effect.
func TestCheckout_PackageS_NotOfferedAddon_422NamesBoth(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Store: store.New(db), Packages: packages.NewClient(bss.URL, nil)}
	expectCustomer(mock, "user-s", "cust-s", "org-s")

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "s", PackageSKU: "plan.s", TenantID: "org-s",
		Addons: []string{"addon.dedicated-ip"}, PromoCode: "WOULD-BURN-A-SLOT-IF-REDEEMED",
	}, "user-s")

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Error      string `json:"error"`
		AddonSKU   string `json:"addon_sku"`
		PackageSKU string `json:"package_sku"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("422 body is not JSON: %v", err)
	}
	if body.AddonSKU != "addon.dedicated-ip" || body.PackageSKU != "plan.s" {
		t.Errorf("structured fields = %+v", body)
	}
	for _, want := range []string{"addon.dedicated-ip", "plan.s"} {
		if !strings.Contains(body.Error, want) {
			t.Errorf("error %q does not name %q", body.Error, want)
		}
	}
	// No promo redemption, no order: the refusal came before any side effect.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected store interactions: %v", err)
	}
}

// An add-on no feature sells anywhere is refused the same way.
func TestCheckout_UnknownBSSAddon_422(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Store: store.New(db), Packages: packages.NewClient(bss.URL, nil)}
	expectCustomer(mock, "user-m", "cust-m", "org-m")

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "m", PackageSKU: "plan.m", TenantID: "org-m", Addons: []string{"addon.teleport"},
	}, "user-m")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("want 422, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "addon.teleport") || !strings.Contains(rec.Body.String(), "plan.m") {
		t.Errorf("body does not name the add-on and the package: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected store interactions: %v", err)
	}
}

// BSS configured but down → 503 "prices unavailable"; the order is never
// priced from the catalog instead, even though a catalog IS reachable here.
func TestCheckout_BSSDown_503PricesUnavailable(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bss := fakeBSS(t, http.StatusBadGateway)
	defer bss.Close()
	catalog := fakeCatalogServer(t, "m", 9)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL, Packages: packages.NewClient(bss.URL, nil)}
	expectCustomer(mock, "user-down", "cust-down", "org-down")

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "m", PackageSKU: "plan.m", TenantID: "org-down", Addons: []string{"addon.backup"},
	}, "user-down")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "prices unavailable") {
		t.Errorf("503 body does not say prices unavailable: %s", rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected store interactions: %v", err)
	}
}

// No package_sku → the catalog path exactly as before: BSS is configured and
// reachable but is NEVER consulted; the response says so (price_source
// "catalog").
func TestCheckout_NoPackageSKU_CatalogPathNeverConsultsBSS(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bss := mustNotBeCalledBSS(t)
	defer bss.Close()
	catalog := fakeCatalogServer(t, "plan-pro", 100)
	defer catalog.Close()
	h := &Handler{Store: store.New(db), CatalogURL: catalog.URL, Packages: packages.NewClient(bss.URL, nil)}

	expectCustomer(mock, "user-legacy", "cust-legacy", "org-legacy")
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT)",
	)).WithArgs("cust-legacy").
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(200)))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WithArgs("cust-legacy", "org-legacy", "plan-pro",
			sqlmock.AnyArg(), sqlmock.AnyArg(), "single-region",
			100, int64(100000), "completed",
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			nil, "catalog", jsonArg{`[]`}, // package_sku NULL, price_source catalog, no lines
			"capped", nil, nil, // overage_mode capped, no grow ceiling, no spend limit
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("order-legacy", time.Now()))
	mock.ExpectExec(regexp.QuoteMeta(
		"INSERT INTO credit_ledger (customer_id, amount_omr, reason, order_id)",
	)).WithArgs("cust-legacy", -100, "order-payment", "order-legacy").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO subscriptions")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("sub-legacy", time.Now(), time.Now()))
	mock.ExpectCommit()

	rec := postCheckout(t, h, checkoutRequest{PlanID: "plan-pro", TenantID: "org-legacy"}, "user-legacy")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp checkoutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.PaidByCredit || resp.CreditBalance != 100 {
		t.Errorf("settlement: %+v", resp)
	}
	if resp.QuoteResponse == nil || resp.PriceSource != store.PriceSourceCatalog || resp.PackageSKU != "" ||
		resp.AmountBaisa != 100000 || resp.AmountOMR != 100 || len(resp.Lines) != 0 {
		t.Errorf("quote on response = %+v", resp.QuoteResponse)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected store interactions: %v", err)
	}
}

// The full credit-only settlement of a BSS-priced order: plan M + backup
// (10.500) against 11 OMR of standing credit. The order row carries the exact
// baisa total, the whole-OMR view rounded up, the package sku, the price
// source and the lines; the response carries the same.
func TestCheckout_PackageM_WithBackup_CreditOnly_PersistsProvenance(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Store: store.New(db), Packages: packages.NewClient(bss.URL, nil)}

	expectCustomer(mock, "user-bss", "cust-bss", "org-bss")
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT)",
	)).WithArgs("cust-bss").
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(11)))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WithArgs("cust-bss", "org-bss", "m",
			sqlmock.AnyArg(), jsonArg{`["addon.backup"]`}, "single-region",
			11, int64(10500), "completed", // whole-OMR view rounded up; exact baisa
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			"plan.m", "bss:OpenOva plans@2026-09-11",
			jsonArg{`[{"sku":"addon.backup","name":"Backup","amount_baisa":1500}]`},
			"capped", nil, nil, // overage_mode capped, no grow ceiling, no spend limit
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("order-bss", time.Now()))
	mock.ExpectExec(regexp.QuoteMeta(
		"INSERT INTO credit_ledger (customer_id, amount_omr, reason, order_id)",
	)).WithArgs("cust-bss", -11, "order-payment", "order-bss").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO subscriptions")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("sub-bss", time.Now(), time.Now()))
	mock.ExpectCommit()

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "m", PackageSKU: "plan.m", TenantID: "org-bss", Addons: []string{"addon.backup"},
	}, "user-bss")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp checkoutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.PaidByCredit || resp.OrderID != "order-bss" || resp.CreditBalance != 0 {
		t.Errorf("settlement: %+v", resp)
	}
	if resp.QuoteResponse == nil {
		t.Fatal("response carries no priced lines")
	}
	if resp.PriceSource != "bss:OpenOva plans@2026-09-11" || resp.PackageSKU != "plan.m" ||
		resp.AmountBaisa != 10500 || resp.AmountOMR != 11 || resp.PlanAmountBaisa != 9000 {
		t.Errorf("provenance on response = %+v", resp.QuoteResponse)
	}
	wantLines := []store.OrderLine{{SKU: "addon.backup", Name: "Backup", AmountBaisa: 1500}}
	if !reflect.DeepEqual(resp.Lines, wantLines) {
		t.Errorf("lines = %+v, want %+v", resp.Lines, wantLines)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected store interactions: %v", err)
	}
}

// Credit of 10 OMR against a 10.500 order does NOT cover it: the comparison
// is in baisa, and the handler moves on to the Stripe path (unconfigured here
// → 503 payment processor), rather than settling a short order from credit.
func TestCheckout_CreditShortByBaisa_IsNotCovered(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Store: store.New(db), Packages: packages.NewClient(bss.URL, nil)}

	expectCustomer(mock, "user-short", "cust-short", "org-short")
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT)",
	)).WithArgs("cust-short").
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(10)))
	mock.ExpectQuery(regexp.QuoteMeta(
		"SELECT stripe_secret_key, stripe_webhook_secret, stripe_public_key, updated_at",
	)).WillReturnRows(sqlmock.NewRows([]string{
		"stripe_secret_key", "stripe_webhook_secret", "stripe_public_key", "updated_at",
	}).AddRow("", "", "", time.Now()))

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "m", PackageSKU: "plan.m", TenantID: "org-short", Addons: []string{"addon.backup"},
	}, "user-short")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "payment processor") {
		t.Fatalf("want 503 payment-processor (credit short by 0.500), got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected store interactions: %v", err)
	}
}

// POST /billing/quote — the same lines Checkout would bill, with NO store
// wired (nil) and nothing created.
func TestQuote_PricesWithoutStoreOrOrder(t *testing.T) {
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)} // Store nil on purpose
	mux := h.Routes()

	post := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/billing/quote", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec
	}

	rec := post(`{"plan_id":"m","package_sku":"plan.m","addons":["addon.backup"],"topology":"single-region"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var q QuoteResponse
	if err := json.NewDecoder(rec.Body).Decode(&q); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if q.AmountBaisa != 10500 || q.PlanAmountBaisa != 9000 || q.PriceSource != "bss:OpenOva plans@2026-09-11" ||
		q.PackageSKU != "plan.m" || q.PlanID != "m" || q.Topology != topologySingleRegion || q.Currency != "OMR" {
		t.Errorf("quote = %+v", q)
	}
	if len(q.Lines) != 1 || q.Lines[0].SKU != "addon.backup" || q.Lines[0].AmountBaisa != 1500 {
		t.Errorf("lines = %+v", q.Lines)
	}

	// Redundant on XL — reported, priced at 0.
	rec = post(`{"package_sku":"plan.xl","addons":["addon.backup"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("XL: want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	_ = json.NewDecoder(rec.Body).Decode(&q)
	if q.AmountBaisa != 30000 || len(q.Lines) != 1 || !q.Lines[0].Redundant {
		t.Errorf("XL quote = %+v", q)
	}

	// Not offered → 422 naming both; unknown topology → 400; nothing to price → 400.
	if rec = post(`{"package_sku":"plan.s","addons":["addon.dedicated-ip"]}`); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), "addon.dedicated-ip") || !strings.Contains(rec.Body.String(), "plan.s") {
		t.Errorf("not offered: got %d %s", rec.Code, rec.Body.String())
	}
	if rec = post(`{"package_sku":"plan.m","topology":"multi-region"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad topology: got %d", rec.Code)
	}
	if rec = post(`{"addons":["addon.backup"]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no plan or package: got %d", rec.Code)
	}
}

// POST /billing/quote without package_sku prices from the catalog, hot-standby
// surcharge included, and says so.
func TestQuote_CatalogPath(t *testing.T) {
	catalog := fakeCatalogServer(t, "plan-m", 9)
	defer catalog.Close()
	bss := mustNotBeCalledBSS(t)
	defer bss.Close()
	h := &Handler{CatalogURL: catalog.URL, Packages: packages.NewClient(bss.URL, nil)}

	r := httptest.NewRequest(http.MethodPost, "/billing/quote",
		strings.NewReader(`{"plan_id":"plan-m","topology":"active-hot-standby"}`))
	rec := httptest.NewRecorder()
	h.Quote(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	raw, _ := io.ReadAll(rec.Body)
	var q QuoteResponse
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if q.PriceSource != store.PriceSourceCatalog || q.AmountBaisa != 14000 || q.TopologyAmountBaisa != 5000 || q.AmountOMR != 14 {
		t.Errorf("quote = %+v", q)
	}
}

// BSS down → the quote answers 503 "prices unavailable" too.
func TestQuote_BSSDown_503(t *testing.T) {
	bss := fakeBSS(t, http.StatusInternalServerError)
	defer bss.Close()
	h := &Handler{Packages: packages.NewClient(bss.URL, nil)}
	r := httptest.NewRequest(http.MethodPost, "/billing/quote", strings.NewReader(`{"package_sku":"plan.m"}`))
	rec := httptest.NewRecorder()
	h.Quote(rec, r)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "prices unavailable") {
		t.Fatalf("want 503 prices unavailable, got %d (body=%s)", rec.Code, rec.Body.String())
	}
}
