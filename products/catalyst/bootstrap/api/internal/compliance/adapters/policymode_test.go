package adapters

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

func policyObj(apiVersion, kind string, spec map[string]any) *unstructured.Unstructured {
	o := map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": "p"}}
	if spec != nil {
		o["spec"] = spec
	}
	return &unstructured.Unstructured{Object: o}
}

func rule(failureAction string) map[string]any {
	return map[string]any{"name": "r", "validate": map[string]any{"failureAction": failureAction}}
}

// TestPolicyModeReadsBothKinds: one case per sentence of the rule of Mode.
func TestPolicyModeReadsBothKinds(t *testing.T) {
	cases := []struct {
		name     string
		obj      *unstructured.Unstructured
		wantMode string
		wantOK   bool
	}{
		{"new kind, Deny", policyObj("policies.kyverno.io/v1", "ValidatingPolicy", map[string]any{"validationActions": []any{"Deny"}}), "enforce", true},
		{"new kind, Deny after Audit, lower case", policyObj("policies.kyverno.io/v1", "ValidatingPolicy", map[string]any{"validationActions": []any{"Audit", " deny "}}), "enforce", true},
		{"new kind, storage version v1beta1", policyObj("policies.kyverno.io/v1beta1", "ValidatingPolicy", map[string]any{"validationActions": []any{"Deny"}}), "enforce", true},
		{"new kind, Audit and Warn", policyObj("policies.kyverno.io/v1", "ValidatingPolicy", map[string]any{"validationActions": []any{"Audit", "Warn"}}), "audit", true},
		{"new kind, no actions", policyObj("policies.kyverno.io/v1", "ValidatingPolicy", map[string]any{}), "audit", true},
		{"new kind read by its own fields only", policyObj("policies.kyverno.io/v1", "ValidatingPolicy", map[string]any{"validationFailureAction": "Enforce", "rules": []any{rule("Enforce")}}), "audit", true},
		{"mutating kind, Deny", policyObj("policies.kyverno.io/v1", "MutatingPolicy", map[string]any{"validationActions": []any{"Deny"}}), "enforce", true},
		{"mutating kind, no actions", policyObj("policies.kyverno.io/v1", "MutatingPolicy", map[string]any{}), "audit", true},
		{"legacy kind, spec Enforce", policyObj("kyverno.io/v1", "ClusterPolicy", map[string]any{"validationFailureAction": "Enforce"}), "enforce", true},
		{"legacy kind, spec lower case", policyObj("kyverno.io/v1", "ClusterPolicy", map[string]any{"validationFailureAction": "enforce"}), "enforce", true},
		{"legacy kind, second rule Enforce", policyObj("kyverno.io/v1", "ClusterPolicy", map[string]any{"validationFailureAction": "Audit", "rules": []any{rule("Audit"), rule("Enforce")}}), "enforce", true},
		{"legacy kind, all Audit", policyObj("kyverno.io/v1", "ClusterPolicy", map[string]any{"validationFailureAction": "Audit", "rules": []any{rule("Audit")}}), "audit", true},
		{"legacy kind read by its own fields only", policyObj("kyverno.io/v1", "ClusterPolicy", map[string]any{"validationActions": []any{"Deny"}}), "audit", true},
		{"legacy namespaced kind", policyObj("kyverno.io/v1", "Policy", map[string]any{"validationFailureAction": "Enforce"}), "", false},
		{"new kind name in the legacy group", policyObj("kyverno.io/v1", "ValidatingPolicy", map[string]any{"validationActions": []any{"Deny"}}), "", false},
		{"legacy kind name in the new group", policyObj("policies.kyverno.io/v1", "ClusterPolicy", map[string]any{"validationFailureAction": "Enforce"}), "", false},
		{"a report", policyObj("wgpolicyk8s.io/v1alpha2", "ClusterPolicyReport", nil), "", false},
		{"no object", nil, "", false},
	}
	for _, c := range cases {
		mode, ok := PolicyMode{}.Mode(c.obj)
		if mode != c.wantMode || ok != c.wantOK {
			t.Errorf("%s: Mode = (%q, %v), want (%q, %v)", c.name, mode, ok, c.wantMode, c.wantOK)
		}
	}
}

// TestPolicyModeKindsAreRegistered: every informer kind the adapter reads is
// in the default registry with the GVR of its group, cluster-scoped; the two
// new kinds at version v1 (spike CMP-S1) and Optional, the legacy kind not.
func TestPolicyModeKindsAreRegistered(t *testing.T) {
	want := map[string]struct {
		gvr      schema.GroupVersionResource
		optional bool
	}{
		"clusterpolicy":    {schema.GroupVersionResource{Group: "kyverno.io", Version: "v1", Resource: "clusterpolicies"}, false},
		"validatingpolicy": {schema.GroupVersionResource{Group: "policies.kyverno.io", Version: "v1", Resource: "validatingpolicies"}, true},
		"mutatingpolicy":   {schema.GroupVersionResource{Group: "policies.kyverno.io", Version: "v1", Resource: "mutatingpolicies"}, true},
	}
	kinds := PolicyMode{}.Kinds()
	if len(kinds) != len(want) {
		t.Fatalf("Kinds() = %v, want the %d kinds %v", kinds, len(want), want)
	}
	registered := map[string]k8scache.Kind{}
	for _, k := range k8scache.DefaultKinds {
		registered[k.Name] = k
	}
	for _, name := range kinds {
		w, known := want[name]
		if !known {
			t.Errorf("Kinds() holds %q, which is not a policy kind", name)
			continue
		}
		k, ok := registered[name]
		if !ok {
			t.Errorf("kind %q is not in k8scache.DefaultKinds", name)
			continue
		}
		if k.GVR != w.gvr || k.Namespaced || k.Optional != w.optional {
			t.Errorf("kind %q registered as %v namespaced=%v optional=%v, want %v namespaced=false optional=%v", name, k.GVR, k.Namespaced, k.Optional, w.gvr, w.optional)
		}
	}
}
