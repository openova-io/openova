// topology_loader_perregion_test.go — proves the multi-region live-source
// fix (fix/console-topology-both-regions): on the mothership path
// (Request.Regions populated) the loader must read EACH declared
// region's live data from that region's OWN kubeconfig (resolved via
// the k8sCache), not always the primary kubeconfig.
//
// Root cause this guards against: before the fix, buildTopology branch-1
// passed the single primary in.DynamicClient to buildRegion for every
// region, so the secondary region row mirrored the primary's live
// vClusters (or was empty) — the operator saw an effectively
// single-region topology on a 2-region Sovereign (hw173, depID
// 7bb723da8da06047 + secondary 7bb723da8da06047-me-east-215-b-1).
package infrastructure

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// fakeK8sCache satisfies K8sCacheReader with an in-memory id→client map.
type fakeK8sCache struct {
	clients map[string]dynamic.Interface
}

func (f *fakeK8sCache) Clusters() []string {
	out := make([]string, 0, len(f.clients))
	for id := range f.clients {
		out = append(out, id)
	}
	return out
}

func (f *fakeK8sCache) DynamicClientFor(id string) (dynamic.Interface, error) {
	if c, ok := f.clients[id]; ok {
		return c, nil
	}
	return nil, nil
}

// vclusterRoleNamespaceClient builds a fake dynamic client whose only
// content is one Namespace carrying the canonical vcluster-role label —
// this is the production fallback path loadVClusters() enumerates, so a
// per-region client returns a region-distinguishable VCluster list.
func vclusterRoleNamespaceClient(t *testing.T, vclusterName string) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: vclusterName,
			Labels: map[string]string{
				"catalyst.openova.io/vcluster-role": vclusterName,
			},
		},
	}
	// Register the CR list-kinds loadVClusters / loadPeerings probe so the
	// fake returns an empty list (production: apiserver 404) instead of
	// panicking — that lets the Namespace-role fallback path run, which is
	// the production vCluster-discovery path this test exercises.
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "vcluster.io", Version: "v1alpha1", Resource: "vclusters"}: "VClusterList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, ns)
}

// TestLoad_PerRegionLiveClientFanOut asserts each declared region's
// Cluster reads its OWN live vClusters from its OWN kubeconfig.
func TestLoad_PerRegionLiveClientFanOut(t *testing.T) {
	const depID = "7bb723da8da06047"
	primaryClient := vclusterRoleNamespaceClient(t, "primary-mgmt")
	secondaryClient := vclusterRoleNamespaceClient(t, "secondary-mgmt")

	cache := &fakeK8sCache{clients: map[string]dynamic.Interface{
		depID:                          primaryClient,   // bare-depID = primary
		depID + "-me-east-215-b-1":     secondaryClient, // materialised secondary
	}}

	in := LoaderInput{
		DeploymentID:  depID,
		Status:        "ready",
		SovereignFQDN: "hw173.omani.works",
		Provider:      "huawei",
		Region:        "me-east-215-a", // primary region key
		Regions: []provisioner.RegionSpec{
			{Provider: "huawei", CloudRegion: "me-east-215-a", WorkerCount: 5},
			{Provider: "huawei", CloudRegion: "me-east-215-b", WorkerCount: 5},
		},
		DynamicClient: primaryClient, // mothership wires the primary kubeconfig
		K8sCache:      cache,
	}

	resp := Load(context.Background(), in)
	regions := resp.Topology.Regions
	if len(regions) != 2 {
		t.Fatalf("expected 2 region rows, got %d", len(regions))
	}

	// Index regions by name for assertion clarity.
	byName := map[string]Region{}
	for _, r := range regions {
		byName[r.Name] = r
	}
	primaryRegion, ok := byName["me-east-215-a"]
	if !ok {
		t.Fatalf("missing primary region me-east-215-a; got %+v", regionNames(regions))
	}
	secondaryRegion, ok := byName["me-east-215-b"]
	if !ok {
		t.Fatalf("missing secondary region me-east-215-b; got %+v", regionNames(regions))
	}

	primaryVC := vclusterNames(primaryRegion)
	secondaryVC := vclusterNames(secondaryRegion)

	if !contains(primaryVC, "primary-mgmt") {
		t.Errorf("primary region should read primary client vClusters; got %v", primaryVC)
	}
	// The decisive assertion: the secondary region must read the
	// SECONDARY client, not mirror the primary. Pre-fix this was
	// "primary-mgmt" because every region used in.DynamicClient.
	if !contains(secondaryVC, "secondary-mgmt") {
		t.Errorf("secondary region should read secondary client vClusters; got %v (regression: per-region client not resolved)", secondaryVC)
	}
	if contains(secondaryVC, "primary-mgmt") {
		t.Errorf("secondary region leaked primary client vClusters %v — one-cluster-blind regression", secondaryVC)
	}
}

// TestLoad_PerRegionFallsBackToPrimaryWhenCacheNil asserts the legacy
// behaviour is preserved when no k8sCache is wired: both regions read
// the single in.DynamicClient (no panic, no empty topology).
func TestLoad_PerRegionFallsBackToPrimaryWhenCacheNil(t *testing.T) {
	const depID = "depX"
	primaryClient := vclusterRoleNamespaceClient(t, "only-mgmt")
	in := LoaderInput{
		DeploymentID:  depID,
		Status:        "ready",
		SovereignFQDN: "depx.omani.works",
		Provider:      "huawei",
		Region:        "reg-a",
		Regions: []provisioner.RegionSpec{
			{Provider: "huawei", CloudRegion: "reg-a", WorkerCount: 1},
			{Provider: "huawei", CloudRegion: "reg-b", WorkerCount: 1},
		},
		DynamicClient: primaryClient,
		// K8sCache intentionally nil.
	}
	resp := Load(context.Background(), in)
	if len(resp.Topology.Regions) != 2 {
		t.Fatalf("expected 2 region rows, got %d", len(resp.Topology.Regions))
	}
	for _, r := range resp.Topology.Regions {
		if !contains(vclusterNames(r), "only-mgmt") {
			t.Errorf("region %s should fall back to primary client; got %v", r.Name, vclusterNames(r))
		}
	}
}

func regionNames(rs []Region) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func vclusterNames(r Region) []string {
	out := []string{}
	for _, c := range r.Clusters {
		for _, vc := range c.VClusters {
			out = append(out, vc.Name)
		}
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
