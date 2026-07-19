// Package handler — org_billing_vouchers.go: catalyst-api proxy for the
// BSS Vouchers surface (Wave 6 PR 5, follow-up to PR #1609 which shipped
// the FE — listVouchers/issueVoucher/revokeVoucher in
// products/catalyst/bootstrap/ui/src/lib/bss.api.ts — without a backend
// wire, so /bss/vouchers could only render the empty target-state chrome
// and every voucher action 404'd at the catalyst-api ingress).
//
// REST surface (registered by main.go inside the RequireSession group,
// mirroring org_billing_revenue.go):
//
//	GET    /api/v1/org/billing/vouchers/list           — list live vouchers
//	POST   /api/v1/org/billing/vouchers/issue          — upsert (resurrects soft-deleted)
//	POST   /api/v1/org/billing/vouchers/revoke/{code}  — soft-delete via POST (task spec)
//	DELETE /api/v1/org/billing/vouchers/revoke/{code}  — soft-delete via DELETE (FE wire — see bss.api.ts revokeVoucher)
//
// The FE bss.api.ts uses DELETE for revoke; the task spec calls out POST.
// Both verbs are registered so either client works and the upstream
// receives a verbatim DELETE (the Organization billing service registers
// `DELETE /billing/vouchers/revoke/{code}` — see
// core/services/billing/handlers/routes.go:42 and the same file's
// vouchers.go for the canonical handler set: IssueVoucher / ListVouchers /
// RevokeVoucher, all gated by requireVoucherIssuer which permits
// superadmin OR sovereign-admin per docs/FRANCHISE-MODEL.md §3.
//
// ── Upstream ─────────────────────────────────────────────────────────
//
// Upstream is the Organization gateway (core/services/gateway/main.go) at
// http://gateway.org-services.svc.cluster.local:8080. The gateway strips `/api`
// and forwards to the in-cluster billing service. Override via
// CATALYST_ORG_GATEWAY_URL (docs/INVIOLABLE-PRINCIPLES.md #4 — never
// hardcode; the default is the chart's documented Service DNS, every
// other reference is env-driven).
//
// Mirrors the orgCatalogClient pattern in org_catalog_client.go: small
// per-process singleton, short HTTP timeout, graceful 503 when the Organization
// services tier isn't deployed (DNS NXDOMAIN → "service unavailable"
// rather than a 5xx surfaced to the operator). Per
// docs/INVIOLABLE-PRINCIPLES.md #10 the Authorization header is
// forwarded but never logged.
//
// ── Auth seam ────────────────────────────────────────────────────────
//
// catalyst-api's RequireSession middleware (the same gate every other
// /api/v1/org/* route uses — see main.go's org/users + org/tenants +
// org/orders + org/billing/revenue registrations) ensures only a valid
// console session reaches these handlers. The session JWT is RS256
// (Keycloak-issued); the Organization gateway (core/services/gateway/proxy.go)
// only accepts HS256 signed with `org-services-secrets/JWT_SECRET`. Forwarding
// the RS256 header verbatim therefore 401s upstream.
//
// Bridge (this file, follow-up to PR #1625):
//   1. RequireSession installs *auth.Claims on the request context.
//   2. proxyOrgVoucher pulls the operator's identity (sub / email)
//      + Keycloak realm-roles + tier from those claims.
//   3. authpkg.OrgRoleFor maps the Keycloak shape onto the Organization
//      role vocabulary (superadmin / sovereign-admin / member) the
//      billing service's requireVoucherIssuer expects.
//   4. authpkg.MintOrgAccessToken signs a fresh 5-minute HS256
//      token with h.orgJWTSecret (mirrored from `org-services-secrets`
//      into catalyst-system by emberstack/reflector — see the
//      annotation block on products/catalyst/chart/templates/
//      org-services/org-services-secrets.yaml).
//   5. The bridged token is forwarded as `Authorization: Bearer …`
//      on the upstream hop. Per docs/INVIOLABLE-PRINCIPLES.md #10
//      the token is NEVER logged.
//
// When the bridge is unwired (Sovereign without marketplace, or
// stale chart that predates the reflector annotation), the proxy
// returns 503 `org-jwt-bridge-unwired` so the FE renders an
// actionable message rather than the silent 401 the pre-bridge
// state produced.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	"github.com/openova-io/openova/core/services/shared/voucher"
	authpkg "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// maxVoucherIssueBody bounds how much of the issue request body the edge
// strength gate buffers before proxying. A voucher-issue payload is a
// handful of small JSON fields; 1 MiB is generous headroom that still
// caps a hostile oversized body.
const maxVoucherIssueBody = 1 << 20

const (
	// defaultOrgGatewayURL — the in-cluster Service DNS the chart's
	// templates/org-services/gateway.yaml exposes on port 8080.
	defaultOrgGatewayURL = "http://gateway.org-services.svc.cluster.local:8080"

	// orgGatewayURLEnv — runtime override per
	// docs/INVIOLABLE-PRINCIPLES.md #4.
	orgGatewayURLEnv = "CATALYST_ORG_GATEWAY_URL"

	// orgVouchersBudget — short budget so a wedged upstream never wedges
	// the console. Mirrors orgCatalogProbeBudget but a touch longer
	// because the gateway adds a JWT-verify hop.
	orgVouchersBudget = 3 * time.Second
)

// orgVouchersClient builds and reuses one http.Client for every voucher
// hop. The Transport is the package default — no custom TLS, the
// upstream is an in-cluster http:// Service.
var orgVouchersClient = &http.Client{Timeout: orgVouchersBudget}

// orgGatewayURL returns the configured Organization gateway base URL, trimmed
// of trailing slash. Picked up once per call rather than memoised so
// tests can flip the env without restarting the binary.
func orgGatewayURL() string {
	base := strings.TrimSpace(os.Getenv(orgGatewayURLEnv))
	if base == "" {
		base = defaultOrgGatewayURL
	}
	return strings.TrimRight(base, "/")
}

// proxyOrgVoucher is the shared core for every voucher hop. It rebuilds
// the upstream URL, mints a fresh HS256 bridge token from the operator's
// already-validated RS256 session (see Auth seam in the package doc),
// streams the body in both directions, and surfaces the upstream status
// verbatim so the FE sees the registrar's real status (mirrors
// orgCatalog.SetPublished which preserves the upstream 404 / 5xx).
//
// upstreamPath must include the leading `/api/billing/vouchers/...`
// segment — the gateway strips the `/api` prefix and forwards to the
// billing service (core/services/gateway/main.go:78). Method is the
// verb to forward; for the revoke endpoint the catalyst-api accepts
// both POST (task spec) and DELETE (FE wire) but always forwards as
// DELETE so the billing service's
// `DELETE /billing/vouchers/revoke/{code}` route matches.
func (h *Handler) proxyOrgVoucher(
	w http.ResponseWriter,
	r *http.Request,
	method string,
	upstreamPath string,
) {
	// Mint the HS256 bridge token BEFORE building the upstream
	// request so an unwired bridge surfaces 503 with no upstream
	// round-trip (avoids the silent-401 pre-bridge state).
	bearer, status, errResp := h.mintOrgBridgeToken(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), orgVouchersBudget)
	defer cancel()

	upstreamURL := orgGatewayURL() + upstreamPath
	req, err := http.NewRequestWithContext(ctx, method, upstreamURL, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "org-vouchers-build-request",
			"detail": err.Error(),
		})
		return
	}
	// Forward the bridged HS256 token (NOT the operator's RS256
	// session header — the Organization gateway rejects RS256 outright). Per
	// docs/INVIOLABLE-PRINCIPLES.md #10 the token is NEVER logged.
	req.Header.Set("Authorization", "Bearer "+bearer)
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := orgVouchersClient.Do(req)
	if err != nil {
		// Common case on a Sovereign where the Organization services tier isn't
		// installed (marketplace.enabled=false): DNS NXDOMAIN. Surface
		// 503 so the FE renders an "unavailable" message rather than
		// the FE-side network error.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "org-gateway-unreachable",
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

// HandleListOrgBillingVouchers — GET /api/v1/org/billing/vouchers/list.
//
// Forwards to `GET /api/billing/vouchers/list` on the Organization gateway,
// which strips `/api` and proxies to the billing service's
// `GET /billing/vouchers/list` (core/services/billing/handlers/vouchers.go
// `ListVouchers`). Returns the array of live vouchers verbatim.
func (h *Handler) HandleListOrgBillingVouchers(w http.ResponseWriter, r *http.Request) {
	h.proxyOrgVoucher(w, r, http.MethodGet, "/api/billing/vouchers/list")
}

// HandleIssueOrgBillingVoucher — POST /api/v1/org/billing/vouchers/issue.
//
// Forwards to `POST /api/billing/vouchers/issue` on the Organization gateway.
// See core/services/billing/handlers/vouchers.go `IssueVoucher` for the
// upsert + D28 voucher-issued email semantics (recipient_email is an
// optional request-only field; the row never persists it).
//
// #4914 — voucher-code strength is enforced at the console edge here, not
// only on the upstream billing service. Previously this handler streamed
// the body straight through, so an operator-supplied weak code (the walk's
// `WALKMART2026`-shape) reached the gateway unchecked — and on a Sovereign
// whose billing image predates #3376 would persist. We now buffer the
// body, inspect the `code` field, and reject a weak CUSTOM code with a 400
// `voucher-code-too-weak` BEFORE the upstream hop, sharing the SAME
// validator the billing service uses (core/services/shared/voucher — one
// implementation, no divergent copies).
//
// An omitted / empty `code` is the auto-generate path (the billing service
// mints a high-entropy `VCH-…` code) and MUST pass through untouched — the
// edge gate only runs when the operator SUPPLIED a code. Any body that is
// absent or not decodable JSON is forwarded verbatim so the upstream owns
// its own shape errors; the only new rejection here is a weak custom code.
func (h *Handler) HandleIssueOrgBillingVoucher(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxVoucherIssueBody))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "voucher-issue-body-read",
			"detail": err.Error(),
		})
		return
	}
	_ = r.Body.Close()

	// Decode leniently — only the `code` field gates the request. A body
	// that isn't valid JSON carries no code to validate; forward it and let
	// the billing service return its own shape error (preserves the proxy's
	// verbatim-passthrough contract for everything but the strength gate).
	var probe struct {
		Code string `json:"code"`
	}
	if len(bytes.TrimSpace(body)) > 0 {
		_ = json.Unmarshal(body, &probe)
	}
	// Normalise exactly as the billing service does (uppercase + trim) so
	// the edge verdict matches the upstream's stored-code semantics. An
	// empty code after normalisation is the auto-generate path — skip.
	if code := strings.ToUpper(strings.TrimSpace(probe.Code)); code != "" {
		if verr := voucher.ValidateCodeStrength(code); verr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "voucher-code-too-weak",
				"detail": verr.Error(),
			})
			return
		}
	}
	// Re-attach the buffered body so proxyOrgVoucher can stream it upstream.
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	h.proxyOrgVoucher(w, r, http.MethodPost, "/api/billing/vouchers/issue")
}

// HandleRevokeOrgBillingVoucher — POST or DELETE
// /api/v1/org/billing/vouchers/revoke/{code}.
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
func (h *Handler) HandleRevokeOrgBillingVoucher(w http.ResponseWriter, r *http.Request) {
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
	h.proxyOrgVoucher(w, r, http.MethodDelete,
		"/api/billing/vouchers/revoke/"+url.PathEscape(code))
}

// mintOrgBridgeToken extracts the operator's Keycloak-derived
// identity from the request context (installed by auth.RequireSession
// middleware) and mints a fresh HS256 token the Organization gateway will
// accept. See the Auth seam comment at the top of this file for the
// full bridge rationale.
//
// Returns the compact JWT (no "Bearer " prefix) on success, or a
// (status, errResp) pair the caller writes verbatim:
//
//   - 401 `unauthenticated` — middleware bypassed (test harness) or
//     stale request with no claims attached.
//   - 503 `org-jwt-bridge-unwired` — the chart hasn't seeded
//     CATALYST_ORG_JWT_SECRET on this Pod yet (Sovereign without
//     marketplace, or stale chart predating the reflector annotation
//     on org-services-secrets). Surfacing 503 lets the FE render an actionable
//     "marketplace not enabled" message rather than the silent 401
//     the pre-bridge state produced.
//   - 500 `org-jwt-mint-failed` — should never happen in production
//     (jwt-go HS256 sign has no fail paths beyond a malformed key);
//     captured here so the cause surfaces in /api/v1/org/* error logs
//     rather than as a generic Go panic.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the minted token is NEVER
// logged — only the operator's email and the mapped role are.
func (h *Handler) mintOrgBridgeToken(r *http.Request) (string, int, map[string]string) {
	if len(h.orgJWTSecret) == 0 {
		return "", http.StatusServiceUnavailable, map[string]string{
			"error":  "org-jwt-bridge-unwired",
			"detail": "CATALYST_ORG_JWT_SECRET is not set on this catalyst-api Pod; the chart's org-services-secrets Secret may not be reflected into catalyst-system yet",
		}
	}
	claims := authpkg.ClaimsFromContext(r.Context())
	if claims == nil {
		return "", http.StatusUnauthorized, map[string]string{
			"error": "unauthenticated",
		}
	}
	role := sharedauth.OrgRoleFor(claims.RealmAccess.Roles, claims.Tier)
	tok, err := sharedauth.MintOrgAccessToken(
		h.orgJWTSecret,
		claims.Sub,
		claims.Email,
		role,
	)
	if err != nil {
		h.log.Warn("org bridge mint failed",
			"email", claims.Email,
			"role", role,
			"err", err,
		)
		return "", http.StatusInternalServerError, map[string]string{
			"error":  "org-jwt-mint-failed",
			"detail": err.Error(),
		}
	}
	return tok, 0, nil
}
