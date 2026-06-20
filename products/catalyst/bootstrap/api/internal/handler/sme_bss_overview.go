// Package handler — sme_bss_overview.go: read-only stub for the BSS
// landing KPI rollup (Refs #1949, D-BSS / TBD-A58).
//
// Backs the /console/bss landing page (BssLandingPage.tsx) which calls
// getBssOverview() in ui/src/lib/bss.api.ts. Pre-fix the wire path
// /api/v1/sme/bss/overview returned 404 (no handler registered) and the
// FE's try/catch flipped `pendingApi=true`, so every tile rendered the
// honest-but-noisy "API pending" placeholder. After this PR the handler
// returns 200 with a fully-shaped zero payload so the operator sees the
// full target-state surface ("0 revenue / 0 customers" — the truthful
// answer on a fresh Sovereign with no marketplace orders yet) instead
// of the "API pending" pill (INVIOLABLE-PRINCIPLES.md #1 — first paint
// is the full target surface).
//
// Wire contract (mirrors BssOverview in ui/src/lib/bss.api.ts:27-64):
//
//	{
//	  "billing":  { "mrrCents": int64,        "deltaPct": float64|null },
//	  "orders":   { "pending":  int,          "oldestDays": int|null  },
//	  "vouchers": { "active":   int,          "redeemRate": float64|null },
//	  "tenants":  { "active":   int,          "newThisWeek": int      },
//	  "revenue":  { "last30dCents": int64,    "deltaPct": float64|null,
//	                "sparkline": []int64 }
//	}
//
// The FE only sets `pendingApi=true` when the HTTP call fails OR the
// response body is not a JSON object — a zero-filled 200 returns
// `pendingApi=false` and the tiles render real (zero) numbers.
//
// The real implementation will project per-tenant rollups from the
// billing / marketplace / orders ledgers once those wires are plumbed
// (siblings: org_billing_revenue.go, sme_orders.go, org_billing_vouchers.go,
// organization_provisioning.go). Until then zero is the truthful answer.
package handler

import (
	"net/http"
)

// smeBssBillingKpi mirrors the FE BssOverview.billing shape.
type smeBssBillingKpi struct {
	MrrCents int64    `json:"mrrCents"`
	DeltaPct *float64 `json:"deltaPct"`
}

// smeBssOrdersKpi mirrors the FE BssOverview.orders shape.
type smeBssOrdersKpi struct {
	Pending    int  `json:"pending"`
	OldestDays *int `json:"oldestDays"`
}

// smeBssVouchersKpi mirrors the FE BssOverview.vouchers shape.
type smeBssVouchersKpi struct {
	Active     int      `json:"active"`
	RedeemRate *float64 `json:"redeemRate"`
}

// smeBssTenantsKpi mirrors the FE BssOverview.tenants shape.
type smeBssTenantsKpi struct {
	Active      int `json:"active"`
	NewThisWeek int `json:"newThisWeek"`
}

// smeBssRevenueKpi mirrors the FE BssOverview.revenue shape. Sparkline
// is always a non-nil slice so the FE's Array.isArray guard passes;
// empty is a valid signal (no revenue yet).
type smeBssRevenueKpi struct {
	Last30dCents int64    `json:"last30dCents"`
	DeltaPct     *float64 `json:"deltaPct"`
	Sparkline    []int64  `json:"sparkline"`
}

// smeBssOverviewResponse mirrors the FE BssOverview shape end-to-end.
// `pendingApi` is intentionally NOT serialised by the BE — the FE
// derives it from the HTTP status / parse outcome.
type smeBssOverviewResponse struct {
	Billing  smeBssBillingKpi  `json:"billing"`
	Orders   smeBssOrdersKpi   `json:"orders"`
	Vouchers smeBssVouchersKpi `json:"vouchers"`
	Tenants  smeBssTenantsKpi  `json:"tenants"`
	Revenue  smeBssRevenueKpi  `json:"revenue"`
}

// HandleGetSMEBssOverview — GET /api/v1/sme/bss/overview.
//
// Returns the zero-filled payload today. When the marketplace / billing
// / orders wires are plumbed this handler will join the per-tenant
// rollups and project the live KPIs; the FE renders the same shape
// with no change required.
func (h *Handler) HandleGetSMEBssOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, smeBssOverviewResponse{
		Billing:  smeBssBillingKpi{MrrCents: 0, DeltaPct: nil},
		Orders:   smeBssOrdersKpi{Pending: 0, OldestDays: nil},
		Vouchers: smeBssVouchersKpi{Active: 0, RedeemRate: nil},
		Tenants:  smeBssTenantsKpi{Active: 0, NewThisWeek: 0},
		Revenue: smeBssRevenueKpi{
			Last30dCents: 0,
			DeltaPct:     nil,
			Sparkline:    []int64{},
		},
	})
}
