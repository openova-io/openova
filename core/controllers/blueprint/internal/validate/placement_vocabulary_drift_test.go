// placement_vocabulary_drift_test.go — #3375 DoD-1 cross-surface drift
// guard.
//
// ONE canonical placement/topology vocabulary is shared by every
// surface. The single Go source of truth is
// core/controllers/internal/placement.CanonicalModes():
//
//	singleton | active-active | active-hot-standby | active-passive
//
// This test pins the OTHER surfaces to that exact set by scanning their
// source from the repo root, so a future edit that re-introduces the
// legacy editor dialect (single-region / active-hotstandby) as a primary
// emitted value — or drops a canonical class — fails CI here:
//
//	(a) the catalog placementSchema enum + this package's accepted set
//	    (the CRD products/catalyst/chart/crds/blueprint.yaml + the Go
//	    canonicalPlacementModes map)
//	(b) the console editor type (products/catalyst/console/src/lib/api/
//	    types.ts BcpTopology union)
//	(c) the bootstrap-ui editor option set
//	    (products/catalyst/bootstrap/ui/.../topology/modes.ts ALL_MODES)
//
// The catalyst-api surface + the FE canonicaliser parity are pinned by
// their own drift tests (products/catalyst/bootstrap/api/.../
// placement_vocabulary_drift_test.go and
// products/catalyst/bootstrap/ui/.../placement-vocabulary.drift.test.tsx).
package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/internal/placement"
)

func TestPlacementVocabulary_CanonicalModesAreFour(t *testing.T) {
	want := []string{"singleton", "active-active", "active-hot-standby", "active-passive"}
	got := placement.CanonicalModes()
	if len(got) != len(want) {
		t.Fatalf("placement.CanonicalModes() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("CanonicalModes()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The validate package's accepted placementSchema.modes set MUST include
// every canonical Application-tier token (it also accepts the legacy
// aliases + the bootstrap-topology tier).
func TestPlacementVocabulary_ValidateAcceptsCanonical(t *testing.T) {
	for _, m := range placement.CanonicalModes() {
		if _, ok := canonicalPlacementModes[m]; !ok {
			t.Errorf("canonicalPlacementModes is missing canonical token %q", m)
		}
	}
	// Legacy spellings remain accepted (back-compat).
	for _, legacy := range []string{"single-region", "active-hotstandby"} {
		if _, ok := canonicalPlacementModes[legacy]; !ok {
			t.Errorf("canonicalPlacementModes dropped legacy alias %q (must stay accepted)", legacy)
		}
	}
}

func TestPlacementVocabulary_CRDEnumHasCanonical(t *testing.T) {
	root := findRepoRoot(t)
	crd := mustReadRepoFile(t, root, "products", "catalyst", "chart", "crds", "blueprint.yaml")
	// The CRD placementSchema enum (and default/defaultOnMultiRegion) must
	// list every canonical token so a Blueprint can declare it structurally.
	for _, m := range placement.CanonicalModes() {
		if !containsYAMLEnumValue(crd, m) {
			t.Errorf("CRD blueprint.yaml placementSchema enum is missing canonical token %q", m)
		}
	}
}

func TestPlacementVocabulary_ConsoleBcpTopologyIsCanonical(t *testing.T) {
	root := findRepoRoot(t)
	types := mustReadRepoFile(t, root, "products", "catalyst", "console", "src", "lib", "api", "types.ts")
	// The console BcpTopology union must carry exactly the canonical set
	// (no legacy spelling).
	for _, m := range placement.CanonicalModes() {
		if !strings.Contains(types, "'"+m+"'") {
			t.Errorf("console types.ts BcpTopology is missing canonical token %q", m)
		}
	}
	for _, legacy := range []string{"'single-region'", "'active-hotstandby'"} {
		if strings.Contains(types, legacy) {
			t.Errorf("console types.ts BcpTopology must NOT carry the legacy spelling %s", legacy)
		}
	}
}

func TestPlacementVocabulary_BootstrapUIAllModesIsCanonical(t *testing.T) {
	root := findRepoRoot(t)
	// #5609 — ALL_MODES moved out of the former TopologyEditor.tsx (whose
	// React component was never mounted in production and was deleted) into
	// the vocabulary-only module topology/modes.ts.
	editor := mustReadRepoFile(t, root, "products", "catalyst", "bootstrap", "ui", "src", "widgets", "topology", "modes.ts")
	// ALL_MODES is the bootstrap-ui picker's option set — it must be the
	// canonical four, with no legacy spelling as a member of the array.
	allModesLine := extractLineContaining(editor, "export const ALL_MODES")
	if allModesLine == "" {
		t.Fatalf("could not find ALL_MODES declaration in topology/modes.ts")
	}
	for _, m := range placement.CanonicalModes() {
		if !strings.Contains(allModesLine, "'"+m+"'") {
			t.Errorf("ALL_MODES is missing canonical token %q (line: %s)", m, allModesLine)
		}
	}
	for _, legacy := range []string{"'single-region'", "'active-hotstandby'"} {
		if strings.Contains(allModesLine, legacy) {
			t.Errorf("ALL_MODES must NOT carry the legacy spelling %s", legacy)
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────

func mustReadRepoFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	p := filepath.Join(append([]string{root}, parts...)...)
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("could not read %s (%v); skipping cross-surface drift check", p, err)
	}
	return string(raw)
}

// containsString reports whether sub appears in s (a tiny strings.Contains
// alias kept local so the file has no extra import churn).
func containsStringHelper(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

// containsYAMLEnumValue checks for a YAML list item `- <value>` (the enum
// shape used in the CRD).
func containsYAMLEnumValue(s, value string) bool {
	return strings.Contains(s, "- "+value+"\n") || strings.Contains(s, "- "+value+"\r\n")
}

// extractLineContaining returns the first line of s containing marker, or
// "" if none.
func extractLineContaining(s, marker string) string {
	start := indexOf(s, marker)
	if start < 0 {
		return ""
	}
	// back up to line start
	ls := start
	for ls > 0 && s[ls-1] != '\n' {
		ls--
	}
	le := start
	for le < len(s) && s[le] != '\n' {
		le++
	}
	return s[ls:le]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
