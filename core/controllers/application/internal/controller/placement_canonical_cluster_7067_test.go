package controller

import (
	"testing"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
	"github.com/openova-io/openova/core/controllers/pkg/regions"
)

// A Topology Save carries the console's cluster ids; the HelmRelease identity
// must stay `<app>-rtz-a` / `<app>-rtz-b` (Refs #7067).
func TestCanonicalizeTargetClusters(t *testing.T) {
	reg, err := regions.Parse(`[{"cloudRegion":"hw-me-east-215-a-rtz-prod"},{"cloudRegion":"hw-me-east-215-b-rtz-prod"}]`)
	if err != nil {
		t.Fatal(err)
	}
	r := &Reconciler{Cfg: Config{Regions: reg}}
	in := []bpv1alpha1.PlacementTarget{
		{Region: "hw-me-east-215-a-rtz-prod", Cluster: "9a1f230f320d7ff9", Role: "Standby"},
		{Region: "hw-me-east-215-b-rtz-prod", Cluster: "9a1f230f320d7ff9-me-east-215-b-1", Role: "Primary"},
		{Region: "hw-me-east-215-a-rtz-prod", Cluster: "mgmt-A"},
		{Region: "elsewhere", Cluster: "x"},
	}
	out := r.canonicalizeTargetClusters(in)
	want := []string{"rtz-A", "rtz-B", "mgmt-A", "x"}
	for i := range want {
		if out[i].Cluster != want[i] {
			t.Errorf("target %d cluster = %q, want %q", i, out[i].Cluster, want[i])
		}
	}
	if in[0].Cluster != "9a1f230f320d7ff9" {
		t.Errorf("input mutated")
	}
}
