package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runWith(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(append([]string{"-scratch", filepath.Join(t.TempDir(), "scratch")}, args...), &out, &errw)
	return code, out.String() + errw.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(root))
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(filepath.Join(repoRoot(t), rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// treeConsumers reads this repository's charts as the tree check does (copyCharts, changeChart,
// refusedBeyond) and returns its library charts in name order, each with the Blueprint folders
// that build it in, in the order the check prints them. No chart name of the tree is written in
// these tests: they hold for whatever charts build a library in today. It fails when no library
// is built in by any chart, so a test of the tree cannot pass on nothing.
func treeConsumers(t *testing.T) ([]string, map[string][]string) {
	t.Helper()
	root := repoRoot(t)
	cut := filepath.Join(root, cutRel)
	cp, err := copyCharts(options{root: root, repo: map[string]string{}}, cut, filepath.Join(t.TempDir(), "consumers"))
	if err != nil {
		t.Fatal(err)
	}
	of := map[string][]string{}
	built := 0
	for _, lib := range cp.libraries {
		head, err := changeChart(cp.tree, cp.base, lib)
		if err != nil {
			t.Fatal(err)
		}
		if of[lib], err = refusedBeyond(cut, cp.tree, head, cp.already); err != nil {
			t.Fatal(err)
		}
		built += len(of[lib])
	}
	if built == 0 {
		t.Fatalf("no chart of the tree builds one of the libraries [%s] in: the check of the tree judges nothing", strings.Join(cp.libraries, " "))
	}
	return cp.libraries, of
}

// workflowWith writes the repository's workflow with old replaced by new, once.
func workflowWith(t *testing.T, old, new string) string {
	t.Helper()
	text := repoFile(t, workflowRel)
	if strings.Count(text, old) != 1 {
		t.Fatalf("%s holds %q %d times, want once", workflowRel, old, strings.Count(text, old))
	}
	path := filepath.Join(t.TempDir(), "blueprint-release.yaml")
	if err := os.WriteFile(path, []byte(strings.Replace(text, old, new, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantAll(t *testing.T, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("the output names no %q:\n%s", w, out)
		}
	}
}

const (
	// The line of the step "Detect changed Blueprints" that computes the release set.
	setLine = `          changed="$(printf '%s\n' "$paths" | release_set)"`
	// That line as it was on main when SEC-219 and SEC-226 merged: the changed paths alone.
	setLineOnMain = `          changed=$(git diff --name-only "$BASE" "$AFTER" | grep -E '^(platform|products)/[^/]+/(chart/|blueprint\.yaml)' | awk -F/ '{print $1"/"$2}' | sort -u)`
)

// The repository's workflow holds every check, and each library of the tree is released with
// every chart that builds it in (the umbrella among them, SEC-216, while it builds one in).
func TestTheWorkflowHolds(t *testing.T) {
	t.Parallel()
	code, out := runWith(t)
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	wantAll(t, out,
		"ok   set: a change of the library a, which b, c, products/f and through b d build in: the release set is [platform/a platform/b platform/c platform/d products/f]",
		"ok   bump: the same push, for products/catalyst, which builds it in and whose build workflow does not run: products/catalyst 1.4.1, bumped=true defer=false",
		"ok   tree: a push that changes ", ", which nobody builds in, releases it alone",
	)
	libraries, of := treeConsumers(t)
	for _, lib := range libraries {
		wantAll(t, out, fmt.Sprintf("ok   tree: a push that changes %s releases it and the %d chart(s) that build it in, the charts the cut's check refuses until they are released again: %s\n",
			lib, len(of[lib]), strings.Join(of[lib], " ")))
	}
}

// The release set as it was on main: a change of a library releases the library alone.
func TestTheSetAsOnMainFails(t *testing.T) {
	t.Parallel()
	wf := workflowWith(t, setLine, setLineOnMain)
	code, out := runWith(t, "-checks", "set,tree", "-workflow", wf)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	wantAll(t, out,
		"FAIL set: a change of the library a, which b, c, products/f and through b d build in: the release set is [platform/a], want [platform/a platform/b platform/c platform/d products/f]",
		"FAIL set: a change of products/q/docs/chart, which products/q builds in: the release set is [], want [products/q]",
		"ok   set: a change of the chart e, which nobody builds in: the release set is [platform/e]",
	)
	libraries, of := treeConsumers(t)
	for _, lib := range libraries {
		if len(of[lib]) == 0 {
			continue
		}
		built := strings.Join(of[lib], " ")
		wantAll(t, out, fmt.Sprintf("FAIL tree: a push that changes %s/%s: the cut's check then refuses [%s], and the release set [%s] lacks [%s]\n",
			lib, probeRel, built, strings.TrimSuffix(lib, "/chart"), built))
	}
}

// A release set that holds every chart is a finding as well.
func TestAnOverreachingSetFails(t *testing.T) {
	t.Parallel()
	wf := workflowWith(t, setLine, `          changed="$(ls -d platform/*/chart products/*/chart | sed 's|/chart$||')"`)
	code, out := runWith(t, "-checks", "set,tree", "-workflow", wf)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	wantAll(t, out,
		"FAIL set: a change of the chart e, which nobody builds in: the release set is [platform/a platform/b",
		"/chart/templates/zz-release-set-probe.tpl, a chart nobody builds in: the release set is [",
	)
}

// The release writer as it was on main: only a change of the chart folder is a change of the chart.
func TestABumpThatIgnoresTheLibraryFails(t *testing.T) {
	t.Parallel()
	wf := workflowWith(t, `-- ${packaged} ${packaged_before})" ]; then`, `-- "${CHART_PATH}/chart")" ]; then`)
	code, out := runWith(t, "-checks", "bump", "-workflow", wf)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	wantAll(t, out,
		"FAIL bump: a push that changed only the library platform/c builds in: the release writer left platform/c 1.0.0 with bumped=false defer=false; want 1.0.1 with bumped=true defer=false",
		"FAIL bump: the same push, for products/catalyst, which builds it in and whose build workflow does not run: the release writer left products/catalyst 1.4.0",
		"ok   bump: the same push, for platform/e, which does not build it in: platform/e 2.0.0, bumped=false defer=false",
	)
}

// The umbrella deferred to its build workflow on every push, as on main.
func TestAnUmbrellaDeferredOnALibraryChangeFails(t *testing.T) {
	t.Parallel()
	wf := workflowWith(t, `                if [ -n "$(git diff --name-only "${PUSH_BEFORE}" "${PUSH_AFTER}" -- "${CHART_PATH}/chart")" ]; then`,
		`                if true; then`)
	code, out := runWith(t, "-checks", "bump", "-workflow", wf)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	wantAll(t, out,
		"FAIL bump: the same push, for products/catalyst, which builds it in and whose build workflow does not run: the release writer left products/catalyst 1.4.0 with bumped=false defer=true",
		"ok   bump: a push that changed only the library platform/c builds in: platform/c 1.0.1, bumped=true defer=false",
		"ok   bump: a push that changed the library and products/catalyst/chart, for products/catalyst: products/catalyst 1.4.0, bumped=false defer=true",
	)
}

// A step the command cannot find, or an expression it has no value for, stops it.
func TestAStepItCannotRunStops(t *testing.T) {
	t.Parallel()
	if code, out := runWith(t, "-checks", "set", "-workflow", workflowWith(t, "        id: changed\n", "        id: detect\n")); code != 2 {
		t.Errorf("no step with the id %s: exit %d, want 2:\n%s", detectID, code, out)
	}
	wf := workflowWith(t, "          BLUEPRINT: ${{ inputs.blueprint }}\n", "          BLUEPRINT: ${{ inputs.other }}\n")
	if code, out := runWith(t, "-checks", "set", "-workflow", wf); code != 2 || !strings.Contains(out, "${{ inputs.other }}, which this command has no value for") {
		t.Errorf("an expression without a value: exit %d, want 2 naming it:\n%s", code, out)
	}
}

func TestExtractStepReadsEnvAndRun(t *testing.T) {
	t.Parallel()
	text := "jobs:\n  a:\n    steps:\n      - name: x\n        id: one\n        env:\n          K: ${{ github.sha }}\n          Q: \"v\"\n        run: |\n          echo \"${{ inputs.tree }}\"\n\n          echo $K\n      - name: y\n        run: echo\n"
	st, err := extractStep(text, "one")
	if err != nil {
		t.Fatal(err)
	}
	body, env, err := st.script(map[string]string{"github.sha": "abc", "inputs.tree": "products"})
	if err != nil {
		t.Fatal(err)
	}
	if body != "echo \"products\"\n\necho $K\n" || strings.Join(env, ",") != "K=abc,Q=v" {
		t.Fatalf("body %q env %q", body, env)
	}
}
