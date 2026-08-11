package bootstrapkit

// org_bp_chart_version_pins_6169_test.go — Refs #6169.
//
// THE DEFECT. catalyst-api reads seven per-Organization chart versions from env
// — CATALYST_ORG_BP_{KEYCLOAK,CNPG,WORDPRESS,OPENCLAW,STALWART,NEWAPI,AGENITY}_VER,
// at products/catalyst/bootstrap/api/cmd/api/main.go:996-1002 — and a repo-wide
// grep for those names across every .yaml/.yml/.tpl/.json matched them ZERO
// times. No configuration the platform shipped could set any of them, so six of
// the seven fell through withVersionDefaults (internal/handler/
// organization_gitops.go:919) to `version: "*"` and every per-Organization
// HelmRelease resolved to whatever the OCI repo served newest at reconcile time.
//
// The provisioning service — the SECOND producer, writing the funnel cart's
// per-Org tree — had the same hole under a different name:
// CATALYST_HR_APP_CHART_VERSIONS (core/services/provisioning/main.go:191) also
// had zero setters, so it ran on its compiled default
// "openclaw=0.2.13,stalwart-mail=0.1.13" — both pins stale against the shipped
// charts, and `newapi` (the third HelmRelease-shaped slug) absent entirely, so
// bp-newapi rendered a bare wildcard on every funnel Organization.
//
// WHY THIS GUARD IS NARROW. It does NOT assert the general "every CATALYST_*
// var Go reads must have a YAML setter". 47 of the 108 such vars are
// legitimately-optional override knobs with deliberate Go-side defaults
// (CATALYST_PIN_ECHO, CATALYST_PREFLIGHT_DISABLE, …); a blunt guard would fire
// on all 47 and be reverted, which is recorded on #6169 precisely so nobody
// writes it. The subject here is one narrow property with a live failure mode:
// a chart VERSION CONSTRAINT that silently degrades to a wildcard.
//
// WHAT IT PINS
//
//  1. All seven names reach the rendered catalyst-api Pod — the literal absence.
//  2. Each carries a CONCRETE SemVer. "Set" is not the property that matters;
//     an env set to "*" would satisfy assertion 1 and change nothing.
//  3. Each equals BOTH that chart's own platform|products/<x>/chart/Chart.yaml
//     `version:` AND the pin the Sovereign's catalog advertises
//     (templates/catalog-seed/blueprints.yaml). Asserting against Chart.yaml is
//     what stops this from passing green over a stale seed — the trap
//     check-bootstrap-kit-pin-sync.sh leaves open, since it compares the
//     bootstrap-kit slot to Chart.yaml and never looks at the seed.
//  4. CATALYST_HR_APP_CHART_VERSIONS reaches the provisioning Pod and covers
//     EVERY slug in helmReleaseAppSlugs — enumerated out of the Go source, so
//     adding a fourth HelmRelease-shaped app without a pin fails here rather
//     than shipping a wildcard.
//
// CONTROL. Every assertion runs against a render that also carries 150+ other
// CATALYST_* entries; the count is asserted first, so an absence below is a
// finding and not an empty parse. The version predicate is unit-tested
// separately in the producer's own package
// (products/catalyst/bootstrap/api/internal/handler/
// organization_chart_version_pins_6169_test.go) against a HelmRelease that
// legitimately carries a version — it must NOT be flagged.
//
// VACUITY. TestOrgBPChartVersionPins_CannotBeBlanked_6169 blanks one pin and
// requires the render to FAIL. That proves both that the guard's subject can
// fail and that the chart refuses to express "unpinned" at all — an empty
// value here reads identically to the state this change ends.

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// orgBPChartPin ties one env var to the chart it pins and to that chart's
// source-tree location, so a failure names the file to edit rather than the
// symptom.
type orgBPChartPin struct {
	Env       string // env var read by catalyst-api
	Chart     string // bp-* chart name, as it appears in the catalog seed
	ChartYAML string // repo-relative path to the chart's own Chart.yaml
}

var orgBPChartPins = []orgBPChartPin{
	{"CATALYST_ORG_BP_KEYCLOAK_VER", "bp-keycloak", "platform/keycloak/chart/Chart.yaml"},
	{"CATALYST_ORG_BP_CNPG_VER", "bp-cnpg", "platform/cnpg/chart/Chart.yaml"},
	{"CATALYST_ORG_BP_WORDPRESS_VER", "bp-wordpress-tenant", "platform/wordpress-tenant/chart/Chart.yaml"},
	{"CATALYST_ORG_BP_OPENCLAW_VER", "bp-openclaw", "platform/openclaw/chart/Chart.yaml"},
	{"CATALYST_ORG_BP_STALWART_VER", "bp-stalwart-tenant", "platform/stalwart-tenant/chart/Chart.yaml"},
	{"CATALYST_ORG_BP_NEWAPI_VER", "bp-newapi", "platform/newapi/chart/Chart.yaml"},
	{"CATALYST_ORG_BP_AGENITY_VER", "bp-agenity", "products/agenity/chart/Chart.yaml"},
}

// hrAppSlugChart maps each HelmRelease-shaped CATALOG APP SLUG (the key format
// CATALYST_HR_APP_CHART_VERSIONS uses) to its bp-* chart. The slug set itself is
// read from the Go source at test time — this map only supplies the slug→chart
// correspondence, which has no machine-readable declaration.
var hrAppSlugChart = map[string]string{
	"openclaw":      "bp-openclaw",
	"stalwart-mail": "bp-stalwart-tenant",
	"newapi":        "bp-newapi",
}

var concreteChartVersionRE = regexp.MustCompile(`^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// chartYAMLVersion reads the `version:` field of a chart's own Chart.yaml.
func chartYAMLVersion(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	re := regexp.MustCompile(`(?m)^version:\s*"?([^"\s#]+)"?`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatalf("%s has no top-level `version:` field — this guard's premise (Chart.yaml is the authoritative chart version) no longer holds", rel)
	}
	return string(m[1])
}

// catalogSeedPinsByChart reuses main_test.go's catalog-seed parser and keys it
// by chart name — the version the Sovereign's own catalog advertises for each
// bp-*. Reusing that parser rather than adding a second one keeps this guard
// and TestCatalogSeed_DeliveryPinsNotBehindComponentCharts reading the seed the
// same way; two parsers that disagree about what a pin IS would let a drift slip
// between them.
func catalogSeedPinsByChart(t *testing.T, root string) map[string]string {
	t.Helper()
	pins := catalogSeedPins(t, root)
	if len(pins) < 40 {
		t.Fatalf("parsed only %d catalog-seed source blocks — the parser is broken, so any comparison below would be an artefact of this harness rather than a finding", len(pins))
	}
	out := map[string]string{}
	for _, p := range pins {
		out[p.chart] = p.version
	}
	return out
}

// renderChartTemplate renders one template of products/catalyst/chart and
// returns its YAML. Extra --set args may be appended.
func renderChartTemplate(t *testing.T, root, tmpl string, sets ...string) string {
	t.Helper()
	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v)", err)
	}
	args := []string{"template", "catalyst", "."}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	args = append(args, "-s", tmpl)
	cmd := exec.Command(helmBin, args...)
	cmd.Dir = filepath.Join(root, "products", "catalyst", "chart")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %s failed: %v\n%s", tmpl, err, out)
	}
	return string(out)
}

// helmReleaseAppSlugsFromSource reads the HelmRelease-shaped app slugs straight
// out of core/services/provisioning/gitops/helmrelease_apps.go. Reading the
// producer rather than restating its contents is what makes a FOURTH such app
// fail this guard instead of quietly shipping a wildcard.
func helmReleaseAppSlugsFromSource(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "core", "services", "provisioning", "gitops", "helmrelease_apps.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	block := regexp.MustCompile(`(?s)var helmReleaseAppSlugs = map\[string\]bool\{(.*?)\n\}`).FindSubmatch(raw)
	if block == nil {
		t.Fatalf("could not locate `var helmReleaseAppSlugs = map[string]bool{` in %s — the enumeration this guard reads has been renamed or reshaped, so it can no longer see a newly added HelmRelease-shaped app", path)
	}
	entries := regexp.MustCompile(`"([^"]+)":\s*true`).FindAllSubmatch(block[1], -1)
	if len(entries) == 0 {
		t.Fatalf("helmReleaseAppSlugs parsed to zero slugs — the scan is blind, not the map empty")
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, string(e[1]))
	}
	sort.Strings(out)
	return out
}

// ── 1-3: the catalyst-api leg ────────────────────────────────────────────────

func TestOrgBPChartVersionPins_ReachThePod_AndAreConcrete_6169(t *testing.T) {
	root := repoRoot(t)

	env, err := renderCatalystAPI(t, root)
	if err != nil {
		t.Fatalf("default render failed: %v", err)
	}

	// CONTROL FIRST — the env block is large and full of CATALYST_ names. If the
	// lookups below find nothing it must be because THESE names are absent, not
	// because the render or the parse came up empty.
	catalystCount := 0
	for name := range env {
		if strings.HasPrefix(name, "CATALYST_") {
			catalystCount++
		}
	}
	if catalystCount < 50 {
		t.Fatalf("only %d CATALYST_* env entries parsed out of the rendered Pod — the render/parse is broken, so any absence below would be an artefact of this harness rather than a finding", catalystCount)
	}

	seed := catalogSeedPinsByChart(t, root)

	for _, pin := range orgBPChartPins {
		got, ok := env[pin.Env]
		if !ok {
			t.Errorf("%s is absent from the rendered catalyst-api Pod (%d CATALYST_* entries present, so the scan is not blind) — main.go reads it into OrganizationChartVersions and no shipped configuration can set it, so every per-Organization %s HelmRelease resolves `version: \"*\"` and installs whatever the OCI repo serves newest at reconcile time (#6169)", pin.Env, catalystCount, pin.Chart)
			continue
		}
		if !concreteChartVersionRE.MatchString(got) {
			t.Errorf("%s renders %q — a per-Organization chart pin must be an exact SemVer. A star, a range or an empty value leaves the installed artifact free to change under a Sovereign nobody redeployed, which is the property that made UAT rows 232/234 cite chart versions their deployment never pinned (#6169)", pin.Env, got)
			continue
		}
		if want := chartYAMLVersion(t, root, pin.ChartYAML); got != want {
			t.Errorf("%s renders %q but %s declares version %s — the per-Organization pin has drifted behind the chart it pins. Bump orgTenants.chartVersions in products/catalyst/chart/values.yaml in the same commit as the chart (#6169)", pin.Env, got, pin.ChartYAML, want)
		}
		// The catalog advertises a version to the customer; the deployment
		// installs one. When those differ, a walk's evidence cites a version the
		// Organization never received — the measurement half of #6169.
		if want, ok := seed[pin.Chart]; ok && got != want {
			t.Errorf("%s renders %q but the Sovereign's own catalog seed advertises %s@%s — the version a customer is shown and the version their Organization installs must be the same one, or UAT evidence naming a chart version is unreproducible (#6169)", pin.Env, got, pin.Chart, want)
		} else if !ok {
			t.Errorf("catalog seed carries no source.version for %s, yet %s pins it — one of the two enumerations is wrong (#6169)", pin.Chart, pin.Env)
		}
	}
}

// ── 4: the provisioning (funnel) leg ─────────────────────────────────────────

func TestHelmReleaseAppChartVersions_ReachProvisioning_AndCoverEverySlug_6169(t *testing.T) {
	root := repoRoot(t)

	// The provisioning Deployment renders only when the marketplace ingress is
	// on — that is the Sovereign shape where funnel Organizations exist at all.
	out := renderChartTemplate(t, root, "templates/org-services/provisioning.yaml", "ingress.marketplace.enabled=true")

	re := regexp.MustCompile(`(?m)^\s*-\s*name:\s*CATALYST_HR_APP_CHART_VERSIONS\s*\n\s*value:\s*"?([^"\n]*)"?`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("CATALYST_HR_APP_CHART_VERSIONS is absent from the rendered provisioning Pod — core/services/provisioning/main.go:191 reads it, nothing sets it, and the compiled fallback (\"openclaw=0.2.13,stalwart-mail=0.1.13\") is both stale and missing newapi, so bp-newapi ships `version: \"*\"` on every funnel Organization (#6169)")
	}

	// CONTROL that the render is not empty: the provisioning Pod carries the
	// TENANT_GITOPS_* block alongside this one.
	if !strings.Contains(out, "TENANT_GITOPS_PER_ORG") {
		t.Fatalf("the rendered provisioning template carries no TENANT_GITOPS_PER_ORG — the render is not the provisioning Deployment, so the finding above is about the wrong object")
	}

	pins := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(m[1]), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok {
			pins[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	for _, slug := range helmReleaseAppSlugsFromSource(t, root) {
		got, ok := pins[slug]
		if !ok {
			t.Errorf("CATALYST_HR_APP_CHART_VERSIONS has no entry for HelmRelease-shaped app %q (rendered value: %q) — an unpinned slug renders the template's literal `version: \"*\"` because pinHRChartVersion only substitutes when a pin is configured (helmrelease_apps.go:211). Add it to the env in products/catalyst/chart/templates/org-services/provisioning.yaml (#6169)", slug, m[1])
			continue
		}
		if !concreteChartVersionRE.MatchString(got) {
			t.Errorf("CATALYST_HR_APP_CHART_VERSIONS pins %s=%q — must be an exact SemVer (#6169)", slug, got)
			continue
		}
		chart, known := hrAppSlugChart[slug]
		if !known {
			t.Errorf("HelmRelease-shaped app %q has no chart mapping in this guard — add it to hrAppSlugChart so its pin can be compared against the chart it installs (#6169)", slug)
			continue
		}
		for _, pin := range orgBPChartPins {
			if pin.Chart != chart {
				continue
			}
			if want := chartYAMLVersion(t, root, pin.ChartYAML); got != want {
				t.Errorf("CATALYST_HR_APP_CHART_VERSIONS pins %s=%s but %s declares version %s — the funnel leg installs a different chart version than the one the repo ships (#6169)", slug, got, pin.ChartYAML, want)
			}
		}
	}
}

// The two producers must agree. A funnel Organization and a BSS-door
// Organization on the SAME Sovereign receiving different versions of the same
// chart is the shape that makes one walk's evidence contradict another's.
func TestBothProducers_PinTheSameVersions_6169(t *testing.T) {
	root := repoRoot(t)

	env, err := renderCatalystAPI(t, root)
	if err != nil {
		t.Fatalf("default render failed: %v", err)
	}
	out := renderChartTemplate(t, root, "templates/org-services/provisioning.yaml", "ingress.marketplace.enabled=true")
	re := regexp.MustCompile(`(?m)^\s*-\s*name:\s*CATALYST_HR_APP_CHART_VERSIONS\s*\n\s*value:\s*"?([^"\n]*)"?`)
	m := re.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("CATALYST_HR_APP_CHART_VERSIONS absent — see TestHelmReleaseAppChartVersions_ReachProvisioning_AndCoverEverySlug_6169")
	}
	funnel := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(m[1]), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if ok {
			funnel[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}

	for slug, chart := range hrAppSlugChart {
		for _, pin := range orgBPChartPins {
			if pin.Chart != chart {
				continue
			}
			door, ok := env[pin.Env]
			if !ok {
				continue // reported by the first test
			}
			if funnel[slug] != door {
				t.Errorf("%s: the funnel leg pins %q and the BSS-door leg pins %q — two Organizations on the same Sovereign would receive different versions of the same chart (#6169)", chart, funnel[slug], door)
			}
		}
	}
}

// VACUITY — blanking a pin must FAIL the render. Two things are proven at once:
// this guard's subject can fail, and the chart cannot express "unpinned", which
// is the state that produced `version: "*"` for the entire life of the feature.
func TestOrgBPChartVersionPins_CannotBeBlanked_6169(t *testing.T) {
	root := repoRoot(t)
	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v)", err)
	}
	for _, key := range []string{"keycloak", "cnpg", "wordpress", "openclaw", "stalwart", "newapi", "agenity"} {
		t.Run(key, func(t *testing.T) {
			cmd := exec.Command(helmBin, "template", "catalyst", ".",
				"--set", "orgTenants.chartVersions."+key+"=",
				"-s", "templates/api-deployment.yaml")
			cmd.Dir = filepath.Join(root, "products", "catalyst", "chart")
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("blanking orgTenants.chartVersions.%s rendered successfully — an empty per-Organization chart pin must fail the render, not degrade to the floating `version: \"*\"` that #6169 exists to end. Rendered output:\n%s", key, out)
			}
			if !strings.Contains(string(out), "concrete chart version") {
				t.Fatalf("blanking orgTenants.chartVersions.%s failed the render, but not with the `required` message this guard is asserting — the failure may be unrelated:\n%s", key, out)
			}
		})
	}
}
