package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The commit step as it was on main before the repair (run 44821486879): three
// tries, the last retry commit never pushed, and no summary. Its re-apply is
// cut down to the three sites of the scratch tree.
const threeTries = `name: Blueprint Release
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: "Commit + push bootstrap-kit pin bump + blueprint.yaml lockstep"
        id: lockstep_push
        env:
          CHART_FILE: ${{ steps.bump_chart.outputs.chart_yaml }}
          CHART_NAME: ${{ steps.chart.outputs.name }}
          CHART_VERSION: ${{ steps.chart.outputs.version }}
          PIN_FILE: ${{ steps.bump_pin.outputs.pin_file }}
          BP_FILE: ${{ steps.bump_blueprint.outputs.bp_file }}
        run: |
          set -euo pipefail
          git config user.name "hatiyildiz"
          git config user.email "hatiyildiz@users.noreply.github.com"
          git add "$CHART_FILE" "$PIN_FILE" "$BP_FILE"
          git commit -q -m "deploy(${CHART_NAME}): bump bootstrap-kit pin -> ${CHART_VERSION} [skip ci]"

          for i in 1 2 3; do
            if git push origin HEAD:main; then
              exit 0
            fi
            echo "push attempt $i failed — re-fetching origin/main and re-applying lockstep"
            git fetch origin main
            git reset --hard origin/main
            sed -i -E "s|^version: .*\$|version: ${CHART_VERSION}|" "$CHART_FILE"
            sed -i -E "s|^      version: .*\$|      version: ${CHART_VERSION}|" "$PIN_FILE"
            sed -i -E "s|^  version: .*\$|  version: ${CHART_VERSION}|" "$BP_FILE"
            git add "$CHART_FILE" "$PIN_FILE" "$BP_FILE"
            git commit -q -m "deploy(${CHART_NAME}): lockstep -> ${CHART_VERSION} (retry $i) [skip ci]"
          done
          echo "::error title=Lockstep push failed::3 attempts exhausted for ${CHART_NAME}=${CHART_VERSION}."
          exit 1

      - name: Summary
        run: echo done
`

func runOn(t *testing.T, workflow string) (int, string) {
	t.Helper()
	var out, errw bytes.Buffer
	args := []string{"-scratch", filepath.Join(t.TempDir(), "scratch")}
	if workflow != "" {
		path := filepath.Join(t.TempDir(), "blueprint-release.yaml")
		if err := os.WriteFile(path, []byte(workflow), 0o600); err != nil {
			t.Fatal(err)
		}
		args = append(args, "-workflow", path)
	}
	code := run(args, &out, &errw)
	return code, out.String() + errw.String()
}

func realWorkflow(t *testing.T) string {
	t.Helper()
	root, err := git("", "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(strings.TrimSpace(root), ".github", "workflows", "blueprint-release.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The workflow of this tree: every case holds. On main before the repair it
// failed: case 2 lost bp-c4, bp-c5 and bp-c6, and case 3 gave up after three
// pushes with no summary.
func TestTheWorkflowHolds(t *testing.T) {
	code, out := runOn(t, "")
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{"ok   case 1", "ok   case 2", "ok   case 3", "0 finding(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
}

// Three tries lose every chart the origin refuses three times or more (and,
// with six jobs pushing at once, often others: the race of 20 Nov), and say
// nothing in the summary.
func TestThreeTriesLoseCharts(t *testing.T) {
	code, out := runOn(t, threeTries)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"case 2 (six charts, the k-th refused k-1 times): bp-c4 is LOST",
		"case 2 (six charts, the k-th refused k-1 times): bp-c5 is LOST",
		"case 2 (six charts, the k-th refused k-1 times): bp-c6 is LOST",
		"case 3 (one chart, every push refused): the step summary does not name bp-c1 1.0.1",
		"case 3 (one chart, every push refused): the step gave up after 3 push(es)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
}

// changeStepEnv edits the env line `CHART_PATH` of the step lockstep_push of
// the workflow of this tree (other steps carry the same line).
func changeStepEnv(t *testing.T, replace string) string {
	t.Helper()
	wf := realWorkflow(t)
	at := strings.Index(wf, "        id: lockstep_push\n")
	line := "          CHART_PATH: ${{ matrix.path }}\n"
	rel := strings.Index(wf[at+1:], line)
	next := strings.Index(wf[at+1:], "\n      - name:")
	if at < 0 || rel < 0 || rel > next {
		t.Fatalf("the step lockstep_push has no env line %q", line)
	}
	i := at + 1 + rel
	return wf[:i] + replace + wf[i+len(line):]
}

// A step whose env loses CHART_PATH runs without it, and its summary cannot
// name the release to start.
func TestAStepWithoutItsPathIsCaught(t *testing.T) {
	code, out := runOn(t, changeStepEnv(t, ""))
	if code != 1 || !strings.Contains(out, "the step summary does not name bp-c1 1.0.1 and the release to start again") {
		t.Fatalf("exit %d, want 1 and the summary finding:\n%s", code, out)
	}
}

// A key of the step's env that this command has no value for stops it.
func TestAnUnknownInputStops(t *testing.T) {
	code, out := runOn(t, changeStepEnv(t, "          CHART_PATH: ${{ matrix.path }}\n          NEW_INPUT: ${{ steps.x.outputs.y }}\n"))
	if code != 2 || !strings.Contains(out, "declares env NEW_INPUT") {
		t.Fatalf("exit %d, want 2 naming NEW_INPUT:\n%s", code, out)
	}
}

func TestExtractStep(t *testing.T) {
	const wf = `jobs:
  build:
    steps:
      - name: first
        id: one
        run: echo one
      - name: second
        id: lockstep_push
        # a comment
        if: always()
        env:
          A: ${{ matrix.path }}
          B: "x"
        run: |
          set -e
            indented

          echo "$A"

      # a comment between steps
      - name: third
        run: echo three
`
	st, err := extractStep(wf, "lockstep_push")
	if err != nil {
		t.Fatal(err)
	}
	if want := "set -e\n  indented\n\necho \"$A\"\n"; st.body != want {
		t.Errorf("body %q, want %q", st.body, want)
	}
	if strings.Join(st.env, ",") != "A,B" {
		t.Errorf("env keys %v, want [A B]", st.env)
	}
	for _, c := range []struct{ name, text, want string }{
		{"no such id", wf, "no step has the id nope"},
		{"two steps with the id", wf + "      - name: again\n        id: lockstep_push\n        run: |\n          true\n", "two steps have the id"},
		{"a one-line run", strings.Replace(wf, "        run: |\n          set -e", "        run: set -e\n        x: |\n          set -e", 1), "is not a block scalar"},
		{"an expression in the run", strings.Replace(wf, `echo "$A"`, `echo "${{ matrix.path }}"`, 1), "holds a ${{ }} expression"},
	} {
		id := "lockstep_push"
		if c.name == "no such id" {
			id = "nope"
		}
		if _, err := extractStep(c.text, id); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want one with %q", c.name, err, c.want)
		}
	}
}
