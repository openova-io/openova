package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree writes files (repository path -> content) under a new root.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// verdict runs Check and returns "<rule> <unit>" of each finding and the units.
func verdict(t *testing.T, files map[string]string, exempt map[string]string) ([]string, []Unit) {
	t.Helper()
	units, findings, err := Check(tree(t, files), exempt)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.Rule+" "+f.Unit)
	}
	return got, units
}

func equal(a, b []string) bool { return strings.Join(a, "\n") == strings.Join(b, "\n") }

const builtTest = `#!/usr/bin/env bash
set -euo pipefail
CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm dependency build --skip-refresh "$CHART_DIR" > /dev/null
cd "$CHART_DIR"
helm template smoke . --show-only templates/a.yaml > /dev/null
`

func TestU1ChartTestOfTheUmbrella(t *testing.T) {
	unbuilt := strings.Replace(builtTest, "helm dependency build --skip-refresh \"$CHART_DIR\" > /dev/null\n", "", 1)
	for _, c := range []struct {
		name, body string
		want       []string
	}{
		{"builds before it renders", builtTest, nil},
		{"renders and builds nothing", unbuilt, []string{"U1 products/catalyst/chart/tests/a.sh"}},
		{"the build is only a comment", strings.Replace(builtTest, "helm dependency build", "# helm dependency build", 1), []string{"U1 products/catalyst/chart/tests/a.sh"}},
		{"the build is only a message", strings.Replace(builtTest, "helm dependency build --skip-refresh \"$CHART_DIR\" > /dev/null", `echo "run helm dependency build first"`, 1), []string{"U1 products/catalyst/chart/tests/a.sh"}},
		{"a renderer through a variable of the helm binary", strings.Replace(unbuilt, "helm template smoke", `"$helm" template smoke`, 1), []string{"U1 products/catalyst/chart/tests/a.sh"}},
	} {
		got, _ := verdict(t, map[string]string{"products/catalyst/chart/tests/a.sh": c.body}, nil)
		if !equal(got, c.want) {
			t.Errorf("%s: findings %q, want %q", c.name, got, c.want)
		}
	}
}

func TestU1ScriptsNamingTheChartFolder(t *testing.T) {
	for _, c := range []struct {
		name, path, body string
		want             []string
	}{
		{"a shell renderer without a build", "scripts/a.sh",
			"C=\"$ROOT/products/catalyst/chart\"\nhelm template x \"$C\" > out.yaml\n", []string{"U1 scripts/a.sh"}},
		{"a shell renderer with a build", "scripts/a.sh",
			"C=\"$ROOT/products/catalyst/chart\"\nhelm dependency build --skip-refresh \"$C\" > /dev/null\nhelm template x \"$C\" > out.yaml\n", nil},
		{"a file below the chart is not the chart", "scripts/a.sh",
			"V=products/catalyst/chart/values.yaml\nhelm template x platform/other/chart -f \"$V\"\n", nil},
		{"the chart named only in a comment", "scripts/a.sh",
			"# renders like products/catalyst/chart does\nhelm template x platform/other/chart\n", nil},
		{"helm package of the chart", "scripts/a.sh",
			"helm package products/catalyst/chart --destination out\n", []string{"U1 scripts/a.sh"}},
		{"a Python argument list without a build", "scripts/a.py",
			"CHART = \"products/catalyst/chart\"\ncmd = [\"helm\", \"template\", \"x\", CHART]\n", []string{"U1 scripts/a.py"}},
		{"a Python argument list with --dependency-update", "scripts/a.py",
			"CHART = \"products/catalyst/chart\"\ncmd = [\"helm\", \"template\", \"x\", CHART, \"--dependency-update\"]\n", nil},
	} {
		got, _ := verdict(t, map[string]string{c.path: c.body}, nil)
		if !equal(got, c.want) {
			t.Errorf("%s: findings %q, want %q", c.name, got, c.want)
		}
	}
}

const goRender = `package kit

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRender(t *testing.T) {
	dir := filepath.Join(root, "products", "catalyst", "chart")
	BUILD
	cmd := exec.Command(helmBin, "template", "x", ".")
	cmd.Dir = dir
}
`

const goHelper = `package kit

func ensureDeps(t *testing.T, helmBin, dir string) {
	if out, err := exec.Command(helmBin, "dependency", "build", dir).CombinedOutput(); err != nil {
		t.Fatalf("helm dependency build %s: %v\n%s", dir, err, out)
	}
}

// buildAll builds through ensureDeps: a builder too.
func buildAll(t *testing.T, helmBin string, dirs ...string) {
	for _, d := range dirs {
		ensureDeps(t, helmBin, d)
	}
}

func unrelated(dir string) {
	t.Fatalf("helm dependency build %s", dir)
}
`

func TestU1GoThroughAHelperOfThePackage(t *testing.T) {
	for _, c := range []struct {
		name, build string
		want        []string
	}{
		{"renders and builds nothing", "", []string{"U1 tests/e2e/kit/render_test.go"}},
		{"calls a helper of another file that builds", "ensureDeps(t, helmBin, dir)", nil},
		{"calls a helper that builds through another", "buildAll(t, helmBin, dir)", nil},
		{"calls a helper whose build is only a message", "unrelated(dir)", []string{"U1 tests/e2e/kit/render_test.go"}},
		{"builds in its own argument list", `exec.Command(helmBin, "dependency", "build", dir)`, nil},
		{"the argument list is a []string", "", []string{"U1 tests/e2e/kit/render_test.go"}},
	} {
		body := strings.Replace(goRender, "BUILD", c.build, 1)
		if c.name == "the argument list is a []string" {
			body = strings.Replace(body, `exec.Command(helmBin, "template", "x", ".")`, "exec.Command(helmBin, append([]string{\"template\"}, args...)...)", 1)
		}
		got, _ := verdict(t, map[string]string{"tests/e2e/kit/render_test.go": body, "tests/e2e/kit/helpers_test.go": goHelper}, nil)
		if !equal(got, c.want) {
			t.Errorf("%s: findings %q, want %q", c.name, got, c.want)
		}
	}
}

const workflow = `name: w
on:
  pull_request:
    paths:
      - 'products/catalyst/chart/**'
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: Helm dependency build
        run: helm dependency build --skip-refresh products/catalyst/chart
  render:
    runs-on: ubuntu-latest
    steps:
      - name: Render
        run: |
          set -euo pipefail
          BUILD
          helm template x products/catalyst/chart > seed.yaml
`

func TestU1WorkflowJobsAreJudgedAlone(t *testing.T) {
	for _, c := range []struct {
		name, build string
		want        []string
	}{
		{"a build in another job does not count", "", []string{"U1 .github/workflows/w.yaml job render"}},
		{"a build in the job counts", "helm dependency build --skip-refresh products/catalyst/chart > /dev/null", nil},
	} {
		got, units := verdict(t, map[string]string{".github/workflows/w.yaml": strings.Replace(workflow, "BUILD", c.build, 1)}, nil)
		if !equal(got, c.want) {
			t.Errorf("%s: findings %q, want %q", c.name, got, c.want)
		}
		// The job build renders nothing, so only the job render is a renderer.
		if len(units) != 1 || units[0].Job != "render" || units[0].Render != 19 {
			t.Errorf("%s: units %+v, want the one job render with its render on line 19", c.name, units)
		}
	}
	// A workflow whose jobs are not indented by two spaces is judged as one unit, not skipped.
	four := strings.NewReplacer("\n  build:", "\n    build:", "\n  render:", "\n    render:").Replace(strings.Replace(workflow, "BUILD", "", 1))
	got, units := verdict(t, map[string]string{".github/workflows/w.yaml": four}, nil)
	if len(got) != 0 || len(units) != 1 || units[0].Job != "" || units[0].Build != 11 {
		t.Errorf("four-space jobs: findings %q, units %+v; want one unit of the whole jobs block, built on line 11", got, units)
	}
}

func TestU2Exemptions(t *testing.T) {
	const reason = "renders a fixture chart"
	unbuilt := "C=products/catalyst/chart\nhelm template x \"$C\"\n"
	built := "C=products/catalyst/chart\nhelm dep build \"$C\"\nhelm template x \"$C\"\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"an exempt renderer without a build", map[string]string{"scripts/a.sh": unbuilt}, nil},
		{"an exempt renderer that builds", map[string]string{"scripts/a.sh": built}, []string{"U2 scripts/a.sh"}},
		{"an exempt file that no longer renders the chart", map[string]string{"scripts/a.sh": "helm template x platform/other/chart\n"}, []string{"U2 scripts/a.sh"}},
		{"an exempt file that does not exist", map[string]string{"scripts/b.sh": built}, []string{"U2 scripts/a.sh"}},
	} {
		got, _ := verdict(t, c.files, map[string]string{"scripts/a.sh": reason})
		if !equal(got, c.want) {
			t.Errorf("%s: findings %q, want %q", c.name, got, c.want)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		code  int
		line  string
	}{
		{"every renderer builds", map[string]string{"products/catalyst/chart/tests/a.sh": builtTest}, 0, "umbrella-deps: PASS — 1 renderer(s)"},
		{"a renderer does not build", map[string]string{"scripts/a.sh": "helm template x products/catalyst/chart\n"}, 1, "umbrella-deps: FAIL U1 scripts/a.sh: renders products/catalyst/chart (line 1)"},
		{"U3: nothing renders the chart", map[string]string{"scripts/a.sh": "helm template x platform/other/chart\n"}, 2, "umbrella-deps: U3 no unit renders and names products/catalyst/chart"},
	} {
		root := tree(t, c.files)
		var out, errb bytes.Buffer
		// The real exemption is absent from these trees; run judges with none.
		saved := Exemptions
		Exemptions = map[string]string{}
		code := run([]string{"--root", root}, &out, &errb)
		Exemptions = saved
		if code != c.code || !strings.Contains(out.String(), c.line) {
			t.Errorf("%s: exit %d, want %d; output:\n%s%s", c.name, code, c.code, out.String(), errb.String())
		}
	}
}

// The patterns read the shapes the tree's renderers use (measured 27 November 2026).
func TestPatterns(t *testing.T) {
	renders := []string{
		`helm template x products/catalyst/chart --set a=b \`,
		`( cd "${REPO_ROOT}/products/catalyst/chart" && "$helm" template . --show-only t.yaml ) > out`,
		`    "${HELM}" template pins "${CHART_DIR}" 2>/dev/null`,
		`rendered="$(timeout "${HELM_TIMEOUT}" helm template "$(basename "${chart}")" "${chart}" 2>/dev/null || true)"`,
		`	cmd := exec.Command(helmBin, "template", ".", "--show-only", "t.yaml")`,
		`	args := []string{"template", "catalyst", "."}`,
		`        r = subprocess.run([os.environ.get("HELM_BIN", "helm"), "template", "x", "."],`,
		`helm upgrade --install x products/catalyst/chart`,
	}
	for _, l := range renders {
		if !code(l) || !rendersLine(l) {
			t.Errorf("not read as a render: %s", l)
		}
	}
	notRenders := []string{
		`	annot, _, _ := unstructured.NestedStringMap(got.Object, "spec", "template", "metadata", "annotations")`,
		`log "step 0/6 — rendering chart template via 'helm template --show-only'"`,
		`	t.Fatalf("helm template %s: %v", dir, err)`,
		`# helm template x products/catalyst/chart`,
		`helm show values products/catalyst/chart | grep template`,
	}
	for _, l := range notRenders {
		if code(l) && rendersLine(l) {
			t.Errorf("read as a render: %s", l)
		}
	}
	for _, l := range []string{`helm dependency build "$c" || exit 1`, `(cd "$chart" && helm dep update)`, `	exec.Command(helmBin, "dependency", "build", dir)`, `x --dependency-update`} {
		if !code(l) || !buildsLine(l) {
			t.Errorf("not read as a build: %s", l)
		}
	}
	for _, l := range []string{`      - name: Helm dependency build`, `echo "helm dependency build failed"`, `helm dependency list "$c"`} {
		if code(l) && buildsLine(l) {
			t.Errorf("read as a build: %s", l)
		}
	}
	for _, l := range []string{`CHART_DIR="${REPO_ROOT}/products/catalyst/chart"`, `cd products/catalyst/chart && x`, `	chartDir := filepath.Join(root, "products", "catalyst", "chart")`} {
		if !names.MatchString(l) {
			t.Errorf("not read as the chart folder: %s", l)
		}
	}
	for _, l := range []string{`V=products/catalyst/chart/values.yaml`, `      - 'products/catalyst/chart/**'`, `	p := filepath.Join(root, "products", "catalyst", "chart", "values.yaml")`, `products/catalyst/charts`} {
		if names.MatchString(l) {
			t.Errorf("read as the chart folder: %s", l)
		}
	}
}
