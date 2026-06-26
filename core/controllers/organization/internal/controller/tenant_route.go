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
// when the Organization spec doesn't override them.
//
// #4075: the per-Org console route attaches to the DEDICATED console
// Gateway (cilium-gateway-console, #4053 blast-radius isolation), NOT
// the shared `cilium-gateway`. The console Gateway is where the
// `*.<parentDomain>` TLS listener + Secret live (reconcileTenantConsoleTLS
// appends them), so the route MUST parent there for the hostname matcher
// + TLS termination to line up. Previously this defaulted to
// `cilium-gateway`, where no `*.<parentDomain>` listener exists — the
// route attached but TLS still closed the connection. The Gateway
// name/namespace are resolved via the Reconciler's consoleGatewayName /
// consoleGatewayNamespace helpers (env-overridable, Inviolable
// Principle #4).
const tenantRouteDefaultBackendPort = int32(80)

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
	// #4186: the per-Org console host (console.<slug>.<pool>) is served by
	// the catalyst-ui SPA + catalyst-api — the SAME Sovereign console + API
	// the operator front door uses — NOT by an Org-namespace product
	// backend. (The Org's PRODUCTS get their own hosts, e.g.
	// agenity.<slug>.<pool>, rendered elsewhere.) Therefore the console
	// route is INDEPENDENT of tenantPublic.backendService: it MUST land as
	// soon as the Org has a public hostname so the customer can SIGN IN and
	// land on /jobs even before any product is Ready. Gating it on
	// backendService (the old product-route model) is exactly why this route
	// never auto-rendered and had to be hand-applied per-Org
	// (`managed-by: issue-4075-live-unblock`), leaving pool Orgs bouncing
	// /jobs → /login (#4186). The marketplace→console secure handoff
	// (/auth/org-handover, #4182) is one of the catalyst-api rules below —
	// without it the customer's session is never established on the pool host.

	// TBD-A67 issue #1990: hostname is `console.<subdomain>.<parentDomain>`
	// — the `console.` infix is the canonical per-tenant console host
	// per CLAUDE.md §0 + org_tenant_gitops.go:536. Without it, the
	// runtime reconciler emitted `<slug>.<parent>` while the chart-side
	// overlay emitted `console.<slug>.<parent>` and the two drifted.
	hostname := fmt.Sprintf("console.%s.%s", subdomain, parentDomain)
	// The route + its backendRefs live in catalyst-system (where catalyst-api
	// + catalyst-ui Services are) so the refs resolve same-namespace without
	// a ReferenceGrant — matching the hand-applied route shape. The name is
	// derived from the host so each Org's console route is distinct and
	// deterministic (catalyst-ui-<host-dashed>).
	ns := r.consoleRouteNamespace()
	name := "catalyst-ui-" + dnsDashed(hostname)

	labels := map[string]string{
		"openova.io/organization":           org.Spec.Slug,
		"openova.io/sovereign":              org.Spec.SovereignRef,
		"openova.io/managed-by":             "organization-controller",
		"app.kubernetes.io/managed-by":      "catalyst",
		"catalyst.openova.io/component":     "catalyst-ui",
		"catalyst.openova.io/parent-zone":   parentDomain,
		"catalyst.openova.io/org-subdomain": subdomain,
	}
	if p := strings.TrimSpace(tp.Product); p != "" {
		labels["catalyst.openova.io/tenant-product"] = p
	}

	apiSvc, apiPort := r.consoleAPIBackend()
	uiSvc, uiPort := r.consoleUIBackend()
	apiBackendRefs := []any{
		map[string]any{"name": apiSvc, "port": int64(apiPort)},
	}
	// catalyst-api rules: health probes, BOTH handover endpoints (the
	// mothership→Sovereign /auth/handover AND the marketplace→console
	// /auth/org-handover #4182), and the /api/ + /catalyst/ surfaces the
	// SPA's whoami + every authed fetch hit. The catch-all `/` →
	// catalyst-ui rule is LAST so the more-specific prefixes win.
	desiredSpec := map[string]any{
		"parentRefs": []any{
			map[string]any{
				"name":      r.consoleGatewayName(),
				"namespace": r.consoleGatewayNamespace(),
			},
		},
		"hostnames": []any{hostname},
		"rules": []any{
			map[string]any{
				"matches": []any{
					map[string]any{"path": map[string]any{"type": "Exact", "value": "/readyz"}},
					map[string]any{"path": map[string]any{"type": "Exact", "value": "/healthz"}},
				},
				"backendRefs": apiBackendRefs,
			},
			map[string]any{
				"matches": []any{
					map[string]any{"path": map[string]any{"type": "Exact", "value": "/auth/handover"}},
					map[string]any{"path": map[string]any{"type": "Exact", "value": "/auth/org-handover"}},
				},
				"backendRefs": apiBackendRefs,
			},
			map[string]any{
				"matches": []any{
					map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/api/"}},
				},
				"backendRefs": apiBackendRefs,
			},
			map[string]any{
				"matches": []any{
					map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/catalyst/"}},
				},
				"backendRefs": apiBackendRefs,
			},
			map[string]any{
				"matches": []any{
					map[string]any{"path": map[string]any{"type": "PathPrefix", "value": "/"}},
				},
				"backendRefs": []any{
					map[string]any{"name": uiSvc, "port": int64(uiPort)},
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

// tenantRouteNameNS computes the deterministic (namespace, name) of the
// per-Org console HTTPRoute the up-path renders, so the teardown targets
// exactly what reconcileTenantRoute created. Returns ok=false when the Org
// has no pool parentDomain (the feature was never engaged → nothing to
// remove). Kept in lockstep with the up-path derivation:
//
//	host = console.<subdomain>.<parentDomain>; name = catalyst-ui-<host-dashed>;
//	ns   = consoleRouteNamespace (catalyst-system).
func (r *Reconciler) tenantRouteNameNS(org *orgapi.Organization) (ns, name string, ok bool) {
	parentDomain := strings.TrimSpace(org.Spec.TenantPublic.ParentDomain)
	if parentDomain == "" {
		return "", "", false
	}
	subdomain := strings.TrimSpace(org.Spec.TenantPublic.Subdomain)
	if subdomain == "" {
		subdomain = org.Spec.Slug
	}
	hostname := fmt.Sprintf("console.%s.%s", subdomain, parentDomain)
	return r.consoleRouteNamespace(), "catalyst-ui-" + dnsDashed(hostname), true
}

// teardownTenantRoute deletes the per-Org console HTTPRoute on Org delete.
// The route lands in catalyst-system (NOT the Org's `<slug>` namespace), so
// neither the Flux `<slug>`-namespace prune nor a same-namespace ownerRef
// reaps it — it leaks unless explicitly removed (#4459). Returns (changed,
// err); changed=true iff a route was actually deleted. No-op when the Org
// had no pool parentDomain. Absent-as-success → idempotent on a re-run.
func (r *Reconciler) teardownTenantRoute(ctx context.Context, org *orgapi.Organization) (bool, error) {
	ns, name, ok := r.tenantRouteNameNS(org)
	if !ok {
		return false, nil
	}
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(httpRouteGVK)
	obj.SetNamespace(ns)
	obj.SetName(name)
	if err := r.Delete(ctx, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete HTTPRoute %s/%s: %w", ns, name, err)
	}
	return true, nil
}
