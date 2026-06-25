// Package handler — org_orders.go: catalyst-api bridge for the BSS
// Orders page (Wave 6 PR 3; live-data wire — issue #4274).
//
// Replaces the iframe-wrapped legacy /bss/orders surface with a native
// React table. The FE (OrdersPage.tsx → bss.api.ts getOrders()) hits
// GET /api/v1/org/orders and renders one row per order (id / organization
// / product / status / created / updated / total).
//
// ── History ──────────────────────────────────────────────────────────
//
// PR #4196 shipped this handler as a hardcoded empty stub
// (`{ orders: [] }` regardless of state), so /billing/orders rendered a
// permanently-empty table even when the Sovereign's billing service had
// real orders. Issue #4274 wires it to the live billing service so the
// page shows granular per-order rows.
//
// ── Wire ─────────────────────────────────────────────────────────────
//
// Mirrors org_billing_vouchers.go exactly: mint a fresh HS256 bridge
// token from the operator's already-validated RS256 session, forward to
// the Organization gateway, which strips `/api` and proxies to the
// billing service. The billing endpoint is `GET /billing/admin/orders`
// (core/services/billing/handlers/handlers.go AdminOrders →
// store.ListRecentOrders), gated by requireVoucherIssuer (superadmin OR
// sovereign-admin) per #4274 so a franchised Sovereign's sovereign-admin
// operator can read the rollup (previously superadmin-only → 403).
//
// The billing service's Order shape (store.Order) differs from the FE
// Order shape (bss.api.ts), so this handler MAPS the upstream payload
// into the FE shape rather than streaming it verbatim. When the upstream
// is unreachable (Sovereign without marketplace) the handler returns
// 200 with an empty list so the FE renders its branded empty state
// rather than an error (INVIOLABLE-PRINCIPLES.md #1 — first paint is the
// full target surface).
package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// orgOrder mirrors the FE Order shape in bss.api.ts so the payload
// type-aligns without any FE change. Lower-case JSON tags match the FE's
// `r.id`, `r.tenantOrg`, etc. parsing.
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

// billingOrder is the subset of the billing service's store.Order JSON we
// consume. The billing service emits OMR + baisa amounts; we map baisa →
// "cents" (the FE's generic minor-unit field). Apps/Addons are raw JSON
// arrays we collapse into a human product label.
type billingOrder struct {
	ID          string          `json:"id"`
	TenantID    string          `json:"tenant_id"`
	PlanID      string          `json:"plan_id"`
	Apps        json.RawMessage `json:"apps"`
	Addons      json.RawMessage `json:"addons"`
	AmountBaisa int64           `json:"amount_baisa"`
	AmountOMR   int             `json:"amount_omr"`
	Status      string          `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	PromoCode   string          `json:"promo_code"`
}

// HandleListOrgOrders — GET /api/v1/org/orders.
//
// Bridges to the billing service's GET /billing/admin/orders and maps
// each store.Order into the FE Order shape. On any upstream failure
// (gateway unreachable, non-2xx, bad JSON) it returns 200 with an empty
// list so the page degrades to its branded empty state. The "pendingApi"
// flag the FE derives from the HTTP status stays false on this 200, so
// the operator sees "No orders yet" rather than the amber "API pending"
// pill on a real (empty) Sovereign.
func (h *Handler) HandleListOrgOrders(w http.ResponseWriter, r *http.Request) {
	upstream, ok := h.fetchOrgBilling(r, "/api/billing/admin/orders")
	if !ok {
		writeJSON(w, http.StatusOK, orgOrdersResponse{Orders: []orgOrder{}})
		return
	}

	var raw []billingOrder
	if err := json.Unmarshal(upstream, &raw); err != nil {
		// Upstream emitted an unexpected shape (e.g. an error object) —
		// treat as empty so the page never blows up on a malformed row.
		writeJSON(w, http.StatusOK, orgOrdersResponse{Orders: []orgOrder{}})
		return
	}

	orders := make([]orgOrder, 0, len(raw))
	for _, o := range raw {
		orders = append(orders, orgOrder{
			ID:         o.ID,
			TenantOrg:  o.TenantID,
			Product:    orderProductLabel(o),
			Status:     normalizeOrderStatusBE(o.Status),
			CreatedAt:  rfc3339OrEmpty(o.CreatedAt),
			UpdatedAt:  rfc3339OrEmpty(o.CreatedAt),
			TotalCents: orderTotalCents(o),
			Currency:   "OMR",
		})
	}
	writeJSON(w, http.StatusOK, orgOrdersResponse{Orders: orders})
}

// orderTotalCents derives the FE "totalCents" minor-unit field from the
// upstream order. Prefer the canonical baisa value (1/1000 OMR); fall
// back to amount_omr * 1000 for legacy rows that predate the baisa
// column (store.OMRToBaisa convention).
func orderTotalCents(o billingOrder) int64 {
	if o.AmountBaisa > 0 {
		return o.AmountBaisa
	}
	return int64(o.AmountOMR) * 1000
}

// orderProductLabel composes a human product label from the order's plan
// + any apps/addons. The billing store keeps apps/addons as raw JSON
// string arrays; we surface the plan id with an "+N add-on" suffix so the
// operator sees what was bought without a per-row drill-in.
func orderProductLabel(o billingOrder) string {
	plan := o.PlanID
	extras := jsonArrayLen(o.Apps) + jsonArrayLen(o.Addons)
	if plan == "" {
		if extras > 0 {
			return pluralExtras(extras)
		}
		return ""
	}
	if extras > 0 {
		return plan + " + " + pluralExtras(extras)
	}
	return plan
}

func pluralExtras(n int) string {
	if n == 1 {
		return "1 add-on"
	}
	return itoa(n) + " add-ons"
}

// jsonArrayLen counts elements in a raw JSON array, tolerating null /
// empty / non-array values by returning 0.
func jsonArrayLen(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return 0
	}
	return len(arr)
}

// normalizeOrderStatusBE collapses the billing service's order status
// vocabulary onto the four buckets the FE OrdersPage renders
// (pending / completed / failed / cancelled). The billing store uses
// "paid"/"settled"/"complete" for finished orders; map those to
// "completed" so the green pill renders.
func normalizeOrderStatusBE(s string) string {
	switch s {
	case "completed", "complete", "paid", "settled", "succeeded":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled", "void", "refunded":
		return "cancelled"
	case "pending", "processing", "open", "":
		return "pending"
	default:
		return "pending"
	}
}

// fetchOrgBilling mints the HS256 bridge token, forwards a GET to the
// Organization gateway at upstreamPath, and returns the raw upstream body
// on a 2xx. Returns (nil, false) on any failure (unwired bridge, gateway
// unreachable, non-2xx) so read-only callers degrade to an empty payload
// rather than surfacing a 5xx to the operator. Shares the bridge-mint +
// gateway-URL plumbing with proxyOrgVoucher (org_billing_vouchers.go).
func (h *Handler) fetchOrgBilling(r *http.Request, upstreamPath string) ([]byte, bool) {
	bearer, _, errResp := h.mintOrgBridgeToken(r)
	if errResp != nil {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(r.Context(), orgVouchersBudget)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orgGatewayURL()+upstreamPath, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "application/json")

	resp, err := orgVouchersClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	return body, true
}

// rfc3339OrEmpty renders a timestamp as RFC3339 for the FE's
// `new Date(iso)` parse, returning "" for the zero time so the FE renders
// an em-dash rather than "0001-01-01".
func rfc3339OrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
