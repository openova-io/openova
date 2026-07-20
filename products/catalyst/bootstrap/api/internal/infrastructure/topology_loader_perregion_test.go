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
		// #4739: loadVClusters' third source enumerates app=vcluster
		// StatefulSets. Register the list-kind so the fake returns an empty
		// list (production: this Sovereign's Namespace-role vclusters win the
		// dedup) instead of panicking → recover → [] (which regressed the
		// Namespace-role fallback this test asserts).
		{Group: "apps", Version: "v1", Resource: "statefulsets"}: "StatefulSetList",
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

// TestLoad_SecondaryAbsentKubeconfigRendersDegraded proves the #4814 fix:
// on a POPULATED k8sCache (multi-cluster mode) where a declared secondary
// region's own kubeconfig never registered (standby-region-absent, #4811),
// the loader must render that region DEGRADED + live-empty — NOT fall back
// to the primary client and mirror its live contents, which rendered the
// secondary's DECLARED worker count as present ("Region 2/2 · WorkerNode
// 24/24" on a region-b-absent 2-region prov with 6 live nodes).
func TestLoad_SecondaryAbsentKubeconfigRendersDegraded(t *testing.T) {
	const depID = "depY"
	primaryClient := vclusterRoleNamespaceClient(t, "only-primary")
	in := LoaderInput{
		DeploymentID:  depID,
		Status:        "ready",
		SovereignFQDN: "depy.omani.works",
		Provider:      "huawei",
		Region:        "reg-a",
		Regions: []provisioner.RegionSpec{
			{Provider: "huawei", CloudRegion: "reg-a", WorkerCount: 3},
			{Provider: "huawei", CloudRegion: "reg-b", WorkerCount: 3},
		},
		DynamicClient: primaryClient,
		// Populated cache with ONLY the primary cluster registered;
		// reg-b's "<depID>-reg-b" kubeconfig is absent (never converged).
		K8sCache: &fakeK8sCache{clients: map[string]dynamic.Interface{
			depID: primaryClient,
		}},
	}
	resp := Load(context.Background(), in)
	if len(resp.Topology.Regions) != 2 {
		t.Fatalf("expected 2 region rows, got %d", len(resp.Topology.Regions))
	}
	var regB *Region
	for i := range resp.Topology.Regions {
		if resp.Topology.Regions[i].Name == "reg-b" {
			regB = &resp.Topology.Regions[i]
		}
	}
	if regB == nil {
		t.Fatalf("reg-b region row missing; got %v", regionNames(resp.Topology.Regions))
	}
	if regB.Status != "degraded" {
		t.Errorf("reg-b (absent kubeconfig) should render degraded, got %q", regB.Status)
	}
	if len(regB.Clusters) != 0 {
		t.Errorf("reg-b should have 0 live clusters (no primary fallback), got %d", len(regB.Clusters))
	}
	if contains(vclusterNames(*regB), "only-primary") {
		t.Errorf("reg-b must NOT mirror the primary client's vclusters (#4814 fabrication), got %v", vclusterNames(*regB))
	}
}

// TestLoad_PerRegionFullSlugRegionMatchesBareCodeKubeconfig_5274 proves the
// #5274 fix: on the chroot fallback path (SOVEREIGN_REGIONS_JSON unwired →
// region list reconstructed from SOVEREIGN_PRIMARY_REGION /
// SOVEREIGN_REPLICA_REGION), the declared CloudRegion is the FULL 4-segment
// cluster slug (e.g. "hw-me-east-215-b-rtz-prod") while the secondary
// kubeconfig is registered under the bare cloud code
// ("<depID>-me-east-215-b-1"). The strict "<depID>-<CloudRegion>" prefix
// match can't bridge that, so region-b was dropped to buildAbsentRegion and
// the Cloud graph rendered "Region 2/2 · Cluster 1/1" on a fully-live
// 2-region Sovereign (hw278, row 66). The loader must now resolve region-b's
// OWN client via the shared cloud-region-code token and enumerate its
// cluster — 2 clusters, region-b reading its own live vClusters.
func TestLoad_PerRegionFullSlugRegionMatchesBareCodeKubeconfig_5274(t *testing.T) {
	const depID = "hw278dep0000abcd"
	primaryClient := vclusterRoleNamespaceClient(t, "primary-mgmt")
	secondaryClient := vclusterRoleNamespaceClient(t, "secondary-mgmt")

	// k8sCache ids follow the bare-cloud-code kubeconfig convention.
	cache := &fakeK8sCache{clients: map[string]dynamic.Interface{
		depID:                      primaryClient,   // bare-depID = primary / chroot-self
		depID + "-me-east-215-b-1": secondaryClient, // materialised secondary (bare code)
	}}

	// Declared regions carry the FULL cluster slug (the chroot
	// primary/replica-env reconstruction, jobs.go:chrootRegionsFromPrimaryReplicaEnv).
	in := LoaderInput{
		DeploymentID:  depID,
		Status:        "ready",
		SovereignFQDN: "hw278.omani.works",
		Provider:      "huawei",
		Region:        "hw-me-east-215-a-rtz-prod", // primary region (full slug)
		Regions: []provisioner.RegionSpec{
			{Provider: "huawei", CloudRegion: "hw-me-east-215-a-rtz-prod", WorkerCount: 5},
			{Provider: "huawei", CloudRegion: "hw-me-east-215-b-rtz-prod", WorkerCount: 5},
		},
		DynamicClient: primaryClient,
		K8sCache:      cache,
	}

	resp := Load(context.Background(), in)
	regions := resp.Topology.Regions
	if len(regions) != 2 {
		t.Fatalf("expected 2 region rows, got %d", len(regions))
	}

	// Decisive census assertion: BOTH regions carry a live Cluster
	// (Region 2/2 · Cluster 2/2), not one absent region → Cluster 1/1.
	clusterCount := 0
	var regB *Region
	for i := range regions {
		clusterCount += len(regions[i].Clusters)
		if regions[i].Name == "hw-me-east-215-b-rtz-prod" {
			regB = &regions[i]
		}
	}
	if clusterCount != 2 {
		t.Fatalf("expected 2 clusters across both regions (Cluster 2/2), got %d — region-b under-enumerated (#5274)", clusterCount)
	}
	if regB == nil {
		t.Fatalf("missing region-b hw-me-east-215-b-rtz-prod; got %v", regionNames(regions))
	}
	if len(regB.Clusters) != 1 {
		t.Fatalf("region-b should carry its live cluster, got %d clusters (buildAbsentRegion regression)", len(regB.Clusters))
	}
	// And it must read its OWN client, not mirror the primary.
	secondaryVC := vclusterNames(*regB)
	if !contains(secondaryVC, "secondary-mgmt") {
		t.Errorf("region-b should read its own (secondary) client vClusters; got %v", secondaryVC)
	}
	if contains(secondaryVC, "primary-mgmt") {
		t.Errorf("region-b leaked primary client vClusters %v — must resolve its OWN cluster", secondaryVC)
	}
}

// TestRegionTokenMatchingHelpers locks the token-overlap semantics that
// bridge the declared-region ↔ kubeconfig-id naming divergence without the
// false positives a raw substring test would allow.
func TestRegionTokenMatchingHelpers(t *testing.T) {
	idxCases := []struct{ in, want string }{
		{"me-east-215-b-1", "me-east-215-b"}, // strip materialisation index
		{"me-east-215-b", "me-east-215-b"},   // bare code — no-op
		{"nbg1-2", "nbg1"},
		{"nbg1", "nbg1"},         // no numeric tail
		{"region-b", "region-b"}, // non-numeric tail is not an index
	}
	for _, c := range idxCases {
		if got := trimMaterialisationIndex(c.in); got != c.want {
			t.Errorf("trimMaterialisationIndex(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	overlap := []struct {
		a, b string
		want bool
	}{
		// bare code embedded in the full slug (both directions) — the #5274 case.
		{"hw-me-east-215-b-rtz-prod", "me-east-215-b", true},
		{"me-east-215-b", "hw-me-east-215-b-rtz-prod", true},
		{"me-east-215-b", "me-east-215-b", true},
		// token-boundary safety: a longer trailing token must NOT match.
		{"hw-me-east-215-ba-rtz-prod", "me-east-215-b", false},
		// different region codes never match.
		{"hw-me-east-215-a-rtz-prod", "me-east-215-b", false},
		// empty needle never pairs.
		{"", "me-east-215-b", false},
	}
	for _, c := range overlap {
		if got := regionTokensOverlap(c.a, c.b); got != c.want {
			t.Errorf("regionTokensOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
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
