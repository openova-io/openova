package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openova-io/openova/core/services/shared/respond"
	"github.com/openova-io/openova/core/services/tenant/catalog"
)

// BackingService is the per-tenant backing-service row rendered by the
// console and admin. The metadata (name, version, endpoint) is derived from
// the catalog + installer — the runtime fields (pod_status, ready_replicas)
// come from the provisioning service, which is the only component with
// kube-API access in this stack.
type BackingService struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`           // "postgres" | "mysql" | "redis"
	Name          string `json:"name"`           // display name from catalog
	Category      string `json:"category"`       // "database" / "cache"
	Version       string `json:"version"`        // inferred from installer image tag
	EndpointHost  string `json:"endpoint_host"`  // in-vCluster FQDN
	EndpointPort  int    `json:"endpoint_port"`  // service port the installer exposes
	PodStatus     string `json:"pod_status"`     // "Running" | "Pending" | "Failed" | "unknown" | "not_found"
	ReadyReplicas int    `json:"ready_replicas"` // from live pod status
	TotalReplicas int    `json:"total_replicas"` // from live pod status
	Image         string `json:"image,omitempty"`
}

// backingServiceSpec captures the hard facts the installer bakes into the
// tenant's manifests (see services/provisioning/gitops/gitops.go —
// generatePostgres/generateMySQL/generateRedis). Keep in sync.
type backingServiceSpec struct {
	port    int
	image   string // fallback when the running pod hasn't reported a status yet
	version string // human-friendly version string extracted from the image
}

// Inside the vCluster every app runs in the `apps` namespace and talks to
// these services via their short name — but rendering the full FQDN in the
// console saves the user a round-trip to our docs when they need to wire up
// a custom config.
var backingServiceSpecs = map[string]backingServiceSpec{
	"postgres": {port: 5432, image: "postgres:16-alpine", version: "16"},
	"mysql":    {port: 3306, image: "mariadb:11", version: "MariaDB 11"},
	"redis":    {port: 6379, image: "valkey/valkey:8-alpine", version: "Valkey 8"},
}

// ListBackingServices handles GET /tenant/orgs/{id}/backing-services. Only
// members of the tenant may call it. Returns an empty list when the tenant
// has no service apps installed — the UI renders a "No backing services"
// empty state on top of that.
func (h *Handler) ListBackingServices(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	// Membership-gated because connection hosts/ports are arguably
	// implementation detail; we don't gate by owner/admin because the value
	// is read-only and helps any team member debug their app.
	if _, ok := h.requireMembership(w, r, tenantID); !ok {
		return
	}
	h.backingServices(w, r, tenantID)
}

// AdminListBackingServices handles GET /tenant/admin/tenants/{id}/backing-services.
// Same payload as ListBackingServices but gated on superadmin, not
// tenant-membership, so the admin console can surface it across all tenants.
func (h *Handler) AdminListBackingServices(w http.ResponseWriter, r *http.Request) {
	if !requireSuperadmin(r) {
		respond.Error(w, http.StatusForbidden, "superadmin role required")
		return
	}
	tenantID := r.PathValue("id")
	h.backingServices(w, r, tenantID)
}

func (h *Handler) backingServices(w http.ResponseWriter, r *http.Request, tenantID string) {
	tenant, err := h.Store.GetTenant(r.Context(), tenantID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to load tenant")
		return
	}
	if tenant == nil {
		respond.Error(w, http.StatusNotFound, "tenant not found")
		return
	}

	if h.Catalog == nil {
		respond.Error(w, http.StatusNotImplemented, "catalog client not configured")
		return
	}

	apps, err := h.Catalog.ListApps(r.Context())
	if err != nil {
		respond.Error(w, http.StatusBadGateway, "failed to reach catalog")
		return
	}
	byID := make(map[string]*catalog.App, len(apps))
	bySlug := make(map[string]*catalog.App, len(apps))
	for i := range apps {
		byID[apps[i].ID] = &apps[i]
		bySlug[apps[i].Slug] = &apps[i]
	}

	// Collect every backing service this tenant actually runs. The installer
	// (services/provisioning/gitops) provisions postgres/mysql/redis whenever
	// any user-selected app declares it as a dependency, even though the
	// dependency itself is not written back into tenant.Apps. Iterating over
	// both direct installs AND the transitive deps keeps console/admin in sync
	// with what the vCluster actually has running.
	seen := make(map[string]bool)
	var installed []*catalog.App
	addService := func(a *catalog.App) {
		if a == nil || !isServiceApp(a) || seen[a.Slug] {
			return
		}
		seen[a.Slug] = true
		installed = append(installed, a)
	}
	for _, appID := range tenant.Apps {
		a, ok := byID[appID]
		if !ok {
			continue
		}
		// Case 1: user directly installed a service app (e.g. picked postgres
		// on its own so multiple apps can share it).
		addService(a)
		// Case 2: regular app that depends on a backing service — pull its
		// deps through the catalog lookup.
		for _, depSlug := range a.Dependencies {
			addService(bySlug[depSlug])
		}
	}
	if len(installed) == 0 {
		respond.OK(w, map[string]any{"services": []BackingService{}})
		return
	}

	// Ask provisioning for live pod status in one round-trip. Failures here
	// are not fatal — we degrade to "unknown" so the metadata still renders.
	slugs := make([]string, 0, len(installed))
	for _, a := range installed {
		slugs = append(slugs, a.Slug)
	}
	statuses := h.fetchPodStatuses(r.Context(), tenant.Subdomain, slugs)

	out := make([]BackingService, 0, len(installed))
	for _, a := range installed {
		spec, hasSpec := backingServiceSpecs[a.Slug]
		host := fmt.Sprintf("%s.apps.svc.cluster.local", a.Slug)
		row := BackingService{
			ID:           a.ID,
			Slug:         a.Slug,
			Name:         a.Name,
			Category:     categoryFor(a.Slug),
			EndpointHost: host,
		}
		if hasSpec {
			row.EndpointPort = spec.port
			row.Version = spec.version
			row.Image = spec.image
		}
		if st, ok := statuses[a.Slug]; ok {
			row.PodStatus = st.PodStatus
			row.ReadyReplicas = st.ReadyReplicas
			row.TotalReplicas = st.TotalReplicas
			if st.Image != "" {
				row.Image = st.Image
			}
		} else {
			row.PodStatus = "unknown"
		}
		out = append(out, row)
	}
	respond.OK(w, map[string]any{"services": out})
}

// isServiceApp returns true when a catalog App represents a backing service
// (database, cache, queue). Kept in sync with the same helper in the console
// and admin UIs.
func isServiceApp(a *catalog.App) bool {
	return a != nil && (a.Kind == "service" || a.System)
}

// categoryFor assigns a coarse bucket used by the UI to group the row
// (database vs cache). The catalog's Category field is stringy — this lookup
// keeps the UI rendering stable even if the catalog entry drifts.
func categoryFor(slug string) string {
	switch slug {
	case "postgres", "mysql":
		return "database"
	case "redis":
		return "cache"
	default:
		return "service"
	}
}

// podStatus mirrors the provisioning response shape.
type podStatus struct {
	Slug          string `json:"slug"`
	PodStatus     string `json:"pod_status"`
	ReadyReplicas int    `json:"ready_replicas"`
	TotalReplicas int    `json:"total_replicas"`
	Image         string `json:"image"`
}

// fetchPodStatuses asks the provisioning service for live pod state. We keep
// the timeout short (3s) so the UI never hangs on a slow kube-API — any
// failure yields an empty map, and the caller renders "unknown" rows.
func (h *Handler) fetchPodStatuses(ctx context.Context, slug string, services []string) map[string]podStatus {
	if h.ProvisioningURL == "" || slug == "" || len(services) == 0 {
		return nil
	}
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	q := url.Values{}
	q.Set("slug", slug)
	q.Set("services", strings.Join(services, ","))
	fullURL := fmt.Sprintf("%s/provisioning/backing-services?%s", h.ProvisioningURL, q.Encode())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var body struct {
		Services []podStatus `json:"services"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	out := make(map[string]podStatus, len(body.Services))
	for _, s := range body.Services {
		out[s.Slug] = s
	}
	return out
}
