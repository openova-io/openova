package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	tablePath    = "core/controllers/organization/internal/gitops/plan_quota_plus_platform_stack_test.go"
	workflowPath = ".github/workflows/build-organization-controller.yaml"
)

func table(paths ...string) string {
	var b strings.Builder
	b.WriteString("package gitops\n\n// pinnedSources is read by the guard.\nvar pinnedSources = []string{\n")
	for _, p := range paths {
		b.WriteString("\t\"" + p + "\",\n")
	}
	b.WriteString("}\n\nvar other = []string{\"not/read.go\"}\n")
	return b.String()
}

// mainOn is the on: block of the workflow on main before the repair
// (21 Nov 2026): the controller's own tree only.
const mainOn = `name: Build organization-controller

on:
  push:
    paths:
      - 'core/controllers/organization/**'
      - 'core/controllers/internal/**'
      - 'core/controllers/pkg/**'
      - 'core/controllers/go.mod'
      - 'core/controllers/go.sum'
      - '.github/workflows/build-organization-controller.yaml'
    branches: [main]
  pull_request:
    paths:
      - 'core/controllers/organization/**'
      - 'core/controllers/internal/**'
      - 'core/controllers/pkg/**'
      - 'core/controllers/go.mod'
      - 'core/controllers/go.sum'
      - '.github/workflows/build-organization-controller.yaml'
  workflow_dispatch:

jobs:
  build:
    runs-on: ubuntu-latest
`

// goodOn adds the two pinned files to the pull_request filter (one by name,
// one by a ** glob), with a comment between the entries.
var goodOn = strings.Replace(mainOn, "      - '.github/workflows/build-organization-controller.yaml'\n  workflow_dispatch:",
	"      - '.github/workflows/build-organization-controller.yaml'\n      # the pinned sources\n      - 'products/agenity/chart/**'\n      - \"platform/newapi/chart/values.yaml\" # by name\n  workflow_dispatch:", 1)

func goodTree() map[string]string {
	return map[string]string{
		tablePath:                            table("products/agenity/chart/values.yaml", "platform/newapi/chart/values.yaml", "products/agenity/chart/templates/statefulset.yaml"),
		workflowPath:                         goodOn,
		"products/agenity/chart/values.yaml": "resources: {}\n",
		"products/agenity/chart/templates/statefulset.yaml": "kind: StatefulSet\n",
		"platform/newapi/chart/values.yaml":                 "newapi: {}\n",
	}
}

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, text := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runOn(t *testing.T, files map[string]string) (int, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run([]string{"-root", write(t, files)}, &out, &errw)
	return code, out.String() + errw.String()
}

func with(file, text string) map[string]string {
	tree := goodTree()
	tree[file] = text
	return tree
}

func expect(t *testing.T, files map[string]string, code int, wants ...string) {
	t.Helper()
	got, out := runOn(t, files)
	if got != code {
		t.Fatalf("exit %d, want %d\n%s", got, code, out)
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q\n%s", w, out)
		}
	}
}

func TestTheRepairPasses(t *testing.T) {
	expect(t, goodTree(), 0, "OK pinnedSources: 3 of 3 sources in "+tablePath, "every pinned source runs the tests that pin it")
}

func TestMainBeforeTheRepairFails(t *testing.T) {
	expect(t, with(workflowPath, mainOn), 1,
		"FAIL uncovered "+workflowPath+": a change to products/agenity/chart/values.yaml",
		"FAIL uncovered "+workflowPath+": a change to platform/newapi/chart/values.yaml",
		"FAIL uncovered "+workflowPath+": a change to products/agenity/chart/templates/statefulset.yaml",
		"OK pinnedSources: 0 of 3")
}

func TestThePushFilterDoesNotCount(t *testing.T) {
	// The pinned paths in push only: still uncovered on pull_request.
	wf := strings.Replace(mainOn, "    branches: [main]", "      - 'products/agenity/chart/**'\n      - 'platform/newapi/chart/**'\n    branches: [main]", 1)
	expect(t, with(workflowPath, wf), 1, "FAIL uncovered "+workflowPath+": a change to products/agenity/chart/values.yaml")
}

func TestASingleStarDoesNotCrossASlash(t *testing.T) {
	wf := strings.Replace(goodOn, "'products/agenity/chart/**'", "'products/agenity/chart/*'", 1)
	expect(t, with(workflowPath, wf), 1, "FAIL uncovered "+workflowPath+": a change to products/agenity/chart/templates/statefulset.yaml")
	if got, out := runOn(t, with(workflowPath, wf)); got != 1 || strings.Contains(out, "a change to products/agenity/chart/values.yaml") {
		t.Errorf("chart/* must still cover chart/values.yaml (exit %d)\n%s", got, out)
	}
}

func TestANegationExcludes(t *testing.T) {
	wf := strings.Replace(goodOn, "      # the pinned sources\n", "      # the pinned sources\n      - 'platform/**'\n", 1)
	wf = strings.Replace(wf, "      - \"platform/newapi/chart/values.yaml\" # by name\n", "      - '!platform/newapi/**'\n", 1)
	expect(t, with(workflowPath, wf), 1, "FAIL uncovered "+workflowPath+": a change to platform/newapi/chart/values.yaml")
}

func TestPathsIgnoreExcludes(t *testing.T) {
	wf := strings.Replace(mainOn, "  pull_request:\n    paths:\n", "  pull_request:\n    paths-ignore:\n      - 'platform/**'\n    other:\n", 1)
	expect(t, with(workflowPath, wf), 1, "FAIL uncovered "+workflowPath+": a change to platform/newapi/chart/values.yaml")
	if _, out := runOn(t, with(workflowPath, wf)); strings.Contains(out, "a change to products/agenity/chart/values.yaml") {
		t.Errorf("a path paths-ignore does not name runs the workflow\n%s", out)
	}
}

func TestNoFilterRunsEveryPath(t *testing.T) {
	for name, wf := range map[string]string{
		"block, no paths": "on:\n  pull_request:\n  push:\n    branches: [main]\n",
		"inline list":     "on: [push, pull_request]\n",
		"inline scalar":   "on: pull_request\n",
	} {
		t.Run(name, func(t *testing.T) { expect(t, with(workflowPath, wf), 0, "OK pinnedSources: 3 of 3") })
	}
}

func TestAWorkflowThatDoesNotRunOnPullRequestsFails(t *testing.T) {
	for name, wf := range map[string]string{
		"block": "on:\n  push:\n    paths:\n      - '**'\n",
		"list":  "on: [push]\n",
		"none":  "name: x\n",
	} {
		t.Run(name, func(t *testing.T) { expect(t, with(workflowPath, wf), 1, "FAIL event "+workflowPath) })
	}
}

func TestAnUnreadPatternFails(t *testing.T) {
	wf := strings.Replace(goodOn, "'products/agenity/chart/**'", "'products/agenity/chart/[a-z]*/**'", 1)
	expect(t, with(workflowPath, wf), 1, `FAIL pattern `+workflowPath+`: pattern "products/agenity/chart/[a-z]*/**"`)
}

func TestAStaleEntryFails(t *testing.T) {
	tree := goodTree()
	delete(tree, "platform/newapi/chart/values.yaml")
	expect(t, tree, 1, "FAIL missing "+tablePath+": pinnedSources lists platform/newapi/chart/values.yaml, which is not a file of the tree")
}

func TestTheTableMustBeLiteral(t *testing.T) {
	for name, src := range map[string]string{
		"missing":     "package gitops\n\nvar other = []string{\"a\"}\n",
		"empty":       "package gitops\n\nvar pinnedSources = []string{}\n",
		"not literal": "package gitops\n\nconst p = \"platform/newapi/chart/values.yaml\"\n\nvar pinnedSources = []string{p}\n",
		"twice":       table("platform/newapi/chart/values.yaml", "platform/newapi/chart/values.yaml"),
		"not strings": "package gitops\n\nvar pinnedSources = map[string]bool{\"a\": true}\n",
	} {
		t.Run(name, func(t *testing.T) { expect(t, with(tablePath, src), 1, "FAIL table "+tablePath) })
	}
}

func TestAnUnreadableFileStops(t *testing.T) {
	tree := goodTree()
	delete(tree, workflowPath)
	if code, out := runOn(t, tree); code != 2 {
		t.Fatalf("exit %d, want 2\n%s", code, out)
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, path string
		want          bool
	}{
		{"core/**", "core/a/b.go", true},
		{"core/*", "core/a/b.go", false},
		{"core/*", "core/a.go", true},
		{"**/values.yaml", "platform/x/chart/values.yaml", true},
		{"platform/x/chart/values.yaml", "platform/x/chart/values.yaml", true},
		{"platform/x/chart/values.yaml", "platform/x/chart/values.yaml.bak", false},
		{"a.b", "aXb", false},
	} {
		if got := match(c.pattern, c.path); got != c.want {
			t.Errorf("match(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}
