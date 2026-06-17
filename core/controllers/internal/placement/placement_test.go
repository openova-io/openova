package placement

import (
	"testing"
)

func TestResolveSingleton(t *testing.T) {
	p, err := Resolve(ModeSingleton, []string{"hetzner-fsn-rtz-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Mode != ModeSingleton {
		t.Errorf("Plan.Mode = %q, want canonical %q", p.Mode, ModeSingleton)
	}
	if p.PrimaryRegion != "hetzner-fsn-rtz-prod" {
		t.Errorf("primary region = %q", p.PrimaryRegion)
	}
	if len(p.Regions) != 1 || p.Regions[0].Role != RolePrimary || p.Regions[0].Standby {
		t.Errorf("region plan = %+v", p.Regions)
	}
}

// The legacy spelling resolves IDENTICALLY to the canonical token —
// one vocabulary on the wire (#3375 DoD-1), legacy aliases folded so no
// existing CR / blueprint.yaml / in-flight POST breaks.
func TestResolveSingleton_LegacyAliasFoldsToCanonical(t *testing.T) {
	p, err := Resolve(ModeSingleRegion /* "single-region" */, []string{"hetzner-fsn-rtz-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Mode != ModeSingleton {
		t.Errorf("legacy single-region must resolve to canonical singleton, got %q", p.Mode)
	}
}

func TestResolveSingletonRejectsMulti(t *testing.T) {
	_, err := Resolve(ModeSingleton, []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error for singleton with 2 entries")
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
	if p.Mode != ModeActiveHotStandby {
		t.Errorf("Plan.Mode = %q, want canonical %q", p.Mode, ModeActiveHotStandby)
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

// The legacy spelling active-hotstandby folds onto the canonical token
// AND produces the same fan-out plan.
func TestResolveActiveHotStandby_LegacyAliasFoldsToCanonical(t *testing.T) {
	p, err := Resolve(LegacyModeActiveHotStandby /* "active-hotstandby" */, []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Mode != ModeActiveHotStandby {
		t.Errorf("legacy active-hotstandby must resolve to canonical active-hot-standby, got %q", p.Mode)
	}
	if !p.Regions[1].Standby {
		t.Errorf("regions[1] should be standby (replicas:0): %+v", p.Regions[1])
	}
}

// active-passive is the fourth canonical class. It shares the SAME
// fan-out plan as active-hot-standby (primary + replicas:0 standbys) —
// the two differ only in the Blueprint's replication/switchover knobs.
func TestResolveActivePassive(t *testing.T) {
	p, err := Resolve(ModeActivePassive, []string{"hetzner-fsn-rtz-prod", "hetzner-nbg-rtz-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Mode != ModeActivePassive {
		t.Errorf("Plan.Mode = %q, want canonical %q", p.Mode, ModeActivePassive)
	}
	if p.PrimaryRegion != "hetzner-fsn-rtz-prod" {
		t.Errorf("primary region = %q", p.PrimaryRegion)
	}
	if p.Regions[0].Role != RolePrimary || p.Regions[0].Standby {
		t.Errorf("regions[0] should be primary not standby: %+v", p.Regions[0])
	}
	if p.Regions[1].Role != RoleStandby || !p.Regions[1].Standby {
		t.Errorf("regions[1] should be standby (replicas:0): %+v", p.Regions[1])
	}
}

func TestResolveEmptyRegions(t *testing.T) {
	_, err := Resolve(ModeSingleton, []string{})
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

// ── Canonicalize / vocabulary contract ───────────────────────────────

func TestCanonicalize_FoldsEverySpelling(t *testing.T) {
	cases := map[string]string{
		// canonical → itself
		"singleton":          ModeSingleton,
		"active-active":      ModeActiveActive,
		"active-hot-standby": ModeActiveHotStandby,
		"active-passive":     ModeActivePassive,
		// legacy → canonical
		"single-region":    ModeSingleton,
		"active-hotstandby": ModeActiveHotStandby,
		// underscore + case variants
		"ACTIVE_HOT_STANDBY": ModeActiveHotStandby,
		"  Single-Region ":   ModeSingleton,
		// unknown → trimmed/lowered, unchanged otherwise
		"bogus": "bogus",
		"":      "",
	}
	for in, want := range cases {
		if got := Canonicalize(in); got != want {
			t.Errorf("Canonicalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanonicalModes_IsTheFourCanonicalTokens(t *testing.T) {
	got := CanonicalModes()
	want := []string{"singleton", "active-active", "active-hot-standby", "active-passive"}
	if len(got) != len(want) {
		t.Fatalf("CanonicalModes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CanonicalModes()[%d] = %q, want %q", i, got[i], want[i])
		}
		if !IsCanonicalMode(got[i]) {
			t.Errorf("IsCanonicalMode(%q) = false, want true", got[i])
		}
	}
	// No legacy spelling is a canonical token.
	for _, legacy := range []string{"single-region", "active-hotstandby"} {
		if IsCanonicalMode(legacy) {
			t.Errorf("IsCanonicalMode(%q) = true, want false (legacy spelling is NOT canonical)", legacy)
		}
	}
}

func TestAllowedByBlueprint(t *testing.T) {
	if !AllowedByBlueprint(ModeSingleton, nil) {
		t.Error("nil allowed list should match all modes")
	}
	if !AllowedByBlueprint(ModeSingleton, []string{ModeSingleton, ModeActiveActive}) {
		t.Error("expected singleton to be allowed")
	}
	if AllowedByBlueprint(ModeActiveHotStandby, []string{ModeSingleton}) {
		t.Error("expected active-hot-standby to be rejected")
	}
	// Cross-spelling: a Blueprint declaring the LEGACY spelling in
	// placementSchema.modes still admits a CANONICAL instance and vice
	// versa — the one-vocabulary rule must not regress the corpus.
	if !AllowedByBlueprint(ModeSingleton, []string{"single-region", "active-active"}) {
		t.Error("canonical singleton must match legacy single-region in modes[]")
	}
	if !AllowedByBlueprint("single-region", []string{ModeSingleton, ModeActiveActive}) {
		t.Error("legacy single-region must match canonical singleton in modes[]")
	}
	if !AllowedByBlueprint("active-hotstandby", []string{ModeActiveHotStandby}) {
		t.Error("legacy active-hotstandby must match canonical active-hot-standby in modes[]")
	}
}

// ── EffectiveDefault (G93.2, Refs #2667) ─────────────────────────────
//
// Pins the four-decision lattice (sovereign topology × Blueprint
// declarations) so a future refactor cannot silently regress the
// Pillar-3 zero-touch contract. Post-#3375 the returned value is always
// a CANONICAL token (legacy spellings in the Blueprint declarations or
// the Sovereign-topology env are folded).

func TestEffectiveDefault_MultiRegionUsesBlueprintOverride(t *testing.T) {
	got := EffectiveDefault(
		SovereignTopologyActiveHotStandby,     // legacy env spelling
		ModeSingleRegion,                      // single-knob default (legacy spelling)
		LegacyModeActiveHotStandby,            // multi-region default (legacy spelling)
	)
	if got != ModeActiveHotStandby {
		t.Fatalf("EffectiveDefault(hot-standby, sr, ahs) = %q, want canonical %q", got, ModeActiveHotStandby)
	}
}

func TestEffectiveDefault_MultiRegionFallsBackToDefault(t *testing.T) {
	// Blueprint did not declare defaultOnMultiRegion — fall back to the
	// existing placementSchema.default (canonicalised) so existing
	// Blueprints don't change behaviour.
	got := EffectiveDefault(
		SovereignTopologyActiveHotStandby,
		ModeSingleRegion,
		"",
	)
	if got != ModeSingleton {
		t.Fatalf("EffectiveDefault(hot-standby, sr, '') = %q, want canonical %q", got, ModeSingleton)
	}
}

func TestEffectiveDefault_SingleRegionIgnoresMultiRegionOverride(t *testing.T) {
	// Single-region Sovereign — defaultOnMultiRegion is irrelevant.
	got := EffectiveDefault(
		SovereignTopologySingleRegion,
		ModeSingleRegion,
		LegacyModeActiveHotStandby,
	)
	if got != ModeSingleton {
		t.Fatalf("EffectiveDefault(sr, sr, ahs) = %q, want canonical %q", got, ModeSingleton)
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
	// resort is the canonical singleton so the controller can still
	// install.
	got := EffectiveDefault(SovereignTopologyActiveHotStandby, "", "")
	if got != ModeSingleton {
		t.Fatalf("EffectiveDefault(hot-standby, '', '') = %q, want canonical %q (safe fallback)", got, ModeSingleton)
	}
}

func TestEffectiveDefault_UnknownTopologyFallsBackToDefault(t *testing.T) {
	// Defensive: an unset / typo'd SOVEREIGN_BCP_TOPOLOGY env should
	// behave like single-region (no multi-region override).
	got := EffectiveDefault("", ModeSingleRegion, LegacyModeActiveHotStandby)
	if got != ModeSingleton {
		t.Fatalf("EffectiveDefault('', sr, ahs) = %q, want canonical %q", got, ModeSingleton)
	}
}
