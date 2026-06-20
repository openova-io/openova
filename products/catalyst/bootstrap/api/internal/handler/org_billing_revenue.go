// Package handler — org_billing_revenue.go: read-only stub for the BSS
// Revenue page (Wave 6 PR 4).
//
// Replaces the iframe-wrapped legacy /bss/revenue surface with a native
// React surface. The FE (RevenuePage.tsx → bss.api.ts getRevenue())
// hits GET /api/v1/org/billing/revenue and tolerates a 200 with a
// zero-filled payload by rendering the full target-state chrome (KPI
// strip + line chart + breakdown table) plus the "API pending" pill —
// same waterfall posture as BssLandingPage's getBssOverview() and
// OrdersPage's getOrders() fallback (INVIOLABLE-PRINCIPLES.md #1:
// first paint is the full target surface).
//
// This stub returns 200 with a zero-filled payload so the FE can ship
// today without the page being a hard 404 forever. The real
// implementation will join the per-tenant billing ledger (MRR per
// plan, daily revenue projection, top tenant / plan) once that wire
// is plumbed; until then zero is the truthful answer (no billings on
// a fresh Sovereign before the marketplace lands).
package handler

import (
	"net/http"
)

// orgRevenueKpi mirrors the FE RevenueKpi shape in bss.api.ts so a
// future non-zero payload type-aligns without any FE change.
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

// HandleGetOrgBillingRevenue — GET /api/v1/org/billing/revenue.
//
// Returns the zero-filled payload today. When the billing/marketplace
// wire is plumbed this handler will project per-tenant billings into a
// daily revenue series + per-plan rollup; the FE renders the same shape
// with no change required.
func (h *Handler) HandleGetOrgBillingRevenue(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, orgRevenueResponse{
		Kpi:       orgRevenueKpi{},
		Sparkline: []int64{},
		Breakdown: []orgPlanBreakdown{},
	})
}
