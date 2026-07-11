package gitops

import (
	"fmt"
	"sort"
	"strings"
)

// PerOrgAppsDir is the directory inside the per-Org `<slug>/catalyst-tenant`
// repo that the org-controller's per-Org apps Flux Kustomization
// (`catalyst-tenant-<slug>-apps`, path `./vcluster/apps`, targetNamespace
// `<slug>`) reconciles into the host `<slug>` namespace. The funnel/day-2 cart
// install (#4384) commits the customer's purchased Application manifests here
// so they land in the SAME boundary the org-controller already wired — instead
// of the legacy contabo per-tenant overlay tree in the global `openova/openova`
// repo, which no per-Org Kustomization watches on a Sovereign.
const PerOrgAppsDir = "vcluster/apps"

// PerOrgHostAppsDir is the ALWAYS-host-applied tree in the per-Org
// `<slug>/catalyst-tenant` repo that the org-controller's per-Org host-apps Flux
// Kustomization (`catalyst-tenant-<slug>-host-apps`, path `./vcluster/host-apps`,
// NO kubeConfig) reconciles straight onto the host `<slug>` namespace. The
// org-controller authors the reserved-entity CiliumNetworkPolicy + provisioning
// RBAC here; the funnel (#4993) adds the host-native per-app HTTPRoutes here so a
// vcluster-tier app's route reaches the host Cilium Gateway WITHOUT relying on
// the vcluster syncer (which registers no httproute reflecting controller in
// vcluster 0.33.4). This is the SAME host-native model the per-Org console route
// uses — never an in-vcluster route that has to sync out.
const PerOrgHostAppsDir = "vcluster/host-apps"

// perOrgAppsBaselineDocs are the boundary files the org-controller authored
// into `vcluster/apps/` on Org create (#4292 boundary set). The funnel MUST
// preserve them in the apps kustomization (they enforce intra-Org isolation),
// so MergePerOrgAppsKustomization keeps them in the resources list even though
// the funnel does not own/regenerate them.
//
// #4567: ONLY `networkpolicy.yaml` lives in `vcluster/apps/`. The Cilium
// `ciliumnetworkpolicy.yaml` lives in `vcluster/host-apps/` (NOT `apps/`) — the
// #4292 split deliberately keeps the CNP out of the apps tree because a
// `cilium.io/v2` doc in the kubeConfig-targeted `vcluster/apps` Kustomization
// wedges the vcluster-tier reconcile (no Cilium CRD inside the vcluster). The
// org-controller's own `vcluster/host-apps` Kustomization delivers the CNP.
// Listing it here made the funnel emit a `vcluster/apps/kustomization.yaml`
// that references a non-existent `vcluster/apps/ciliumnetworkpolicy.yaml` →
// `kustomize build failed: …: no such file or directory` → the ENTIRE apps
// Kustomization failed → the purchased app + its db dependency never deployed
// (host-tier S/free funnel Orgs). The CNP must NOT be re-listed in the apps
// tree by the funnel.
var perOrgAppsBaselineDocs = []string{
	"networkpolicy.yaml",
}

// perOrgHostAppsBaselineDocs are the boundary files the org-controller authored
// into `vcluster/host-apps/` on Org create (#4475 §1 CNP + #4991 provisioning
// RBAC). The funnel MUST preserve them when it merges its host-native app-route
// docs into the host-apps kustomization (#4993) — the CNP admits the Cilium
// Gateway `ingress` entity for the whole `<slug>` ns (without it every app 503s
// behind its route) and the RBAC unblocks the kubeconfig mirror. Force-included
// by MergePerOrgHostAppsKustomization exactly as networkpolicy.yaml is by the
// apps merge, so a controller reconcile that seeded the index (or a funnel merge
// that ran first) never drops them.
var perOrgHostAppsBaselineDocs = []string{
	"ciliumnetworkpolicy.yaml",
	"provisioning-rbac.yaml",
}

// GeneratePerOrgAppsTree renders the customer's purchased Applications for a
// Sovereign's per-Org `<slug>/catalyst-tenant` repo (#4384). It reuses the
// canonical generator (GenerateAllWithAppConfigs) and re-roots ONLY the app
// workload payload into `vcluster/apps/`:
//
//   - the in-tree workloads under `<basePath>/<slug>/apps/<file>` →
//     `vcluster/apps/<file>` (the Deployment-shaped apps + their db Secrets),
//     EXCLUDING `apps/namespace.yaml` and `apps/kustomization.yaml` — the
//     org-controller OWNS the boundary namespace + the apps kustomization, and
//     re-creating the `apps` ns here would collide with the targetNamespace
//     rewrite the org-controller's apps Kustomization applies.
//   - the host-scoped HelmRelease-shaped apps `<basePath>/<slug>/app-<x>.yaml`
//     (openclaw / stalwart-mail / any HR app) → `vcluster/apps/app-<x>.yaml`.
//     On the de-vcluster'd host-native Sovereign these install straight into
//     the host `<slug>` ns the apps Kustomization targets.
//
// The contabo-model host scaffolding (apps-sync.yaml, ingress.yaml,
// provisioning-rbac.yaml, the host kustomization.yaml) is dropped: the
// org-controller's per-Org Flux loop already provides the GitRepository +
// apps Kustomization that this tree feeds, so re-emitting an apps-sync
// Kustomization or a second namespace boundary would duplicate/fight it.
//
// Returns the re-rooted file map (paths relative to the per-Org repo root) and
// the sorted list of app file basenames the caller merges into
// `vcluster/apps/kustomization.yaml`.
func (g *ManifestGenerator) GeneratePerOrgAppsTree(slug, planSlug string, appSlugs []string, dbPassword string) (files map[string]string, appDocs []string) {
	raw := g.GenerateAllWithAppConfigs(slug, planSlug, appSlugs, dbPassword, nil)

	tenantDir := g.TenantDir(slug)     // e.g. clusters/<fqdn>/org-tenants/<slug>
	appsPrefix := tenantDir + "/apps/" // in-tree workload payload
	hostHRPrefix := tenantDir + "/"    // host-scoped HR app files live directly here

	out := make(map[string]string, len(raw))
	docSet := map[string]struct{}{}

	for path, content := range raw {
		switch {
		case strings.HasPrefix(path, appsPrefix):
			name := strings.TrimPrefix(path, appsPrefix)
			// The org-controller owns the boundary ns + the apps
			// kustomization index — never re-emit them from the funnel.
			if name == "namespace.yaml" || name == "kustomization.yaml" {
				continue
			}
			out[PerOrgAppsDir+"/"+name] = content
			docSet[name] = struct{}{}
		case isHostHelmReleaseAppFile(path, hostHRPrefix):
			name := strings.TrimPrefix(path, hostHRPrefix)
			out[PerOrgAppsDir+"/"+name] = content
			docSet[name] = struct{}{}
		default:
			// Drop the contabo-model host scaffolding (apps-sync, ingress,
			// provisioning-rbac, host kustomization) — the org-controller's
			// per-Org Flux loop supersedes it on a Sovereign.
		}
	}

	appDocs = make([]string, 0, len(docSet))
	for name := range docSet {
		appDocs = append(appDocs, name)
	}
	sort.Strings(appDocs)
	return out, appDocs
}

// GeneratePerOrgHostAppRoutes renders the HOST-NATIVE per-app HTTPRoutes for a
// VCLUSTER-tier Org (#4993) into the per-Org repo's `vcluster/host-apps/` tree.
// Each Deployment-shaped purchased app gets one route (`app-<x>-hostroute.yaml`)
// that binds the SYNCED Service (`<app>-x-<slug>-x-vcluster:80`) on the host
// `<slug>` ns to the app's public host (`<app>.<slug>.<parentDomain>`), parented
// to the dedicated console Cilium Gateway — so the route reaches the host gateway
// deterministically instead of via the (non-existent) vcluster httproute syncer.
//
// Emitted ONLY for the vcluster tier: the host tier (free/S) runs the app + its
// plain Service directly in the host `<slug>` ns, where the co-located
// generateAppHTTPRoute already routes (no synced-service name exists there), so
// this returns (nil, nil) for host-tier plans. HelmRelease-shaped apps (openclaw,
// stalwart-mail) carry their OWN chart-emitted HTTPRoute parented to the same
// gateway and sync no `<app>-x-…-x-vcluster` Service, so they are skipped — a
// second route to a non-existent Service would 404. Shareable DB slugs
// (mysql/postgres/redis) are never externally routed and are skipped too.
//
// Returns the file map (paths relative to the per-Org repo root) and the sorted
// list of route doc basenames the caller merges into
// `vcluster/host-apps/kustomization.yaml` (via MergePerOrgHostAppsKustomization).
func (g *ManifestGenerator) GeneratePerOrgHostAppRoutes(slug, planSlug string, appSlugs []string) (files map[string]string, hostAppDocs []string) {
	if !BoundaryIsVcluster(planSlug) {
		return nil, nil
	}
	hostNS := slug // the org-controller-owned boundary ns (apps targetNamespace)
	out := map[string]string{}
	docs := []string{}
	for _, a := range appSlugs {
		switch a {
		case "mysql", "postgres", "redis":
			continue // shareable DB backends — never externally routed
		}
		if isHelmReleaseApp(a) {
			continue // carries its own chart-emitted HTTPRoute; syncs no Service
		}
		name := fmt.Sprintf("app-%s-hostroute.yaml", a)
		out[PerOrgHostAppsDir+"/"+name] = generateHostNativeAppRoute(hostNS, slug, a, g.parentDomain())
		docs = append(docs, name)
	}
	if len(out) == 0 {
		return nil, nil
	}
	sort.Strings(docs)
	return out, docs
}

// MergePerOrgHostAppsKustomization rebuilds `vcluster/host-apps/kustomization.yaml`
// so it enumerates the org-controller host-apps baseline
// (ciliumnetworkpolicy.yaml + provisioning-rbac.yaml) AND the funnel's
// host-native app-route docs (#4993). Symmetric with MergePerOrgAppsKustomization:
// any resource already listed in `existing` is preserved (a second cart install
// is additive), the baseline is force-included, and the new hostAppDocs are
// unioned in. The result is deterministic (sorted) so a re-render is byte-stable
// and the commit short-circuits when nothing changed.
//
// The org-controller SEEDS this index once (create-if-absent, #4993) and never
// clobbers it thereafter — exactly like the apps kustomization — so the funnel's
// route entries survive every subsequent Org reconcile.
func MergePerOrgHostAppsKustomization(existing string, hostAppDocs []string) string {
	resources := map[string]struct{}{}
	for _, d := range perOrgHostAppsBaselineDocs {
		resources[d] = struct{}{}
	}
	for _, line := range strings.Split(existing, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if name == "" || name == "kustomization.yaml" {
			continue
		}
		resources[name] = struct{}{}
	}
	for _, d := range hostAppDocs {
		if d == "kustomization.yaml" {
			continue
		}
		resources[d] = struct{}{}
	}

	names := make([]string, 0, len(resources))
	for n := range resources {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("resources:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  - %s\n", n)
	}
	return b.String()
}

// isHostHelmReleaseAppFile reports whether `path` is a host-scoped
// HelmRelease-shaped app file (`<hostPrefix>app-<x>.yaml`) directly under the
// tenant dir — i.e. NOT nested under `apps/`. These are the bp-* HelmReleases
// the generator emits as host files (openclaw / stalwart-mail / cnpg-pair).
func isHostHelmReleaseAppFile(path, hostPrefix string) bool {
	if !strings.HasPrefix(path, hostPrefix) {
		return false
	}
	name := strings.TrimPrefix(path, hostPrefix)
	if strings.Contains(name, "/") {
		return false // nested (apps/...) — handled by the apps-prefix branch
	}
	return strings.HasPrefix(name, "app-") && strings.HasSuffix(name, ".yaml")
}

// MergePerOrgAppsKustomization rebuilds the `vcluster/apps/kustomization.yaml`
// resources list so it enumerates the org-controller boundary baseline
// (networkpolicy.yaml — the ONLY baseline that lives in `vcluster/apps/`) AND
// the funnel's app docs.
//
// `existing` is the current kustomization.yaml content read from the per-Org
// repo (empty if absent). Any resource already listed there is preserved
// (so a second cart install is additive, never destructive), the baseline docs
// are force-included, and the new appDocs are unioned in. The result is
// deterministic (sorted) so a re-render is byte-stable and the PutFile/commit
// short-circuits when nothing changed.
//
// #4567: `ciliumnetworkpolicy.yaml` is force-EXCLUDED here even if a prior
// (buggy) commit listed it in `existing` — the CNP file only ever exists in
// `vcluster/host-apps/`, so referencing it from the apps kustomization breaks
// the kustomize build for the WHOLE apps tree (→ purchased app never deploys).
// Stripping it on every merge self-heals an already-broken per-Org repo on the
// next cart install / reconcile.
func MergePerOrgAppsKustomization(existing string, appDocs []string) string {
	// excludedFromAppsTree are docs that must NEVER appear in
	// `vcluster/apps/kustomization.yaml` because their file does not live in
	// `vcluster/apps/` (the org-controller authors them under
	// `vcluster/host-apps/`). Listing one makes kustomize build fail with
	// "no such file or directory" and wedges the entire apps Kustomization.
	excludedFromAppsTree := map[string]struct{}{
		"ciliumnetworkpolicy.yaml": {},
	}

	resources := map[string]struct{}{}
	for _, d := range perOrgAppsBaselineDocs {
		resources[d] = struct{}{}
	}
	for _, line := range strings.Split(existing, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if name == "" || name == "kustomization.yaml" {
			continue
		}
		if _, bad := excludedFromAppsTree[name]; bad {
			continue // #4567: drop a stale CNP entry from a prior bad commit
		}
		resources[name] = struct{}{}
	}
	for _, d := range appDocs {
		if d == "kustomization.yaml" {
			continue
		}
		if _, bad := excludedFromAppsTree[d]; bad {
			continue
		}
		resources[d] = struct{}{}
	}

	names := make([]string, 0, len(resources))
	for n := range resources {
		names = append(names, n)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("apiVersion: kustomize.config.k8s.io/v1beta1\n")
	b.WriteString("kind: Kustomization\n")
	b.WriteString("resources:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  - %s\n", n)
	}
	return b.String()
}
