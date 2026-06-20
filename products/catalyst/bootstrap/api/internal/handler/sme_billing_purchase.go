// Package handler — org_billing_purchase.go: catalyst-api proxy for the
// BSS Purchase surface (TBD-C15 / #1750).
//
// Background
// ──────────
// The marketplace's customer-journey Step 15 "Purchase" button
// (core/marketplace/src/components/CheckoutStep.svelte:722) POSTs to
// `/api/billing/checkout` on the marketplace host. That path is the
// canonical purchase wire and has always worked.
//
// The close-audit DoD validator on a Sovereign host
// (e.g. console.t32.omani.works) instead probes
// `/api/v1/billing/purchase` and `/api/v1/sme/billing/purchase`. Both
// returned 404 because the routes were never registered on catalyst-api
// (which serves the Sovereign Console — a different surface from the
// SME marketplace). The 404 produced a confusing "purchase button 502"
// → "purchase route missing" symptom chain (issue #1750 close-audit on
// t32, 2026-05-19).
//
// This file
// ─────────
// Wires `POST /api/v1/billing/purchase` and `POST /api/v1/sme/billing/purchase`
// on catalyst-api to forward to the SME gateway at
// http://gateway.org-services.svc.cluster.local:8080, which strips the `/api`
// prefix and proxies to the billing service's
// `POST /billing/purchase` route (a registered alias for `Checkout` —
// see core/services/billing/handlers/routes.go). The handler is a thin
// proxy in the same shape as org_billing_vouchers.go:
//
//   1. Mint a fresh HS256 bridge token from the operator's already-
//      validated RS256 session (same `mintSMEBridgeToken` helper as the
//      vouchers proxy — the SME gateway rejects RS256 outright).
//   2. Rebuild the upstream URL `/api/billing/purchase` and stream the
//      request body unchanged.
//   3. Stream the upstream status + body back verbatim so the operator
//      sees Stripe's real session URL (or `paid_by_credit: true` when a
//      voucher / credit covers the order in full).
//
// When the SME services tier isn't installed on this Sovereign (DNS
// NXDOMAIN), the handler returns 503 `sme-gateway-unreachable` so the
// FE renders an actionable "marketplace not enabled" message rather
// than a generic network error. This mirrors the vouchers-proxy
// graceful-degradation contract.
//
// Auth seam
// ─────────
// RequireSession is applied by the router group registration in
// cmd/api/main.go — only a valid console session reaches this handler.
// `mintSMEBridgeToken` returns 503 `sme-jwt-bridge-unwired` when
// `CATALYST_ORG_JWT_SECRET` is unset (Sovereign without marketplace,
// or stale chart predating the sme-secrets reflector annotation).
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the minted token is NEVER
// logged — only the operator's email and the mapped role are.
package handler

import (
	"context"
	"io"
	"net/http"
)

// HandleSMEBillingPurchase — POST /api/v1/billing/purchase
//
//	          and POST /api/v1/sme/billing/purchase.
//
// Both routes funnel here. Forwards to `POST /api/billing/purchase` on
// the SME gateway, which proxies to the billing service's
// `POST /billing/purchase` alias (semantic alias for the canonical
// `POST /billing/checkout` handler — same wire contract, same
// promo-code / Stripe-session semantics).
func (h *Handler) HandleSMEBillingPurchase(w http.ResponseWriter, r *http.Request) {
	// Mint the HS256 bridge token BEFORE building the upstream request
	// so an unwired bridge surfaces 503 with no upstream round-trip
	// (avoids the silent-401 pre-bridge state).
	bearer, status, errResp := h.mintSMEBridgeToken(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), smeVouchersBudget)
	defer cancel()

	upstreamURL := smeGatewayURL() + "/api/billing/purchase"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "sme-purchase-build-request",
			"detail": err.Error(),
		})
		return
	}
	// Forward the bridged HS256 token (NOT the operator's RS256 session
	// header — the SME gateway rejects RS256 outright). Per
	// docs/INVIOLABLE-PRINCIPLES.md #10 the token is NEVER logged.
	req.Header.Set("Authorization", "Bearer "+bearer)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")

	// Re-use the shared smeVouchersClient (same Transport/timeout, same
	// in-cluster Service target) so connection pooling is consistent
	// across every catalyst-api → SME-gateway hop.
	resp, err := smeVouchersClient.Do(req)
	if err != nil {
		// Common case on a Sovereign where the SME services tier isn't
		// installed (marketplace.enabled=false): DNS NXDOMAIN. Surface
		// 503 so the FE renders an "unavailable" message rather than
		// a generic network error.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sme-gateway-unreachable",
			"detail": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	// Stream the upstream body + status code through verbatim. The
	// marketplace's /api/billing/checkout response shape is
	// `{order_id, paid_by_credit, session_url}` (see
	// core/services/billing/handlers/handlers.go `Checkout`) — the
	// purchase alias preserves it, so any operator-side tooling parsing
	// the response shape on console.<sov-fqdn> sees the same contract.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
