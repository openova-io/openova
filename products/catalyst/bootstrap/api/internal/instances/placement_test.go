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

// #5616 — this used to assert that ALL FOUR tiers validate
// unconditionally. That assertion was the defect written down as a test:
// mgmt/dmz/rtz resolve to host namespaces no Sovereign creates, so
// accepting them produced a 201 followed by a permanently Degraded
// Application. The contract is now "vocabulary AND availability"; the
// availability half is covered in placement_availability_5616_test.go.
func TestValidateShape_PlacementValidTiers(t *testing.T) {
	for _, tier := range []string{"", "host", "mgmt", "dmz", "rtz"} {
		t.Run(tier, func(t *testing.T) {
			// Every tier in the VOCABULARY is well-formed; the only way
			// one may be refused is the availability gate.
			SetAvailableVClusterTiers("mgmt,dmz,rtz")
			t.Cleanup(func() { SetAvailableVClusterTiers("") })
			r := validReq()
			r.Placement = &InstancePlacementRequest{VCluster: tier}
			if err := r.ValidateShape(); err != nil {
				t.Errorf("vcluster %q must validate when the tier is installed, got %v", tier, err)
			}
		})
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
	// #5616 — pin the tier to one that is always available so this test
	// keeps exercising the CLUSTERS check rather than tripping the new
	// availability gate first.
	r := validReq()
	r.Placement = &InstancePlacementRequest{VCluster: "host", Clusters: []string{"mgmt-A", " "}}
	err := r.ValidateShape()
	if err == nil || err.Code != "placement-clusters-invalid" {
		t.Fatalf("want placement-clusters-invalid, got %v", err)
	}
}

func TestBuild_PlacementPassesThroughToSeed(t *testing.T) {
	// #5616 — an INSTALLED rtz tier must still pass straight through to
	// the seed. This is the vacuity check on the availability gate: it
	// refuses tiers the Sovereign lacks, never tiers it has.
	SetAvailableVClusterTiers("rtz")
	t.Cleanup(func() { SetAvailableVClusterTiers("") })
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
