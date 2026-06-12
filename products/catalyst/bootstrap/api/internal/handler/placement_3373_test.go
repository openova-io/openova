// Tests for #3373 — the dual-form spec.placement stamp on the two
// Application-CR write paths (install handler + create-instance seed)
// and the placement read-back helpers.
package handler

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/instances"
)

func TestNewApplicationUnstructured_LegacyStringForm(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement:       applicationPlacement{Mode: "single-region", Regions: []string{"fsn"}},
	}
	obj := newApplicationUnstructured(req)
	pl, ok, err := unstructured.NestedString(obj.Object, "spec", "placement")
	if err != nil || !ok || pl != "single-region" {
		t.Fatalf("legacy callers must keep the byte-identical string form, got (%q, %v, %v)", pl, ok, err)
	}
}

func TestNewApplicationUnstructured_ObjectFormWhenVClusterSet(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:     "single-region",
			Regions:  []string{"fsn"},
			VCluster: "rtz",
			Clusters: []string{"mgmt-A"},
		},
	}
	obj := newApplicationUnstructured(req)
	vc, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "vcluster")
	if vc != "rtz" {
		t.Fatalf("spec.placement.vcluster = %q, want rtz", vc)
	}
	mode, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "mode")
	if mode != "single-region" {
		t.Fatalf("spec.placement.mode = %q, want single-region", mode)
	}
	clusters, _, _ := unstructured.NestedSlice(obj.Object, "spec", "placement", "clusters")
	if len(clusters) != 1 || clusters[0] != "mgmt-A" {
		t.Fatalf("spec.placement.clusters = %v, want [mgmt-A]", clusters)
	}
	// legacy top-level regions[] stays stamped (CRD requires it).
	regions, _, _ := unstructured.NestedSlice(obj.Object, "spec", "regions")
	if len(regions) != 1 {
		t.Fatalf("spec.regions = %v, want 1 entry", regions)
	}
}

func TestValidateApplicationInstall_RejectsUnknownVCluster(t *testing.T) {
	req := applicationInstallRequest{
		BlueprintRef:    applicationBlueprintRef{Name: "bp-wp", Version: "1.0.0"},
		Name:            "site",
		OrganizationRef: "acme",
		EnvironmentRef:  "acme-prod",
		Placement: applicationPlacement{
			Mode:     "single-region",
			Regions:  []string{"fsn"},
			VCluster: "warp-zone",
		},
	}
	if msg, ok := validateApplicationInstallRequest(req); ok {
		t.Fatalf("unknown vcluster must be rejected, got ok (msg=%q)", msg)
	}
}

func TestNewApplicationCRFromSeed_PlacementObjectStamped(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "obs",
		Namespace: "acme",
		Blueprint: "bp-grafana",
		Topology:  "singleton",
		Placement: &instances.InstancePlacementRequest{
			VCluster: "rtz",
			Regions:  []string{"hetzner-fsn-rtz-prod"},
		},
	}
	obj := newApplicationCRFromSeed(seed)
	vc, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "vcluster")
	if vc != "rtz" {
		t.Fatalf("spec.placement.vcluster = %q, want rtz", vc)
	}
	mode, _, _ := unstructured.NestedString(obj.Object, "spec", "placement", "mode")
	if mode != "singleton" {
		t.Fatalf("spec.placement.mode = %q, want singleton (the chosen topology)", mode)
	}
	// placement.regions overrides the "primary" sentinel.
	regions, _, _ := unstructured.NestedSlice(obj.Object, "spec", "regions")
	if len(regions) != 1 || regions[0] != "hetzner-fsn-rtz-prod" {
		t.Fatalf("spec.regions = %v, want the placement regions", regions)
	}
}

func TestNewApplicationCRFromSeed_NoPlacement_LegacyString(t *testing.T) {
	seed := instances.ApplicationSeed{
		Name:      "obs",
		Namespace: "acme",
		Blueprint: "bp-grafana",
		Topology:  "singleton",
	}
	obj := newApplicationCRFromSeed(seed)
	pl, ok, err := unstructured.NestedString(obj.Object, "spec", "placement")
	if err != nil || !ok || pl != "singleton" {
		t.Fatalf("silent-accept flow must keep the legacy string form, got (%q, %v, %v)", pl, ok, err)
	}
}

func TestPlacementInfoFromCR_StatusWinsOverSpec(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"placement": map[string]interface{}{"vcluster": "mgmt"},
		},
		"status": map[string]interface{}{
			"placement": map[string]interface{}{
				"vcluster": "rtz", "source": "instance",
				"regions": []interface{}{"hetzner-fsn-rtz-prod"},
			},
		},
	}}
	pi := placementInfoFromCR(u)
	if pi == nil || pi.VCluster != "rtz" || pi.Source != "instance" {
		t.Fatalf("status.placement (the reconciled effective truth) must win, got %+v", pi)
	}
}

func TestPlacementInfoFromCR_LegacyStringYieldsNil(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{"placement": "single-region"},
	}}
	if pi := placementInfoFromCR(u); pi != nil {
		t.Fatalf("legacy string-form spec without status must yield nil, got %+v", pi)
	}
}

func TestReadTopology_ObjectFormMode(t *testing.T) {
	u := &unstructured.Unstructured{Object: map[string]interface{}{
		"spec": map[string]interface{}{
			"placement": map[string]interface{}{"vcluster": "mgmt", "mode": "active-hotstandby"},
		},
	}}
	if got := readTopology(u); got != "active-hotstandby" {
		t.Fatalf("readTopology object-form = %q, want active-hotstandby", got)
	}
}
