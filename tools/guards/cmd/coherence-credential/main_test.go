package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const cut = `name: cut
on: workflow_dispatch
jobs:
  cut:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Cut
        env:
          GH_TOKEN: ${{ github.token }}
          ORIGIN_USER: 'robot$openova-warmup'
          ORIGIN_TOKEN: ${{ secrets.BASTION_HARBOR_ROBOT_TOKEN }}
        run: |
          set -euo pipefail
          args=(cut)
          id="$(bash scripts/release-candidate.sh "${args[@]}")"
`

// The shape of foundation-gate.yaml before the repair: the verify step has
// the workflow token and nothing else.
const gateBefore = `name: gate
on:
  workflow_dispatch:
  workflow_call:
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: coherence self-test
        run: bash scripts/check-train-coherent.sh --self-test
      - name: release tool test
        run: bash scripts/test-release-candidate.sh
  candidate:
    runs-on: ubuntu-latest
    steps:
      - name: release-candidate.sh verify
        id: verify
        env:
          GH_TOKEN: ${{ github.token }}
          CANDIDATE_ID: ${{ inputs.candidate_id }}
        run: |
          set -euo pipefail
          # the coherence runs inside: bash scripts/check-train-coherent.sh
          commit="$(bash scripts/release-candidate.sh verify "$CANDIDATE_ID")"
`

const gateAfter = `name: gate
on:
  workflow_dispatch:
  workflow_call:
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - name: coherence self-test
        run: bash scripts/check-train-coherent.sh --self-test
  candidate:
    runs-on: ubuntu-latest
    steps:
      - name: release-candidate.sh verify
        id: verify
        env:
          GH_TOKEN: ${{ github.token }}
          ORIGIN_USER: 'robot$openova-warmup'
          ORIGIN_TOKEN: ${{ secrets.BASTION_HARBOR_ROBOT_TOKEN }}
          CANDIDATE_ID: ${{ inputs.candidate_id }}
        run: |
          set -euo pipefail
          commit="$(bash scripts/release-candidate.sh verify "$CANDIDATE_ID")"
`

// The credential at the level of the job (an indentless step list) counts;
// prune runs no coherence check.
const jobLevel = `on: workflow_dispatch
jobs:
  check:
    runs-on: ubuntu-latest
    env:
      ORIGIN_USER: robot$openova-warmup   # the warmup robot
      ORIGIN_TOKEN: "${{ secrets.BASTION_HARBOR_ROBOT_TOKEN }}"
    steps:
    - name: coherence
      run: bash ./scripts/check-train-coherent.sh
    - name: prune
      run: bash scripts/release-candidate.sh prune --dry-run
`

const literal = `on: workflow_dispatch
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - name: verify with a literal
        env:
          ORIGIN_USER: robot$openova-warmup
          ORIGIN_TOKEN: abcdefgh
        run: bash scripts/release-candidate.sh verify x
      - name: verify with an empty user
        env:
          ORIGIN_USER: ''
          ORIGIN_TOKEN: ${{ secrets.BASTION_HARBOR_ROBOT_TOKEN }}
        run: bash scripts/release-candidate.sh verify x
`

const otherRobot = `on: workflow_dispatch
jobs:
  verify:
    runs-on: ubuntu-latest
    steps:
      - name: verify as another robot
        env:
          ORIGIN_USER: robot$other
          ORIGIN_TOKEN: ${{ secrets.OTHER_ROBOT_TOKEN }}
        run: bash scripts/release-candidate.sh verify x
`

const callerOf = `on: workflow_dispatch
jobs:
  inherit:
    uses: ./.github/workflows/job-level.yml
    secrets: inherit
  named:
    uses: ./.github/workflows/job-level.yml
    secrets:
      bastion_harbor_robot_token: ${{ secrets.BASTION_HARBOR_ROBOT_TOKEN }}
  none:
    uses: ./.github/workflows/job-level.yml
    with:
      env: x
`

func TestRunOnATree(t *testing.T) {
	root := t.TempDir()
	put := func(p, text string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put(".github/workflows/cut.yaml", cut)
	put(".github/workflows/gate.yaml", gateBefore)
	put(".github/workflows/job-level.yml", jobLevel)
	put(".github/workflows/literal.yaml", literal)
	put(".github/workflows/other.yaml", otherRobot)
	put(".github/workflows/caller.yaml", callerOf)
	put(".github/actions/x/action.yml", gateBefore)
	put("scripts/notes.yaml", gateBefore)
	gitInit(t, root)

	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out.String(), errw.String())
	}
	for _, want := range []string{
		`OK .github/workflows/cut.yaml:16 job cut step "Cut": ORIGIN_USER robot$openova-warmup, ORIGIN_TOKEN from secrets.BASTION_HARBOR_ROBOT_TOKEN`,
		`OK .github/workflows/job-level.yml:10 job check step "coherence"`,
		`FAIL missing .github/workflows/gate.yaml:24 job candidate step "release-candidate.sh verify": ORIGIN_USER is not in the env`,
		`FAIL missing .github/workflows/gate.yaml:24 job candidate step "release-candidate.sh verify": ORIGIN_TOKEN is not in the env`,
		`FAIL literal .github/workflows/literal.yaml:10 job verify step "verify with a literal": ORIGIN_TOKEN is not one ${{ secrets.<NAME> }} (its value is not printed)`,
		`FAIL missing .github/workflows/literal.yaml:15 job verify step "verify with an empty user": ORIGIN_USER`,
		`FAIL differ ORIGIN_USER is not one value: robot$openova-warmup at .github/workflows/cut.yaml:16, .github/workflows/job-level.yml:10; robot$other at .github/workflows/other.yaml:10`,
		`FAIL differ the secret of ORIGIN_TOKEN is not one value: BASTION_HARBOR_ROBOT_TOKEN at`,
		`FAIL call .github/workflows/caller.yaml:10 job none calls .github/workflows/job-level.yml without secrets: inherit or BASTION_HARBOR_ROBOT_TOKEN`,
		"7 findings, 6 steps run the coherence check in 6 workflows read",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{"abcdefgh", "self-test", "release tool test", `"prune"`, "action.yml", "notes.yaml", "job inherit", "job named"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("%q is in the output:\n%s", unwanted, out.String())
		}
	}

	// The repair: the gate's step gets the cut's env; the bad shapes go.
	put(".github/workflows/gate.yaml", gateAfter)
	for _, p := range []string{"literal.yaml", "other.yaml"} {
		if err := os.Remove(filepath.Join(root, ".github/workflows", p)); err != nil {
			t.Fatal(err)
		}
	}
	put(".github/workflows/caller.yaml", strings.Replace(callerOf, "    with:\n      env: x\n", "    secrets: inherit\n", 1))
	out.Reset()
	errw.Reset()
	if code := run([]string{"--root", root}, &out, &errw); code != 0 {
		t.Fatalf("exit %d after the repair, want 0\n%s%s", code, out.String(), errw.String())
	}
	if want := "OK, 3 steps run the coherence check in 4 workflows read"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// Workflows that run no coherence check, and a tree without workflows, cannot
// be judged: the guard does not pass on what it did not measure.
func TestNothingMeasuredIsNotAPass(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"no caller":   {".github/workflows/a.yaml": strings.Replace(gateBefore, "release-candidate.sh verify \"$CANDIDATE_ID\"", "echo", 1)},
		"no workflow": {"README.md": gateBefore},
	} {
		root := t.TempDir()
		for p, text := range files {
			full := filepath.Join(root, p)
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		gitInit(t, root)
		var out, errw bytes.Buffer
		if code := run([]string{"--root", root}, &out, &errw); code != 2 {
			t.Errorf("%s: exit %d, want 2\n%s%s", name, code, out.String(), errw.String())
		}
	}
}

// The reader keeps the structure the guard depends on: jobs, their env and
// steps, block scalars with comment and blank lines and their line numbers,
// quoted and commented scalars, indentless sequences, a plain scalar that
// continues on the next line.
func TestParse(t *testing.T) {
	doc := parse(`# head
on:
  push:
env:
  A: "x # y"
  B: 'it''s'   # comment
  C: plain # comment
jobs:
  one:
    if: ${{ a == 'b' }}
    description: a plain scalar
      that continues
    steps:
    - uses: x
      with:
        k: v
    - name: two
      run: |
        # a comment line
        echo a

        echo b
    after: here
  two:
    steps:
      -
        name: three
        run: >-
          bash scripts/release-candidate.sh verify
`)
	env := envOf(doc.get("env"))
	for k, want := range map[string]string{"A": "x # y", "B": "it's", "C": "plain"} {
		if env[k].value != want {
			t.Errorf("env %s = %q, want %q", k, env[k].value, want)
		}
	}
	jobs := doc.get("jobs")
	if jobs == nil || len(jobs.children) != 2 {
		t.Fatalf("jobs: %+v", jobs)
	}
	one := jobs.children[0]
	if got := one.get("if").scalar(); got != "${{ a == 'b' }}" {
		t.Errorf("if = %q", got)
	}
	if got := one.get("after").scalar(); got != "here" {
		t.Errorf("after = %q (the entry after an indentless sequence)", got)
	}
	steps := one.get("steps")
	if steps == nil || len(steps.children) != 2 {
		t.Fatalf("steps of one: %+v", steps)
	}
	if got := steps.children[0].get("with").get("k").scalar(); got != "v" {
		t.Errorf("with.k = %q", got)
	}
	r := steps.children[1].get("run")
	if r == nil || len(r.block) < 4 || r.block[0].no != 19 || r.block[0].text != "# a comment line" || r.block[1].text != "echo a" {
		t.Fatalf("run block: %+v", r)
	}
	if line, ok := callsCoherence(r); ok {
		t.Errorf("echo a/b calls the coherence check at line %d", line)
	}
	three := jobs.children[1].get("steps").children[0]
	if got := three.get("name").scalar(); got != "three" {
		t.Errorf("name of three = %q", got)
	}
	if line, ok := callsCoherence(three.get("run")); !ok || line != 29 {
		t.Errorf("callsCoherence(three) = %d, %v; want 29, true", line, ok)
	}
}

// Which lines call the coherence check.
func TestCallsCoherence(t *testing.T) {
	for line, want := range map[string]bool{
		`bash scripts/release-candidate.sh verify "$ID"`:            true,
		`id="$(bash scripts/release-candidate.sh "${args[@]}")"`:    true,
		`bash "$GITHUB_WORKSPACE/scripts/release-candidate.sh" cut`: true,
		`(cd tree && bash scripts/check-train-coherent.sh)`:         true,
		`bash scripts/check-train-coherent.sh --no-ghcr`:            true,
		`bash scripts/check-train-coherent.sh --self-test`:          false,
		`bash scripts/release-candidate.sh prune`:                   false,
		`bash scripts/test-release-candidate.sh`:                    false,
		`# bash scripts/release-candidate.sh verify x`:              false,
		`python3 scripts/check-chart-content-after-version.py x`:    false,
	} {
		_, got := callsCoherence(&node{line: 1, value: line})
		if got != want {
			t.Errorf("callsCoherence(%q) = %v, want %v", line, got, want)
		}
	}
}
