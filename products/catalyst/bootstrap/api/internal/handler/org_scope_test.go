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
