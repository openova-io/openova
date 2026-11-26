package adapters

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

// The two modes of a policy of the engine.
const (
	ModeEnforce = "enforce"
	ModeAudit   = "audit"
)

// LegacyPolicyGroup is the API group of the legacy policy kind ClusterPolicy;
// PolicyGroup is the API group of the kinds that replace it.
const (
	LegacyPolicyGroup = "kyverno.io"
	PolicyGroup       = "policies.kyverno.io"
)

// PolicyMode reads the mode of the engine's policy objects: the legacy kind
// and the two new kinds. It keeps no state and never calls an API server.
type PolicyMode struct{}

// Kinds returns the informer kinds of the policy objects: the legacy kind
// first, then the two new kinds.
func (PolicyMode) Kinds() []string {
	return []string{"clusterpolicy", k8scache.KindValidatingPolicy, k8scache.KindMutatingPolicy}
}

// Mode returns the mode of one policy object, "enforce" or "audit", and false
// for an object of any other kind. A ValidatingPolicy or a MutatingPolicy of
// group policies.kyverno.io enforces when spec.validationActions holds Deny.
// A ClusterPolicy of group kyverno.io enforces when spec.validationFailureAction
// is Enforce, or when validate.failureAction of one of its rules is. Each kind
// is read only by its own fields; every other policy of the three kinds
// audits. Upper and lower case are the same.
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
	case gv.Group == LegacyPolicyGroup && obj.GetKind() == "ClusterPolicy":
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
