package switchover

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func newHTTPRoute(ns, name string, backendsByRegion map[string]int) *unstructured.Unstructured {
	hr := &unstructured.Unstructured{}
	hr.SetGroupVersionKind(schema.GroupVersionKind{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute"})
	hr.SetNamespace(ns)
	hr.SetName(name)

	brs := []interface{}{}
	for region, weight := range backendsByRegion {
		brs = append(brs, map[string]interface{}{
			"name":   "demo-app-" + region,
			"weight": int64(weight),
			"port":   int64(8080),
		})
	}
	rules := []interface{}{
		map[string]interface{}{
			"backendRefs": brs,
		},
	}
	_ = unstructured.SetNestedSlice(hr.Object, rules, "spec", "rules")
	return hr
}

func TestDynamicHTTPRouteDrainer_SetWeightZero_RegionMatch(t *testing.T) {
	t.Parallel()
	hr := newHTTPRoute("demo", "demo-app", map[string]int{"fsn": 100, "hel": 0})
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HTTPRouteGVR: "HTTPRouteList"}, hr)
	d := NewDynamicHTTPRouteDrainer(dyn)

	prior, err := d.SetWeightZero(context.Background(), "demo", "demo-app", "fsn")
	if err != nil {
		t.Fatalf("SetWeightZero: %v", err)
	}
	if len(prior) != 1 || prior[0] != 100 {
		t.Errorf("prior = %v want [100]", prior)
	}

	got, _ := dyn.Resource(HTTPRouteGVR).Namespace("demo").Get(context.Background(), "demo-app", metav1.GetOptions{})
	rules, _, _ := unstructured.NestedSlice(got.Object, "spec", "rules")
	rm, _ := rules[0].(map[string]interface{})
	brs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
	for _, br := range brs {
		bm := br.(map[string]interface{})
		name, _, _ := unstructured.NestedString(bm, "name")
		w := intWeight(bm)
		if name == "demo-app-fsn" && w != 0 {
			t.Errorf("fsn backend weight = %d want 0", w)
		}
		if name == "demo-app-hel" && w != 0 {
			// hel was already 0 — should be unchanged.
			t.Errorf("hel backend weight = %d want 0", w)
		}
	}
}

func TestDynamicHTTPRouteDrainer_SetWeightZero_NoMatchDrainsAll(t *testing.T) {
	t.Parallel()
	// HTTPRoute uses bare names (no -region suffix) — fallback path
	// drains EVERY backend.
	hr := newHTTPRoute("demo", "demo-app", map[string]int{"X": 100})
	// Override the name to break the convention.
	rules, _, _ := unstructured.NestedSlice(hr.Object, "spec", "rules")
	rm := rules[0].(map[string]interface{})
	brs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
	br := brs[0].(map[string]interface{})
	br["name"] = "demo-app"
	brs[0] = br
	_ = unstructured.SetNestedSlice(rm, brs, "backendRefs")
	rules[0] = rm
	_ = unstructured.SetNestedSlice(hr.Object, rules, "spec", "rules")

	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HTTPRouteGVR: "HTTPRouteList"}, hr)
	d := NewDynamicHTTPRouteDrainer(dyn)

	if _, err := d.SetWeightZero(context.Background(), "demo", "demo-app", "fsn"); err != nil {
		t.Fatalf("SetWeightZero: %v", err)
	}
	got, _ := dyn.Resource(HTTPRouteGVR).Namespace("demo").Get(context.Background(), "demo-app", metav1.GetOptions{})
	rules, _, _ = unstructured.NestedSlice(got.Object, "spec", "rules")
	rm = rules[0].(map[string]interface{})
	brs, _, _ = unstructured.NestedSlice(rm, "backendRefs")
	for _, br := range brs {
		bm := br.(map[string]interface{})
		if intWeight(bm) != 0 {
			t.Errorf("fallback drain didn't zero weight: %v", bm)
		}
	}
}

func TestDynamicHTTPRouteDrainer_RestoreWeights(t *testing.T) {
	t.Parallel()
	hr := newHTTPRoute("demo", "demo-app", map[string]int{"fsn": 0})
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HTTPRouteGVR: "HTTPRouteList"}, hr)
	d := NewDynamicHTTPRouteDrainer(dyn)

	if err := d.RestoreWeights(context.Background(), "demo", "demo-app", []int{75}); err != nil {
		t.Fatalf("RestoreWeights: %v", err)
	}
	got, _ := dyn.Resource(HTTPRouteGVR).Namespace("demo").Get(context.Background(), "demo-app", metav1.GetOptions{})
	rules, _, _ := unstructured.NestedSlice(got.Object, "spec", "rules")
	rm := rules[0].(map[string]interface{})
	brs, _, _ := unstructured.NestedSlice(rm, "backendRefs")
	bm := brs[0].(map[string]interface{})
	if intWeight(bm) != 75 {
		t.Errorf("restored weight = %d want 75", intWeight(bm))
	}
}

func TestDynamicHTTPRouteDrainer_NotFound(t *testing.T) {
	t.Parallel()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{HTTPRouteGVR: "HTTPRouteList"})
	d := NewDynamicHTTPRouteDrainer(dyn)
	if _, err := d.SetWeightZero(context.Background(), "demo", "missing", "fsn"); err == nil {
		t.Fatal("expected error on missing HTTPRoute")
	}
}
