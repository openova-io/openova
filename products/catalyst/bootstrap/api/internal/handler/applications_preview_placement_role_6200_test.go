// applications_preview_placement_role_6200_test.go — #6200.
//
// WHAT BROKE
// ----------
// `previewRoleForPlacement` and the two `standby :=` sites in
// applications_preview.go compared the RAW placement mode against the LEGACY
// spelling "active-hotstandby" only. The CANONICAL spelling is
// "active-hot-standby" — it is what validPlacementMode advertises in its own
// error text, what the console sends, and what 112 of the repo's 141 mode
// literals use. Sent canonically, the switch fell through to `return
// "primary"` and the standby flag stayed false, so an active-hot-standby
// preview rendered EVERY region as primary.
//
// The failure mode is the dangerous kind: HTTP 200, no error, no warning, and
// two `catalyst.openova.io/placement-role: primary` manifests where one should
// have been the hot standby. Measured live on hw295 walking UAT row 60 — the
// preview for {mode: active-hot-standby, regions:[me-east-215-a,
// me-east-215-b]} returned role=primary for BOTH regions.
//
// WHY A TEST AND NOT JUST THE FIX
// -------------------------------
// The alias mapping already existed (canonicalizeTopology, same package, and
// core placement.Canonicalize behind the drift test). Nothing failed when the
// call sites bypassed it, because every existing test that exercised a standby
// happened to pass the legacy spelling. This test pins the CANONICAL spelling
// specifically, so re-introducing a raw comparison fails here.
package handler

import "testing"

// The canonical spelling MUST yield primary/standby. This is the case that was
// broken; asserting only the legacy spelling would pass on the buggy code.
func TestPreviewRoleForPlacement_CanonicalHotStandby_6200(t *testing.T) {
	if got := previewRoleForPlacement("active-hot-standby", 0); got != "primary" {
		t.Fatalf("canonical active-hot-standby idx0: got %q, want primary", got)
	}
	if got := previewRoleForPlacement("active-hot-standby", 1); got != "standby" {
		t.Fatalf("canonical active-hot-standby idx1: got %q, want standby — "+
			"this is #6200: the raw comparison matched only the legacy "+
			"spelling, so every region rendered primary", got)
	}
}

// The legacy spelling must keep working — the fix canonicalises rather than
// swapping one hard-coded literal for another.
func TestPreviewRoleForPlacement_LegacyHotStandby_StillWorks_6200(t *testing.T) {
	if got := previewRoleForPlacement("active-hotstandby", 1); got != "standby" {
		t.Fatalf("legacy active-hotstandby idx1: got %q, want standby", got)
	}
	if got := previewRoleForPlacement("ACTIVE_HOT_STANDBY", 1); got != "standby" {
		t.Fatalf("underscore/upper alias idx1: got %q, want standby", got)
	}
}

// CONTROL — the exclusion must not leak. Modes that are NOT hot-standby keep
// their own roles, or an over-broad canonicalisation would pass the two tests
// above while silently mislabelling everything else.
func TestPreviewRoleForPlacement_OtherModesUnaffected_6200(t *testing.T) {
	for _, tc := range []struct{ mode, want0, want1 string }{
		{"active-active", "active", "active"},
		{"singleton", "primary", "primary"},
		{"single-region", "primary", "primary"},
		{"active-passive", "primary", "primary"},
		{"", "primary", "primary"},
		{"nonsense-mode", "primary", "primary"},
	} {
		if got := previewRoleForPlacement(tc.mode, 0); got != tc.want0 {
			t.Errorf("mode %q idx0: got %q, want %q", tc.mode, got, tc.want0)
		}
		if got := previewRoleForPlacement(tc.mode, 1); got != tc.want1 {
			t.Errorf("mode %q idx1: got %q, want %q", tc.mode, got, tc.want1)
		}
	}
}
