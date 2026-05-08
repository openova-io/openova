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
