package main

import "github.com/go-chi/chi/v5"

func registerSessionCatalog(rg chi.Router, d routeDeps) {
	h := d.h
	// qa-loop iter-3 — catalog proxy. Slice-L originally exposed
	// catalyst-catalog only via a Gateway HTTPRoute on the
	// `api.<sovereign>` hostname; the Sovereign Console at
	// `console.<sovereign>` proxies its `/api/*` calls through
	// catalyst-api's ingress, so without these registrations the
	// UI's `/api/v1/catalog*` fetches 404'd at chi's NotFound
	// handler. See internal/handler/catalog_proxy.go for the
	// architectural rationale (Fix #5's HTTPRoute template
	// explicitly flagged this as the in-tier follow-up). Routes
	// are session-gated like the rest of /api/v1/* — anonymous
	// catalog browsing is not a UI surface and the catalog client
	// forwards the caller's token so AnonymousReads policy on
	// catalyst-catalog still applies if the upstream is configured
	// for it.
	rg.Get("/api/v1/catalog", h.HandleCatalogList)
	rg.Get("/api/v1/catalog/{name}", h.HandleCatalogGet)
	rg.Get("/api/v1/catalog/{name}/versions/{version}", h.HandleCatalogGetVersion)
	// #3668 §5D — the full-CR catalog IaC editor. Commits the WHOLE
	// blueprint.yaml the admin edited (spec.source / manifests / sso /
	// placementSchema / endpoints / shareable / contextSchema — fields
	// the 7-field card form can never touch) to the SAME Gitea file the
	// card-edit path writes (catalog-sovereign/<bp>/blueprint.yaml),
	// under the dedicated git budget, tier-admin-gated. The console's
	// "Edit IaC" mode (YamlEditor) drives this.
	rg.Get("/api/v1/catalog/{name}/iac", h.HandleGetCatalogBlueprintIaC)
	rg.Put("/api/v1/catalog/{name}/iac", h.HandleCatalogBlueprintIaCEdit)
}
