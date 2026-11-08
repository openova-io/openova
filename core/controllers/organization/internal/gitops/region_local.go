package gitops

import "strings"

// region_local.go — ADR-0015 D1 follow-up (Refs #7067): the two pieces of the
// per-Org `catalyst-tenant` repo that let EVERY region of a Sovereign run the
// Org from its own Flux, without a cross-region pivot and without a split
// brain.
//
// 1. A REGION-LOCAL KUBECONFIG PRODUCER
//
//    The per-Org `-apps` Kustomization (flux-system) and every per-region
//    Application HelmRelease (`<slug>` ns) reach the Org's vCluster through
//    `tenant-<slug>-kubeconfig`. Before this file the only producer was the
//    provisioning funnel's mirrorVClusterKubeconfig — an imperative copy run
//    by org-services/provisioning, which runs in region A only. Region B had
//    the vCluster (the `-vcluster` tree is applied everywhere) but never the
//    kubeconfig, so nothing could be delivered into it.
//
//    Each region's vCluster already exports its own kubeconfig: the loft-sh
//    chart writes `vc-vcluster` into `<slug>` (exportKubeConfig.server is the
//    in-cluster `https://vcluster.<slug>:443`, so the bytes are valid in every
//    region for that region's own vCluster). The tree below lets the
//    platform's own cross-namespace copier, bp-reflector, mirror THAT Secret
//    under the two names consumers read:
//
//      <slug>/vc-vcluster                  annotations only (source opt-in)
//      <slug>/tenant-<slug>-kubeconfig     reflects <slug>/vc-vcluster
//      flux-system/tenant-<slug>-kubeconfig reflects <slug>/vc-vcluster
//
//    All three are applied by the region's own `-vcluster` Kustomization.
//    Flux applies them WITHOUT `data`, so server-side apply owns only the
//    metadata: the vCluster syncer keeps writing `vc-vcluster`'s data, and
//    reflector keeps writing the mirrors' data. Same stub idiom as
//    platform/powerdns-admin/chart/templates/pdns-api-credentials-mirror.yaml.
//    The source opt-in names exactly the two namespaces above, so no other
//    namespace can pull an Organization's kubeconfig.
//
//    Not done with exportKubeConfig.additionalSecrets[].namespace=flux-system:
//    vcluster 0.33 then adds flux-system to its manager cache
//    (pkg/setup/controller_context.go getLocalCacheOptions), which needs list/
//    watch on every synced kind in flux-system — every Organization's vCluster
//    could read every other Organization's kubeconfig. Rejected.
//
// 2. REGION BASELINE TREES (no split brain)
//
//    `vcluster/apps/` and `vcluster/host-apps/` are SHARED with the funnel:
//    it commits the customer's placement-less installs there (app-*.yaml,
//    db-*.yaml, app-*-hostroute.yaml). Those files name no region. Applied by
//    two regions' Flux they would run twice — two databases, and a global
//    route splitting traffic between them. So only the Organization's home
//    region (per_org_flux.go perOrgHomeRegion) reconciles those two shared
//    trees. Every other region reconciles the controller-owned BASELINE copy
//    below — the namespace, NetworkPolicy and storage quota inside the
//    vCluster, and the CNP + provisioning RBAC on the host — and receives its
//    Applications only from `regions/<region-id>/apps/`, the placement-bound
//    folder the application-controller writes (ADR-0015 D1/D3).
//
//    The baseline trees are written by this controller only and are never
//    merged with funnel entries (MergeAppsKustomizationIndex targets the
//    shared index paths only).

// RegionBaselineAppsDir / RegionBaselineHostAppsDir are the controller-owned
// copies of the vCluster-internal and host-side baselines that non-home
// regions reconcile in place of the shared, funnel-written trees.
const (
	RegionBaselineAppsDir     = "vcluster/region-baseline/apps"
	RegionBaselineHostAppsDir = "vcluster/region-baseline/host-apps"
)

// SharedAppsDir / SharedHostAppsDir are the funnel-shared trees. They carry
// placement-less installs and are reconciled by the home region ONLY.
const (
	SharedAppsDir     = "vcluster/apps"
	SharedHostAppsDir = "vcluster/host-apps"
)

// kubeconfigMirrorDoc is the boundary-tree file holding the three Secret
// stubs of the region-local kubeconfig producer.
const kubeconfigMirrorDoc = "kubeconfig-mirror.yaml"

// VClusterKubeconfigSourceSecret is the Secret the loft-sh vCluster chart
// exports its kubeconfig into (`vc-<release>`, release `vcluster`).
const VClusterKubeconfigSourceSecret = "vc-vcluster"

// kubeconfigMirrorTemplate — see the file header (1). No `data:` on any of
// the three stubs: that is what keeps Flux from ever owning (or blanking)
// the kubeconfig bytes.
const kubeconfigMirrorTemplate = `# Region-local producer of the Organization's vCluster kubeconfig
# (ADR-0015 D1, Refs #7067). Applied by THIS region's own Flux; the bytes are
# THIS region's vCluster's own export. Flux owns metadata only.
apiVersion: v1
kind: Secret
metadata:
  name: vc-vcluster
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    catalyst.openova.io/component: per-org-kubeconfig-source
  annotations:
    reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
    reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "{{ .Slug }},flux-system"
type: Opaque
---
apiVersion: v1
kind: Secret
metadata:
  name: tenant-{{ .Slug }}-kubeconfig
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    catalyst.openova.io/component: per-org-kubeconfig-mirror
  annotations:
    reflector.v1.k8s.emberstack.com/reflects: "{{ .Slug }}/vc-vcluster"
type: Opaque
---
apiVersion: v1
kind: Secret
metadata:
  name: tenant-{{ .Slug }}-kubeconfig
  namespace: flux-system
  labels:
    openova.io/organization: {{ .Slug }}
    catalyst.openova.io/component: per-org-kubeconfig-mirror
  annotations:
    reflector.v1.k8s.emberstack.com/reflects: "{{ .Slug }}/vc-vcluster"
type: Opaque
`

// addRegionBaselineTrees copies every controller-owned file of the shared
// apps / host-apps trees (their kustomization index included, which at this
// point lists the baseline only) into the region-baseline trees. Called by
// Render after both shared trees are assembled, so the baseline can never
// drift from what the home region applies.
func addRegionBaselineTrees(files map[string]string) {
	for path, tmpl := range copyTree(files, SharedAppsDir+"/", RegionBaselineAppsDir+"/") {
		files[path] = tmpl
	}
	for path, tmpl := range copyTree(files, SharedHostAppsDir+"/", RegionBaselineHostAppsDir+"/") {
		files[path] = tmpl
	}
}

func copyTree(files map[string]string, from, to string) map[string]string {
	out := map[string]string{}
	for path, tmpl := range files {
		rest, ok := strings.CutPrefix(path, from)
		if !ok || rest == "" || strings.Contains(rest, "/") {
			continue
		}
		out[to+rest] = tmpl
	}
	return out
}
