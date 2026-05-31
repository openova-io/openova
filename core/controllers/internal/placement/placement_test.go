package placement

import (
	"testing"
)

func TestResolveSingleRegion(t *testing.T) {
	p, err := Resolve(ModeSingleRegion, []string{"hetzner-fsn-rtz-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.PrimaryRegion != "hetzner-fsn-rtz-prod" {
		t.Errorf("primary region = %q", p.PrimaryRegion)
	}
	if len(p.Regions) != 1 || p.Regions[0].Role != RolePrimary || p.Regions[0].Standby {
		t.Errorf("region plan = %+v", p.Regions)
	}
}

func TestResolveSingleRegionRejectsMulti(t *testing.T) {
	_, err := Resolve(ModeSingleRegion, []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for single-region with 2 entries")
	}
}

func TestResolveActiveActive(t *testing.T) {
	p, err := Resolve(ModeActiveActive, []string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.PrimaryRegion != "" {
		t.Errorf("active-active should have no primary, got %q", p.PrimaryRegion)
	}
	if len(p.Regions) != 2 {
		t.Fatalf("expected 2 regions, got %d", len(p.Regions))
	}
	for _, r := range p.Regions {
		if r.Role != RoleActive {
			t.Errorf("expected Role=active, got %q", r.Role)
		}
		if r.Standby {
			t.Errorf("active-active region should not be standby")
		}
	}
	// Sorted by name for byte-stable output.
	if p.Regions[0].Name >= p.Regions[1].Name {
		t.Errorf("active-active output not sorted: %v", p.Regions)
	}
}

func TestResolveActiveHotStandby(t *testing.T) {
	p, err := Resolve(ModeActiveHotStandby, []string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod", "hetzner-hel-rtz-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.PrimaryRegion != "hetzner-fsn-rtz-prod" {
		t.Errorf("primary region = %q", p.PrimaryRegion)
	}
	if p.Regions[0].Role != RolePrimary || p.Regions[0].Standby {
		t.Errorf("regions[0] should be primary not standby: %+v", p.Regions[0])
	}
	for i := 1; i < len(p.Regions); i++ {
		if p.Regions[i].Role != RoleStandby || !p.Regions[i].Standby {
			t.Errorf("regions[%d] should be standby: %+v", i, p.Regions[i])
		}
	}
}

func TestResolveEmptyRegions(t *testing.T) {
	_, err := Resolve(ModeSingleRegion, []string{})
	if err == nil {
		t.Fatal("expected error for empty regions")
	}
}

func TestResolveUnknownMode(t *testing.T) {
	_, err := Resolve("bogus", []string{"a"})
	if err == nil {
		t.Fatal("expected error for unknown mode")
	}
}

func TestAllowedByBlueprint(t *testing.T) {
	if !AllowedByBlueprint(ModeSingleRegion, nil) {
		t.Error("nil allowed list should match all modes")
	}
	if !AllowedByBlueprint(ModeSingleRegion, []string{ModeSingleRegion, ModeActiveActive}) {
		t.Error("expected single-region to be allowed")
	}
	if AllowedByBlueprint(ModeActiveHotStandby, []string{ModeSingleRegion}) {
		t.Error("expected active-hotstandby to be rejected")
	}
}

// ── EffectiveDefault (G93.2, Refs #2667) ─────────────────────────────
//
// Pins the four-decision lattice (sovereign topology × Blueprint
// declarations) so a future refactor cannot silently regress the
// Pillar-3 zero-touch contract.

func TestEffectiveDefault_MultiRegionUsesBlueprintOverride(t *testing.T) {
	got := EffectiveDefault(
		SovereignTopologyActiveHotStandby,
		ModeSingleRegion,     // single-knob default
		ModeActiveHotStandby, // multi-region default
	)
	if got != ModeActiveHotStandby {
		t.Fatalf("EffectiveDefault(hot-standby, sr, ahs) = %q, want %q", got, ModeActiveHotStandby)
	}
}

func TestEffectiveDefault_MultiRegionFallsBackToDefault(t *testing.T) {
	// Blueprint did not declare defaultOnMultiRegion — fall back to the
	// existing placementSchema.default so existing Blueprints don't
	// change behaviour.
	got := EffectiveDefault(
		SovereignTopologyActiveHotStandby,
		ModeSingleRegion,
		"",
	)
	if got != ModeSingleRegion {
		t.Fatalf("EffectiveDefault(hot-standby, sr, '') = %q, want %q", got, ModeSingleRegion)
	}
}

func TestEffectiveDefault_SingleRegionIgnoresMultiRegionOverride(t *testing.T) {
	// Single-region Sovereign — defaultOnMultiRegion is irrelevant.
	got := EffectiveDefault(
		SovereignTopologySingleRegion,
		ModeSingleRegion,
		ModeActiveHotStandby,
	)
	if got != ModeSingleRegion {
		t.Fatalf("EffectiveDefault(sr, sr, ahs) = %q, want %q", got, ModeSingleRegion)
	}
}

func TestEffectiveDefault_ActiveActiveUsesMultiRegionOverride(t *testing.T) {
	// Symmetric topology is also multi-region — overrides apply.
	got := EffectiveDefault(
		SovereignTopologyActiveActive,
		ModeSingleRegion,
		ModeActiveActive,
	)
	if got != ModeActiveActive {
		t.Fatalf("EffectiveDefault(aa, sr, aa) = %q, want %q", got, ModeActiveActive)
	}
}

func TestEffectiveDefault_NoBlueprintDefaultsAtAll(t *testing.T) {
	// Defensive: Blueprint forgot to declare either knob — safe last
	// resort is single-region so the controller can still install.
	got := EffectiveDefault(SovereignTopologyActiveHotStandby, "", "")
	if got != ModeSingleRegion {
		t.Fatalf("EffectiveDefault(hot-standby, '', '') = %q, want %q (safe fallback)", got, ModeSingleRegion)
	}
}

func TestEffectiveDefault_UnknownTopologyFallsBackToDefault(t *testing.T) {
	// Defensive: an unset / typo'd SOVEREIGN_BCP_TOPOLOGY env should
	// behave like single-region (no multi-region override).
	got := EffectiveDefault("", ModeSingleRegion, ModeActiveHotStandby)
	if got != ModeSingleRegion {
		t.Fatalf("EffectiveDefault('', sr, ahs) = %q, want %q", got, ModeSingleRegion)
	}
}
