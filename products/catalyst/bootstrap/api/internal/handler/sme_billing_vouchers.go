// Package handler — sme_billing_vouchers.go: catalyst-api proxy for the
// BSS Vouchers surface (Wave 6 PR 5, follow-up to PR #1609 which shipped
// the FE — listVouchers/issueVoucher/revokeVoucher in
// products/catalyst/bootstrap/ui/src/lib/bss.api.ts — without a backend
// wire, so /bss/vouchers could only render the empty target-state chrome
// and every voucher action 404'd at the catalyst-api ingress).
//
// REST surface (registered by main.go inside the RequireSession group,
// mirroring sme_billing_revenue.go):
//
//	GET    /api/v1/sme/billing/vouchers/list           — list live vouchers
//	POST   /api/v1/sme/billing/vouchers/issue          — upsert (resurrects soft-deleted)
//	POST   /api/v1/sme/billing/vouchers/revoke/{code}  — soft-delete via POST (task spec)
//	DELETE /api/v1/sme/billing/vouchers/revoke/{code}  — soft-delete via DELETE (FE wire — see bss.api.ts revokeVoucher)
//
// The FE bss.api.ts uses DELETE for revoke; the task spec calls out POST.
// Both verbs are registered so either client works and the upstream
// receives a verbatim DELETE (the SME billing service registers
// `DELETE /billing/vouchers/revoke/{code}` — see
// core/services/billing/handlers/routes.go:42 and the same file's
// vouchers.go for the canonical handler set: IssueVoucher / ListVouchers /
// RevokeVoucher, all gated by requireVoucherIssuer which permits
// superadmin OR sovereign-admin per docs/FRANCHISE-MODEL.md §3.
//
// ── Upstream ─────────────────────────────────────────────────────────
//
// Upstream is the SME gateway (core/services/gateway/main.go) at
// http://gateway.sme.svc.cluster.local:8080. The gateway strips `/api`
// and forwards to the in-cluster billing service. Override via
// CATALYST_SME_GATEWAY_URL (docs/INVIOLABLE-PRINCIPLES.md #4 — never
// hardcode; the default is the chart's documented Service DNS, every
// other reference is env-driven).
//
// Mirrors the smeCatalogClient pattern in sme_catalog_client.go: small
// per-process singleton, short HTTP timeout, graceful 503 when the SME
// services tier isn't deployed (DNS NXDOMAIN → "service unavailable"
// rather than a 5xx surfaced to the operator). Per
// docs/INVIOLABLE-PRINCIPLES.md #10 the Authorization header is
// forwarded but never logged.
//
// ── Auth seam ────────────────────────────────────────────────────────
//
// catalyst-api's RequireSession middleware (the same gate every other
// /api/v1/sme/* route uses — see main.go's sme/users + sme/tenants +
// sme/orders + sme/billing/revenue registrations) ensures only a valid
// console session reaches these handlers. The Authorization header from
// the request is forwarded to the SME gateway so the gateway's
// HS256-JWT validator (core/services/gateway/proxy.go:117) can apply its
// own role check. When catalyst-api and the SME gateway don't share the
// same JWT_SECRET (today's reality on a fresh Sovereign — the chart
// doesn't wire SME's JWT_SECRET into catalyst-api), the gateway will
// surface 401 verbatim and the FE renders that error inline (its
// `listVouchers` THROWS on non-2xx by design — see
// products/catalyst/bootstrap/ui/src/lib/bss.api.ts:323). Wiring a
// shared secret or a mint-on-the-fly RS256→HS256 bridge is tracked as a
// chart-level follow-up; this PR ships the proxy so the FE has a
// reachable backend the moment that wire lands.
package handler

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	// defaultSMEGatewayURL — the in-cluster Service DNS the chart's
	// templates/sme-services/gateway.yaml exposes on port 8080.
	defaultSMEGatewayURL = "http://gateway.sme.svc.cluster.local:8080"

	// smeGatewayURLEnv — runtime override per
	// docs/INVIOLABLE-PRINCIPLES.md #4.
	smeGatewayURLEnv = "CATALYST_SME_GATEWAY_URL"

	// smeVouchersBudget — short budget so a wedged upstream never wedges
	// the console. Mirrors smeCatalogProbeBudget but a touch longer
	// because the gateway adds a JWT-verify hop.
	smeVouchersBudget = 3 * time.Second
)

// smeVouchersClient builds and reuses one http.Client for every voucher
// hop. The Transport is the package default — no custom TLS, the
// upstream is an in-cluster http:// Service.
var smeVouchersClient = &http.Client{Timeout: smeVouchersBudget}

// smeGatewayURL returns the configured SME gateway base URL, trimmed
// of trailing slash. Picked up once per call rather than memoised so
// tests can flip the env without restarting the binary.
func smeGatewayURL() string {
	base := strings.TrimSpace(os.Getenv(smeGatewayURLEnv))
	if base == "" {
		base = defaultSMEGatewayURL
	}
	return strings.TrimRight(base, "/")
}

// proxySMEVoucher is the shared core for every voucher hop. It rebuilds
// the upstream URL, copies the inbound Authorization header, streams the
// body in both directions, and surfaces the upstream status verbatim so
// the FE sees the registrar's real status (mirrors smeCatalog.SetPublished
// which preserves the upstream 404 / 5xx).
//
// upstreamPath must include the leading `/api/billing/vouchers/...`
// segment — the gateway strips the `/api` prefix and forwards to the
// billing service (core/services/gateway/main.go:78). Method is the
// verb to forward; for the revoke endpoint the catalyst-api accepts
// both POST (task spec) and DELETE (FE wire) but always forwards as
// DELETE so the billing service's
// `DELETE /billing/vouchers/revoke/{code}` route matches.
func (h *Handler) proxySMEVoucher(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	upstreamPath string,
) {
	ctx, cancel := context.WithTimeout(r.Context(), smeVouchersBudget)
	defer cancel()

	upstreamURL := smeGatewayURL() + upstreamPath
	req, err := http.NewRequestWithContext(ctx, method, upstreamURL, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "sme-vouchers-build-request",
			"detail": err.Error(),
		})
		return
	}
	// Forward the caller's Authorization header (Bearer ...) verbatim.
	// The SME gateway's JWT validator (proxy.go:118) will accept or
	// reject based on its own HS256 secret. We do NOT log the value
	// (INVIOLABLE-PRINCIPLES.md #10).
	if a := r.Header.Get("Authorization"); a != "" {
		req.Header.Set("Authorization", a)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := smeVouchersClient.Do(req)
	if err != nil {
		// Common case on a Sovereign where the SME services tier isn't
		// installed (marketplace.enabled=false): DNS NXDOMAIN. Surface
		// 503 so the FE renders an "unavailable" message rather than
		// the FE-side network error.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sme-gateway-unreachable",
			"detail": err.Error(),
		})
		return
	}
	defer resp.Body.Close()

	// Stream the upstream body + status code through verbatim. The FE's
	// listVouchers / issueVoucher / revokeVoucher all parse the body
	// shape directly (see bss.api.ts:265-383), so a verbatim copy keeps
	// the wire contract symmetrical.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// HandleListSMEBillingVouchers — GET /api/v1/sme/billing/vouchers/list.
//
// Forwards to `GET /api/billing/vouchers/list` on the SME gateway,
// which strips `/api` and proxies to the billing service's
// `GET /billing/vouchers/list` (core/services/billing/handlers/vouchers.go
// `ListVouchers`). Returns the array of live vouchers verbatim.
func (h *Handler) HandleListSMEBillingVouchers(w http.ResponseWriter, r *http.Request) {
	h.proxySMEVoucher(w, r, http.MethodGet, "/api/billing/vouchers/list")
}

// HandleIssueSMEBillingVoucher — POST /api/v1/sme/billing/vouchers/issue.
//
// Forwards to `POST /api/billing/vouchers/issue` on the SME gateway.
// The body is streamed through unchanged — see
// core/services/billing/handlers/vouchers.go `IssueVoucher` for the
// upsert + D28 voucher-issued email semantics (recipient_email is an
// optional request-only field; the row never persists it).
func (h *Handler) HandleIssueSMEBillingVoucher(w http.ResponseWriter, r *http.Request) {
	h.proxySMEVoucher(w, r, http.MethodPost, "/api/billing/vouchers/issue")
}

// HandleRevokeSMEBillingVoucher — POST or DELETE
// /api/v1/sme/billing/vouchers/revoke/{code}.
//
// The FE bss.api.ts revokeVoucher uses DELETE; the task spec calls out
// POST. Both verbs route here. The upstream call is always DELETE so
// the billing service's
// `DELETE /billing/vouchers/revoke/{code}` route matches (see
// core/services/billing/handlers/routes.go:42).
//
// The handler URL-escapes the {code} path param because the upstream
// expects the literal voucher code (uppercased server-side on save —
// see vouchers.go:91 `strings.ToUpper(strings.TrimSpace(p.Code))`).
func (h *Handler) HandleRevokeSMEBillingVoucher(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(chi.URLParam(r, "code"))
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing-code",
		})
		return
	}
	// url.PathEscape so a code with a stray slash or space cannot punch
	// through to an unrelated upstream path. The billing service
	// uppercases internally — pass-through preserves whatever the
	// operator typed.
	h.proxySMEVoucher(w, r, http.MethodDelete,
		"/api/billing/vouchers/revoke/"+url.PathEscape(code))
}
