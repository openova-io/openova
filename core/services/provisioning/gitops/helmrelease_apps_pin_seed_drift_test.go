// helmrelease_apps_pin_seed_drift_test.go — UAT row 234 (Refs #4307).
//
// The gap this closes: the funnel's built-in chart pins
// (gitops.DefaultHRAppChartVersions, consumed by services/provisioning/main.go)
// and the catalog-seed delivery pins
// (products/catalyst/chart/templates/catalog-seed/blueprints.yaml `source.version`)
// are two independent numbers describing the same install, and only the first
// one decides what a funnel Org actually gets. Nothing compared them, so they
// drifted: stalwart-mail sat at 0.1.13 against a seed of 0.1.15, straddling
// 0.1.14 — the §854 nodePort fix, without which Kyverno DENIES the HelmRelease
// at admission and `mail.<slug>.<pool-tld>` has no pod and no HTTPRoute to
// serve. openclaw was five behind. newapi carried no pin at all and rendered
// the floating `version: "*"` that #4706 exists to forbid.
//
// Both halves are read from their REAL sources — the exported constant the
// binary ships, and the seed file parsed by the repo's own canonical parser
// (scripts/lib/parse-catalog-seed-pins.awk, the same one
// tests/e2e/bootstrap-kit and sync-catalog-seed-pin.py mirror). Neither number
// is retyped here, so the guard cannot pass against a value the platform does
// not use.
package gitops

import (
	"bufio"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// repoRoot resolves the monorepo root from this package's directory
// (core/services/provisioning/gitops → ../../../..).
func repoRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return abs
}

// catalogSeedPins returns chart-name → source.version for every seeded
// Blueprint, by shelling out to the repo's canonical seed parser rather than
// re-implementing its line-scan here. Re-typing that predicate is exactly how
// two "equivalent" parsers come to disagree, and the awk one is already the
// shared source for the bootstrap-kit e2e gate and the bump writer.
func catalogSeedPins(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	awkScript := filepath.Join(root, "scripts", "lib", "parse-catalog-seed-pins.awk")
	seed := filepath.Join(root, "products", "catalyst", "chart", "templates",
		"catalog-seed", "blueprints.yaml")

	out, err := exec.Command("awk", "-f", awkScript, seed).Output()
	if err != nil {
		t.Fatalf("parse catalog seed via %s: %v", awkScript, err)
	}

	pins := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		// name|visibility|source.chart|source.version
		f := strings.Split(sc.Text(), "|")
		if len(f) != 4 {
			continue
		}
		chart, version := strings.TrimSpace(f[2]), strings.TrimSpace(f[3])
		if chart == "" || version == "" {
			continue
		}
		pins[chart] = version
	}
	if len(pins) == 0 {
		t.Fatalf("catalog seed parsed to ZERO pins — the parser or the seed path is wrong, "+
			"and a guard reading nothing would pass on everything (awk=%s seed=%s)", awkScript, seed)
	}
	return pins
}

// chartRE pulls the bp-* chart name out of a rendered HelmRelease so the
// slug→chart mapping comes from the GENERATOR's own output, never from a table
// retyped in this file. `stalwart-mail` renders `bp-stalwart-tenant`; a guard
// that hardcoded that pairing would keep passing if the generator changed it.
var chartRE = regexp.MustCompile(`(?m)^\s+chart:\s*"?(bp-[a-z0-9-]+)"?\s*$`)

// TestDefaultHRAppPins_MatchCatalogSeed — the funnel's default chart pin for
// every HelmRelease-shaped app equals the catalog-seed delivery pin for the
// chart that app's generator actually emits.
func TestDefaultHRAppPins_MatchCatalogSeed(t *testing.T) {
	seedPins := catalogSeedPins(t)
	funnelPins := ParseHRAppVersions(DefaultHRAppChartVersions)

	if len(helmReleaseAppSlugs) == 0 {
		t.Fatal("helmReleaseAppSlugs is empty — this guard would assert nothing")
	}

	for slug := range helmReleaseAppSlugs {
		slug := slug
		t.Run(slug, func(t *testing.T) {
			// (1) Every HR-shaped app MUST carry a pin. An absent one leaves the
			// template's floating `version: "*"`, which resolves to the highest
			// semver tag on the OCI repo — the exact failure #4706 was written
			// to stop, and how newapi shipped unpinned.
			funnelVersion, pinned := funnelPins[slug]
			if !pinned {
				t.Fatalf("slug %q is a HelmRelease-shaped funnel app but carries no pin in "+
					"DefaultHRAppChartVersions — it will render the floating version \"*\"", slug)
			}

			// (2) Ask the REAL generator which chart this slug installs, then
			// compare against that chart's seed pin.
			rendered := generateHelmReleaseApp(slug, helmReleaseAppOpts{
				slug:         "acme",
				parentDomain: "omani.trade",
				chartVersion: funnelVersion,
			})
			if strings.TrimSpace(rendered) == "" {
				t.Fatalf("generateHelmReleaseApp(%q) rendered nothing — the slug registry and "+
					"the generator switch disagree", slug)
			}
			m := chartRE.FindStringSubmatch(rendered)
			if m == nil {
				t.Fatalf("no bp-* chart name in the HelmRelease %q renders; "+
					"cannot resolve its catalog-seed pin", slug)
			}
			chart := m[1]

			seedVersion, seeded := seedPins[chart]
			if !seeded {
				t.Fatalf("chart %q (funnel slug %q) has no catalog-seed entry — the funnel "+
					"installs a chart the Sovereign's own catalog does not deliver", chart, slug)
			}
			if funnelVersion != seedVersion {
				t.Errorf("PIN DRIFT for %q: the funnel installs %s@%s but the catalog seed "+
					"delivers %s@%s. A funnel Org gets the older chart, and every fix "+
					"published between those two versions is absent from the purchase path "+
					"while the seed says it shipped.",
					slug, chart, funnelVersion, chart, seedVersion)
			}
		})
	}
}

// TestDefaultHRAppPins_GuardIsNotVacuous — the VACUITY control.
//
// The test above compares two values read from real sources; if either read
// silently yielded nothing it would pass on everything. This pins the guard's
// own machinery: the seed parse must produce the charts we look up, and a
// deliberately WRONG pin must be detected by the same comparison the guard
// makes. Without this, a broken awk path or a renamed constant turns the drift
// guard into decoration that reports green forever.
func TestDefaultHRAppPins_GuardIsNotVacuous(t *testing.T) {
	seedPins := catalogSeedPins(t)

	// The seed parse really resolves the charts this guard is about.
	for _, chart := range []string{"bp-stalwart-tenant", "bp-openclaw", "bp-newapi"} {
		if seedPins[chart] == "" {
			t.Fatalf("seed parse produced no version for %q — the drift guard above would "+
				"fail-open on a chart it is supposed to police", chart)
		}
	}

	// A mutant pin set that still parses must be REJECTED by the same equality
	// the guard applies. This is the "watched it fail" half: it reproduces the
	// exact 0.1.13-vs-0.1.15 shape that shipped.
	mutant := ParseHRAppVersions("stalwart-mail=0.1.13")
	if mutant["stalwart-mail"] == seedPins["bp-stalwart-tenant"] {
		t.Fatalf("the historical stale pin 0.1.13 equals the current seed pin %q — this "+
			"control no longer discriminates and must be re-pointed at a version the "+
			"seed does not carry", seedPins["bp-stalwart-tenant"])
	}
}
