// sme_commerce.go — the Organizations commerce editors' proxy hop
// (issue #3378 DoD 7/8).
//
// The Sovereign console's Organizations menu surfaces editor UIs for the
// commerce catalog (plans / add-ons / bundles / industries / apps). Those
// editors ride the EXISTING superadmin-JWT /catalog/admin/* endpoints on
// the SME commerce catalog (core/services/catalog/handlers/routes.go:
// 19-38) — §6 of #3378: "Any new endpoint beyond B1-B3 = FAIL". This file
// is therefore NOT a new business endpoint: it is the same thin proxy hop
// HandleSovereignAppPublish already uses (mintSMEBridgeToken → smeCatalog
// client → /catalog/admin/*), generalized to the full CRUD so the console
// (served at console.<sovereign>, which proxies /api/* through catalyst-
// api) can reach the admin endpoints that are otherwise only reachable
// behind the catalog service's own gateway.
//
// Routes (registered in cmd/api/main.go), all session-gated like the rest
// of /api/v1/* and forwarded with the canonical SME bridge token:
//
//   POST   /api/v1/sme/commerce/{kind}            → create
//   PUT    /api/v1/sme/commerce/{kind}/{id}       → update
//   DELETE /api/v1/sme/commerce/{kind}/{id}       → delete
//
// where {kind} ∈ {plans, addons, bundles, industries, apps}. Reads use
// the existing public catalog list endpoints via the SME gateway — the
// editors fetch those directly, so no read proxy is added here.

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// commerceKinds is the closed set of /catalog/admin/* resource families
// the editors manage. Anything outside this set is rejected with 404 so
// the proxy can never be pointed at an arbitrary upstream sub-path.
var commerceKinds = map[string]bool{
	"plans":      true,
	"addons":     true,
	"bundles":    true,
	"industries": true,
	"apps":       true,
}

// HandleSMECommerceList — GET /api/v1/sme/commerce/{kind}.
//
// Reads the PUBLIC catalog list endpoint (/catalog/{kind}) through
// catalyst-api so the Sovereign console's commerce tables render the rows
// that exist in the catalog store. On the console host (console.<sovereign>)
// /api/* proxies to catalyst-api, which — unlike the SME/marketplace
// gateway — does not route /api/catalog/* to the catalog service, so the
// console's old direct GET /api/catalog/plans 404'd even though the
// storefront showed the plan. This read hop closes that gap (issue #3378
// — the plans-table 404). No bearer: the catalog list endpoints are public.
func (h *Handler) HandleSMECommerceList(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	if !commerceKinds[kind] {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "unknown-commerce-kind",
			"detail": "kind must be one of plans, addons, bundles, industries, apps",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), smeCatalogProbeBudget)
	defer cancel()
	upstreamStatus, respBody, ct, err := smeCatalog().PublicProxy(ctx, "/"+kind)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sme-catalog-unreachable",
			"detail": err.Error(),
		})
		return
	}
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(upstreamStatus)
	if len(respBody) > 0 {
		_, _ = w.Write(respBody)
	}
}

// HandleSMECommerceCreate — POST /api/v1/sme/commerce/{kind}.
func (h *Handler) HandleSMECommerceCreate(w http.ResponseWriter, r *http.Request) {
	h.proxyCommerce(w, r, http.MethodPost, "")
}

// HandleSMECommerceUpdate — PUT /api/v1/sme/commerce/{kind}/{id}.
func (h *Handler) HandleSMECommerceUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeBadRequest(w, "missing-id", "id path parameter is required")
		return
	}
	h.proxyCommerce(w, r, http.MethodPut, id)
}

// HandleSMECommerceDelete — DELETE /api/v1/sme/commerce/{kind}/{id}.
func (h *Handler) HandleSMECommerceDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeBadRequest(w, "missing-id", "id path parameter is required")
		return
	}
	h.proxyCommerce(w, r, http.MethodDelete, id)
}

// proxyCommerce is the shared body: validate kind, mint the bridge
// token, read the request body, forward to /catalog/admin/{kind}[/{id}],
// and relay the upstream status + body verbatim so the editor sees the
// real validation errors from the catalog service.
func (h *Handler) proxyCommerce(w http.ResponseWriter, r *http.Request, method, id string) {
	kind := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	if !commerceKinds[kind] {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "unknown-commerce-kind",
			"detail": "kind must be one of plans, addons, bundles, industries, apps",
		})
		return
	}

	bearer, status, errResp := h.mintSMEBridgeToken(r)
	if errResp != nil {
		writeJSON(w, status, errResp)
		return
	}

	// Read the request body verbatim (create/update carry JSON; delete
	// carries none). We forward raw bytes — the catalog service owns the
	// schema, so its own decoder + validation is the single source of
	// truth. readMutationBody returns (body, true) when a 4xx was already
	// written (the inverted-boolean convention; see infrastructure.go).
	var body []byte
	if method != http.MethodDelete {
		b, errd := readMutationBody(w, r)
		if errd {
			return
		}
		body = b
	}

	subPath := "/" + kind
	if id != "" {
		subPath += "/" + id
	}

	ctx, cancel := context.WithTimeout(r.Context(), smeCatalogProbeBudget)
	defer cancel()
	upstreamStatus, respBody, ct, err := smeCatalog().AdminProxy(ctx, method, subPath, body, bearer)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sme-catalog-unreachable",
			"detail": err.Error(),
		})
		return
	}
	// #3648 (train/hw150) — make the catalog edit IaC: when an `apps`
	// create/update succeeds at the commerce store, ALSO commit the edited
	// card fields into the catalog-sovereign Gitea Org as a Blueprint CR.
	// git is the single source of truth; the store stays as a cache. This
	// is additive + best-effort — a git failure NEVER changes the API
	// response the unchanged UI relies on (the store write already
	// happened); it is logged for the operator. DELETEs are not mirrored
	// here (removing a curated CR is the /blueprints lifecycle, not an
	// edit). See catalog_edit_git.go.
	if kind == "apps" && method != http.MethodDelete &&
		upstreamStatus >= 200 && upstreamStatus < 300 {
		h.commitCatalogAppEditToGit(ctx, body)
	}

	// Relay the upstream response verbatim — status + body — so the
	// editor surfaces the catalog service's real result (created object,
	// validation error, 404, etc.).
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(upstreamStatus)
	if len(respBody) > 0 {
		_, _ = w.Write(respBody)
	}
}

// commitCatalogAppEditToGit decodes the `apps` mutation body (the
// commerce App JSON the UI's saveCatalogEdit PUT/POSTs) into the catalog
// card-overlay shape and commits it to the local catalog git
// (catalog-sovereign). The commerce App JSON tags (slug, name, tagline,
// icon, icon_light, icon_dark, supported_topologies) line up with
// catalogEdit's tags VERBATIM, so the same bytes decode straight in.
//
// Failures are logged + swallowed: the store write already succeeded, so
// the API response stays correct; the entry simply isn't yet reflected in
// git (e.g. Gitea unreachable pre-cutover). The next edit re-attempts.
func (h *Handler) commitCatalogAppEditToGit(ctx context.Context, body []byte) {
	if len(body) == 0 {
		return
	}
	var edit catalogEdit
	if err := json.Unmarshal(body, &edit); err != nil {
		if h.log != nil {
			h.log.Warn("catalog-edit-git: decode apps body failed", "err", err)
		}
		return
	}
	if strings.TrimSpace(edit.Slug) == "" || !edit.hasOverlay() {
		return // nothing IaC-relevant to commit
	}
	if _, err := h.writeCatalogEditToGit(ctx, edit); err != nil && h.log != nil {
		h.log.Warn("catalog-edit-git: commit to catalog-sovereign failed",
			"slug", edit.Slug, "err", err)
	}
}
