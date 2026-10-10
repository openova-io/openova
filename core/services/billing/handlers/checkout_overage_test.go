package handlers

// checkout_overage_test.go — the overage mode on POST /billing/quote and
// POST /billing/checkout (founder model 2026-10-10). A package is a prepaid
// minimum commitment; the order chooses `capped` (default) or `grow` (quota
// raised to a ceiling, usage above the package allowance billed in arrears at
// the package's overage rates). DR active-passive is included on XL and, on
// S/M/L, available only in grow mode at no upfront price.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/openova-io/openova/core/services/billing/packages"
	"github.com/openova-io/openova/core/services/billing/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// growContractBody: grow on all four packages with the XL ceiling, per-package
// overage rates, the headline on every package, and dr_topology grow_only on
// S/M/L (active-passive on XL).
const growContractBody = `{"currency":"OMR","price_book":"OpenOva plans","prices_as_of":"2026-10-10",
 "packages":[
  {"sku":"plan.s","name":"S","price_month":"2.490","includes":{"vcpu":1,"memory_gb":2,"disk_gb":25,"bandwidth_mbps":50},
   "grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[
    {"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.992"},
    {"key":"memory","sku":"k8s.mem_gb","unit":"GB","price_month":"0.374"},
    {"key":"disk","sku":"k8s.pvc_gb","unit":"GB","price_month":"0.035"},
    {"key":"bandwidth","sku":"eip.bandwidth_mbps","unit":"Mbps","price_month":"1.253"}]}},
  {"sku":"plan.m","name":"M","price_month":"4.490","includes":{"vcpu":2,"memory_gb":4,"disk_gb":50,"bandwidth_mbps":100},
   "grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[
    {"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.796"},
    {"key":"memory","sku":"k8s.mem_gb","unit":"GB","price_month":"0.337"},
    {"key":"disk","sku":"k8s.pvc_gb","unit":"GB","price_month":"0.035"},
    {"key":"bandwidth","sku":"eip.bandwidth_mbps","unit":"Mbps","price_month":"1.253"}]}},
  {"sku":"plan.l","name":"L","price_month":"7.990","includes":{"vcpu":4,"memory_gb":8,"disk_gb":100,"bandwidth_mbps":250},
   "grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[
    {"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.598"},
    {"key":"memory","sku":"k8s.mem_gb","unit":"GB","price_month":"0.300"},
    {"key":"disk","sku":"k8s.pvc_gb","unit":"GB","price_month":"0.035"},
    {"key":"bandwidth","sku":"eip.bandwidth_mbps","unit":"Mbps","price_month":"1.253"}]}},
  {"sku":"plan.xl","name":"XL","price_month":"13.990","includes":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},
   "grow":{"allowed":true,"ceiling":{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000},"overage_rates":[
    {"key":"vcpu","sku":"k8s.vcpu","unit":"vCPU","price_month":"1.399"},
    {"key":"memory","sku":"k8s.mem_gb","unit":"GB","price_month":"0.263"},
    {"key":"disk","sku":"k8s.pvc_gb","unit":"GB","price_month":"0.035"},
    {"key":"bandwidth","sku":"eip.bandwidth_mbps","unit":"Mbps","price_month":"1.253"}]}}],
 "features":[
  {"key":"backup","name":"Backup","kind":"boolean","cells":{
    "plan.s":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
    "plan.m":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
    "plan.l":{"state":"optional","addon_sku":"addon.backup","price_month":"1.500","included_from":"plan.xl"},
    "plan.xl":{"state":"included"}}},
  {"key":"dr_topology","name":"DR topology","kind":"level","levels":["single region","active-passive"],"cells":{
    "plan.s":{"state":"optional","level":0,"grow_only":true,"included_from":"plan.xl"},
    "plan.m":{"state":"optional","level":0,"grow_only":true,"included_from":"plan.xl"},
    "plan.l":{"state":"optional","level":0,"grow_only":true,"included_from":"plan.xl"},
    "plan.xl":{"state":"included","level":1}}}
 ]}`

func growBSS(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != packages.Path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(growContractBody))
	}))
}

// quote posts a raw body to /billing/quote on a handler with no store.
func quote(t *testing.T, bssURL, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := &Handler{Packages: packages.NewClient(bssURL, nil)}
	r := httptest.NewRequest(http.MethodPost, "/billing/quote", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.Quote(rec, r)
	return rec
}

func decodeQuote(t *testing.T, rec *httptest.ResponseRecorder) (QuoteResponse, map[string]any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	raw := rec.Body.Bytes()
	var q QuoteResponse
	var m map[string]any
	if err := json.Unmarshal(raw, &q); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &m)
	return q, m
}

var xlCeiling = events.GrowCeiling{VCPU: 8, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 1000}

var mRates = []overageRateJSON{
	{Key: "vcpu", SKU: "k8s.vcpu", Unit: "vCPU", PriceMonth: "1.796"},
	{Key: "memory", SKU: "k8s.mem_gb", Unit: "GB", PriceMonth: "0.337"},
	{Key: "disk", SKU: "k8s.pvc_gb", Unit: "GB", PriceMonth: "0.035"},
	{Key: "bandwidth", SKU: "eip.bandwidth_mbps", Unit: "Mbps", PriceMonth: "1.253"},
}

// M capped: the mode is echoed, no ceiling, no spend limit — and the
// package's rates ride along so the storefront can show what grow would cost.
func TestQuote_MCapped_EchoesCappedAndRatesNoCeiling(t *testing.T) {
	bss := growBSS(t)
	defer bss.Close()
	for _, body := range []string{
		`{"package_sku":"plan.m"}`,
		`{"package_sku":"plan.m","overage_mode":"capped"}`,
		`{"package_sku":"plan.m","overage_mode":"capped","grow_ceiling":{}}`,
	} {
		q, m := decodeQuote(t, quote(t, bss.URL, body))
		if q.OverageMode != "capped" || q.GrowCeiling != nil || q.SpendLimitMonth != "" {
			t.Errorf("%s: overage = %q %+v %q", body, q.OverageMode, q.GrowCeiling, q.SpendLimitMonth)
		}
		if _, has := m["grow_ceiling"]; has {
			t.Errorf("%s: capped quote carries grow_ceiling: %v", body, m)
		}
		if _, has := m["spend_limit_month"]; has {
			t.Errorf("%s: capped quote carries spend_limit_month: %v", body, m)
		}
		if !reflect.DeepEqual(q.OverageRates, mRates) {
			t.Errorf("%s: rates = %+v, want M's %+v", body, q.OverageRates, mRates)
		}
		if q.AmountBaisa != 4490 {
			t.Errorf("%s: total = %d, want the package alone (4490)", body, q.AmountBaisa)
		}
	}
}

// M grow with no ceiling: the resolved ceiling is the package's (XL's) and the
// rates are M's; S echoes S's rates.
func TestQuote_MGrow_ResolvesThePackageCeiling(t *testing.T) {
	bss := growBSS(t)
	defer bss.Close()
	q, m := decodeQuote(t, quote(t, bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","spend_limit_month":"25"}`))
	if q.OverageMode != "grow" || q.GrowCeiling == nil || *q.GrowCeiling != xlCeiling {
		t.Fatalf("grow quote: mode %q ceiling %+v", q.OverageMode, q.GrowCeiling)
	}
	if want := map[string]any{"vcpu": 8.0, "memory_gb": 16.0, "disk_gb": 250.0, "bandwidth_mbps": 1000.0}; !reflect.DeepEqual(m["grow_ceiling"], want) {
		t.Errorf("wire grow_ceiling = %v, want %v", m["grow_ceiling"], want)
	}
	if q.SpendLimitMonth != "25.000" {
		t.Errorf("spend limit = %q, want 25.000 (normalised)", q.SpendLimitMonth)
	}
	if !reflect.DeepEqual(q.OverageRates, mRates) || q.OverageRates[0].PriceMonth != "1.796" {
		t.Errorf("rates = %+v", q.OverageRates)
	}
	if q.AmountBaisa != 4490 {
		t.Errorf("grow is billed in arrears: upfront total = %d, want 4490", q.AmountBaisa)
	}

	// A partial ceiling: the given value kept, the others the package's.
	q, _ = decodeQuote(t, quote(t, bss.URL, `{"package_sku":"plan.m","overage_mode":"GROW","grow_ceiling":{"vcpu":4,"memory_gb":0,"bandwidth_mbps":100}}`))
	if want := (events.GrowCeiling{VCPU: 4, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 100}); q.GrowCeiling == nil || *q.GrowCeiling != want {
		t.Errorf("partial ceiling resolved to %+v, want %+v", q.GrowCeiling, want)
	}

	q, _ = decodeQuote(t, quote(t, bss.URL, `{"package_sku":"plan.s"}`))
	if len(q.OverageRates) != 4 || q.OverageRates[0].PriceMonth != "1.992" || q.OverageRates[1].PriceMonth != "0.374" {
		t.Errorf("S rates = %+v, want vcpu 1.992 / memory 0.374", q.OverageRates)
	}
}

// The catalog path and a document without grow carry no rates.
func TestQuote_NoGrowBlock_NoRates(t *testing.T) {
	bss := fakeBSS(t, http.StatusOK)
	defer bss.Close()
	q, m := decodeQuote(t, quote(t, bss.URL, `{"package_sku":"plan.m"}`))
	if q.OverageMode != "capped" {
		t.Errorf("mode = %q", q.OverageMode)
	}
	if _, has := m["overage_rates"]; has {
		t.Errorf("a package without grow echoes rates: %v", m["overage_rates"])
	}
}

// Every overage rule that answers 400, with the sentence that says why.
func TestQuote_OverageValidation_400(t *testing.T) {
	bss := growBSS(t)
	defer bss.Close()
	v1 := fakeBSS(t, http.StatusOK) // no grow block on any package
	defer v1.Close()
	for _, tc := range []struct {
		name, url, body string
		want            []string
	}{
		{"grow on a package without grow", v1.URL, `{"package_sku":"plan.m","overage_mode":"grow"}`,
			[]string{"the M package does not offer grow mode"}},
		{"ceiling below the headline", bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","grow_ceiling":{"vcpu":1}}`,
			[]string{"vcpu", "1", "between 2", "and 8", "M"}},
		{"ceiling above the package ceiling", bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","grow_ceiling":{"vcpu":9}}`,
			[]string{"vcpu", "9", "between 2", "and 8"}},
		{"fractional memory above", bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","grow_ceiling":{"memory_gb":16.5}}`,
			[]string{"memory_gb", "16.5", "between 4", "and 16"}},
		{"negative ceiling", bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","grow_ceiling":{"disk_gb":-1}}`,
			[]string{"disk_gb", "negative"}},
		{"ceiling with capped", bss.URL, `{"package_sku":"plan.m","grow_ceiling":{"vcpu":4}}`,
			[]string{"grow_ceiling", "only in grow mode"}},
		{"spend limit with capped", bss.URL, `{"package_sku":"plan.m","overage_mode":"capped","spend_limit_month":"25.000"}`,
			[]string{"spend_limit_month", "only in grow mode"}},
		{"malformed spend limit", bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","spend_limit_month":"25.0001"}`,
			[]string{"spend_limit_month", "25.0001"}},
		{"non-numeric spend limit", bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","spend_limit_month":"lots"}`,
			[]string{"spend_limit_month"}},
		{"zero spend limit", bss.URL, `{"package_sku":"plan.m","overage_mode":"grow","spend_limit_month":"0.000"}`,
			[]string{"more than zero"}},
		{"grow without a package", bss.URL, `{"plan_id":"m","overage_mode":"grow"}`,
			[]string{"grow mode needs a package"}},
		{"ceiling without a package", bss.URL, `{"plan_id":"m","grow_ceiling":{"vcpu":4}}`,
			[]string{"grow mode needs a package"}},
		{"unknown mode", bss.URL, `{"package_sku":"plan.m","overage_mode":"unlimited"}`,
			[]string{"overage_mode", "unlimited"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := quote(t, tc.url, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (body=%s)", rec.Code, rec.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			msg, _ := body["error"].(string)
			if len(body) != 1 || msg == "" {
				t.Fatalf("400 body must be {\"error\": <sentence>}, got %v", body)
			}
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error %q does not contain %q", msg, w)
				}
			}
		})
	}
}

// A headline the document does not state bounds nothing: only the upper
// bound is checked.
func TestResolveGrowCeiling_MissingHeadlineChecksOnlyTheUpperBound(t *testing.T) {
	pkg := packages.Package{SKU: "plan.m", Name: "M",
		Includes: packages.Resources{"memory_gb": 4},
		Grow:     &packages.Grow{Allowed: true, Ceiling: packages.Resources{"vcpu": 8, "memory_gb": 16, "disk_gb": 250, "bandwidth_mbps": 1000}}}
	got, err := resolveGrowCeiling(pkg, &events.GrowCeiling{VCPU: 0.5, DiskGB: 10})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if want := (events.GrowCeiling{VCPU: 0.5, MemoryGB: 16, DiskGB: 10, BandwidthMbps: 1000}); *got != want {
		t.Errorf("resolved = %+v, want %+v", *got, want)
	}
	if _, err := resolveGrowCeiling(pkg, &events.GrowCeiling{VCPU: 8.5}); err == nil || !strings.Contains(err.Error(), "at most 8") {
		t.Errorf("above the ceiling without a headline: %v", err)
	}
	if _, err := resolveGrowCeiling(pkg, &events.GrowCeiling{MemoryGB: 3}); err == nil {
		t.Errorf("below a stated headline must still be refused")
	}
}

// DR active-passive on a grow_only package: refused when capped (422, the
// message names grow mode), free upfront in grow mode; XL includes it.
func TestQuote_ActivePassive_GrowOnly(t *testing.T) {
	bss := growBSS(t)
	defer bss.Close()

	rec := quote(t, bss.URL, `{"package_sku":"plan.m","topology":"active-hot-standby"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("M capped + active-passive: want 422, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var refused map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &refused)
	if msg, _ := refused["error"].(string); !strings.Contains(msg, "only in grow mode") || refused["package_sku"] != "plan.m" {
		t.Errorf("refusal = %v", refused)
	}

	q, _ := decodeQuote(t, quote(t, bss.URL, `{"package_sku":"plan.m","topology":"active-hot-standby","overage_mode":"grow"}`))
	if q.TopologyAmountBaisa != 0 || q.AmountBaisa != 4490 || q.Topology != topologyActiveHotStandby || q.OverageMode != "grow" {
		t.Errorf("M grow + active-passive = %+v", q)
	}

	q, _ = decodeQuote(t, quote(t, bss.URL, `{"package_sku":"plan.xl","topology":"active-hot-standby"}`))
	if q.TopologyAmountBaisa != 0 || q.AmountBaisa != 13990 || q.OverageMode != "capped" {
		t.Errorf("XL capped + active-passive = %+v", q)
	}
}

// The credit-only settlement of a grow order: the order row persists the
// mode, the resolved ceiling and the spend limit; order.placed carries them;
// and the settlement launch hands them to core/services/tenant.
func TestCheckout_Grow_CreditOnly_PersistsOverageAndHandsItOver(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	bss := growBSS(t)
	defer bss.Close()

	const tenantID = "org-grow"
	var (
		mu     sync.Mutex
		launch []byte
	)
	tenant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/subdomain"):
			_, _ = w.Write([]byte(`{"subdomain":"grow"}`))
		case strings.HasSuffix(r.URL.Path, "/app-configs"):
			_, _ = w.Write([]byte(`{"app_configs":{}}`))
		case strings.HasSuffix(r.URL.Path, "/launch"):
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			launch = b
			mu.Unlock()
			_, _ = w.Write([]byte(`{"launched":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer tenant.Close()
	prod := &capturingProducer{}
	h := &Handler{Store: store.New(db), Packages: packages.NewClient(bss.URL, nil), Producer: prod, TenantURL: tenant.URL}

	const wantCeiling = `{"vcpu":4,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000}`
	expectCustomer(mock, "user-grow", "cust-grow", tenantID)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(CAST(SUM(amount_omr) AS BIGINT)")).
		WithArgs("cust-grow").
		WillReturnRows(sqlmock.NewRows([]string{"balance"}).AddRow(int64(5)))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO orders")).
		WithArgs("cust-grow", tenantID, "m",
			sqlmock.AnyArg(), sqlmock.AnyArg(), "active-hot-standby",
			5, int64(4490), "completed",
			sqlmock.AnyArg(), sqlmock.AnyArg(),
			"plan.m", "bss:OpenOva plans@2026-10-10", jsonArg{`[]`},
			"grow", jsonArg{wantCeiling}, "25.000",
		).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at"}).AddRow("order-grow", time.Now()))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO credit_ledger (customer_id, amount_omr, reason, order_id)")).
		WithArgs("cust-grow", -5, "order-payment", "order-grow").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO subscriptions")).
		WillReturnRows(sqlmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("sub-grow", time.Now(), time.Now()))
	mock.ExpectCommit()

	rec := postCheckout(t, h, checkoutRequest{
		PlanID: "m", PackageSKU: "plan.m", TenantID: tenantID, Topology: "active-hot-standby",
		OverageMode: "grow", GrowCeiling: &events.GrowCeiling{VCPU: 4}, SpendLimitMonth: "25",
	}, "user-grow")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	var resp checkoutResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.QuoteResponse == nil || resp.OverageMode != "grow" || resp.SpendLimitMonth != "25.000" ||
		resp.GrowCeiling == nil || *resp.GrowCeiling != (events.GrowCeiling{VCPU: 4, MemoryGB: 16, DiskGB: 250, BandwidthMbps: 1000}) ||
		len(resp.OverageRates) != 4 {
		t.Errorf("checkout response overage = %+v", resp.QuoteResponse)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("store interactions: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	var got struct {
		OverageMode     string              `json:"overage_mode"`
		GrowCeiling     *events.GrowCeiling `json:"grow_ceiling"`
		SpendLimitMonth string              `json:"spend_limit_month"`
		PackageSKU      string              `json:"package_sku"`
	}
	if err := json.Unmarshal(launch, &got); err != nil {
		t.Fatalf("launch body: %v (%s)", err, launch)
	}
	if got.OverageMode != "grow" || got.SpendLimitMonth != "25.000" || got.PackageSKU != "plan.m" ||
		got.GrowCeiling == nil || got.GrowCeiling.VCPU != 4 || got.GrowCeiling.BandwidthMbps != 1000 {
		t.Errorf("launch body = %s", launch)
	}

	for _, e := range prod.published {
		if e.Type != "order.placed" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p["overage_mode"] != "grow" || p["spend_limit_month"] != "25.000" {
			t.Errorf("order.placed overage: %v %v", p["overage_mode"], p["spend_limit_month"])
		}
		if c, _ := p["grow_ceiling"].(map[string]any); c["vcpu"] != 4.0 {
			t.Errorf("order.placed grow_ceiling = %v", p["grow_ceiling"])
		}
		return
	}
	t.Fatalf("no order.placed published; got %v", prod.types())
}

func TestSettlementLaunchBody_OverageMode(t *testing.T) {
	// A package order with no mode on the row (capped by default) says so.
	b := settlementLaunchBody(&store.Order{ID: "o1", TenantID: "t", PackageSKU: "plan.m"})
	if b.OverageMode != "capped" || b.GrowCeiling != nil || b.SpendLimitMonth != "" {
		t.Errorf("capped package launch body: %+v", b)
	}
	wire, _ := json.Marshal(b)
	if !strings.Contains(string(wire), `"overage_mode":"capped"`) || strings.Contains(string(wire), "grow_ceiling") ||
		strings.Contains(string(wire), "spend_limit_month") {
		t.Errorf("capped wire = %s", wire)
	}
	// A legacy catalog order hands over no overage keys at all.
	legacy, _ := json.Marshal(settlementLaunchBody(&store.Order{ID: "o0", TenantID: "t", PriceSource: "catalog", OverageMode: "capped"}))
	for _, key := range []string{"overage_mode", "grow_ceiling", "spend_limit_month"} {
		if strings.Contains(string(legacy), key) {
			t.Errorf("legacy launch body leaked %s: %s", key, legacy)
		}
	}
	// A grow order's row hands over its ceiling and limit.
	g := settlementLaunchBody(&store.Order{ID: "o2", TenantID: "t", PackageSKU: "plan.s", OverageMode: "grow",
		GrowCeiling: json.RawMessage(`{"vcpu":8,"memory_gb":16,"disk_gb":250,"bandwidth_mbps":1000}`), SpendLimitMonth: "10.000"})
	if g.OverageMode != "grow" || g.GrowCeiling == nil || *g.GrowCeiling != xlCeiling || g.SpendLimitMonth != "10.000" {
		t.Errorf("grow launch body: %+v", g)
	}
}
