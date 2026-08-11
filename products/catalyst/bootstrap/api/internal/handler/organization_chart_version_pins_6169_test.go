package handler

// organization_chart_version_pins_6169_test.go — Refs #6169.
//
// THE DEFECT. Every per-Organization application HelmRelease this overlay
// writes shipped `version: "*"`.
//
// The seven chart pins are read from env by cmd/api/main.go:996-1002 into
// OrganizationChartVersions. A repo-wide grep for CATALYST_ORG_BP_*_VER across
// every .yaml/.yml/.tpl/.json matched ZERO times — no setter existed anywhere
// in the repo — so withVersionDefaults (organization_gitops.go:919) resolved
// six of the seven to the floating star, and Flux installed whatever the OCI
// repo happened to serve newest at reconcile time. Same read-by-Go /
// set-by-nothing shape as the console pin (#6139, 3.5 months) and
// CATALYST_JANITOR_DESTRUCTIVE (UAT row 228).
//
// It is not a theoretical hazard. bp-agenity alone falls back to a BOUNDED
// range instead of a star (agenityChartConstraint, organization_gitops.go:917)
// because an unbounded star once resolved a stray 0.9.7 IMAGE manifest over
// the real 0.5.x chart and bp-agenity never installed at all (#4922). Only
// agenity was hardened, and only after it had already fired. The remaining six
// carried the identical exposure.
//
// WHAT THIS GUARD PINS. Narrowly: a rendered per-Organization HelmRelease must
// carry a CONCRETE chart version. It deliberately does NOT assert the general
// "every env var Go reads must have a YAML setter" — 47 of the 108 CATALYST_*
// vars are legitimately-optional override knobs with deliberate Go-side
// defaults (recorded on #6169 so nobody writes that guard and then reverts it).
// The subject here is a VERSION CONSTRAINT that silently degrades to a
// wildcard, not an unset variable.
//
// CONTROL (TestChartVersionScanner_Table_6169). The scanner is exercised
// against a HelmRelease that legitimately carries `version: "0.2.17"` — it
// must return NO finding. A checker that flagged every HelmRelease would pass
// the main assertion below for the wrong reason.
//
// VACUITY (TestOrgTenantHelmReleases_ScannerFailsOnThePreFixShape_6169). The
// same scanner is pointed at the pre-fix render — the overlay built from an
// EMPTY OrganizationChartVersions, which is exactly what the shipped chart
// produced before this change — and is required to report findings. Without
// that arm, a scanner that silently matched nothing would report green over
// the very defect it exists to catch.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// concreteVersionRE accepts an exact SemVer pin and nothing else. A range
// (">=0.5.0 <0.6.0"), a caret/tilde constraint, an `x` wildcard and the bare
// star are all rejected: every one of them lets the resolved artifact change
// under a Sovereign that was never redeployed, which is the property that made
// UAT rows 232/234 cite chart versions their deployment never pinned.
var concreteVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// chartVersionFinding is one rendered HelmRelease whose chart version is not a
// concrete pin.
type chartVersionFinding struct {
	File    string
	Release string
	Chart   string
	Version string
}

func (f chartVersionFinding) String() string {
	return fmt.Sprintf("%s: HelmRelease %q chart %q version %q", f.File, f.Release, f.Chart, f.Version)
}

// scanHelmReleaseChartVersions decodes every YAML document in the rendered
// overlay and returns the HelmReleases whose spec.chart.spec.version is not a
// concrete SemVer, plus the number of HelmReleases actually inspected.
//
// The inspected count is returned so callers can prove the scan was not blind:
// a parse that yielded zero HelmReleases would otherwise report "no wildcards"
// with total confidence.
func scanHelmReleaseChartVersions(files map[string]string) (findings []chartVersionFinding, inspected int, err error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		body := files[name]
		dec := yaml.NewDecoder(strings.NewReader(body))
		for {
			var doc map[string]any
			if derr := dec.Decode(&doc); derr != nil {
				break
			}
			if doc == nil {
				continue
			}
			if k, _ := doc["kind"].(string); k != "HelmRelease" {
				continue
			}
			meta, _ := doc["metadata"].(map[string]any)
			release, _ := meta["name"].(string)

			spec, _ := doc["spec"].(map[string]any)
			chartOuter, _ := spec["chart"].(map[string]any)
			chartSpec, _ := chartOuter["spec"].(map[string]any)
			if chartSpec == nil {
				// A HelmRelease with no chart spec at all (chartRef-shaped, or
				// malformed) is a different defect; report it rather than
				// letting it pass as "no wildcard found".
				return nil, inspected, fmt.Errorf("%s: HelmRelease %q has no spec.chart.spec — cannot assert a version pin on it", name, release)
			}
			chartName, _ := chartSpec["chart"].(string)
			version := strings.TrimSpace(fmt.Sprintf("%v", chartSpec["version"]))
			inspected++
			if !concreteVersionRE.MatchString(version) {
				findings = append(findings, chartVersionFinding{
					File: name, Release: release, Chart: chartName, Version: version,
				})
			}
		}
	}
	return findings, inspected, nil
}

// chartValuesOrgPins reads the SHIPPED per-Organization chart pins out of
// products/catalyst/chart/values.yaml. Reading the real chart rather than
// restating the numbers here is the point: a test carrying its own copy of the
// versions would stay green against a values.yaml that lost the block entirely.
func chartValuesOrgPins(t *testing.T) OrganizationChartVersions {
	t.Helper()
	path := filepath.Join(repoRootFromTest(t), "products", "catalyst", "chart", "values.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Navigated as a yaml.Node rather than unmarshalled into a struct on
	// purpose: values.yaml carries two top-level `migrations:` keys (:2894 and
	// :2908, a documented deliberate duplicate whose LAST occurrence wins under
	// Helm), and yaml.v3's map decoder rejects the whole document for it. A
	// node walk reads the block this test is about without taking a position on
	// an unrelated key.
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	cvNode := mappingChild(mappingChild(docRoot(&root), "orgTenants"), "chartVersions")
	if cvNode == nil {
		t.Fatalf("%s carries no orgTenants.chartVersions block — the per-Organization chart pins have no writer, so every per-Organization HelmRelease resolves `version: \"*\"` at reconcile time (#6169)", path)
	}
	get := func(key string) string {
		n := mappingChild(cvNode, key)
		if n == nil {
			return ""
		}
		return strings.TrimSpace(n.Value)
	}
	return OrganizationChartVersions{
		Keycloak:  get("keycloak"),
		CNPG:      get("cnpg"),
		WordPress: get("wordpress"),
		OpenClaw:  get("openclaw"),
		Stalwart:  get("stalwart"),
		NewAPI:    get("newapi"),
		Agenity:   get("agenity"),
	}
}

// docRoot unwraps a decoded DocumentNode to its content mapping.
func docRoot(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

// mappingChild returns the value node for key in a YAML mapping, or nil. When a
// key appears more than once the LAST occurrence wins, matching Helm.
func mappingChild(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	var found *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			found = n.Content[i+1]
		}
	}
	return found
}

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, "docs", "PRINCIPLES.md")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root from %s", wd)
	return ""
}

// THE ASSERTION. Rendered with the pins the shipped chart actually supplies,
// no per-Organization HelmRelease may carry a floating version.
func TestOrgTenantHelmReleases_CarryConcreteChartVersion_6169(t *testing.T) {
	files, err := renderOrganizationOverlay(d31TestRec(), chartValuesOrgPins(t))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	findings, inspected, err := scanHelmReleaseChartVersions(files)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	// The scan must have SEEN the releases before its silence means anything.
	// The overlay emits bp-keycloak, bp-newapi, bp-wordpress-tenant,
	// bp-openclaw, bp-stalwart-tenant and bp-agenity (orgTenantTemplates).
	if inspected < 6 {
		t.Fatalf("only %d HelmReleases parsed out of the rendered overlay (want >= 6) — the render or the decode is broken, so any clean result below would be an artefact of this harness rather than a finding", inspected)
	}
	if len(findings) != 0 {
		var b strings.Builder
		for _, f := range findings {
			b.WriteString("\n  " + f.String())
		}
		t.Fatalf("%d per-Organization HelmRelease(s) carry a non-concrete chart version even though the chart supplies pins:%s\n\nA floating version resolves to the HIGHEST semver tag on the OCI repo at reconcile time, so an Organization installs whatever was published most recently — which is how a stray bp-agenity IMAGE tag out-ranked the real chart and broke every install (#4922), and why UAT rows 232/234 cited chart versions their deployment never pinned. Fix the missing key in products/catalyst/chart/values.yaml orgTenants.chartVersions and its env entry in products/catalyst/chart/templates/api-deployment.yaml (#6169).", len(findings), b.String())
	}
}

// Every pin the chart ships must itself be a concrete SemVer. Catches a
// values.yaml that supplies a range or a star — which would satisfy "the env
// is set" while leaving the resolution just as floating as before.
func TestChartValuesOrgPins_AreConcrete_6169(t *testing.T) {
	pins := chartValuesOrgPins(t)
	for _, tc := range []struct{ key, val string }{
		{"keycloak", pins.Keycloak},
		{"cnpg", pins.CNPG},
		{"wordpress", pins.WordPress},
		{"openclaw", pins.OpenClaw},
		{"stalwart", pins.Stalwart},
		{"newapi", pins.NewAPI},
		{"agenity", pins.Agenity},
	} {
		if !concreteVersionRE.MatchString(tc.val) {
			t.Errorf("orgTenants.chartVersions.%s = %q — a per-Organization chart pin must be an exact SemVer; a range or a star leaves the installed artifact free to change under a Sovereign nobody redeployed (#6169)", tc.key, tc.val)
		}
	}
}

// The exact pin supplied for bp-agenity must stay inside the bounded range the
// Go fallback declares. If they diverge, the chart and the never-fatal fallback
// disagree about which minor line is correct and only one of them is right.
func TestAgenityPin_StaysInsideTheBoundedFallback_6169(t *testing.T) {
	pins := chartValuesOrgPins(t)
	if !strings.HasPrefix(pins.Agenity, "0.5.") {
		t.Fatalf("orgTenants.chartVersions.agenity = %q, which is outside agenityChartConstraint %q (organization_gitops.go:917) — bump the constant in lockstep or the chart pin and the fallback disagree about the bp-agenity minor line (#4922, #6169)", pins.Agenity, agenityChartConstraint)
	}
}

// CONTROL — the scanner must not flag a HelmRelease that legitimately carries a
// version. Without this arm, a scanner that flagged everything would satisfy
// the vacuity check and the main assertion could never be trusted.
func TestChartVersionScanner_Table_6169(t *testing.T) {
	hr := func(version string) string {
		return `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: bp-openclaw
spec:
  chart:
    spec:
      chart: bp-openclaw
      version: ` + version + `
`
	}
	cases := []struct {
		name    string
		version string
		flagged bool
	}{
		// CONTROL: a real pin, the shape the fix ships.
		{"exact semver is accepted", `"0.2.17"`, false},
		{"exact semver, four-part patch line", `"1.4.153"`, false},
		{"prerelease pin is accepted", `"0.2.17-rc.1"`, false},
		// The defect, and its near neighbours — all must be flagged.
		{"bare star is rejected", `"*"`, true},
		{"empty is rejected", `""`, true},
		{"caret range is rejected", `"^0.2.17"`, true},
		{"tilde range is rejected", `"~0.2.17"`, true},
		{"bounded range is rejected", `">=0.5.0 <0.6.0"`, true},
		{"x-wildcard minor is rejected", `"0.2.x"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings, inspected, err := scanHelmReleaseChartVersions(map[string]string{"bp-openclaw.yaml": hr(tc.version)})
			if err != nil {
				t.Fatalf("scan: %v", err)
			}
			if inspected != 1 {
				t.Fatalf("scanner inspected %d HelmReleases, want 1 — the fixture did not parse, so the verdict below means nothing", inspected)
			}
			if got := len(findings) > 0; got != tc.flagged {
				t.Fatalf("version %s: flagged=%v, want %v", tc.version, got, tc.flagged)
			}
		})
	}
}

// VACUITY — the same scanner, pointed at the PRE-FIX render (an empty
// OrganizationChartVersions, which is precisely what the shipped chart produced
// before this change), must report findings. This is the red half of
// red-then-green kept permanently in the suite: if the scanner ever stops being
// able to fail, this test goes red and says so.
func TestOrgTenantHelmReleases_ScannerFailsOnThePreFixShape_6169(t *testing.T) {
	files, err := renderOrganizationOverlay(d31TestRec(), OrganizationChartVersions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	findings, inspected, err := scanHelmReleaseChartVersions(files)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if inspected < 6 {
		t.Fatalf("only %d HelmReleases parsed (want >= 6) — the harness, not the fixture, is what would be failing", inspected)
	}
	if len(findings) == 0 {
		t.Fatalf("the scanner found NO floating version in an overlay rendered with zero configured pins — that is the exact pre-fix state (six of seven CATALYST_ORG_BP_*_VER resolving to `version: \"*\"`), so a scanner that reports it clean cannot detect the defect it exists to catch (#6169)")
	}
	// The star specifically, not merely "something non-concrete": the pre-fix
	// shape is the bare wildcard for the five star-fallback charts, and
	// agenity's bounded range for the sixth.
	stars := 0
	for _, f := range findings {
		if f.Version == "*" {
			stars++
		}
	}
	if stars < 5 {
		t.Fatalf("expected at least 5 bare `*` versions in the unconfigured render (keycloak, newapi, wordpress-tenant, openclaw, stalwart-tenant), got %d across findings %v — withVersionDefaults' star fallback has changed shape and this guard's premise needs re-reading (#6169)", stars, findings)
	}
}
