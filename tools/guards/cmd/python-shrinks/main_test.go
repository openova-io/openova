package main

import (
	"bytes"
	"encoding/json"
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
	image     = "products/img/Containerfile"
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
		cloudInit:                   "runcmd:\n  - python3 -c 'print(1)'\n",
		image:                       "FROM alpine:3.20\nRUN apk add --no-cache bash python3\n",
		"core/x/main.go":            "package main\n\nfunc main() {}\n",
		"docs/README.md":            "Run python3 scripts/tool.py.\n",
		shellTool:                   "#!/usr/bin/env bash\nset -eu\nexec python3 - \"$@\" <<'PY'\nimport sys\nprint(sys.argv)\nPY\n",
		configMap:                   "kind: ConfigMap\ndata:\n  reload.py: |\n    import os\n    print(os.environ)\n",
		".github/workflows/ci.yaml": "jobs:\n  a:\n    steps:\n      - run: python3 -c 'import json; print(1)'\n",
	}
}

// The fixture holds 4 Python files of 3 + 1 + 1 + 3 lines; Python inside 5 other files: the
// here-document of check.sh (2), reload.py of the ConfigMap (2), and one python -c line in each of
// the workflow, the cloud-init template and values.yaml; and 4 places.
const fixtureSummary = "python_files=9 python_lines=15 (inside other files: 5 files, 7 lines) places=4 (install=1 invoke=2 shebang=1) exceptions=0"

func gitRun(t *testing.T, dir string, cmd *exec.Cmd) {
	t.Helper()
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %v\n%s", cmd.Args, err, out)
	}
}

func commitAll(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, exec.Command("git", "add", "-A"))
	gitRun(t, dir, exec.Command("git", "-c", "user.name=guard-test", "-c", "user.email=guard-test@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-q", "--allow-empty", "-m", "fixture"))
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
	gitRun(t, dir, exec.Command("git", "init", "-q"))
	for p, text := range fixture() {
		put(t, dir, p, text)
	}
	if code, out := guardOn(dir, "--write-baseline"); code != 0 || !strings.Contains(out, fixtureSummary) {
		t.Fatalf("the first write of the fixture baseline: exit %d\n%s", code, out)
	}
	return dir
}

func editBaseline(t *testing.T, root string, change func(b *baseline)) {
	t.Helper()
	b, err := parseBaseline([]byte(get(t, root, baselinePath)), baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	change(b)
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	put(t, root, baselinePath, string(data)+"\n")
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

func TestUnchangedTreePasses(t *testing.T) {
	dir := seeded(t)
	code, out := guardOn(dir)
	want(t, code, out, 0, fixtureSummary)
}

func TestNewPythonFileFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "scripts/new_tool.py", "print('new')\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL a new file that holds Python: scripts/new_tool.py (1 lines, a Python file)", "New tooling is written in Go")
}

func TestNewPythonScriptWithoutSuffixFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "scripts/new-tool", "#!/usr/bin/env python3\nprint('new')\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL a new file that holds Python: scripts/new-tool (2 lines, a Python file)")
}

func TestShellScriptIsNotPython(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "scripts/new-tool.sh", "#!/bin/sh\n# python3 - <<'PY' is not run here\npython3 scripts/tool.py\ncat <<'EOF'\nprint(1)\nEOF\n")
	code, out := guardOn(dir)
	want(t, code, out, 0, fixtureSummary)
}

// The bypass a reviewer named: a .sh file that hands a here-document to python.
func TestPythonHereDocumentInANewShellScriptFails(t *testing.T) {
	dir := seeded(t)
	body := strings.Repeat("print('a line of Python')\n", 1000)
	put(t, dir, "scripts/new-guard.sh", "#!/usr/bin/env bash\nset -eu\nexec python3 - \"$@\" <<'PY'\n"+body+"PY\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL a new file that holds Python: scripts/new-guard.sh (1000 lines, a file with Python inside)")
}

func TestInlinePythonInANewWorkflowFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, ".github/workflows/new.yaml", "jobs:\n  a:\n    steps:\n      - run: |\n          python3 -c \"\n          import json\n          print(1)\n          \"\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL a new file that holds Python: .github/workflows/new.yaml (4 lines, a file with Python inside)")
}

func TestGrowthOfAHereDocumentFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, shellTool, strings.Replace(get(t, dir, shellTool), "print(sys.argv)\n", "print(sys.argv)\nprint(len(sys.argv))\n", 1))
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL "+shellTool+" holds 3 lines of Python, the baseline allows 2")
}

// Python in a ConfigMap of a chart runs on a Sovereign: its lines are counted, not only the call.
func TestGrowthOfAProgramInAConfigMapFails(t *testing.T) {
	for name, more := range map[string]string{
		"a longer reload.py":    "    print(1)\n",
		"a second key of a .py": "  check.py: |\n    import sys\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := seeded(t)
			put(t, dir, configMap, get(t, dir, configMap)+more)
			code, out := guardOn(dir)
			want(t, code, out, 1, "FAIL "+configMap+" holds 3 lines of Python, the baseline allows 2")
		})
	}
}

func TestGrowthOfAFileFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, tool, get(t, dir, tool)+"print(3)\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL "+tool+" holds 4 lines of Python, the baseline allows 3")
}

// Every file has its own number: Python removed from one file is no room for another.
func TestOneFileCannotGrowWhileAnotherShrinks(t *testing.T) {
	dir := seeded(t)
	put(t, dir, tool, get(t, dir, tool)+"print(3)\n")
	remove(t, dir, "scripts/lib/helper.py")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL "+tool+" holds 4 lines of Python, the baseline allows 3",
		"FAIL scripts/lib/helper.py is listed with 1 lines but holds no counted Python now")
	code, out = guardOn(dir, "--write-baseline")
	want(t, code, out, 1, "the baseline was not written")
}

// A fall fails until the baseline is lowered, so the room cannot be taken again by a later change.
func TestFallFailsUntilWriteBaselineLowersIt(t *testing.T) {
	dir := seeded(t)
	put(t, dir, tool, "#!/usr/bin/env python3\nprint(1)\n")
	remove(t, dir, "scripts/lib/helper.py")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL "+tool+" holds 2 lines of Python, the baseline still says 3; lower the baseline with --write-baseline",
		"FAIL scripts/lib/helper.py is listed with 1 lines but holds no counted Python now")
	code, out = guardOn(dir, "--write-baseline")
	want(t, code, out, 0, "wrote "+baselinePath, "python_files=8 python_lines=13")
	b, err := parseBaseline([]byte(get(t, dir, baselinePath)), baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.PythonFiles["scripts/lib/helper.py"]; ok || b.PythonFiles[tool] != 2 {
		t.Fatalf("the written baseline holds %v, want %s at 2 and no scripts/lib/helper.py", b.PythonFiles, tool)
	}
	code, out = guardOn(dir)
	if code != 0 || strings.Contains(out, "FAIL") {
		t.Fatalf("after the write the tree passes: exit %d\n%s", code, out)
	}
	put(t, dir, tool, "#!/usr/bin/env python3\nprint(1)\nprint(2)\n")
	code, out = guardOn(dir)
	want(t, code, out, 1, "FAIL "+tool+" holds 3 lines of Python, the baseline allows 2")
}

func TestWriteBaselineRefusesARise(t *testing.T) {
	dir := seeded(t)
	before := get(t, dir, baselinePath)
	put(t, dir, tool, get(t, dir, tool)+"print(3)\n")
	code, out := guardOn(dir, "--write-baseline")
	want(t, code, out, 1, "the baseline was not written")
	if get(t, dir, baselinePath) != before {
		t.Fatal("a refused write changed the baseline")
	}
}

func TestWriteBaselineRefusesANewFile(t *testing.T) {
	dir := seeded(t)
	before := get(t, dir, baselinePath)
	remove(t, dir, "scripts/lib/helper.py")
	put(t, dir, "scripts/other.py", "x = 2\n") // the total stays at 15
	code, out := guardOn(dir, "--write-baseline")
	want(t, code, out, 1, "a new file that holds Python: scripts/other.py", "the baseline was not written")
	if get(t, dir, baselinePath) != before {
		t.Fatal("a refused write changed the baseline")
	}
}

func TestNewInvocationInAChartTemplateFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, template, get(t, dir, template)+"            - sh\n            - -c\n            - python3 -c 'import os'\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL invoke: "+template+" has 2 places, the baseline allows 1 (lines 9, 13)")
}

func TestInvocationInANewChartTemplateFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "platform/app/chart/templates/cron.yaml", "args: [\"python3\", \"/s/x.py\"]\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL a new place where Python runs or is installed: invoke platform/app/chart/templates/cron.yaml (line 1)")
}

func TestNewInstallInAnImageFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "products/other/Dockerfile", "FROM docker.io/library/python:3.12-slim\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL a new place where Python runs or is installed: install products/other/Dockerfile (line 1)")
}

func TestRemovedInvocationFailsUntilTheBaselineIsLowered(t *testing.T) {
	dir := seeded(t)
	put(t, dir, template, strings.Replace(get(t, dir, template), "/usr/bin/python3", "/usr/local/bin/run", 1))
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL invoke: "+template+" has 0 places, the baseline still says 1; lower the baseline")
	code, out = guardOn(dir, "--write-baseline")
	want(t, code, out, 0, "places=3 (install=1 invoke=1 shebang=1)")
	b, err := parseBaseline([]byte(get(t, dir, baselinePath)), baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Sites["invoke"][template]; ok {
		t.Fatalf("the written baseline still lists %s", template)
	}
}

func TestExceptionWithoutAReasonFails(t *testing.T) {
	for name, reason := range map[string]string{"empty": "", "short": "vendored", "two lines": "a tool of a vendor\nthat is Python"} {
		t.Run(name, func(t *testing.T) {
			dir := seeded(t)
			editBaseline(t, dir, func(b *baseline) { b.Exceptions = []exception{{Path: tool, Reason: reason}} })
			code, out := guardOn(dir)
			want(t, code, out, 1, "exceptions[0] ("+tool+"): an exception has a reason of one line and ten characters or more")
		})
	}
}

func TestExceptionWithoutAReasonKeyFails(t *testing.T) {
	dir := seeded(t)
	text := strings.Replace(get(t, dir, baselinePath), `"exceptions": []`, `"exceptions": [{"path": "`+tool+`"}]`, 1)
	put(t, dir, baselinePath, text)
	code, out := guardOn(dir)
	want(t, code, out, 1, "an exception has a reason of one line")
}

func TestExceptionNamesAThirdPartyPythonFile(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "third_party/vendor_tool.py", "print('vendor')\n")
	editBaseline(t, dir, func(b *baseline) {
		b.Exceptions = []exception{{Path: "third_party/vendor_tool.py", Reason: "the vendor ships this tool in Python only"}}
	})
	code, out := guardOn(dir)
	want(t, code, out, 0, "python_files=9 python_lines=15", "exceptions=1")
}

// An exception takes one file out; the other files keep their own numbers.
func TestAnExceptionFreesNoRoomForOtherFiles(t *testing.T) {
	dir := seeded(t)
	if code, out := guardOn(dir, "--write-baseline"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	editBaseline(t, dir, func(b *baseline) {
		b.Exceptions = []exception{{Path: tool, Reason: "the port of this tool to Go is tracked"}}
		delete(b.PythonFiles, tool)
	})
	put(t, dir, "scripts/lib/helper.py", "x = 1\ny = 2\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL scripts/lib/helper.py holds 2 lines of Python, the baseline allows 1", "exceptions=1")
}

func TestExceptionForAMissingOrNonPythonFileFails(t *testing.T) {
	dir := seeded(t)
	editBaseline(t, dir, func(b *baseline) {
		b.Exceptions = []exception{
			{Path: "scripts/gone.py", Reason: "the vendor ships this tool in Python only"},
			{Path: "core/x/main.go", Reason: "the vendor ships this tool in Python only"},
		}
	})
	code, out := guardOn(dir)
	want(t, code, out, 1, "exceptions[0] (scripts/gone.py): Git lists no such file", "exceptions[1] (core/x/main.go): the file holds no Python")
}

func TestBaseRefusesARaisedBaseline(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	editBaseline(t, dir, func(b *baseline) {
		b.PythonFiles[tool] += 10
		b.PythonFiles["scripts/later.py"] = 1
		b.Sites["invoke"]["platform/app/chart/templates/later.yaml"] = 1
	})
	code, out := guardOn(dir, "--base", "HEAD")
	want(t, code, out, 1, "python_files lists "+tool+" with 13, higher than 3 at HEAD. A baseline only falls",
		"python_files lists scripts/later.py, which it did not list at HEAD",
		"sites.invoke lists platform/app/chart/templates/later.yaml with 1, 0 at HEAD")
}

func TestBaseAcceptsALoweredBaseline(t *testing.T) {
	dir := seeded(t)
	commitAll(t, dir)
	remove(t, dir, "scripts/lib/helper.py")
	if code, out := guardOn(dir, "--write-baseline"); code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	code, out := guardOn(dir, "--base", "HEAD")
	want(t, code, out, 0, "python_lines=14")
}

func TestBaseWithoutABaselineHasNothingToCompare(t *testing.T) {
	dir := seeded(t)
	baselineText := get(t, dir, baselinePath)
	remove(t, dir, baselinePath)
	commitAll(t, dir)
	put(t, dir, baselinePath, baselineText)
	code, out := guardOn(dir, "--base", "HEAD")
	want(t, code, out, 0, "does not exist at HEAD; there is nothing to compare")
}

func TestUnreadableInputsCannotBeJudged(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"a missing baseline": func(t *testing.T, dir string) { remove(t, dir, baselinePath) },
		"another schema": func(t *testing.T, dir string) {
			editBaseline(t, dir, func(b *baseline) { b.Schema = "other" })
		},
		"a missing kind": func(t *testing.T, dir string) {
			editBaseline(t, dir, func(b *baseline) { delete(b.Sites, "shebang") })
		},
		"a listed file of 0 lines": func(t *testing.T, dir string) {
			editBaseline(t, dir, func(b *baseline) { b.PythonFiles[tool] = 0 })
		},
		"an unknown key": func(t *testing.T, dir string) {
			put(t, dir, baselinePath, strings.Replace(get(t, dir, baselinePath), `"exceptions"`, `"extra": 1, "exceptions"`, 1))
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
		"infra/providers/x/cloudinit-worker.tftpl":           scopeCloudInit,
		"infra/providers/_shared/cloud-init-control.yaml":    scopeCloudInit,
		"infra/providers/_shared/tests/cloudinit_render.hcl": "",
		"infra/providers/x/provider-substitutes.yaml.tftpl":  "",
		"infra/other/cloudinit-worker.tftpl":                 "",
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
		{"python3 after env", scopeCloudInit, "c.tftpl", "  - /usr/bin/env python3 -c 'import socket'\n", []string{"invoke"}},
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
	}
	for _, c := range cases {
		if got := embedded(c.text); got != c.want {
			t.Errorf("%s: %d lines, want %d", c.name, got, c.want)
		}
	}
}

func TestReadsEmbedded(t *testing.T) {
	cases := map[string]bool{
		"scripts/x.sh": true, ".github/workflows/x.yaml": true, ".github/actions/a/action.yml": true,
		"infra/providers/huawei/main.tf": true, "Makefile": true, "core/x/main.go": false,
		"docs/RUNBOOKS.md": false, "notes.txt": false, "scripts/no-suffix": false,
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
