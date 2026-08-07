package placement

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A Blueprint whose chart ships `crossRegion` support must declare a
// multi-region placement mode in its catalog-seed `placementSchema.modes`.
//
// WHY THIS EXISTS. bp-guacamole 0.2.36 gained the ClusterMesh primary/secondary
// idiom — the same one bp-harbor 1.2.45 used for its identical session split
// (#5406) — so a two-region Sovereign anchors the singleton webapp in the
// primary and reaches it over the mesh. The seed's topology block was updated
// to match (`defaults.multi-region: active-hot-standby`). Its
// `placementSchema.modes` was not, and stayed `[single-region]`.
//
// That combination is self-contradictory rather than merely untidy, and the
// contradiction is invisible unless you know how the list is canonicalised.
// Measured with AllowedByBlueprint, not assumed:
//
//	AllowedByBlueprint("singleton",          ["single-region"]) == true
//	AllowedByBlueprint("active-hot-standby", ["single-region"]) == false
//
// `single-region` is the legacy spelling of `singleton` ONLY, so the Blueprint
// was rejecting the very multi-region default it declared one field below.
// Every one of guacamole's four ClusterMesh-singleton peers — harbor, keycloak,
// gitea, openbao — lists its multi-region mode here. Guacamole alone did not.
//
// The check is DERIVED on both sides. The crossRegion-capable set is read from
// the chart tree (`platform/*/chart/values.yaml` declaring a `crossRegion:`
// key), and the declared modes are read from the seed. A future chart that
// gains crossRegion is therefore covered the day it lands, with no list here to
// forget to update.
//
// This is a source-consistency check on two files in this repo, so it is
// deliberately not a live-cluster assertion: it must fail in CI on the PR that
// introduces the drift, not on a Sovereign months later.

var multiRegionModes = map[string]bool{
	ModeActiveActive:     true,
	ModeActiveHotStandby: true,
	ModeActivePassive:    true,
}

// repoRoot walks up from this package to the git root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Skip("repo root not found — running outside a checkout")
	return ""
}

// crossRegionCharts returns the bp-<name> set whose chart values declare a
// top-level `crossRegion:` key.
func crossRegionCharts(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	matches, err := filepath.Glob(filepath.Join(root, "platform", "*", "chart", "values.yaml"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		hasKey := false
		for _, line := range strings.Split(string(b), "\n") {
			// Top-level key only — no leading whitespace, not a comment.
			if strings.HasPrefix(line, "crossRegion:") {
				hasKey = true
				break
			}
		}
		if hasKey {
			out["bp-"+filepath.Base(filepath.Dir(filepath.Dir(m)))] = true
		}
	}
	return out
}

var (
	seedNameRe  = regexp.MustCompile(`(?m)^  name: (bp-[a-z0-9-]+)$`)
	seedModesRe = regexp.MustCompile(`placementSchema:\s*\n(?:\s*#[^\n]*\n)*\s*modes:\s*\[([^\]]*)\]`)
)

// seedModes maps each seeded Blueprint to its declared placementSchema.modes.
func seedModes(t *testing.T, root string) map[string][]string {
	t.Helper()
	path := filepath.Join(root, "products", "catalyst", "chart", "templates", "catalog-seed", "blueprints.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("catalog seed not readable (%v)", err)
	}
	text := string(b)
	idx := seedNameRe.FindAllStringSubmatchIndex(text, -1)
	out := map[string][]string{}
	for i, loc := range idx {
		name := text[loc[2]:loc[3]]
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		if m := seedModesRe.FindStringSubmatch(text[loc[0]:end]); m != nil {
			var modes []string
			for _, p := range strings.Split(m[1], ",") {
				if p = strings.TrimSpace(p); p != "" {
					modes = append(modes, p)
				}
			}
			out[name] = modes
		}
	}
	return out
}

func TestCrossRegionChartsDeclareAMultiRegionPlacementMode(t *testing.T) {
	root := repoRoot(t)
	charts := crossRegionCharts(t, root)
	modes := seedModes(t, root)

	// Vacuity: both extractors must have found something. A regex that matched
	// nothing would make every assertion below pass on an empty set.
	if len(charts) == 0 {
		t.Fatal("no crossRegion-capable charts discovered — the chart-tree scan is broken, " +
			"so this guard would pass on anything")
	}
	if len(modes) < 20 {
		t.Fatalf("only %d seeded Blueprints parsed — the seed scan is broken", len(modes))
	}

	checked := 0
	for chart := range charts {
		declared, ok := modes[chart]
		if !ok {
			// Not every crossRegion chart is catalog-seeded; nothing to check.
			continue
		}
		if len(declared) == 0 {
			// Unconstrained: AllowedByBlueprint admits every mode.
			continue
		}
		checked++
		found := ""
		for _, m := range declared {
			if multiRegionModes[Canonicalize(m)] {
				found = m
				break
			}
		}
		if found == "" {
			t.Errorf("%s ships crossRegion support but its catalog-seed placementSchema.modes=%v "+
				"admits no multi-region mode. AllowedByBlueprint canonicalises `single-region` to "+
				"`singleton` only, so the Blueprint rejects the multi-region topology it declares "+
				"one field below — the #5358 defect. Add the matching mode, as bp-harbor/keycloak/"+
				"gitea/openbao already do.", chart, declared)
		}
	}
	if checked == 0 {
		t.Fatal("zero crossRegion charts were actually checked against the seed — " +
			"the join between the two scans is broken and this guard proves nothing")
	}
	t.Logf("checked %d crossRegion-capable seeded Blueprint(s)", checked)
}

// TestAllowedByBlueprint_SingleRegionRejectsMultiRegion pins the canonicalisation
// fact the guard above depends on. If `single-region` ever starts admitting a
// multi-region mode, the guard becomes vacuous and this test says so first.
func TestAllowedByBlueprint_SingleRegionRejectsMultiRegion(t *testing.T) {
	if !AllowedByBlueprint(ModeSingleton, []string{"single-region"}) {
		t.Fatal("`single-region` must remain the legacy spelling of `singleton`")
	}
	for _, m := range []string{ModeActiveHotStandby, ModeActivePassive, ModeActiveActive} {
		if AllowedByBlueprint(m, []string{"single-region"}) {
			t.Fatalf("`single-region` must NOT admit %q — if it does, the crossRegion seed "+
				"guard is asserting nothing", m)
		}
	}
}
