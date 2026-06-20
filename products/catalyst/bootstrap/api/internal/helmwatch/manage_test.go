// manage_test.go — coverage for the #3996 reconciler-management reader:
// kind→GVR mapping, kind→controller mapping, and the rich status/revision/
// suspended projection from live objects.
package helmwatch

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestManagedGVRForKind(t *testing.T) {
	for _, kind := range []string{
		ManageKindHelmRelease, ManageKindKustomization, ManageKindGitRepository,
		ManageKindOCIRepository, ManageKindHelmRepository, ManageKindHelmChart,
	} {
		if _, ok := ManagedGVRForKind(kind); !ok {
			t.Fatalf("kind %q should be manageable", kind)
		}
	}
	if _, ok := ManagedGVRForKind("Banana"); ok {
		t.Fatalf("unknown kind should not be manageable")
	}
}

func TestControllerForKind(t *testing.T) {
	cases := map[string]string{
		ManageKindHelmRelease:    "helm-controller",
		ManageKindKustomization:  "kustomize-controller",
		ManageKindGitRepository:  "source-controller",
		ManageKindOCIRepository:  "source-controller",
		ManageKindHelmRepository: "source-controller",
		ManageKindHelmChart:      "source-controller",
	}
	for kind, want := range cases {
		if got := ControllerForKind(kind); got != want {
			t.Fatalf("ControllerForKind(%q)=%q want %q", kind, got, want)
		}
	}
}

func mkHR(name string, readyStatus string, suspend bool, rev string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"name": name, "namespace": "flux-system"},
		"spec":       map[string]any{"suspend": suspend},
		"status": map[string]any{
			"lastAppliedRevision": rev,
			"conditions": []any{map[string]any{
				"type": "Ready", "status": readyStatus, "reason": "x",
				"message": "m", "lastTransitionTime": "2026-06-20T10:00:00Z",
			}},
		},
	}}
}

func TestListManagedReconcilers_Projection(t *testing.T) {
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		HelmReleaseGVR:    "HelmReleaseList",
		KustomizationGVR:  "KustomizationList",
		GitRepositoryGVR:  "GitRepositoryList",
		OCIRepositoryGVR:  "OCIRepositoryList",
		HelmRepositoryGVR: "HelmRepositoryList",
		HelmChartGVR:      "HelmChartList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds,
		mkHR("bp-ready", "True", false, "1.0.0"),
		mkHR("bp-suspended", "True", true, "2.0.0"),
		mkHR("bp-progressing", "False", false, "3.0.0"),
	)
	rows, err := ListManagedReconcilers(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListManagedReconcilers: %v", err)
	}
	got := map[string]ManagedReconciler{}
	for _, r := range rows {
		got[r.Name] = r
	}
	if got["bp-ready"].State != ManageStateReconciled || got["bp-ready"].Revision != "1.0.0" {
		t.Fatalf("bp-ready wrong: %+v", got["bp-ready"])
	}
	if got["bp-suspended"].State != ManageStateSuspended || !got["bp-suspended"].Suspended {
		t.Fatalf("bp-suspended wrong: %+v", got["bp-suspended"])
	}
	// Ready=False but NOT Stalled → Reconciling (anti-flap), never Degraded.
	if got["bp-progressing"].State != ManageStateReconciling {
		t.Fatalf("bp-progressing should be Reconciling, got %q", got["bp-progressing"].State)
	}
	if got["bp-ready"].LastReconcile == "" {
		t.Fatalf("bp-ready should carry a lastReconcile timestamp")
	}
}

func TestListManagedReconcilers_NilClient(t *testing.T) {
	rows, err := ListManagedReconcilers(context.Background(), nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("nil client should return empty,nil; got %v, %v", rows, err)
	}
}
