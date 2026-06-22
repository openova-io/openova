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

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

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

// orgAppsGet drives GET /api/v1/org/applications for the given host/claims
// against a handler seeded with the supplied dynamic objects (HRs, HTTPRoutes,
// Application CRs).
func orgAppsGet(t *testing.T, host string, claims *auth.Claims, dynObjs []runtime.Object) *httptest.ResponseRecorder {
	t.Helper()
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	h := newSovereignHandler(t, nil, dynObjs)
	h.SetTenantRegistry(orgAppTestRegistry(t))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/org/applications", nil)
	req.Header.Set("X-Tenant-Host", host)
	if claims != nil {
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	}
	rec := httptest.NewRecorder()
	h.HandleOrgApplications(rec, req)
	return rec
}

// makeHRWithRelease is makeHR plus a spec.chart.spec.chart + spec.releaseName,
// so the projection can resolve the open URL by the release/Service-name shape.
func makeHRWithRelease(name, ns, ready, chart, release string) *unstructured.Unstructured {
	u := makeHR(name, ns, ready)
	_ = unstructured.SetNestedField(u.Object, chart, "spec", "chart", "spec", "chart")
	if release != "" {
		_ = unstructured.SetNestedField(u.Object, release, "spec", "releaseName")
	}
	return u
}

// makeHTTPRouteWithBackend is makeHTTPRoute plus a backendRef Service name so
// the projection's (ns, service-name)→url index resolves the agenity route
// whose name (`agenity`) differs from its Service (`agenity-demo-bp-agenity`).
func makeHTTPRouteWithBackend(name, ns string, hosts []string, backendSvc string) *unstructured.Unstructured {
	u := makeHTTPRoute(name, ns, hosts)
	rules := []interface{}{
		map[string]interface{}{
			"backendRefs": []interface{}{
				map[string]interface{}{"name": backendSvc},
			},
		},
	}
	_ = unstructured.SetNestedSlice(u.Object, rules, "spec", "rules")
	return u
}

// makeApplicationFull is makeApplication plus spec.blueprintRef.name +
// status.phase so the projection can render its blueprint + status.
func makeApplicationFull(name, ns, bp, phase string) *unstructured.Unstructured {
	u := makeApplication(name, ns, ns+"-prod")
	_ = unstructured.SetNestedField(u.Object, bp, "spec", "blueprintRef", "name")
	if phase != "" {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	}
	return u
}

const demoOrgNS = "org-7283eb4a-19e5-4e86-9066-d4aa26762064"

// TestOrgApplications_ProjectsOwnNamespaceOnly — #4113. A demo-org session GET
// /api/v1/org/applications sees ONLY its own namespace's estate: the agenity
// HR (with its Open URL resolved from the in-namespace HTTPRoute) + the two
// wordpress Application CRs. A sibling Org's app + a sovereign-wide app in a
// foreign namespace must NOT appear.
func TestOrgApplications_ProjectsOwnNamespaceOnly(t *testing.T) {
	dynObjs := []runtime.Object{
		// demo Org's own estate.
		makeHRWithRelease("agenity-demo", demoOrgNS, "True", "bp-agenity", ""),
		makeHTTPRouteWithBackend("agenity", demoOrgNS, []string{"agenity.demo.omani.homes"}, "agenity-demo-bp-agenity"),
		makeApplicationFull("demo-wp-shop", demoOrgNS, "bp-wordpress", "Pending"),
		makeApplicationFull("demo-wp-blog", demoOrgNS, "bp-wordpress", "Pending"),
		// FOREIGN estate — must never leak into the demo projection.
		makeHRWithRelease("agenity-acme", "org-acme-uuid", "True", "bp-agenity", ""),
		makeHR("bp-cilium", "flux-system", "True"), // sovereign spine
	}
	claims := &auth.Claims{Tier: orgScopedTier, Org: "demo"}
	rec := orgAppsGet(t, "console.demo.omani.homes", claims, dynObjs)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Apps []struct {
			ID          string `json:"id"`
			Blueprint   string `json:"blueprint"`
			Status      string `json:"status"`
			ExternalURL string `json:"externalURL"`
		} `json:"apps"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]struct {
		bp, status, url string
	}{}
	for _, a := range resp.Apps {
		byID[a.ID] = struct{ bp, status, url string }{a.Blueprint, a.Status, a.ExternalURL}
	}
	// agenity present + Ready + Open URL resolved.
	ag, ok := byID["agenity-demo"]
	if !ok {
		t.Fatalf("agenity-demo missing from org projection; got %v", byID)
	}
	if ag.status != "installed" {
		t.Errorf("agenity status = %q, want installed", ag.status)
	}
	if ag.url != "https://agenity.demo.omani.homes" {
		t.Errorf("agenity externalURL = %q, want https://agenity.demo.omani.homes", ag.url)
	}
	// Both wordpress Application CRs present.
	for _, want := range []string{"demo-wp-shop", "demo-wp-blog"} {
		if _, ok := byID[want]; !ok {
			t.Errorf("%s missing from org projection; got %v", want, byID)
		}
	}
	// Foreign estate absent.
	for _, forbidden := range []string{"agenity-acme", "bp-cilium"} {
		if _, leaked := byID[forbidden]; leaked {
			t.Errorf("FOREIGN app %s leaked into demo org projection: %v", forbidden, byID)
		}
	}
}

// TestOrgApplications_CrossOrgForged — a demo session forging the acme host is
// 403 org-scope-mismatch (the #4110 binding), never projecting acme's estate.
func TestOrgApplications_CrossOrgForged(t *testing.T) {
	claims := &auth.Claims{Tier: orgScopedTier, Org: "demo"}
	rec := orgAppsGet(t, "console.acme.omani.homes", claims, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 org-scope-mismatch, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "org-scope-mismatch") {
		t.Fatalf("expected org-scope-mismatch body, got %s", rec.Body.String())
	}
}
