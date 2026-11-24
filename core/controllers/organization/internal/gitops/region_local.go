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
//    provisioning funnel's kubeconfig copy (now deleted) — an imperative copy run
//    by org-services/provisioning, which runs in region A only. Region B had
//    the vCluster (the `-vcluster` tree is applied everywhere) but never the
//    kubeconfig, so nothing could be delivered into it.
//
//    Each region's vCluster exports its own kubeconfig (exportKubeConfig.server
//    is the in-cluster `https://vcluster.<slug>:443`, so the bytes are valid in
//    every region for that region's own vCluster). It always writes its
//    default `vc-vcluster`, and it writes `tenant-<slug>-kubeconfig` in
//    `<slug>` too, because the vcluster HelmRelease lists that name under
//    exportKubeConfig.additionalSecrets (manifests.go). Both come from one
//    call (loft-sh vcluster 0.33.4 pkg/setup WriteKubeConfigToSecret) with
//    the same properties, so they hold the same bytes. The flux-system copy
//    is made by the platform's cross-namespace copier, bp-reflector:
//
//      <slug>/tenant-<slug>-kubeconfig      written by the vCluster; the
//                                           SOURCE, auto-reflected into
//                                           flux-system only
//      flux-system/tenant-<slug>-kubeconfig reflects the source above
//      <slug>/vc-vcluster                   written by the vCluster; reflected
//                                           nowhere (stub kept so an upgrade
//                                           never prunes it)
//
//    The scheme is SOURCE-DRIVEN on purpose. The earlier tree had the two
//    mirrors name `vc-vcluster` (`reflects`). Reflector 7.1.288 records that
//    binding only if the source exists when it handles the MIRROR's event
//    (ResourceMirror.cs HandleUpsert: a missing source logs "could not be
//    found" and returns before `_directReflectionCache` is filled). Flux
//    creates the mirrors in the same second as the source request, while the
//    vCluster writes its kubeconfig about 40 s later. So the source's events
//    found no binding, and the mirrors stayed empty until the reflector's
//    Secret watch session ended and it listed again (51 min on the
//    2026-11-21 gate run, 22 min on an earlier one). A source with
//    reflection-auto-enabled is handled on EVERY Added/Modified event of the
//    SOURCE itself (HandleUpsert → AutoReflectionForSource): the reflector
//    lists the Secrets of that name and fills each permitted copy that is
//    missing or stale. No binding cache is involved, so the copy holds the
//    kubeconfig as soon as the reflector has handled the source's own event,
//    in any order of creation.
//
//    All three are applied by the region's own `-vcluster` Kustomization.
//    Flux applies them WITHOUT `data`, so server-side apply owns only the
//    metadata: the vCluster writes the data of its two Secrets (CreateOrPatch
//    keeps the annotations), and reflector writes the data of the
//    flux-system copy. Same stub idiom as
//    platform/powerdns-admin/chart/templates/pdns-api-credentials-mirror.yaml.
//    The source opt-in names exactly flux-system, both for what may reflect it
//    and for where it is auto-created. An empty list would mean every
//    namespace to the reflector, so no other namespace can pull an
//    Organization's kubeconfig. Auto-reflections keep the source's name, so
//    the flux-system copy of each Organization is unique by name.
//
//    One writer per Secret: the provisioning funnel no longer copies
//    `vc-vcluster` into the two names. Its full-object PUT would strip the
//    source's opt-in, and the reflector would then delete the flux-system
//    copy. The funnel now only waits until both hold the kubeconfig
//    (core/services/provisioning handlers.waitForVClusterKubeconfig).
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

// VClusterDefaultKubeconfigSecret is the Secret the loft-sh vCluster always
// writes its kubeconfig into (`vc-<release>`, release `vcluster`). It is not
// reflected anywhere; KubeconfigSecretName is the source.
const VClusterDefaultKubeconfigSecret = "vc-vcluster"

// KubeconfigSecretName is the Organization's vCluster kubeconfig Secret: the
// vCluster writes it in `<slug>` (exportKubeConfig.additionalSecrets) and
// bp-reflector auto-reflects it into flux-system under the same name.
func KubeconfigSecretName(slug string) string {
	return "tenant-" + slug + "-kubeconfig"
}

// kubeconfigMirrorTemplate — see the file header (1). No `data:` on any of
// the three stubs: that is what keeps Flux from ever owning (or blanking)
// the kubeconfig bytes.
const kubeconfigMirrorTemplate = `# Region-local producer of the Organization's vCluster kubeconfig
# (ADR-0015 D1, Refs #7067). Applied by THIS region's own Flux; the bytes are
# THIS region's vCluster's own export. Flux owns metadata only.
#
# The source: the vCluster writes this Secret itself
# (exportKubeConfig.additionalSecrets). Reflector auto-reflects it into
# flux-system on every event of this Secret, so the copy is filled when the
# kubeconfig appears, not at the reflector's next watch session.
apiVersion: v1
kind: Secret
metadata:
  name: tenant-{{ .Slug }}-kubeconfig
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    catalyst.openova.io/component: per-org-kubeconfig-source
  annotations:
    reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
    reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "flux-system"
    reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"
    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "flux-system"
type: Opaque
---
# The copy the per-Org Flux Kustomizations read (spec.kubeConfig.secretRef
# has no namespace). Reflector writes its data.
apiVersion: v1
kind: Secret
metadata:
  name: tenant-{{ .Slug }}-kubeconfig
  namespace: flux-system
  labels:
    openova.io/organization: {{ .Slug }}
    catalyst.openova.io/component: per-org-kubeconfig-mirror
  annotations:
    reflector.v1.k8s.emberstack.com/reflects: "{{ .Slug }}/tenant-{{ .Slug }}-kubeconfig"
type: Opaque
---
# The vCluster's default export. Reflected nowhere; kept in this tree so
# that Flux never prunes it from an Organization created before the
# source-driven scheme (the vCluster writes it only when it starts).
apiVersion: v1
kind: Secret
metadata:
  name: vc-vcluster
  namespace: {{ .Slug }}
  labels:
    openova.io/organization: {{ .Slug }}
    catalyst.openova.io/component: per-org-kubeconfig-default
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
