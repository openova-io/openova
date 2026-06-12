// Tests for #3373 — instance placement on the create-instance wire.
package instances

import "testing"

func validReq() CreateInstanceRequest {
	return CreateInstanceRequest{
		Blueprint: "bp-grafana",
		Org:       "acme",
		Name:      "obs",
	}
}

func TestValidateShape_PlacementNil_OK(t *testing.T) {
	r := validReq()
	if err := r.ValidateShape(); err != nil {
		t.Fatalf("nil placement (silent-accept default flow) must validate, got %v", err)
	}
}

func TestValidateShape_PlacementValidTiers(t *testing.T) {
	for _, tier := range []string{"", "host", "mgmt", "dmz", "rtz"} {
		r := validReq()
		r.Placement = &InstancePlacementRequest{VCluster: tier}
		if err := r.ValidateShape(); err != nil {
			t.Errorf("vcluster %q must validate, got %v", tier, err)
		}
	}
}

func TestValidateShape_PlacementUnknownTierRejected(t *testing.T) {
	r := validReq()
	r.Placement = &InstancePlacementRequest{VCluster: "warp-zone"}
	err := r.ValidateShape()
	if err == nil || err.Code != "placement-vcluster-invalid" {
		t.Fatalf("want placement-vcluster-invalid, got %v", err)
	}
}

func TestValidateShape_PlacementEmptyClusterEntryRejected(t *testing.T) {
	r := validReq()
	r.Placement = &InstancePlacementRequest{VCluster: "rtz", Clusters: []string{"mgmt-A", " "}}
	err := r.ValidateShape()
	if err == nil || err.Code != "placement-clusters-invalid" {
		t.Fatalf("want placement-clusters-invalid, got %v", err)
	}
}

func TestBuild_PlacementPassesThroughToSeed(t *testing.T) {
	r := validReq()
	r.Placement = &InstancePlacementRequest{
		VCluster: "rtz",
		Regions:  []string{"hetzner-fsn-rtz-prod"},
		Clusters: []string{"mgmt-A"},
	}
	seed, err := r.Build("singleton")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if seed.Placement == nil || seed.Placement.VCluster != "rtz" {
		t.Fatalf("seed.Placement = %+v, want vcluster=rtz", seed.Placement)
	}
	if len(seed.Placement.Regions) != 1 || len(seed.Placement.Clusters) != 1 {
		t.Fatalf("seed.Placement regions/clusters not passed through: %+v", seed.Placement)
	}
}

func TestBuild_NoPlacement_SeedNil(t *testing.T) {
	r := validReq()
	seed, err := r.Build("singleton")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if seed.Placement != nil {
		t.Fatalf("seed.Placement must stay nil when the user accepted defaults, got %+v", seed.Placement)
	}
}
