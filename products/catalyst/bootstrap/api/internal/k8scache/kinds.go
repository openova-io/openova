// kinds.go — registry of GroupVersionResource (GVR) values that the
// Catalyst-Zero data plane watches on every managed Sovereign cluster.
//
// Per ADR-0001 §5 the kube-apiserver is the system of record for live
// cluster state. Every watched GVR participates in the read path:
//
//	kube-apiserver → SharedInformerFactory → Indexer → SSE → browser
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode) the default
// registry below is the FALLBACK; the runtime registry is loaded from
// a ConfigMap at factory boot (see factory.go BootKinds). Operators
// extend the watch surface by editing the ConfigMap — no code change.
//
// Two security invariants:
//
//  1. Secret data + stringData fields are NEVER stored in the
//     Indexer's cached object. Even though the watch surfaces Secret
//     metadata so the UI can render existence + age + labels, the
//     snapshot path strips the data block before serialisation. See
//     redactSecretData in snapshot.go for the enforcement point.
//  2. ConfigMap data is similarly stripped on the cache write path so
//     a sensitive value mistakenly stored in a ConfigMap is not
//     leaked through the SSE stream. The UI requests the full body
//     via a separate authenticated GET (with SAR gating) when an
//     operator opens the detail panel.
package k8scache

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Kind is a logical name for a watched resource. The wire format
// (events from the SSE endpoint, ConfigMap entries, REST path
// segments) all use this short identifier — never the GVR string.
//
// New kinds are registered by editing DefaultKinds OR by appending to
// the ConfigMap referenced by CATALYST_K8SCACHE_KINDS_CONFIGMAP. The
// short name is canonicalised lower-case (singular) so an operator who
// types "pod", "Pod", or "pods" all hit the same GVR.
type Kind struct {
	// Name — wire identifier ("pod", "deployment", "vcluster",
	// "server.hcloud"). Matched case-insensitively against the
	// ?kinds= query parameter on the SSE endpoint.
	Name string

	// GVR — apiserver group/version/resource the informer watches.
	GVR schema.GroupVersionResource

	// Namespaced — true when the resource is namespace-scoped.
	// Cluster-scoped resources (Node, Namespace, vCluster's
	// namespace-mapped variants) are watched at the cluster level.
	Namespaced bool

	// Sensitive — true for kinds that may carry secret material in
	// their .data / .stringData / similar fields. The snapshot path
	// scrubs these before writing to disk and the SSE event encoder
	// strips them before each frame leaves the process. Today: just
	// Secret. ConfigMap data is treated as PII-adjacent and also
	// stripped (see redactObject).
	Sensitive bool

	// Optional — true for kinds whose GVR is NOT served on every
	// managed cluster (provider-hcloud Crossplane managed resources
	// only exist where the Hetzner provider is installed;
	// cilium.io/v2alpha1 CiliumEndpointSlice is not served on every
	// Cilium build). Universally-present kinds (namespace, pod,
	// deployment, …) leave this false.
	//
	// #5352 — registering an informer for a GVR the apiserver does
	// NOT serve makes the client-go reflector hot-loop
	// "Failed to watch … → retry" forever, leaking memory until
	// catalyst-api OOMKills (62 restarts / 2.5d on hw288). The
	// factory therefore gates Optional kinds behind a BOUNDED
	// discovery probe in AddCluster: it registers the informer only
	// when the apiserver actually serves the GVR, and skips it
	// otherwise. Non-optional kinds skip the probe entirely so the
	// synchronous-network-free startup fast path is preserved (a dead
	// kubeconfig can never block boot). See factory.go AddCluster.
	Optional bool
}

// DefaultKinds is the built-in registry — every Sovereign starts with
// these GVRs registered. Per ADR-0001 §5 these are the K8s objects the
// Architecture graph and List pages consume.
//
// The ordering is intentional: cluster-scoped first (Namespace, Node)
// then core/v1 namespaced, apps/v1 workloads, networking, storage,
// then Crossplane managed-resources for the cloud join, then
// vCluster.
var DefaultKinds = []Kind{
	// Cluster-scoped core/v1.
	{Name: "namespace", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}, Namespaced: false},
	{Name: "node", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "nodes"}, Namespaced: false},

	// Namespaced core/v1.
	{Name: "pod", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}, Namespaced: true},
	{Name: "service", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "services"}, Namespaced: true},
	{Name: "configmap", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}, Namespaced: true, Sensitive: true},
	{Name: "secret", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}, Namespaced: true, Sensitive: true},
	{Name: "persistentvolumeclaim", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}, Namespaced: true},
	// PV is cluster-scoped; needed by the architecture-graph PVC→Volume.hcloud
	// bridge (PV.csi.volumeAttributes carries the hcloud volume id).
	{Name: "persistentvolume", GVR: schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumes"}, Namespaced: false},

	// Workloads (apps/v1).
	{Name: "deployment", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, Namespaced: true},
	{Name: "statefulset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, Namespaced: true},
	{Name: "daemonset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, Namespaced: true},
	// ReplicaSet — intermediate ownerRef hop on the Deployment→Pod chain.
	// The graph adapter chases this hop to attribute Pods to their Deployment.
	{Name: "replicaset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, Namespaced: true},

	// Networking (networking.k8s.io/v1).
	{Name: "ingress", GVR: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, Namespaced: true},
	// EndpointSlice — exact Service→Pod membership without recomputing
	// label-selector matches client-side for every Service-Pod pair.
	{Name: "endpointslice", GVR: schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}, Namespaced: true},
	// Note: `event` is intentionally NOT registered as a cache kind per
	// TBD-V50 #2125 — events are unbounded; consumers must hit the
	// apiserver directly via EventsV1().Events(ns).List(FieldSelector,
	// Limit). EventsPanel's empty-state bug (G89 #2636) is fixed in
	// the events handler + UI, not by registering this kind here.

	// Crossplane managed resources — provider-hcloud's K8s projection
	// of cloud-side objects (ADR-0001 §5: cloud + K8s data are
	// pre-married before reaching Catalyst).
	//
	// Optional: true (#5352) — the hcloud.crossplane.io provider is
	// installed ONLY on the Hetzner mothership. On a Huawei-hosted
	// Sovereign (hw288) these GVRs are not served, and an
	// unconditionally-registered informer's reflector hot-loops
	// "Failed to watch → retry" forever, leaking memory until
	// catalyst-api OOMKills. The AddCluster discovery gate skips them
	// where the provider is absent (see factory.go).
	{Name: "server.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "servers"}, Namespaced: false, Optional: true},
	{Name: "loadbalancer.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "loadbalancers"}, Namespaced: false, Optional: true},
	{Name: "network.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "networks"}, Namespaced: false, Optional: true},
	{Name: "volume.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "volumes"}, Namespaced: false, Optional: true},

	// vCluster.io tenants.
	{Name: "vcluster", GVR: schema.GroupVersionResource{Group: "vcluster.com", Version: "v1alpha1", Resource: "vclusters"}, Namespaced: true},

	// metrics.k8s.io/PodMetrics — drives the dashboard's
	// `color_by=utilization` overlay (#1084). Earlier versions excluded
	// this kind because the synchronous AddCluster discovery probe
	// blocked startup on dead kubeconfigs. With that probe removed,
	// dynamicinformer can attempt LIST+WATCH directly — when the API
	// isn't served the informer logs a soft error and retries with
	// exponential backoff (no hot loop, no startup block). Every
	// real Sovereign ships bp-metrics-server in the platform bundle,
	// so the utilization gradient renders out of the box.
	{Name: "podmetrics", GVR: schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}, Namespaced: true},

	// EPIC-1 (#1096) — Compliance: Kyverno PolicyReports.
	//
	// `wgpolicyk8s.io/v1alpha2/PolicyReport` is the namespace-scoped
	// per-resource compliance report Kyverno emits for every Pod /
	// Workload it audits. `ClusterPolicyReport` is the cluster-scoped
	// equivalent for cluster-scoped resources (Namespaces, Nodes,
	// CRDs, …). The score aggregator (slice S1) consumes both via the
	// same SSE fanout the architecture graph already uses — no special
	// path. The reports themselves carry no secret material (Kyverno
	// omits the offending object's data fields by design) so
	// Sensitive=false is correct.
	{Name: "policyreport", GVR: schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "policyreports"}, Namespaced: true},
	{Name: "clusterpolicyreport", GVR: schema.GroupVersionResource{Group: "wgpolicyk8s.io", Version: "v1alpha2", Resource: "clusterpolicyreports"}, Namespaced: false},

	// QA-loop iter-8 Fix #41 — ClusterPolicy CRD lookup so the
	// per-policy drill-down (TC-026) renders Severity + Rule list off
	// the live ClusterPolicy CR's annotations + spec.rules without an
	// extra apiserver round-trip per request. The compliance handler's
	// SubscribeKinds defaults include `clusterpolicy` so the Factory
	// fanout streams these too. ClusterPolicy is cluster-scoped.
	{Name: "clusterpolicy", GVR: schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}, Namespaced: false},

	// TBD-V50 (#2125) — Kubernetes Events deliberately NOT registered.
	//
	// Events are a write-once / read-rare resource that the kube-apiserver
	// already keeps bounded via `--event-ttl=1h` (default). The
	// SharedInformer abstraction, in contrast, stores every event it
	// receives for the lifetime of the Pod — an unbounded write-store on
	// a write-once stream. PR #2124 (V49) attempted to contain the bleed
	// with a 5000-row initial-LIST cap, but the watch that follows kept
	// accumulating new events and catalyst-api still OOM-cycled (178
	// restarts in 20h on the mothership, image d9e678f).
	//
	// Architecturally-correct fix: do NOT register `event` as a cached
	// kind at all. Consumers that need event data already issue direct
	// apiserver `EventsV1().Events(ns).List(FieldSelector + Limit)` calls
	// (see handler/compliance_runtime.go listFalcoK8sEvents and
	// handler/sovereign.go HandleSovereignJobs). New consumers MUST follow
	// the same pattern — no cache, no informer, no boundedFactory.
	//
	// The regression test TestFactory_NoEventInformerRegistered in
	// k8scache_test.go enforces this invariant.

	// QA-loop iter-2 Fix #17 — CRDs surfaced through /k8s/{kind} need
	// matching registry entries; otherwise the handler returns
	// `{"error":"unknown kind",...}` even when the CRD is installed
	// and the apiserver would happily serve it. Caught live on
	// omantel.biz: TC-070..075, TC-184/192/194/227 all returned the
	// "unknown kind" body for helmreleases / applications / blueprints
	// / useraccesses / organizations / environments — the kinds were
	// reachable individually via the existing per-CRD handlers
	// (HandleListUserAccesses, etc.) but the generic SSE / list surface
	// at /api/v1/sovereigns/{id}/k8s/{kind} did not know about them.
	//
	// Per feedback_chroot_in_cluster_fallback.md: every new GVR added
	// here MUST get a matching rule on catalyst-api-cutover-driver
	// ClusterRole (clusterrole-cutover-driver.yaml) — the chroot
	// SovereignClient uses that SA via in-cluster fallback.
	//
	// helm.toolkit.fluxcd.io/v2 HelmReleases — Flux's release records
	// for every blueprint installed on the cluster. The dashboard's
	// platform-health view + the components page both consume this.
	{Name: "helmrelease", GVR: schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, Namespaced: true},
	// access.openova.io/v1alpha1 UserAccess — RBAC binding CRD
	// surfaced on the /users page. CLUSTER-scoped (Refs #4773): a single
	// grant spans many namespaces and can emit ClusterRoleBindings, so
	// the CR carries no namespace and the generic /k8s/{kind} + informer
	// paths must treat it cluster-scoped.
	{Name: "useraccess", GVR: schema.GroupVersionResource{Group: "access.openova.io", Version: "v1alpha1", Resource: "useraccesses"}, Namespaced: false},
	// apps.openova.io/v1 Application — workload CRD owning the
	// `/apps` and AppDetail pages (EPIC-2 slice T+O+P).
	//
	// TBD-A54 (#1946): the served CRD at
	// products/catalyst/chart/crds/application.yaml exposes ONLY v1
	// (storage:true). A previous v1alpha1 GVR here returned zero events
	// from the apiserver because the version is not served, which broke
	// every read of `/k8s/application` and silently stalled the
	// application controller's UI consumers (#1097 EPIC reopened).
	// Keep this pinned to the storage version of the CRD shipped with
	// the catalyst chart — see also handler/applications.go
	// (ApplicationGVR()) and controllers/application/internal/controller
	// (ApplicationGVR) which both already use v1.
	{Name: "application", GVR: schema.GroupVersionResource{Group: "apps.openova.io", Version: "v1", Resource: "applications"}, Namespaced: true},
	// catalyst.openova.io/v1 Blueprint — published blueprint records
	// the curate/publish handlers operate on. The CRD serves both
	// v1alpha1 (deprecated, storage:false) and v1 (storage:true); pin
	// the watcher to the storage version so events are not silently
	// missed. Refs #1946.
	//
	// CLUSTER-scoped (Refs #4860): the served CRD at
	// products/catalyst/chart/crds/blueprint.yaml declares `scope: Cluster`,
	// so the Blueprint CR carries no namespace. Registering it Namespaced
	// drove the generic /k8s/{kind} read + the Edit-IaC dry-run/apply write
	// down the namespaced dynamic-client branch (`.Namespace(ns)`) and the
	// GET/tree handlers' `Namespaced && ns==""` guard rejected the "_" ns
	// with a spurious 400; align the registry with the CRD scope so every
	// path resolves the cluster-scoped Blueprint correctly.
	{Name: "blueprint", GVR: schema.GroupVersionResource{Group: "catalyst.openova.io", Version: "v1", Resource: "blueprints"}, Namespaced: false},
	// orgs.openova.io/v1 Organization — top-level tenancy CRD
	// surfaced on the /organizations page. CRD ships v1 only. Refs #1946.
	{Name: "organization", GVR: schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}, Namespaced: false},
	// catalyst.openova.io/v1 Environment — logical environment
	// dimension (one Environment realised by N Clusters per
	// docs/NAMING-CONVENTION.md). Surfaced on the /environments page.
	// CRD ships v1 only. Refs #1946.
	{Name: "environment", GVR: schema.GroupVersionResource{Group: "catalyst.openova.io", Version: "v1", Resource: "environments"}, Namespaced: true},

	// dr.openova.io/v1 Continuum — DR orchestration CR per
	// EPIC-6 #1101 (Multi-cluster + Continuum DR). CRD shipped by
	// products/catalyst/chart/crds/continuum.yaml; not registered in
	// the kinds.go list before Wave 5.68 (#1094 acceptance #2 gap)
	// so the k9s-on-web Cloud/list/continuum URL silently fell back
	// to kind=pods. With this entry the operator can view + drill
	// into Continuum CRs from console.<sov-fqdn>/cloud.
	{Name: "continuum", GVR: schema.GroupVersionResource{Group: "dr.openova.io", Version: "v1", Resource: "continuums"}, Namespaced: true},

	// dr.openova.io/v1 CNPGPair — paired CNPG Cluster orchestration
	// (primary in regionA, replica in regionB) for #1094 acceptance #2
	// active-hotstandby. Same registration gap as Continuum.
	{Name: "cnpgpair", GVR: schema.GroupVersionResource{Group: "dr.openova.io", Version: "v1", Resource: "cnpgpairs"}, Namespaced: true},

	// QA-loop iter-3 Fix #18 — RBAC ClusterRole + ClusterRoleBinding
	// surfaced through GET /api/v1/sovereigns/{id}/k8s/clusterroles and
	// /clusterrolebindings. Both are cluster-scoped (Namespaced=false).
	// The matrix expects these on TC-122/196/199/248 to render the RBAC
	// section of the Sovereign Console; without them the generic /k8s/
	// surface returned 404 "unknown kind".
	//
	// Per feedback_chroot_in_cluster_fallback.md: every new GVR added
	// here MUST get a matching rule on catalyst-api-cutover-driver
	// ClusterRole (clusterrole-cutover-driver.yaml). Both RBAC kinds
	// carry binding metadata only (subject + roleRef references) — no
	// secret material — so Sensitive=false is correct.
	{Name: "clusterrole", GVR: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, Namespaced: false},
	{Name: "clusterrolebinding", GVR: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, Namespaced: false},

	// QA-loop iter-4 Fix #24 — CustomResourceDefinitions surfaced through
	// GET /api/v1/sovereigns/{id}/k8s/customresourcedefinitions. The
	// generic /k8s/ surface returned 404 "unknown kind" because the GVR
	// for apiextensions.k8s.io/v1/customresourcedefinitions was never
	// registered. Caught live on omantel.biz iter-4: TC-199 returned
	// HTTP 404 with body
	//   {"availableKinds":[…],"error":"unknown kind","kind":"customresourcedefinitions"}
	// CRDs are cluster-scoped (Namespaced=false). The kind carries a
	// schema definition + reconciliation rules — no secret material — so
	// Sensitive=false is correct.
	//
	// Per feedback_chroot_in_cluster_fallback.md: every new GVR added
	// here MUST get a matching rule on catalyst-api-cutover-driver
	// ClusterRole (clusterrole-cutover-driver.yaml). The chroot
	// SovereignClient uses that SA via in-cluster fallback. Read-only
	// verbs only — the Sovereign Console renders CRD inventory; CRD
	// install/uninstall happens through Flux + the blueprint catalog,
	// not direct apiextensions writes.
	{Name: "customresourcedefinition", GVR: schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}, Namespaced: false},

	// QA-loop iter-11 Fix #48 — Networking GVRs surfaced through the
	// generic /k8s/{kind} endpoint AND the new networking aggregator
	// handlers (HandleNetworkingPolicies / ClusterMesh / NetBird /
	// DMZ / Hubble). Caught live on omantel iter-11: TC-279 and
	// TC-294 returned "missing required token: 'CiliumNetworkPolicy'"
	// because the GVRs were never registered.
	//
	// Cilium NetworkPolicy CRDs (cilium.io/v2) — namespace-scoped and
	// cluster-scoped tier-3 micro-segmentation policies. Matrix
	// asserts `kubectl get cnp -A` and `kubectl get ccnp` for the
	// default-deny baseline (TC-278/279/280/287/294).
	{Name: "ciliumnetworkpolicy", GVR: schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}, Namespaced: true},
	{Name: "ciliumclusterwidenetworkpolicy", GVR: schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}, Namespaced: false},
	// Gateway API (gateway.networking.k8s.io/v1) — Cilium implements
	// GatewayClass `cilium`. Matrix asserts on TC-302 (gateway class
	// presence) + TC-295 (HTTPRoute listing for ingress visibility).
	{Name: "gatewayclass", GVR: schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}, Namespaced: false},
	{Name: "gateway", GVR: schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, Namespaced: true},
	{Name: "httproute", GVR: schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}, Namespaced: true},
	// Cilium ClusterMesh (cilium.io/v2alpha1) — multi-region peering
	// records. Matrix asserts on TC-273/297 (omantel-fsn ↔ omantel-hel
	// peer status).
	//
	// Optional: true (#5352) — cilium.io/v2alpha1 CiliumEndpointSlice
	// is not served on every Cilium build (it depends on the
	// endpointslice-based ClusterMesh mode being enabled). Where the
	// version is absent an unconditionally-registered informer's
	// reflector hot-loops "Failed to watch → retry", leaking memory
	// (a #5352 contributor on hw288). The AddCluster discovery gate
	// registers it only where the GVR is actually served.
	{Name: "ciliumendpointslice", GVR: schema.GroupVersionResource{Group: "cilium.io", Version: "v2alpha1", Resource: "ciliumendpointslices"}, Namespaced: false, Optional: true},
	// k8s.io NetworkPolicy (networking.k8s.io/v1) — vanilla NPs
	// surfaced alongside CNPs on the Policies tab. Already covered by
	// the cutover-driver ClusterRole (`networking.k8s.io/networkpolicies`)
	// but the kind was never registered for the generic /k8s/ surface.
	{Name: "networkpolicy", GVR: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, Namespaced: true},

	// The Sandbox CRD (sandbox.openova.io/v1) was REMOVED platform-wide
	// by founder direction 2026-06-30 — the Sandbox concept + menu are
	// gone, superseded by the per-Org Agenity workspace + bp-openova-mcp.
	// Its former kinds.go registry entry is deliberately NOT re-added:
	// no cluster serves the GVR anymore, so an informer would only
	// hot-loop "Failed to watch → retry" and leak memory (the #5352
	// churn class). See TestDefaultKinds_NoSandboxKind.

	// Issue #3978 (Refs #3970) — restore the rich per-kind cloud-list
	// (`/cloud?view=list`) page. Each reconciler resource kind must render
	// as a live K8sListPage driven by the catalyst-api k8scache SSE stream,
	// and that stream only emits kinds registered HERE. The five reconciler
	// families below (Flux source/kustomize, cert-manager, External-Secrets,
	// CloudNativePG) are the desired-state controllers the Reconciliation
	// view surfaces; without these registry entries the K8sListPage's
	// SSE-snapshot filter (`${name}:` prefix) returns zero objects and the
	// per-kind page falls back to "unknown kind".
	//
	// GVRs verified against the authoritative existing references:
	//   - helmwatch/reconcilers.go            KustomizationGVR
	//   - controllers/application/...          FluxGitRepositoryGVR
	//   - helmwatch/declarative_reconcilers.go CertificateGVR /
	//                                          ExternalSecretGVR / CNPGClusterGVR
	//
	// Per feedback_chroot_in_cluster_fallback.md: every GVR added here gets
	// a matching get/list/watch rule on the catalyst-api-cutover-driver
	// ClusterRole (clusterrole-cutover-driver.yaml) — the chroot
	// SovereignClient uses that SA via in-cluster fallback. None of these
	// kinds carry secret material in the cached object (cert-manager writes
	// the issued cert into a Secret gated by the `secret` Sensitive=true
	// entry; an ExternalSecret only references a remote key, never inlines
	// it), so Sensitive=false is correct.

	// Flux source-controller GitRepository — the Git source CRs the
	// Application controller upserts (FluxGitRepositoryGVR). v1 is the only
	// served + storage version per the Application controller comment.
	{Name: "gitrepository", GVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}, Namespaced: true},
	// Flux kustomize-controller Kustomization — the reconcile leaves the
	// Jobs canvas + Reconciliation DAG render (KustomizationGVR).
	{Name: "kustomization", GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Namespaced: true},
	// cert-manager Certificate — desired-state reconciler whose Ready
	// condition drives the node status (CertificateGVR).
	{Name: "certificate", GVR: schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}, Namespaced: true},
	// External-Secrets ExternalSecret — references a remote secret store
	// key; the CR itself inlines no secret value (ExternalSecretGVR).
	//
	// #3987: pinned to v1beta1, NOT v1. The ESO release shipped in the
	// platform bundle serves ONLY v1alpha1 + v1beta1 (v1beta1 is the
	// served+storage version); it does NOT serve v1. A static-GVR informer
	// has no version fallback (unlike helmwatch's declarative reconciler,
	// which retries ExternalSecretGVR→ExternalSecretGVRBeta), so pinning v1
	// made the informer LIST/WATCH a non-served version and silently return
	// zero — `/cloud?view=list&kind=externalsecrets` rendered "No
	// externalsecret objects" on hw173 despite 13 live (under v1beta1),
	// while the GRAPH showed them via the helmwatch fallback path. v1beta1
	// is the version every current platform ESO serves, so the cache reads
	// the real objects. (If a future ESO release drops v1beta1 in favour of
	// v1, the kinds ConfigMap override — CATALYST_K8SCACHE_KINDS_CONFIGMAP —
	// re-pins it with no code change, per the LoadRegistry merge path.)
	{Name: "externalsecret", GVR: schema.GroupVersionResource{Group: "external-secrets.io", Version: "v1beta1", Resource: "externalsecrets"}, Namespaced: true},
	// CloudNativePG Cluster (CNPGClusterGVR). The registry Name is
	// `cnpgcluster` (NOT `cluster`) deliberately: the cloud-side join
	// already owns a "Cluster" concept, and the GVR's plural `clusters`
	// would otherwise become an ambiguous alias. Short aliases `cnpg` and
	// `cnpgclusters` resolve here (see kindShortAliases). CNPG has no
	// standard Ready condition; the FE reads status.phase.
	{Name: "cnpgcluster", GVR: schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}, Namespaced: true},
	// CloudNativePG Database — a distinct reconciler kind (the founder
	// listed it alongside the CNPG Cluster). Same group/version, resource
	// `databases`; Name `cnpgdatabase` to keep the cloud-list rows
	// unambiguous against the Cluster kind.
	{Name: "cnpgdatabase", GVR: schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databases"}, Namespaced: true},
}

// Registry is a runtime-mutable lookup keyed by the short Name. It
// fronts every read path: the REST list handler resolves
// path-segment → Kind → GVR, the SSE handler resolves ?kinds=foo,bar
// → []Kind, and the factory iterates over Registry.All() to spawn
// informers.
//
// Two indexes are maintained internally:
//
//   - byName    — canonical singular short-name (the wire identifier).
//   - byPlural  — alias index covering the GVR.Resource (plural form
//     emitted by `kubectl api-resources`) plus a small set of
//     common short-name aliases (pvc → persistentvolumeclaim,
//     pv → persistentvolume, ns → namespace, …). Handlers
//     accept any of these forms transparently.
//
// `kubectl get pods` and `kubectl get pod` both work; the catalyst-api
// REST list endpoint must mirror that ergonomic. Without the plural
// index a path like `/k8s/services` returned 404 even though
// `/k8s/service` succeeded — surprising for operators with kubectl
// muscle memory and surfaced in the iter-1 QA loop matrix as TC-084 /
// TC-085 / TC-090 / TC-091 / TC-130 (cloud-list 404s on plural paths).
type Registry struct {
	byName   map[string]Kind
	byPlural map[string]string // alias → canonical singular Name
}

// kindShortAliases maps common kubectl short-names to the canonical
// singular Name a Kind is registered under. Mirrors the conventional
// `kubectl api-resources -o wide` short-name column for the few cases
// where the short form is in widespread operator muscle memory and
// not derivable from the GVR.Resource (the simple "trim trailing s"
// rule the plural index handles).
var kindShortAliases = map[string]string{
	"ns":     "namespace",
	"no":     "node",
	"po":     "pod",
	"svc":    "service",
	"cm":     "configmap",
	"sec":    "secret", // not a real kubectl alias but unambiguous
	"pvc":    "persistentvolumeclaim",
	"pvcs":   "persistentvolumeclaim", // pluralised short form (matrix usage)
	"pv":     "persistentvolume",
	"pvs":    "persistentvolume",
	"deploy": "deployment",
	"sts":    "statefulset",
	"ds":     "daemonset",
	"rs":     "replicaset",
	"ing":    "ingress",
	"ep":     "endpointslice",
	// TBD-V50 (#2125) — `ev` → `event` alias removed because `event` is
	// no longer cached (see kinds.go above). Consumers that need event
	// data hit the apiserver directly via EventsV1().Events().List().
	// QA-loop iter-4 Fix #24 — `kubectl get crd` and `kubectl get crds`
	// are the conventional ergonomic forms operators reach for. The
	// plural-alias index handles "customresourcedefinitions" naturally
	// (trim-trailing-s rule); these short forms are not derivable so
	// they live here.
	"crd":    "customresourcedefinition",
	"crds":   "customresourcedefinition",
	// Issue #3978 (Refs #3970) — CNPG Cluster is registered under Name
	// `cnpgcluster` (not `cluster`) to avoid colliding with the cloud-side
	// Cluster concept. The GVR plural is `clusters`, so the natural
	// trim-trailing-s plural-alias index would map `clusters` → `cnpgcluster`
	// — but operators reach for `cnpg` and the doubled-plural `cnpgclusters`,
	// neither of which the plural index derives. Pin both here so the
	// rich cloud-list per-kind page resolves them.
	"cnpg":         "cnpgcluster",
	"cnpgclusters": "cnpgcluster",
}

// NewRegistry — start empty; callers pass DefaultKinds (or the loaded
// ConfigMap registry) to Add to build the runtime set.
func NewRegistry() *Registry {
	return &Registry{
		byName:   map[string]Kind{},
		byPlural: map[string]string{},
	}
}

// Add or replace a Kind in the registry. Name is canonicalised
// lower-case so the ?kinds=Pod query path resolves correctly.
//
// Two index entries are written:
//
//  1. byName[canonical(k.Name)] = k         — singular wire identifier
//  2. byPlural[canonical(k.GVR.Resource)]   — kubectl plural form
//
// Returns an error when GVR.Resource is empty — every registered Kind
// must specify the resource (the GVR's plural form). Group + Version
// may be empty for core/v1 resources.
func (r *Registry) Add(k Kind) error {
	if k.Name == "" {
		return fmt.Errorf("k8scache: Kind.Name is required")
	}
	if k.GVR.Resource == "" {
		return fmt.Errorf("k8scache: Kind.GVR.Resource is required for %q", k.Name)
	}
	canonName := canonicalKindName(k.Name)
	r.byName[canonName] = k
	// Maintain the plural alias index. Two collision rules:
	//
	//   (1) If the plural collides with a registered singular Name
	//       (e.g. someone registered Name="services"), the singular wins
	//       and the alias is skipped.
	//   (2) If the plural is already aliased to a DIFFERENT singular
	//       (concrete: metrics.k8s.io/PodMetrics has GVR.Resource="pods"
	//       same as core/v1 Pod's plural), the FIRST registration wins.
	//       Without this guard, registration order silently flips which
	//       Kind `/k8s/pods` resolves to, depending on whether pod or
	//       podmetrics was loaded first.
	plural := canonicalKindName(k.GVR.Resource)
	if plural != "" && plural != canonName {
		if _, takenSingular := r.byName[plural]; !takenSingular {
			if _, alreadyAliased := r.byPlural[plural]; !alreadyAliased {
				r.byPlural[plural] = canonName
			}
		}
	}
	return nil
}

// Get returns the Kind for a name (case-insensitive). Resolution order:
//
//  1. Exact canonical match against the registered singular Name.
//  2. Plural alias (matches against GVR.Resource — the form `kubectl
//     api-resources` prints, e.g. "services", "namespaces", "pods").
//  3. Common short alias (pvc, ns, svc, …) — see kindShortAliases.
//
// Returns false when none of the three resolve — handlers translate
// that to a 404 with the kinds list.
func (r *Registry) Get(name string) (Kind, bool) {
	canon := canonicalKindName(name)
	if k, ok := r.byName[canon]; ok {
		return k, true
	}
	if singular, ok := r.byPlural[canon]; ok {
		if k, ok := r.byName[singular]; ok {
			return k, true
		}
	}
	if singular, ok := kindShortAliases[canon]; ok {
		if k, ok := r.byName[singular]; ok {
			return k, true
		}
	}
	return Kind{}, false
}

// All returns every registered Kind. Allocation-light — callers iterate
// once at factory boot to spawn informers, and again on each SSE
// connection to validate ?kinds=. Stable iteration order is NOT
// guaranteed (Go map ranging is randomised); the SSE multiplexer
// sorts by Name on connect.
func (r *Registry) All() []Kind {
	out := make([]Kind, 0, len(r.byName))
	for _, k := range r.byName {
		out = append(out, k)
	}
	return out
}

// Names returns every registered Kind name in lower-case
// canonicalised form. Used by the REST 404 path so an operator who
// types `/k8s/podz` gets back a list of valid kinds.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.byName))
	for n := range r.byName {
		out = append(out, n)
	}
	return out
}

// canonicalKindName lowercases the input and trims surrounding
// whitespace. Plural / singular are NOT collapsed — the registry
// stores whatever form was registered. Handlers normalise the inbound
// query parameter via this same function.
func canonicalKindName(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out = append(out, c)
	}
	return string(out)
}

// CanonicalKindName exposes the canonicalisation rule for handlers
// that need to compare ?kinds= input against registered names.
func CanonicalKindName(s string) string { return canonicalKindName(s) }
