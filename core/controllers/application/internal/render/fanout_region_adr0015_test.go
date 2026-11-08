package render

// fanout_region_adr0015_test.go — ADR-0015 D1/D2/D3: every placement leg is
// rendered for ITS OWN region, carries that region's id + the topology values
// contract, and is grouped into one Git file per region. Supersedes the #6268
// "local-undelivered" tests: a standby can no longer land beside its primary,
// because no region ever applies another region's leg.

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

const (
	testRegionA = "hw-me-east-215-a-rtz-prod"
	testRegionB = "hw-me-east-215-b-rtz-prod"
	testRegionC = "hw-me-east-215-c-rtz-prod"
)

// testRegionFor maps the canonical cluster-ID letter to a region id the way
// pkg/regions.Registry.Canonical does for a three-region Sovereign.
func testRegionFor(cluster string) string {
	switch {
	case strings.HasSuffix(cluster, "-A"):
		return testRegionA
	case strings.HasSuffix(cluster, "-B"):
		return testRegionB
	case strings.HasSuffix(cluster, "-C"):
		return testRegionC
	}
	return ""
}

func twoClusterVariant() bpv1alpha1.TopologyVariant {
	return bpv1alpha1.TopologyVariant{
		Placement: &bpv1alpha1.PlacementSpec{
			Tier:     "rtz",
			Clusters: []string{"rtz-A", "rtz-B"},
			Roles:    map[string]string{"rtz-A": "active", "rtz-B": "passive"},
		},
	}
}

func hrByRole(t *testing.T, hrs []*unstructured.Unstructured, role string) *unstructured.Unstructured {
	t.Helper()
	var found []*unstructured.Unstructured
	for _, hr := range hrs {
		if hr.GetLabels()[LabelRole] == role {
			found = append(found, hr)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly 1 HR with role=%q; got %d (of %d rendered)", role, len(found), len(hrs))
	}
	return found[0]
}

func hrValues(t *testing.T, hr *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	v, ok, _ := unstructured.NestedMap(hr.Object, "spec", "values")
	if !ok {
		t.Fatalf("HR %q has no spec.values", hr.GetName())
	}
	return v
}

func topologyOf(t *testing.T, hr *unstructured.Unstructured) map[string]interface{} {
	t.Helper()
	topo, ok, _ := unstructured.NestedMap(hrValues(t, hr), TopologyValuesKey, "topology")
	if !ok {
		t.Fatalf("HR %q carries no openova.topology", hr.GetName())
	}
	return topo
}

func render2(t *testing.T, topology bpv1alpha1.BcpTopology, values map[string]interface{}) []*unstructured.Unstructured {
	t.Helper()
	v := twoClusterVariant()
	hrs, err := FanoutHRs(FanoutInputs{
		AppName:      "wp",
		AppNamespace: "acme",
		Topology:     topology,
		Variant:      &v,
		Chart:        "bp-wordpress-tenant",
		RegionFor:    testRegionFor,
		Values:       values,
	})
	if err != nil {
		t.Fatal(err)
	}
	return hrs
}

// D2 — each leg states mode/self/primary/peers for its own region.
func TestFanoutHRs_ADR0015_TopologyContractPerRegion(t *testing.T) {
	hrs := render2(t, bpv1alpha1.BcpActiveHotStandby, map[string]interface{}{
		"openova": map[string]interface{}{"keep": "me"},
	})
	active, standby := hrByRole(t, hrs, RoleActive), hrByRole(t, hrs, RolePassive)

	want := map[string]map[string]interface{}{
		active.GetName(): {
			"mode": "active-hot-standby", "self": testRegionA, "primary": testRegionA,
			"peers": []interface{}{testRegionB},
		},
		standby.GetName(): {
			"mode": "active-hot-standby", "self": testRegionB, "primary": testRegionA,
			"peers": []interface{}{testRegionA},
		},
	}
	for _, hr := range []*unstructured.Unstructured{active, standby} {
		if got := topologyOf(t, hr); !reflect.DeepEqual(got, want[hr.GetName()]) {
			t.Errorf("%s openova.topology = %#v, want %#v", hr.GetName(), got, want[hr.GetName()])
		}
		if keep, _, _ := unstructured.NestedString(hrValues(t, hr), "openova", "keep"); keep != "me" {
			t.Errorf("%s dropped a sibling key under openova", hr.GetName())
		}
	}
	if active.GetLabels()[LabelRegion] != testRegionA || standby.GetLabels()[LabelRegion] != testRegionB {
		t.Fatalf("region labels = %q / %q", active.GetLabels()[LabelRegion], standby.GetLabels()[LabelRegion])
	}
}

// A hot standby streams in its own region: no replicas:0, no pivot needed.
func TestFanoutHRs_ADR0015_HotStandbyStreamsWithoutCrossRegionPivot(t *testing.T) {
	hrs := render2(t, bpv1alpha1.BcpActiveHotStandby, map[string]interface{}{"replicas": int64(2)})
	standby := hrByRole(t, hrs, RolePassive)
	if got := hrValues(t, standby)["replicas"]; got != int64(2) {
		t.Fatalf("hot standby replicas = %v, want 2", got)
	}
	if _, has, _ := unstructured.NestedMap(standby.Object, "spec", "kubeConfig"); has {
		t.Fatal("no leg may carry a kubeConfig pivot into another region")
	}
	for k := range standby.GetLabels() {
		if strings.Contains(k, "standby-delivery") {
			t.Fatalf("the local-undelivered label class is retired, found %q", k)
		}
	}
}

func TestFanoutHRs_ADR0015_ColdStandbyScaledDown(t *testing.T) {
	hrs := render2(t, bpv1alpha1.BcpActivePassive, map[string]interface{}{"replicas": int64(2)})
	if got := hrValues(t, hrByRole(t, hrs, RolePassive))["replicas"]; got != int64(0) {
		t.Fatalf("cold standby replicas = %v, want 0", got)
	}
	if got := hrValues(t, hrByRole(t, hrs, RoleActive))["replicas"]; got != int64(2) {
		t.Fatalf("active replicas = %v, want 2", got)
	}
}

func TestFanoutHRs_ADR0015_SingletonHasNoPeers(t *testing.T) {
	v := bpv1alpha1.TopologyVariant{Placement: &bpv1alpha1.PlacementSpec{Clusters: []string{"rtz-B"}}}
	hrs, err := FanoutHRs(FanoutInputs{AppName: "x", Topology: bpv1alpha1.BcpSingleton, Variant: &v, RegionFor: testRegionFor})
	if err != nil {
		t.Fatal(err)
	}
	topo := topologyOf(t, hrs[0])
	if topo["self"] != testRegionB || topo["primary"] != testRegionB || len(topo["peers"].([]interface{})) != 0 {
		t.Fatalf("singleton contract = %#v", topo)
	}
}

// D3 — a leg whose region is unknown is never rendered.
func TestFanoutHRs_ADR0015_UnknownRegionFails(t *testing.T) {
	v := twoClusterVariant()
	if _, err := FanoutHRs(FanoutInputs{AppName: "x", Variant: &v}); err == nil {
		t.Fatal("nil RegionFor must fail")
	}
	onlyA := func(c string) string {
		if c == "rtz-A" {
			return testRegionA
		}
		return ""
	}
	if _, err := FanoutHRs(FanoutInputs{AppName: "x", Variant: &v, RegionFor: onlyA}); err == nil {
		t.Fatal("a cluster with no region must fail, never fall back to the local region")
	}
}

// D1 — one file per region, stable bytes, each file holds only its region.
func TestRegionFiles_OneFilePerRegion(t *testing.T) {
	hrs := render2(t, bpv1alpha1.BcpActiveHotStandby, nil)
	SortHRsForReconcile(hrs)
	files, err := RegionFiles(hrs)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	for region, b := range files {
		var hr map[string]interface{}
		if err := yaml.Unmarshal(b, &hr); err != nil {
			t.Fatalf("%s: %v", region, err)
		}
		u := &unstructured.Unstructured{Object: hr}
		if u.GetKind() != "HelmRelease" || u.GetLabels()[LabelRegion] != region {
			t.Fatalf("file for %s holds %s labelled %q", region, u.GetKind(), u.GetLabels()[LabelRegion])
		}
		self, _, _ := unstructured.NestedString(u.Object, "spec", "values", "openova", "topology", "self")
		if self != region {
			t.Fatalf("file for %s states self=%s", region, self)
		}
	}
	again, _ := RegionFiles(hrs)
	if !reflect.DeepEqual(files, again) {
		t.Fatal("RegionFiles must be byte-stable")
	}
	if got := RegionAppPath(testRegionB, "wp"); got != "regions/"+testRegionB+"/apps/wp.yaml" {
		t.Fatalf("RegionAppPath = %s", got)
	}
}

func TestRegionFiles_RejectsUnlabelled(t *testing.T) {
	u := &unstructured.Unstructured{}
	u.SetKind("HelmRelease")
	u.SetName("x")
	if _, err := RegionFiles([]*unstructured.Unstructured{u}); err == nil {
		t.Fatal("an object without a region label must be rejected")
	}
}
