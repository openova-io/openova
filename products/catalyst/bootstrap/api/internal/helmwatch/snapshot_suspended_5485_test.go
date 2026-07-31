// snapshot_suspended_5485_test.go — #5485 defect 4: the snapshot
// builders must carry spec.suspend as ComponentSnapshot.Suspended so
// downstream readers (the Reconciliation DAG) can render a suspended
// HR distinctly, while Status stays StateInstalled per the Wave 5.103
// (#2447) Phase-1 readiness rule.
package helmwatch

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// newSuspendableHR builds a bp-* HelmRelease with Ready=True and an
// optional spec.suspend=true.
func newSuspendableHR(name string, suspended bool) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata": map[string]any{
			"name":      name,
			"namespace": FluxNamespace,
		},
		"spec": map[string]any{},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":               "Ready",
					"status":             string(metav1.ConditionTrue),
					"reason":             "ReconciliationSucceeded",
					"message":            "Helm install succeeded",
					"lastTransitionTime": time.Now().UTC().Format(time.RFC3339),
				},
			},
		},
	}
	if suspended {
		obj["spec"].(map[string]any)["suspend"] = true
	}
	u := &unstructured.Unstructured{Object: obj}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmRelease",
	})
	return u
}

func TestListAndSnapshotHelmReleases_CarriesSuspendedFlag(t *testing.T) {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{
		Group:   "helm.toolkit.fluxcd.io",
		Version: "v2",
		Kind:    "HelmReleaseList",
	}, &unstructured.UnstructuredList{})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{HelmReleaseGVR: "HelmReleaseList"},
		newSuspendableHR("bp-velero", true),
		newSuspendableHR("bp-cilium", false),
	)

	snap, err := ListAndSnapshotHelmReleases(context.Background(), dyn)
	if err != nil {
		t.Fatalf("ListAndSnapshotHelmReleases: %v", err)
	}
	byID := map[string]ComponentSnapshot{}
	for _, cs := range snap {
		byID[cs.AppID] = cs
	}
	velero, ok := byID["velero"]
	if !ok {
		t.Fatalf("bp-velero missing from snapshot: %+v", snap)
	}
	if !velero.Suspended {
		t.Errorf("suspended HR must carry Suspended=true")
	}
	// The Wave 5.103 readiness rule is PRESERVED: a suspended HR still
	// reports StateInstalled so Phase-1 convergence never blocks on it.
	if velero.Status != StateInstalled {
		t.Errorf("suspended HR Status: want %q (Wave 5.103 preserved), got %q", StateInstalled, velero.Status)
	}
	cilium, ok := byID["cilium"]
	if !ok {
		t.Fatalf("bp-cilium missing from snapshot: %+v", snap)
	}
	if cilium.Suspended {
		t.Errorf("unsuspended HR must carry Suspended=false")
	}
	if cilium.Status != StateInstalled {
		t.Errorf("unsuspended Ready HR Status: want %q, got %q", StateInstalled, cilium.Status)
	}
}
