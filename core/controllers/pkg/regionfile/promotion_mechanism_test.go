package regionfile

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func hr(values map[string]interface{}) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]interface{}{"name": "x", "namespace": "n"},
		"spec":       map[string]interface{}{"values": values},
	}}
}

func topo(mode string) map[string]interface{} {
	return map[string]interface{}{"openova": map[string]interface{}{"topology": map[string]interface{}{
		"mode": mode, "self": "a", "primary": "a", "peers": []interface{}{"b"}}}}
}

func TestDeclarePromotionMechanism(t *testing.T) {
	multi := hr(topo("active-hot-standby"))
	single := hr(topo("singleton"))
	manual := hr(topo("active-hot-standby"))
	_ = unstructured.SetNestedField(manual.Object, "manual", "spec", "values", "database", "topology", "promotion", "mechanism")
	plain := hr(map[string]interface{}{"replicas": int64(1)})
	cm := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]interface{}{"name": "bp-x-topology", "namespace": "flux-system"},
		"data":     map[string]interface{}{TopologyValuesKey: "openova:\n  topology:\n    mode: active-hot-standby\n"},
	}}
	n, err := DeclarePromotionMechanism([]*unstructured.Unstructured{multi, single, manual, plain, cm}, "continuum")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("changed=%d, want 1 (only the undeclared multi-region HelmRelease)", n)
	}
	get := func(o *unstructured.Unstructured) string {
		s, _, _ := unstructured.NestedString(o.Object, "spec", "values", "database", "topology", "promotion", "mechanism")
		return s
	}
	if got := get(multi); got != "continuum" {
		t.Errorf("multi-region HR mechanism=%q, want continuum", got)
	}
	if got := get(single); got != "" {
		t.Errorf("singleton HR mechanism=%q, want unset", got)
	}
	if got := get(manual); got != "manual" {
		t.Errorf("explicit manual overwritten: %q", got)
	}
	if got := get(plain); got != "" {
		t.Errorf("HR without topology contract got mechanism %q", got)
	}
	if raw, _, _ := unstructured.NestedString(cm.Object, "data", TopologyValuesKey); raw != "openova:\n  topology:\n    mode: active-hot-standby\n" {
		t.Errorf("topology ConfigMap modified: %q", raw)
	}
}
