// Package handler — ownership.go (issue #689)
//
// Multi-tenancy enforcement for /api/v1/deployments/{id}* endpoints.
//
// The contract: a signed-in operator who is NOT the original creator of a
// deployment MUST receive 404 Not Found, NEVER 403 Forbidden.  The 404
// distinction is deliberate — emitting 403 leaks the existence of someone
// else's deployment via the response code (a network observer can map ids
// to "exists/doesn't-exist" without ever reading any state).  By collapsing
// both "not found" and "found but not yours" into the same 404 response,
// the surface looks identical from the outside regardless of which branch
// fired.
//
// Backwards compatibility: a Deployment loaded from an on-disk record
// persisted before this issue landed has OwnerEmail == "".  The check
// MUST treat empty as "legacy — skip" so a catalyst-api upgrade does not
// lock operators out of their own pre-existing deployments.  We log a
// warning the first time we observe a legacy record on a request so an
// operator can spot the migration window.  Newly created deployments
// always populate OwnerEmail (CreateDeployment reads X-User-Email at
// stamp time).
//
// SSE-stream caveat (GET /events): the helper writes the 404 body BEFORE
// any SSE bytes are written.  Callers MUST invoke checkOwnership before
// flushing any "data:" frame; once the response has been hijacked into
// SSE mode, switching back to a JSON 404 is impossible.

package handler

import (
	"net/http"
	"os"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// checkOwnership returns true when the request's authenticated session
// owns dep.  Returns false AND writes a 404 response when the session
// email differs from dep.OwnerEmail.  Empty dep.OwnerEmail is treated as
// a legacy pre-#689 record — the check is skipped (with a debug log) so
// in-place upgrades don't break existing deployments.
//
// The helper writes a JSON 404 body identical to the "deployment not
// found" body emitted by callers when the id is genuinely unknown, so
// the wire shape never distinguishes the two cases.
//
// When auth.RequireSession is wired (production Catalyst-Zero), the
// session-email header is always populated for an authenticated request;
// requests that bypass the middleware (CI, tests) carry no header and
// the helper treats those as "no session — skip" so test handlers that
// build a Handler{} directly continue to work unchanged.
//
// Caller must NOT have hijacked the ResponseWriter into SSE mode before
// calling this helper.  The 404 path writes JSON via writeJSON.
func (h *Handler) checkOwnership(w http.ResponseWriter, r *http.Request, dep *Deployment) bool {
	dep.mu.Lock()
	owner := dep.OwnerEmail
	dep.mu.Unlock()

	// Legacy records persisted before #689 — log once per request and
	// allow through.  The warning gives an operator visible evidence of
	// the migration window without spamming on every read.
	if strings.TrimSpace(owner) == "" {
		if h.log != nil {
			h.log.Warn("deployment has no OwnerEmail — legacy pre-#689 record, ownership check skipped",
				"id", dep.ID,
			)
		}
		return true
	}

	// Read the session email injected by auth.RequireSession.  Empty
	// means the middleware was not in the chain (CI, tests, or a
	// catalyst-api running without CATALYST_KC_ADDR).  Treat as
	// "passthrough — no session enforcement" so existing tests keep
	// working unchanged; the production routing wires the handler
	// inside RequireSession so this branch only fires off-prod.
	sessionEmail := strings.TrimSpace(r.Header.Get("X-User-Email"))
	if sessionEmail == "" {
		return true
	}

	// Chroot Sovereign co-admin bypass (#4193).
	//
	// `OwnerEmail` is the operator who CREATED the deployment record on the
	// MOTHERSHIP — multi-tenant there, so the email match is the right
	// cross-tenant guard. But on a chrooted Sovereign (SOVEREIGN_FQDN set)
	// there is exactly ONE deployment, and EVERY authenticated
	// sovereign-admin legitimately co-administers it. Gating the
	// per-deployment endpoints (the #3996 reconciler logs/reconcile/
	// suspend/resume drill-in, deployment GET, etc.) on an exact
	// OwnerEmail match wrongly 404s a sovereign-admin who is not the
	// original creator — caught live on omantel.biz 2026-06-24: the
	// `uat215@omani.works` sovereign-admin session got
	// `ownership-check: rejected cross-tenant access` on
	// /deployments/4635277cae4ffed9/reconcilers (owner = the founder who
	// created it), so the recon drill-in logged 404 on every call while
	// the recon graph (which skips this gate) rendered fine.
	//
	// So on a chroot, let a privileged-tier / privileged-realm-role
	// session through. The same tier set the reconciler-mutation RBAC
	// (jobRetryCallerAuthorized) and applicationInstallCallerAuthorized
	// already trust as operator-equivalent — this just moves that trust
	// ahead of the (mothership-shaped) email gate so the request reaches
	// the tier-aware RBAC at all. The mothership path is UNCHANGED
	// (SOVEREIGN_FQDN unset → email match still required).
	if strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")) != "" {
		claims := auth.ClaimsFromContext(r.Context())
		if claims != nil && (applicationInstallCallerAuthorized(claims) || isPrivilegedSovereignTier(claims.Tier)) {
			return true
		}
	}

	if !strings.EqualFold(sessionEmail, owner) {
		// 404, not 403 — never leak the existence of another user's
		// deployment via the response code.  Log the rejection at
		// info-level (NOT warn) so the catalyst-api log distinguishes
		// "expected hostile probe" from "legitimate misconfiguration".
		if h.log != nil {
			h.log.Info("ownership-check: rejected cross-tenant access",
				"id", dep.ID,
				"path", r.URL.Path,
				"method", r.Method,
			)
		}
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": "deployment not found",
		})
		return false
	}

	return true
}

// isPrivilegedSovereignTier reports whether the session tier marks a
// Sovereign co-administrator who may manage the chroot's single
// deployment. The Sovereign session middleware stamps `owner`; PIN /
// OIDC flows may stamp `sovereign-admin` or `admin`; the jobs-retry
// path additionally trusts `operator`. Matched case-insensitively.
func isPrivilegedSovereignTier(tier string) bool {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "owner", "admin", "operator", "sovereign-admin":
		return true
	}
	return false
}
