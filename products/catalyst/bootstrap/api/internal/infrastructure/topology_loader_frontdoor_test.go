// topology_loader_frontdoor_test.go — proves the front-door LoadBalancer
// fix (Refs #3998): the cloud-view LB page must surface the REAL front
// door on EVERY Sovereign, sourced from the deployment record
// (Result.LoadBalancerIP), NOT the empty Crossplane XRC layer.
//
// Root cause this guards against: the live chroot path
// (buildRegionFromLiveNodes — the branch every real Sovereign hits, since
// catalyst-api runs in-cluster) previously hardcoded
// `LoadBalancers: []LoadBalancer{}`, so the LB page rendered 0/0 even
// though Result.LoadBalancerIP held the live EIP/cloud-LB. On hw174
// (Huawei, EIP DNAT'd to the Cilium Gateway NodePort) that meant the
// operator could not answer "how is the platform fronted".
//
// The front-door kind comes from the cloud profile of the provider
// (spec.network.loadBalancer.mode). These tests read the profiles of this
// repository: the Huawei pack is external-hostport, the Hetzner pack
// controller.
package infrastructure

import (
	"context"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/cloudprofile"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// useRepositoryProfiles points the loader at infra/providers of this
// repository and names the Sovereign's own pack (empty: none).
func useRepositoryProfiles(t *testing.T, sovereignPack string) {
	t.Helper()
	old := providersDir
	providersDir = filepath.Join("..", "..", "..", "..", "..", "..", "infra", "providers")
	t.Cleanup(func() { providersDir = old })
	t.Setenv(cloudprofile.ProviderEnv, sovereignPack)
	t.Setenv(cloudprofile.InstallationEnv, "")
}

// liveNodeClient builds a fake dynamic client carrying real Nodes so the
// loader's live chroot path (buildRegionFromLiveNodes) fires. The vcluster
// + node list-kinds are registered so the discovery probes return empty
// (production: apiserver 404) instead of panicking.
func liveNodeClient(t *testing.T, providerID string) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	cp := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "cp-0",
			Labels: map[string]string{
				"node-role.kubernetes.io/control-plane": "",
				"node.kubernetes.io/instance-type":      "s7.large.2",
			},
		},
		Spec:   corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
	worker := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "worker-0",
			Labels: map[string]string{"node.kubernetes.io/instance-type": "s7.xlarge.2"},
		},
		Spec:   corev1.NodeSpec{ProviderID: providerID},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}
	gvrToListKind := map[schema.GroupVersionResource]string{
		{Group: "vcluster.com", Version: "v1alpha1", Resource: "vclusters"}: "VClusterList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind, cp, worker)
}

func lbsOf(r Region) []LoadBalancer {
	out := []LoadBalancer{}
	for _, c := range r.Clusters {
		out = append(out, c.LoadBalancers...)
	}
	return out
}

// TestFrontDoor_LiveChrootPath_SurfacesLBFromRecord is the decisive
// regression guard: the live chroot path now emits a non-empty LB sourced
// from Result.LoadBalancerIP, with FrontDoorKind=gateway-eip on Huawei.
func TestFrontDoor_LiveChrootPath_SurfacesLBFromRecord(t *testing.T) {
	useRepositoryProfiles(t, "huawei")
	const depID = "ea30d1d816f2eee2"
	in := LoaderInput{
		DeploymentID:  depID,
		Status:        "ready",
		SovereignFQDN: "hw174.omani.works",
		// No declared Provider + no Regions → forces the live chroot path
		// (buildRegionFromLiveNodes), exactly like the in-cluster catalyst-api
		// on a real Sovereign. The provider is the Sovereign's own pack
		// (CLOUD_PROVIDER), never sniffed from the node providerID.
		Result:        &provisioner.Result{LoadBalancerIP: "212.72.24.33"},
		DynamicClient: liveNodeClient(t, "huawei:///me-east-215-a/abc-123"),
	}

	resp := Load(context.Background(), in)
	if len(resp.Topology.Regions) != 1 {
		t.Fatalf("expected 1 region from live nodes, got %d", len(resp.Topology.Regions))
	}
	if got := resp.Topology.Regions[0].Provider; got != "huawei" {
		t.Errorf("region provider = %q, want the Sovereign's pack huawei", got)
	}
	if len(resp.Cloud) != 1 || resp.Cloud[0].Provider != "huawei" {
		t.Errorf("cloud entries = %+v, want one for huawei", resp.Cloud)
	}
	lbs := lbsOf(resp.Topology.Regions[0])
	if len(lbs) != 1 {
		t.Fatalf("REGRESSION: live chroot path emitted %d LBs, want 1 (the front door sourced from Result.LoadBalancerIP)", len(lbs))
	}
	lb := lbs[0]
	if lb.PublicIP != "212.72.24.33" {
		t.Errorf("LB PublicIP = %q, want the record's LoadBalancerIP 212.72.24.33", lb.PublicIP)
	}
	if lb.FrontDoorKind != "gateway-eip" {
		t.Errorf("FrontDoorKind = %q, want gateway-eip (Huawei EIP→Gateway NodePort)", lb.FrontDoorKind)
	}
	if len(lb.Listeners) == 0 {
		t.Errorf("LB.Listeners empty — the UI's listeners column would render '—'")
	}
	// Worker nodes must still be real (sourced from live K8s, not XRCs).
	var nodeCount int
	for _, c := range resp.Topology.Regions[0].Clusters {
		nodeCount += len(c.Nodes)
	}
	if nodeCount != 2 {
		t.Errorf("expected 2 live nodes surfaced, got %d", nodeCount)
	}
}

// TestFrontDoor_DeclaredPath_CloudLBForHetzner asserts the mothership /
// declared path also emits the structured listeners + a cloud-lb
// FrontDoorKind for a provider that provisions a real cloud LB.
func TestFrontDoor_DeclaredPath_CloudLBForHetzner(t *testing.T) {
	useRepositoryProfiles(t, "")
	in := LoaderInput{
		DeploymentID:  "depHZ",
		Status:        "ready",
		SovereignFQDN: "t99.omani.works",
		Provider:      "hetzner",
		Region:        "fsn1",
		Regions: []provisioner.RegionSpec{
			{Provider: "hetzner", CloudRegion: "fsn1", WorkerCount: 2},
		},
		Result: &provisioner.Result{LoadBalancerIP: "5.6.7.8"},
	}
	resp := Load(context.Background(), in)
	if len(resp.Topology.Regions) != 1 {
		t.Fatalf("expected 1 region, got %d", len(resp.Topology.Regions))
	}
	lbs := lbsOf(resp.Topology.Regions[0])
	if len(lbs) != 1 {
		t.Fatalf("expected 1 LB on the declared path, got %d", len(lbs))
	}
	lb := lbs[0]
	if lb.PublicIP != "5.6.7.8" {
		t.Errorf("LB PublicIP = %q, want 5.6.7.8", lb.PublicIP)
	}
	if lb.FrontDoorKind != "cloud-lb" {
		t.Errorf("FrontDoorKind = %q, want cloud-lb (Hetzner provisions a real hcloud LB)", lb.FrontDoorKind)
	}
	if len(lb.Listeners) == 0 {
		t.Errorf("LB.Listeners empty on the declared path")
	}
}

// TestFrontDoor_NoIPYields_EmptyNotFabricated guards the no-placeholder
// principle: when the record has no LoadBalancerIP yet, the LB slice is
// empty — never a synthesised row.
func TestFrontDoor_NoIPYields_EmptyNotFabricated(t *testing.T) {
	in := LoaderInput{
		DeploymentID:  "depPending",
		Status:        "unknown",
		SovereignFQDN: "pending.omani.works",
		Provider:      "hetzner",
		Region:        "fsn1",
		Regions: []provisioner.RegionSpec{
			{Provider: "hetzner", CloudRegion: "fsn1", WorkerCount: 1},
		},
		// Result nil → no LoadBalancerIP known yet.
	}
	resp := Load(context.Background(), in)
	if len(resp.Topology.Regions) != 1 {
		t.Fatalf("expected 1 region, got %d", len(resp.Topology.Regions))
	}
	if got := len(lbsOf(resp.Topology.Regions[0])); got != 0 {
		t.Errorf("expected 0 LBs when no LoadBalancerIP is known, got %d (fabricated placeholder)", got)
	}
}

// TestFrontDoor_UnknownProviderStaysEmpty: with no CLOUD_PROVIDER on the live
// path (providerID is not read) and a pack name that names no pack on the
// declared path, the provider, the cloud entry and the front-door kind stay
// empty. Nothing defaults to a provider name.
func TestFrontDoor_UnknownProviderStaysEmpty(t *testing.T) {
	useRepositoryProfiles(t, "")
	for _, in := range []LoaderInput{
		{DeploymentID: "depLive", SovereignFQDN: "live.omani.works", DynamicClient: liveNodeClient(t, "hcloud://12345")},
		{DeploymentID: "depOther", Provider: "nothere", Region: "r1", Regions: []provisioner.RegionSpec{{CloudRegion: "r1", WorkerCount: 1}}},
	} {
		in.Result = &provisioner.Result{LoadBalancerIP: "192.0.2.10"}
		resp := Load(context.Background(), in)
		if len(resp.Topology.Regions) != 1 || resp.Topology.Regions[0].Provider != "" {
			t.Fatalf("%s: regions %+v, want one with an empty provider", in.DeploymentID, resp.Topology.Regions)
		}
		if lbs := lbsOf(resp.Topology.Regions[0]); len(lbs) != 1 || lbs[0].FrontDoorKind != "" {
			t.Errorf("%s: LBs %+v, want one with an empty FrontDoorKind", in.DeploymentID, lbs)
		}
		if in.Provider == "" && len(resp.Cloud) != 0 {
			t.Errorf("%s: cloud entries %+v, want none", in.DeploymentID, resp.Cloud)
		}
	}
}
