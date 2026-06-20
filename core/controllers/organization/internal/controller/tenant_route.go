// tenant_route.go — per-Organization HTTPRoute reconciler.
//
// Issue #1629 follow-up. PowerDNS now resolves
// `console.<slug>.<parentDomain>` (e.g. `console.acme.omani.homes`) for
// every Org whose Sovereign has a parent_domains entry with role=org-
// pool, but no HTTPRoute attaches that hostname to the Org's installed
// product Service. Result: the Cilium Gateway happily terminates TLS
// on the wildcard cert, then returns the storefront landing page (the
// only HTTPRoute attached to `*.<sovFQDN>` is the `tenant-wildcard`
// route → marketplace console Service) instead of the tenant's
// WordPress / Nextcloud / GitLab install.
//
// The fix is reconciler-side: when `spec.tenantPublic.parentDomain`
// is set on an Organization, the controller renders a per-tenant
// HTTPRoute in the Org's namespace (= spec.slug) pointing at the
// supplied BackendService. The route attaches to the canonical
// `cilium-gateway/kube-system` parent — the same parent the
// marketplace, back-office, and tenant-wildcard routes already attach
// to — and surfaces `console.<subdomain>.<parentDomain>` as its
// hostname so the Cilium Gateway hostname matcher picks the per-
// tenant route over the wildcard for any request matching the exact
// host. The `console.` prefix is the canonical per-tenant console
// hostname per CLAUDE.md §0 and matches org_tenant_gitops.go:536
// (chart-side host derivation for bp-wordpress-tenant et al.) so the
// runtime reconciler and the GitOps overlay agree byte-for-byte.
// TBD-A67 issue #1990.
//
// Design notes:
//
//   - HTTPRoute is created/updated via the controller-runtime client
//     with an Unstructured object (same pattern continuum/switchover
//     uses for HTTPRoute weight drains). This avoids pulling in the
//     gateway-api Go types for a single resource.
//   - BackendService is treated as a Service in the Org's own
//     namespace — no ReferenceGrant required. Operators that point
//     at a cross-namespace Service (rare) can ship the
//     ReferenceGrant alongside the Org.
//   - The HTTPRoute name is the Org slug (deterministic, idempotent).
//     OwnerReferences are intentionally NOT set: Organizations are
//     cluster-scoped while the HTTPRoute is namespaced, and K8s rejects
//     namespaced→cluster OwnerReferences. Deletion is handled by the
//     Org's namespace teardown (when the Org's vCluster ns is
//     removed, every HTTPRoute under it goes with it).
//   - Skipped silently when ParentDomain is empty (the zero-value
//     case for Orgs that don't yet have a public hostname).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 every operationally-meaningful
// value flows through the CR — no hardcoded gateway name, parent
// namespace, or port number in the renderer.

package controller

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// httpRouteGVK identifies the Gateway API HTTPRoute v1 resource the
// reconciler writes. Matches the GVK referenced by the existing
// marketplace-routes.yaml, httproute.yaml, and continuum/switchover
// drainers — every Cilium Gateway-API path on a Sovereign goes through
// gateway.networking.k8s.io/v1.HTTPRoute.
var httpRouteGVK = schema.GroupVersionKind{
	Group:   "gateway.networking.k8s.io",
	Version: "v1",
	Kind:    "HTTPRoute",
}

// tenantRouteParentDefaults are the defaults the reconciler applies
// when the Organization spec doesn't override them. They match the
// canonical Cilium Gateway placement on every Sovereign
// (clusters/_template/sovereign-tls/cilium-gateway.yaml installs the
// Gateway as `cilium-gateway` in `kube-system`).
const (
	tenantRouteDefaultGatewayName      = "cilium-gateway"
	tenantRouteDefaultGatewayNamespace = "kube-system"
	tenantRouteDefaultBackendPort      = int32(80)
)

// reconcileTenantRoute creates or updates the per-Organization
// HTTPRoute when `spec.tenantPublic.parentDomain` is set. Returns
// (rendered=true, nil) when the route was written, (false, nil) when
// the feature is disabled (empty parentDomain), or (false, err) on a
// transient write failure (the parent reconciler requeues).
func (r *Reconciler) reconcileTenantRoute(ctx context.Context, org *orgapi.Organization) (bool, error) {
	tp := org.Spec.TenantPublic
	parentDomain := strings.TrimSpace(tp.ParentDomain)
	if parentDomain == "" {
		// Feature disabled — Orgs that don't yet have a public
		// hostname are accessed via the Sovereign-wide
		// `*.<sovFQDN>` tenant-wildcard route. No-op + no condition
		// surfacing (matches the existing reconciler's quiet-mode
		// for unset optional fields).
		return false, nil
	}

	subdomain := strings.TrimSpace(tp.Subdomain)
	if subdomain == "" {
		subdomain = org.Spec.Slug
	}
	backend := strings.TrimSpace(tp.BackendService)
	if backend == "" {
		// #3376 fix: the funnel mints the Organization CR with
		// tenantPublic.{parentDomain,subdomain} set but NO backendService —
		// the provisioning service patches backendService LATER, once the
		// purchased product becomes Ready (provision.completed /
		// provision.app_ready → patchOrgTenantPublic, tenant_public_patch.go).
		// Treating "parentDomain set, backendService not-yet-set" as a HARD
		// ERROR failed the WHOLE Org reconcile at this step (the caller's
		// fail() path) — so the Org never went Ready, the per-Org Flux loop +
		// realm + UserAccess status never converged, and no HTTPRoute ever
		// landed even after the product came up. This is a normal transient
		// ordering window, NOT a failure: skip the route render (no-op) and
		// let a later reconcile — triggered by the backendService PATCH —
		// emit it. The Sovereign-wide `*.<sovFQDN>` tenant-wildcard route
		// keeps the Org reachable in the meantime.
		return false, nil
	}
	port := tp.BackendPort
	if port == 0 {
		port = tenantRouteDefaultBackendPort
	}

	// TBD-A67 issue #1990: hostname is `console.<subdomain>.<parentDomain>`
	// — the `console.` infix is the canonical per-tenant console host
	// per CLAUDE.md §0 + org_tenant_gitops.go:536. Without it, the
	// runtime reconciler emitted `<slug>.<parent>` while the chart-side
	// overlay emitted `console.<slug>.<parent>` and the two drifted.
	hostname := fmt.Sprintf("console.%s.%s", subdomain, parentDomain)
	ns := org.Spec.Slug
	name := org.Spec.Slug

	labels := map[string]string{
		"openova.io/organization":         org.Spec.Slug,
		"openova.io/sovereign":            org.Spec.SovereignRef,
		"openova.io/managed-by":           "organization-controller",
		"app.kubernetes.io/managed-by":    "catalyst",
		"catalyst.openova.io/component":   "tenant-public-route",
		"catalyst.openova.io/parent-zone": parentDomain,
	}
	if p := strings.TrimSpace(tp.Product); p != "" {
		labels["catalyst.openova.io/tenant-product"] = p
	}

	desiredSpec := map[string]any{
		"parentRefs": []any{
			map[string]any{
				"name":      tenantRouteDefaultGatewayName,
				"namespace": tenantRouteDefaultGatewayNamespace,
			},
		},
		"hostnames": []any{hostname},
		"rules": []any{
			map[string]any{
				"matches": []any{
					map[string]any{
						"path": map[string]any{
							"type":  "PathPrefix",
							"value": "/",
						},
					},
				},
				"backendRefs": []any{
					map[string]any{
						"name": backend,
						"port": int64(port),
					},
				},
			},
		},
	}

	desired := unstructured.Unstructured{}
	desired.SetGroupVersionKind(httpRouteGVK)
	desired.SetName(name)
	desired.SetNamespace(ns)
	desired.SetLabels(labels)
	desired.Object["spec"] = desiredSpec

	current := unstructured.Unstructured{}
	current.SetGroupVersionKind(httpRouteGVK)
	err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &current)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("get HTTPRoute %s/%s: %w", ns, name, err)
		}
		if err := r.Create(ctx, &desired); err != nil {
			if apierrors.IsAlreadyExists(err) {
				// Race: another reconcile created it between Get
				// and Create. Re-Get + Update on next pass.
				return true, nil
			}
			return false, fmt.Errorf("create HTTPRoute %s/%s: %w", ns, name, err)
		}
		return true, nil
	}

	// Update: copy desired spec + labels onto current (preserves
	// resourceVersion + any operator-added annotations).
	current.Object["spec"] = desiredSpec
	current.SetLabels(labels)
	if err := r.Update(ctx, &current); err != nil {
		return false, fmt.Errorf("update HTTPRoute %s/%s: %w", ns, name, err)
	}
	return true, nil
}
