package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/respond"
	"github.com/openova-io/openova/core/services/tenant/catalog"
)

// InstallApp handles POST /tenant/orgs/{id}/apps — adds an app to an existing
// workspace. Membership is required. Dependencies are resolved from the
// catalog. Shareable deps may be marked "dedicated" (new instance) or given
// an existing instance slug to reuse via the optional dep_choices map.
//
// Responses:
//   - 200 queued    — event published, provisioning will pick it up
//   - 400 bad input — missing/invalid slug
//   - 404           — app not in catalog
//   - 409 capacity  — install would exceed plan limits; body includes
//                     upgrade_suggestion with the next tier's slug
//   - 501           — catalog client unavailable at startup
func (h *Handler) InstallApp(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	if _, ok := h.requireOwnerOrAdmin(w, r, tenantID); !ok {
		return
	}
	// Serialize day-2 writes per-tenant so concurrent install calls see
	// consistent tenant.Apps. Storage-layer $addToSet is the primary defense,
	// this in-process mutex is belt-and-braces against FerretDB 1.24 edge
	// cases observed in dod-chaos scenario7 where 3 concurrent installs
	// ended up with only 2 ids recorded. Issue #110.
	if h.DayTwoLocks != nil {
		release := h.DayTwoLocks.acquire(tenantID)
		defer release()
	}

	if h.Catalog == nil {
		respond.Error(w, http.StatusNotImplemented, "catalog client not configured — day-2 installs unavailable")
		return
	}

	var body struct {
		Slug       string            `json:"slug"`
		DepChoices map[string]string `json:"dep_choices"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Slug == "" {
		respond.Error(w, http.StatusBadRequest, "slug is required")
		return
	}

	tenant, err := h.Store.GetTenant(r.Context(), tenantID)
	if err != nil || tenant == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}

	apps, err := h.Catalog.ListApps(r.Context())
	if err != nil {
		slog.Error("install: list catalog", "error", err)
		respond.Error(w, http.StatusBadGateway, "failed to reach catalog")
		return
	}
	byID := make(map[string]*catalog.App, len(apps))
	bySlug := make(map[string]*catalog.App, len(apps))
	for i := range apps {
		byID[apps[i].ID] = &apps[i]
		bySlug[apps[i].Slug] = &apps[i]
	}

	target, ok := bySlug[body.Slug]
	if !ok {
		respond.Error(w, http.StatusNotFound, "app not found in catalog")
		return
	}

	// Refuse installs of catalog entries that don't have a real deployment
	// template yet — before issue #102 these silently provisioned an nginx
	// placeholder that the UI happily reported as "installed".
	if !target.Deployable {
		respond.Error(w, http.StatusBadRequest,
			"app '"+body.Slug+"' is listed in the catalog but not yet deployable — try again after the provisioning template ships")
		return
	}

	// Idempotency: if already installed, return OK without re-emitting.
	if contains(tenant.Apps, target.ID) {
		respond.OK(w, map[string]string{"status": "already_installed"})
		return
	}

	// Resolve which apps actually need to be deployed: the target plus any
	// dependencies the user did not choose to reuse. Already-installed deps
	// are skipped (they're shared by default when re-requested).
	toDeploy := []string{target.ID}
	for _, depSlug := range target.Dependencies {
		dep, ok := bySlug[depSlug]
		if !ok {
			continue
		}
		// Reuse an existing instance — do not re-deploy this dep.
		if choice, ok := body.DepChoices[depSlug]; ok && choice != "dedicated" && choice != "" {
			continue
		}
		// Dep already running? Default to reuse (safe for shareable; for
		// non-shareable we still skip because two copies in one namespace
		// would collide — this is the conservative path).
		if contains(tenant.Apps, dep.ID) {
			continue
		}
		toDeploy = append(toDeploy, dep.ID)
	}

	// Capacity check: sum existing footprint + new deployables vs plan.
	plan, err := h.Catalog.GetPlan(r.Context(), tenant.PlanID)
	if err != nil {
		slog.Error("install: get plan", "error", err, "plan_id", tenant.PlanID)
		respond.Error(w, http.StatusBadGateway, "failed to look up plan")
		return
	}
	if plan != nil {
		limits := plan.ParsedLimits()
		if over, suggest := overCapacity(tenant.Apps, toDeploy, byID, limits, apps); over {
			respond.JSON(w, http.StatusConflict, map[string]any{
				"error":              "plan capacity exceeded",
				"message":            "Installing this app and its dependencies would exceed your plan's resources.",
				"upgrade_suggestion": suggest,
			})
			return
		}
	}

	// Commit: append to tenant.Apps and mark each as installing so the
	// console UI can render a progress state until provisioning confirms
	// readiness (via provision.app_ready event).
	//
	// Atomic $addToSet + $set avoids the lost-update race where N concurrent
	// InstallApp calls on the same tenant all read the same tenant.Apps,
	// append, and overwrite each other. dod-chaos scenario7 reproduces it:
	// 3 concurrent installs → only 2 recorded. Issue discovered 2026-04-20.
	appStates := make(map[string]string, len(toDeploy))
	for _, id := range toDeploy {
		appStates[id] = "installing"
	}
	if err := h.Store.AtomicAppendApps(r.Context(), tenantID, toDeploy, appStates); err != nil {
		slog.Error("install: atomic append failed", "tenant_id", tenantID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to update organization")
		return
	}
	// Re-read to get the definitive post-update Apps list — other concurrent
	// callers may have appended their ids too, and the payload / event need
	// to reflect reality.
	fresh, err := h.Store.GetTenant(r.Context(), tenantID)
	if err == nil && fresh != nil {
		tenant = fresh
	}

	// Hand the K8s work off to provisioning. HTTP is the primary path —
	// works even when the event bus is down. The event is fired as a
	// best-effort notification for any future observers.
	deploySlugs := make([]string, 0, len(toDeploy))
	for _, id := range toDeploy {
		if a, ok := byID[id]; ok {
			deploySlugs = append(deploySlugs, a.Slug)
		}
	}
	// One idempotency key per user click, shared between HTTP + event so the
	// provisioning service can dedup the two transports into a single run.
	// See issue #71.
	idempotencyKey := uuid.NewString()
	payload := map[string]any{
		"tenant_id":       tenantID,
		"tenant_slug":     tenant.Subdomain,
		"plan_id":         tenant.PlanID,
		"app_slug":        target.Slug,
		"app_id":          target.ID,
		"idempotency_key": idempotencyKey,
		"deploy_ids":      toDeploy,
		"deploy_slugs":    deploySlugs,
		"dep_choices":     body.DepChoices,
		"apps":            tenant.Apps,
	}
	if err := h.callProvisioning(r.Context(), "/provisioning/apps/install", payload); err != nil {
		slog.Error("install: provisioning HTTP call", "error", err, "tenant_id", tenantID)
	}
	if evt, err := events.NewEvent("tenant.app_install_requested", "tenant-service", tenantID, payload); err == nil {
		pubCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if pubErr := h.Producer.Publish(pubCtx, "sme.tenant.events", evt); pubErr != nil {
			slog.Debug("install: event publish (best-effort)", "error", pubErr, "tenant_id", tenantID)
		}
	}

	respond.OK(w, map[string]any{
		"status":     "queued",
		"apps":       tenant.Apps,
		"app_states": tenant.AppStates,
		"deployed":   toDeploy,
	})
}

// UninstallApp handles DELETE /tenant/orgs/{id}/apps/{slug} — removes an app
// from an existing workspace. If other installed apps depend on this one and
// the caller hasn't set ?force=true, responds 409 listing the dependents.
func (h *Handler) UninstallApp(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	slug := r.PathValue("slug")
	if _, ok := h.requireOwnerOrAdmin(w, r, tenantID); !ok {
		return
	}
	// Same per-tenant serialization as InstallApp. Issue #110.
	if h.DayTwoLocks != nil {
		release := h.DayTwoLocks.acquire(tenantID)
		defer release()
	}

	if h.Catalog == nil {
		respond.Error(w, http.StatusNotImplemented, "catalog client not configured — day-2 removals unavailable")
		return
	}

	tenant, err := h.Store.GetTenant(r.Context(), tenantID)
	if err != nil || tenant == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}

	apps, err := h.Catalog.ListApps(r.Context())
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "failed to reach catalog")
		return
	}
	bySlug := make(map[string]*catalog.App, len(apps))
	byID := make(map[string]*catalog.App, len(apps))
	for i := range apps {
		bySlug[apps[i].Slug] = &apps[i]
		byID[apps[i].ID] = &apps[i]
	}

	target, ok := bySlug[slug]
	if !ok {
		respond.Error(w, http.StatusNotFound, "app not found in catalog")
		return
	}
	if !contains(tenant.Apps, target.ID) {
		respond.OK(w, map[string]string{"status": "not_installed"})
		return
	}

	// Dependency reverse-lookup: find installed apps that list this as a dep.
	force := r.URL.Query().Get("force") == "true"
	dependents := []string{}
	for _, installedID := range tenant.Apps {
		a, ok := byID[installedID]
		if !ok || a.ID == target.ID {
			continue
		}
		for _, d := range a.Dependencies {
			if d == target.Slug {
				dependents = append(dependents, a.Name)
				break
			}
		}
	}
	if len(dependents) > 0 && !force {
		respond.JSON(w, http.StatusConflict, map[string]any{
			"error":      "app has dependents",
			"message":    "Other apps rely on this one — remove them first or retry with ?force=true.",
			"dependents": dependents,
		})
		return
	}

	// Commit: keep the app in tenant.Apps for now but flag it as
	// "uninstalling". The tenant consumer removes it from Apps once
	// provisioning publishes provision.app_removed. Until then the console
	// renders a "Removing…" state instead of misleading the user into
	// thinking the workload is already gone.
	//
	// SetAppState writes only the single app_states.<id> entry, so a
	// sibling install / uninstall on a different app can't trample us.
	// Matches the race fix landed in InstallApp above.
	if err := h.Store.SetAppState(r.Context(), tenantID, target.ID, "uninstalling"); err != nil {
		slog.Error("uninstall: set app state failed", "tenant_id", tenantID, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to update organization")
		return
	}

	// Build the post-uninstall app list for provisioning (the target is
	// still in tenant.Apps because we track it as "uninstalling" until the
	// provision.app_removed event arrives).
	postApps := make([]string, 0, len(tenant.Apps))
	for _, id := range tenant.Apps {
		if id != target.ID {
			postApps = append(postApps, id)
		}
	}
	// Shared idempotency key between HTTP + event so provisioning dedups them.
	// See issue #71.
	idempotencyKey := uuid.NewString()
	payload := map[string]any{
		"tenant_id":       tenantID,
		"tenant_slug":     tenant.Subdomain,
		"plan_id":         tenant.PlanID,
		"app_slug":        target.Slug,
		"app_id":          target.ID,
		"idempotency_key": idempotencyKey,
		"apps":            postApps,
	}
	if err := h.callProvisioning(r.Context(), "/provisioning/apps/uninstall", payload); err != nil {
		slog.Error("uninstall: provisioning HTTP call", "error", err, "tenant_id", tenantID)
	}
	if evt, err := events.NewEvent("tenant.app_uninstall_requested", "tenant-service", tenantID, payload); err == nil {
		pubCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if pubErr := h.Producer.Publish(pubCtx, "sme.tenant.events", evt); pubErr != nil {
			slog.Debug("uninstall: event publish (best-effort)", "error", pubErr, "tenant_id", tenantID)
		}
	}

	respond.OK(w, map[string]any{"status": "queued", "apps": tenant.Apps, "app_states": tenant.AppStates})
}

// UninstallPreview handles GET /tenant/orgs/{id}/apps/{slug}/uninstall-preview.
// It returns the classification the console's confirm modal renders: which
// backing services (databases, caches) would be purged versus retained
// because other installed apps still depend on them.
//
// Contract:
//
//	{
//	  "app_slug": "ghost",
//	  "app_name": "Ghost",
//	  "purged_services":   [{ "slug": "mysql", "name": "MySQL" }],
//	  "retained_services": [{ "slug": "redis", "name": "Redis" }],
//	  "dependents": []                // installed apps blocking uninstall
//	}
func (h *Handler) UninstallPreview(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	slug := r.PathValue("slug")
	if _, ok := h.requireMembership(w, r, tenantID); !ok {
		return
	}
	if h.Catalog == nil {
		respond.Error(w, http.StatusNotImplemented, "catalog client not configured")
		return
	}
	tenant, err := h.Store.GetTenant(r.Context(), tenantID)
	if err != nil || tenant == nil {
		respond.Error(w, http.StatusNotFound, "organization not found")
		return
	}
	apps, err := h.Catalog.ListApps(r.Context())
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "failed to reach catalog")
		return
	}
	bySlug := make(map[string]*catalog.App, len(apps))
	byID := make(map[string]*catalog.App, len(apps))
	for i := range apps {
		bySlug[apps[i].Slug] = &apps[i]
		byID[apps[i].ID] = &apps[i]
	}

	target, ok := bySlug[slug]
	if !ok {
		respond.Error(w, http.StatusNotFound, "app not found in catalog")
		return
	}

	type servicePreview struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	preview := struct {
		AppSlug          string           `json:"app_slug"`
		AppName          string           `json:"app_name"`
		Installed        bool             `json:"installed"`
		PurgedServices   []servicePreview `json:"purged_services"`
		RetainedServices []servicePreview `json:"retained_services"`
		Dependents       []string         `json:"dependents"`
	}{
		AppSlug:          target.Slug,
		AppName:          target.Name,
		Installed:        contains(tenant.Apps, target.ID),
		PurgedServices:   []servicePreview{},
		RetainedServices: []servicePreview{},
		Dependents:       []string{},
	}

	// Dependents blocking the uninstall.
	for _, installedID := range tenant.Apps {
		a, ok := byID[installedID]
		if !ok || a.ID == target.ID {
			continue
		}
		for _, d := range a.Dependencies {
			if d == target.Slug {
				preview.Dependents = append(preview.Dependents, a.Name)
				break
			}
		}
	}

	// A dep is retained if any OTHER installed app depends on it.
	depCount := map[string]int{}
	for _, installedID := range tenant.Apps {
		if installedID == target.ID {
			continue
		}
		a, ok := byID[installedID]
		if !ok {
			continue
		}
		for _, d := range a.Dependencies {
			depCount[d]++
		}
	}
	for _, dep := range target.Dependencies {
		row := servicePreview{Slug: dep, Name: dep}
		if da, ok := bySlug[dep]; ok && da.Name != "" {
			row.Name = da.Name
		}
		if depCount[dep] > 0 {
			preview.RetainedServices = append(preview.RetainedServices, row)
		} else {
			preview.PurgedServices = append(preview.PurgedServices, row)
		}
	}

	respond.OK(w, preview)
}

// overCapacity returns (true, suggestedNextPlan) when the union of
// existingAppIDs and newDeployIDs exceeds the plan limits on any axis.
// suggestedNextPlan is filled in with a naive "next tier" heuristic (nil if
// no larger plan is known).
func overCapacity(
	existing []string,
	toDeploy []string,
	byID map[string]*catalog.App,
	limits catalog.Limits,
	_ []catalog.App,
) (bool, string) {
	var cpu, ram, disk int
	for _, id := range existing {
		if a, ok := byID[id]; ok {
			cpu += a.CpuMilli
			ram += a.RamMB
			disk += a.DiskGB
		}
	}
	for _, id := range toDeploy {
		if contains(existing, id) {
			continue
		}
		if a, ok := byID[id]; ok {
			cpu += a.CpuMilli
			ram += a.RamMB
			disk += a.DiskGB
		}
	}
	if (limits.CpuMilli > 0 && cpu > limits.CpuMilli) ||
		(limits.RamMB > 0 && ram > limits.RamMB) ||
		(limits.DiskGB > 0 && disk > limits.DiskGB) {
		return true, suggestNextPlan(limits)
	}
	return false, ""
}

// suggestNextPlan maps the current plan's size class to the next one up. The
// tenant service doesn't introspect the full plan list here to avoid another
// catalog round-trip; the marketplace uses the same S→M→L→XL sizing so this
// lookup stays truthful.
func suggestNextPlan(cur catalog.Limits) string {
	switch {
	case cur.CpuMilli <= 2000:
		return "m"
	case cur.CpuMilli <= 4000:
		return "l"
	case cur.CpuMilli <= 8000:
		return "xl"
	default:
		return ""
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
