package bootstrapkit

// janitor_env_wiring_228_test.go — UAT row 228.
//
// THE DEFECT. `janitorDestructive()`
// (products/catalyst/bootstrap/api/internal/handler/janitor.go:135) reads
// CATALYST_JANITOR_DESTRUCTIVE, and six sweep sites in
// internal/providers/huawei/provider.go print
// "would-reap (log-only; set CATALYST_JANITOR_DESTRUCTIVE=true to act)".
// A repo-wide grep for that name across every .yaml/.tpl matched it ZERO
// times. The control that the scan was not blind: the same api-deployment.yaml
// carries 155 other CATALYST_ env entries.
//
// So the gate was written, shipped, deployed — and permanently shut. Row 228's
// "janitor log shows the orphaned VPC(s) swept" half could not be satisfied in
// ANY configuration the platform ships, because no configuration could reach
// the variable. This is truncation, not absence: the Go half is complete and
// running; the chart wiring was never authored.
//
// WHAT THIS GUARD PINS
//
//  1. The name reaches the rendered Pod at all — the literal absence that made
//     the row unsatisfiable.
//  2. It is emitted EVEN WHEN FALSE. Every other janitor knob is
//     omit-when-unset (absent env == Go default, one source of truth), but this
//     one arms deletion of live cloud infrastructure: an operator reading the
//     Pod must be able to tell "log-only" from "this chart cannot express it",
//     and an absent env says both at once.
//  3. The default is log-only. A guard that only proved the name appears would
//     pass just as well against a chart that shipped `true`.
//  4. The SAFETY INTERLOCK: arming it with no protect-list fails the render.
//
// VACUITY. Assertions 1-3 fail on the parent commit (no janitor block exists,
// so the env is absent and `find` returns ""). Assertion 4's negative arm —
// "destructive with a protect-list renders fine" — is the control for the
// interlock: without it, a template that failed on EVERY destructive render
// would pass the interlock test while breaking the feature outright.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderCatalystAPI renders products/catalyst/chart with the given --set args
// and returns the catalyst-api Deployment's container env as name -> value,
// plus the raw helm error (nil on success).
func renderCatalystAPI(t *testing.T, root string, sets ...string) (map[string]string, error) {
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
	// Only the catalyst-api Deployment is under test; rendering the whole
	// chart is fine but we filter to that one template to keep the parse
	// cheap and the failure messages specific.
	args = append(args, "-s", "templates/api-deployment.yaml")
	cmd := exec.Command(helmBin, args...)
	cmd.Dir = filepath.Join(root, "products", "catalyst", "chart")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &renderErr{msg: string(out)}
	}
	env := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var doc map[string]any
		if derr := dec.Decode(&doc); derr != nil {
			break
		}
		if doc == nil {
			continue
		}
		if k, _ := doc["kind"].(string); k != "Deployment" {
			continue
		}
		spec, _ := doc["spec"].(map[string]any)
		tmpl, _ := spec["template"].(map[string]any)
		pspec, _ := tmpl["spec"].(map[string]any)
		ctrs, _ := pspec["containers"].([]any)
		for _, c := range ctrs {
			cm, _ := c.(map[string]any)
			evs, _ := cm["env"].([]any)
			for _, e := range evs {
				em, _ := e.(map[string]any)
				name, _ := em["name"].(string)
				if name == "" {
					continue
				}
				if v, ok := em["value"]; ok {
					env[name] = strings.TrimSpace(toStr(v))
				} else {
					env[name] = "<valueFrom>"
				}
			}
		}
	}
	return env, nil
}

type renderErr struct{ msg string }

func (e *renderErr) Error() string { return e.msg }

func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func TestJanitorDestructive_ReachesThePod_228(t *testing.T) {
	root := repoRoot(t)

	env, err := renderCatalystAPI(t, root)
	if err != nil {
		t.Fatalf("default render failed: %v", err)
	}

	// The CONTROL first: this env block is large and full of CATALYST_ names,
	// so if the scan below finds nothing it must be because THIS name is
	// absent, not because the render or the parse came up empty.
	catalystCount := 0
	for name := range env {
		if strings.HasPrefix(name, "CATALYST_") {
			catalystCount++
		}
	}
	if catalystCount < 50 {
		t.Fatalf("only %d CATALYST_* env entries parsed out of the rendered Pod — the render/parse is broken, so any absence below would be an artefact of this harness rather than a finding", catalystCount)
	}

	got, ok := env["CATALYST_JANITOR_DESTRUCTIVE"]
	if !ok {
		t.Fatalf("CATALYST_JANITOR_DESTRUCTIVE is absent from the rendered catalyst-api Pod (%d CATALYST_* entries present, so the scan is not blind) — janitorDestructive() reads a variable no shipped configuration can set, and the janitor can never leave log-only mode (#4466, UAT row 228)", catalystCount)
	}
	// Emitted, and emitted SAFE.
	if got != "false" {
		t.Fatalf("CATALYST_JANITOR_DESTRUCTIVE renders %q by default, want \"false\" — the default must be log-only; #4466 made destructive sweeps opt-in after the b9f9590b self-reap took all 12 ECS nodes", got)
	}
}

func TestJanitorKnobs_AreOmittedUnlessSet_228(t *testing.T) {
	root := repoRoot(t)
	env, err := renderCatalystAPI(t, root)
	if err != nil {
		t.Fatalf("default render failed: %v", err)
	}
	// Everything EXCEPT the destructive arm stays absent by default, so the
	// Go code remains the single source of truth for those defaults. If the
	// chart emitted its own values here, a Go-side default change would
	// silently stop taking effect.
	for _, name := range []string{
		"CATALYST_JANITOR_ENABLED",
		"CATALYST_JANITOR_INTERVAL",
		"CATALYST_JANITOR_FAILED_MAX_AGE",
		"CATALYST_JANITOR_WIPED_MAX_AGE",
		"CATALYST_JANITOR_GHOST_MAX_AGE",
		"CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS",
	} {
		if v, ok := env[name]; ok {
			t.Errorf("%s is emitted by default as %q — it must be omitted when unset so internal/handler/janitor.go keeps owning the default", name, v)
		}
	}

	// …and each one DOES reach the Pod once set. Without this half the test
	// above would pass against a chart that dropped the knobs entirely.
	env2, err := renderCatalystAPI(t, root,
		"catalystApi.janitor.enabled=false",
		"catalystApi.janitor.interval=30m",
		"catalystApi.janitor.ghostMaxAge=48h",
	)
	if err != nil {
		t.Fatalf("render with janitor knobs set failed: %v", err)
	}
	for name, want := range map[string]string{
		"CATALYST_JANITOR_ENABLED":      "false",
		"CATALYST_JANITOR_INTERVAL":     "30m",
		"CATALYST_JANITOR_GHOST_MAX_AGE": "48h",
	} {
		if got := env2[name]; got != want {
			t.Errorf("%s = %q, want %q — the knob does not reach the Pod when set", name, got, want)
		}
	}
}

func TestJanitorDestructive_RequiresAProtectList_228(t *testing.T) {
	root := repoRoot(t)

	// Arming the deletes with NO protect-list must fail the render. janitor.go
	// describes activeDeploymentIds as the hard exclusion that survives a
	// regression in status inference; arming without it leaves the live
	// Sovereign's survival resting on inference alone.
	if _, err := renderCatalystAPI(t, root, "catalystApi.janitor.destructive=true"); err == nil {
		t.Fatalf("catalystApi.janitor.destructive=true rendered successfully with an EMPTY activeDeploymentIds — the chart will happily ship a Pod that deletes live cloud resources with no explicit protect-list (#4466)")
	} else if !strings.Contains(err.Error(), "activeDeploymentIds") {
		t.Fatalf("render failed, but not on the interlock: %v", err)
	}

	// THE CONTROL for the interlock. With a protect-list the render must
	// SUCCEED and the arm must actually be on. Without this arm, a template
	// that failed on every destructive render — i.e. one where the feature
	// does not work at all — would satisfy the assertion above.
	env, err := renderCatalystAPI(t, root,
		"catalystApi.janitor.destructive=true",
		"catalystApi.janitor.activeDeploymentIds=a0077ba47e3720e5",
	)
	if err != nil {
		t.Fatalf("destructive=true WITH a protect-list must render, got: %v", err)
	}
	if got := env["CATALYST_JANITOR_DESTRUCTIVE"]; got != "true" {
		t.Errorf("CATALYST_JANITOR_DESTRUCTIVE = %q with destructive=true, want \"true\"", got)
	}
	if got := env["CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS"]; got != "a0077ba47e3720e5" {
		t.Errorf("CATALYST_JANITOR_ACTIVE_DEPLOYMENT_IDS = %q, want the protect-list to reach the Pod", got)
	}
}
