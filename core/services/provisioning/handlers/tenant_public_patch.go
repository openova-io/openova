// tenant_public_patch.go — patch Organization.spec.tenantPublic on
// product install so the organization-controller's HTTPRoute reconciler
// (PR #1644) has the fields it needs to render `<slug>.<parentDomain>`.
//
// Background. PR #1644 added Organization.spec.tenantPublic and a
// per-tenant HTTPRoute reconciler. Without that struct populated, the
// reconciler short-circuits at the empty ParentDomain guard and no
// route ever lands — every `<slug>.omani.homes` host bounces to the
// Sovereign-wide tenant-wildcard route (storefront landing page) and
// the tenant's installed product is unreachable. Nothing was setting
// the field. This file closes the loop.
//
// Trigger. The provisioning service is the only component that knows
// when a tenant's product (WordPress / Nextcloud / GitLab / BookStack /
// Ghost / …) has actually become Ready. Both the initial provisioning
// path (`provision.completed`) and the day-2 install path
// (`provision.app_ready`) call patchOrgTenantPublic at the same point
// they publish the corresponding event. The patch is a JSON merge-
// patch against the cluster-scoped Organization CR keyed by tenant
// slug (= Org slug per CreateOrg in tenant-service/handlers.go); a
// 404 on the PATCH is treated as benign because not every tenant has
// a matching Organization CR (the Organization flow is opt-in per
// ADR-0001 §2.7 — legacy tenant-service tenants live alongside
// Organization-managed ones during the migration).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 the parent zone comes from the
// CR-input chain (env → Handler.TenantParentDomain → patch body),
// never hardcoded. Empty TenantParentDomain disables the feature at
// this Sovereign — the operator opts in by setting the env on the
// provisioning Deployment.

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// tenantPublicDefaultBackendPort matches
// core/controllers/organization/internal/controller/tenant_route.go's
// tenantRouteDefaultBackendPort. Re-declared (not imported) so the
// provisioning service has no compile-time dep on the controllers
// module.
const tenantPublicDefaultBackendPort = 80

// patchOrgTenantPublic writes spec.tenantPublic onto the Organization
// CR for the given tenant slug. Returns (rendered=false, nil) when the
// feature is disabled (empty parent domain) so callers can log a single
// info line and move on; returns a non-nil err only on a true infra
// failure (apiserver unreachable / 5xx / non-401-non-404 4xx).
//
// Inputs:
//   - tenantSlug → maps to Organization.metadata.name (Organizations
//     are cluster-scoped and named by slug).
//   - product     → the picked product slug ("wordpress" |
//     "nextcloud" | "gitlab" | "bookstack" | "ghost" | …) the tenant
//     just finished installing. Stamped on the HTTPRoute as
//     catalyst.openova.io/tenant-product so operators can filter
//     access-matrix views by installed product.
//   - hostNamespace → "tenant-<slug>" — the host NS where the vCluster
//     syncs the product's Service as `<product>-x-<host-ns>-x-vcluster`.
//     We forward that canonical name as BackendService so the HTTPRoute
//     reconciler emits a backendRef that resolves on day one. The
//     reconciler's "Service in Org's host namespace" contract is the
//     controller's concern, not provisioning's — when the Organization
//     flow eventually adopts a tenant-service tenant the operator
//     adds a ReferenceGrant.
//
// The handler is best-effort by design: any failure is logged but does
// NOT block the provisioning workflow from publishing its success
// event. A failed patch leaves the Org CR's existing spec.tenantPublic
// untouched (merge-patch semantics) — the next install retry covers it.
func (h *Handler) patchOrgTenantPublic(ctx context.Context, tenantSlug, product, hostNamespace string) (bool, error) {
	parent := strings.ToLower(strings.TrimSpace(h.TenantParentDomain))
	if parent == "" {
		// Feature disabled on this Sovereign — caller logs and moves
		// on. The legacy tenant-service path keeps working unchanged.
		return false, nil
	}
	slug := strings.TrimSpace(tenantSlug)
	if slug == "" {
		return false, fmt.Errorf("tenant slug is empty — cannot derive Organization CR name")
	}
	product = strings.TrimSpace(product)

	// Derive the BackendService name in the vCluster-synced form:
	// `<product>-x-<host-ns>-x-vcluster`. This mirrors the syncedName
	// helper in gitops.go (the ingress generator uses the same shape).
	// Empty product / host NS means caller has no product wired yet —
	// skip the patch rather than emit a useless route with backend="".
	hostNS := strings.TrimSpace(hostNamespace)
	if product == "" || hostNS == "" {
		slog.Info("tenant-public-patch: skipping — product or host namespace empty",
			"slug", slug, "product", product, "host_namespace", hostNS)
		return false, nil
	}
	backendService := fmt.Sprintf("%s-x-%s-x-vcluster", product, hostNS)

	// Build the merge-patch body. We patch ONLY spec.tenantPublic
	// (and stable sub-fields) so any operator-managed field elsewhere
	// on the CR is untouched.
	body := map[string]any{
		"spec": map[string]any{
			"tenantPublic": map[string]any{
				"parentDomain":   parent,
				"subdomain":      slug,
				"backendService": backendService,
				"backendPort":    int64(tenantPublicDefaultBackendPort),
				"product":        product,
			},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return false, fmt.Errorf("marshal tenant-public patch: %w", err)
	}

	path := fmt.Sprintf("/apis/orgs.openova.io/v1/organizations/%s", slug)
	if _, err := h.k8sRequest("PATCH", path, payload); err != nil {
		// 404 = no matching Organization CR — benign (tenant lives in
		// the legacy tenant-service flow without an Organization
		// counterpart). Log debug and treat as not-rendered.
		if strings.Contains(err.Error(), "status 404") {
			slog.Debug("tenant-public-patch: Organization CR not found — skipping",
				"slug", slug)
			return false, nil
		}
		return false, fmt.Errorf("patch Organization %s: %w", slug, err)
	}
	slog.Info("tenant-public-patch: Organization.spec.tenantPublic set",
		"slug", slug,
		"parent_domain", parent,
		"product", product,
		"backend_service", backendService,
	)
	return true, nil
}

// emitOrgTenantPublic is the call-site convenience wrapper. It runs
// patchOrgTenantPublic and emits a single info / warn log line that
// the provisioning workflow can fire-and-forget — failures DO NOT
// surface as provision-failed because the route is auxiliary (the
// Sovereign-wide tenant-wildcard route keeps the tenant reachable on
// the legacy `console.<sovFQDN>` path either way).
//
// Returning nothing here is deliberate — callers don't need to thread
// the rendered flag through their flow.
func (h *Handler) emitOrgTenantPublic(ctx context.Context, tenantSlug, product, hostNamespace string) {
	rendered, err := h.patchOrgTenantPublic(ctx, tenantSlug, product, hostNamespace)
	if err != nil {
		slog.Warn("tenant-public-patch: best-effort patch failed",
			"slug", tenantSlug, "product", product, "error", err)
		return
	}
	if !rendered {
		// Either the feature is disabled (empty parent domain) or the
		// Organization CR doesn't exist for this tenant slug. Both are
		// benign no-ops worth logging at debug so the operator can
		// confirm the path was exercised.
		slog.Debug("tenant-public-patch: no-op",
			"slug", tenantSlug, "product", product,
			"parent_domain_configured", strings.TrimSpace(h.TenantParentDomain) != "")
	}
}
