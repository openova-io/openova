// partial_region_materialisation_test.go — G117 #2840 unit coverage
// for the post-condition that rejects a partial-region `tofu apply`.
//
// Background: hw86 declared 2 region codes (me-east-215-a +
// me-east-215-b) in tofu.auto.tfvars.json but only `-a` materialised
// because HCS Common.0021 quota cascade silently refused `-b`. The
// admission gate at Validate() correctly rejects single-region
// active-hotstandby SUBMISSIONS, but the downstream tofu-apply
// partial-failure has no defense — `tofu apply` returns nil-error
// when the per-region resource block evaluates to an empty `for`
// expression. The catalyst-api then promoted DNS + ran the Phase-1
// watch on a Sovereign that violates Pillar 2 (multi-region BCP) +
// Pillar 3 (zero-tx-loss).
//
// These tests pin the observable behaviours that close that gap:
//
//  1. detectPartialRegionMaterialisation produces a stable
//     (materialised, missing) split when N declared > M materialised.
//  2. Returns empty missing-slice when every declared region came up
//     (the happy path must not false-positive).
//  3. The PartialRegionMaterialisationError carries the Result so the
//     handler can stamp Status="partial-failure" without re-running tofu.
//  4. errors.As round-trips through a wrapped error so the handler's
//     errors.As(&partialErr) path works even if the provisioner ever
//     wraps the typed error in an additional fmt.Errorf("...: %w").
package provisioner

import (
	"errors"
	"strings"
	"testing"
)

// Test 1 — the canonical hw86 shape: 2 declared, 1 materialised,
// 1 missing. detectPartialRegionMaterialisation must enumerate
// materialised in declaration order and report the missing code.
func TestDetectPartialRegionMaterialisation_2DeclaredOneMissing(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	perRegion := map[string]string{
		"me-east-215-a": "100.100.100.10",
		// me-east-215-b deliberately missing — HCS Common.0021.
	}
	materialised, missing := detectPartialRegionMaterialisation(declared, perRegion)
	if len(materialised) != 1 || materialised[0] != "me-east-215-a" {
		t.Errorf("materialised = %v, want [me-east-215-a]", materialised)
	}
	if len(missing) != 1 || missing[0] != "me-east-215-b" {
		t.Errorf("missing = %v, want [me-east-215-b]", missing)
	}
}

// Test 2 — happy path: both declared regions came up. Must produce
// nil missing-slice so the caller's `len(missing) > 0` gate doesn't
// false-positive on a fully-materialised prov.
func TestDetectPartialRegionMaterialisation_HappyPath(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	perRegion := map[string]string{
		"me-east-215-a": "100.100.100.10",
		"me-east-215-b": "100.100.100.20",
	}
	materialised, missing := detectPartialRegionMaterialisation(declared, perRegion)
	if len(materialised) != 2 {
		t.Errorf("materialised = %v, want both regions", materialised)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty (every declared region came up)", missing)
	}
}

// Test 3 — empty per-region map (zero regions materialised). Every
// declared region must show up as missing so the operator can see the
// total wipeout, not just a per-region diff.
func TestDetectPartialRegionMaterialisation_NothingMaterialised(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	perRegion := map[string]string{}
	materialised, missing := detectPartialRegionMaterialisation(declared, perRegion)
	if len(materialised) != 0 {
		t.Errorf("materialised = %v, want empty (no per-region readback)", materialised)
	}
	if len(missing) != 2 {
		t.Errorf("missing = %v, want both regions", missing)
	}
}

// Test 4 — per-region map with an EMPTY string IP (tofu rendered the
// resource address but the EIP allocation returned a blank). Treat as
// missing — an empty IP is not a working CP node.
func TestDetectPartialRegionMaterialisation_EmptyIPCountsAsMissing(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	perRegion := map[string]string{
		"me-east-215-a": "100.100.100.10",
		"me-east-215-b": "", // partial — EIP allocation never completed.
	}
	_, missing := detectPartialRegionMaterialisation(declared, perRegion)
	if len(missing) != 1 || missing[0] != "me-east-215-b" {
		t.Errorf("missing = %v, want [me-east-215-b] (empty IP must count as missing)", missing)
	}
}

// Test 5 — the typed error formats with the declared/materialised/missing
// breakdown so the operator-console drill-down has the data it needs.
func TestPartialRegionMaterialisationError_FormatsBreakdown(t *testing.T) {
	err := &PartialRegionMaterialisationError{
		DeclaredRegions:     []string{"me-east-215-a", "me-east-215-b"},
		MaterializedRegions: []string{"me-east-215-a"},
		MissingRegions:      []string{"me-east-215-b"},
		Result:              &Result{SovereignFQDN: "hw86.omani.works"},
	}
	msg := err.Error()
	for _, want := range []string{
		"me-east-215-a",
		"me-east-215-b",
		"partial-region",
		"Pillar 2",
		"Pillar 3",
		"Common.0021",
		"#2840",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing substring %q", msg, want)
		}
	}
}

// Test 6 — errors.As round-trips through a wrapped error so the
// handler-side errors.As(&partialErr) path in runProvisioning works
// even if the provisioner ever wraps the typed error in an additional
// fmt.Errorf("...: %w").
func TestPartialRegionMaterialisationError_ErrorsAs(t *testing.T) {
	original := &PartialRegionMaterialisationError{
		DeclaredRegions:     []string{"me-east-215-a", "me-east-215-b"},
		MaterializedRegions: []string{"me-east-215-a"},
		MissingRegions:      []string{"me-east-215-b"},
		Result:              &Result{SovereignFQDN: "hw86.omani.works"},
	}
	wrapped := &wrappedErr{inner: original}
	var got *PartialRegionMaterialisationError
	if !errors.As(wrapped, &got) {
		t.Fatalf("errors.As(wrapped, &PartialRegionMaterialisationError{}) = false, want true")
	}
	if got != original {
		t.Errorf("errors.As unwrapped to %v, want pointer-equal original", got)
	}
}

// wrappedErr exists only to verify errors.As traverses Unwrap().
type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }

// Test 7 — the simulated end-to-end shape the catalyst-api will see:
// build a partial Result + a PartialRegionMaterialisationError, then
// assert that downstream consumers can read MaterializedRegions off
// the Result (operator-console drill-down contract).
func TestResult_MaterializedRegions_Exposed(t *testing.T) {
	result := &Result{
		SovereignFQDN:       "hw86.omani.works",
		ControlPlaneIP:      "100.100.100.10",
		LoadBalancerIP:      "100.100.100.99",
		MaterializedRegions: []string{"me-east-215-a"},
	}
	if len(result.MaterializedRegions) != 1 || result.MaterializedRegions[0] != "me-east-215-a" {
		t.Errorf("Result.MaterializedRegions = %v, want [me-east-215-a]", result.MaterializedRegions)
	}
	// Operator-console diff: declared - materialised = missing.
	declared := map[string]struct{}{"me-east-215-a": {}, "me-east-215-b": {}}
	for _, m := range result.MaterializedRegions {
		delete(declared, m)
	}
	if len(declared) != 1 {
		t.Fatalf("declared - materialised has %d entries, want 1", len(declared))
	}
	if _, ok := declared["me-east-215-b"]; !ok {
		t.Errorf("missing set = %v, want [me-east-215-b]", declared)
	}
}

// Test 7 (#2840-followup, 2026-06-03) — Hetzner key format pass-through
// via normalisePerRegionKeys. Hetzner emits the per-region map keyed by
// `<cloudRegion>-<index>` for secondaries + literal `primary` for the
// primary CP; detectPartialRegionMaterialisation expects cloudRegion
// keys. Verify the normaliser collapses both shapes correctly so a
// healthy 3-region Hetzner prov is NOT mis-classified as fully-missing.
func TestNormalisePerRegionKeys_HetznerThreeRegionHealthy(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "hetzner", CloudRegion: "nbg1"},
		{Provider: "hetzner", CloudRegion: "fsn1"},
		{Provider: "hetzner", CloudRegion: "hel1"},
	}
	hetznerShape := map[string]string{
		"primary": "100.100.100.10",
		"fsn1-1":  "100.100.100.20",
		"hel1-2":  "100.100.100.30",
	}
	normalised := normalisePerRegionKeys(declared, hetznerShape)
	want := map[string]string{
		"nbg1": "100.100.100.10",
		"fsn1": "100.100.100.20",
		"hel1": "100.100.100.30",
	}
	for k, v := range want {
		if got := normalised[k]; got != v {
			t.Errorf("normalised[%q] = %q, want %q", k, got, v)
		}
	}
	// And the downstream detection must see ZERO missing.
	materialised, missing := detectPartialRegionMaterialisation(declared, normalised)
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty (every declared region came up)", missing)
	}
	if len(materialised) != 3 {
		t.Errorf("materialised = %v, want all 3 regions", materialised)
	}
}

// Test 8 — Hetzner partial-region shape: primary + one secondary
// materialised, second secondary missing. detection must name the
// missing cloudRegion correctly even though the input keys carried
// the suffixed Hetzner format.
func TestNormalisePerRegionKeys_HetznerPartialThirdRegionMissing(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "hetzner", CloudRegion: "nbg1"},
		{Provider: "hetzner", CloudRegion: "fsn1"},
		{Provider: "hetzner", CloudRegion: "hel1"},
	}
	hetznerShape := map[string]string{
		"primary": "100.100.100.10",
		"fsn1-1":  "100.100.100.20",
		// hel1-2 deliberately missing (capacity cascade).
	}
	normalised := normalisePerRegionKeys(declared, hetznerShape)
	_, missing := detectPartialRegionMaterialisation(declared, normalised)
	if len(missing) != 1 || missing[0] != "hel1" {
		t.Errorf("missing = %v, want [hel1]", missing)
	}
}

// Test 9 — Same-region duplicates (legal Hetzner stacking): fsn1-mgmt
// + fsn1-dataplane both materialised. Collapses to one fsn1 entry;
// detection must treat declared duplicate as satisfied by the single
// normalised entry (first-non-empty wins).
func TestNormalisePerRegionKeys_HetznerSameRegionDuplicates(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1"},
		{Provider: "hetzner", CloudRegion: "fsn1"},
	}
	hetznerShape := map[string]string{
		"primary": "100.100.100.10",
		"fsn1-1":  "100.100.100.20",
	}
	normalised := normalisePerRegionKeys(declared, hetznerShape)
	if normalised["fsn1"] == "" {
		t.Errorf("normalised[fsn1] should be non-empty, got %v", normalised)
	}
	_, missing := detectPartialRegionMaterialisation(declared, normalised)
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty (stacked duplicate counts as satisfied)", missing)
	}
}

// Test 10 — Huawei pass-through: keys already in cloudRegion form; the
// normaliser must not mangle them (no '-' suffix stripping, no
// 'primary' remap when the key isn't literal 'primary').
func TestNormalisePerRegionKeys_HuaweiPassThrough(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215-a"},
		{Provider: "huawei", CloudRegion: "me-east-215-b"},
	}
	huaweiShape := map[string]string{
		"me-east-215-a": "100.100.100.10",
		"me-east-215-b": "100.100.100.20",
	}
	normalised := normalisePerRegionKeys(declared, huaweiShape)
	for code, ip := range huaweiShape {
		if got := normalised[code]; got != ip {
			t.Errorf("normalised[%q] = %q, want %q (Huawei keys must pass through)", code, got, ip)
		}
	}
}

// Test 11 (#5381) — Huawei DIGIT-TAILED codes must pass through.
//
// The pre-#5381 normaliser stripped any trailing "-<digits>" segment for
// every provider (a Hetzner "<cloudRegion>-<index>" rule). Huawei region
// codes end in digits themselves, so "me-east-215" became "me-east" and
// "me-east-215-b-1" became "me-east-215-b" → detectPartialRegionMaterialisation
// found NEITHER declared code → a fully-materialised 2-region Sovereign was
// stamped partial-failure and never reached commitPDM/phase-1 (hw289 live,
// dep 44ac86240f0afab7). Test 10 missed it because its codes end in letters.
func TestNormalisePerRegionKeys_HuaweiDigitTailedCodes_5381(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "huawei", CloudRegion: "me-east-215"},
		{Provider: "huawei", CloudRegion: "me-east-215-b-1"},
	}
	huaweiShape := map[string]string{
		"me-east-215":     "100.100.100.10",
		"me-east-215-b-1": "100.100.100.20",
	}
	normalised := normalisePerRegionKeys(declared, huaweiShape)
	for code, ip := range huaweiShape {
		if got := normalised[code]; got != ip {
			t.Errorf("normalised[%q] = %q, want %q (digit-tailed Huawei codes must NOT be truncated)", code, got, ip)
		}
	}
	if _, truncated := normalised["me-east"]; truncated {
		t.Errorf("normalised contains truncated key %q — the digit-suffix strip fired on a declared code", "me-east")
	}
	materialised, missing := detectPartialRegionMaterialisation(declared, normalised)
	if len(missing) != 0 {
		t.Errorf("missing = %v, want empty (both regions materialised)", missing)
	}
	if len(materialised) != 2 {
		t.Errorf("materialised = %v, want both declared codes", materialised)
	}
}

// Test 12 (#5381) — the Hetzner "<cloudRegion>-<index>" strip still fires
// when the raw key is NOT itself a declared region (the behaviour Test 8/9
// rely on), proving the declared-set-aware guard is a strict superset.
func TestNormalisePerRegionKeys_HetznerIndexStripStillFires_5381(t *testing.T) {
	declared := []RegionSpec{
		{Provider: "hetzner", CloudRegion: "fsn1"},
		{Provider: "hetzner", CloudRegion: "hel1"},
	}
	hetznerShape := map[string]string{
		"primary": "10.0.0.1",
		"hel1-1":  "10.0.0.2",
	}
	normalised := normalisePerRegionKeys(declared, hetznerShape)
	if got := normalised["fsn1"]; got != "10.0.0.1" {
		t.Errorf("normalised[fsn1] = %q, want 10.0.0.1 (literal 'primary' remap)", got)
	}
	if got := normalised["hel1"]; got != "10.0.0.2" {
		t.Errorf("normalised[hel1] = %q, want 10.0.0.2 (index strip must still fire for undeclared raw keys)", got)
	}
	if _, missing := detectPartialRegionMaterialisation(declared, normalised); len(missing) != 0 {
		t.Errorf("missing = %v, want empty", missing)
	}
}
