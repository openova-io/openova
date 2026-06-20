// org_commerce.go — the Organizations commerce editors' proxy hop
// (issue #3378 DoD 7/8).
//
// The Sovereign console's Organizations menu surfaces editor UIs for the
// commerce catalog (plans / add-ons / bundles / industries / apps). Those
// editors ride the EXISTING superadmin-JWT /catalog/admin/* endpoints on
// the Organization commerce catalog (core/services/catalog/handlers/routes.go:
// 19-38) — §6 of #3378: "Any new endpoint beyond B1-B3 = FAIL". This file
// is therefore NOT a new business endpoint: it is the same thin proxy hop
// HandleSovereignAppPublish already uses (mintOrgBridgeToken → orgCatalog
// client → /catalog/admin/*), generalized to the full CRUD so the console
// (served at console.<sovereign>, which proxies /api/* through catalyst-
// api) can reach the admin endpoints that are otherwise only reachable
// behind the catalog service's own gateway.
//
// Routes (registered in cmd/api/main.go), all session-gated like the rest
// of /api/v1/* and forwarded with the canonical Organization bridge token:
//
//   POST   /api/v1/org/commerce/{kind}            → create
//   PUT    /api/v1/org/commerce/{kind}/{id}       → update
//   DELETE /api/v1/org/commerce/{kind}/{id}       → delete
//
// where {kind} ∈ {plans, addons, bundles, industries, apps}. Reads use
// the existing public catalog list endpoints via the Organization gateway — the
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

// HandleOrgCommerceList — GET /api/v1/org/commerce/{kind}.
//
// Reads the PUBLIC catalog list endpoint (/catalog/{kind}) through
// catalyst-api so the Sovereign console's commerce tables render the rows
// that exist in the catalog store. On the console host (console.<sovereign>)
// /api/* proxies to catalyst-api, which — unlike the Organization/marketplace
// gateway — does not route /api/catalog/* to the catalog service, so the
// console's old direct GET /api/catalog/plans 404'd even though the
// storefront showed the plan. This read hop closes that gap (issue #3378
// — the plans-table 404). No bearer: the catalog list endpoints are public.
func (h *Handler) HandleOrgCommerceList(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "kind")))
	if !commerceKinds[kind] {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error":  "unknown-commerce-kind",
			"detail": "kind must be one of plans, addons, bundles, industries, apps",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), orgCatalogProbeBudget)
	defer cancel()
	upstreamStatus, respBody, ct, err := orgCatalog().PublicProxy(ctx, "/"+kind)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "org-catalog-unreachable",
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

// HandleOrgCommerceCreate — POST /api/v1/org/commerce/{kind}.
func (h *Handler) HandleOrgCommerceCreate(w http.ResponseWriter, r *http.Request) {
	h.proxyCommerce(w, r, http.MethodPost, "")
}

// HandleOrgCommerceUpdate — PUT /api/v1/org/commerce/{kind}/{id}.
func (h *Handler) HandleOrgCommerceUpdate(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		writeBadRequest(w, "missing-id", "id path parameter is required")
		return
	}
	h.proxyCommerce(w, r, http.MethodPut, id)
}

// HandleOrgCommerceDelete — DELETE /api/v1/org/commerce/{kind}/{id}.
func (h *Handler) HandleOrgCommerceDelete(w http.ResponseWriter, r *http.Request) {
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

	bearer, status, errResp := h.mintOrgBridgeToken(r)
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

	ctx, cancel := context.WithTimeout(r.Context(), orgCatalogProbeBudget)
	defer cancel()
	upstreamStatus, respBody, ct, err := orgCatalog().AdminProxy(ctx, method, subPath, body, bearer)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "org-catalog-unreachable",
			"detail": err.Error(),
		})
		return
	}
	// #3668 (train/hw150) — make the catalog edit a load-bearing IaC commit.
	// When an `apps` create/update succeeds at the commerce store, COMMIT the
	// edited card fields into the catalog-sovereign Gitea Org as a Blueprint
	// CR — git is the single source of truth, the store is its cache. The
	// commit runs under its OWN budget (catalogEditGitBudget, NOT the
	// already-partly-consumed 1500ms probe ctx — §3C/§5C), and its result is
	// surfaced to the caller instead of being swallowed: a non-committed edit
	// must never read as a durable green "Saved" (§5: "no green 'Saved'
	// unless the IaC write is durable"). DELETEs are not mirrored here
	// (removing a curated CR is the /blueprints lifecycle, not an edit). See
	// catalog_edit_git.go.
	if kind == "apps" && method != http.MethodDelete &&
		upstreamStatus >= 200 && upstreamStatus < 300 {
		committed, reason := h.commitCatalogAppEditToGit(r.Context(), body)
		// The commerce store (the cache) has the edit; whether git (the
		// source) durably accepted it is reported alongside so the UI shows
		// "Saved to IaC ✓" vs "Saved (cache only) — IaC commit failed: …".
		// Only a successful 2xx-with-JSON-body upstream reaches here, so we
		// re-wrap the upstream object with the commit verdict rather than
		// relaying it opaquely.
		h.writeCatalogEditEnvelope(w, upstreamStatus, respBody, committed, reason)
		return
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

// catalogEditEnvelope wraps a successful commerce-store `apps` mutation with
// the IaC-commit verdict (#3668 §5C). The UI's saveCatalogEdit reads
// `committed` + `reason` to decide between a durable "Saved to IaC ✓" and an
// amber "Saved (cache only) — IaC commit failed: <reason>". `store` carries
// the upstream object verbatim so any consumer that read the raw created/
// updated row before #3668 still finds it.
type catalogEditEnvelope struct {
	Stored    bool            `json:"stored"`
	Committed bool            `json:"committed"`
	Reason    string          `json:"reason,omitempty"`
	Store     json.RawMessage `json:"store,omitempty"`
}

// writeCatalogEditEnvelope emits the #3668 edit envelope. The commerce store
// write already succeeded (stored:true is unconditional on this path); the
// `committed`/`reason` fields report whether the git source-of-truth write
// landed. The upstream status is preserved so existing 2xx handling in the UI
// is unchanged.
func (h *Handler) writeCatalogEditEnvelope(w http.ResponseWriter, upstreamStatus int, storeBody []byte, committed bool, reason string) {
	env := catalogEditEnvelope{
		Stored:    true,
		Committed: committed,
		Reason:    reason,
	}
	if len(storeBody) > 0 && json.Valid(storeBody) {
		env.Store = json.RawMessage(storeBody)
	}
	writeJSON(w, upstreamStatus, env)
}

// commitCatalogAppEditToGit decodes the `apps` mutation body (the
// commerce App JSON the UI's saveCatalogEdit PUT/POSTs) into the catalog
// card-overlay shape and commits it to the local catalog git
// (catalog-sovereign). The commerce App JSON tags (slug, name, tagline,
// icon, icon_light, icon_dark, supported_topologies) line up with
// catalogEdit's tags VERBATIM, so the same bytes decode straight in.
//
// #3668 §5C — the git commit is the SOURCE write, not a best-effort
// side-effect. It runs under its OWN budget (catalogEditGitBudgetDuration,
// derived from a FRESH context off the parent request — NOT the 1500ms
// commerce probe ctx that was already partly consumed by AdminProxy) so a
// busy-but-healthy Gitea commits durably. The verdict is RETURNED, never
// swallowed: the caller surfaces (committed, reason) so the UI distinguishes
// a durable "Saved to IaC ✓" from a cache-only "IaC commit failed".
//
// Returns:
//   - (true, "")            — the bytes were committed (a new commit, or the
//     file was already byte-identical to the edit).
//   - (false, "<reason>")   — the edit is not yet durable in git; reason is a
//     short human string for the UI (unwired client / nothing-to-commit /
//     the transport error). The store write already succeeded, so the entry
//     is served from the cache until a later edit (or a reconcile) lands it.
func (h *Handler) commitCatalogAppEditToGit(parent context.Context, body []byte) (bool, string) {
	if len(body) == 0 {
		return false, "empty body"
	}
	var edit catalogEdit
	if err := json.Unmarshal(body, &edit); err != nil {
		if h.log != nil {
			h.log.Warn("catalog-edit-git: decode apps body failed", "err", err)
		}
		return false, "decode apps body failed"
	}
	if strings.TrimSpace(edit.Slug) == "" || !edit.hasOverlay() {
		return false, "nothing IaC-relevant to commit"
	}
	if h.giteaClient == nil {
		// Pre-cutover / CI: no local catalog git yet. The store cache holds
		// the edit; it becomes IaC once Gitea is reachable. Reported (not
		// swallowed) so the UI can say "cache only" rather than a false green.
		return false, "local catalog git not yet available (pre-cutover)"
	}

	// Fresh, git-sized budget off the ORIGINAL request context (so a client
	// disconnect still cancels) — decoupled from the consumed 1500ms probe ctx.
	ctx, cancel := context.WithTimeout(parent, catalogEditGitBudgetDuration())
	defer cancel()

	// writeCatalogEditToGit returns committed=false WITHOUT error in two
	// cases now that the unwired + no-overlay guards are handled above:
	// (a) a fresh commit was suppressed because the file was already
	// BYTE-IDENTICAL to the edit (PutFile's idempotency short-circuit). That
	// is a DURABLE state — the source of truth already carries these bytes —
	// so it must read as committed/durable to the UI, NOT a "cache only"
	// failure. We therefore treat (false, nil) here as durable.
	if _, err := h.writeCatalogEditToGit(ctx, edit); err != nil {
		if h.log != nil {
			h.log.Warn("catalog-edit-git: commit to catalog-sovereign failed",
				"slug", edit.Slug, "err", err)
		}
		return false, "IaC commit failed: " + err.Error()
	}
	return true, ""
}
