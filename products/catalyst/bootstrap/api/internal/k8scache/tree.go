// tree.go — owner+selector lookups across a Sovereign's informer
// indexers. Powers EPIC-4 Slice R2 (#1099) Resource Tree.
//
// Both operations read from the in-process Indexer (already populated by
// the watch stream) — NEVER hit the apiserver. Per ADR-0001 §5 the
// SSE consumers + this resource-tree path share one watch surface.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//
//	#3 (event-driven) — no apiserver calls; everything served from the
//	   already-warmed Indexers.
//	#4 (never hardcode) — kind discovery comes from the live Registry.
package k8scache

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

// GetResourcesByOwner returns every cached object across all kinds
// whose `metadata.ownerReferences` contains an entry matching the
// supplied (kind, name) pair in the same namespace.
//
// Cluster-scoped owners are matched on (kind, name) only — `ns` is
// ignored when the candidate kind is cluster-scoped (this is correct:
// a Namespace owns nothing namespaced and cluster-scoped resources
// like CRDs may have cluster-scoped owners).
//
// Match semantics: case-insensitive on Kind, exact on Name. We do NOT
// match on UID — the resource tree consumer wants "what's owned by the
// Deployment named X", which is more useful than UID-based matching
// when scrolling through the cache (the Pod's ownerReference points at
// a ReplicaSet UID; the tree walker walks via name+kind hops regardless).
//
// Returns the redacted form (Sensitive kinds scrubbed). Caller can
// safely include the slice in an HTTP response.
func (f *Factory) GetResourcesByOwner(clusterID, ownerKind, ownerNs, ownerName string) ([]*unstructured.Unstructured, error) {
	f.mu.RLock()
	cs, ok := f.clusters[clusterID]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("k8scache: cluster %q not registered", clusterID)
	}
	out := []*unstructured.Unstructured{}
	for kindName, idx := range cs.indexers {
		kind, ok := f.registry.Get(kindName)
		if !ok {
			continue
		}
		for _, item := range idx.List() {
			u, ok := item.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			// Namespace must match for namespaced owners. Cluster-scoped
			// owners (Node, Namespace, CRD) match anywhere — but in
			// practice nothing is owned by a Namespace, so this branch
			// almost never fires.
			if ownerNs != "" && u.GetNamespace() != "" && u.GetNamespace() != ownerNs {
				continue
			}
			refs := u.GetOwnerReferences()
			for _, ref := range refs {
				if !equalKind(ref.Kind, ownerKind) {
					continue
				}
				if ref.Name != ownerName {
					continue
				}
				out = append(out, redactObject(kind, u))
				break
			}
		}
	}
	return out, nil
}

// GetResourcesBySelector returns every cached object of `targetKind`
// in `ns` whose labels match the supplied selector. Used by the resource
// tree to walk Service→Pod via label selector and Deployment→ReplicaSet
// matching pod-template labels.
//
// `selector` is a Kubernetes-style label selector string (e.g.
// `app=wordpress,tier=frontend`). An empty selector matches every
// object in the namespace.
func (f *Factory) GetResourcesBySelector(clusterID, targetKind, ns, selector string) ([]*unstructured.Unstructured, error) {
	f.mu.RLock()
	cs, ok := f.clusters[clusterID]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("k8scache: cluster %q not registered", clusterID)
	}
	kind, ok := f.registry.Get(targetKind)
	if !ok {
		return nil, fmt.Errorf("k8scache: kind %q not registered", targetKind)
	}
	// Indexers are keyed by canonical singular Kind.Name (set in
	// AddCluster). Resolve via the Kind we just looked up — `targetKind`
	// itself may be a plural alias ("services") or short-form ("svc").
	idx, ok := cs.indexers[CanonicalKindName(kind.Name)]
	if !ok {
		return nil, fmt.Errorf("k8scache: kind %q has no indexer on cluster %q", targetKind, clusterID)
	}

	sel := labels.Everything()
	if selector != "" {
		parsed, err := labels.Parse(selector)
		if err != nil {
			return nil, fmt.Errorf("k8scache: parse selector %q: %w", selector, err)
		}
		sel = parsed
	}

	out := []*unstructured.Unstructured{}
	for _, item := range idx.List() {
		u, ok := item.(*unstructured.Unstructured)
		if !ok {
			continue
		}
		if ns != "" && u.GetNamespace() != ns {
			continue
		}
		if !sel.Matches(labels.Set(u.GetLabels())) {
			continue
		}
		out = append(out, redactObject(kind, u))
	}
	return out, nil
}

// equalKind compares two K8s kind names case-insensitively.
//
// ownerReferences carry the Kind of the owning resource as it appears
// in the apiVersion+kind tuple ("Deployment", "ReplicaSet"). The
// Registry stores canonical lower-case names ("deployment", "replicaset").
// The caller normalises by passing the human-friendly form;
// equalKind handles both directions.
func equalKind(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca := a[i]
		cb := b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
