// topology_loader_volumes_test.go — proves the #5611 fix: the cloud
// Volumes page must reflect the live block volumes (PersistentVolumes),
// not a hardcoded empty slice.
//
// Root cause this guards against: buildStorage returned
// `Volumes: []Volume{}` unconditionally (the doc comment claimed a
// Crossplane managed-resource source that was never queried), so
// `/cloud?view=list&kind=volumes` rendered a POSITIVE "Volumes 0 / No
// volumes yet" empty-state on hw292, a Sovereign carrying 50 EVS block
// volumes (50 Bound PVs). A PV is the K8s projection of the cloud block
// volume attached to a node, so loadVolumes reads PVs directly.
package infrastructure

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// pvClient builds a fake dynamic client carrying real cluster-scoped
// PersistentVolumes so loadVolumes' live read fires.
func pvClient(t *testing.T, pvs ...*corev1.PersistentVolume) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "persistentvolumes"}:      "PersistentVolumeList",
		{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}: "PersistentVolumeClaimList",
	}
	objs := make([]runtime.Object, 0, len(pvs))
	for _, pv := range pvs {
		objs = append(objs, pv)
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
}

func volumeByID(vols []Volume, id string) (Volume, bool) {
	for _, v := range vols {
		if v.ID == id {
			return v, true
		}
	}
	return Volume{}, false
}

// TestLoadVolumes_FromLivePVs_5611 is the decisive regression guard: with
// two live PVs present, buildStorage must surface two Volume rows with
// faithful capacity / attachment / region / status — NOT the pre-fix
// empty slice. On the old hardcoded `Volumes: []Volume{}` this fails at
// the len check (0 != 2).
func TestLoadVolumes_FromLivePVs_5611(t *testing.T) {
	bound := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-cnpg-a-1"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("50Gi")},
			ClaimRef: &corev1.ObjectReference{Namespace: "openova-system", Name: "data-cnpg-a-1"},
			NodeAffinity: &corev1.VolumeNodeAffinity{
				Required: &corev1.NodeSelector{
					NodeSelectorTerms: []corev1.NodeSelectorTerm{{
						MatchExpressions: []corev1.NodeSelectorRequirement{{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"me-east-215-a"},
						}},
					}},
				},
			},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
	}
	// Released PV with no claimRef and no topology term — proves the
	// honest-empty path (AttachedTo "" → "detached" in the UI) and the
	// Released→degraded status mapping.
	orphan := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-orphan-9"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
		Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeReleased},
	}

	in := LoaderInput{DynamicClient: pvClient(t, bound, orphan)}
	storage := buildStorage(context.Background(), in)

	if len(storage.Volumes) != 2 {
		t.Fatalf("expected 2 volume rows from 2 live PVs, got %d — the cloud Volumes page falsely reports empty (#5611)", len(storage.Volumes))
	}

	b, ok := volumeByID(storage.Volumes, "pvc-cnpg-a-1")
	if !ok {
		t.Fatalf("bound PV pvc-cnpg-a-1 missing from volumes; got %+v", storage.Volumes)
	}
	if b.Capacity != "50Gi" {
		t.Errorf("bound PV capacity = %q, want 50Gi", b.Capacity)
	}
	if b.AttachedTo != "openova-system/data-cnpg-a-1" {
		t.Errorf("bound PV attachedTo = %q, want openova-system/data-cnpg-a-1 (claimRef)", b.AttachedTo)
	}
	if b.Region != "me-east-215-a" {
		t.Errorf("bound PV region = %q, want me-east-215-a (CSI topology zone)", b.Region)
	}
	if b.Status != "healthy" {
		t.Errorf("bound PV status = %q, want healthy (Bound)", b.Status)
	}

	o, ok := volumeByID(storage.Volumes, "pvc-orphan-9")
	if !ok {
		t.Fatalf("released PV pvc-orphan-9 missing from volumes; got %+v", storage.Volumes)
	}
	if o.AttachedTo != "" {
		t.Errorf("released PV attachedTo = %q, want empty (detached — no claimRef)", o.AttachedTo)
	}
	if o.Status != "degraded" {
		t.Errorf("released PV status = %q, want degraded (Released)", o.Status)
	}
}

// TestLoadVolumes_NilClient_HonestEmpty proves the no-data-plane path
// yields an honest empty slice (not a nil that would JSON-marshal to
// null and not a fabricated row).
func TestLoadVolumes_NilClient_HonestEmpty(t *testing.T) {
	storage := buildStorage(context.Background(), LoaderInput{DynamicClient: nil})
	if storage.Volumes == nil {
		t.Fatalf("Volumes must be a non-nil empty slice, got nil")
	}
	if len(storage.Volumes) != 0 {
		t.Fatalf("nil client must yield 0 volumes, got %d", len(storage.Volumes))
	}
}
