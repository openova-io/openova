package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/store"
)

func getJSON(t *testing.T, h http.Handler, path string, jar string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	if jar != "" {
		r.Header.Set("X-Forwarded-Email", jar)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// THE property that makes the split trustworthy: every row's share must add
// up to the whole, or cost silently disappears between the cloud total and
// the rows a customer is shown (#6850, ADR-0014 D3).
func TestAllocationSharesSumToOne(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	seedAllocationUsage(t, st)

	code, body := getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", opEmail)
	if code != 200 {
		t.Fatalf("allocation: got %d, want 200", code)
	}
	rows, _ := body["rows"].([]any)
	if len(rows) == 0 {
		t.Fatal("no allocation rows — the split would show an empty screen on a busy Sovereign")
	}
	total, _ := body["share_total"].(float64)
	if total < 0.999999 || total > 1.000001 {
		t.Fatalf("shares sum to %v, want 1 — cost is being lost or double-counted between the cloud total and the rows", total)
	}
}

// The overhead line must be present and distinguishable, or the Sovereign's
// own footprint gets billed to a tenant.
func TestAllocationSeparatesOverheadFromTenants(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	seedAllocationUsage(t, st)

	_, body := getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", opEmail)
	if n, _ := body["platform_overhead"].(float64); n < 1 {
		t.Fatal("no platform-overhead row — the Sovereign's own footprint would be billed as tenant consumption")
	}
	if n, _ := body["organization_rows"].(float64); n < 1 {
		t.Fatal("no organization row — tenant consumption is not being separated")
	}
}

// Cross-customer data must never reach a customer-scoped caller.
func TestAllocationIsOperatorOnly(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Forwarded-Email")

	code, _ := getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", "")
	if code != 401 {
		t.Fatalf("unauthenticated allocation: got %d, want 401", code)
	}
	code, _ = getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", "stranger@example.com")
	if code == 200 {
		t.Fatal("a non-operator read the cross-customer allocation view")
	}
}

// An empty window must report zero shares, not an invented even split.
func TestAllocationEmptyWindowInventsNothing(t *testing.T) {
	h, _ := setupGateAPI(t, "X-Forwarded-Email")

	code, body := getJSON(t, h, "/api/v1/allocation?from=2019-01-01&to=2019-01-02", opEmail)
	if code != 200 {
		t.Fatalf("empty window: got %d, want 200", code)
	}
	if total, _ := body["share_total"].(float64); total != 0 {
		t.Fatalf("empty window reported share_total=%v — numbers with no measurement behind them", total)
	}
}

// seedAllocationUsage writes platform usage for one tenant Organization and
// one platform-overhead row, so the split has both tiers to separate.
func seedAllocationUsage(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	tenant, err := st.CreateCustomer(ctx, store.CustomerInput{
		Slug: "acme", Name: "Acme", AdminEmail: "a@acme.test", Kind: "organization", BillingMode: "showback",
	})
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	sov, err := st.CreateCustomer(ctx, store.CustomerInput{
		Slug: "sov", Name: "Sovereign", AdminEmail: "o@sov.test", Kind: "organization", BillingMode: "showback",
	})
	if err != nil {
		t.Fatalf("create sovereign org: %v", err)
	}
	mk := func(cust string, overhead bool, qty string) store.UsageRecord {
		src, _, err := st.UpsertSource(ctx, cust, "openova-org", "", cust)
		if err != nil {
			t.Fatalf("source: %v", err)
		}
		lb := `{"namespace":"n"}`
		if overhead {
			lb = `{"namespace":"gitea","tier":"platform-overhead"}`
		}
		return store.UsageRecord{
			CustomerID: cust, SourceID: src.ID,
			ResourceID: "r-" + cust + qty, ResourceKind: "k8s-pod",
			SKU: "k8s.vcpu", Quantity: store.Decimal(qty), Unit: "vcpu-hour",
			WindowStart: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			WindowEnd:   time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC),
			Labels:      json.RawMessage(lb),
		}
	}
	if _, err := st.UpsertUsage(ctx, []store.UsageRecord{
		mk(tenant.ID, false, "3.000000"),
		mk(sov.ID, true, "1.000000"),
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
}

func putJSON(t *testing.T, h http.Handler, path string, email string, v any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(v)
	r := httptest.NewRequest("PUT", path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	if email != "" {
		r.Header.Set("X-Forwarded-Email", email)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// The allocation document carries money, not just shares (#6867): the
// pool it split, per-row allocated cost / revenue / margin, and totals —
// and the wire keys are pinned by name so the page cannot read zeros.
func TestAllocationCarriesPoolAndMoney(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	seedAllocationUsage(t, st)

	code, body := getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", opEmail)
	if code != 200 {
		t.Fatalf("allocation: got %d, want 200", code)
	}
	for _, k := range []string{"from", "to", "settings", "pool", "rows", "share_total", "totals", "organization_rows", "platform_overhead"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("allocation document lacks %q", k)
		}
	}
	pool := body["pool"].(map[string]any)
	// The seed has no verified huawei-project source, so the pool is
	// unresolved and says what to set instead of inventing an amount.
	if pool["source"] != "unresolved" || pool["amount"].(float64) != 0 || pool["note"] == nil || pool["note"] == "" {
		t.Fatalf("pool = %+v", pool)
	}
	row := body["rows"].([]any)[0].(map[string]any)
	for _, k := range []string{"customer_id", "customer_slug", "customer_name", "tier", "vcpu_hours", "mem_gib_hours", "pvc_gb_hours", "weight", "share", "allocated_cost", "rated_revenue", "margin", "margin_pct"} {
		if _, ok := row[k]; !ok {
			t.Fatalf("allocation row lacks %q: %+v", k, row)
		}
	}
	totals := body["totals"].(map[string]any)
	for _, k := range []string{"allocated", "revenue", "margin"} {
		if _, ok := totals[k]; !ok {
			t.Fatalf("totals lacks %q", k)
		}
	}
	if body["from"] != "2020-01-01" || body["to"] != "2030-01-01" {
		t.Fatalf("window echoed as %v..%v", body["from"], body["to"])
	}
	// No from/to: the current calendar month.
	code, body = getJSON(t, h, "/api/v1/allocation", opEmail)
	if code != 200 || body["from"].(string)[8:] != "01" {
		t.Fatalf("default window: %d %v..%v", code, body["from"], body["to"])
	}
}

// Settings are readable, editable, validated and audited — the founder's
// complaint was that the basis was a constant nobody could see or change.
func TestAllocationSettingsRoundTrip(t *testing.T) {
	h, st := setupGateAPI(t, "X-Forwarded-Email")
	seedAllocationUsage(t, st)

	code, body := getJSON(t, h, "/api/v1/allocation/settings", opEmail)
	if code != 200 {
		t.Fatalf("get settings: %d", code)
	}
	w := body["weights"].(map[string]any)
	if w["vcpu"].(float64) != 1 || w["mem_gib"].(float64) != 1 || w["pvc_gb"].(float64) != 1 || body["overhead_policy"] != "separate" || body["pool"] != "sovereign-cost" || body["sovereign_customer_id"] != nil {
		t.Fatalf("default settings = %+v", body)
	}

	// Edit: manual pool of 1200 USD, vCPU-only, distribute.
	code, body = putJSON(t, h, "/api/v1/allocation/settings", opEmail, map[string]any{
		"weights": map[string]any{"vcpu": 2, "mem_gib": 0, "pvc_gb": 0}, "overhead_policy": "distribute",
		"pool": "manual", "manual_amount": 1200, "currency": "usd", "sovereign_customer_id": nil,
	})
	if code != 200 || body["currency"] != "USD" || body["pool"] != "manual" || body["manual_amount"].(float64) != 1200 || body["overhead_policy"] != "distribute" {
		t.Fatalf("put settings: %d %+v", code, body)
	}
	// The split now follows the settings: overhead distributed, pool 1200.
	code, body = getJSON(t, h, "/api/v1/allocation?from=2020-01-01&to=2030-01-01", opEmail)
	if code != 200 {
		t.Fatalf("allocation after edit: %d", code)
	}
	pool := body["pool"].(map[string]any)
	if pool["source"] != "manual" || pool["amount"].(float64) != 1200 || pool["currency"] != "USD" {
		t.Fatalf("pool after edit = %+v", pool)
	}
	if body["platform_overhead"].(float64) != 0 || body["organization_rows"].(float64) != 1 {
		t.Fatalf("distribute left rows = org %v / overhead %v", body["organization_rows"], body["platform_overhead"])
	}
	if total, _ := body["share_total"].(float64); total < 0.999999 || total > 1.000001 {
		t.Fatalf("shares after distribute sum to %v", total)
	}
	if body["totals"].(map[string]any)["allocated"].(float64) != 1200 {
		t.Fatalf("allocated total = %v, want the whole pool", body["totals"])
	}
	// Audited.
	var n int
	if err := st.DB().QueryRow(`SELECT count(*) FROM audit_log WHERE action = 'allocation.settings' AND actor = $1`, opEmail).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit entries = %d err=%v", n, err)
	}

	// Validation: 400 with the field named; a bad Sovereign customer too.
	for _, bad := range []map[string]any{
		{"weights": map[string]any{"vcpu": 0, "mem_gib": 0, "pvc_gb": 0}, "overhead_policy": "separate", "pool": "manual", "manual_amount": 0, "currency": "OMR"},
		{"weights": map[string]any{"vcpu": -1, "mem_gib": 1, "pvc_gb": 1}, "overhead_policy": "separate", "pool": "manual", "manual_amount": 0, "currency": "OMR"},
		{"weights": map[string]any{"vcpu": 1, "mem_gib": 1, "pvc_gb": 1}, "overhead_policy": "average", "pool": "manual", "manual_amount": 0, "currency": "OMR"},
		{"weights": map[string]any{"vcpu": 1, "mem_gib": 1, "pvc_gb": 1}, "overhead_policy": "separate", "pool": "manual", "manual_amount": -3, "currency": "OMR"},
		{"weights": map[string]any{"vcpu": 1, "mem_gib": 1, "pvc_gb": 1}, "overhead_policy": "separate", "pool": "manual", "manual_amount": 0, "currency": "rials"},
		{"weights": map[string]any{"vcpu": 1, "mem_gib": 1, "pvc_gb": 1}, "overhead_policy": "separate", "pool": "sovereign-cost", "manual_amount": 0, "currency": "OMR", "sovereign_customer_id": "00000000-0000-0000-0000-000000000000"},
		{"weights": map[string]any{"vcpu": 1}, "colour": "blue"},
	} {
		if code, body := putJSON(t, h, "/api/v1/allocation/settings", opEmail, bad); code != 400 || body["error"] == "" {
			t.Fatalf("%+v: got %d %+v, want 400", bad, code, body)
		}
	}
	// A rejected update changed nothing.
	if _, body := getJSON(t, h, "/api/v1/allocation/settings", opEmail); body["currency"] != "USD" {
		t.Fatalf("settings after rejected updates = %+v", body)
	}

	// Operator-only, both verbs.
	if code, _ := getJSON(t, h, "/api/v1/allocation/settings", "a@acme.test"); code == 200 {
		t.Fatal("a customer read the allocation settings")
	}
	if code, _ := putJSON(t, h, "/api/v1/allocation/settings", "a@acme.test", map[string]any{"weights": map[string]any{"vcpu": 1}, "overhead_policy": "separate", "pool": "manual", "currency": "OMR"}); code == 200 {
		t.Fatal("a customer edited the allocation settings")
	}
	if code, _ := getJSON(t, h, "/api/v1/allocation/settings", ""); code != 401 {
		t.Fatalf("unauthenticated settings read: %d", code)
	}
}
