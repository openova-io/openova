package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mainWorkflow is the frame of work-package.yaml on main before the repair
// (21 Nov 2026): the acceptance job has no OpenTofu, the precondition job no
// line of tools.
const mainWorkflow = `name: work-package

on:
  pull_request:
    branches: [main]

jobs:
  scope:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      # The tools an acceptance command may use: not this job.
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - name: Install PyYAML
        run: pip install --quiet pyyaml

  precondition:
    # In a merge group the commit a branch was cut from is no longer the base.
    if: github.event_name == 'pull_request'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - name: Install PyYAML
        run: pip install --quiet pyyaml
      - name: Precondition at the merge base
        run: |
          set -euo pipefail
          python3 "$GITHUB_WORKSPACE/scripts/plan.py" precondition

  acceptance:
    runs-on: ubuntu-latest
    timeout-minutes: 60
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      # The whole history is fetched, with every branch.
      # The tools an acceptance command may use: git, the shell, Python with PyYAML, Go, Node with npm, Helm.
      - uses: actions/setup-python@v5
        with:
          python-version: "3.12"
      - name: Install PyYAML
        run: pip install --quiet pyyaml
      # Go: the newest version a module of this repository asks for.
      - uses: actions/setup-go@v5
        with:
          go-version-file: products/catalyst/bootstrap/api/go.mod
          cache: false
      - uses: actions/setup-node@v7
        with:
          node-version: '22'
      # Helm: the version that blueprint-release.yaml pins.
      - uses: azure/setup-helm@v4
        with:
          version: v3.18.4
      - name: Acceptance commands of the package
        run: |
          set -euo pipefail
          python3 scripts/plan.py accept

  frozen-paths:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
`

const preconditionLine = "      - uses: actions/checkout@v4\n        with:\n          fetch-depth: 0\n      - uses: actions/setup-python@v5\n        with:\n          python-version: \"3.12\"\n      - name: Install PyYAML\n        run: pip install --quiet pyyaml\n      - name: Precondition"

const tofuSteps = `      - name: Install OpenTofu
        uses: opentofu/setup-opentofu@v1
        with:
          tofu_version: 1.11.6
      - name: OpenTofu version
        run: tofu version
      - name: Acceptance commands of the package`

// goodWorkflow is the repair: the precondition job names its tools, the
// acceptance job installs OpenTofu as the provider modules' tests do and names it.
var goodWorkflow = func() string {
	s := strings.Replace(mainWorkflow, preconditionLine,
		"      - uses: actions/checkout@v4\n        with:\n          fetch-depth: 0\n      # The tools a precondition command may use: git, the shell, Python with PyYAML.\n      - uses: actions/setup-python@v5\n        with:\n          python-version: \"3.12\"\n      - name: Install PyYAML\n        run: pip install --quiet pyyaml\n      - name: Precondition", 1)
	s = strings.Replace(s, "Go, Node with npm, Helm.", "Go, Node with npm, Helm, OpenTofu.", 1)
	return strings.Replace(s, "      - name: Acceptance commands of the package", tofuSteps, 1)
}()

func providerWorkflow(ref, version string) string {
	return `name: infra/x
on:
  pull_request:
jobs:
  validate-and-test:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: infra/providers/x
    steps:
      - name: Checkout
        uses: actions/checkout@v4

      - name: Install OpenTofu
        uses: opentofu/setup-opentofu@` + ref + `
        with:
          # lockstep with the sibling
          tofu_version: ` + version + `

      - name: tofu test
        run: tofu test
`
}

type tree map[string]string

func goodTree() tree {
	return tree{
		workflow: goodWorkflow,
		".github/workflows/infra-huawei-tofu.yaml":  providerWorkflow("v1", "1.11.6"),
		".github/workflows/infra-hetzner-tofu.yaml": providerWorkflow("v1", "1.11.6"),
	}
}

func (tr tree) with(path, content string) tree {
	out := tree{}
	for k, v := range tr {
		out[k] = v
	}
	if content == "" {
		delete(out, path)
	} else {
		out[path] = content
	}
	return out
}

func (tr tree) edit(from, to string) tree {
	return tr.with(workflow, strings.Replace(tr[workflow], from, to, 1))
}

func runOn(t *testing.T, tr tree) (int, string) {
	t.Helper()
	root := t.TempDir()
	for p, c := range tr {
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out, errw bytes.Buffer
	code := run([]string{"-root", root}, &out, &errw)
	return code, out.String() + errw.String()
}

func TestTheRepairPasses(t *testing.T) {
	code, out := runOn(t, goodTree())
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{"OK precondition", "OK acceptance", "OK tofu: job acceptance sets up opentofu/setup-opentofu@v1 1.11.6"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// The workflow of main on 21 Nov 2026: no OpenTofu, and no line for the precondition.
func TestMainOf21NovFails(t *testing.T) {
	code, out := runOn(t, goodTree().with(workflow, mainWorkflow))
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{"FAIL tofu " + workflow + ": job acceptance has 0 opentofu/setup-opentofu steps", "FAIL comment " + workflow + ": job precondition has 0 comment lines"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestEachRuleFails(t *testing.T) {
	cases := []struct {
		name string
		tr   tree
		want string
	}{
		{"the step is there, the line does not name it",
			goodTree().edit("Helm, OpenTofu.", "Helm."),
			"FAIL unnamed " + workflow + ":"},
		{"the line names it, no step installs it",
			goodTree().edit(tofuSteps, "      - name: Acceptance commands of the package"),
			"names OpenTofu, which no step of the job installs"},
		{"no step installs it: the tofu rule as well",
			goodTree().edit(tofuSteps, "      - name: Acceptance commands of the package"),
			"FAIL tofu " + workflow + ": job acceptance has 0"},
		{"a second version of OpenTofu",
			goodTree().edit("tofu_version: 1.11.6", "tofu_version: 1.8.5"),
			`with tofu_version "1.8.5"; the provider modules' tests use @v1 with 1.11.6`},
		{"a second way to install it",
			goodTree().edit("opentofu/setup-opentofu@v1", "opentofu/setup-opentofu@v2"),
			"sets up opentofu/setup-opentofu@v2"},
		{"twice",
			goodTree().edit("      - name: OpenTofu version", "      - uses: opentofu/setup-opentofu@v1\n        with:\n          tofu_version: 1.11.6\n      - name: OpenTofu version"),
			"job acceptance has 2 opentofu/setup-opentofu steps"},
		{"the provider modules' tests disagree",
			goodTree().with(".github/workflows/infra-hetzner-tofu.yaml", providerWorkflow("v1", "1.11.7")),
			"must set up OpenTofu one way, found 2"},
		{"no provider module's test sets up OpenTofu",
			goodTree().with(".github/workflows/infra-hetzner-tofu.yaml", "").with(".github/workflows/infra-huawei-tofu.yaml", ""),
			"must set up OpenTofu one way, found 0"},
		{"an action the guard cannot name",
			goodTree().edit("      - name: OpenTofu version", "      - uses: hashicorp/setup-terraform@v3\n      - name: OpenTofu version"),
			"FAIL unknown " + workflow + ":"},
		{"a Python package the guard cannot name",
			goodTree().edit("run: tofu version", "run: pip install requests==2.32"),
			"installs the Python package requests"},
		{"a tool installed by apt",
			goodTree().edit("run: tofu version", "run: sudo apt-get install -y jq"),
			`installs with "apt-get install"`},
		{"no line in the acceptance job",
			goodTree().edit("      # The tools an acceptance command may use: git,", "      # Tools: git,"),
			"job acceptance has 0 comment lines"},
		{"two lines in the acceptance job",
			goodTree().edit("      # The whole history", "      # The tools an acceptance command may use: git.\n      # The whole history"),
			"job acceptance has 2 comment lines"},
		{"a name twice",
			goodTree().edit("Helm, OpenTofu.", "Helm, Go, OpenTofu."),
			"names Go twice"},
		{"an empty name",
			goodTree().edit("Helm, OpenTofu.", "Helm, , OpenTofu."),
			"holds an empty name"},
		{"the precondition names a tool it does not install",
			goodTree().edit("may use: git, the shell, Python with PyYAML.", "may use: git, the shell, Python with PyYAML, Go."),
			"job precondition names Go, which no step"},
		{"the runner's git left out of the line",
			goodTree().edit("acceptance command may use: git, the shell,", "acceptance command may use: the shell,"),
			"job acceptance has git, which its line of tools does not name"},
		{"PyYAML named, never installed",
			goodTree().edit("      - name: Install PyYAML\n        run: pip install --quiet pyyaml\n      # Go:", "      # Go:"),
			"job acceptance names PyYAML, which no step"},
		{"Node with npm named, Node not installed",
			goodTree().edit("      - uses: actions/setup-node@v7\n        with:\n          node-version: '22'\n", ""),
			"job acceptance names npm, which no step"},
		{"no job acceptance",
			goodTree().edit("  acceptance:\n", "  accept:\n"),
			"FAIL comment " + workflow + ": no job acceptance"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.tr[workflow] == goodWorkflow && !strings.Contains(c.name, "provider") {
				t.Fatalf("the edit of case %q changed nothing", c.name)
			}
			code, out := runOn(t, c.tr)
			if code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", code, out)
			}
			if !strings.Contains(out, c.want) {
				t.Errorf("output lacks %q:\n%s", c.want, out)
			}
		})
	}
}

// A provider workflow that pins the action by digest, with the tag in a
// comment, is read the same way; the acceptance job must then use that digest.
func TestPinnedByDigest(t *testing.T) {
	digest := "9d84900f3238fab8cd84ce47d658d25dd008be2f # v1.0.8"
	tr := goodTree().
		with(".github/workflows/infra-huawei-tofu.yaml", providerWorkflow(digest, "1.11.6")).
		with(".github/workflows/infra-hetzner-tofu.yaml", providerWorkflow(digest, "'1.11.6'"))
	if code, out := runOn(t, tr); code != 1 || !strings.Contains(out, "the provider modules' tests use @9d84900f3238fab8cd84ce47d658d25dd008be2f with 1.11.6") {
		t.Fatalf("exit %d, want 1 naming the digest:\n%s", code, out)
	}
	tr = tr.edit("opentofu/setup-opentofu@v1", "opentofu/setup-opentofu@"+digest)
	if code, out := runOn(t, tr); code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

func TestUnreadableWorkflowStops(t *testing.T) {
	if code, out := runOn(t, goodTree().with(workflow, "")); code != 2 {
		t.Fatalf("exit %d, want 2:\n%s", code, out)
	}
}
