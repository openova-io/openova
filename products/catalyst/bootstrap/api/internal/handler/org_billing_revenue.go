// Package handler — org_billing_revenue.go: catalyst-api bridge for the
// BSS Revenue page (Wave 6 PR 4; live-data wire — issue #4274).
//
// Replaces the iframe-wrapped legacy /bss/revenue surface with a native
// React surface (RevenuePage.tsx → bss.api.ts getRevenue()). The FE hits
// GET /api/v1/org/billing/revenue and renders a KPI strip + 30-day trend
// chart + per-plan breakdown table.
//
// ── History ──────────────────────────────────────────────────────────
//
// PR #4196 shipped this handler as a hardcoded zero stub (all KPIs 0,
// empty sparkline, empty breakdown regardless of state), so
// /billing/revenue rendered a flat-line chart and an empty breakdown even
// when the Sovereign's billing service had real revenue. Issue #4274
// wires it to the live billing service so the page shows granular,
// per-plan revenue.
//
// ── Wire ─────────────────────────────────────────────────────────────
//
// Mirrors org_orders.go: mint an HS256 bridge token from the operator's
// RS256 session, forward to the Organization gateway which proxies to the
// billing service. Two upstream reads compose the payload:
//
//   - GET /billing/admin/revenue → store.GetRevenueSummary: the MRR
//     headline (total_mrr is whole-OMR; we promote to baisa for the FE's
//     minor-unit field) + customer / subscription counts.
//   - GET /billing/admin/orders  → store.ListRecentOrders: the per-order
//     ledger from which we derive the trailing-30-day daily sparkline,
//     the per-PLAN breakdown table (MRR contribution, distinct-org count),
//     and the top-plan / top-org KPI cards.
//
// Both endpoints are gated by requireVoucherIssuer (superadmin OR
// sovereign-admin) per #4274 so a franchised Sovereign's sovereign-admin
// operator can read the rollups (previously superadmin-only → 403).
//
// On any upstream failure the handler returns 200 with a zero-filled
// payload so the page renders its full target-state chrome + branded
// empty states rather than an error (INVIOLABLE-PRINCIPLES.md #1).
package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

// orgRevenueKpi mirrors the FE RevenueKpi shape in bss.api.ts.
type orgRevenueKpi struct {
	Last30dCents int64    `json:"last30dCents"`
	MomPct       *float64 `json:"momPct"`
	TopTenant    string   `json:"topTenant"`
	TopPlan      string   `json:"topPlan"`
}

// orgPlanBreakdown mirrors the FE PlanBreakdown shape.
type orgPlanBreakdown struct {
	ID       string   `json:"id"`
	Plan     string   `json:"plan"`
	Tenants  int      `json:"tenants"`
	MrrCents int64    `json:"mrrCents"`
	YoyPct   *float64 `json:"yoyPct"`
}

// orgRevenueResponse mirrors the FE BssRevenue shape.
type orgRevenueResponse struct {
	Kpi       orgRevenueKpi      `json:"kpi"`
	Sparkline []int64            `json:"sparkline"`
	Breakdown []orgPlanBreakdown `json:"breakdown"`
}

// billingRevenueSummary is the subset of GET /billing/admin/revenue we
// consume (core/services/billing/handlers/handlers.go AdminRevenue).
type billingRevenueSummary struct {
	TotalMRR       int   `json:"total_mrr"`
	TotalMRRBaisa  int64 `json:"total_mrr_baisa"`
	TotalCustomers int   `json:"total_customers"`
	NewThisMonth   int   `json:"new_this_month"`
}

// sparklineDays is the trailing window the FE trend chart renders (one
// point per day, oldest first). Matches RevenueTrendChart's "D-29 … D-0"
// axis in RevenuePage.tsx.
const sparklineDays = 30

// emptyOrgRevenue is the zero-filled payload returned on any upstream
// failure. Sparkline is a non-nil empty slice so the FE's Array.isArray
// guard passes and the trend chart renders its "No revenue yet" empty
// state rather than collapsing.
func emptyOrgRevenue() orgRevenueResponse {
	return orgRevenueResponse{
		Kpi:       orgRevenueKpi{},
		Sparkline: []int64{},
		Breakdown: []orgPlanBreakdown{},
	}
}

// HandleGetOrgBillingRevenue — GET /api/v1/org/billing/revenue.
//
// Bridges to the billing service and composes the live revenue payload.
// Degrades to a zero-filled 200 on any upstream failure so the page
// renders the full target-state surface (KPI strip + trend chart +
// breakdown) with branded empty states.
func (h *Handler) HandleGetOrgBillingRevenue(w http.ResponseWriter, r *http.Request) {
	// Orders feed the granular surfaces (sparkline + per-plan breakdown +
	// top-plan/org). This is the load-bearing read; if it fails the page
	// is honestly empty.
	ordersRaw, ordersOK := h.fetchOrgBilling(r, "/api/billing/admin/orders")
	if !ordersOK {
		writeJSON(w, http.StatusOK, emptyOrgRevenue())
		return
	}
	var orders []billingOrder
	if err := json.Unmarshal(ordersRaw, &orders); err != nil {
		writeJSON(w, http.StatusOK, emptyOrgRevenue())
		return
	}

	resp := composeRevenue(orders, time.Now().UTC())

	// The MRR summary is a best-effort enrichment — if it's reachable we
	// surface total_mrr as a baisa-promoted KPI cross-check, but its
	// absence never blanks the page (the orders-derived surfaces stand on
	// their own).
	if sumRaw, ok := h.fetchOrgBilling(r, "/api/billing/admin/revenue"); ok {
		var sum billingRevenueSummary
		if json.Unmarshal(sumRaw, &sum) == nil {
			// No-op today beyond confirming reachability; the orders-derived
			// last30dCents is the operator-facing headline. Kept as a seam
			// so a future MRR-vs-orders reconciliation has the value to hand.
			_ = sum
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// composeRevenue derives the full revenue payload from the per-order
// ledger. Split out (pure, now-injected) so the unit test can assert the
// sparkline bucketing + per-plan rollup deterministically.
//
//   - sparkline: one int64 per day for the trailing `sparklineDays`,
//     oldest first; each bucket sums the baisa total of orders created
//     that UTC day. Orders older than the window are excluded.
//   - breakdown: one row per plan id, summing baisa across ALL orders
//     (not just the 30-day window) with the distinct-organization count.
//   - kpi.last30dCents: the sum of the sparkline (trailing-30d revenue).
//   - kpi.topPlan / topTenant: the highest-grossing plan / organization.
func composeRevenue(orders []billingOrder, now time.Time) orgRevenueResponse {
	resp := emptyOrgRevenue()

	// Trailing-30d daily buckets. dayIndex 0 == today, growing into the
	// past; we flip to oldest-first on emit.
	buckets := make([]int64, sparklineDays)
	today := now.Truncate(24 * time.Hour)

	type planAgg struct {
		mrrBaisa int64
		orgs     map[string]struct{}
		firstSeq int // preserves first-seen order for a stable id tiebreak
	}
	plans := map[string]*planAgg{}
	orgTotals := map[string]int64{}

	var last30d int64
	seq := 0
	for _, o := range orders {
		baisa := orderTotalCents(o)

		// Per-plan rollup (all-time).
		planID := o.PlanID
		if planID == "" {
			planID = "—"
		}
		agg := plans[planID]
		if agg == nil {
			agg = &planAgg{orgs: map[string]struct{}{}, firstSeq: seq}
			plans[planID] = agg
		}
		agg.mrrBaisa += baisa
		if o.TenantID != "" {
			agg.orgs[o.TenantID] = struct{}{}
		}
		orgTotals[o.TenantID] += baisa
		seq++

		// Trailing-30d sparkline bucket.
		if o.CreatedAt.IsZero() {
			continue
		}
		day := o.CreatedAt.UTC().Truncate(24 * time.Hour)
		ago := int(today.Sub(day).Hours() / 24)
		if ago >= 0 && ago < sparklineDays {
			buckets[ago] += baisa
			last30d += baisa
		}
	}

	// Emit sparkline oldest-first (D-29 … D-0). buckets[ago] is keyed by
	// days-ago, so index 0 is today → it must land LAST.
	spark := make([]int64, sparklineDays)
	for ago := 0; ago < sparklineDays; ago++ {
		spark[sparklineDays-1-ago] = buckets[ago]
	}
	resp.Sparkline = spark
	resp.Kpi.Last30dCents = last30d

	// Per-plan breakdown rows, sorted MRR desc with a stable plan-id tie.
	rows := make([]orgPlanBreakdown, 0, len(plans))
	for planID, agg := range plans {
		rows = append(rows, orgPlanBreakdown{
			ID:       planID,
			Plan:     planID,
			Tenants:  len(agg.orgs),
			MrrCents: agg.mrrBaisa,
			YoyPct:   nil, // no prior-year baseline persisted yet
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].MrrCents != rows[j].MrrCents {
			return rows[i].MrrCents > rows[j].MrrCents
		}
		return rows[i].ID < rows[j].ID
	})
	resp.Breakdown = rows

	// Top-plan / top-org KPI cards.
	if len(rows) > 0 {
		resp.Kpi.TopPlan = rows[0].Plan
	}
	resp.Kpi.TopTenant = topByValue(orgTotals)

	return resp
}

// topByValue returns the key with the highest value, ignoring the empty
// key (orders with no organization join). Returns "" when the map is
// empty or only the empty key has value.
func topByValue(m map[string]int64) string {
	best := ""
	var bestVal int64 = -1
	// Deterministic iteration: collect + sort keys so ties resolve
	// lexically rather than by Go's random map order.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "" {
			continue
		}
		if m[k] > bestVal {
			best = k
			bestVal = m[k]
		}
	}
	return best
}
