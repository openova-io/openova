// org_applications.go — the Org-scoped Application install path (#4116).
//
// THE PROBLEM this closes: a customer Org-scoped session (a User on their
// own console.<org>.<pool> console, tier=org-admin per org_scope.go) cannot
// install an Application via the Sovereign-admin seam
// `POST /api/v1/sovereigns/{id}/applications` (HandleApplicationInstall),
// because:
//
//   1. OrgScopeGuard (org_scope.go) deny-by-defaults the
//      `/api/v1/sovereigns/...` surface for org sessions (the #4110/#4112
//      privilege-escalation fix) → 403 org-scoped-forbidden before the
//      handler runs.
//   2. Even if it reached the handler, applicationInstallCallerAuthorized
//      rejects tier=org-admin (it wants tier-admin/owner/sovereign-admin).
//   3. orgNamespace(ref) is a pure slugger and never consults the tenant
//      registry — a free-subdomain Org (e.g. slug "demo", real namespace
//      `org-<tenant-uuid>`) would land the CR in the wrong namespace.
//
// This is exactly what blocked the agentic journey: the bp-agenity solo
// agent's openova-mcp create_application tool forwards the User's Org-scoped
// session bearer, which got 403'd at the Sovereign seam.
//
// THE FIX (Approach A — respects the #4110/#4112 model, no surface widening):
// a NEW Org-scoped route `POST /api/v1/org/applications` that is ALREADY on
// the OrgScopeGuard allowlist (orgSafePathPrefixes in org_scope.go). It:
//
//   - resolves the caller's OWN Organization via resolveOrganization() — the
//     same host→tenant-registry seam HandleCreateOrgUser uses, which returns
//     tenant.OrganizationNamespace (the real `org-<uuid>` namespace, solving
//     problem 3) AND enforces the #4110 cross-org binding (an Org-scoped
//     session can only ever resolve its OWN Org, solving the confinement
//     requirement),
//   - forces body.OrganizationRef to that real namespace so the shared
//     install core writes the Application CR into the caller's own namespace,
//   - reuses h.installApplicationCore — the SAME catalog-fetch → validate →
//     ensure-namespace → create-CR core the Sovereign path uses (no
//     duplicated CR-write logic),
//   - applies NO applicationInstallCallerAuthorized tier gate: reaching this
//     route already proves an Org-scoped session confined to its own Org by
//     resolveOrganization's binding — that IS the authz boundary.
//
// The Sovereign surface stays sealed: `/api/v1/sovereigns/{id}/applications`
// is unchanged and still 403s org sessions. Only the dedicated
// `/api/v1/org/applications` own-org route is opened.

package handler

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/catalog"
)

// perOrgAppRouteComponents — the `catalyst.openova.io/component` label values
// the funnel stamps on the HOST-visible per-app HTTPRoute of a
// Deployment-shaped purchased app (core/services/provisioning/gitops):
//   - `per-org-app-route`     — host tier (free/S): generateAppHTTPRoute
//     co-locates the route with the Deployment+Service in the host `<slug>` ns.
//   - `per-org-app-hostroute` — vcluster tier (paid M+): #4993
//     generateHostNativeAppRoute lands the route host-side in the `<slug>` ns
//     (the Deployment itself lives INSIDE the Org vcluster, invisible to the
//     host client).
//
// #5123 — these routes are the ONLY host-visible per-app artifact of a funnel
// purchase: the purchase deploys NO Application CR and NO HelmRelease (the
// provisioning consumer commits raw Deployment-shaped manifests into the
// per-Org repo's `vcluster/apps` tree, reconciled by the org-controller's
// `catalyst-tenant-<slug>-apps` Flux Kustomization). The Org-scoped Apps
// projection joins on this label to surface each purchase as its own card.
var perOrgAppRouteComponents = map[string]bool{
	"per-org-app-route":     true,
	"per-org-app-hostroute": true,
}

// HandleOrgApplicationInstall — POST /api/v1/org/applications.
//
// Installs an Application into the CALLER'S OWN Organization. The target
// namespace is resolved server-side from the request host (X-Tenant-Host)
// via the tenant registry — the client cannot point this at another Org
// (resolveOrganization enforces the #4110 own-org binding). The body is the
// SAME applicationInstallRequest shape the Sovereign install seam accepts
// (long-form or short-form), minus organizationRef/namespace, which are
// forced to the resolved Org namespace and IGNORED if supplied.
func (h *Handler) HandleOrgApplicationInstall(w http.ResponseWriter, r *http.Request) {
	// Resolve the caller's own Organization (host → tenant registry). This
	// returns tenant.OrganizationNamespace (the real `org-<uuid>` namespace)
	// and enforces the #4110 cross-org binding: an Org-scoped session may
	// only ever resolve its OWN Org (a forged X-Tenant-Host for a sibling
	// Org is 403 org-scope-mismatch).
	tenant, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}
	orgNS := strings.TrimSpace(tenant.OrganizationNamespace)
	if orgNS == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "org-namespace-unresolved",
			"detail": "tenant registry has no org_tenant_namespace for this Organization",
		})
		return
	}

	// Resolve the local Sovereign deployment. On a Sovereign chroot, ANY
	// non-empty id resolves lookupDeploymentForInfra → chrootEnsureDeployment
	// → the in-cluster client (sovereignDynamicClient), so the synthesized
	// self id is sufficient. This mirrors HandleSovereignSelf's id-resolution
	// ladder so the org path uses the same single self-registered cluster.
	depID := h.selfDeploymentID(r)
	dep, found := h.lookupDeploymentForInfra(depID)
	if !found {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "deployment-unresolved",
			"detail": "could not resolve the local Sovereign deployment for an org-scoped install",
		})
		return
	}

	// Decode + normalize + validate the body, identical to the Sovereign
	// path, so the same dual-shape (long/short-form) contract holds.
	rawBody, readErr := readMutationBody(w, r)
	if readErr {
		return
	}
	body, decodeErr := decodeApplicationInstallBody(rawBody)
	if decodeErr != nil {
		writeApplicationInstallSoftError(w, "invalid-body", http.StatusBadRequest, decodeErr.Error())
		return
	}

	// FORCE the target Org to the caller's own resolved namespace — the
	// client's organizationRef / namespace are IGNORED so an org session can
	// never create outside its own Org. orgNamespace() is idempotent on an
	// already-valid RFC-1123 label (the `org-<uuid>` namespace is one), so
	// the shared core's orgNamespace(body.OrganizationRef) == orgNS.
	body.OrganizationRef = orgNS
	body.NamespaceShort = ""

	body = applicationInstallRequestNormalize(body)
	if msg, ok := validateApplicationInstallRequest(body); !ok {
		writeApplicationInstallSoftError(w, "invalid-application-install", http.StatusBadRequest, msg)
		return
	}

	// No applicationInstallCallerAuthorized tier gate: reaching this
	// OrgScopeGuard-allowlisted, own-org-bound route IS the authz boundary
	// for an Org-scoped session (it can only ever touch its own Org).
	h.installApplicationCore(w, r, dep, depID, body)
}

// selfDeploymentID resolves the local Sovereign's deployment id for an
// org-scoped request, mirroring HandleSovereignSelf's ladder: the explicit
// self-id env, then the session cookie claims, then a stable id synthesized
// from SOVEREIGN_FQDN. Any non-empty value resolves to the in-cluster client
// on a Sovereign chroot.
func (h *Handler) selfDeploymentID(r *http.Request) string {
	if id := strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID")); id != "" {
		return id
	}
	if _, jwtDepID, ok := readSessionClaimsFromCookie(r); ok && strings.TrimSpace(jwtDepID) != "" {
		return strings.TrimSpace(jwtDepID)
	}
	fqdn := strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	if fqdn == "" {
		fqdn = strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	}
	if fqdn != "" {
		return "sovereign-" + fqdn
	}
	return ""
}

// HandleOrgApplications — GET /api/v1/org/applications.
//
// #4113 — the TRUE per-Org Apps projection. An Org-scoped customer session
// on its own console (console.<org>.<pool>) used to render the SOVEREIGN-WIDE
// catalog grid because the only allowlisted Apps feed was
// /api/v1/sovereign/apps (HandleSovereignApps lists every HelmRelease +
// Application CR + catalog blueprint cluster-wide). A customer must see ONLY
// their OWN estate.
//
// This handler resolves the caller's OWN Organization namespace from the
// request host (resolveOrganization → tenant.OrganizationNamespace, the real
// `org-<uuid>` namespace, with the #4110 own-org binding enforced) and lists
// ONLY the Application CRs + HelmReleases that live in that namespace. Each
// row is projected as an instance card with its open/launch URL derived from
// the Org namespace's HTTPRoute set (the host of the route fronting the
// release). agenity-demo (HR Ready, HTTPRoute host agenity.<org>.<pool>) thus
// surfaces with a working Open link; the demo-wp Application CRs surface as
// their own cards even while still Pending.
//
// Response shape is the SAME sovereignAppsResponse the FE's AppsPage already
// consumes (instance rows), so the SPA reuses its existing card-rendering and
// silent-SSO Open path with zero new wire-types — it only swaps the endpoint
// when whoami.orgScoped is true.
func (h *Handler) HandleOrgApplications(w http.ResponseWriter, r *http.Request) {
	// Resolve the caller's own Organization (host → tenant registry). Enforces
	// the #4110 own-org binding: an Org session can only ever resolve its OWN
	// Org (a forged sibling host is 403 org-scope-mismatch). Returns the real
	// `org-<uuid>` namespace.
	tenant, ok := h.resolveOrganization(w, r)
	if !ok {
		return
	}
	orgNS := strings.TrimSpace(tenant.OrganizationNamespace)
	if orgNS == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error":  "org-namespace-unresolved",
			"detail": "tenant registry has no org_tenant_namespace for this Organization",
		})
		return
	}

	resp := sovereignAppsResponse{Apps: []sovereignAppItem{}, BootstrapKit: []string{}}

	deps, depsErr := h.sovereignDepsFor()
	if depsErr != nil {
		// No in-cluster client (e.g. CI) — return an empty-but-valid estate
		// rather than a 500, mirroring HandleSovereignApps' best-effort
		// posture. The customer sees the empty-state affordance, not an error.
		if gen, gErr := catalog.GeneratedAt(); gErr == nil {
			resp.GeneratedAt = gen
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Build the per-Org HTTPRoute index: (name → url) and (backend-service
	// name → url), all within the Org namespace. The agenity route is named
	// `agenity` but its backendRef Service is `agenity-demo-bp-agenity`, so we
	// index BOTH so the release→route join below resolves whichever key the
	// chart used.
	urlByKey := map[string]string{}
	// funnelApps — app slug → open URL for every funnel-purchased
	// Deployment-shaped app, joined from its per-org-app HTTPRoute (#5123, the
	// only host-visible per-app artifact of a funnel purchase — see
	// perOrgAppRouteComponents). funnelTenantSlug is the Org slug the funnel
	// stamped on those routes (`openova.io/tenant`), used to resolve the
	// purchase-delivering `catalyst-tenant-<slug>-apps` Kustomization.
	funnelApps := map[string]string{}
	funnelTenantSlug := ""
	if rts, err := deps.dyn.Resource(httpRouteGVR).Namespace(orgNS).List(ctx, metav1.ListOptions{}); err == nil {
		for _, rt := range rts.Items {
			hosts, _, _ := unstructured.NestedStringSlice(rt.Object, "spec", "hostnames")
			if len(hosts) == 0 || hosts[0] == "" {
				continue
			}
			url := "https://" + hosts[0]
			urlByKey[rt.GetName()] = url
			if labels := rt.GetLabels(); perOrgAppRouteComponents[labels["catalyst.openova.io/component"]] {
				slug := strings.TrimSpace(labels["app"])
				if slug == "" {
					// Route names are `app-<slug>` (host tier) or
					// `app-<slug>-hostroute` (vcluster tier).
					slug = strings.TrimSuffix(strings.TrimPrefix(rt.GetName(), "app-"), "-hostroute")
				}
				if slug != "" {
					if _, exists := funnelApps[slug]; !exists {
						funnelApps[slug] = url
					}
					if funnelTenantSlug == "" {
						funnelTenantSlug = strings.TrimSpace(labels["openova.io/tenant"])
					}
				}
			}
			rules, _, _ := unstructured.NestedSlice(rt.Object, "spec", "rules")
			for _, rl := range rules {
				rm, isMap := rl.(map[string]interface{})
				if !isMap {
					continue
				}
				refs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
				for _, ref := range refs {
					rfm, isMap := ref.(map[string]interface{})
					if !isMap {
						continue
					}
					if svc, _, _ := unstructured.NestedString(rfm, "name"); svc != "" {
						if _, exists := urlByKey[svc]; !exists {
							urlByKey[svc] = url
						}
					}
				}
			}
		}
	}

	// Index the Org namespace's HelmReleases by name so an Application CR's
	// spec.helmRelease.name resolves to ready-state + a launch URL, and so an
	// HR WITHOUT a companion Application CR (e.g. agenity-demo on the demo Org)
	// still projects as its own card.
	type orgHR struct {
		ready bool
		// urlKeys — candidate HTTPRoute index keys to resolve the open URL:
		// the release name and `<release>-<chart>` (the Service-name shape the
		// agenity route's backendRef uses). First hit in urlByKey wins.
		urlKeys []string
		chart   string
	}
	hrByName := map[string]orgHR{}
	if hrs, err := deps.dyn.Resource(helmReleaseGVR).Namespace(orgNS).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range hrs.Items {
			hr := &hrs.Items[i]
			name := hr.GetName()
			rel, _, _ := unstructured.NestedString(hr.Object, "spec", "releaseName")
			if rel == "" {
				rel = name
			}
			chart, _, _ := unstructured.NestedString(hr.Object, "spec", "chart", "spec", "chart")
			if chart == "" {
				chart = strings.TrimPrefix(name, "bp-")
			}
			hrByName[name] = orgHR{
				ready:   helmReleaseReady(hr),
				urlKeys: []string{rel, rel + "-" + chart, rel + "-bp-" + strings.TrimPrefix(chart, "bp-"), name},
				chart:   chart,
			}
		}
	}
	resolveOpenURL := func(keys []string) string {
		for _, k := range keys {
			if u, ok := urlByKey[k]; ok && u != "" {
				return u
			}
		}
		return ""
	}

	rows := []sovereignAppItem{}
	adopted := map[string]bool{} // HR names already projected via an Application CR

	// Application CR pass — each CR in the Org namespace is one card.
	if appList, err := deps.dyn.Resource(applicationGVR).Namespace(orgNS).List(ctx, metav1.ListOptions{}); err == nil {
		for i := range appList.Items {
			app := &appList.Items[i]
			bpFull, _, _ := unstructured.NestedString(app.Object, "spec", "blueprintRef", "name")
			slug := strings.TrimPrefix(bpFull, "bp-")
			phase, _, _ := unstructured.NestedString(app.Object, "status", "phase")
			status := "installing"
			if strings.EqualFold(phase, "Ready") {
				status = "installed"
			}
			env, _, _ := unstructured.NestedString(app.Object, "spec", "environmentRef")
			if env == "" {
				env = defaultSovereignEnvironment
			}
			externalURL := ""
			if hrName, _, _ := unstructured.NestedString(app.Object, "spec", "helmRelease", "name"); hrName != "" {
				adopted[hrName] = true
				if hr, ok := hrByName[hrName]; ok {
					externalURL = resolveOpenURL(hr.urlKeys)
				}
			}
			// #4897 — spec.placement is dual-form (legacy posture STRING or the
			// object {mode, regions, …}). readTopology reads .mode for the
			// object form, so an active-hot-standby instance created via
			// Catalog→New-instance renders its topology badge in the Org-scoped
			// Apps grid too. A raw NestedString read collapsed the object to
			// "" → the sov-app-topology-<name> badge was absent.
			topology := readTopology(app)
			rows = append(rows, sovereignAppItem{
				ID:          app.GetName(),
				Slug:        slug,
				Title:       app.GetName(),
				Status:      status,
				Environment: env,
				ExternalURL: externalURL,
				Instance:    true,
				Blueprint:   bpFull,
				Topology:    topology,
			})
		}
	}

	// HelmRelease pass — any HR in the Org namespace WITHOUT a companion
	// Application CR (e.g. agenity-demo) projects as its own card. Internal
	// platform spines for the Org's own vCluster (vc-*, bp-cnpg) carry no
	// customer-facing UI; we still surface them as cards (the customer owns
	// their estate) but only attach an Open link when an HTTPRoute resolves.
	hrNames := make([]string, 0, len(hrByName))
	for name := range hrByName {
		hrNames = append(hrNames, name)
	}
	sort.Strings(hrNames)
	for _, name := range hrNames {
		if adopted[name] {
			continue
		}
		hr := hrByName[name]
		status := "installing"
		if hr.ready {
			status = "installed"
		}
		slug := strings.TrimPrefix(hr.chart, "bp-")
		bpFull := hr.chart
		if !strings.HasPrefix(bpFull, "bp-") {
			bpFull = "bp-" + bpFull
		}
		rows = append(rows, sovereignAppItem{
			ID:          name,
			Slug:        slug,
			Title:       name,
			Status:      status,
			Environment: defaultSovereignEnvironment,
			ExternalURL: resolveOpenURL(hr.urlKeys),
			Instance:    true,
			Blueprint:   bpFull,
		})
	}

	// Funnel-purchase pass (#5123) — every per-org-app HTTPRoute in the Org
	// namespace is one purchased Application (see perOrgAppRouteComponents: a
	// funnel purchase deploys NO Application CR and NO HelmRelease, so neither
	// pass above can see it — hw256 acme256 rendered "0 apps" while the
	// purchased WordPress served 200 at its route host). Status is read from
	// the org-controller's `catalyst-tenant-<slug>-apps` Flux Kustomization
	// (flux-system) — the delivery unit that reconciles the purchase's
	// manifests into the Org boundary: Ready=True → installed, anything else
	// (including not-yet-created) → installing, never fabricated. Cards
	// already projected via an Application CR or HelmRelease of the same id
	// are skipped — this pass only surfaces what the others cannot see.
	if len(funnelApps) > 0 {
		seen := map[string]bool{}
		for i := range rows {
			seen[rows[i].ID] = true
		}
		if funnelTenantSlug == "" {
			// Funnel Orgs register OrganizationNamespace = the boundary slug
			// namespace (tenantRegistrationFromOrgCR), so orgNS is the slug.
			funnelTenantSlug = orgNS
		}
		status := "installing"
		appsKSName := "catalyst-tenant-" + funnelTenantSlug + "-apps"
		if ks, err := deps.dyn.Resource(fluxKustomizationGVR).Namespace(fluxSystemNamespace).
			Get(ctx, appsKSName, metav1.GetOptions{}); err == nil && helmReleaseReady(ks) {
			// helmReleaseReady is a generic status.conditions[type=Ready]
			// reader — it works on any Flux object, Kustomizations included.
			status = "installed"
		}
		slugs := make([]string, 0, len(funnelApps))
		for slug := range funnelApps {
			slugs = append(slugs, slug)
		}
		sort.Strings(slugs)
		for _, slug := range slugs {
			if seen[slug] {
				continue
			}
			rows = append(rows, sovereignAppItem{
				ID:          slug,
				Slug:        slug,
				Title:       slug,
				Status:      status,
				Environment: defaultSovereignEnvironment,
				ExternalURL: funnelApps[slug],
				Instance:    true,
				Blueprint:   "bp-" + slug,
			})
		}
	}

	resp.Apps = rows
	if gen, gErr := catalog.GeneratedAt(); gErr == nil {
		resp.GeneratedAt = gen
	}
	writeJSON(w, http.StatusOK, resp)
}
