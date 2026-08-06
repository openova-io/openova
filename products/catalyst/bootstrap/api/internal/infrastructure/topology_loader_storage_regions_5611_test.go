// topology_loader_storage_regions_5611_test.go — #5611, second defect.
//
// PR #5685 removed the hardcoded `Volumes: []Volume{}` so the cloud
// Volumes page stopped claiming a false zero. It did NOT fix where the
// number comes from: buildStorage read ONLY in.DynamicClient — the
// PRIMARY region's cluster — while buildTopology right beside it already
// fans out per region. On the 2-region hw292 that leaves the Volumes
// chip reporting region-a's 57 PVs while the PersistentVolumes chip
// (k8scache SSE, which fans out across every registered cluster) reports
// the true union of 105. Same underlying object kind, two different
// numbers on the same chip row — a zero replaced by a different wrong
// number.
//
// Live hw292 numbers these fixtures are scaled down from (both
// kubeconfigs sampled 2026-08-06):
//
//	region-a (1c56518035a83e03)                    57 PVs / 58 PVCs
//	region-b (1c56518035a83e03-me-east-215-b-1)    48 PVs / 48 PVCs
//	union                                         105 PVs / 106 PVCs
//
// Region attribution is asserted too, and it is not cosmetic: on hw292
// every PV in BOTH regions carries the SAME CSI topology zone
// (`topology.evs.csi.huaweicloud.com/zone = me-east-215a`), so the
// in-object term folds the regions together and only the source cluster
// can tell them apart. A union that mislabels every row as one region is
// still a console that misinforms.
package infrastructure

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

const (
	depID5611     = "1c56518035a83e03"
	regionA5611   = "me-east-215-a"
	regionB5611   = "me-east-215-b"
	clusterB5611  = depID5611 + "-me-east-215-b-1"
	csiZoneShared = "me-east-215a" // identical in BOTH regions on hw292
)

// storageClient builds a fake dynamic client carrying cluster-scoped PVs
// and namespaced PVCs, every object carrying the shared CSI zone so the
// fixture reproduces hw292's region-indistinguishable topology terms.
func storageClient(t *testing.T, pvNames, pvcNames []string, uidPrefix string) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "", Version: "v1", Resource: "persistentvolumes"}:      "PersistentVolumeList",
		{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}: "PersistentVolumeClaimList",
	}
	objs := []runtime.Object{}
	for _, n := range pvNames {
		objs = append(objs, &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: n, UID: types.UID(uidPrefix + n)},
			Spec: corev1.PersistentVolumeSpec{
				Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("50Gi")},
				NodeAffinity: &corev1.VolumeNodeAffinity{
					Required: &corev1.NodeSelector{
						NodeSelectorTerms: []corev1.NodeSelectorTerm{{
							MatchExpressions: []corev1.NodeSelectorRequirement{{
								Key:      "topology.evs.csi.huaweicloud.com/zone",
								Operator: corev1.NodeSelectorOpIn,
								Values:   []string{csiZoneShared},
							}},
						}},
					},
				},
			},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		})
	}
	for _, n := range pvcNames {
		objs = append(objs, &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "openova-system", UID: types.UID(uidPrefix + n)},
			Status:     corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		})
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, objs...)
}

// twoRegionInput mirrors hw292's declared shape: Regions[0] is the
// primary (served by in.DynamicClient), Regions[1] is a secondary whose
// kubeconfig is registered in the k8sCache under "<depID>-<region>-1".
func twoRegionInput(t *testing.T) LoaderInput {
	t.Helper()
	primary := storageClient(t, []string{"pv-a-1", "pv-a-2", "pv-a-3"}, []string{"pvc-a-1", "pvc-a-2", "pvc-a-3"}, "uid-a-")
	secondary := storageClient(t, []string{"pv-b-1", "pv-b-2"}, []string{"pvc-b-1", "pvc-b-2"}, "uid-b-")
	return LoaderInput{
		DeploymentID:  depID5611,
		Region:        regionA5611,
		DynamicClient: primary,
		Regions: []provisioner.RegionSpec{
			{CloudRegion: regionA5611, Provider: "huawei"},
			{CloudRegion: regionB5611, Provider: "huawei"},
		},
		K8sCache: &fakeK8sCache{
			clients: map[string]dynamic.Interface{depID5611: primary, clusterB5611: secondary},
		},
	}
}

// TestBuildStorage_UnionsBothRegions_5611 is the decisive guard. On the
// pre-fix tree buildStorage reads only in.DynamicClient, so Volumes is 3
// (region-a only) and this fails at "got 3, want 5".
func TestBuildStorage_UnionsBothRegions_5611(t *testing.T) {
	storage := buildStorage(context.Background(), twoRegionInput(t))

	if got := len(storage.Volumes); got != 5 {
		t.Fatalf("Volumes = %d, want 5 (3 region-a + 2 region-b) — the Cloud Volumes chip reports one region while the PersistentVolumes chip reports the union (#5611)", got)
	}
	if got := len(storage.PVCs); got != 5 {
		t.Fatalf("PVCs = %d, want 5 (3 region-a + 2 region-b) — same single-region fold (#5611)", got)
	}

	// Both regions actually present, by name — a union that silently
	// read the primary twice would also total 5 without the fix.
	names := map[string]bool{}
	for _, v := range storage.Volumes {
		names[v.Name] = true
	}
	for _, want := range []string{"pv-a-1", "pv-a-2", "pv-a-3", "pv-b-1", "pv-b-2"} {
		if !names[want] {
			t.Errorf("volume %q missing from the union; got %v", want, names)
		}
	}
}

// TestBuildStorage_RegionAttribution_5611 — every row must carry the
// region of the CLUSTER it was read from, not the CSI topology term the
// two regions share. Pre-fix (and with a naive union) all five rows read
// "me-east-215a", collapsing the Volumes page's region filter to a
// single useless option.
func TestBuildStorage_RegionAttribution_5611(t *testing.T) {
	storage := buildStorage(context.Background(), twoRegionInput(t))

	perRegion := map[string]int{}
	for _, v := range storage.Volumes {
		perRegion[v.Region]++
	}
	if perRegion[regionA5611] != 3 {
		t.Errorf("region %s = %d volumes, want 3 (got the whole map %v)", regionA5611, perRegion[regionA5611], perRegion)
	}
	if perRegion[regionB5611] != 2 {
		t.Errorf("region %s = %d volumes, want 2 (got the whole map %v)", regionB5611, perRegion[regionB5611], perRegion)
	}
	if perRegion[csiZoneShared] != 0 {
		t.Errorf("%d volumes fell back to the shared CSI zone %q — the source cluster must win, or both regions read as one", perRegion[csiZoneShared], csiZoneShared)
	}
}

// TestBuildStorage_NoDoubleCount_5611 is the CONTROL: a SINGLE-region
// Sovereign must report exactly its own objects. Green on the pre-fix
// tree too — it is what rules out a "fix" that counts everything twice.
func TestBuildStorage_NoDoubleCount_5611(t *testing.T) {
	primary := storageClient(t, []string{"pv-a-1", "pv-a-2", "pv-a-3"}, []string{"pvc-a-1"}, "uid-a-")
	in := LoaderInput{
		DeploymentID:  depID5611,
		Region:        regionA5611,
		DynamicClient: primary,
		Regions:       []provisioner.RegionSpec{{CloudRegion: regionA5611, Provider: "huawei"}},
		K8sCache: &fakeK8sCache{
			clients: map[string]dynamic.Interface{depID5611: primary},
		},
	}
	storage := buildStorage(context.Background(), in)
	if got := len(storage.Volumes); got != 3 {
		t.Fatalf("single-region Volumes = %d, want exactly 3 — the fan-out must not read the same cluster twice", got)
	}
	if got := len(storage.PVCs); got != 1 {
		t.Fatalf("single-region PVCs = %d, want exactly 1", got)
	}
}

// TestStorageSources_MothershipDoesNotUnionOtherSovereigns_5611 — the
// guard on the fan-out's blast radius. On the MOTHERSHIP the k8sCache
// holds clusters for EVERY deployment, so a fan-out that iterated
// Clusters() unconditionally would union other Sovereigns' volumes into
// this one's Cloud page: a far worse number than the undercount it
// replaces. With Regions declared, only this deployment's clusters may
// be read.
func TestStorageSources_MothershipDoesNotUnionOtherSovereigns_5611(t *testing.T) {
	in := twoRegionInput(t)
	cache := in.K8sCache.(*fakeK8sCache)
	// A DIFFERENT Sovereign's cluster, registered in the same cache.
	other := storageClient(t, []string{"pv-other-1", "pv-other-2"}, nil, "uid-other-")
	cache.clients["deadbeefdeadbeef"] = other

	storage := buildStorage(context.Background(), in)

	if got := len(storage.Volumes); got != 5 {
		t.Fatalf("Volumes = %d, want 5 — another Sovereign's volumes leaked into this deployment's Cloud page", got)
	}
	for _, v := range storage.Volumes {
		if v.Name == "pv-other-1" || v.Name == "pv-other-2" {
			t.Fatalf("volume %q belongs to a different deployment and must never appear here", v.Name)
		}
	}
}
