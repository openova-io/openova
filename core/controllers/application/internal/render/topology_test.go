// G117.6 (W2.C1) topology resolver tests.
//
// Per the brief's anti-theater red-flag list: "Unit tests that assert
// only the multi-region case — single-region must also be covered".
// We cover every (variant × Sovereign-region-count) permutation +
// every documented error path.

package render

import (
	"errors"
	"testing"

	bpv1alpha1 "github.com/openova-io/openova/core/controllers/pkg/apis/blueprint/v1alpha1"
)

// fixtureGrafanaTopology returns a typed Topology that matches the
// shape examples/blueprint-grafana-fully-declared.yaml lands on main
// after W1.B1. Used as a representative `supported = {active-hot-
// standby, singleton}` multi-region-capable Blueprint.
func fixtureGrafanaTopology() *bpv1alpha1.Topology {
	lag5 := 5
	rto30 := 30
	rpo0 := 0
	return &bpv1alpha1.Topology{
		Supported: []bpv1alpha1.BcpTopology{
			bpv1alpha1.BcpActiveHotStandby,
			bpv1alpha1.BcpSingleton,
		},
		Defaults: bpv1alpha1.TopologyDefaults{
			MultiRegion:  bpv1alpha1.BcpActiveHotStandby,
			SingleRegion: bpv1alpha1.BcpSingleton,
		},
		PerTopology: map[bpv1alpha1.BcpTopology]bpv1alpha1.TopologyVariant{
			bpv1alpha1.BcpActiveHotStandby: {
				Replication: &bpv1alpha1.ReplicationSpec{
					Backend:       "cnpg-pair",
					Mode:          "sync",
					LagSloSeconds: &lag5,
				},
				Switchover: &bpv1alpha1.SwitchoverSpec{
					Mechanism:  "bp-continuum",
					RtoSeconds: &rto30,
					RpoSeconds: &rpo0,
				},
				Placement: &bpv1alpha1.PlacementSpec{
					Tier:     "mgmt",
					Clusters: []string{"mgmt-A", "mgmt-B"},
					Roles: map[string]string{
						"mgmt-A": "active",
						"mgmt-B": "passive",
					},
				},
			},
			bpv1alpha1.BcpSingleton: {
				Placement: &bpv1alpha1.PlacementSpec{
					Tier:     "mgmt",
					Clusters: []string{"mgmt-A"},
					Roles: map[string]string{
						"mgmt-A": "singleton",
					},
				},
			},
		},
	}
}

// fixtureCiliumTopology — a per-host-cluster substrate Blueprint that
// supports singleton only. Used as the "Sovereign-shape is irrelevant
// because there's only one supported variant" coverage row.
func fixtureCiliumTopology() *bpv1alpha1.Topology {
	return &bpv1alpha1.Topology{
		Supported: []bpv1alpha1.BcpTopology{bpv1alpha1.BcpSingleton},
		Defaults: bpv1alpha1.TopologyDefaults{
			MultiRegion:  bpv1alpha1.BcpSingleton,
			SingleRegion: bpv1alpha1.BcpSingleton,
		},
		PerTopology: map[bpv1alpha1.BcpTopology]bpv1alpha1.TopologyVariant{
			bpv1alpha1.BcpSingleton: {
				Placement: &bpv1alpha1.PlacementSpec{
					Clusters: []string{"mgmt-A"},
				},
			},
		},
	}
}

func TestResolveTopology_OperatorOverride_Supported(t *testing.T) {
	bp := fixtureGrafanaTopology()
	choice, variant, err := ResolveTopology("singleton", bp, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bpv1alpha1.BcpSingleton {
		t.Fatalf("override should win: got %q, want singleton", choice)
	}
	if variant == nil {
		t.Fatalf("variant should be non-nil for singleton")
	}
}

func TestResolveTopology_OperatorOverride_Unsupported(t *testing.T) {
	bp := fixtureGrafanaTopology()
	_, _, err := ResolveTopology("active-active", bp, 2)
	if err == nil || !errors.Is(err, ErrInvalidTopology) {
		t.Fatalf("expected ErrInvalidTopology for active-active vs %v; got %v",
			bp.Supported, err)
	}
}

func TestResolveTopology_DefaultMultiRegion(t *testing.T) {
	bp := fixtureGrafanaTopology()
	choice, variant, err := ResolveTopology("", bp, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bpv1alpha1.BcpActiveHotStandby {
		t.Fatalf("multi-region default should be active-hot-standby; got %q", choice)
	}
	if variant == nil || variant.Placement == nil {
		t.Fatalf("variant + placement must be non-nil")
	}
	if got := len(variant.Placement.Clusters); got != 2 {
		t.Fatalf("active-hot-standby variant must place on 2 clusters; got %d", got)
	}
}

func TestResolveTopology_DefaultSingleRegion(t *testing.T) {
	bp := fixtureGrafanaTopology()
	choice, variant, err := ResolveTopology("", bp, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bpv1alpha1.BcpSingleton {
		t.Fatalf("single-region default should be singleton; got %q", choice)
	}
	if variant == nil || variant.Placement == nil {
		t.Fatalf("variant + placement must be non-nil")
	}
	if got := len(variant.Placement.Clusters); got != 1 {
		t.Fatalf("singleton variant must place on 1 cluster; got %d", got)
	}
}

func TestResolveTopology_ZeroRegions_TreatsAsSingleRegion(t *testing.T) {
	// Edge: an uninitialised Sovereign reports 0 regions before its
	// region list lands. The resolver must NOT panic — it should
	// treat <2 as single-region (locked decision #7).
	bp := fixtureGrafanaTopology()
	choice, _, err := ResolveTopology("", bp, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bpv1alpha1.BcpSingleton {
		t.Fatalf("0 regions should resolve to single-region default; got %q", choice)
	}
}

func TestResolveTopology_SubstrateBlueprint(t *testing.T) {
	// bp-cilium-style — supports only singleton. Both region shapes
	// must resolve to singleton without error.
	bp := fixtureCiliumTopology()
	for _, regions := range []int{1, 2, 6} {
		choice, variant, err := ResolveTopology("", bp, regions)
		if err != nil {
			t.Fatalf("regions=%d unexpected error: %v", regions, err)
		}
		if choice != bpv1alpha1.BcpSingleton {
			t.Fatalf("regions=%d should resolve to singleton; got %q", regions, choice)
		}
		if variant == nil {
			t.Fatalf("variant must be non-nil")
		}
	}
}

func TestResolveTopology_NilTopology(t *testing.T) {
	_, _, err := ResolveTopology("", nil, 2)
	if err == nil || !errors.Is(err, ErrMissingTopology) {
		t.Fatalf("expected ErrMissingTopology; got %v", err)
	}
}

func TestResolveTopology_EmptySupported(t *testing.T) {
	bp := &bpv1alpha1.Topology{}
	_, _, err := ResolveTopology("", bp, 2)
	if err == nil || !errors.Is(err, ErrMissingTopology) {
		t.Fatalf("expected ErrMissingTopology for empty supported; got %v", err)
	}
}

func TestResolveTopology_MissingDefault(t *testing.T) {
	// Defaults block missing the multi-region key — should surface
	// InvalidTopology rather than silently picking supported[0].
	bp := &bpv1alpha1.Topology{
		Supported: []bpv1alpha1.BcpTopology{bpv1alpha1.BcpActiveHotStandby},
		Defaults: bpv1alpha1.TopologyDefaults{
			SingleRegion: bpv1alpha1.BcpActiveHotStandby,
		},
	}
	_, _, err := ResolveTopology("", bp, 2)
	if err == nil || !errors.Is(err, ErrInvalidTopology) {
		t.Fatalf("expected ErrInvalidTopology for missing multi-region default; got %v", err)
	}
}

func TestResolveTopology_DefaultNotInSupported(t *testing.T) {
	// Defaults.MultiRegion points at a topology absent from Supported
	// (schema gate should have caught this — defensive failure mode).
	bp := &bpv1alpha1.Topology{
		Supported: []bpv1alpha1.BcpTopology{bpv1alpha1.BcpSingleton},
		Defaults: bpv1alpha1.TopologyDefaults{
			MultiRegion:  bpv1alpha1.BcpActiveActive, // not in supported
			SingleRegion: bpv1alpha1.BcpSingleton,
		},
	}
	_, _, err := ResolveTopology("", bp, 2)
	if err == nil || !errors.Is(err, ErrInvalidTopology) {
		t.Fatalf("expected ErrInvalidTopology for default not in supported; got %v", err)
	}
}

func TestResolveTopology_TableDriven_VariantsTimesRegionShapes(t *testing.T) {
	// Brief acceptance row: "every variant × every Sovereign-region-
	// count permutation".
	type row struct {
		name             string
		override         string
		sovereignRegions int
		wantChoice       bpv1alpha1.BcpTopology
		wantErr          bool
	}
	bp := fixtureGrafanaTopology()
	rows := []row{
		// override paths
		{"override-singleton-multireg-sovereign", "singleton", 2, bpv1alpha1.BcpSingleton, false},
		{"override-singleton-singlereg-sovereign", "singleton", 1, bpv1alpha1.BcpSingleton, false},
		{"override-ahs-multireg-sovereign", "active-hot-standby", 2, bpv1alpha1.BcpActiveHotStandby, false},
		{"override-ahs-singlereg-sovereign", "active-hot-standby", 1, bpv1alpha1.BcpActiveHotStandby, false},
		{"override-unsupported-multireg", "active-active", 2, "", true},
		// default paths
		{"default-multireg-sovereign", "", 2, bpv1alpha1.BcpActiveHotStandby, false},
		{"default-singlereg-sovereign", "", 1, bpv1alpha1.BcpSingleton, false},
		{"default-zero-regions", "", 0, bpv1alpha1.BcpSingleton, false},
		// many-region sovereign — still multi-region default
		{"default-6-regions", "", 6, bpv1alpha1.BcpActiveHotStandby, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			choice, _, err := ResolveTopology(r.override, bp, r.sovereignRegions)
			if r.wantErr {
				if err == nil {
					t.Fatalf("expected error; got choice=%q", choice)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if choice != r.wantChoice {
				t.Fatalf("choice = %q, want %q", choice, r.wantChoice)
			}
		})
	}
}

func TestResolveTopology_VariantNilForMissingPerTopologyKey(t *testing.T) {
	// A Blueprint that declares Supported includes a topology but
	// doesn't populate PerTopology[<choice>] (rare; W1.B1 admission
	// gate should reject, but G92.6 hollow-chart-substrate Blueprints
	// sometimes ship Supported only). Resolver returns
	// (choice, nil, nil) so callers can pick a safe default.
	bp := &bpv1alpha1.Topology{
		Supported: []bpv1alpha1.BcpTopology{bpv1alpha1.BcpSingleton},
		Defaults: bpv1alpha1.TopologyDefaults{
			MultiRegion:  bpv1alpha1.BcpSingleton,
			SingleRegion: bpv1alpha1.BcpSingleton,
		},
		// PerTopology intentionally nil
	}
	choice, variant, err := ResolveTopology("", bp, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice != bpv1alpha1.BcpSingleton {
		t.Fatalf("want singleton; got %q", choice)
	}
	if variant != nil {
		t.Fatalf("want nil variant when PerTopology absent; got %+v", variant)
	}
}
