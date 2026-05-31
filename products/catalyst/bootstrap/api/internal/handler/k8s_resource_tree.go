// Package handler — k8s_resource_tree.go: EPIC-4 Slice R2 (#1099)
// resource-tree fan-out endpoint.
//
// REST surface added by this slice:
//
//	GET /api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/tree
//
// Returns a single JSON document describing the focused resource and
// every resource that owns it (`owners`) or that it owns / selects
// (`children`). Both directions reuse the in-process Indexer through
// k8scache.GetResourcesByOwner / GetResourcesBySelector — no apiserver
// LIST calls.
//
// Tree shape (recursive):
//
//	{
//	  "node": { "kind": "Pod", "ns": "default", "name": "wp-abc",
//	             "ready": true, "phase": "Running" },
//	  "owners": [ <Node> ],            // upward (ownerReferences)
//	  "children": [ <Node> ],          // downward (selector or owner)
//	}
//
// The handler walks UP via ownerReferences and DOWN via label selectors
// + ownerReferences, with depth caps to keep the response bounded
// regardless of tree size.
package handler

import (
	"fmt"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// resourceTreeMaxDepth caps tree-walk recursion. Real workloads top out
// at depth ~3 (Service → EndpointSlice → Pod, or Deployment → ReplicaSet
// → Pod), so a cap of 6 leaves headroom for HPA/PDB-style chains.
const resourceTreeMaxDepth = 6

// resourceTreeMaxFanout caps per-level breadth. A Service that selects
// 500 Pods isn't useful in the UI; the tree returns the first N and the
// UI shows a "+N more" affordance.
const resourceTreeMaxFanout = 50

// resourceTreeNode is the wire shape returned to the UI.
type resourceTreeNode struct {
	Kind     string             `json:"kind"`
	APIGroup string             `json:"apiGroup,omitempty"`
	Ns       string             `json:"ns,omitempty"`
	Name     string             `json:"name"`
	UID      string             `json:"uid,omitempty"`
	Phase    string             `json:"phase,omitempty"`
	Ready    bool               `json:"ready"`
	// G80 #2627: ReadinessApplicable is false for K8s kinds that have
	// no `Ready` condition concept (Secret, ConfigMap, PolicyReport,
	// Endpoints, ServiceAccount, Role, etc.). The UI uses this to
	// render `N/A` instead of defaulting to `Pending` when Ready is
	// false. Omitted from JSON when true (the common case) so existing
	// clients are unaffected.
	// G80 #2627: `passive: true` marks kinds with no Ready concept
	// (Secret/ConfigMap/PolicyReport/Endpoints/etc.) so the UI renders
	// `N/A` instead of `Pending`. Inverted from the natural
	// "applicable" framing because `bool + json:omitempty` strips the
	// false case — the OMITTED side must be the common one (non-passive
	// workload kinds). When omitted, the UI treats the kind as
	// readiness-applicable (default behavior).
	Passive bool `json:"passive,omitempty"`
	Owners              []resourceTreeNode `json:"owners,omitempty"`
	Children            []resourceTreeNode `json:"children,omitempty"`
}

// passiveKindNames — lower-cased k8scache.Kind.Name values for K8s
// resource kinds that don't carry a `Ready` condition. These are status-
// free or static-data objects; the readiness classifier should render
// them as N/A rather than defaulting to Pending. G80 #2627.
//
// Notable additions on top of obvious ones (Secret/ConfigMap):
//   - PolicyReport / ClusterPolicyReport — Kyverno status objects with
//     a `results[]` block, no Ready condition
//   - Endpoints / EndpointSlice — derived from Service+Pod readiness;
//     parent Service is the right surface for readiness
//   - Role / RoleBinding / ClusterRole / ClusterRoleBinding — pure RBAC
//     bindings, no runtime state
//   - ServiceAccount — just a token holder
//   - PersistentVolume / PersistentVolumeClaim — Phase-based (Bound /
//     Pending / Released / Failed) NOT condition-based; the resource-
//     tree node already surfaces Phase, so don't apply the Ready badge
// G88 #2635 (2026-05-31): broader passive set per founder feedback that
// initial G80 fix covered only a limited set. Inverted to opt-IN active
// kinds + treat everything else as passive — the active set is small
// and well-defined (workloads + Flux source/release CRs), while the
// passive set is open-ended (anything with no Ready condition).
var activeKindNames = map[string]struct{}{
	// Pod-lifecycle workloads (status.conditions[type=Ready])
	"pod":                   {},
	"replicaset":            {},
	"deployment":            {},
	"statefulset":           {},
	"daemonset":             {},
	"job":                   {},
	"cronjob":               {},
	"replicationcontroller": {},
	// Flux source + release (status.conditions[type=Ready])
	"helmrelease":      {},
	"helmrepository":   {},
	"helmchart":        {},
	"gitrepository":    {},
	"ocirepository":    {},
	"bucket":           {},
	"kustomization":    {},
	"imagerepository":  {},
	"imageupdateautomation": {},
	"alert":            {},
	"provider":         {}, // pkg.crossplane.io + flux notification
	"receiver":         {},
	// Networking — Service/Ingress/Gateway have Ready/Programmed conditions
	"service":      {},
	"ingress":      {},
	"gateway":      {},
	"httproute":    {},
	"grpcroute":    {},
	// Cert-Manager
	"certificate":        {},
	"certificaterequest": {},
	"order":              {},
	"challenge":          {},
	"issuer":             {},
	"clusterissuer":      {},
	// Argo-rollouts / Karpenter / KEDA / etc — most have Ready
	"rollout":           {},
	"nodeclaim":         {},
	"scaledobject":      {},
	"scaledjob":         {},
	// CNPG database CR
	"cluster": {}, // postgresql.cnpg.io/Cluster has Ready
	"pooler":  {},
	// Cilium / CRDs with status
	"ciliumloadbalancerippool": {},
	"ciliumnode":               {},
	// Crossplane managed resources have Ready+Synced
	"providerrevision": {},
	"compositionrevision": {},
	// Application/Continuum/CnpgPair (Catalyst)
	"application": {},
	"continuum":   {},
	"cnpgpair":    {},
}

// isPassiveKind returns true for K8s kinds without a Ready-condition
// concept (Secrets/ConfigMaps/PolicyReports/Endpoints/RBAC bindings/
// passive networking/static config/etc.). UI renders these as `N/A`
// instead of defaulting to `Pending` which is misleading for objects
// that have no readiness semantics by design.
func isPassiveKind(name string) bool {
	_, isActive := activeKindNames[strings.ToLower(name)]
	return !isActive
}

// HandleK8sResourceTree — GET
//
//	/api/v1/sovereigns/{id}/k8s/{kind}/{ns}/{name}/tree
//
// Walks the in-process Indexer to build the upward (owners) and
// downward (children) view of the focused resource.
func (h *Handler) HandleK8sResourceTree(w http.ResponseWriter, r *http.Request) {
	if h.k8sCache == nil {
		http.Error(w, "k8scache disabled", http.StatusServiceUnavailable)
		return
	}
	clusterID, kindName, ns, name, ok := h.parseResourceParams(w, r)
	if !ok {
		return
	}

	dyn, kind, err := h.resolveResourceClient(clusterID, kindName)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "cluster-unavailable",
			"detail": err.Error(),
		})
		return
	}

	var focus *unstructured.Unstructured
	var fetchErr error
	if kind.Namespaced {
		if ns == "" {
			writeBadRequest(w, "namespace-required",
				fmt.Sprintf("kind %q is namespace-scoped; ns must be supplied", kindName))
			return
		}
		focus, fetchErr = dyn.Resource(kind.GVR).Namespace(ns).Get(r.Context(), name, metav1.GetOptions{})
	} else {
		focus, fetchErr = dyn.Resource(kind.GVR).Get(r.Context(), name, metav1.GetOptions{})
	}
	if fetchErr != nil {
		if apierrors.IsNotFound(fetchErr) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "resource-not-found",
				"detail": fmt.Sprintf("%s %q not found", kindName, qualifiedName(ns, name)),
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "resource-get-failed",
			"detail": fetchErr.Error(),
		})
		return
	}

	visited := map[string]bool{}
	tree := h.buildResourceTreeNode(clusterID, kind, focus, 0, visited)
	writeJSON(w, http.StatusOK, tree)
}

// buildResourceTreeNode constructs the (owners, children) view rooted at
// the supplied object. Visited tracks UIDs across the recursion so a
// cycle (e.g. CRD owning itself by accident) doesn't blow the stack.
func (h *Handler) buildResourceTreeNode(
	clusterID string,
	kind k8scache.Kind,
	obj *unstructured.Unstructured,
	depth int,
	visited map[string]bool,
) resourceTreeNode {
	node := resourceTreeNodeFor(kind, obj)
	if obj == nil {
		return node
	}
	uid := string(obj.GetUID())
	if uid != "" {
		if visited[uid] {
			return node
		}
		visited[uid] = true
	}
	if depth >= resourceTreeMaxDepth {
		return node
	}

	// ── Walk UP: ownerReferences → fetch each via the Indexer ─────
	for _, ref := range obj.GetOwnerReferences() {
		ownerKind := canonicalKindByRefKind(h.k8sCache.Registry(), ref.Kind)
		if ownerKind == "" {
			continue
		}
		ownerKindObj, _ := h.k8sCache.Registry().Get(ownerKind)
		ownerObjs, _ := h.k8sCache.GetResourcesByOwner(clusterID, "self-anchor-not-used", "", "")
		_ = ownerObjs
		// Direct list lookup: GetResourcesByOwner walks every object's
		// ownerReferences — that's the inverse direction. For the
		// upward walk we want a specific named owner. Use the indexer
		// list filtered by name.
		owner := h.findIndexerObject(clusterID, ownerKind, obj.GetNamespace(), ref.Name)
		if owner == nil {
			// Cluster-scoped owner — try without namespace.
			owner = h.findIndexerObject(clusterID, ownerKind, "", ref.Name)
		}
		if owner == nil {
			continue
		}
		child := h.buildResourceTreeNode(clusterID, ownerKindObj, owner, depth+1, visited)
		node.Owners = append(node.Owners, child)
		if len(node.Owners) >= resourceTreeMaxFanout {
			break
		}
	}

	// ── Walk DOWN: ownerReferences pointing AT us ─────────────────
	owned, _ := h.k8sCache.GetResourcesByOwner(clusterID, kind.Name, obj.GetNamespace(), obj.GetName())
	for _, child := range owned {
		childKindName := strings.ToLower(child.GetKind())
		childKind, ok := h.k8sCache.Registry().Get(childKindName)
		if !ok {
			continue
		}
		node.Children = append(node.Children,
			h.buildResourceTreeNode(clusterID, childKind, child, depth+1, visited))
		if len(node.Children) >= resourceTreeMaxFanout {
			break
		}
	}

	// ── Walk DOWN: label-selector resolution (Service → Pod, etc.) ─
	for _, sel := range deriveDownwardSelectors(kind, obj) {
		matched, _ := h.k8sCache.GetResourcesBySelector(clusterID, sel.targetKind, obj.GetNamespace(), sel.selector)
		for _, m := range matched {
			if uid := string(m.GetUID()); uid != "" && visited[uid] {
				continue
			}
			targetKind, _ := h.k8sCache.Registry().Get(sel.targetKind)
			node.Children = append(node.Children,
				h.buildResourceTreeNode(clusterID, targetKind, m, depth+1, visited))
			if len(node.Children) >= resourceTreeMaxFanout {
				break
			}
		}
	}
	return node
}

// resourceTreeNodeFor projects an unstructured object into the
// wire-shape tree node (sans recursion). Pulls phase + ready flags out
// of common spots so the UI doesn't have to peek into every CR shape.
func resourceTreeNodeFor(kind k8scache.Kind, obj *unstructured.Unstructured) resourceTreeNode {
	if obj == nil {
		return resourceTreeNode{Kind: kind.Name, Passive: isPassiveKind(kind.Name)}
	}
	apiGroup := kind.GVR.Group
	node := resourceTreeNode{
		Kind:                kind.Name,
		APIGroup:            apiGroup,
		Ns:                  obj.GetNamespace(),
		Name:                obj.GetName(),
		UID:                 string(obj.GetUID()),
		Passive: isPassiveKind(kind.Name),
	}
	if phase, ok, _ := unstructured.NestedString(obj.Object, "status", "phase"); ok {
		node.Phase = phase
	}
	// Heuristic readiness: status.conditions[?(@.type=="Ready")].status == "True".
	if conds, ok, _ := unstructured.NestedSlice(obj.Object, "status", "conditions"); ok {
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cm["type"] == "Ready" && cm["status"] == "True" {
				node.Ready = true
				break
			}
		}
	}
	// Workloads — readyReplicas == replicas means "ready".
	if !node.Ready {
		ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
		desired, hasDesired, _ := unstructured.NestedInt64(obj.Object, "status", "replicas")
		if hasDesired && desired > 0 && ready == desired {
			node.Ready = true
		}
	}
	return node
}

// downwardSelector describes one (targetKind, labelSelector) hop the
// tree-walker should chase.
type downwardSelector struct {
	targetKind string
	selector   string
}

// deriveDownwardSelectors produces the canonical label-selector hops a
// resource exposes to the tree-walker. Centralised here so the resource
// tree behaves consistently across types.
func deriveDownwardSelectors(kind k8scache.Kind, obj *unstructured.Unstructured) []downwardSelector {
	if obj == nil {
		return nil
	}
	out := []downwardSelector{}
	switch strings.ToLower(kind.Name) {
	case "deployment", "statefulset", "daemonset", "replicaset":
		// spec.selector.matchLabels → Pod labels.
		if labels, ok, _ := unstructured.NestedStringMap(obj.Object, "spec", "selector", "matchLabels"); ok && len(labels) > 0 {
			out = append(out, downwardSelector{targetKind: "pod", selector: labelsToSelector(labels)})
		}
	case "service":
		if labels, ok, _ := unstructured.NestedStringMap(obj.Object, "spec", "selector"); ok && len(labels) > 0 {
			out = append(out, downwardSelector{targetKind: "pod", selector: labelsToSelector(labels)})
			// Service → EndpointSlice via discovery label
			out = append(out, downwardSelector{
				targetKind: "endpointslice",
				selector:   fmt.Sprintf("kubernetes.io/service-name=%s", obj.GetName()),
			})
		}
	}
	return out
}

// labelsToSelector renders a label map as a Kubernetes label-selector
// string suitable for k8s.io/apimachinery/labels.Parse.
func labelsToSelector(in map[string]string) string {
	parts := make([]string, 0, len(in))
	for k, v := range in {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}

// canonicalKindByRefKind resolves an ownerReference.Kind ("Deployment")
// to its canonical Registry name ("deployment"). Returns "" when no
// match — the tree-walker silently skips unregistered owner kinds (the
// resource exists but we don't watch it, so we can't visualise it).
func canonicalKindByRefKind(reg *k8scache.Registry, refKind string) string {
	want := strings.ToLower(refKind)
	for _, n := range reg.Names() {
		if strings.ToLower(n) == want {
			return n
		}
	}
	return ""
}

// findIndexerObject scans the named indexer for an object matching
// (ns, name). O(N) — fine for typical indexer cardinality (a few
// hundred objects per kind). The indexer cache is the source of truth;
// no apiserver call.
func (h *Handler) findIndexerObject(clusterID, kindName, ns, name string) *unstructured.Unstructured {
	if h.k8sCache == nil {
		return nil
	}
	// Use the Factory's List() — already-redacted, indexer-only path.
	items, _, err := h.k8sCache.List(clusterID, kindName, nil)
	if err != nil {
		return nil
	}
	for _, it := range items {
		if it.GetName() != name {
			continue
		}
		if ns != "" && it.GetNamespace() != ns {
			continue
		}
		return it
	}
	return nil
}
