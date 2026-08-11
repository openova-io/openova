package handler

import (
	"fmt"
	"strconv"
	"strings"
)

// ── Minimum cutover chart version (#5919) ───────────────────────────────────
//
// WHY A FLOOR EXISTS AT ALL
// ─────────────────────────
// hw292 reached `cutoverComplete=true` on chart 0.1.159 while 62 of its
// platform HelmRepositories were still pointing at `oci://ghcr.io/openova-io`
// (#5919). The sovereignty proof certified a Sovereign that was still tethered.
// That was not a flake: on 0.1.159 step-06's secondary leg read its pivot back
// ONCE, immediately after patching, and the owning Kustomization re-applied the
// pre-cutover Git artifact 55 seconds LATER. The leg measured the *patch* and
// called it the *pivot*.
//
// WHY 0.1.171 SPECIFICALLY
// ────────────────────────
// #5710 ("step-06 secondary leg must prove the pivot DURABLE, not merely
// applied") replaced that single-shot read-back with
// `assert_secondary_pivot_durable()`, which pokes the region's GitRepository +
// the HR-owning Kustomizations, refuses to certify until one of them reports
// `status.lastHandledReconcileAt == <token>` (proof the re-apply HAPPENED), and
// fails on ANY offender at ANY sample. It also closed two sibling no-op paths
// in the same leg — a failed HelmRepository enumeration that recorded `skipped`,
// and an unmounted-secondary-kubeconfig case indistinguishable from a genuine
// single-region no-op.
//
// #5710 merged as commit 881115109, whose Chart.yaml reads `version: 0.1.171`;
// its parent reads 0.1.170. So 0.1.171 is the FIRST chart version in which a
// completed cutover means the secondary pivot survived the actor that reverts
// it.
//
// AND WHY THE FLOOR IS 0.1.172, ONE HIGHER
// ────────────────────────────────────────
// A second defect of the same class sits just above it. Below 0.1.172 the
// cutover pivots the `vcluster-system/loft` chart source in the PRIMARY REGION
// ONLY (#5650), so a two-region Sovereign reaches cutoverComplete=true with
// region B still pointed at charts.loft.sh. Step-08's timed deny-egress hold
// cannot see it, because a dormant dependency is not exercised in the window.
// #5719 fixed that and merged as 901b3da22, whose Chart.yaml reads 0.1.172
// (parent 0.1.171).
//
// Two tethers, two versions; the floor is the LATER, because at 0.1.171 the
// loft tether is still there. Below this line `cutoverComplete=true` is not
// evidence of sovereignty and Pillar 5 cannot be claimed from it.
//
// `scripts/check-cutover-version-floor.py` carried 0.1.171 with the loft fix
// cited as its reason, which was one version below the guarantee it documented;
// that is corrected in the same change as this constant, and
// TestChartFloorMatchesTheSourceSideGuard keeps the two numbers in lockstep.
//
// RAISING OR LOWERING THIS NUMBER
// ───────────────────────────────
// The floor tracks exactly one thing: the oldest chart whose OWN assertions are
// strong enough that a green cutover is trustworthy. Raise it when a future
// change makes a previously-green outcome unprovable — and say which issue and
// which assert, as above. Never raise it merely to push Sovereigns forward;
// that is what the chart pin is for. Never lower it without showing that the
// weaker assert cannot certify a tethered Sovereign.
const (
	// cutoverMinChartVersion is the oldest bp-self-sovereign-cutover chart
	// permitted to RUN a cutover. See the block comment above for the
	// derivation. Bare "MAJOR.MINOR.PATCH" — no leading "v", no pre-release.
	cutoverMinChartVersion = "0.1.172"

	// cutoverChartNameForFloor is the chart name Helm embeds in the
	// `helm.sh/chart` label value, which is formatted "<name>-<version>".
	cutoverChartNameForFloor = "bp-self-sovereign-cutover"

	// cutoverChartLabel is the standard Helm label every resource this chart
	// emits already carries (chart templates/_helpers.tpl, "common labels").
	// Reusing it is deliberate: the version is ALREADY on the very step
	// ConfigMaps listCutoverSteps enumerates, so the floor needs no new
	// plumbing, no new RBAC and no new chart field that an old chart would
	// not have emitted anyway.
	cutoverChartLabel = "helm.sh/chart"
)

// chartVersionFromHelmLabel extracts the version out of a Helm `helm.sh/chart`
// label value of the form "<chartName>-<version>".
//
// It returns ok=false rather than a guess whenever it cannot prove the answer:
// an empty label, a label for a DIFFERENT chart, or a "<name>-" with nothing
// after it. A caller must treat !ok as "version unknown" and refuse — never as
// "probably fine". Reporting a verdict from absent evidence is the exact defect
// class this floor exists to catch.
func chartVersionFromHelmLabel(label string) (string, bool) {
	label = strings.TrimSpace(label)
	prefix := cutoverChartNameForFloor + "-"
	if !strings.HasPrefix(label, prefix) {
		return "", false
	}
	// Helm replaces "+" with "_" in this label; we only need the core version.
	v := strings.TrimSpace(strings.TrimPrefix(label, prefix))
	if v == "" {
		return "", false
	}
	return v, true
}

// parseChartVersion splits a bare "MAJOR.MINOR.PATCH" into three ints.
//
// Deliberately strict — this chart's versions are machine-generated by
// scripts/bump-chart-version.sh and have always been three numeric segments.
// A lexicographic string compare would rank "0.1.99" ABOVE "0.1.171", which is
// precisely the comparison that must not silently go wrong here, so anything
// that is not three integers is an error rather than a best effort.
func parseChartVersion(v string) ([3]int, error) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return out, fmt.Errorf("empty version")
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", v)
	}
	for i, p := range parts {
		if p == "" {
			return out, fmt.Errorf("version %q has an empty segment", v)
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, fmt.Errorf("version %q segment %q is not an integer", v, p)
		}
		if n < 0 {
			return out, fmt.Errorf("version %q segment %q is negative", v, p)
		}
		out[i] = n
	}
	return out, nil
}

// compareChartVersions returns -1 if a < b, 0 if equal, +1 if a > b.
func compareChartVersions(a, b string) (int, error) {
	av, err := parseChartVersion(a)
	if err != nil {
		return 0, err
	}
	bv, err := parseChartVersion(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		switch {
		case av[i] < bv[i]:
			return -1, nil
		case av[i] > bv[i]:
			return 1, nil
		}
	}
	return 0, nil
}

// cutoverChartFloorError is the typed refusal the HTTP layer maps to 412.
// It carries the version actually observed so the operator gets the real
// diagnosis (which chart is installed) and not just "too old".
type cutoverChartFloorError struct {
	// observed is the LOWEST version found across the steps that would run,
	// or "" when no step carried a readable version.
	observed string
	// floor is the minimum this build requires.
	floor string
	// detail explains WHICH condition failed, in operator language.
	detail string
}

func (e *cutoverChartFloorError) Error() string { return e.detail }

// assertCutoverChartFloor refuses to run a cutover on a chart older than
// cutoverMinChartVersion.
//
// It reads the MINIMUM version across every step that is about to run, not the
// first one and not a single sampled step. A Helm upgrade that partially fails,
// or a stale step ConfigMap left behind by an earlier release, can leave a mixed
// set — and the cutover executes ALL of them, so the WEAKEST step governs
// whether the outcome is provable. Step-06 is exactly the step whose assert the
// floor is about, so "some step is new enough" is not the question worth asking.
//
// Certification requires POSITIVE evidence. A step whose version cannot be read
// fails the gate rather than being skipped: an unreadable version is not a pass,
// and "no evidence of being old" is not evidence of being new.
func assertCutoverChartFloor(steps []cutoverStep) error {
	if len(steps) == 0 {
		// Caller handles the no-steps case (424) before reaching here; being
		// explicit keeps this function honest if it is ever called directly.
		return &cutoverChartFloorError{
			floor:  cutoverMinChartVersion,
			detail: "no cutover steps to version-check",
		}
	}

	var lowest string
	var lowestStep string
	for _, s := range steps {
		v := strings.TrimSpace(s.chartVersion)
		if v == "" {
			return &cutoverChartFloorError{
				floor: cutoverMinChartVersion,
				detail: fmt.Sprintf(
					"cutover step %q (ConfigMap %q) carries no readable %s version label, so this Sovereign's cutover chart version cannot be established; refusing to certify a cutover whose chart is unknown (minimum is %s, see #5919)",
					s.stepName, s.cmName, cutoverChartLabel, cutoverMinChartVersion,
				),
			}
		}
		if _, err := parseChartVersion(v); err != nil {
			return &cutoverChartFloorError{
				observed: v,
				floor:    cutoverMinChartVersion,
				detail: fmt.Sprintf(
					"cutover step %q (ConfigMap %q) reports chart version %q which cannot be parsed (%v); refusing to certify a cutover whose chart is unknown (minimum is %s, see #5919)",
					s.stepName, s.cmName, v, err, cutoverMinChartVersion,
				),
			}
		}
		if lowest == "" {
			lowest, lowestStep = v, s.stepName
			continue
		}
		cmp, err := compareChartVersions(v, lowest)
		if err != nil {
			return &cutoverChartFloorError{
				observed: v,
				floor:    cutoverMinChartVersion,
				detail:   fmt.Sprintf("comparing chart versions %q and %q: %v", v, lowest, err),
			}
		}
		if cmp < 0 {
			lowest, lowestStep = v, s.stepName
		}
	}

	cmp, err := compareChartVersions(lowest, cutoverMinChartVersion)
	if err != nil {
		return &cutoverChartFloorError{
			observed: lowest,
			floor:    cutoverMinChartVersion,
			detail:   fmt.Sprintf("comparing chart version %q against floor %q: %v", lowest, cutoverMinChartVersion, err),
		}
	}
	if cmp < 0 {
		return &cutoverChartFloorError{
			observed: lowest,
			floor:    cutoverMinChartVersion,
			detail: fmt.Sprintf(
				"bp-self-sovereign-cutover %s is installed (lowest across steps, at step %q) but this Sovereign requires at least %s to run a cutover. Below 0.1.171 step-06 reads its secondary pivot back only once and the owning Kustomization reverts it seconds later, so cutoverComplete=true can be reached with HelmRepositories still on ghcr.io (#5710; measured on hw292 chart 0.1.159, 62 of them). Below %s the loft chart source is pivoted in the primary region only, so region B stays on charts.loft.sh behind a green cutover (#5719, Refs #5650). Both are live tethers behind a completed cutover. Bump the bp-self-sovereign-cutover pin to %s or newer and re-trigger; the cutover reconciler retries on its own once the pin lands (#5919).",
				lowest, lowestStep, cutoverMinChartVersion, cutoverMinChartVersion, cutoverMinChartVersion,
			),
		}
	}
	return nil
}
