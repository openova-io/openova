package main

import (
	"bytes"
	"os"
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

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := git("", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Clean(filepath.Join(strings.TrimSpace(root), rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeTemp(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// needReleaseHelm skips a test that runs the release's dependency build when the
// helm on PATH is another version: `go test ./...` of the module runs in
// tree-guards before the Helm of the release is set up. The step of this guard
// sets RELEASE_OWN_TREE_HELM=required, and there such a test fails instead.
func needReleaseHelm(t *testing.T) {
	t.Helper()
	if err := helmIsTheRelease(repoFile(t, workflowRel)); err != nil {
		if os.Getenv("RELEASE_OWN_TREE_HELM") == "required" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
}

// The workflow, the charts and the chart tests of this tree hold.
func TestTheWorkflowHolds(t *testing.T) {
	needReleaseHelm(t)
	if code, out := runWith(t); code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

// The two steps as they were on main before the repair (run 44837071750): no
// repository is added, so the dependency build after the release writer of a
// dispatched run, and after any earlier build, meets Helm's refusal.
func TestTheStepsWithoutRepositoriesFail(t *testing.T) {
	needReleaseHelm(t)
	var kept []string
	for _, l := range strings.Split(repoFile(t, workflowRel), "\n") {
		if strings.Contains(l, "add_dep_repos") || strings.Contains(l, "helm-dep-repos.sh") {
			continue
		}
		kept = append(kept, l)
	}
	wf := writeTemp(t, "blueprint-release.yaml", strings.Join(kept, "\n"))
	code, out := runWith(t, "-checks", "workflow", "-workflow", wf)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"no repository definition for https://127.0.0.1",
		"a tree an earlier `helm dependency build` left its Chart.lock in: the step",
		"after the release writer: the step",
		"before it sources " + libRel,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output names no %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "ok   workflow: a tree without a Chart.lock (a push run)") {
		t.Errorf("the push route must still pass without a lock:\n%s", out)
	}
}

// step6490 as it was on main: it compares with main's tip.
func TestTheChartTestOnMainsTipFails(t *testing.T) {
	text := repoFile(t, cutoverChart+"/"+cutoverTest)
	const fixed = `BASE_TREE="$(git -C "${REPO_ROOT}" merge-base HEAD origin/main 2>/dev/null || true)"`
	if !strings.Contains(text, fixed) {
		t.Fatalf("%s holds no line %s", cutoverTest, fixed)
	}
	test := writeTemp(t, "step6490.sh", strings.Replace(text, fixed, `BASE_TREE=origin/main`, 1))
	code, out := runWith(t, "-checks", "charttests", "-chart-test", test)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{
		"FAIL charttests: a release run of X after main moved on",
		"FAIL charttests: a pull request's merge ref after main moved on",
		"ok   charttests: a pull request whose own change alters the single-region render",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output names no %q:\n%s", want, out)
		}
	}
}

// A chart test whose comparison cannot fail, or that skips it, is a finding.
func TestAComparisonThatCannotFailIsAFinding(t *testing.T) {
	test := writeTemp(t, "step6490.sh", "#!/usr/bin/env bash\necho \"  ok   — "+comparedLine+" to the main this tree left\"\n")
	code, out := runWith(t, "-checks", "charttests", "-chart-test", test)
	if code != 1 || !strings.Contains(out, "its comparison cannot fail") {
		t.Fatalf("exit %d, want 1 with a finding of the own change:\n%s", code, out)
	}
	test = writeTemp(t, "step6490.sh", "#!/usr/bin/env bash\necho skip\n")
	code, out = runWith(t, "-checks", "charttests", "-chart-test", test)
	if code != 1 || !strings.Contains(out, "passed without making its comparison") {
		t.Fatalf("exit %d, want 1 with a finding of the skipped comparison:\n%s", code, out)
	}
}

func TestATreeReadAtMainsTipIsAFinding(t *testing.T) {
	cases := map[string]bool{
		`  git -C "${REPO_ROOT}" archive origin/main platform/x/chart | tar -x -C "${TMP}"`: true,
		`git show origin/main:platform/x/chart/values.yaml > "$t"`:                          true,
		`out="$(git diff origin/main -- platform/x)"`:                                       true,
		`git -C "${REPO_ROOT}" archive "$(git merge-base HEAD origin/main)" platform/x`:     false,
		`if git -C "${REPO_ROOT}" rev-parse --verify --quiet origin/main; then`:             false,
		`# git archive origin/main is what the old test did`:                                false,
		`echo "compare with origin/main"`:                                                   false,
	}
	for line, want := range cases {
		if got := len(tipReads("t.sh", line+"\n")) > 0; got != want {
			t.Errorf("%q: finding %v, want %v", line, got, want)
		}
	}
}

// A repository the library parses wrong, and one given by name, are findings.
func TestTheWalkCanFail(t *testing.T) {
	root := t.TempDir()
	lib := repoFile(t, libRel)
	files := map[string]string{
		libRel: lib,
		"platform/a/chart/Chart.yaml": "apiVersion: v2\nname: a\nversion: 1.0.0\ndependencies:\n" +
			"  - name: up\n    version: 1.0.0\n    repository: \"https://example.invalid/charts\" # the upstream\n",
		"platform/b/chart/Chart.yaml": "apiVersion: v2\nname: b\nversion: 1.0.0\ndependencies:\n" +
			"  - name: up\n    version: 1.0.0\n    repository: \"@bitnami\"\n",
		"platform/c/chart/Chart.yaml": "apiVersion: v2\nname: c\nversion: 1.0.0\ndependencies:\n" +
			"  - name: up\n    version: 1.0.0\n    repository: https://example.invalid/other/\n",
	}
	if err := writeFiles(root, files); err != nil {
		t.Fatal(err)
	}
	if _, err := git(root, baseEnv(), "init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	findings, err := checkCoverage(root, filepath.Join(t.TempDir(), "coverage"), &out)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	if len(findings) != 2 || !strings.Contains(joined, "platform/a/chart: Helm lists the dependency repository https://example.invalid/charts") ||
		!strings.Contains(joined, `platform/b/chart: a dependency names the repository "@bitnami"`) {
		t.Fatalf("want the findings of a and b only, got:\n%s\n%s", joined, out.String())
	}
}

func TestASecondDefinitionIsAFinding(t *testing.T) {
	root := t.TempDir()
	lib := repoFile(t, libRel)
	if err := writeFiles(root, map[string]string{libRel: lib}); err != nil {
		t.Fatal(err)
	}
	if _, err := git(root, baseEnv(), "init", "-q", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if f, err := oneDefinition(root); err != nil || len(f) != 0 {
		t.Fatalf("one definition: findings %v, error %v", f, err)
	}
	if err := writeFiles(root, map[string]string{"scripts/copy.sh": "add_dep_repos() { :; }\n"}); err != nil {
		t.Fatal(err)
	}
	if f, err := oneDefinition(root); err != nil || len(f) != 1 || !strings.Contains(f[0], "scripts/copy.sh:1") {
		t.Fatalf("a copy: findings %v, error %v", f, err)
	}
}
