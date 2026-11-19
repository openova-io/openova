package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// Every rule is shown to fail on a Git repository built here. No state of this repository is read.

const (
	template  = "platform/app/chart/templates/job.yaml"
	configMap = "platform/app/chart/templates/configmap.yaml"
	tool      = "scripts/tool.py"
	shellTool = "scripts/check.sh"
	cloudInit = "infra/providers/x/cloudinit-worker.tftpl"
	nodeTF    = "infra/providers/x/main.tf"
	image     = "products/img/Containerfile"
	ciFlow    = ".github/workflows/ci.yaml"
	pyStep    = ".github/workflows/py.yaml"
)

func fixture() map[string]string {
	return map[string]string{
		tool:                                     "#!/usr/bin/env python3\nprint(1)\nprint(2)\n",
		"scripts/lib/helper.py":                  "x = 1\n",
		"platform/app/chart/Chart.yaml":          "apiVersion: v2\nname: app\nversion: 0.1.0\n",
		"platform/app/chart/values.yaml":         "image: python:3.12\ncommand: python3 -c 'print(1)'\n",
		"platform/app/chart/tests/check.py":      "import sys\n",
		"platform/app/chart/files/run.py":        "#!/usr/bin/env python3\nimport subprocess\nsubprocess.run([\"python3\", \"-V\"])\n",
		"platform/app/chart/templates/NOTES.txt": "python3 is used by the Job\n",
		template: "kind: Job\nspec:\n  template:\n    spec:\n      containers:\n        - name: run\n" +
			"          # python3 runs the script of files/\n          command:\n            - /usr/bin/python3\n            - /scripts/run.py\n",
		cloudInit: "runcmd:\n  - python3 -c 'print(1)'\n  ${indent(2, put_block)}\n",
		nodeTF: "locals {\n  put_block = <<-PUT\n- |\n  curl -s https://example.invalid/healthz\nPUT\n" +
			"  user_data = templatefile(\"${path.module}/cloudinit-worker.tftpl\", {\n    put_block = local.put_block\n  })\n}\n",
		image:            "FROM alpine:3.20\nRUN apk add --no-cache bash python3\n",
		"core/x/main.go": "package main\n\nfunc main() {}\n",
		"docs/README.md": "Run python3 scripts/tool.py.\n",
		shellTool:        "#!/usr/bin/env bash\nset -eu\nexec python3 - \"$@\" <<'PY'\nimport sys\nprint(sys.argv)\nPY\n",
		configMap:        "kind: ConfigMap\ndata:\n  reload.py: |\n    import os\n    print(os.environ)\n",
		ciFlow:           "jobs:\n  a:\n    steps:\n      - run: python3 -c 'import json; print(1)'\n",
		pyStep: "jobs:\n  a:\n    runs-on: ubuntu-latest\n    steps:\n      - name: report\n        shell: python\n" +
			"        run: |\n          import os\n          print(os.environ[\"GITHUB_SHA\"])\n",
	}
}

// The fixture holds 4 Python files of 3 + 1 + 1 + 3 lines. Python inside other files: the
// here-document of check.sh (2), reload.py of the ConfigMap (2), the run of the shell: python step
// (2), and one python -c line in each of the workflow, the cloud-init template and values.yaml.
// The lines that run python: values.yaml, job.yaml, the cloud-init template, the Containerfile,
// check.sh, ci.yaml and the shell: python line of py.yaml. The places: the #! of files/run.py,
// python3 in job.yaml and in the cloud-init template, the apk add of the Containerfile.
const fixtureSummary = "python_files=4 python_file_lines=8 embedded_lines=9 invoke_lines=7 sovereign_places=4 (install=1 invoke=2 shebang=1)"

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "-c", "user.name=guard-test", "-c", "user.email=guard-test@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-q", "--allow-empty", "-m", "fixture")
}

// move renames a file with git mv, into a folder that may not exist yet.
func move(t *testing.T, root, from, to string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(filepath.FromSlash(to))), 0o750); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "mv", from, to)
}

func put(t *testing.T, root, p, text string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func get(t *testing.T, root, p string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(root), p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func remove(t *testing.T, root, p string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(p))); err != nil {
		t.Fatal(err)
	}
}

func guardOn(root string, args ...string) (int, string) {
	var out bytes.Buffer
	code := run(append([]string{"--root", root}, args...), &out)
	return code, out.String()
}

// seeded is the fixture with its baseline written by the guard, as a first write.
func seeded(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	for p, text := range fixture() {
		put(t, dir, p, text)
	}
	if code, out := guardOn(dir, "--write-baseline"); code != 0 || !strings.Contains(out, fixtureSummary) {
		t.Fatalf("the first write of the fixture baseline: exit %d\n%s", code, out)
	}
	return dir
}

func editBaseline(t *testing.T, root string, change func(c *counts)) {
	t.Helper()
	c, err := parseBaseline([]byte(get(t, root, baselinePath)), baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	change(&c)
	put(t, root, baselinePath, string(encodeBaseline(c)))
}

func readBaseline(t *testing.T, root string) counts {
	t.Helper()
	c, err := parseBaseline([]byte(get(t, root, baselinePath)), baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func want(t *testing.T, code int, out string, wantCode int, needles ...string) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("exit %d, want %d\n%s", code, wantCode, out)
	}
	for _, n := range needles {
		if !strings.Contains(out, n) {
			t.Fatalf("the output does not hold %q\n%s", n, out)
		}
	}
}

// failLines returns the FAIL lines of the output and the lines that list a file under them.
func failLines(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, me+": FAIL ") || strings.HasPrefix(l, me+":   ") {
			got = append(got, strings.TrimPrefix(l, me+": "))
		}
	}
	return got
}

func wantFails(t *testing.T, code int, out string, lines ...string) {
	t.Helper()
	if code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, out)
	}
	if got := failLines(out); !reflect.DeepEqual(got, lines) {
		t.Fatalf("the failures are\n%s\nwant\n%s\n\n%s", strings.Join(got, "\n"), strings.Join(lines, "\n"), out)
	}
	if !strings.Contains(out, goLine) {
		t.Fatalf("the output does not end with the rule\n%s", out)
	}
}

func TestUnchangedTreePasses(t *testing.T) {
	dir := seeded(t)
	code, out := guardOn(dir)
	want(t, code, out, 0, fixtureSummary)
	if strings.Contains(out, "FAIL") || strings.Contains(out, "lower than") {
		t.Fatalf("an unchanged tree has nothing to say\n%s", out)
	}
}

// A rise of each total fails, names the total and by how much, and with --base lists the file.
func TestRiseOfEachTotalFails(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, dir string)
		fail   string
		grew   string
	}{
		{"python_files: a new empty .py file", func(t *testing.T, dir string) { put(t, dir, "scripts/empty.py", "") },
			"FAIL python_files is 5, 1 more than the baseline 4", "scripts/empty.py grew from 0 to 1"},
		{"python_file_lines: one more line in a .py file", func(t *testing.T, dir string) { put(t, dir, tool, get(t, dir, tool)+"print(3)\n") },
			"FAIL python_file_lines is 9, 1 more than the baseline 8", tool + " grew from 3 to 4"},
		{"embedded_lines: one more line in a here-document", func(t *testing.T, dir string) {
			put(t, dir, shellTool, strings.Replace(get(t, dir, shellTool), "print(sys.argv)\n", "print(sys.argv)\nprint(len(sys.argv))\n", 1))
		}, "FAIL embedded_lines is 10, 1 more than the baseline 9", shellTool + " grew from 2 to 3"},
		{"invoke_lines: python3 <path> of a program without the .py suffix", func(t *testing.T, dir string) {
			put(t, dir, shellTool, get(t, dir, shellTool)+"python3 scripts/new-check --flag\n")
		}, "FAIL invoke_lines is 8, 1 more than the baseline 7", shellTool + " grew from 1 to 2"},
		{"sovereign_places: a python image in a chart template", func(t *testing.T, dir string) {
			put(t, dir, template, get(t, dir, template)+"        - image: docker.io/library/python:3.12-slim\n")
		}, "FAIL sovereign_places is 5, 1 more than the baseline 4", template + " grew from 1 to 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := seeded(t)
			commitAll(t, dir)
			c.change(t, dir)
			code, out := guardOn(dir)
			wantFails(t, code, out, c.fail)
			code, out = guardOn(dir, "--base", "HEAD")
			wantFails(t, code, out, c.fail, "  "+c.grew+" against HEAD")
		})
	}
}

// A fall of each total passes, with one line that says the baseline can be lowered.
func TestFallOfEachTotalPasses(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, dir string)
		lower  string
	}{
		{"python_files", func(t *testing.T, dir string) { remove(t, dir, "scripts/lib/helper.py") },
			"python_files 3 (baseline 4), python_file_lines 7 (baseline 8)"},
		{"python_file_lines", func(t *testing.T, dir string) { put(t, dir, tool, "#!/usr/bin/env python3\nprint(1)\n") },
			"python_file_lines 7 (baseline 8)"},
		{"embedded_lines", func(t *testing.T, dir string) {
			put(t, dir, shellTool, strings.Replace(get(t, dir, shellTool), "print(sys.argv)\n", "", 1))
		}, "embedded_lines 8 (baseline 9)"},
		{"invoke_lines", func(t *testing.T, dir string) {
			put(t, dir, ciFlow, strings.Replace(get(t, dir, ciFlow), "python3 -c 'import json; print(1)'", "go version", 1))
		}, "embedded_lines 8 (baseline 9), invoke_lines 6 (baseline 7)"},
		{"sovereign_places", func(t *testing.T, dir string) {
			put(t, dir, image, strings.Replace(get(t, dir, image), " python3", "", 1))
		}, "invoke_lines 6 (baseline 7), sovereign_places 3 (baseline 4)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := seeded(t)
			commitAll(t, dir)
			c.change(t, dir)
			for _, args := range [][]string{nil, {"--base", "HEAD"}} {
				code, out := guardOn(dir, args...)
				want(t, code, out, 0, me+": lower than the baseline: "+c.lower+"; "+lowerLine+"\n")
				if strings.Contains(out, "FAIL") || strings.Count(out, "lower than") != 1 {
					t.Fatalf("a fall passes with one line\n%s", out)
				}
			}
		})
	}
}

// There is no list of files: a rename or a move changes no total.
func TestRenameOrMoveOfAPythonFilePasses(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	move(t, dir, tool, "tools/py/renamed.py")
	move(t, dir, "platform/app/chart/tests/check.py", "scripts/check_chart.py")
	for _, args := range [][]string{nil, {"--base", "HEAD"}} {
		code, out := guardOn(dir, args...)
		want(t, code, out, 0, fixtureSummary)
		if strings.Contains(out, "FAIL") || strings.Contains(out, "lower than") {
			t.Fatalf("a rename changes no total\n%s", out)
		}
	}
}

// A file that grew in a rename is compared with its old path.
func TestBaseComparesARenamedFileWithItsOldPath(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	move(t, dir, tool, "tools/py/renamed.py")
	put(t, dir, "tools/py/renamed.py", get(t, dir, "tools/py/renamed.py")+"print(3)\n")
	code, out := guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out, "FAIL python_file_lines is 9, 1 more than the baseline 8",
		"  tools/py/renamed.py (was "+tool+") grew from 3 to 4 against HEAD")
}

// The reviewer's bypass: a step whose shell is python is Python inside the workflow.
func TestShellPythonStepIsCounted(t *testing.T) {
	dir := seeded(t)
	put(t, dir, ".github/workflows/new.yaml", "jobs:\n  b:\n    steps:\n      - name: new\n        shell: python3\n"+
		"        run: |\n          import json\n          print(json.dumps({}))\n          print(1)\n")
	code, out := guardOn(dir)
	wantFails(t, code, out, "FAIL embedded_lines is 12, 3 more than the baseline 9", "FAIL invoke_lines is 8, 1 more than the baseline 7")
}

// The reviewer's other bypass: a Python program saved without the .py suffix and run as python3 <path>.
func TestPythonProgramWithoutSuffixIsCountedByItsCommand(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "scripts/new-check", "import sys\nprint(sys.argv)\n")
	put(t, dir, ".github/workflows/new.yaml", "jobs:\n  b:\n    steps:\n      - run: python3 scripts/new-check\n")
	code, out := guardOn(dir)
	wantFails(t, code, out, "FAIL invoke_lines is 8, 1 more than the baseline 7")
	put(t, dir, "scripts/new-check", "#!/usr/bin/env python3\nprint(1)\n")
	code, out = guardOn(dir)
	wantFails(t, code, out, "FAIL python_files is 5, 1 more than the baseline 4",
		"FAIL python_file_lines is 10, 2 more than the baseline 8", "FAIL invoke_lines is 8, 1 more than the baseline 7")
}

// The reviewer's undercount: a here-document of a Terraform file that templatefile() puts into
// the cloud-init of the nodes runs on a Sovereign.
func TestPythonInTerraformStartUpDataIsAPlace(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	put(t, dir, nodeTF, strings.Replace(get(t, dir, nodeTF), "  curl -s https://example.invalid/healthz\n",
		"  curl -s https://example.invalid/healthz\n  python3 -c \"import json; print(json.dumps({'a': 1}))\" > /tmp/body.json\n", 1))
	code, out := guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out,
		"FAIL embedded_lines is 10, 1 more than the baseline 9", "  "+nodeTF+" grew from 0 to 1 against HEAD",
		"FAIL invoke_lines is 8, 1 more than the baseline 7", "  "+nodeTF+" grew from 0 to 1 against HEAD",
		"FAIL sovereign_places is 5, 1 more than the baseline 4", "  "+nodeTF+" grew from 0 to 1 against HEAD")
}

func TestWriteBaselineLowersAndRefusesARise(t *testing.T) {
	dir := seeded(t)
	remove(t, dir, "scripts/lib/helper.py")
	code, out := guardOn(dir, "--write-baseline")
	want(t, code, out, 0, "wrote "+baselinePath)
	if strings.Contains(out, "lower than") {
		t.Fatalf("after the write the baseline is the tree\n%s", out)
	}
	if got, wantC := readBaseline(t, dir), (counts{3, 7, 9, 7, 4}); got != wantC {
		t.Fatalf("the written baseline is %v, want %v", got, wantC)
	}

	before := get(t, dir, baselinePath)
	put(t, dir, "scripts/lib/helper.py", "x = 1\n")
	code, out = guardOn(dir, "--write-baseline")
	want(t, code, out, 1, "FAIL python_files is 4, 1 more than the baseline 3", "the baseline was not written")
	if get(t, dir, baselinePath) != before {
		t.Fatal("a refused write changed the baseline")
	}

	// One total that rises is refused also when another falls.
	put(t, dir, tool, "print(1)\n")
	code, out = guardOn(dir, "--write-baseline")
	want(t, code, out, 1, "FAIL python_files is 4, 1 more than the baseline 3", "the baseline was not written")
	if get(t, dir, baselinePath) != before {
		t.Fatal("a refused write changed the baseline")
	}
}

// With --base, a baseline raised in the change under test raises nothing.
func TestBaseUsesTheLowerBaseline(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	editBaseline(t, dir, func(c *counts) { c[pyFiles]++ })
	put(t, dir, "scripts/new.py", "")
	code, out := guardOn(dir)
	want(t, code, out, 0)
	code, out = guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out, "FAIL python_files is 5, 1 more than the baseline 4", "  scripts/new.py grew from 0 to 1 against HEAD")
	want(t, code, out, 1, "note: the baseline file raises python_files from 4 at HEAD to 5; 4 is used")
}

// The reviewer's reuse of freed room: change A deletes Python and does not lower the baseline
// file; change B adds Python up to the freed number. Against the baseline file alone B fits;
// with --base each total is also held to the total of the base commit, a deletion of B included.
func TestBaseHoldsTheTotalOfTheBaseCommit(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	remove(t, dir, "scripts/lib/helper.py")
	code, out := guardOn(dir, "--base", "HEAD")
	want(t, code, out, 0, "lower than the baseline: python_files 3 (baseline 4), python_file_lines 7 (baseline 8)")
	commitAll(t, dir)

	// B: tool.py (3 lines) goes, two files of one line come: one file more, one line less.
	remove(t, dir, tool)
	put(t, dir, "scripts/new.py", "x = 2\n")
	put(t, dir, "scripts/other.py", "y = 3\n")
	code, out = guardOn(dir)
	want(t, code, out, 0)
	code, out = guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out, "FAIL python_files is 4, 1 more than the baseline 3",
		"  scripts/new.py grew from 0 to 1 against HEAD", "  scripts/other.py grew from 0 to 1 against HEAD")
	want(t, code, out, 1, "note: python_files is 3 at HEAD, below the baseline 4: room freed before is not taken again; 3 is used",
		"note: python_file_lines is 7 at HEAD, below the baseline 8")

	// The same reuse of room freed in Python inside another file.
	dir = seeded(t)
	commitAll(t, dir)
	put(t, dir, shellTool, strings.Replace(get(t, dir, shellTool), "print(sys.argv)\n", "", 1))
	commitAll(t, dir)
	put(t, dir, configMap, get(t, dir, configMap)+"    print(2)\n")
	code, out = guardOn(dir)
	want(t, code, out, 0)
	code, out = guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out, "FAIL embedded_lines is 9, 1 more than the baseline 8", "  "+configMap+" grew from 2 to 3 against HEAD")
}

// A Chart.yaml that comes makes an unchanged template a file of the chart scope: the base total
// measures that file in the scope of the base commit.
func TestBaseMeasuresAFileWhoseScopeChanged(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "platform/lib/templates/run.yaml", "command: /usr/bin/python3 /x/run\n")
	editBaseline(t, dir, func(c *counts) { c[invokeLines]++ })
	commitAll(t, dir)
	put(t, dir, image, strings.Replace(get(t, dir, image), " python3", "", 1))
	commitAll(t, dir)
	put(t, dir, "platform/lib/Chart.yaml", "apiVersion: v2\nname: lib\nversion: 0.1.0\n")
	code, out := guardOn(dir)
	want(t, code, out, 0)
	code, out = guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out, "FAIL sovereign_places is 4, 1 more than the baseline 3",
		"  platform/lib/templates/run.yaml grew from 0 to 1 against HEAD")
}

// The reviewer's other gap: Python run from Go, TypeScript or JavaScript.
func TestPythonInsideGoIsCounted(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	put(t, dir, "core/x/main.go", "package main\n\nimport \"os/exec\"\n\nfunc main() {\n"+
		"\t_ = exec.Command(\"python3\", \"-c\", `\nimport os\nprint(os.sep)\n`).Run()\n}\n")
	code, out := guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out,
		"FAIL embedded_lines is 13, 4 more than the baseline 9", "  core/x/main.go grew from 0 to 4 against HEAD",
		"FAIL invoke_lines is 8, 1 more than the baseline 7", "  core/x/main.go grew from 0 to 1 against HEAD")

	put(t, dir, "core/x/main.go", "package main\n\n//go:generate python3 gen.py\n\nfunc main() {}\n")
	put(t, dir, "web/src/run.ts", "import { spawnSync } from 'node:child_process'\nspawnSync('python3', ['-c', 'print(1)'])\n")
	code, out = guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out,
		"FAIL embedded_lines is 10, 1 more than the baseline 9", "  web/src/run.ts grew from 0 to 1 against HEAD",
		"FAIL invoke_lines is 9, 2 more than the baseline 7", "  core/x/main.go grew from 0 to 1 against HEAD", "  web/src/run.ts grew from 0 to 1 against HEAD")
}

// A .py path is a Python file by its path, also when it holds a NUL byte.
func TestPythonFileWithANulByteIsCounted(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	put(t, dir, "scripts/blob.py", "x = 1\x00\n")
	code, out := guardOn(dir, "--base", "HEAD")
	wantFails(t, code, out,
		"FAIL python_files is 5, 1 more than the baseline 4", "  scripts/blob.py grew from 0 to 1 against HEAD",
		"FAIL python_file_lines is 9, 1 more than the baseline 8", "  scripts/blob.py grew from 0 to 1 against HEAD")
}

func TestBaseWithoutABaselineUsesTheFile(t *testing.T) {
	dir := seeded(t)
	baselineText := get(t, dir, baselinePath)
	remove(t, dir, baselinePath)
	commitAll(t, dir)
	put(t, dir, baselinePath, baselineText)
	code, out := guardOn(dir, "--base", "HEAD")
	want(t, code, out, 0, "does not exist at HEAD; the baseline file alone is used")
}

func TestUnreadableInputsCannotBeJudged(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"a missing baseline": func(t *testing.T, dir string) { remove(t, dir, baselinePath) },
		"another schema": func(t *testing.T, dir string) {
			put(t, dir, baselinePath, strings.Replace(get(t, dir, baselinePath), schema, "openova.io/guard-baseline/v1", 1))
		},
		"a missing total": func(t *testing.T, dir string) {
			put(t, dir, baselinePath, strings.Replace(get(t, dir, baselinePath), `,
  "sovereign_places": 4`, "", 1))
		},
		"an unknown key": func(t *testing.T, dir string) {
			put(t, dir, baselinePath, strings.Replace(get(t, dir, baselinePath), `"schema"`, `"extra": 1, "schema"`, 1))
		},
		"a negative total": func(t *testing.T, dir string) {
			put(t, dir, baselinePath, strings.Replace(get(t, dir, baselinePath), `"python_files": 4`, `"python_files": -1`, 1))
		},
		"a total that is not a whole number": func(t *testing.T, dir string) {
			put(t, dir, baselinePath, strings.Replace(get(t, dir, baselinePath), `"python_files": 4`, `"python_files": 4.5`, 1))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			dir := seeded(t)
			change(t, dir)
			code, out := guardOn(dir)
			want(t, code, out, 2, "cannot judge")
		})
	}
	t.Run("a base that is not a commit", func(t *testing.T) {
		dir := seeded(t)
		commitAll(t, dir)
		code, out := guardOn(dir, "--base", "no-such-ref")
		want(t, code, out, 2, "the base no-such-ref is not a commit")
	})
}

func TestScope(t *testing.T) {
	charts := map[string]bool{"platform/app/chart": true, "platform/app/chart/charts/sub": true}
	cases := map[string]string{
		"platform/app/chart/templates/job.yaml":              scopeChart,
		"platform/app/chart/templates/tests/hook.yaml":       scopeChart,
		"platform/app/chart/files/run.py":                    scopeChart,
		"platform/app/chart/charts/sub/templates/a.yaml":     scopeChart,
		"platform/app/chart/templates/NOTES.txt":             "",
		"platform/app/chart/templates/README.md":             "",
		"platform/app/chart/values.yaml":                     "",
		"platform/app/chart/tests/check.py":                  "",
		"platform/app/chart/charts/sub/values.yaml":          "",
		"platform/other/templates/job.yaml":                  "",
		"infra/providers/x/cloudinit-worker.tftpl":           scopeNode,
		"infra/providers/_shared/cloud-init-control.yaml":    scopeNode,
		"infra/providers/huawei/main.tf":                     scopeNode,
		"infra/providers/huawei/variables.tf":                scopeNode,
		"infra/providers/_shared/tests/cloudinit_render.hcl": "",
		"infra/providers/x/provider-substitutes.yaml.tftpl":  "",
		"infra/providers/huawei/tests/user_data.py":          "",
		"infra/other/cloudinit-worker.tftpl":                 "",
		"infra/other/main.tf":                                "",
		"products/img/Containerfile":                         scopeImage,
		"core/x/Containerfile.test":                          scopeImage,
		"core/x/Dockerfile":                                  scopeImage,
		"core/x/containerfile-notes.md":                      "",
	}
	for p, wantScope := range cases {
		if got := scopeOf(p, charts); got != wantScope {
			t.Errorf("scopeOf(%s) = %q, want %q", p, got, wantScope)
		}
	}
}

func TestPatterns(t *testing.T) {
	cases := []struct {
		name, scope, path, text string
		want                    []string // the kinds, sorted
	}{
		{"python3 in a YAML list", scopeChart, "t.yaml", "  - python3 -c 'x'\n", []string{"invoke"}},
		{"a path to python3 in a command array", scopeChart, "t.yaml", `command: ["/usr/bin/python3", "/s/run.py"]`, []string{"invoke"}},
		{"python3 in a command substitution", scopeChart, "t.yaml", `EXP=$(python3 -c 'import json;print(1)')`, []string{"invoke"}},
		{"python3.12 and python", scopeChart, "t.yaml", "python3.12 -V && python x.py\n", []string{"invoke", "invoke"}},
		{"python3 right before a here-document", scopeChart, "t.yaml", "python3<<'PY'\n", []string{"invoke"}},
		{"python3 as the default of a variable", scopeChart, "t.yaml", "\"${PYTHON:-python3}\" /s/run.py\n", []string{"invoke"}},
		{"python3 right before a redirect", scopeChart, "t.yaml", "python3>/tmp/out -V\n", []string{"invoke"}},
		{"python3 after env", scopeNode, "c.tftpl", "  - /usr/bin/env python3 -c 'import socket'\n", []string{"invoke"}},
		{"python3 -c in a here-document of Terraform", scopeNode, "main.tf",
			"  put = <<-PUT\n- |\n  python3 -c \"import json,os; print(json.dumps({}))\" > /tmp/b.json\nPUT\n", []string{"invoke"}},
		{"a Terraform comment", scopeNode, "outputs.tf", "# python3 scripts/check.py r.json\n", nil},
		{"a continued line counts once", scopeChart, "t.yaml", "python3 /scripts/reload.py \\\n  --fast || \\\n  echo done\n", []string{"invoke"}},
		{"a comment holds no place", scopeChart, "t.yaml", "# python3 runs here\n  # and python here\n", nil},
		{"a template comment holds no place", scopeChart, "t.yaml", "{{- /* Generated by python3 x.py */ -}}\n{{/*\npython3 y.py\n*/}}\nkind: Job\n", nil},
		{"a word inside another word is not python", scopeChart, "t.yaml", "inject-{java,python,nodejs}\ninstrumentation.io/inject-python: \"x\"\n{{ .Values.python.enabled }}\nmypython3 x\n", nil},
		{"a package name is not an invocation", scopeImage, "Containerfile", "RUN apt-get purge -y python3-pip && rm -rf /tools/python3-openscap/\n", nil},
		{"a shebang in a chart file", scopeChart, "files/x.sh", "#!/usr/bin/python3\nprint(1)\n", []string{"shebang"}},
		{"a shebang inside a ConfigMap", scopeChart, "t.yaml", "data:\n  run: |\n    #!/usr/bin/env python3\n    print(1)\n", []string{"shebang"}},
		{"a shell shebang is not python", scopeChart, "t.yaml", "#!/bin/sh\necho ok\n", nil},
		{"a .py file of a chart: only its shebang", scopeChart, "files/x.py", "#!/usr/bin/env python3\nimport subprocess\nsubprocess.run([\"python3\"])\n", []string{"shebang"}},
		{"a .py file without a shebang", scopeChart, "files/y.py", "print(1)\n", nil},
		{"apk add python3", scopeImage, "Containerfile", "RUN apk add --no-cache bash python3\n", []string{"install"}},
		{"apt-get install over continued lines", scopeImage, "Containerfile", "RUN apt-get update \\\n    && apt-get install -y \\\n       python3 python3-pip \\\n    && rm -rf /var/lib/apt/lists/*\n", []string{"install"}},
		{"apt-get install without python", scopeImage, "Containerfile", "RUN apt-get install -y curl git\n", nil},
		{"pip3 install", scopeImage, "Containerfile", "RUN pip3 install --no-cache-dir tool==1.0\n", []string{"install"}},
		{"python3 -m pip install then python3 -m pip check", scopeImage, "Containerfile", "RUN python3 -m pip install --no-deps x && python3 -m pip check\n", []string{"install", "invoke"}},
		{"FROM python", scopeImage, "Containerfile", "FROM python:3.12-slim\n", []string{"install"}},
		{"FROM a python image with a registry and a platform", scopeImage, "Dockerfile", "FROM --platform=$BUILDPLATFORM docker.io/library/python:3.12 AS build\n", []string{"install"}},
		{"FROM another image", scopeImage, "Containerfile", "FROM docker.io/library/alpine:3.20\n", nil},
		{"a LABEL that names python3", scopeImage, "Containerfile", "LABEL org.opencontainers.image.description=\"migrations (python3 + kubectl)\"\n", nil},
		{"CMD with python3", scopeImage, "Containerfile", "CMD [\"/usr/bin/python3\", \"--version\"]\n", []string{"invoke"}},
		{"a python image in a chart", scopeChart, "t.yaml", "      - image: docker.io/library/python:3.12-slim\n", []string{"install"}},
		{"another image in a chart", scopeChart, "t.yaml", "      - image: ghcr.io/x/autoinstrumentation-python:1.0\n", nil},
		{"a variable the file assigns python3 runs python", scopeChart, "files/x.sh", "PY=python3\n\"$PY\" -c 'x'\n${PY} y\n", []string{"invoke", "invoke", "invoke"}},
		{"a variable assigned another program", scopeChart, "files/x.sh", "PY=/bin/sh\n\"$PY\" -c 'x'\n", nil},
	}
	for _, c := range cases {
		var got []string
		for _, pl := range scan(c.scope, c.path, c.text) {
			got = append(got, pl.kind)
		}
		sort.Strings(got)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: kinds %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEmbedded(t *testing.T) {
	cases := []struct {
		name, text string
		want       int
	}{
		{"a here-document given to python", "exec python3 - \"$@\" <<'PY'\nimport sys\nprint(1)\nPY\necho done\n", 2},
		{"<<- with a tab before the delimiter", "\tpython3 - <<-EOF\n\timport os\n\tEOF\n", 1},
		{"a here-document piped into python", "cat <<'EOF' | python3 -\nprint(1)\nEOF\n", 1},
		{"python3 right before <<", "python3<<\"PY\"\nprint(1)\nPY\n", 1},
		{"a here-document that writes a .py file", "cat > \"$TMP/check.py\" <<'PYEOF'\nimport yaml\nPYEOF\npython3 \"$TMP/check.py\"\n", 1},
		{"an opener over a continued line", "python3 - \"$a\" \\\n  \"$b\" <<'PY'\nprint(1)\nPY\n", 1},
		{"a here-document not given to python", "cat <<'EOF' > notes\npython is named here\nEOF\n", 0},
		{"a commented opener", "# python3 - <<'PY'\nprint(1)\n", 0},
		{"a here-document without its delimiter runs to the end", "python3 - <<'PY'\nprint(1)\nprint(2)\n", 2},
		{"python -c on one line", "python3 -c 'import json; print(1)'\n", 1},
		{"python -c over lines, from quote to quote", "python3 -c \"\nimport os\nprint(os.sep)\n\"\n", 4},
		{"python -c in a JSON array", "command: [\"python3\", \"-c\", \"import os\"]\n", 1},
		{"python -I -c", "python3 -I -c 'import os'\n", 1},
		{"python -c inside a quoted YAML string", "run: \"python3 -c \\\"import yaml\\nprint(1)\\\"\"\n", 2},
		{"a here-document inside a quoted YAML string", "run: \"python3 - <<'PY'\\nimport yaml\\nprint(1)\\nPY\"\n", 2},
		{"a << in the Python of a here-document inside a YAML string", "run: \"python3 - <<'EOF'\\nx = re.findall(r\\\"<<'PY'\\\\n\\\")\\nEOF\"\nnext: 1\n", 1},
		{"a << inside python -c is Python", "python3 -c 'h = \"<<-EOT\"; print(h)'\nline\nline\n", 1},
		{"a here-string given to python", "python3 - <<< 'print(1)'\n", 1},
		{"two programs on one line", "python3 -c 'a' && python3 -c 'b'\n", 2},
		{"a program in a quoted variable", "PRED='import os\nprint(1)'\npython3 -c \"$PRED\"\n", 3},
		{"a program read from a here-document", "read -r -d '' CLASSIFY_PY <<'PY' || true\nimport sys\nprint(1)\nPY\nclassify() { python3 -c \"$CLASSIFY_PY\"; }\n", 3},
		{"a program from $(cat <<)", "S=$(cat <<'EOF'\nimport os\nEOF\n)\npython3 -c \"${S}\"\n", 2},
		{"a .py key of a ConfigMap", "data:\n  reload.py: |\n    import os\n\n    print(1)\n  other: x\n", 3},
		{"a block that starts with a python #! line", "data:\n  run: |\n    #!/usr/bin/env python3\n    print(1)\n", 2},
		{"a block of shell", "run: |\n  echo hi\n  ls\n", 0},
		{"a .py key filled by a template action", "  x.py: |-\n{{ .Files.Get \"files/x.py\" | default \"import sys\\nsys.exit(1)\\n\" | indent 4 }}\n", 3},
		{"a run of a .py file is no Python inside", "python3 scripts/tool.py --flag\n", 0},
		// A program saved without the .py suffix: the here-document does not name python, its body does.
		{"a here-document whose body starts with a python #! line", "cat > \"$BIN/curl\" <<'PY'\n#!/usr/bin/env python3\nimport sys\n\nprint(sys.argv)\nPY\nchmod +x \"$BIN/curl\"\n", 4},
		{"<<- and a tab before the python #! line", "\tcat > \"$BIN/x\" <<-EOF\n\t#!/usr/bin/python3\n\tprint(1)\n\tEOF\n", 2},
		{"a here-document of shell is read line by line, one of python is counted", "cat > a <<'EOF'\n#!/bin/sh\npython3 -c 'print(1)'\nEOF\n" +
			"cat > b <<'EOF'\n#!/usr/bin/env python3\nprint(2)\nEOF\n", 3},
		{"the second here-document of a line starts with a python #! line", "cat <<A >a; cat <<B >b\n#!/bin/sh\nA\n#!/usr/bin/env python3\nprint(1)\nB\n", 2},
		{"a python #! here-document inside a quoted YAML string", "run: \"cat > x <<'PY'\\n#!/usr/bin/env python3\\nprint(1)\\nPY\"\n", 2},
		{"a python #! here-document in Terraform", "locals {\n  stub = <<-EOT\n    #!/usr/bin/env python3\n    print(1)\n  EOT\n}\n", 2},
		// python run through a variable that the file itself assigns a name of python.
		{"\"$PYTHON\" -c over lines", "PYTHON=\"${PYTHON:-python3}\"\nok=$(echo \"$j\" | \"$PYTHON\" -c \"\nimport json\nprint(1)\n\")\n", 4},
		{"\"$PYTHON\" - <<'PY'", "PYTHON=\"${PYTHON:-python3}\"\n\"$PYTHON\" - \"$f\" <<'PY'\nimport sys\nprint(1)\nPY\necho done\n", 2},
		{"\"${PY_BIN}\" - <<'PY'", "PY_BIN=\"${PY_BIN:-python3}\"\nout=$(\"${PY_BIN}\" - <<'PY'\nimport os\nPY\n)\n", 1},
		{"$PY -c after PY=$(command -v python3)", "PY=$(command -v python3)\n$PY -c 'print(1)'\n", 1},
		{"a variable assigned another program opens nothing", "PYTHON=/bin/sh\n\"$PYTHON\" -c \"\necho 1\n\"\n\"$PYTHON\" - <<'EOF'\necho 2\nEOF\n", 0},
		{"a longer variable that starts with the name is not python", "PYTHON=python3\ncat > \"$PYTHONPATH/x\" <<'EOF'\nprint(1)\nEOF\n", 0},
	}
	for _, c := range cases {
		if got := embedded(c.text, nil); got != c.want {
			t.Errorf("%s: %d lines, want %d", c.name, got, c.want)
		}
	}
}

// The run of a step whose shell is python is counted in embedded_lines, once.
func TestPythonSteps(t *testing.T) {
	step := func(shell, run string) string {
		return "jobs:\n  a:\n    steps:\n      - name: s\n" + shell + run
	}
	block := "        run: |\n          import os\n\n          print(os.sep)\n"
	cases := []struct {
		name, path, text string
		want             int
	}{
		{"shell: python before the run", ".github/workflows/a.yaml", step("        shell: python\n", block), 3},
		{"shell: python3 after the run", ".github/workflows/a.yml", step("", block+"        shell: python3\n"), 3},
		{"shell: python {0}", ".github/workflows/a.yaml", step("        shell: 'python {0}'\n", block), 3},
		{"shell: a path to python3", ".github/workflows/a.yaml", step("        shell: /usr/bin/python3 {0}\n", block), 3},
		{"shell: bash", ".github/workflows/a.yaml", step("        shell: bash\n", block), 0},
		{"an inline run", ".github/workflows/a.yaml", step("        shell: python\n", "        run: print(1)\n"), 1},
		{"the run is the first key of the step", ".github/workflows/a.yaml",
			"jobs:\n  a:\n    steps:\n      - run: |\n          print(1)\n        shell: python\n      - run: echo hi\n", 1},
		{"the next step has its own shell", ".github/workflows/a.yaml",
			step("        shell: python\n", block) + "      - name: t\n        run: |\n          echo hi\n", 3},
		{"the run default of the workflow", ".github/workflows/a.yaml",
			"defaults:\n  run:\n    shell: python\njobs:\n  a:\n    steps:\n      - run: |\n          print(1)\n      - shell: bash\n        run: echo hi\n", 1},
		{"the run default of the job wins over the workflow", ".github/workflows/a.yaml",
			"defaults:\n  run:\n    shell: python\njobs:\n  a:\n    defaults:\n      run:\n        shell: bash\n    steps:\n      - run: |\n          echo hi\n  b:\n    steps:\n      - run: print(1)\n", 1},
		{"a step of a composite action", "tools/act/action.yml",
			"runs:\n  using: composite\n  steps:\n    - shell: python\n      run: |\n        import sys\n        print(sys.argv)\n", 2},
		{"python3 -c inside the Python of the step is counted once", ".github/workflows/a.yaml",
			step("        shell: python\n", "        run: |\n          import subprocess\n          subprocess.run([\"python3\", \"-c\", \"print(1)\"])\n"), 2},
		{"another YAML file is not read for steps", "platform/x/steps.yaml", step("        shell: python\n", block), 0},
	}
	for _, c := range cases {
		got, _ := measureFile(c.path, "", []byte(c.text))
		if got[embeddedLines] != c.want {
			t.Errorf("%s: embedded_lines %d, want %d", c.name, got[embeddedLines], c.want)
		}
	}
}

func TestInvocations(t *testing.T) {
	cases := []struct {
		name, text string
		want       int
	}{
		{"python3 <path>", "python3 scripts/new-check\n", 1},
		{"a run step", "      - run: python3 scripts/x --self-test\n", 1},
		{"python in a Makefile recipe", "check:\n\tpython tools/check\n", 1},
		{"shell: python", "        shell: python\n", 1},
		{"package.json script", `    "check": "python3 tools/check",` + "\n", 1},
		{"a comment", "# python3 scripts/x\n// python3 y\n", 0},
		{"a word inside another word", "inject-python: true\nmypython3 x\npython3-pip\n", 0},
		{"two lines", "python3 a\npython b\n", 2},
		{"a variable the file assigns python3", "PYTHON=\"${PYTHON:-python3}\"\nx=$(\"$PYTHON\" -c 'print(1)')\n${PYTHON} - <<'PY'\n$PYTHON x\n", 4},
		{"PY_BIN and PY=$(command -v python3)", "PY_BIN=\"${PY_BIN:-python3}\"\nPY=\"$(command -v python3 || true)\"\n\"${PY_BIN}\" - <<'PY'\n\"$PY\" y\n", 4},
		{"a variable assigned another program", "PYTHON=/bin/sh\n\"$PYTHON\" -c 'x'\n", 0},
		{"a longer variable that starts with the name", "PYTHON=python3\nexport PYTHONPATH=\"$PYTHONPATH:/x\"\n", 1},
	}
	for _, c := range cases {
		if got := invocations(c.text, nil); got != c.want {
			t.Errorf("%s: %d lines, want %d", c.name, got, c.want)
		}
	}
}

// Of a Go, TypeScript or JavaScript file only the lines that can run python are read.
func TestCodeFiles(t *testing.T) {
	cases := []struct {
		name, path, text  string
		embedded, invokes int
	}{
		{"exec.CommandContext", "s/check.go", "\tcmd := exec.CommandContext(ctx, \"python3\")\n", 0, 1},
		{"a Go raw string given to python3 -c", "s/x.go", "cmd := exec.Command(\"python3\", \"-c\", `\nimport os\nprint(os.sep)\n`)\n", 4, 1},
		{"a call over lines", "s/x.go", "cmd := exec.Command(\n\t\"/usr/bin/python3\",\n\tscript,\n)\n", 0, 1},
		{"python3 run by sh -c", "s/x.go", "exec.Command(\"sh\", \"-c\", \"python3 tools/check\")\n", 0, 1},
		{"the name of python held for later", "s/x.go", "const interpreter = \"python3\"\n", 0, 1},
		{"a //go:generate line", "s/gen.go", "package all\n\n//go:generate python3 ../gen.py\n", 0, 1},
		{"a spawn of TypeScript", "web/run.ts", "spawn('python3', [script])\n", 0, 1},
		{"an argument list of JavaScript", "web/run.mjs", "spawnSync('python3', ['-c', 'import os'])\n", 1, 1},
		{"a template literal of JavaScript", "web/run.js", "execFileSync(\"python3\", [\"-I\", \"-c\", `import os\nprint(1)`])\n", 2, 1},
		{"execSync of a command line", "web/run.cjs", "execSync(`python3 tools/check --flag`)\n", 0, 1},
		{"a comment", "s/x.go", "// exec.Command(\"python3\", \"-c\", \"x\")\n/* python3 -c 'y' */\n", 0, 0},
		{"the fixture of a test is data", "s/x_test.go",
			"var f = map[string]string{\"run.sh\": \"python3 -c 'print(1)'\\n\", \"job.yaml\": `command: [\"python3\", \"-c\", \"x\"]`}\n" +
				"var g = \"cmd := exec.Command(\\\"python3\\\")\"\n", 0, 0},
		{"a word in code is not a command", "s/x.go", "if lang == python {\n\tpythonSteps(x)\n}\n", 0, 0},
		{"a call that runs another program", "s/x.go", "exec.Command(\"git\", \"-c\", \"core.x=1\", \"status\")\n", 0, 0},
		// A string that starts with a python #! line is a program saved without the .py suffix.
		{"a Go stub written with os.WriteFile", "s/x_test.go",
			"stub := \"#!/usr/bin/env python3\\nimport sys\\nprint(sys.argv)\\n\"\nos.WriteFile(p, []byte(stub), 0o755)\n", 3, 0},
		{"a Go raw string: a \\n in it is not a line", "s/x_test.go",
			"os.WriteFile(p, []byte(`#!/usr/bin/python3\nimport sys\nprint(\"a\\nb\")\n`), 0o755)\n", 3, 0},
		{"a TypeScript template literal", "web/stub.test.ts", "writeFileSync(p, `#!/usr/bin/env python3\\nprint(1)\\n`, { mode: 0o755 })\n", 2, 0},
		{"a #! string of shell is data, one of python is not", "s/x_test.go",
			"a := \"#!/bin/sh\\necho hi\\n\"\nb := \"#!/usr/bin/env python3\\nprint(1)\\n\"\n", 2, 0},
		{"a python #! string given to python3 -c is counted once", "s/x.go", "exec.Command(\"python3\", \"-c\", \"#!/usr/bin/env python3\\nprint(1)\")\n", 2, 1},
		{"the quote that closes \"-c\" opens no program", "s/x.go", "exec.Command(\"python3\", \"-c\", prog)\nx := \"a\"\n", 0, 1},
		// Rust is a first-party language.
		{"a Rust Command::new", "src/run.rs", "let out = std::process::Command::new(\"python3\").arg(\"-V\").output()?;\n", 0, 1},
		{"a Rust lifetime is code", "src/run.rs", "fn run(s: &'static str) -> Output { Command::new(\"python3\").arg(s).output().unwrap() }\n", 0, 1},
		{"a Rust raw string", "src/stub.rs", "fs::write(&p, r#\"#!/usr/bin/env python3\nprint(\"x\")\n\"#)?;\n", 2, 0},
		{"a Rust string over lines", "src/stub.rs", "let stub = \"#!/usr/bin/env python3\nimport sys\nprint(1)\n\";\n", 3, 0},
	}
	for _, c := range cases {
		got, _ := measureFile(c.path, "", []byte(c.text))
		if got[embeddedLines] != c.embedded || got[invokeLines] != c.invokes {
			t.Errorf("%s: embedded_lines %d invoke_lines %d, want %d %d", c.name, got[embeddedLines], got[invokeLines], c.embedded, c.invokes)
		}
	}
}

func TestReadsEmbedded(t *testing.T) {
	cases := map[string]bool{
		"scripts/x.sh": true, ".github/workflows/x.yaml": true, ".github/actions/a/action.yml": true,
		"infra/providers/huawei/main.tf": true, "Makefile": true, "package.json": true, "core/x/main.go": true,
		"core/x/main_test.go": true, "core/x/src/main.rs": true, "web/src/run.ts": true, "web/src/App.tsx": true, "web/lib/x.mjs": true, "web/lib/x.js": true,
		"docs/RUNBOOKS.md": false, "notes.txt": false, "scripts/no-suffix": false, "web/logo.svg": false,
	}
	for p, wantRead := range cases {
		if got := readsEmbedded(p, "", ""); got != wantRead {
			t.Errorf("readsEmbedded(%s) = %v, want %v", p, got, wantRead)
		}
	}
	if !readsEmbedded("scripts/no-suffix", "", "#!/usr/bin/env bash") {
		t.Error("a file with a #! line is read")
	}
}
