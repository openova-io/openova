// Package handler — org_orders.go: read-only stub for the BSS Orders
// page (Wave 6 PR 3).
//
// Replaces the iframe-wrapped legacy /bss/orders surface with a native
// React table. The FE (OrdersPage.tsx → bss.api.ts getOrders()) hits
// GET /api/v1/org/orders and tolerates a 200 with `{ orders: [] }` by
// rendering its full empty-state chrome — same waterfall posture as
// BssLandingPage's getBssOverview() fallback (INVIOLABLE-PRINCIPLES.md
// #1: first paint is the full target surface).
//
// This stub returns 200 with an empty list so the FE can ship today
// without the page being "API pending" forever. The real implementation
// will project per-tenant orders from the marketplace/billing service
// once that wire is plumbed; until then an empty list is the truthful
// answer (no marketplace orders have been placed on a fresh Sovereign).
package handler

import (
	"net/http"
)

// orgOrder mirrors the FE Order shape in bss.api.ts so a future
// non-empty payload type-aligns without any FE change. Lower-case JSON
// tags match the FE's `r.id`, `r.tenantOrg`, etc. parsing.
type orgOrder struct {
	ID         string `json:"id"`
	TenantOrg  string `json:"tenantOrg"`
	Product    string `json:"product"`
	Status     string `json:"status"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
	TotalCents int64  `json:"totalCents"`
	Currency   string `json:"currency"`
}

type orgOrdersResponse struct {
	Orders []orgOrder `json:"orders"`
}

// HandleListOrgOrders — GET /api/v1/org/orders.
//
// Returns the empty list today. When the marketplace/billing wire is
// plumbed this handler will join the per-tenant order ledger and
// project a denormalised row per order; the FE table renders the same
// shape with no change required.
func (h *Handler) HandleListOrgOrders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, orgOrdersResponse{Orders: []orgOrder{}})
}
