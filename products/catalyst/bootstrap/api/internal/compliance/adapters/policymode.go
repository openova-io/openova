package adapters

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/compliance"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// The two modes of a policy of the engine.
const (
	ModeEnforce = "enforce"
	ModeAudit   = "audit"
)

// LegacyPolicyGroup is the API group of the legacy policy kinds ClusterPolicy
// and Policy; PolicyGroup is the API group of the kinds that replace them.
const (
	LegacyPolicyGroup = "kyverno.io"
	PolicyGroup       = "policies.kyverno.io"
)

// PolicyMode reads the mode of the engine's policy objects: the two legacy
// kinds and the two new kinds. It keeps no state and never calls an API server.
type PolicyMode struct{}

// Kinds returns the informer kinds of the policy objects: the two legacy
// kinds first (ClusterPolicy, then the namespaced Policy), then the two new
// kinds.
func (PolicyMode) Kinds() []string {
	return []string{"clusterpolicy", k8scache.KindPolicy, k8scache.KindValidatingPolicy, k8scache.KindMutatingPolicy}
}

// PolicyName is the name the engine's reports give a policy object, and so
// the name the policies route lists it by: <namespace>/<name> for an object of
// a namespaced kind, the bare name for a cluster-scoped one (CMP-024).
func PolicyName(obj *unstructured.Unstructured) string {
	if ns := obj.GetNamespace(); ns != "" {
		return ns + "/" + obj.GetName()
	}
	return obj.GetName()
}

// Mode returns the mode of one policy object, "enforce" or "audit", and false
// for an object of any other kind. A ValidatingPolicy or a MutatingPolicy of
// group policies.kyverno.io enforces when spec.validationActions holds Deny.
// A ClusterPolicy or a Policy of group kyverno.io enforces when
// spec.validationFailureAction is Enforce, or when validate.failureAction of
// one of its rules is. Each kind is read only by its own fields; every other
// policy of the four kinds audits. Upper and lower case are the same.
func (PolicyMode) Mode(obj *unstructured.Unstructured) (string, bool) {
	if obj == nil {
		return "", false
	}
	gv, err := schema.ParseGroupVersion(obj.GetAPIVersion())
	if err != nil {
		return "", false
	}
	switch {
	case gv.Group == PolicyGroup && (obj.GetKind() == "ValidatingPolicy" || obj.GetKind() == "MutatingPolicy"):
		return newKindMode(obj), true
	case gv.Group == LegacyPolicyGroup && (obj.GetKind() == "ClusterPolicy" || obj.GetKind() == "Policy"):
		return legacyKindMode(obj), true
	}
	return "", false
}

// newKindMode: Deny in spec.validationActions enforces.
func newKindMode(obj *unstructured.Unstructured) string {
	actions, _, _ := unstructured.NestedSlice(obj.Object, "spec", "validationActions")
	for _, a := range actions {
		if s, _ := a.(string); isWord(s, "Deny") {
			return ModeEnforce
		}
	}
	return ModeAudit
}

// legacyKindMode: Enforce in spec.validationFailureAction or in the
// validate.failureAction of any rule enforces.
func legacyKindMode(obj *unstructured.Unstructured) string {
	if vfa, _, _ := unstructured.NestedString(obj.Object, "spec", "validationFailureAction"); isWord(vfa, "Enforce") {
		return ModeEnforce
	}
	rules, _, _ := unstructured.NestedSlice(obj.Object, "spec", "rules")
	for _, ri := range rules {
		r, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		if fa, _, _ := unstructured.NestedString(r, "validate", "failureAction"); isWord(fa, "Enforce") {
			return ModeEnforce
		}
	}
	return ModeAudit
}

func isWord(s, word string) bool { return strings.EqualFold(strings.TrimSpace(s), word) }

// Store is the informer store of the clusters a process watches: the
// clusters it holds and the objects of one kind. *k8scache.Factory
// satisfies it.
type Store interface {
	compliance.Lister
	Clusters() []string
}

// PolicyObjects reads the policy objects of one cluster from the store of
// each kind of PolicyMode.Kinds at the call, and returns by PolicyName what
// read makes of them: the first kind whose object of a name read accepts
// gives the entry, the legacy kinds first (during an in-place rewrite two
// kinds hold the name). A namespaced object never takes the entry of a
// cluster-scoped one: its name holds the slash of <namespace>/<name>. held is
// false for a cluster the store does not hold.
//
// The informer keeps its store exact; an event of the fanout may never
// reach a subscriber: Factory.fanout drops the oldest event of a full
// subscriber buffer, an event sent before a subscriber subscribed reaches
// none, and with resync 0 neither is sent again. A mode kept from the
// events alone therefore stayed audit after the engine object flipped to
// enforce (CMP-023). A kind whose List fails has no informer on the
// cluster (an optional kind the cluster does not serve, CMP-016), or the
// cluster left the store after Clusters was read: neither has an object.
func PolicyObjects[T any](s Store, cluster string, read func(*unstructured.Unstructured) (T, bool)) (byName map[string]T, held bool) {
	if !slices.Contains(s.Clusters(), cluster) {
		return nil, false
	}
	byName = map[string]T{}
	for _, kind := range (PolicyMode{}).Kinds() {
		objs, _, err := s.List(cluster, kind, nil)
		if err != nil {
			continue
		}
		for _, o := range objs {
			name := PolicyName(o)
			if _, taken := byName[name]; taken {
				continue
			}
			if v, ok := read(o); ok {
				byName[name] = v
			}
		}
	}
	return byName, true
}
