package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// orgBillingTestSecret is a throwaway HS256 secret so mintOrgBridgeToken
// produces a non-empty bearer (the handler short-circuits to a degraded
// payload when orgJWTSecret is empty).
var orgBillingTestSecret = []byte("test-org-jwt-secret-4274")

// withSovereignAdminClaims installs a Claims carrying the sovereign-admin
// realm role on the request context so mintOrgBridgeToken maps to the
// `sovereign-admin` upstream role (org/billing/admin/* now permits it).
func withSovereignAdminClaims(r *http.Request) *http.Request {
	claims := &auth.Claims{
		Sub:   "op-sub",
		Email: "billing-walk@omantel.biz",
		RealmAccess: auth.RealmAccess{
			Roles: []string{"sovereign-admin"},
		},
	}
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, claims))
}

// TestHandleListOrgOrders_MapsLiveUpstream pins the live-data wire: the
// billing service's store.Order shape is mapped into the FE Order shape
// (#4274). Asserts baisa→cents, plan→product label, status normalisation,
// and the OMR currency stamp.
func TestHandleListOrgOrders_MapsLiveUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/billing/admin/orders" {
			t.Errorf("upstream path = %q, want /api/billing/admin/orders", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got == "" {
			t.Errorf("upstream Authorization header missing — bridge token not forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"id":"ord_1","tenant_id":"acme","plan_id":"growth","apps":["wp"],"addons":[],
			 "amount_baisa":9000,"amount_omr":9,"status":"paid","created_at":"2026-06-20T10:00:00Z"},
			{"id":"ord_2","tenant_id":"globex","plan_id":"starter","apps":[],"addons":[],
			 "amount_baisa":0,"amount_omr":5,"status":"pending","created_at":"2026-06-21T10:00:00Z"}
		]`))
	}))
	defer upstream.Close()
	t.Setenv(orgGatewayURLEnv, upstream.URL)

	h := &Handler{orgJWTSecret: orgBillingTestSecret}
	r := withSovereignAdminClaims(httptest.NewRequest(http.MethodGet, "/api/v1/org/orders", nil))
	w := httptest.NewRecorder()
	h.HandleListOrgOrders(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp orgOrdersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v\nbody=%s", err, w.Body.String())
	}
	if len(resp.Orders) != 2 {
		t.Fatalf("orders len = %d, want 2; body=%s", len(resp.Orders), w.Body.String())
	}

	o1 := resp.Orders[0]
	if o1.ID != "ord_1" || o1.TenantOrg != "acme" {
		t.Errorf("order[0] id/org = %q/%q, want ord_1/acme", o1.ID, o1.TenantOrg)
	}
	if o1.Product != "growth + 1 add-on" {
		t.Errorf("order[0] product = %q, want 'growth + 1 add-on'", o1.Product)
	}
	if o1.Status != "completed" {
		t.Errorf("order[0] status = %q, want completed (paid→completed)", o1.Status)
	}
	if o1.TotalCents != 9000 {
		t.Errorf("order[0] totalCents = %d, want 9000 (baisa)", o1.TotalCents)
	}
	if o1.Currency != "OMR" {
		t.Errorf("order[0] currency = %q, want OMR", o1.Currency)
	}

	// order[1] has amount_baisa=0 → falls back to amount_omr*1000.
	o2 := resp.Orders[1]
	if o2.TotalCents != 5000 {
		t.Errorf("order[1] totalCents = %d, want 5000 (omr 5 → baisa)", o2.TotalCents)
	}
	if o2.Status != "pending" {
		t.Errorf("order[1] status = %q, want pending", o2.Status)
	}
}

// TestHandleListOrgOrders_DegradedReturnsEmpty asserts the read-only
// handler degrades to a 200 with an empty list (not a 5xx) when the
// bridge is unwired, so the FE renders its branded empty state.
func TestHandleListOrgOrders_DegradedReturnsEmpty(t *testing.T) {
	// orgJWTSecret empty → mintOrgBridgeToken returns 503 errResp →
	// fetchOrgBilling returns (nil,false) → handler emits empty 200.
	h := &Handler{}
	r := withSovereignAdminClaims(httptest.NewRequest(http.MethodGet, "/api/v1/org/orders", nil))
	w := httptest.NewRecorder()
	h.HandleListOrgOrders(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded)", w.Code)
	}
	var resp orgOrdersResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if resp.Orders == nil {
		t.Errorf("orders must serialise as [] not null so FE Array guard passes")
	}
	if len(resp.Orders) != 0 {
		t.Errorf("orders len = %d, want 0 on degraded path", len(resp.Orders))
	}
}

// TestComposeRevenue_BucketsAndBreakdown pins the granular revenue
// derivation (#4274): the per-day sparkline bucketing (oldest-first),
// the per-plan breakdown rollup (MRR desc, distinct-org count), and the
// top-plan / top-org KPI selection.
func TestComposeRevenue_BucketsAndBreakdown(t *testing.T) {
	now := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	mk := func(id, tenant, plan string, baisa int64, daysAgo int) billingOrder {
		return billingOrder{
			ID:          id,
			TenantID:    tenant,
			PlanID:      plan,
			AmountBaisa: baisa,
			Status:      "paid",
			CreatedAt:   now.Add(-time.Duration(daysAgo) * 24 * time.Hour),
		}
	}
	orders := []billingOrder{
		mk("o1", "acme", "growth", 10000, 0),   // today
		mk("o2", "globex", "growth", 5000, 3),  // 3 days ago
		mk("o3", "acme", "starter", 3000, 10),  // 10 days ago
		mk("o4", "initech", "growth", 2000, 40), // OUTSIDE 30d window
	}

	resp := composeRevenue(orders, now)

	// Sparkline: 30 points, oldest-first, today (index 29) = 10000.
	if len(resp.Sparkline) != sparklineDays {
		t.Fatalf("sparkline len = %d, want %d", len(resp.Sparkline), sparklineDays)
	}
	if today := resp.Sparkline[sparklineDays-1]; today != 10000 {
		t.Errorf("sparkline[today] = %d, want 10000", today)
	}
	if d3 := resp.Sparkline[sparklineDays-1-3]; d3 != 5000 {
		t.Errorf("sparkline[D-3] = %d, want 5000", d3)
	}

	// last30dCents excludes the 40-days-ago order (2000).
	if resp.Kpi.Last30dCents != 18000 {
		t.Errorf("last30dCents = %d, want 18000 (10000+5000+3000)", resp.Kpi.Last30dCents)
	}

	// Breakdown: growth all-time = 10000+5000+2000=17000 across 3 orgs;
	// starter = 3000 across 1 org. Growth sorts first (MRR desc).
	if len(resp.Breakdown) != 2 {
		t.Fatalf("breakdown rows = %d, want 2", len(resp.Breakdown))
	}
	growth := resp.Breakdown[0]
	if growth.Plan != "growth" {
		t.Errorf("breakdown[0].plan = %q, want growth (highest MRR)", growth.Plan)
	}
	if growth.MrrCents != 17000 {
		t.Errorf("growth MRR = %d, want 17000 (all-time, incl out-of-window)", growth.MrrCents)
	}
	if growth.Tenants != 3 {
		t.Errorf("growth tenants = %d, want 3 (acme/globex/initech)", growth.Tenants)
	}

	// KPI top-plan / top-org. acme = 10000+3000=13000 (highest org total).
	if resp.Kpi.TopPlan != "growth" {
		t.Errorf("topPlan = %q, want growth", resp.Kpi.TopPlan)
	}
	if resp.Kpi.TopTenant != "acme" {
		t.Errorf("topTenant = %q, want acme", resp.Kpi.TopTenant)
	}
}

// TestComposeRevenue_EmptyIsZeroFilled asserts an empty ledger yields the
// zero-filled-but-non-nil payload the FE Array guards rely on.
func TestComposeRevenue_EmptyIsZeroFilled(t *testing.T) {
	resp := composeRevenue(nil, time.Now().UTC())
	if resp.Sparkline == nil || len(resp.Sparkline) != sparklineDays {
		t.Errorf("sparkline must be a non-nil %d-len slice; got len=%d nil=%v",
			sparklineDays, len(resp.Sparkline), resp.Sparkline == nil)
	}
	if resp.Breakdown == nil {
		t.Errorf("breakdown must serialise as [] not null")
	}
	if resp.Kpi.Last30dCents != 0 || resp.Kpi.TopPlan != "" || resp.Kpi.TopTenant != "" {
		t.Errorf("empty ledger KPIs must be zero/blank, got %+v", resp.Kpi)
	}
}

// TestHandleGetOrgBillingRevenue_DegradedReturnsZeroFilled asserts the
// revenue handler degrades to a zero-filled 200 (not 5xx) when the bridge
// is unwired so the page renders its full target-state chrome.
func TestHandleGetOrgBillingRevenue_DegradedReturnsZeroFilled(t *testing.T) {
	h := &Handler{}
	r := withSovereignAdminClaims(httptest.NewRequest(http.MethodGet, "/api/v1/org/billing/revenue", nil))
	w := httptest.NewRecorder()
	h.HandleGetOrgBillingRevenue(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (degraded)", w.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("response not a JSON object: %v", err)
	}
	for _, k := range []string{"kpi", "sparkline", "breakdown"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing top-level key %q: %s", k, w.Body.String())
		}
	}
	// sparkline + breakdown MUST be `[]` not `null`.
	if string(raw["sparkline"]) == "null" {
		t.Errorf("sparkline serialised as null; FE Array.isArray guard fails")
	}
	if string(raw["breakdown"]) == "null" {
		t.Errorf("breakdown serialised as null; FE Array.isArray guard fails")
	}
}

// TestHandleGetOrgBillingRevenue_ComposesFromLiveOrders pins the full
// live wire: orders upstream feeds the sparkline + breakdown, and the
// revenue-summary upstream is reached without blanking the page.
func TestHandleGetOrgBillingRevenue_ComposesFromLiveOrders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/billing/admin/orders":
			_, _ = w.Write([]byte(`[
				{"id":"o1","tenant_id":"acme","plan_id":"growth","amount_baisa":12000,"status":"paid","created_at":"2026-06-24T09:00:00Z"}
			]`))
		case "/api/billing/admin/revenue":
			_, _ = w.Write([]byte(`{"total_mrr":12,"total_mrr_baisa":12000,"total_customers":1,"new_this_month":1,"active_subscriptions":1}`))
		default:
			t.Errorf("unexpected upstream path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	t.Setenv(orgGatewayURLEnv, upstream.URL)

	h := &Handler{orgJWTSecret: orgBillingTestSecret}
	r := withSovereignAdminClaims(httptest.NewRequest(http.MethodGet, "/api/v1/org/billing/revenue", nil))
	w := httptest.NewRecorder()
	h.HandleGetOrgBillingRevenue(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp orgRevenueResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if len(resp.Breakdown) != 1 || resp.Breakdown[0].Plan != "growth" {
		t.Fatalf("breakdown = %+v, want one 'growth' row", resp.Breakdown)
	}
	if resp.Breakdown[0].MrrCents != 12000 {
		t.Errorf("growth MRR = %d, want 12000", resp.Breakdown[0].MrrCents)
	}
	if resp.Kpi.TopPlan != "growth" || resp.Kpi.TopTenant != "acme" {
		t.Errorf("kpi top = %q/%q, want growth/acme", resp.Kpi.TopPlan, resp.Kpi.TopTenant)
	}
}
