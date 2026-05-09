// tree_test.go — coverage for GetResourcesByOwner +
// GetResourcesBySelector + DynamicClientFor + RedactForKind (added by
// EPIC-4 Slice R, #1099).
package k8scache

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgokubernetes "k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
)

func newReplicaSet(ns, name, ownerKind, ownerName string, matchLabels map[string]string) *unstructured.Unstructured {
	owners := []any{
		map[string]any{
			"apiVersion": "apps/v1",
			"kind":       ownerKind,
			"name":       ownerName,
			"uid":        "uid-" + ownerName,
		},
	}
	matchLabelsAny := map[string]any{}
	for k, v := range matchLabels {
		matchLabelsAny[k] = v
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "ReplicaSet",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-rs-" + name,
			"ownerReferences": owners,
			"labels":          matchLabelsAny,
		},
		"spec": map[string]any{
			"selector": map[string]any{
				"matchLabels": matchLabelsAny,
			},
		},
	}}
	return u
}

func newPodWithLabels(ns, name string, lbls map[string]string, owner string) *unstructured.Unstructured {
	owners := []any{}
	if owner != "" {
		owners = append(owners, map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "ReplicaSet",
			"name":       owner,
			"uid":        "uid-rs-" + owner,
		})
	}
	lblsAny := map[string]any{}
	for k, v := range lbls {
		lblsAny[k] = v
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"namespace":       ns,
			"name":            name,
			"uid":             "uid-pod-" + name,
			"ownerReferences": owners,
			"labels":          lblsAny,
		},
		"spec": map[string]any{},
	}}
}

func fakeClientsWithRS(objs ...runtime.Object) (*dynamicfake.FakeDynamicClient, clientgokubernetes.Interface) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "PodList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSetList"}, &unstructured.UnstructuredList{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "ReplicaSet"}, &unstructured.Unstructured{})
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "pods"}:                          "PodList",
		{Group: "apps", Version: "v1", Resource: "replicasets"}:    "ReplicaSetList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
	core := kfake.NewSimpleClientset()
	return dyn, core
}

func registryWithRSAndPod() *Registry {
	r := NewRegistry()
	_ = r.Add(Kind{Name: "pod", GVR: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, Namespaced: true})
	_ = r.Add(Kind{Name: "replicaset", GVR: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, Namespaced: true})
	return r
}

func waitForList(t *testing.T, f *Factory, kind string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, _, err := f.List("alpha", kind, labels.Everything())
		if err == nil && len(items) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("informer %q never reached %d items", kind, want)
}

func TestGetResourcesByOwner_OwnerWalk(t *testing.T) {
	rs := newReplicaSet("default", "wp-67", "Deployment", "wp", map[string]string{"app": "wp"})
	pod := newPodWithLabels("default", "wp-67-abc", map[string]string{"app": "wp"}, "wp-67")
	dyn, core := fakeClientsWithRS(rs, pod)

	cfg := Config{
		Logger:   quietLogger(),
		Registry: registryWithRSAndPod(),
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForList(t, f, "pod", 1)
	waitForList(t, f, "replicaset", 1)

	// ReplicaSet is owned by Deployment "wp" in default ns.
	owned, err := f.GetResourcesByOwner("alpha", "Deployment", "default", "wp")
	if err != nil {
		t.Fatalf("GetResourcesByOwner: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("owned count: got %d want 1", len(owned))
	}
	if owned[0].GetName() != "wp-67" {
		t.Fatalf("unexpected owned name: %q", owned[0].GetName())
	}

	// Pod is owned by ReplicaSet "wp-67".
	owned, err = f.GetResourcesByOwner("alpha", "ReplicaSet", "default", "wp-67")
	if err != nil {
		t.Fatalf("pod-by-rs: %v", err)
	}
	if len(owned) != 1 {
		t.Fatalf("pod owned count: got %d want 1", len(owned))
	}
}

func TestGetResourcesBySelector_LabelMatch(t *testing.T) {
	pod1 := newPodWithLabels("default", "wp-1", map[string]string{"app": "wp", "env": "prod"}, "")
	pod2 := newPodWithLabels("default", "wp-2", map[string]string{"app": "wp", "env": "dev"}, "")
	pod3 := newPodWithLabels("default", "other", map[string]string{"app": "other"}, "")
	dyn, core := fakeClientsWithRS(pod1, pod2, pod3)

	cfg := Config{
		Logger:   quietLogger(),
		Registry: registryWithRSAndPod(),
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForList(t, f, "pod", 3)

	// app=wp matches 2 pods.
	matched, err := f.GetResourcesBySelector("alpha", "pod", "default", "app=wp")
	if err != nil {
		t.Fatalf("GetResourcesBySelector: %v", err)
	}
	if len(matched) != 2 {
		t.Fatalf("app=wp count: got %d want 2", len(matched))
	}

	// app=wp,env=prod matches 1.
	matched, err = f.GetResourcesBySelector("alpha", "pod", "default", "app=wp,env=prod")
	if err != nil {
		t.Fatalf("compound selector: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("compound count: got %d want 1", len(matched))
	}
	if matched[0].GetName() != "wp-1" {
		t.Fatalf("matched name: %q", matched[0].GetName())
	}
}

func TestGetResourcesBySelector_BadSelectorErrors(t *testing.T) {
	cfg := Config{
		Logger:   quietLogger(),
		Registry: registryWithRSAndPod(),
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), CoreClient: kfake.NewSimpleClientset()}},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	if _, err := f.GetResourcesBySelector("alpha", "pod", "default", "this=is=invalid"); err == nil {
		t.Fatalf("expected parse error on invalid selector")
	}
}

func TestDynamicClientFor_ReturnsRegisteredClient(t *testing.T) {
	dyn, core := fakeClients()
	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	got, err := f.DynamicClientFor("alpha")
	if err != nil {
		t.Fatalf("DynamicClientFor: %v", err)
	}
	if got == nil {
		t.Fatalf("got nil dynamic client")
	}
	if _, err := f.DynamicClientFor("missing"); err == nil {
		t.Fatalf("expected error for unknown cluster")
	}
}

func TestRedactForKind_StripsSecretBody(t *testing.T) {
	dyn, core := fakeClients()
	cfg := Config{
		Logger:   quietLogger(),
		Registry: minimalRegistry(),
		Clusters: []ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: core}},
	}
	f, err := NewFactory(cfg)
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	defer f.Stop()
	sec := newSecret("default", "creds", map[string]any{"password": "c2hocg=="})
	secKind, _ := f.Registry().Get("secret")
	red := f.RedactForKind(secKind, sec)
	if _, ok, _ := unstructured.NestedMap(red.Object, "data"); ok {
		t.Fatalf("secret data should be redacted")
	}
}

// Quiet the unused-import linter on metav1 for future tests.
var _ = metav1.GetOptions{}
