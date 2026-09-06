package handler

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"
)

func prefixTestApp(name string, labels map[string]string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	o.SetKind("Application")
	o.SetName(name)
	o.SetNamespace(spineApplicationNamespace)
	o.SetLabels(labels)
	return o
}

func newDyn(objs ...runtime.Object) *dynfake.FakeDynamicClient {
	sch := runtime.NewScheme()
	gvr := ApplicationGVR()
	return dynfake.NewSimpleDynamicClientWithCustomListKinds(sch,
		map[schema.GroupVersionResource]string{gvr: "ApplicationList"}, objs...)
}

// The name must no longer carry the Spine portfolio prefix (#6849).
func TestPlatformApplicationNameDropsTheSpinePrefix(t *testing.T) {
	got := platformApplicationName("gitea")
	if got != "platform-gitea" {
		t.Fatalf("name = %q, want platform-gitea", got)
	}
	if len(got) >= 6 && got[:6] == "spine-" {
		t.Fatal("still using the Spine portfolio name for a platform component")
	}
}

// Renaming a CR is a create-plus-orphan. Without the reap, every live
// Sovereign keeps printing the banned name in `kubectl get applications`.
func TestReapRemovesTheLegacySpineCR(t *testing.T) {
	ours := map[string]string{"catalyst.openova.io/managed-by": "catalyst-api"}
	dyn := newDyn(prefixTestApp("platform-gitea", ours), prefixTestApp("spine-gitea", ours))

	if err := reapLegacyPrefixedApplication(dyn, "gitea"); err != nil {
		t.Fatalf("reap: %v", err)
	}
	ri := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace)
	if _, err := ri.Get(context.Background(), "spine-gitea", metav1.GetOptions{}); err == nil {
		t.Fatal("legacy spine- CR survived the reap — the banned name stays visible on the Sovereign")
	}
	if _, err := ri.Get(context.Background(), "platform-gitea", metav1.GetOptions{}); err != nil {
		t.Fatalf("replacement CR was removed: %v", err)
	}
}

// If the replacement is missing the reap must do nothing, or a failed apply
// leaves the component with NO Application CR at all.
func TestReapWaitsForTheReplacement(t *testing.T) {
	ours := map[string]string{"catalyst.openova.io/managed-by": "catalyst-api"}
	dyn := newDyn(prefixTestApp("spine-gitea", ours))

	if err := reapLegacyPrefixedApplication(dyn, "gitea"); err != nil {
		t.Fatalf("reap: %v", err)
	}
	ri := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace)
	if _, err := ri.Get(context.Background(), "spine-gitea", metav1.GetOptions{}); err != nil {
		t.Fatal("reaped the legacy CR while no replacement existed — the component would have none")
	}
}

// A CR we do not manage must never be deleted, whatever it is called.
func TestReapLeavesForeignObjectsAlone(t *testing.T) {
	ours := map[string]string{"catalyst.openova.io/managed-by": "catalyst-api"}
	foreign := map[string]string{"catalyst.openova.io/managed-by": "somebody-else"}
	dyn := newDyn(prefixTestApp("platform-gitea", ours), prefixTestApp("spine-gitea", foreign))

	if err := reapLegacyPrefixedApplication(dyn, "gitea"); err != nil {
		t.Fatalf("reap: %v", err)
	}
	ri := dyn.Resource(ApplicationGVR()).Namespace(spineApplicationNamespace)
	if _, err := ri.Get(context.Background(), "spine-gitea", metav1.GetOptions{}); err != nil {
		t.Fatal("deleted an Application this producer does not manage")
	}
}

// A Sovereign provisioned after the rename has no legacy object; that is the
// common case and must not error.
func TestReapIsANoOpWithNoLegacyObject(t *testing.T) {
	ours := map[string]string{"catalyst.openova.io/managed-by": "catalyst-api"}
	dyn := newDyn(prefixTestApp("platform-gitea", ours))

	if err := reapLegacyPrefixedApplication(dyn, "gitea"); err != nil {
		t.Fatalf("reap on a clean Sovereign returned %v, want nil", err)
	}
}
