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
	tool      = "scripts/tool.py"
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
		cloudInit:        "runcmd:\n  - python3 -c 'print(1)'\n",
		image:            "FROM alpine:3.20\nRUN apk add --no-cache bash python3\n",
		"core/x/main.go": "package main\n\nfunc main() {}\n",
		"docs/README.md": "Run python3 scripts/tool.py.\n",
	}
}

// The fixture holds 4 Python files of 3 + 1 + 1 + 3 lines, and 4 places.
const fixtureSummary = "python_files=4 python_lines=8 places=4 (install=1 invoke=2 shebang=1) exceptions=0"

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
	want(t, code, out, 1, "FAIL a new Python file: scripts/new_tool.py (1 lines)", "New tooling is written in Go")
}

func TestNewPythonScriptWithoutSuffixFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "scripts/new-tool", "#!/usr/bin/env python3\nprint('new')\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL a new Python file: scripts/new-tool (2 lines)")
}

func TestShellScriptIsNotPython(t *testing.T) {
	dir := seeded(t)
	put(t, dir, "scripts/new-tool.sh", "#!/bin/sh\n# python3 is not called here\necho ok\n")
	code, out := guardOn(dir)
	want(t, code, out, 0, fixtureSummary)
}

func TestGrowthOfTheTotalFails(t *testing.T) {
	dir := seeded(t)
	put(t, dir, tool, get(t, dir, tool)+"print(3)\n")
	code, out := guardOn(dir)
	want(t, code, out, 1, "FAIL the Python files hold 9 lines, the baseline allows 8")
}

func TestFallPassesAndWriteBaselineLowersIt(t *testing.T) {
	dir := seeded(t)
	put(t, dir, tool, "#!/usr/bin/env python3\nprint(1)\n")
	remove(t, dir, "scripts/lib/helper.py")
	code, out := guardOn(dir)
	want(t, code, out, 0, "the Python files hold 6 lines, the baseline still says 8", "scripts/lib/helper.py is listed but is no longer counted")
	code, out = guardOn(dir, "--write-baseline")
	want(t, code, out, 0, "wrote "+baselinePath, "python_files=3 python_lines=6")
	b, err := parseBaseline([]byte(get(t, dir, baselinePath)), baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if *b.PythonLines != 6 || len(b.PythonFiles) != 3 {
		t.Fatalf("the written baseline holds %d lines and %d files, want 6 and 3", *b.PythonLines, len(b.PythonFiles))
	}
	code, out = guardOn(dir)
	if code != 0 || strings.Contains(out, "note:") {
		t.Fatalf("after the write the tree passes without a note: exit %d\n%s", code, out)
	}
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
	put(t, dir, "scripts/other.py", "x = 2\n") // the total stays at 8
	code, out := guardOn(dir, "--write-baseline")
	want(t, code, out, 1, "a new Python file: scripts/other.py", "the baseline was not written")
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

func TestRemovedInvocationPassesAndLowersTheBaseline(t *testing.T) {
	dir := seeded(t)
	put(t, dir, template, strings.Replace(get(t, dir, template), "/usr/bin/python3", "/usr/local/bin/run", 1))
	code, out := guardOn(dir)
	want(t, code, out, 0, "invoke: "+template+" has 0 places, the baseline still says 1")
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
	want(t, code, out, 0, "python_files=4 python_lines=8", "exceptions=1")
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
		n := *b.PythonLines + 10
		b.PythonLines = &n
		b.PythonFiles = append(b.PythonFiles, "scripts/later.py")
		b.Sites["invoke"]["platform/app/chart/templates/later.yaml"] = 1
	})
	code, out := guardOn(dir, "--base", "HEAD")
	want(t, code, out, 1, "python_lines is 18, higher than 8 at HEAD. A baseline only falls",
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
	want(t, code, out, 0, "python_lines=7")
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
