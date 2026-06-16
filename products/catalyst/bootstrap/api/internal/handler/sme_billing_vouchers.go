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
// http://gateway.org-services.svc.cluster.local:8080. The gateway strips `/api`
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
// console session reaches these handlers. The session JWT is RS256
// (Keycloak-issued); the SME gateway (core/services/gateway/proxy.go)
// only accepts HS256 signed with `sme-secrets/JWT_SECRET`. Forwarding
// the RS256 header verbatim therefore 401s upstream.
//
// Bridge (this file, follow-up to PR #1625):
//   1. RequireSession installs *auth.Claims on the request context.
//   2. proxySMEVoucher pulls the operator's identity (sub / email)
//      + Keycloak realm-roles + tier from those claims.
//   3. authpkg.SMERoleFor maps the Keycloak shape onto the SME
//      role vocabulary (superadmin / sovereign-admin / member) the
//      billing service's requireVoucherIssuer expects.
//   4. authpkg.MintSMEAccessToken signs a fresh 5-minute HS256
//      token with h.smeJWTSecret (mirrored from `sme-secrets`
//      into catalyst-system by emberstack/reflector — see the
//      annotation block on products/catalyst/chart/templates/
//      org-services/org-services-secrets.yaml).
//   5. The bridged token is forwarded as `Authorization: Bearer …`
//      on the upstream hop. Per docs/INVIOLABLE-PRINCIPLES.md #10
//      the token is NEVER logged.
//
// When the bridge is unwired (Sovereign without marketplace, or
// stale chart that predates the reflector annotation), the proxy
// returns 503 `sme-jwt-bridge-unwired` so the FE renders an
// actionable message rather than the silent 401 the pre-bridge
// state produced.
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

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	authpkg "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

const (
	// defaultSMEGatewayURL — the in-cluster Service DNS the chart's
	// templates/org-services/gateway.yaml exposes on port 8080.
	defaultSMEGatewayURL = "http://gateway.org-services.svc.cluster.local:8080"

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
// the upstream URL, mints a fresh HS256 bridge token from the operator's
// already-validated RS256 session (see Auth seam in the package doc),
// streams the body in both directions, and surfaces the upstream status
// verbatim so the FE sees the registrar's real status (mirrors
// smeCatalog.SetPublished which preserves the upstream 404 / 5xx).
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
	// Mint the HS256 bridge token BEFORE building the upstream
	// request so an unwired bridge surfaces 503 with no upstream
	// round-trip (avoids the silent-401 pre-bridge state).
	bearer, status, errResp := h.mintSMEBridgeToken(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

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
	// Forward the bridged HS256 token (NOT the operator's RS256
	// session header — the SME gateway rejects RS256 outright). Per
	// docs/INVIOLABLE-PRINCIPLES.md #10 the token is NEVER logged.
	req.Header.Set("Authorization", "Bearer "+bearer)
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

// mintSMEBridgeToken extracts the operator's Keycloak-derived
// identity from the request context (installed by auth.RequireSession
// middleware) and mints a fresh HS256 token the SME gateway will
// accept. See the Auth seam comment at the top of this file for the
// full bridge rationale.
//
// Returns the compact JWT (no "Bearer " prefix) on success, or a
// (status, errResp) pair the caller writes verbatim:
//
//   - 401 `unauthenticated` — middleware bypassed (test harness) or
//     stale request with no claims attached.
//   - 503 `sme-jwt-bridge-unwired` — the chart hasn't seeded
//     CATALYST_SME_JWT_SECRET on this Pod yet (Sovereign without
//     marketplace, or stale chart predating the reflector annotation
//     on sme-secrets). Surfacing 503 lets the FE render an actionable
//     "marketplace not enabled" message rather than the silent 401
//     the pre-bridge state produced.
//   - 500 `sme-jwt-mint-failed` — should never happen in production
//     (jwt-go HS256 sign has no fail paths beyond a malformed key);
//     captured here so the cause surfaces in /api/v1/sme/* error logs
//     rather than as a generic Go panic.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the minted token is NEVER
// logged — only the operator's email and the mapped role are.
func (h *Handler) mintSMEBridgeToken(r *http.Request) (string, int, map[string]string) {
	if len(h.smeJWTSecret) == 0 {
		return "", http.StatusServiceUnavailable, map[string]string{
			"error":  "sme-jwt-bridge-unwired",
			"detail": "CATALYST_SME_JWT_SECRET is not set on this catalyst-api Pod; the chart's sme-secrets Secret may not be reflected into catalyst-system yet",
		}
	}
	claims := authpkg.ClaimsFromContext(r.Context())
	if claims == nil {
		return "", http.StatusUnauthorized, map[string]string{
			"error": "unauthenticated",
		}
	}
	role := sharedauth.SMERoleFor(claims.RealmAccess.Roles, claims.Tier)
	tok, err := sharedauth.MintSMEAccessToken(
		h.smeJWTSecret,
		claims.Sub,
		claims.Email,
		role,
	)
	if err != nil {
		h.log.Warn("sme bridge mint failed",
			"email", claims.Email,
			"role", role,
			"err", err,
		)
		return "", http.StatusInternalServerError, map[string]string{
			"error":  "sme-jwt-mint-failed",
			"detail": err.Error(),
		}
	}
	return tok, 0, nil
}
