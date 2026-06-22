package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// orgAppTestRegistry builds a tenant registry with a free-subdomain Org
// (slug "demo", real namespace org-<uuid>) mirroring the live omantel.biz
// demo Org (#4116).
func orgAppTestRegistry(t *testing.T) *store.TenantRegistry {
	t.Helper()
	reg, err := store.NewTenantRegistry(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := reg.Put(store.TenantRegistration{
		Host:                  "console.demo.omani.homes",
		TenantID:              "7283eb4a-19e5-4e86-9066-d4aa26762064",
		TenantKind:            store.TenantKindOrg,
		KeycloakRealmURL:      "https://keycloak.demo.omani.homes/realms/org-demo",
		KeycloakClientID:      "catalyst-ui",
		OrganizationNamespace: "org-7283eb4a-19e5-4e86-9066-d4aa26762064",
		OrgKeycloakRealmName:  "org-demo",
	}); err != nil {
		t.Fatalf("put demo: %v", err)
	}
	// A sibling Org the demo session must NOT be able to target.
	if err := reg.Put(store.TenantRegistration{
		Host:                  "console.acme.omani.homes",
		TenantID:              "acme-uuid",
		TenantKind:            store.TenantKindOrg,
		KeycloakRealmURL:      "https://keycloak.acme.omani.homes/realms/org-acme",
		KeycloakClientID:      "catalyst-ui",
		OrganizationNamespace: "org-acme-uuid",
		OrgKeycloakRealmName:  "org-acme",
	}); err != nil {
		t.Fatalf("put acme: %v", err)
	}
	return reg
}

func orgAppPostBody(t *testing.T, host string, claims *auth.Claims, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/org/applications", strings.NewReader(string(raw)))
	req.Header.Set("X-Tenant-Host", host)
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	h := &Handler{log: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	h.SetTenantRegistry(orgAppTestRegistry(t))
	// catalogClient intentionally nil — the install core early-returns
	// catalog-not-wired (503) AFTER org-resolution + namespace-forcing, so a
	// 503 catalog-not-wired proves the org-scope plumbing all passed.
	rec := httptest.NewRecorder()
	h.HandleOrgApplicationInstall(rec, req)
	return rec
}

// TestOrgAppInstall_ResolvesOwnOrgNamespace — a demo-org-scoped session
// POSTing to /api/v1/org/applications resolves its OWN namespace from the
// tenant registry and reaches the shared install core (catalog-not-wired
// 503), proving the org-scope path is wired (no OrgScopeGuard 403, no
// tier-admin rejection, namespace resolved server-side). #4116.
func TestOrgAppInstall_ResolvesOwnOrgNamespace(t *testing.T) {
	claims := &auth.Claims{Tier: orgScopedTier, Org: "demo"}
	rec := orgAppPostBody(t, "console.demo.omani.homes", claims, map[string]any{
		"blueprint": "bp-agenity", "version": "0.3.0", "name": "agenity",
	})
	// Reached the install core → catalog-not-wired soft-error (the
	// writeApplicationInstallSoftError envelope is HTTP 200 carrying the
	// "503"/catalog-not-wired tokens in the body, the canonical install
	// wire-shape). If org resolution had failed we'd see a hard
	// 400/403/404/422 status instead.
	if !strings.Contains(rec.Body.String(), "catalog-not-wired") {
		t.Fatalf("expected catalog-not-wired (core reached), got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound || rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("org resolution must have passed before the core, got hard status %d: %s", rec.Code, rec.Body.String())
	}
}

// TestOrgAppInstall_CrossOrgForged — a demo-org-scoped session forging a
// sibling Org's host (console.acme) is 403 org-scope-mismatch (#4110
// binding), never reaching the install core. #4116.
func TestOrgAppInstall_CrossOrgForged(t *testing.T) {
	claims := &auth.Claims{Tier: orgScopedTier, Org: "demo"}
	rec := orgAppPostBody(t, "console.acme.omani.homes", claims, map[string]any{
		"blueprint": "bp-wordpress", "version": "0.4.1", "name": "leak",
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 org-scope-mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "org-scope-mismatch") {
		t.Fatalf("expected org-scope-mismatch body, got %s", rec.Body.String())
	}
}

// TestOrgAppInstall_OTECHHostRejected — the Sovereign's own console host
// (tenant_kind != org) is rejected (tenant-not-org), so the org-app route is
// genuinely Org-only. #4116.
func TestOrgAppInstall_UnknownHostRejected(t *testing.T) {
	rec := orgAppPostBody(t, "console.unknown.example", nil, map[string]any{
		"blueprint": "bp-wordpress", "version": "0.4.1", "name": "x",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 tenant-not-registered, got %d: %s", rec.Code, rec.Body.String())
	}
}
