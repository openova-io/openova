package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

func TestOrgSlugFromHost(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"console.demo.omani.homes", "demo"},
		{"api.demo.omani.homes", "demo"},
		{"marketplace.acme.omani.rest", "acme"},
		{"console.omantel.biz", "omantel"}, // resolved against registry, not slug-trusted
		{"demo.omani.homes", ""},           // no service label
		{"omani.homes", ""},                // too short
		{"", ""},
	}
	for _, c := range cases {
		if got := orgSlugFromHost(c.host); got != c.want {
			t.Errorf("orgSlugFromHost(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

func TestClaimsAreOrgScoped(t *testing.T) {
	if claimsAreOrgScoped(nil) {
		t.Fatal("nil claims must not be org-scoped")
	}
	if claimsAreOrgScoped(&auth.Claims{Tier: "owner"}) {
		t.Fatal("owner tier must not be org-scoped")
	}
	if claimsAreOrgScoped(&auth.Claims{Tier: "admin"}) {
		t.Fatal("admin tier must not be org-scoped")
	}
	if !claimsAreOrgScoped(&auth.Claims{Tier: orgScopedTier}) {
		t.Fatal("org-admin tier must be org-scoped")
	}
	if !claimsAreOrgScoped(&auth.Claims{Tier: "ORG-ADMIN"}) {
		t.Fatal("org-scoped tier match must be case-insensitive")
	}
}

func TestPathIsOrgSafe(t *testing.T) {
	safe := []string{
		"/api/v1/whoami",
		"/api/v1/auth/session",
		"/api/v1/org/users",
		"/api/v1/org/users/abc",
		"/api/v1/catalog",
		"/api/v1/catalog/bp-agenity",
		"/api/v1/sandbox/sessions",
		"/api/v1/sovereign/apps",
		"/api/v1/sovereign/self",
		// #4937 — self-service app list + install (server-side confined to
		// the caller's own Org inside the handlers).
		"/catalyst/v1/catalog/wordpress/instances",
		"/catalyst/v1/apps/instances",
	}
	for _, p := range safe {
		if !pathIsOrgSafe(p) {
			t.Errorf("expected %q to be Org-safe", p)
		}
	}
	unsafe := []string{
		"/api/v1/deployments",
		"/api/v1/deployments/4635277cae4ffed9",
		"/api/v1/deployments/4635277cae4ffed9/logs",
		"/api/v1/sovereigns/abc/k8s/pods",
		"/api/v1/org/bss/overview",
		"/api/v1/org/billing/revenue",
		"/api/v1/organizations",
		"/api/v1/parent-domains",
		"/api/v1/org/commerce/plans",
		// #5516 — the DEPLOYMENT-ADDRESSED Application seam is Sovereign-wide
		// (HandleApplicationList/Get resolve across every namespace) and stays
		// denied for an Org session. This is why an Org-scoped bearer cannot be
		// made to work on it by stamping a deployment_id claim: the guard never
		// looks at deployment_id, only at the tier. The own-org seam
		// /api/v1/org/applications (allowlisted above) is the reachable path.
		"/api/v1/sovereigns/29b7e14918178f7e/applications",
		"/api/v1/sovereigns/29b7e14918178f7e/applications/shop",
		// #4937 stays NARROW: the sibling catalyst/v1 app surfaces (endpoint
		// mutation, launch-url) are NOT opened to Org sessions by this change.
		"/catalyst/v1/apps/uid-1/endpoints",
		"/catalyst/v1/apps/uid-1/launch-url",
		// #5401 — the DESTRUCTIVE deployment routes, named explicitly.
		//
		// These are already denied transitively: the allowlist is prefix-based
		// and nothing grants "/api/v1/deployments", so every path under it
		// falls through to the 403. Naming them anyway is deliberate. #5401
		// asked whether an org-admin can "destroy the Sovereign hosting every
		// other Organization on it", and the answer should not depend on a
		// reader reconstructing prefix arithmetic — the wipe path is the one
		// route on this API whose successful call is unrecoverable.
		"/api/v1/deployments/4635277cae4ffed9/wipe",
		"/api/v1/deployments/4635277cae4ffed9/cloudinit-log",
	}
	for _, p := range unsafe {
		if pathIsOrgSafe(p) {
			t.Errorf("expected %q to be DENIED (not Org-safe)", p)
		}
	}
}

// orgScopeGuardTestHandler is the protected target the guard wraps.
func orgScopeGuardTestHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// TestOrgScopeGuard_OrgScoped_CannotReachDeploymentWipe answers the question
// #5401 left open, and answers it about the WIPE specifically.
//
// That issue observed the Sovereign operator panel rendering to an org-admin
// and correctly refused to guess the severity, writing: "The walk observed the
// page rendering; it did not establish whether the controls function for an
// org-admin bearer or whether the backend rejects them. Those are very
// different severities." The Decommission control is a link to a page that
// POSTs /api/v1/deployments/{id}/wipe, so that route is where the severity is
// actually decided.
//
// This asserts the middleware verdict rather than a status code alone. A 403
// can be produced by a downstream handler that already ran and did work; what
// matters for a destructive route is that the target is never ENTERED. So the
// wrapped handler records whether it was invoked, and the assertion is that it
// was not.
//
// The POST method is deliberate: pathIsOrgSafe is method-blind, and the guard
// must not become method-sensitive in a way that lets a write through on a path
// whose reads are denied.
func TestOrgScopeGuard_OrgScoped_CannotReachDeploymentWipe(t *testing.T) {
	h := &Handler{log: quietLog()}

	reached := false
	guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/4635277cae4ffed9/wipe", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Email: "customer@acme", Tier: orgScopedTier, Org: "acme"}))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if reached {
		t.Fatal("an Org-scoped session REACHED the wipe handler — this is the " +
			"privilege-escalation path #5401 asked about, and it is unrecoverable when it succeeds")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wipe must be 403'd for an Org-scoped session, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestOrgScopeGuard_SovereignAdmin_ReachesDeploymentWipe is the control for the
// test above. Without it, deleting the wipe route from the router — or breaking
// the guard closed for everyone — would leave that test green while the
// operator's own Decommission flow was dead.
func TestOrgScopeGuard_SovereignAdmin_ReachesDeploymentWipe(t *testing.T) {
	h := &Handler{log: quietLog()}

	reached := false
	guard := h.OrgScopeGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/4635277cae4ffed9/wipe", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Email: "operator@openova.io", Tier: "admin"}))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)

	if !reached {
		t.Fatalf("the Sovereign operator must still reach wipe — guard returned %d, "+
			"so the deny above proves nothing", rec.Code)
	}
}

func TestOrgScopeGuard_SovereignAdmin_Passthrough(t *testing.T) {
	h := &Handler{log: quietLog()}
	guard := h.OrgScopeGuard(http.HandlerFunc(orgScopeGuardTestHandler))

	// Sovereign-admin (owner) session reaches a sovereign-only endpoint.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/4635277cae4ffed9", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Email: "emrah.baysal@openova.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner must pass deployments endpoint, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOrgScopeGuard_OrgScoped_DeniesSovereignEndpoint(t *testing.T) {
	h := &Handler{log: quietLog()}
	guard := h.OrgScopeGuard(http.HandlerFunc(orgScopeGuardTestHandler))

	// Org-scoped customer session hits the deployments API — must 403.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/4635277cae4ffed9", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Email: "demo@openova.io", Tier: orgScopedTier, Org: "demo"}))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("org-scoped session must be 403'd on deployments, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// TestOrgScopeGuard_OrgScoped_DeniesDeploymentAddressedApplicationRoutes is
// the #5516 root-cause proof, and the reason the fix lives in the openova-MCP
// facade rather than in the bearer the Sovereign mints.
//
// The per-Org agenity MCP is handed an Org-scoped session bearer
// (sovereign_mcp_bearer_seed.go: tier=org-admin, org+org_id=<slug>, NO
// deployment_id). Its read tools used to call the deployment-addressed seam
// GET /api/v1/sovereigns/{id}/applications, which fails on the MCP side with
// "no deployment binding on the caller's token".
//
// The tempting fix — stamp a deployment_id claim onto the minted bearer — does
// NOT work, and this test is what proves it: OrgScopeGuard's verdict is a pure
// function of the TIER (claimsAreOrgScoped) and the PATH, and the
// deployment-addressed path is not allowlisted. So a deployment_id would only
// convert the MCP-side error into an upstream 403 `org-scoped-forbidden` — an
// opaque failure further from its cause. Both legs are asserted below:
// the deployment-addressed seam 403s, the own-org seam passes.
func TestOrgScopeGuard_OrgScoped_DeniesDeploymentAddressedApplicationRoutes(t *testing.T) {
	h := &Handler{log: quietLog()}
	guard := h.OrgScopeGuard(http.HandlerFunc(orgScopeGuardTestHandler))

	// The exact claim set mintOrgScopedMCPBearer produces, PLUS a
	// deployment_id — i.e. the hypothetical "fixed" bearer. It still 403s.
	orgClaimsWithDeployment := &auth.Claims{
		Email:        "openova-mcp@acme",
		Tier:         orgScopedTier,
		Org:          "acme",
		DeploymentID: "29b7e14918178f7e",
	}

	for _, path := range []string{
		"/api/v1/sovereigns/29b7e14918178f7e/applications",
		"/api/v1/sovereigns/29b7e14918178f7e/applications/shop",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, orgClaimsWithDeployment))
		rec := httptest.NewRecorder()
		guard.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: org-scoped session must be 403'd even WITH a deployment_id claim, got %d body=%s",
				path, rec.Code, rec.Body.String())
		}
	}

	// The seam the MCP must use instead is reachable for the SAME session —
	// otherwise the assertions above would merely prove "everything 403s".
	req := httptest.NewRequest(http.MethodGet, "/api/v1/org/applications", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, orgClaimsWithDeployment))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("the own-org Application seam must be reachable for an Org session, got %d body=%s",
			rec.Code, rec.Body.String())
	}
}

func TestOrgScopeGuard_OrgScoped_AllowsOwnSurface(t *testing.T) {
	h := &Handler{log: quietLog()}
	guard := h.OrgScopeGuard(http.HandlerFunc(orgScopeGuardTestHandler))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Email: "demo@openova.io", Tier: orgScopedTier, Org: "demo"}))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("org-scoped session must reach whoami, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestResolveOrgScope_OrgHost(t *testing.T) {
	dir := t.TempDir()
	reg, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:       "console.demo.omani.homes",
		TenantID:   "7283eb4a",
		TenantKind: store.TenantKindOrg,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	h := &Handler{log: quietLog(), tenantRegistry: reg}

	// Org host → Org scope.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify", nil)
	req.Header.Set("X-Forwarded-Host", "console.demo.omani.homes")
	scope, ok := h.resolveOrgScope(req)
	if !ok {
		t.Fatal("expected Org scope for console.demo.omani.homes")
	}
	if scope.Org != "demo" {
		t.Fatalf("scope.Org = %q, want demo", scope.Org)
	}

	// Unknown host (the Sovereign's own front door) → no Org scope.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/auth/pin/verify", nil)
	req2.Header.Set("X-Forwarded-Host", "console.omantel.biz")
	if _, ok := h.resolveOrgScope(req2); ok {
		t.Fatal("Sovereign front door must NOT resolve to an Org scope")
	}
}

// TestOrgScopeGuard_HostAnchored_StaleAdminCookieOnOrgHost_Denied is THE
// #4110 regression: a STALE sovereign-admin cookie (tier=admin, minted
// before the Org-scoping fix shipped, or replayed/forged) that lands on an
// Org console host must STILL be confined to the Org allowlist — the
// request HOST is the trust anchor, not the JWT tier. Without host-anchoring
// the guard keyed solely off claimsAreOrgScoped(claims), so a tier=admin
// cookie sailed straight through to /deployments — the live god-mode leak.
func TestOrgScopeGuard_HostAnchored_StaleAdminCookieOnOrgHost_Denied(t *testing.T) {
	dir := t.TempDir()
	reg, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:       "console.demo.omani.homes",
		TenantID:   "7283eb4a",
		TenantKind: store.TenantKindOrg,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	h := &Handler{log: quietLog(), tenantRegistry: reg}
	guard := h.OrgScopeGuard(http.HandlerFunc(orgScopeGuardTestHandler))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/4635277cae4ffed9", nil)
	req.Header.Set("X-Forwarded-Host", "console.demo.omani.homes")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Email: "demo@openova.io", Tier: "admin"})) // stale god-mode cookie
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stale admin cookie on Org host must be 403'd on deployments, got %d body=%s",
			rec.Code, rec.Body.String())
	}
}

// TestOrgScopeGuard_HostAnchored_AdminOnSovereignHost_Passthrough proves the
// host-anchor introduces ZERO regression: the genuine operator on the
// Sovereign's OWN console host (tenant_kind=otech) is untouched.
func TestOrgScopeGuard_HostAnchored_AdminOnSovereignHost_Passthrough(t *testing.T) {
	dir := t.TempDir()
	reg, err := store.NewTenantRegistry(dir)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:       "console.omantel.biz",
		TenantID:   "4635277cae4ffed9",
		TenantKind: store.TenantKindOTECH,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	h := &Handler{log: quietLog(), tenantRegistry: reg}
	guard := h.OrgScopeGuard(http.HandlerFunc(orgScopeGuardTestHandler))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/4635277cae4ffed9", nil)
	req.Header.Set("X-Forwarded-Host", "console.omantel.biz")
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey,
		&auth.Claims{Email: "emrah.baysal@openova.io", Tier: "owner"}))
	rec := httptest.NewRecorder()
	guard.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("operator on Sovereign host must pass, got %d body=%s", rec.Code, rec.Body.String())
	}
}
