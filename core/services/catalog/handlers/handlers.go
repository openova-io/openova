package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/openova-io/openova/core/services/catalog/store"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// Handler holds dependencies for catalog HTTP handlers.
type Handler struct {
	Store    *store.Store
	Producer *events.Producer
}

// requireAdmin checks that the request was made by a superadmin or a
// Sovereign-admin.
//
// Two roles are accepted:
//
//   - "superadmin"      — direct OpenOva operations (Catalyst-Zero
//     operators). Mints a token with role=superadmin via
//     sharedauth.SMERoleFor when the bearer holds the `catalyst-owner`
//     realm role.
//   - "sovereign-admin" — franchisee operations on a franchised
//     Sovereign. The chroot's catalyst-api bridges an operator's
//     RS256 session into the SME mesh's HS256 wire contract
//     (org_billing_vouchers.go::mintSMEBridgeToken) and stamps the
//     SME-vocabulary role from the operator's realm roles (per
//     docs/FRANCHISE-MODEL.md §3 — sovereign-admin owns the franchise's
//     marketplace catalog). Without this widening the chroot's bridge
//     token is rejected with 403 on every publish toggle (C4-012 /
//     #1735), even though the operator is the rightful admin of that
//     Sovereign's catalog.
//
// "member" still 403s — read-only operators cannot mutate the
// catalog.
func requireAdmin(r *http.Request) error {
	switch middleware.RoleFromContext(r.Context()) {
	case "superadmin", "sovereign-admin":
		return nil
	}
	return errForbidden
}

var errForbidden = &httpError{status: http.StatusForbidden, msg: "superadmin or sovereign-admin role required"}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// Public — Apps
// ---------------------------------------------------------------------------

// appResponse wraps store.App with a dependency_ids field resolved from the
// admin-edited dependency slugs. This is the #89 fix: tenant.Apps stores
// canonical UUIDs, catalog.Dependencies stores friendly slugs, and
// downstream consumers used to rebuild the slug↔ID map on every event.
// Computing dependency_ids once in the catalog service makes the answer
// authoritative and lets downstream code pass IDs through without an
// in-process translation layer.
//
// Dependencies (slugs) is preserved for the admin UI chip-picker and
// marketplace's "bundled dependencies" list, which render slugs as human
// labels. Both fields always represent the same underlying set.
type appResponse struct {
	*store.App
	DependencyIDs []string `json:"dependency_ids"`
}

func buildAppResponses(apps []store.App) []appResponse {
	bySlug := make(map[string]string, len(apps))
	for i := range apps {
		bySlug[apps[i].Slug] = apps[i].ID
	}
	out := make([]appResponse, len(apps))
	for i := range apps {
		ids := make([]string, 0, len(apps[i].Dependencies))
		for _, slug := range apps[i].Dependencies {
			if id, ok := bySlug[slug]; ok {
				ids = append(ids, id)
			}
		}
		out[i] = appResponse{App: &apps[i], DependencyIDs: ids}
	}
	return out
}

// ListApps returns apps from the unified catalog. The catalog is the
// SINGLE SOURCE OF TRUTH (#710); both the Sovereign-console operator
// view and the marketplace storefront read from this endpoint, distinguished
// by the `published` query param:
//
//   - GET /catalog/apps                  → operator view: every app, regardless
//     of Published / System / Deployable. Sovereign console renders the
//     publish/unpublish toggle from this set.
//   - GET /catalog/apps?published=true   → marketplace storefront view: only
//     Published=true AND System=false AND Deployable=true. Returned set is
//     what end users actually see + can buy.
//
// Default (no query param) is operator view — the catalog has been an
// operator surface since pre-#710, and changing the default would break
// every existing internal consumer.
//
// Response is cache-friendly (5 min) for both filters.
func (h *Handler) ListApps(w http.ResponseWriter, r *http.Request) {
	publishedOnly := r.URL.Query().Get("published") == "true"
	var apps []store.App
	var err error
	if publishedOnly {
		apps, err = h.Store.ListPublishedApps(r.Context())
	} else {
		apps, err = h.Store.ListApps(r.Context())
	}
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list apps")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	respond.OK(w, buildAppResponses(apps))
}

// GetApp returns a single app by slug. Includes dependency_ids alongside the
// slug-typed dependencies so callers can pick whichever identifier they need
// without going back to the list endpoint (see #89).
func (h *Handler) GetApp(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	app, err := h.Store.GetApp(r.Context(), slug)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get app")
		return
	}
	if app == nil {
		respond.Error(w, http.StatusNotFound, "app not found")
		return
	}
	// Resolve slugs → IDs. Cheap: a single list fetch covers it and the
	// catalog is ~100 rows. On list-fetch failure fall back to the raw app
	// with empty dependency_ids — caller can recover by hitting /catalog/apps.
	all, err := h.Store.ListApps(r.Context())
	if err != nil {
		respond.OK(w, appResponse{App: app, DependencyIDs: []string{}})
		return
	}
	resolved := buildAppResponses(all)
	for i := range resolved {
		if resolved[i].App != nil && resolved[i].App.ID == app.ID {
			respond.OK(w, resolved[i])
			return
		}
	}
	respond.OK(w, appResponse{App: app, DependencyIDs: []string{}})
}

// SearchApps searches apps by query and optional category.
func (h *Handler) SearchApps(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	category := r.URL.Query().Get("category")
	apps, err := h.Store.SearchApps(r.Context(), q, category)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to search apps")
		return
	}
	respond.OK(w, apps)
}

// ---------------------------------------------------------------------------
// Public — Industries
// ---------------------------------------------------------------------------

// ListIndustries returns all industries.
func (h *Handler) ListIndustries(w http.ResponseWriter, r *http.Request) {
	industries, err := h.Store.ListIndustries(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list industries")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	respond.OK(w, industries)
}

// ---------------------------------------------------------------------------
// Public — Bundles
// ---------------------------------------------------------------------------

// ListBundles returns all bundles.
func (h *Handler) ListBundles(w http.ResponseWriter, r *http.Request) {
	bundles, err := h.Store.ListBundles(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list bundles")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	respond.OK(w, bundles)
}

// GetBundle returns a bundle by slug with expanded app details.
func (h *Handler) GetBundle(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	bundle, err := h.Store.GetBundle(r.Context(), slug)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to get bundle")
		return
	}
	if bundle == nil {
		respond.Error(w, http.StatusNotFound, "bundle not found")
		return
	}

	// Expand apps in the bundle.
	apps, err := h.Store.GetAppsByIDs(r.Context(), bundle.Apps)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to expand bundle apps")
		return
	}

	type expandedBundle struct {
		store.Bundle
		ExpandedApps []store.App `json:"expanded_apps"`
	}

	respond.OK(w, expandedBundle{Bundle: *bundle, ExpandedApps: apps})
}

// ---------------------------------------------------------------------------
// Public — Plans
// ---------------------------------------------------------------------------

// ListPlans returns plans, optionally scoped by product via ?product=.
//
//	GET /catalog/plans                  — every plan (back-compat: the
//	                                      tenant service's GetPlan walks
//	                                      this list to resolve a plan by
//	                                      slug, including the sandbox-*
//	                                      tiers — see core/services/tenant/
//	                                      catalog/client.go). Changing the
//	                                      default would break sandbox
//	                                      checkout with "plan_id not found".
//	GET /catalog/plans?product=generic  — only unscoped compute tiers
//	                                      (ProductSlug==""); the marketplace
//	                                      Org-provisioning picker uses this
//	                                      so product-scoped tiers (e.g.
//	                                      Sandbox Free/Pro/Ent) do not
//	                                      duplicate alongside S/M/L/XL/Flexi.
//	GET /catalog/plans?product=sandbox  — only the Sandbox product tiers.
func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.Store.ListPlans(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list plans")
		return
	}
	plans = filterPlansByProduct(plans, r.URL.Query().Get("product"))
	w.Header().Set("Cache-Control", "public, max-age=300")
	respond.OK(w, plans)
}

// filterPlansByProduct scopes a plan list by the ?product= query value.
// An empty product returns the list unchanged (back-compat). The reserved
// keyword "generic" selects the unscoped compute tiers (ProductSlug=="");
// any other value matches Plan.ProductSlug verbatim.
func filterPlansByProduct(plans []store.Plan, product string) []store.Plan {
	if product == "" {
		return plans
	}
	filtered := make([]store.Plan, 0, len(plans))
	for _, p := range plans {
		if (product == "generic" && p.ProductSlug == "") || p.ProductSlug == product {
			filtered = append(filtered, p)
		}
	}
	return filtered
}

// ---------------------------------------------------------------------------
// Public — AddOns
// ---------------------------------------------------------------------------

// ListAddOns returns all add-ons.
func (h *Handler) ListAddOns(w http.ResponseWriter, r *http.Request) {
	addons, err := h.Store.ListAddOns(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list addons")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	respond.OK(w, addons)
}

// ---------------------------------------------------------------------------
// Admin — Apps
// ---------------------------------------------------------------------------

// CreateApp creates a new app (superadmin only).
func (h *Handler) CreateApp(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var app store.App
	if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.CreateApp(r.Context(), &app); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create app")
		return
	}
	respond.JSON(w, http.StatusCreated, app)
}

// UpdateApp updates an existing app (superadmin only).
func (h *Handler) UpdateApp(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	var app store.App
	if err := json.NewDecoder(r.Body).Decode(&app); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.UpdateApp(r.Context(), id, &app); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update app")
		return
	}
	respond.OK(w, app)
}

// SetAppPublished is the hot path the Sovereign console hits when the
// operator flips "Publish on marketplace" for a single app row (#710).
// Slug-keyed (one-bit change, one round-trip), no body — published
// state comes in as a query param.
//
//	PATCH /catalog/admin/apps/{slug}/publish?value=true
//	PATCH /catalog/admin/apps/{slug}/publish?value=false
//
// Existing tenant deployments of an unpublished app keep running per
// founder rule 2026-05-04 — unpublish is a marketplace-visibility
// toggle, not a deployment-lifecycle action.
func (h *Handler) SetAppPublished(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	slug := r.PathValue("slug")
	if slug == "" {
		respond.Error(w, http.StatusBadRequest, "slug is required")
		return
	}
	rawValue := r.URL.Query().Get("value")
	var published bool
	switch rawValue {
	case "true":
		published = true
	case "false":
		published = false
	default:
		respond.Error(w, http.StatusBadRequest, "value query param must be 'true' or 'false'")
		return
	}
	if err := h.Store.SetAppPublished(r.Context(), slug, published); err != nil {
		// SetAppPublished surfaces "not found" for unknown slugs.
		if strings.Contains(err.Error(), "not found") {
			respond.Error(w, http.StatusNotFound, "app not found")
			return
		}
		respond.Error(w, http.StatusInternalServerError, "failed to set published flag")
		return
	}
	respond.OK(w, map[string]any{"slug": slug, "published": published})
}

// DeleteApp deletes an app (superadmin only).
func (h *Handler) DeleteApp(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	if err := h.Store.DeleteApp(r.Context(), id); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete app")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// Admin — Industries
// ---------------------------------------------------------------------------

// CreateIndustry creates a new industry (superadmin only).
func (h *Handler) CreateIndustry(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var ind store.Industry
	if err := json.NewDecoder(r.Body).Decode(&ind); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.CreateIndustry(r.Context(), &ind); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create industry")
		return
	}
	respond.JSON(w, http.StatusCreated, ind)
}

// UpdateIndustry updates an existing industry (superadmin only).
func (h *Handler) UpdateIndustry(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	var ind store.Industry
	if err := json.NewDecoder(r.Body).Decode(&ind); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.UpdateIndustry(r.Context(), id, &ind); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update industry")
		return
	}
	respond.OK(w, ind)
}

// DeleteIndustry deletes an industry (superadmin only).
func (h *Handler) DeleteIndustry(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	if err := h.Store.DeleteIndustry(r.Context(), id); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete industry")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// Admin — Bundles
// ---------------------------------------------------------------------------

// CreateBundle creates a new bundle (superadmin only).
func (h *Handler) CreateBundle(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var b store.Bundle
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.CreateBundle(r.Context(), &b); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create bundle")
		return
	}
	respond.JSON(w, http.StatusCreated, b)
}

// UpdateBundle updates an existing bundle (superadmin only).
func (h *Handler) UpdateBundle(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	var b store.Bundle
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.UpdateBundle(r.Context(), id, &b); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update bundle")
		return
	}
	respond.OK(w, b)
}

// DeleteBundle deletes a bundle (superadmin only).
func (h *Handler) DeleteBundle(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	if err := h.Store.DeleteBundle(r.Context(), id); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete bundle")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// Admin — Plans
// ---------------------------------------------------------------------------

// CreatePlan creates a new plan (superadmin only).
func (h *Handler) CreatePlan(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var p store.Plan
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.CreatePlan(r.Context(), &p); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create plan")
		return
	}
	respond.JSON(w, http.StatusCreated, p)
}

// UpdatePlan updates an existing plan (superadmin only).
func (h *Handler) UpdatePlan(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	var p store.Plan
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.UpdatePlan(r.Context(), id, &p); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update plan")
		return
	}
	respond.OK(w, p)
}

// DeletePlan deletes a plan (superadmin only).
func (h *Handler) DeletePlan(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	if err := h.Store.DeletePlan(r.Context(), id); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete plan")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}

// ---------------------------------------------------------------------------
// Admin — AddOns
// ---------------------------------------------------------------------------

// CreateAddOn creates a new add-on (superadmin only).
func (h *Handler) CreateAddOn(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	var a store.AddOn
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.CreateAddOn(r.Context(), &a); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create addon")
		return
	}
	respond.JSON(w, http.StatusCreated, a)
}

// UpdateAddOn updates an existing add-on (superadmin only).
func (h *Handler) UpdateAddOn(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	var a store.AddOn
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := h.Store.UpdateAddOn(r.Context(), id, &a); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to update addon")
		return
	}
	respond.OK(w, a)
}

// DeleteAddOn deletes an add-on (superadmin only).
func (h *Handler) DeleteAddOn(w http.ResponseWriter, r *http.Request) {
	if err := requireAdmin(r); err != nil {
		respond.Error(w, http.StatusForbidden, err.Error())
		return
	}
	id := r.PathValue("id")
	if err := h.Store.DeleteAddOn(r.Context(), id); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to delete addon")
		return
	}
	respond.JSON(w, http.StatusNoContent, nil)
}
