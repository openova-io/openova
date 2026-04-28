package handlers

import (
	"encoding/json"
	"net/http"

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

// requireAdmin checks that the request was made by a superadmin.
func requireAdmin(r *http.Request) error {
	if middleware.RoleFromContext(r.Context()) != "superadmin" {
		return errForbidden
	}
	return nil
}

var errForbidden = &httpError{status: http.StatusForbidden, msg: "superadmin role required"}

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

// ListApps returns all apps. Response is cache-friendly (5 min).
func (h *Handler) ListApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.Store.ListApps(r.Context())
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

// ListPlans returns all plans.
func (h *Handler) ListPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.Store.ListPlans(r.Context())
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to list plans")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	respond.OK(w, plans)
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
