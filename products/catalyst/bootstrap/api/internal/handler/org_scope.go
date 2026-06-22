// org_scope.go — request-host → Organization scope resolution + the
// authorization guard that confines an Org-scoped session to its OWN
// Organization (issue #4110).
//
// THE BUG this file closes (live on omantel.biz dep 4635277cae4ffed9,
// 2026-06-22): a customer (demo@openova.io) PIN-authenticating on their
// Org pool-domain console (console.demo.omani.homes) was minted the SAME
// sovereign-admin session the operator gets on console.omantel.biz.
// pinSessionAuthority() derived the tier purely from /sovereign-admins
// group membership in the SOVEREIGN realm and NEVER consulted the request
// host, so a session minted on an Org host carried full god-mode:
// provisioning wizard, the Sovereign's own deployment-stream, every Org,
// the whole cloud view, sovereign DNS / parent-domain / BSS settings.
//
// The fix has two halves, both in this file:
//
//  1. resolveOrgScope(r) — resolves the request host against the tenant
//     registry. When the host is a registered tenant_kind=org console
//     (e.g. console.demo.omani.homes → org-demo), the session minted on
//     that host MUST be Org-scoped: a non-sovereign tier + the org slug,
//     NEVER catalyst-admin/sovereign-admin. HandlePinVerify consumes this
//     to stamp the JWT (org + tier) at mint time.
//
//  2. OrgScopeGuard / claimsAreOrgScoped(claims) — the authorization guard.
//     A session whose JWT carries the org-scoped tier is a customer; it is
//     FORBIDDEN (deny-by-default outside orgSafePathPrefixes) from the
//     sovereign-admin + cross-org surfaces (the deployments API, parent-
//     domains, the all-orgs directory, sovereign settings, the whole-cluster
//     estate). Enforced as middleware on the RequireSession group so the API
//     returns 403 even if the SPA is bypassed — defense in depth, not
//     UI-only hiding.
//
// Why the request host is the trust anchor: catalyst-api sits behind the
// Cilium Gateway which sets X-Forwarded-Host to the real browser host. A
// browser on console.demo.omani.homes can only ever send that host (the
// gateway routes the Org console host to this same catalyst-api). The PIN
// itself proves inbox control, but the HOST proves WHICH console door the
// customer walked through — and an Org door must never open onto the
// Sovereign estate.

package handler

import (
	"net/http"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgScopedTier is the tier stamped on an Org-scoped PIN session. It is
// deliberately NOT one of the sovereign tiers (viewer/developer/operator/
// admin/owner consumed by the sovereign-admin RBAC gates): an Org owner is
// the top of THEIR org, but holds zero authority over the Sovereign. The
// console reads tier=org-admin to render the scoped chrome; the API gate
// (requireNotOrgScoped) reads it to 403 sovereign endpoints.
//
// Single source of truth: aliases auth.OrgScopedTier so the middleware's
// owner-stamp guard (auth.stampSovereignOperatorClaim) and this package
// agree on the exact marker string.
const orgScopedTier = auth.OrgScopedTier

// orgScopedRealmRole mirrors orgScopedTier in the realm_access.roles list
// so the SPA's role-chip projection has something to show without ever
// emitting catalyst-admin/catalyst-owner (which would light up the
// sovereign nav). It is intentionally a NON-catalyst role name so no
// existing sovereign-admin gate (which matches catalyst-*/sovereign-*)
// ever accepts it.
const orgScopedRealmRole = "org-admin"

// orgScope is the resolved Org identity for a request host.
type orgScope struct {
	// Org is the Organization slug (e.g. "demo"). Stamped as the JWT `org`
	// claim so downstream Org-scoped handlers (org_users.go etc.) and the
	// console scope to this Org.
	Org string
	// TenantID is the opaque tenant id from the registry.
	TenantID string
	// Host is the resolved (lowercased, port-stripped) request host.
	Host string
}

// resolveOrgScope reports whether the request arrived on a registered
// Organization console host (tenant_kind=org) and, if so, returns the Org
// scope. The second return is false for the Sovereign's own front door
// (console.<sov-fqdn>) or any host the registry does not know — those keep
// the existing sovereign-admin resolution path.
//
// Resolution:
//  1. requestHost(r) — the real browser host via X-Forwarded-Host.
//  2. tenant registry lookup. A tenant_kind=org match → Org scope.
//
// A host that is NOT in the registry, OR is registered as tenant_kind=otech
// (the Sovereign's own console), is NOT Org-scoped.
func (h *Handler) resolveOrgScope(r *http.Request) (orgScope, bool) {
	if h == nil || h.tenantRegistry == nil {
		return orgScope{}, false
	}
	host := requestHost(r)
	if host == "" {
		return orgScope{}, false
	}
	t, ok := h.tenantRegistry.Get(host)
	if !ok || t.TenantKind != store.TenantKindOrg {
		return orgScope{}, false
	}
	// The Org slug is the first label of the console host
	// (console.<slug>.<zone>). Fall back to the tenant id when the host
	// shape is unusual so the claim is never empty.
	slug := orgSlugFromHost(host)
	if slug == "" {
		slug = t.TenantID
	}
	return orgScope{Org: slug, TenantID: t.TenantID, Host: host}, true
}

// orgSlugFromHost extracts the Org slug from a console host. For
// "console.demo.omani.homes" it returns "demo" (the label after the
// leading service label). Returns "" when the host has no recognisable
// service label or too few labels to carry a slug.
func orgSlugFromHost(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	labels := strings.Split(host, ".")
	if len(labels) < 3 {
		return ""
	}
	switch labels[0] {
	case "console", "api", "marketplace", "auth":
		return labels[1]
	}
	return ""
}

// claimsAreOrgScoped reports whether a session is confined to a single
// Organization (a customer session minted on an Org console host). Such a
// session must never reach the Sovereign-admin or other-org surfaces.
//
// The marker is the dedicated tier value (orgScopedTier) carried in the
// JWT. It is a single, unambiguous discriminant that survives the JWT
// round-trip (the generic auth.Claims has no bool-marker field, but Tier
// is a first-class claim already validated end-to-end). An Org-scoped
// session ALWAYS carries tier=org-admin AND a non-empty Org.
func claimsAreOrgScoped(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(claims.Tier), orgScopedTier)
}

// orgSafePathPrefixes is the DENY-BY-DEFAULT allowlist: the ONLY API path
// prefixes an Org-scoped customer session may reach. Everything else under
// the RequireSession group (deployments + deployment-stream, the whole-
// cluster /sovereigns/{id}/k8s estate, BSS/billing/commerce, parent-domains,
// the cross-org organizations directory, sovereign settings) is 403'd.
//
// Deny-by-default is the secure posture for a privilege-escalation fix: a
// new sovereign-admin endpoint added later is locked to customers by
// default and must be EXPLICITLY allowlisted here to reach an Org session —
// the inverse of an allow-by-default block-list, which silently re-leaks on
// every new route. Each entry below is an Org-OWN surface:
//
//   - /api/v1/whoami            — identity + scope (the SPA bootstrap)
//   - /api/v1/auth/             — login/logout/session lifecycle
//   - /api/v1/tenant/discover   — host→tenant bootstrap (public anyway)
//   - /api/v1/org/users         — the Org's OWN users (scoped by X-Tenant-Host)
//   - /api/v1/org/roles         — the Org's OWN role assignments
//   - /api/v1/org/apps          — the Org's OWN application estate
//   - /api/v1/org/applications  — alias of the above
//   - /api/v1/org/self          — the Org's identity card
//   - /api/v1/catalog           — the read-only blueprint catalog (browse)
//   - /api/v1/sandbox/          — the Org's own Sandbox (scoped by sub/org)
//
// NOTE: BSS / billing / commerce / consumption / organizations-directory
// are Sovereign-WIDE operator surfaces despite some carrying an `/org/`
// path prefix — they are intentionally NOT allowlisted.
var orgSafePathPrefixes = []string{
	"/api/v1/whoami",
	"/api/v1/auth/",
	"/api/v1/tenant/discover",
	"/api/v1/org/users",
	"/api/v1/org/roles",
	"/api/v1/org/apps",
	"/api/v1/org/applications",
	"/api/v1/org/self",
	"/api/v1/catalog",
	"/api/v1/sandbox/",
	"/api/v1/sandbox",
}

// pathIsOrgSafe reports whether the request path is on the Org-scoped
// allowlist. Matches an exact path or a path under an allowlisted prefix.
func pathIsOrgSafe(p string) bool {
	for _, pre := range orgSafePathPrefixes {
		if p == pre || strings.HasPrefix(p, strings.TrimSuffix(pre, "/")+"/") {
			return true
		}
	}
	return false
}

// OrgScopeGuard is a chi-compatible middleware applied to the whole
// RequireSession group in cmd/api/main.go. For a NON-Org-scoped session
// (Sovereign-admin / operator) it is a transparent passthrough — zero
// behaviour change. For an Org-scoped customer session it enforces the
// deny-by-default allowlist: anything outside orgSafePathPrefixes is 403'd
// (org-scoped-forbidden). RequireSession must run first so
// auth.ClaimsFromContext is populated.
//
// Exposed as a method on *Handler so cmd/api/main.go installs it with
// rg.Use(h.OrgScopeGuard) right after rg.Use(auth.RequireSession(...)).
func (h *Handler) OrgScopeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// OPTIONS pre-flights pass through (CORS).
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		claims := auth.ClaimsFromContext(r.Context())
		// Non-Org sessions are unaffected — full Sovereign-admin surface.
		if !claimsAreOrgScoped(claims) {
			next.ServeHTTP(w, r)
			return
		}
		// Org-scoped customer session: deny-by-default outside the allowlist.
		if pathIsOrgSafe(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "org-scoped-forbidden",
			"detail": "this Organization session has no access to Sovereign-admin or cross-organization resources",
		})
	})
}
