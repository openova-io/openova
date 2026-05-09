// catalog_proxy_test.go — qa-loop iter-3 coverage for the
// /api/v1/catalog* proxy on catalyst-api. Reuses the fakeCatalogClient
// + sampleWordpressBlueprint helpers defined in applications_test.go.
//
// Cases:
//   - GET /api/v1/catalog          → 200 with items array
//   - GET /api/v1/catalog/{name}   → 200 happy / 404 missing
//   - GET /api/v1/catalog/{name}/versions/{version} → 200 happy / 404 missing
//   - 502 when catalog client is unwired (defensive)
package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func registerCatalogProxyRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/catalog", h.HandleCatalogList)
	r.Get("/api/v1/catalog/{name}", h.HandleCatalogGet)
	r.Get("/api/v1/catalog/{name}/versions/{version}", h.HandleCatalogGetVersion)
}

func TestHandleCatalogList_OK(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	r := chi.NewRouter()
	registerCatalogProxyRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body CatalogListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v (body=%s)", err, rec.Body.String())
	}
	if len(body.Items) != 1 || body.Items[0].Name != "bp-wordpress" {
		t.Fatalf("expected 1 item bp-wordpress, got %+v", body.Items)
	}
}

func TestHandleCatalogList_EmptyNeverNull(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog())
	r := chi.NewRouter()
	registerCatalogProxyRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// Wire shape MUST be {"items":[]} not {"items":null} so the UI's
	// .map() / .find() in catalog.api.ts doesn't trip on null.
	var raw map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	items, ok := raw["items"].([]interface{})
	if !ok {
		t.Fatalf("items not an array: %T %v", raw["items"], raw["items"])
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestHandleCatalogList_502WhenUnwired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// deliberately do NOT call SetCatalogClient
	r := chi.NewRouter()
	registerCatalogProxyRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCatalogGet_OK(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	r := chi.NewRouter()
	registerCatalogProxyRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/bp-wordpress", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var bp CatalogBlueprint
	if err := json.Unmarshal(rec.Body.Bytes(), &bp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if bp.Name != "bp-wordpress" {
		t.Fatalf("expected bp-wordpress, got %s", bp.Name)
	}
}

func TestHandleCatalogGet_404Missing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog()) // empty catalog
	r := chi.NewRouter()
	registerCatalogProxyRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/bp-missing", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleCatalogGetVersion_OK(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	r := chi.NewRouter()
	registerCatalogProxyRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/bp-wordpress/versions/1.2.3", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var bp CatalogBlueprint
	if err := json.Unmarshal(rec.Body.Bytes(), &bp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if bp.Name != "bp-wordpress" || bp.Version != "1.2.3" {
		t.Fatalf("expected bp-wordpress@1.2.3, got %s@%s", bp.Name, bp.Version)
	}
	// GetVersion populates Raw — confirm it's still on the wire.
	if bp.Raw == nil {
		t.Fatalf("expected Raw to be populated on getVersion response")
	}
}

func TestHandleCatalogGetVersion_404Missing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetCatalogClient(newFakeCatalog(sampleWordpressBlueprint()))
	r := chi.NewRouter()
	registerCatalogProxyRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/bp-wordpress/versions/9.9.9", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}
