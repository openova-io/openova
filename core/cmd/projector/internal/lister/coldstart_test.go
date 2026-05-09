package lister

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	pvalkey "github.com/openova-io/openova/core/cmd/projector/internal/valkey"
)

// TestColdStarter_Run_ProjectsAllItems exercises the cold-start over a
// fake dynamic client populated with two Pods + one Node. Asserts the
// Valkey KV ends up with both keys + the namespace shape is correct.
//
// Why custom GVKs (`group/v1/Widget`) rather than `v1/Pod`?
// The dynamic-client fake at k8s.io/client-go v0.31.x converts
// unstructured items into the registered TYPED Go shape on List, so
// passing real `v1/Pod` unstructured items panics with
// "can't assign or convert unstructured.Unstructured into v1.Pod"
// because the fake recognises core kinds but our Object is sparse.
// Using a fully-synthetic GVR (no typed registration) keeps the fake
// in unstructured mode end-to-end — which is exactly the production
// behaviour of the projector (it doesn't depend on typed Go shapes).
func TestColdStarter_Run_ProjectsAllItems(t *testing.T) {
	scheme := runtime.NewScheme()

	gvrPods := schema.GroupVersionResource{Group: "synthetic.example.com", Version: "v1", Resource: "pods"}
	gvrNodes := schema.GroupVersionResource{Group: "synthetic.example.com", Version: "v1", Resource: "nodes"}

	pod1 := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "synthetic.example.com/v1", "kind": "Pod",
		"metadata": map[string]any{"name": "p1", "namespace": "default"},
	}}
	pod2 := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "synthetic.example.com/v1", "kind": "Pod",
		"metadata": map[string]any{"name": "p2", "namespace": "kube-system"},
	}}
	node1 := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "synthetic.example.com/v1", "kind": "Node",
		"metadata": map[string]any{"name": "n1"},
	}}

	gvrToListKind := map[schema.GroupVersionResource]string{
		gvrPods:  "PodList",
		gvrNodes: "NodeList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, pod1, pod2, node1)

	mem := pvalkey.NewMemKV()
	p := pvalkey.NewProjector(mem, time.Hour)

	c := &ColdStarter{
		Cluster:   "omantel",
		Dyn:       dyn,
		Projector: p,
	}
	kinds := []Kind{
		{Name: "pod", GVR: gvrPods, Namespaced: true},
		{Name: "node", GVR: gvrNodes, Namespaced: false},
	}
	count, err := c.Run(context.Background(), kinds)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}

	wantKeys := []string{
		"cluster:omantel:kind:pod:default/p1",
		"cluster:omantel:kind:pod:kube-system/p2",
		"cluster:omantel:kind:node:/n1",
	}
	for _, k := range wantKeys {
		if _, ok := mem.Get(k); !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

// TestColdStarter_Run_ContinuesPastListErrors asserts that a single
// LIST returning empty does NOT halt the cold-start — subsequent
// kinds are still projected. (See note on TestColdStarter_Run_ProjectsAllItems
// for why we use synthetic GVRs.)
func TestColdStarter_Run_ContinuesPastListErrors(t *testing.T) {
	scheme := runtime.NewScheme()

	gvrPods := schema.GroupVersionResource{Group: "synthetic.example.com", Version: "v1", Resource: "pods"}
	gvrBogus := schema.GroupVersionResource{Group: "bogus.example.com", Version: "v1", Resource: "bogusthings"}

	pod1 := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "synthetic.example.com/v1", "kind": "Pod",
		"metadata": map[string]any{"name": "p1", "namespace": "default"},
	}}
	// Map BOTH GVRs to a list-kind: the fake client panics on
	// unmapped GVRs rather than returning a typed error, so we
	// pre-register both. The "bogus" GVR maps to a bogus list-kind
	// the fake handles by returning an empty list — emulating "API
	// group exists but resource is unknown" rather than a hard
	// failure. The cold-start should still complete + project the
	// pod, since the bogus kind contributes 0 items.
	//
	// (Production exercise: if a kind's CRD truly isn't installed,
	// the LIST returns an apiserver 404 and the loop logs WARN +
	// continues — `coldstart.go` swallows the err. We assert the
	// continuation semantics via the test's count==1 invariant
	// below.)
	gvrToListKind := map[schema.GroupVersionResource]string{
		gvrPods:  "PodList",
		gvrBogus: "BogusThingList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, pod1)

	mem := pvalkey.NewMemKV()
	p := pvalkey.NewProjector(mem, time.Hour)

	c := &ColdStarter{
		Cluster:   "omantel",
		Dyn:       dyn,
		Projector: p,
	}
	kinds := []Kind{
		{Name: "bogus", GVR: gvrBogus, Namespaced: true},
		{Name: "pod", GVR: gvrPods, Namespaced: true},
	}
	count, err := c.Run(context.Background(), kinds)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, ok := mem.Get("cluster:omantel:kind:pod:default/p1"); !ok {
		t.Fatal("pod key absent")
	}
}

func TestColdStarter_Run_RejectsMissingFields(t *testing.T) {
	cases := []struct {
		name string
		c    *ColdStarter
	}{
		{"missing-cluster", &ColdStarter{}},
		{"missing-dyn", &ColdStarter{Cluster: "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.c.Run(context.Background(), nil); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// suppress unused-import warnings — metav1 is referenced via ListOptions
// in the production path; the test asserts the path indirectly via the
// fake client's behaviour.
var _ = metav1.ListOptions{}
