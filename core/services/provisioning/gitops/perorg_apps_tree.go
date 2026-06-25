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

// perOrgAppsBaselineDocs are the boundary files the org-controller authored
// into `vcluster/apps/` on Org create (#4292 boundary set). The funnel MUST
// preserve them in the apps kustomization (they enforce intra-Org isolation),
// so MergePerOrgAppsKustomization keeps them in the resources list even though
// the funnel does not own/regenerate them.
var perOrgAppsBaselineDocs = []string{
	"networkpolicy.yaml",
	"ciliumnetworkpolicy.yaml",
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
// resources list so it enumerates BOTH the org-controller boundary baseline
// (networkpolicy.yaml + ciliumnetworkpolicy.yaml) AND the funnel's app docs.
//
// `existing` is the current kustomization.yaml content read from the per-Org
// repo (empty if absent). Any resource already listed there is preserved
// (so a second cart install is additive, never destructive), the baseline docs
// are force-included, and the new appDocs are unioned in. The result is
// deterministic (sorted) so a re-render is byte-stable and the PutFile/commit
// short-circuits when nothing changed.
func MergePerOrgAppsKustomization(existing string, appDocs []string) string {
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
		resources[name] = struct{}{}
	}
	for _, d := range appDocs {
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
