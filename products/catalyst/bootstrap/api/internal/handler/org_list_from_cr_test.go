package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// newOrgHandlerWithSeededCRs wires the Organization deps with a local store
// AND a fake dynamic client pre-seeded with the supplied Organization CRs, so
// the #4479 list/detail read path can be exercised against canonical CRs that
// the local provision store never saw (the funnel-Org case).
func newOrgHandlerWithSeededCRs(t *testing.T, crs ...*unstructured.Unstructured) (*Handler, *store.OrganizationProvisionStore) {
	t.Helper()
	dir := t.TempDir()
	tenantStore, err := store.NewOrganizationProvisionStore(dir)
	if err != nil {
		t.Fatalf("tenant store: %v", err)
	}
	h := &Handler{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h.SetOrganizationDeps(OrganizationDeps{
		Store:     tenantStore,
		OTECHFQDN: "otech.example",
	})

	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		organizationGVR(): "OrganizationList",
	}
	objs := make([]runtime.Object, 0, len(crs))
	for _, c := range crs {
		objs = append(objs, c)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList, objs...)
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: nil, dyn: dyn}, nil
	})
	return h, tenantStore
}

// orgCR builds a minimal Ready Organization CR (orgs.openova.io/v1) shaped
// like the one both doors mint.
func orgReadyCR(slug, displayName, parentDomain, ownerEmail, phase string) *unstructured.Unstructured {
	spec := map[string]any{
		"slug":         slug,
		"displayName":  displayName,
		"kind":         "customer",
		"tier":         "org",
		"planSlug":     "s",
		"billingMode":  "real",
		"sovereignRef": "otech.example",
		"owners": []any{
			map[string]any{"email": ownerEmail, "role": "owner"},
		},
	}
	if parentDomain != "" {
		spec["tenantPublic"] = map[string]any{
			"parentDomain": parentDomain,
			"subdomain":    slug,
		}
	}
	obj := map[string]any{
		"apiVersion": "orgs.openova.io/v1",
		"kind":       "Organization",
		"metadata": map[string]any{
			"name": slug,
			"labels": map[string]any{
				"openova.io/tenant-id": "tid-" + slug,
			},
		},
		"spec": spec,
	}
	if phase != "" {
		obj["status"] = map[string]any{
			"vcluster": map[string]any{"phase": phase},
		}
	}
	return &unstructured.Unstructured{Object: obj}
}

// TestListOrganizations_IncludesFunnelCRs is the #4479 DoD: a funnel-created
// Org (CR only, no local provision record) appears in GET /api/v1/organizations
// alongside the parent Sovereign Org. Before the fix the directory returned
// only the local store rows (empty for funnel Orgs) → {"items":[]}.
func TestListOrganizations_IncludesFunnelCRs(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("uatwalk91", "UAT Walk 91", "omani.homes", "owner@uatwalk91.test", "Ready"),
		orgReadyCR("omantel", "Omantel", "", "admin@omantel.biz", "Ready"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Items []orgTenantResponse `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items: want 2 (funnel Org + parent) got %d: %s", len(got.Items), w.Body.String())
	}
	bySlug := map[string]orgTenantResponse{}
	for _, it := range got.Items {
		bySlug[it.Subdomain] = it
	}
	uat, ok := bySlug["uatwalk91"]
	if !ok {
		t.Fatalf("funnel Org uatwalk91 missing from directory")
	}
	if uat.State != store.STSDone {
		t.Errorf("uatwalk91 state: want done (Ready phase) got %s", uat.State)
	}
	if uat.ConsoleHost != "console.uatwalk91.omani.homes" {
		t.Errorf("uatwalk91 console_host: want console.uatwalk91.omani.homes got %q", uat.ConsoleHost)
	}
	if _, ok := bySlug["omantel"]; !ok {
		t.Errorf("parent Org omantel missing from directory")
	}
}

// TestListOrganizations_LocalWinsOnCollision proves the merge dedupes by slug
// and the local store row (richer in-flight timeline) wins over the CR row.
func TestListOrganizations_LocalWinsOnCollision(t *testing.T) {
	h, st := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("acme", "ACME from CR", "omani.homes", "cr@acme.test", "Ready"),
	)
	// Local store row for the SAME slug with a distinct company name +
	// in-flight (not done) state.
	if err := st.Put(store.OrganizationProvisionRecord{
		OrganizationID: "uuid-acme",
		State:          store.STSBPChartsInstalled,
		Subdomain:      "acme",
		DomainMode:     store.OrganizationDomainFreeSubdomain,
		ParentDomain:   "omani.homes",
		AdminEmail:     "local@acme.test",
		CompanyName:    "ACME from local store",
		OTECHFQDN:      "otech.example",
	}); err != nil {
		t.Fatalf("seed local: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations", nil)
	w := httptest.NewRecorder()
	h.HandleListOrganizations(w, req)

	var got struct {
		Items []orgTenantResponse `json:"items"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Items) != 1 {
		t.Fatalf("items: want 1 (deduped) got %d", len(got.Items))
	}
	if got.Items[0].CompanyName != "ACME from local store" {
		t.Errorf("collision: local row should win, got company %q", got.Items[0].CompanyName)
	}
}

// TestGetOrganization_FunnelCRBySlug is the org-detail half: a slug-addressed
// GET resolves the Organization CR when the local store has no matching
// record. Before the fix this 404'd org-tenant-not-found.
func TestGetOrganization_FunnelCRBySlug(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("uatwalk91", "UAT Walk 91", "omani.homes", "owner@uatwalk91.test", "Ready"),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/uatwalk91", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "uatwalk91")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.HandleGetOrganization(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	var got orgTenantResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Subdomain != "uatwalk91" {
		t.Errorf("subdomain: want uatwalk91 got %q", got.Subdomain)
	}
	if got.AdminEmail != "owner@uatwalk91.test" {
		t.Errorf("admin_email: want owner@uatwalk91.test got %q", got.AdminEmail)
	}
	if got.State != store.STSDone {
		t.Errorf("state: want done got %s", got.State)
	}
}

// TestGetOrganization_StillMissesUnknown confirms a slug with neither a local
// record nor a CR still 404s org-tenant-not-found.
func TestGetOrganization_StillMissesUnknown(t *testing.T) {
	h, _ := newOrgHandlerWithSeededCRs(t,
		orgReadyCR("acme", "ACME", "omani.homes", "a@b.test", "Ready"),
	)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/organizations/nope", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "nope")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.HandleGetOrganization(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status: want 404 got %d", w.Code)
	}
	var got map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["error"] != "org-tenant-not-found" {
		t.Errorf("error: want org-tenant-not-found got %q", got["error"])
	}
}
