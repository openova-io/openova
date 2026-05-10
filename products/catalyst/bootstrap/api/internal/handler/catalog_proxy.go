// Package handler — catalog_proxy.go: qa-loop iter-3 fix for the
// /api/v1/catalog* surface on Sovereign Console.
//
// Background (qa-loop iter-1, Fix #5, PR #1186): catalyst-catalog runs
// behind catalyst-api in the Slice-L architecture, but the Slice-L
// chart originally exposed it via a Gateway HTTPRoute attached to the
// `api.<sovereign>` hostname only. The Sovereign Console is served at
// `console.<sovereign>` and proxies its `/api/*` calls through the
// catalyst-api ingress. With no `/api/v1/catalog*` route registered in
// catalyst-api itself, every UI catalog call from the console hostname
// 404'd at chi's NotFound handler — even though a direct curl to
// `https://api.<sovereign>/api/v1/catalog` returned the expected 401.
//
// Fix #5's HTTPRoute template explicitly noted this follow-up:
//
//   "Operators who prefer to keep the catalog endpoint behind catalyst-api
//    (rather than via a standalone HTTPRoute on the api hostname) can
//    disable this template via .Values.services.catalog.httpRoute.enabled
//    = false and add a proxy rule in catalyst-api instead — that path is
//    left open as a follow-up so catalyst-api can do additional auth/audit
//    wrapping."
//
// This file IS that proxy rule. Both surfaces (HTTPRoute + this proxy)
// can coexist: the HTTPRoute serves direct `api.<sovereign>` consumers
// (e.g. CLI tooling) while this proxy serves the in-tier
// `console.<sovereign>/api/v1/catalog*` UI fetches that go through
// catalyst-api's ingress.
//
// Endpoints exposed (all GET, session-gated):
//
//   GET /api/v1/catalog                              — list (org from query)
//   GET /api/v1/catalog/{name}                       — get latest
//   GET /api/v1/catalog/{name}/versions/{version}    — get version
//
// All three are thin proxies onto the existing `httpCatalogClient`
// (catalog_client.go) which is already wired in main.go via
// SetCatalogClient. Per docs/INVIOLABLE-PRINCIPLES.md #4 the upstream
// URL is configurable via CATALYST_CATALOG_URL — this proxy adds no
// new hardcoded URLs.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #10 the session token is forwarded
// via the catalog client's attachAuth helper, never logged.

package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// HandleCatalogList — proxies GET /api/v1/catalog?org=<slug> to
// catalyst-catalog. Returns the wire shape catalyst-catalog returns
// ({"items": [...]}) so the UI's catalog.api.ts decoder is unchanged.
//
// 502 when the catalog client is unwired (defensive: main.go always
// wires it, but a misconfigured deployment shouldn't 500).
// 502 when the upstream call fails — the upstream's status code is
// surfaced in the error body so the operator sees the real cause.
func (h *Handler) HandleCatalogList(w http.ResponseWriter, r *http.Request) {
	if h.catalogClient == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "catalog_unavailable",
			"message": "catalog client not configured",
		})
		return
	}
	org := strings.TrimSpace(r.URL.Query().Get("org"))
	tok := applicationSessionToken(r)
	items, err := h.catalogClient.List(r.Context(), org, tok)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "catalog_upstream",
			"message": err.Error(),
		})
		return
	}
	if items == nil {
		// json marshalling: never emit `null` for a list endpoint.
		items = []CatalogBlueprint{}
	}
	// Populate the versions[] + chartRef wire alias on every entry so
	// every consumer (UI version-picker, CLI, matrix asserters) sees a
	// stable shape regardless of whether the upstream catalog inlined
	// the version index. Per `feedback_no_mvp_no_workarounds.md` the
	// alias carries the REAL OCI chart reference (TC-059, TC-060).
	for i := range items {
		items[i].PopulateVersionsAlias()
	}
	writeJSON(w, http.StatusOK, CatalogListResponse{Items: items})
}

// HandleCatalogGet — proxies GET /api/v1/catalog/{name} to
// catalyst-catalog (latest visible version). 404 when the upstream
// reports the blueprint doesn't exist (matches catalog_client's
// ErrBlueprintNotFound contract).
func (h *Handler) HandleCatalogGet(w http.ResponseWriter, r *http.Request) {
	if h.catalogClient == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "catalog_unavailable",
			"message": "catalog client not configured",
		})
		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "invalid_request",
			"message": "name path parameter is required",
		})
		return
	}
	tok := applicationSessionToken(r)
	bp, err := h.catalogClient.Get(r.Context(), name, tok)
	if err != nil {
		if errors.Is(err, ErrBlueprintNotFound) {
			// qa-loop iter-9 Fix #43, Cluster-C (TC-088): the matrix
			// asserts the literal "404" token in the body so the
			// status field is explicit alongside the canonical error
			// vocabulary. The HTTP status is also 404.
			writeJSON(w, http.StatusNotFound, map[string]any{
				"error":   "blueprint_not_found",
				"status":  404,
				"code":    "404",
				"message": "blueprint " + name + " not found in catalog",
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "catalog_upstream",
			"message": err.Error(),
		})
		return
	}
	bp.PopulateVersionsAlias()
	writeJSON(w, http.StatusOK, bp)
}

// HandleCatalogGetVersion — proxies GET
// /api/v1/catalog/{name}/versions/{version} to catalyst-catalog. The
// upstream populates Raw on this endpoint so the install handler (and
// the InstallPage version-detail panel) can read configSchema without
// a second hop.
func (h *Handler) HandleCatalogGetVersion(w http.ResponseWriter, r *http.Request) {
	if h.catalogClient == nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "catalog_unavailable",
			"message": "catalog client not configured",
		})
		return
	}
	name := strings.TrimSpace(chi.URLParam(r, "name"))
	version := strings.TrimSpace(chi.URLParam(r, "version"))
	if name == "" || version == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":   "invalid_request",
			"message": "name and version path parameters are required",
		})
		return
	}
	tok := applicationSessionToken(r)
	bp, err := h.catalogClient.GetVersion(r.Context(), name, version, tok)
	if err != nil {
		if errors.Is(err, ErrBlueprintNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":   "blueprint_version_not_found",
				"message": "blueprint " + name + "@" + version + " not found in catalog",
			})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":   "catalog_upstream",
			"message": err.Error(),
		})
		return
	}
	bp.PopulateVersionsAlias()
	writeJSON(w, http.StatusOK, bp)
}
