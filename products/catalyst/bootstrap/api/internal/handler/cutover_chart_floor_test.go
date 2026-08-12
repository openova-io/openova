package handler

import (
	"errors"
	"os"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// helmLabel builds the exact `helm.sh/chart` value Helm emits for this chart,
// so the fixtures cannot drift from the real label shape. Proven against a real
// `helm template` render in TestCutoverStepCarriesChartVersionFromRealLabelShape.
func helmLabel(version string) string {
	return cutoverChartNameForFloor + "-" + version
}

func stepAt(name, version string) cutoverStep {
	return cutoverStep{
		order:        1,
		cmName:       "cutover-step-06-helmrepository-patches",
		stepName:     name,
		mode:         cutoverModeJob,
		chartVersion: version,
	}
}

// ── The defect itself ───────────────────────────────────────────────────────

// TestCutoverChartFloorRefusesHw292Version is the RED-then-GREEN case.
//
// hw292 cut over on 0.1.159 and reached cutoverComplete=true with 62
// HelmRepositories still on ghcr.io (#5919). Before this change the engine
// started on that chart; it must now refuse.
func TestCutoverChartFloorRefusesHw292Version(t *testing.T) {
	err := assertCutoverChartFloor([]cutoverStep{stepAt("helmrepository-patches", "0.1.159")})
	if err == nil {
		t.Fatal("0.1.159 (the hw292 chart) was ACCEPTED — the floor does not hold")
	}
	var fe *cutoverChartFloorError
	if !errors.As(err, &fe) {
		t.Fatalf("want *cutoverChartFloorError, got %T", err)
	}
	// Assert on the VALUE carried, not merely that an error happened. An
	// error whose observed version is empty would be indistinguishable from
	// the unknown-version refusal and would give the operator no diagnosis.
	if fe.observed != "0.1.159" {
		t.Errorf("observed = %q, want %q", fe.observed, "0.1.159")
	}
	if fe.floor != cutoverMinChartVersion {
		t.Errorf("floor = %q, want %q", fe.floor, cutoverMinChartVersion)
	}
	for _, want := range []string{"0.1.159", cutoverMinChartVersion, "#5919"} {
		if !strings.Contains(fe.detail, want) {
			t.Errorf("detail does not mention %q; got: %s", want, fe.detail)
		}
	}
}

// ── CONTROL: shares the suspect property, must stay green ───────────────────

// TestCutoverChartFloorAcceptsFloorExactly is the CONTROL that shares the
// suspect property — it is the SAME chart, the SAME label shape, the SAME code
// path, differing only in being at the floor rather than below it. If this went
// red the gate would be refusing everything and the red test above would prove
// nothing.
func TestCutoverChartFloorAcceptsFloorExactly(t *testing.T) {
	if err := assertCutoverChartFloor([]cutoverStep{stepAt("helmrepository-patches", cutoverMinChartVersion)}); err != nil {
		t.Fatalf("the floor version %s must be ACCEPTED (>=, not >): %v", cutoverMinChartVersion, err)
	}
}

// TestCutoverChartFloorAcceptsCurrentChart is the second control: the version
// actually in platform/self-sovereign-cutover/chart/Chart.yaml today must pass.
// A floor that rejects the shipping chart would wedge every Sovereign.
func TestCutoverChartFloorAcceptsCurrentChart(t *testing.T) {
	for _, v := range []string{"0.1.172", "0.1.179", "0.2.0", "1.0.0"} {
		if err := assertCutoverChartFloor([]cutoverStep{stepAt("helmrepository-patches", v)}); err != nil {
			t.Errorf("version %s at/above the floor must be accepted: %v", v, err)
		}
	}
}

// TestCutoverChartFloorRefusesOneBelowTheFloor pins the boundary that moved.
//
// 0.1.171 carries #5710's durable secondary-pivot assert but NOT #5719's loft
// pivot for every region, so a two-region Sovereign at 0.1.171 still reaches
// cutoverComplete=true with region B on charts.loft.sh. The source-side guard
// used to sit exactly here and called it a pass.
func TestCutoverChartFloorRefusesOneBelowTheFloor(t *testing.T) {
	err := assertCutoverChartFloor([]cutoverStep{stepAt("helmrepository-patches", "0.1.171")})
	if err == nil {
		t.Fatal("0.1.171 was ACCEPTED — it is one below the floor and still carries the loft tether in region B")
	}
	var fe *cutoverChartFloorError
	if !errors.As(err, &fe) {
		t.Fatalf("want *cutoverChartFloorError, got %T", err)
	}
	if fe.observed != "0.1.171" {
		t.Errorf("observed = %q, want %q", fe.observed, "0.1.171")
	}
}

// ── The comparison that a naive implementation gets wrong ───────────────────

// TestChartVersionCompareIsNumericNotLexicographic pins the one comparison
// most likely to be silently wrong. Lexicographically "0.1.99" > "0.1.171"
// (because "9" > "1"), which would let a 0.1.99 chart through a 0.1.17x floor.
// This is the entire reason parseChartVersion is strict.
func TestChartVersionCompareIsNumericNotLexicographic(t *testing.T) {
	if "0.1.99" <= cutoverMinChartVersion {
		t.Fatal("premise broken: this test only means something while 0.1.99 sorts ABOVE the floor as a string")
	}
	err := assertCutoverChartFloor([]cutoverStep{stepAt("helmrepository-patches", "0.1.99")})
	if err == nil {
		t.Fatalf("0.1.99 was accepted against a %s floor — the comparison is lexicographic, not numeric", cutoverMinChartVersion)
	}

	cases := []struct {
		a, b string
		want int
	}{
		{"0.1.159", "0.1.171", -1},
		{"0.1.99", "0.1.171", -1},
		{"0.1.171", "0.1.171", 0},
		{"0.1.172", "0.1.171", 1},
		{"0.2.0", "0.1.999", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.1.170", "0.1.171", -1},
	}
	for _, c := range cases {
		got, err := compareChartVersions(c.a, c.b)
		if err != nil {
			t.Errorf("compare(%s,%s) errored: %v", c.a, c.b, err)
			continue
		}
		if got != c.want {
			t.Errorf("compare(%s,%s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// ── Certification requires POSITIVE evidence ────────────────────────────────

// TestCutoverChartFloorRefusesUnknownVersion covers the verdict-from-absent-
// evidence class. A step with no readable version must FAIL the gate. If it
// passed, then deleting the label — or installing a chart old enough not to
// emit it — would become a way to bypass the floor entirely, which is strictly
// worse than not having a floor.
func TestCutoverChartFloorRefusesUnknownVersion(t *testing.T) {
	cases := []struct {
		name         string
		chartVersion string
	}{
		{"empty label", ""},
		{"whitespace only", "   "},
		{"not a semver", "latest"},
		{"two segments", "0.1"},
		{"four segments", "0.1.171.1"},
		{"empty segment", "0..171"},
		{"non-numeric segment", "0.1.x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := assertCutoverChartFloor([]cutoverStep{stepAt("helmrepository-patches", c.chartVersion)})
			if err == nil {
				t.Fatalf("chartVersion=%q was ACCEPTED — an unreadable version must never certify", c.chartVersion)
			}
			var fe *cutoverChartFloorError
			if !errors.As(err, &fe) {
				t.Fatalf("want *cutoverChartFloorError, got %T", err)
			}
			if !strings.Contains(fe.detail, cutoverMinChartVersion) {
				t.Errorf("refusal must name the floor; got: %s", fe.detail)
			}
		})
	}
}

// ── The weakest step governs ────────────────────────────────────────────────

// TestCutoverChartFloorTakesMinimumAcrossSteps proves the gate reads every step
// rather than the first. A partially-failed Helm upgrade, or a stale step
// ConfigMap from an earlier release, leaves a MIXED set — and the engine runs
// all of them, so one old step-06 is enough to make the outcome unprovable.
//
// A first-step-only implementation passes the "all new" case and the "all old"
// case, and is wrong only here.
func TestCutoverChartFloorTakesMinimumAcrossSteps(t *testing.T) {
	mixed := []cutoverStep{
		stepAt("gitea-mirror", "0.1.179"),
		stepAt("harbor-projects", "0.1.179"),
		// The stale one, in the middle, and it is step-06 — the step whose
		// assert the floor is actually about.
		stepAt("helmrepository-patches", "0.1.159"),
		stepAt("egress-block-test", "0.1.179"),
	}
	err := assertCutoverChartFloor(mixed)
	if err == nil {
		t.Fatal("a mixed step set containing 0.1.159 was ACCEPTED — the gate is not reading every step")
	}
	var fe *cutoverChartFloorError
	if !errors.As(err, &fe) {
		t.Fatalf("want *cutoverChartFloorError, got %T", err)
	}
	if fe.observed != "0.1.159" {
		t.Errorf("observed = %q, want the LOWEST version %q", fe.observed, "0.1.159")
	}
	if !strings.Contains(fe.detail, "helmrepository-patches") {
		t.Errorf("refusal must name WHICH step is stale; got: %s", fe.detail)
	}

	// Control: the same shape, same length, same ordering — all at or above
	// the floor — must pass. This isolates "mixed" from "many steps".
	allGood := []cutoverStep{
		stepAt("gitea-mirror", "0.1.179"),
		stepAt("harbor-projects", "0.1.179"),
		stepAt("helmrepository-patches", cutoverMinChartVersion),
		stepAt("egress-block-test", "0.1.179"),
	}
	if err := assertCutoverChartFloor(allGood); err != nil {
		t.Fatalf("control: an all-at-or-above step set must be accepted: %v", err)
	}
}

// ── Label parsing ───────────────────────────────────────────────────────────

func TestChartVersionFromHelmLabel(t *testing.T) {
	cases := []struct {
		label   string
		want    string
		wantOK  bool
		comment string
	}{
		{helmLabel("0.1.179"), "0.1.179", true, "the real shape"},
		{helmLabel("0.1.159"), "0.1.159", true, "the hw292 shape"},
		{"", "", false, "absent label"},
		{"bp-self-sovereign-cutover-", "", false, "name with no version"},
		{"bp-agenity-0.4.2", "", false, "a DIFFERENT chart must not be read as this one"},
		{"0.1.179", "", false, "bare version with no chart name"},
		{"self-sovereign-cutover-0.1.179", "", false, "near-miss chart name"},
	}
	for _, c := range cases {
		got, ok := chartVersionFromHelmLabel(c.label)
		if ok != c.wantOK || got != c.want {
			t.Errorf("chartVersionFromHelmLabel(%q) = (%q,%v), want (%q,%v) — %s",
				c.label, got, ok, c.want, c.wantOK, c.comment)
		}
	}
}

// TestParseCutoverStepReadsChartVersionFromLabel proves the wiring: the version
// reaches cutoverStep from a real ConfigMap's labels. Without this the floor
// would be correct in isolation and dead in production — the guard-tested-a-
// surface-that-cannot-fail shape.
func TestParseCutoverStepReadsChartVersionFromLabel(t *testing.T) {
	cm := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cutover-step-06-helmrepository-patches",
			Namespace: "catalyst",
			Labels: map[string]string{
				cutoverStepPartOfLabel:    cutoverStepPartOfValue,
				cutoverStepComponentLabel: cutoverStepComponentValue,
				cutoverStepOrderLabel:     "6",
				cutoverChartLabel:         helmLabel("0.1.179"),
			},
		},
		Data: map[string]string{
			"stepName": "helmrepository-patches",
			"podSpec":  "containers:\n- name: x\n  image: busybox:1.36\nrestartPolicy: Never\n",
		},
	}
	step, err := parseCutoverStep(cm)
	if err != nil {
		t.Fatalf("parseCutoverStep: %v", err)
	}
	if step.chartVersion != "0.1.179" {
		t.Fatalf("chartVersion = %q, want %q — the floor would read nothing in production", step.chartVersion, "0.1.179")
	}

	// Same ConfigMap, label removed: the version must be empty (which the
	// floor refuses), NOT silently defaulted to something that passes.
	delete(cm.Labels, cutoverChartLabel)
	step, err = parseCutoverStep(cm)
	if err != nil {
		t.Fatalf("parseCutoverStep without chart label: %v", err)
	}
	if step.chartVersion != "" {
		t.Fatalf("chartVersion = %q with no label, want empty", step.chartVersion)
	}
	if err := assertCutoverChartFloor([]cutoverStep{step}); err == nil {
		t.Fatal("a step parsed from a ConfigMap with no chart label was ACCEPTED")
	}
}

// ── VACUITY CHECK ───────────────────────────────────────────────────────────

// TestCutoverChartFloorGuardCanFail is the vacuity check: it proves the
// assertions above are capable of going red, by running the SAME assertion
// against a deliberately-broken stand-in floor.
//
// Without this, every test in this file would still pass if
// assertCutoverChartFloor were replaced by `return nil` in some future
// refactor that also relaxed the red cases — a guard that cannot fail is
// decorative. Here the failure is demonstrated, not assumed.
func TestCutoverChartFloorGuardCanFail(t *testing.T) {
	// A stand-in with the defect the real gate must not have: it accepts
	// anything it cannot parse, and compares lexicographically.
	permissive := func(steps []cutoverStep) error {
		for _, s := range steps {
			if s.chartVersion == "" {
				continue // the verdict-from-absent-evidence defect
			}
			if s.chartVersion < cutoverMinChartVersion { // the lexicographic defect
				return errors.New("too old")
			}
		}
		return nil
	}

	// Each sample is one the REAL gate rejects. The permissive stand-in must
	// accept at least one of them, or these samples prove nothing about the
	// real gate's strength.
	samples := []struct {
		name string
		step cutoverStep
	}{
		{"0.1.99 beats a 0.1.171 floor lexicographically", stepAt("helmrepository-patches", "0.1.99")},
		{"absent version", stepAt("helmrepository-patches", "")},
		{"unparseable version", stepAt("helmrepository-patches", "latest")},
	}

	permissiveAccepted := 0
	for _, s := range samples {
		realErr := assertCutoverChartFloor([]cutoverStep{s.step})
		if realErr == nil {
			t.Errorf("VACUITY: the real gate accepted %q — it is not asserting what this file claims", s.name)
			continue
		}
		if permissive([]cutoverStep{s.step}) == nil {
			permissiveAccepted++
			t.Logf("vacuity ok: %q is rejected by the real gate and accepted by the permissive stand-in", s.name)
		}
	}
	if permissiveAccepted == 0 {
		t.Fatal("VACUITY: no sample distinguishes the real gate from a permissive one — these tests would pass against a broken gate")
	}
	if permissiveAccepted != len(samples) {
		t.Errorf("expected all %d samples to distinguish the gates, got %d", len(samples), permissiveAccepted)
	}
}

// TestCutoverChartFloorConstantMatchesItsStatedDerivation guards the number
// itself. 0.1.171 is not arbitrary — it is the chart version at commit
// 881115109, where #5710 landed assert_secondary_pivot_durable(). If someone
// edits the constant without editing the rationale, this fails and says so.
func TestCutoverChartFloorConstantMatchesItsStatedDerivation(t *testing.T) {
	if cutoverMinChartVersion != "0.1.172" {
		t.Fatalf("cutoverMinChartVersion = %q, want %q.\n"+
			"The floor is the LATER of two same-class tethers: #5710 (assert_secondary_pivot_durable,\n"+
			"merged at 881115109 where Chart.yaml reads 0.1.171) and #5719 (loft pivot in EVERY region,\n"+
			"merged at 901b3da22 where Chart.yaml reads 0.1.172).\n"+
			"If you are RAISING it, update the derivation comment in cutover_chart_floor.go to name the\n"+
			"new issue and the new assert, and raise scripts/check-cutover-version-floor.py in the same change.",
			cutoverMinChartVersion, "0.1.172")
	}
	if _, err := parseChartVersion(cutoverMinChartVersion); err != nil {
		t.Fatalf("the floor constant itself must parse: %v", err)
	}
}

// TestChartFloorMatchesTheSourceSideGuard keeps the two floors in lockstep.
//
// There are two, and they answer different questions:
//
//   - scripts/check-cutover-version-floor.py is SOURCE-side. It reads the seven
//     bp-self-sovereign-cutover pins across the five lockstep sites and stops
//     this repo from SHIPPING a low pin.
//   - cutoverMinChartVersion is RUNTIME. It stops a Sovereign from RUNNING a
//     cutover on a low chart that is already installed — the case a source scan
//     structurally cannot see, and the case hw292 actually was (0.1.159 live).
//
// They must agree, because both encode the same fact: below this version a
// cutover can report success while the Sovereign is still tethered. Raising one
// alone would leave the other quietly permissive, which is how the source guard
// came to sit one version below its own documented guarantee in the first place.
func TestChartFloorMatchesTheSourceSideGuard(t *testing.T) {
	raw, err := os.ReadFile("../../../../../../scripts/check-cutover-version-floor.py")
	if err != nil {
		t.Skipf("source-side guard not readable from this working dir (%v)", err)
	}

	// Parse the marker the script maintains for exactly this purpose.
	const marker = "# FLOOR-LOCKSTEP:"
	var pyFloor string
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, marker); i >= 0 {
			pyFloor = strings.TrimSpace(line[i+len(marker):])
			break
		}
	}
	if pyFloor == "" {
		t.Fatalf("could not find %q in check-cutover-version-floor.py — the lockstep marker was "+
			"removed or reshaped, so the two floors can now diverge silently", marker)
	}
	if pyFloor != cutoverMinChartVersion {
		t.Errorf("source-side floor is %s but the runtime floor is %s.\n"+
			"These must move together: one guards what the repo ships, the other guards what a "+
			"Sovereign runs. Raise both in the same change.", pyFloor, cutoverMinChartVersion)
	}

	// Vacuity: the marker must carry a real version, not an empty or unparsed
	// value that happens to compare equal to a broken constant.
	if _, err := parseChartVersion(pyFloor); err != nil {
		t.Errorf("the source-side floor marker %q does not parse as a version: %v", pyFloor, err)
	}

	// And the value must actually be the one the script enforces, not a stale
	// comment left behind next to a changed FLOOR tuple.
	wantTuple := "FLOOR = (" + strings.ReplaceAll(pyFloor, ".", ", ") + ")"
	if !strings.Contains(string(raw), wantTuple) {
		t.Errorf("the lockstep marker says %s but the script does not contain %q — "+
			"the marker has drifted from the FLOOR it is supposed to mirror", pyFloor, wantTuple)
	}
}
