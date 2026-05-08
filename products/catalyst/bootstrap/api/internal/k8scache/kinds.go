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

	// Crossplane managed resources — provider-hcloud's K8s projection
	// of cloud-side objects (ADR-0001 §5: cloud + K8s data are
	// pre-married before reaching Catalyst).
	{Name: "server.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "servers"}, Namespaced: false},
	{Name: "loadbalancer.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "loadbalancers"}, Namespaced: false},
	{Name: "network.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "networks"}, Namespaced: false},
	{Name: "volume.hcloud", GVR: schema.GroupVersionResource{Group: "hcloud.crossplane.io", Version: "v1alpha1", Resource: "volumes"}, Namespaced: false},

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
}

// Registry is a runtime-mutable lookup keyed by the short Name. It
// fronts every read path: the REST list handler resolves
// path-segment → Kind → GVR, the SSE handler resolves ?kinds=foo,bar
// → []Kind, and the factory iterates over Registry.All() to spawn
// informers.
type Registry struct {
	byName map[string]Kind
}

// NewRegistry — start empty; callers pass DefaultKinds (or the loaded
// ConfigMap registry) to Add to build the runtime set.
func NewRegistry() *Registry {
	return &Registry{byName: map[string]Kind{}}
}

// Add or replace a Kind in the registry. Name is canonicalised
// lower-case so the ?kinds=Pod query path resolves correctly.
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
	r.byName[canonicalKindName(k.Name)] = k
	return nil
}

// Get returns the Kind for a name (case-insensitive). Returns false
// when the name is not registered — handlers translate that to a
// 404 with the kinds list.
func (r *Registry) Get(name string) (Kind, bool) {
	k, ok := r.byName[canonicalKindName(name)]
	return k, ok
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
