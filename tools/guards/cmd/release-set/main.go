// Command release-set holds that a push which changes a chart another chart builds in by a
// file:// dependency releases every chart that builds it in, each with its own next version.
//
// `helm dependency build` packs the folder of each file:// dependency into the chart that names
// it, and a library chart (type: library) is never published by itself. On 27 Nov 2026 SEC-219
// (pull request 9073) changed platform/secrets-lib/chart and on 28 Nov SEC-226 (9090) changed it
// again. Both times the push run of .github/workflows/blueprint-release.yaml built the library
// alone, products/catalyst and then products/axon kept a published version without the change,
// and the cut of a candidate (scripts/release-candidate.sh, whose check
// scripts/check-chart-content-after-version.py follows each chart's file:// dependencies)
// refused main. Checks:
//
//	set   the step "Detect changed Blueprints" (id changed), run as a push run runs it on a
//	      fixture tree: a change of a library releases it and every chart that builds it in,
//	      directly or through another library, in both trees, whatever the quotes of the
//	      repository line; a change of a chart folder that another chart names in a deeper
//	      folder releases that chart; a change of a chart nobody builds in releases that chart
//	      alone; a dependency inside the chart folder (file://charts/x) or a commented one makes
//	      no consumer; a path outside a chart releases nothing; a dispatched run releases the
//	      Blueprint it names.
//	bump  the step "Release writer: bump the chart version" (id bump_chart), run as a push run
//	      runs it after a push that changed only the library: it writes the next version of a
//	      chart that builds the library in, and of products/catalyst too (its build workflow
//	      runs only on a change of its chart folder); it leaves a chart that does not build it
//	      in as it is; and it defers products/catalyst to its build workflow when the push
//	      changed its chart folder.
//	tree  the step on a copy of this repository's charts: for each library chart, a push that
//	      changes a file of it releases the library and every chart that the cut's own check
//	      then refuses (it reads each chart's file:// dependencies its own way), printed with
//	      their number; for the first chart in name order that the cut alone refuses, that
//	      chart alone. A chart the release adds beyond the cut's (one that builds the library
//	      in through another library, which the cut does not follow) is printed, not a finding.
//
// The steps are read from the workflow, never copied: their env and run, with the ${{ }}
// expressions of a push run (inputs.blueprint, inputs.tree, github.event.before, github.sha,
// matrix.path) given by the case. They run with bash -e, as a step without a shell runs.
// It needs bash, git, jq, sed, awk and python3 (the cut's check and the release writer's
// version comparison, scripts/lib/version_order.py).
//
//	cd tools/guards && go run ./cmd/release-set [-workflow <file>] [-checks set,bump,tree] [-scratch <dir>] [-keep]
//
// Exit 0: every check holds. Exit 1: findings. Exit 2: could not be prepared.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	workflowRel = ".github/workflows/blueprint-release.yaml"
	detectID    = "changed"
	writerID    = "bump_chart"
	cutRel      = "scripts/check-chart-content-after-version.py"
	probeRel    = "templates/zz-release-set-probe.tpl"
	// The charts the tree check tries, in name order, for one that nobody builds in.
	nobodyTries = 10
)

// Files of the repository the two steps run, copied into each fixture tree when they exist.
var stepFiles = []string{"scripts/lib/chart-file-deps.sh", "scripts/lib/version_order.py"}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type options struct {
	root string
	repo map[string]string // stepFiles that exist, by path
}

func run(args []string, out, errw io.Writer) int {
	fs := flag.NewFlagSet("release-set", flag.ContinueOnError)
	fs.SetOutput(errw)
	workflow := fs.String("workflow", "", "the workflow file (default: "+workflowRel+" of the repository)")
	checks := fs.String("checks", "set,bump,tree", "the checks to run, separated by commas")
	scratch := fs.String("scratch", "", "the folder for the fixture trees (default: a new temporary folder)")
	keep := fs.Bool("keep", false, "keep the scratch folder")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	want := map[string]bool{}
	for _, c := range strings.Split(*checks, ",") {
		switch c {
		case "set", "bump", "tree":
			want[c] = true
		default:
			say(errw, "release-set: unknown check %q (set, bump, tree)\n", c)
			return 2
		}
	}
	root, err := command("", nil, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		say(errw, "release-set: %v\n", err)
		return 2
	}
	o := options{root: strings.TrimSpace(root), repo: map[string]string{}}
	for _, rel := range stepFiles {
		b, err := os.ReadFile(filepath.Clean(filepath.Join(o.root, rel)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			say(errw, "release-set: %v\n", err)
			return 2
		}
		o.repo[rel] = string(b)
	}
	if *workflow == "" {
		*workflow = filepath.Join(o.root, workflowRel)
	}
	text, err := os.ReadFile(filepath.Clean(*workflow))
	if err != nil {
		say(errw, "release-set: %v\n", err)
		return 2
	}
	steps := map[string]step{}
	for _, id := range []string{detectID, writerID} {
		st, err := extractStep(string(text), id)
		if err == nil {
			// The expressions the cases give the step: one it uses beyond them stops the command.
			vals := pushVals("", "")
			if id == writerID {
				vals["matrix.path"] = ""
			}
			_, _, err = st.script(vals)
		}
		if err != nil {
			say(errw, "release-set: %s: %v\n", *workflow, err)
			return 2
		}
		steps[id] = st
	}
	base := *scratch
	if base == "" {
		if base, err = os.MkdirTemp("", "release-set-"); err != nil {
			say(errw, "release-set: %v\n", err)
			return 2
		}
	} else if err := os.MkdirAll(base, 0o750); err != nil {
		say(errw, "release-set: %v\n", err)
		return 2
	}
	if !*keep {
		defer func() { _ = os.RemoveAll(base) }()
	}
	var findings []string
	for _, c := range []struct {
		name string
		fn   func() ([]string, error)
	}{
		{"set", func() ([]string, error) { return checkSet(o, steps[detectID], filepath.Join(base, "set"), out) }},
		{"bump", func() ([]string, error) { return checkBump(o, steps[writerID], filepath.Join(base, "bump"), out) }},
		{"tree", func() ([]string, error) { return checkTree(o, steps[detectID], filepath.Join(base, "tree"), out) }},
	} {
		if !want[c.name] {
			continue
		}
		f, err := c.fn()
		if err != nil {
			say(errw, "release-set: %s: %v\n", c.name, err)
			return 2
		}
		findings = append(findings, f...)
	}
	for _, f := range findings {
		say(out, "FAIL %s\n", f)
	}
	say(out, "release-set: %d finding(s) in the checks %s of %s\n", len(findings), *checks, *workflow)
	if len(findings) > 0 {
		say(out, "release-set: a push that changes a chart another chart builds in by a file:// dependency must release every chart that builds it in, each with its own next version.\n")
		return 1
	}
	return 0
}

// ── check set ─────────────────────────────────────────────────────────────────

// The fixture of the set check: a library a, a library b that builds a in, charts that build
// them in with every form of the repository line the cut's check reads, and charts that do not.
func setFixture() map[string]string {
	dep := func(name, repo string) string {
		return "dependencies:\n  - name: " + name + "\n    version: 0.1.0\n    repository: " + repo + "\n"
	}
	return map[string]string{
		"platform/a/chart/Chart.yaml":                  "apiVersion: v2\nname: a\nversion: 0.1.0\ntype: library\n",
		"platform/a/chart/templates/_a.tpl":            "{{- define \"a\" -}}a{{- end -}}\n",
		"platform/b/chart/Chart.yaml":                  "apiVersion: v2\nname: b\nversion: 0.1.0\ntype: \"library\"\n" + dep("a", "\"file://../../a/chart\""),
		"platform/b/chart/templates/_b.tpl":            "{{- define \"b\" -}}b{{- end -}}\n",
		"platform/c/chart/Chart.yaml":                  "apiVersion: v2\nname: bp-c\nversion: 1.0.0\n" + dep("a", "file://../../a/chart"),
		"platform/d/chart/Chart.yaml":                  "apiVersion: v2\nname: bp-d\nversion: 1.0.0\ndependencies:\n  - name: b\n    version: 0.1.0\n    repository:\t'file://../../b/chart' # through b\n",
		"platform/e/chart/Chart.yaml":                  "apiVersion: v2\nname: bp-e\nversion: 1.0.0\n",
		"platform/e/chart/values.yaml":                 "e: 1\n",
		"platform/g/chart/Chart.yaml":                  "apiVersion: v2\nname: bp-g\nversion: 1.0.0\n" + dep("vendored", "\"file://charts/vendored\""),
		"platform/g/chart/charts/vendored/Chart.yaml":  "apiVersion: v2\nname: vendored\nversion: 0.1.0\n",
		"platform/g/chart/charts/vendored/values.yaml": "v: 1\n",
		"platform/h/chart/Chart.yaml":                  "apiVersion: v2\nname: bp-h\nversion: 1.0.0\n# repository: \"file://../../a/chart\" (not a dependency)\n",
		"products/f/chart/Chart.yaml":                  "apiVersion: v2\nname: bp-f\nversion: 1.0.0\ndependencies:\n  - repository: \"file://../../../platform/a/chart\"\n    name: a\n    version: 0.1.0\n",
		"products/q/chart/Chart.yaml":                  "apiVersion: v2\nname: bp-q\nversion: 1.0.0\n" + dep("docs", "\"file://../docs/chart\""),
		"products/q/docs/chart/Chart.yaml":             "apiVersion: v2\nname: docs\nversion: 0.1.0\n",
		"products/q/docs/chart/templates/cm.yaml":      "kind: ConfigMap\n",
		"docs/notes.md":                                "notes\n",
	}
}

func checkSet(o options, detect step, dir string, out io.Writer) ([]string, error) {
	tree := filepath.Join(dir, "tree")
	files := setFixture()
	for rel, text := range o.repo {
		files[rel] = text
	}
	if err := newRepo(tree, files); err != nil {
		return nil, err
	}
	var findings []string
	for _, c := range []struct {
		label, file string
		want        []string
	}{
		{"a change of the library a, which b, c, products/f and through b d build in", "platform/a/chart/templates/_a.tpl",
			[]string{"platform/a", "platform/b", "platform/c", "platform/d", "products/f"}},
		{"a change of the library b, which d builds in", "platform/b/chart/templates/_b.tpl", []string{"platform/b", "platform/d"}},
		{"a change of the chart e, which nobody builds in", "platform/e/chart/values.yaml", []string{"platform/e"}},
		{"a change inside g's own folder (file://charts/vendored)", "platform/g/chart/charts/vendored/values.yaml", []string{"platform/g"}},
		{"a change of products/q/docs/chart, which products/q builds in", "products/q/docs/chart/templates/cm.yaml", []string{"products/q"}},
		{"a change outside every chart", "docs/notes.md", nil},
	} {
		before, err := command(tree, gitEnv(tree), "git", "rev-parse", "HEAD")
		if err != nil {
			return nil, err
		}
		after, err := commitChange(tree, c.file)
		if err != nil {
			return nil, err
		}
		vals := pushVals(strings.TrimSpace(before), after)
		findings = append(findings, judgeSet(detect, tree, vals, "set: "+c.label, c.want, out)...)
	}
	head, err := command(tree, gitEnv(tree), "git", "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	for _, c := range []struct {
		label, blueprint, tree string
		want                   []string
	}{
		{"a run dispatched for products/catalyst", "catalyst", "products", []string{"products/catalyst"}},
		{"a run dispatched for e, the tree left at its default", "e", "", []string{"platform/e"}},
	} {
		vals := map[string]string{"inputs.blueprint": c.blueprint, "inputs.tree": c.tree, "github.event.before": "", "github.sha": strings.TrimSpace(head)}
		findings = append(findings, judgeSet(detect, tree, vals, "set: "+c.label, c.want, out)...)
	}
	return findings, nil
}

// judgeSet runs the step and compares the Blueprint folders of its matrix with want.
func judgeSet(detect step, tree string, vals map[string]string, label string, want []string, out io.Writer) []string {
	got, res, err := releaseSet(detect, tree, vals)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v\n%s", label, err, tail(res.out, 12))}
	}
	if missing, extra := diff(want, got); len(missing)+len(extra) > 0 {
		return []string{fmt.Sprintf("%s: the release set is [%s], want [%s] (lacks [%s], has beyond [%s])",
			label, strings.Join(got, " "), strings.Join(want, " "), strings.Join(missing, " "), strings.Join(extra, " "))}
	}
	say(out, "ok   %s: the release set is [%s]\n", label, strings.Join(got, " "))
	return nil
}

// releaseSet runs the detect step and reads the Blueprint folders of the matrix it wrote.
func releaseSet(detect step, tree string, vals map[string]string) ([]string, result, error) {
	res, err := runStep(detect, tree, vals, nil, "")
	if err != nil {
		return nil, res, err
	}
	if res.err != nil {
		return nil, res, fmt.Errorf("the step %s failed: %v", detect.id, res.err)
	}
	var matrix string
	for _, l := range strings.Split(res.outputs, "\n") {
		if v, ok := strings.CutPrefix(l, "matrix="); ok {
			matrix = v
		}
	}
	var m struct {
		Include []struct {
			Path string `json:"path"`
		} `json:"include"`
	}
	if err := json.Unmarshal([]byte(matrix), &m); err != nil {
		return nil, res, fmt.Errorf("the step %s wrote no matrix that reads as JSON (%q): %v", detect.id, matrix, err)
	}
	var got []string
	for _, e := range m.Include {
		got = append(got, e.Path)
	}
	sort.Strings(got)
	return got, res, nil
}

// ── check bump ────────────────────────────────────────────────────────────────

func checkBump(o options, writer step, dir string, out io.Writer) ([]string, error) {
	tree := filepath.Join(dir, "tree")
	files := map[string]string{
		"platform/lib/chart/Chart.yaml":         "apiVersion: v2\nname: lib\nversion: 0.1.0\ntype: library\n",
		"platform/lib/chart/templates/_lib.tpl": "{{- define \"lib\" -}}lib{{- end -}}\n",
		"platform/c/chart/Chart.yaml":           "apiVersion: v2\nname: bp-c\nversion: 1.0.0\ndependencies:\n  - name: lib\n    version: 0.1.0\n    repository: \"file://../../lib/chart\"\n",
		"platform/e/chart/Chart.yaml":           "apiVersion: v2\nname: bp-e\nversion: 2.0.0\n",
		"products/catalyst/chart/Chart.yaml":    "apiVersion: v2\nname: bp-catalyst-platform\nversion: 1.4.0\ndependencies:\n  - name: lib\n    version: 0.1.0\n    repository: \"file://../../../platform/lib/chart\"\n",
		"products/catalyst/chart/values.yaml":   "v: 1\n",
	}
	for rel, text := range o.repo {
		files[rel] = text
	}
	if err := newRepo(tree, files); err != nil {
		return nil, err
	}
	env := gitEnv(tree)
	origin := tree + ".git"
	for _, args := range [][]string{{"init", "-q", "--bare", "-b", "main", origin}, {"remote", "add", "origin", origin}, {"push", "-q", "origin", "main"}} {
		if _, err := command(tree, env, "git", args...); err != nil {
			return nil, err
		}
	}
	// The registry stand-in: it holds no version, so the next version is the one after main's.
	stub := filepath.Join(dir, "registry-stand-in")
	if err := os.MkdirAll(stub, 0o750); err != nil {
		return nil, err
	}
	if err := writeExec(filepath.Join(stub, "helm"), "#!/bin/sh\necho \"Error: $2: not found\" >&2\nexit 1\n"); err != nil {
		return nil, err
	}
	c0, err := command(tree, env, "git", "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	c1, err := commitChange(tree, "platform/lib/chart/templates/_lib.tpl")
	if err != nil {
		return nil, err
	}
	if _, err := command(tree, env, "git", "push", "-q", "origin", "main"); err != nil {
		return nil, err
	}
	var findings []string
	for _, c := range []struct {
		label, path, want string
		bumped, defer_    bool
	}{
		{"a push that changed only the library platform/c builds in", "platform/c", "1.0.1", true, false},
		{"the same push, for products/catalyst, which builds it in and whose build workflow does not run", "products/catalyst", "1.4.1", true, false},
		{"the same push, for platform/e, which does not build it in", "platform/e", "2.0.0", false, false},
	} {
		findings = append(findings, judgeBump(writer, tree, stub, strings.TrimSpace(c0), c1, c.label, c.path, c.want, c.bumped, c.defer_, out)...)
	}
	if err := writeFiles(tree, map[string]string{"products/catalyst/chart/values.yaml": "v: 2\n"}); err != nil {
		return nil, err
	}
	c2, err := commitChange(tree, "platform/lib/chart/templates/_lib.tpl")
	if err != nil {
		return nil, err
	}
	if _, err := command(tree, env, "git", "push", "-q", "origin", "main"); err != nil {
		return nil, err
	}
	findings = append(findings, judgeBump(writer, tree, stub, c1, c2,
		"a push that changed the library and products/catalyst/chart, for products/catalyst", "products/catalyst", "1.4.0", false, true, out)...)
	return findings, nil
}

// judgeBump runs the release writer of a push run for one chart and reads what it wrote.
func judgeBump(writer step, tree, stub, before, after, label, path, want string, bumped, deferred bool, out io.Writer) []string {
	label = "bump: " + label
	vals := pushVals(before, after)
	vals["matrix.path"] = path
	res, err := runStep(writer, tree, vals, nil, stub)
	defer func() { _, _ = command(tree, gitEnv(tree), "git", "checkout", "-q", "--", ".") }()
	if err != nil || res.err != nil {
		return []string{fmt.Sprintf("%s: the step %s failed: %v %v\n%s", label, writer.id, err, res.err, tail(res.out, 12))}
	}
	b, err := os.ReadFile(filepath.Clean(filepath.Join(tree, path, "chart", "Chart.yaml")))
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", label, err)}
	}
	got := chartVersion(string(b))
	gotBumped := strings.Contains(res.outputs, "bumped=true")
	gotDefer := strings.Contains(res.outputs, "defer=true")
	if got != want || gotBumped != bumped || gotDefer != deferred {
		return []string{fmt.Sprintf("%s: the release writer left %s %s with bumped=%v defer=%v; want %s with bumped=%v defer=%v\n%s",
			label, path, got, gotBumped, gotDefer, want, bumped, deferred, tail(res.out, 6))}
	}
	say(out, "ok   %s: %s %s, bumped=%v defer=%v\n", label, path, got, gotBumped, gotDefer)
	return nil
}

var chartVersionRe = regexp.MustCompile(`(?m)^version:[ \t]*["']?([^"'\s#]+)`)

func chartVersion(text string) string {
	if m := chartVersionRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// ── check tree ────────────────────────────────────────────────────────────────

var (
	chartYAMLRe = regexp.MustCompile(`^(platform|products)/[^/]+/chart/Chart\.yaml$`)
	// The cut's check reads a library the same way (TOP_TYPE).
	libraryRe = regexp.MustCompile(`(?m)^type:[ \t]*["']?library\b`)
)

// chartsCopy is the copy of this repository's charts the tree check runs on: a repository of
// its own at the commit base, its library charts and its other charts in name order, and what
// the cut's check refuses at base itself (a chart without a version line), which no push changes.
type chartsCopy struct {
	tree, base                 string
	libraries, charts, already []string
}

// copyCharts copies every Chart.yaml of platform and products, and the files the steps run,
// into a new repository at dir/tree.
func copyCharts(o options, cut, dir string) (chartsCopy, error) {
	listed, err := command(o.root, nil, "git", "ls-files", "--", "platform", "products")
	if err != nil {
		return chartsCopy{}, err
	}
	files := map[string]string{}
	var charts, libraries []string
	for _, rel := range strings.Split(strings.TrimSpace(listed), "\n") {
		if filepath.Base(rel) != "Chart.yaml" {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(filepath.Join(o.root, rel)))
		if err != nil {
			return chartsCopy{}, err
		}
		files[rel] = string(b)
		if chartYAMLRe.MatchString(rel) {
			chart := strings.TrimSuffix(rel, "/Chart.yaml")
			if libraryRe.Match(b) {
				libraries = append(libraries, chart)
			} else {
				charts = append(charts, chart)
			}
		}
	}
	if len(libraries) == 0 {
		return chartsCopy{}, errors.New("no chart of platform/*/chart or products/*/chart says type: library: there is nothing to judge")
	}
	for rel, text := range o.repo {
		files[rel] = text
	}
	tree := filepath.Join(dir, "tree")
	if err := newRepo(tree, files); err != nil {
		return chartsCopy{}, err
	}
	base, err := command(tree, gitEnv(tree), "git", "rev-parse", "HEAD")
	if err != nil {
		return chartsCopy{}, err
	}
	base = strings.TrimSpace(base)
	// What the cut refuses at the copy itself (a chart without a version line): not a change.
	already, err := refused(cut, tree, base)
	if err != nil {
		return chartsCopy{}, err
	}
	sort.Strings(libraries)
	sort.Strings(charts)
	return chartsCopy{tree: tree, base: base, libraries: libraries, charts: charts, already: already}, nil
}

func checkTree(o options, detect step, dir string, out io.Writer) ([]string, error) {
	cut := filepath.Join(o.root, cutRel)
	cp, err := copyCharts(o, cut, dir)
	if err != nil {
		return nil, err
	}
	tree, base, already, libraries, charts := cp.tree, cp.base, cp.already, cp.libraries, cp.charts
	var findings []string
	for _, lib := range libraries {
		got, cutSet, err := probe(detect, cut, tree, base, lib, already)
		if err != nil {
			return nil, err
		}
		bp := strings.TrimSuffix(lib, "/chart")
		var lacks []string
		for _, c := range append([]string{bp}, cutSet...) {
			if !contains(got, c) {
				lacks = append(lacks, c)
			}
		}
		if len(lacks) > 0 {
			findings = append(findings, fmt.Sprintf("tree: a push that changes %s/%s: the cut's check then refuses [%s], and the release set [%s] lacks [%s]",
				lib, probeRel, strings.Join(cutSet, " "), strings.Join(got, " "), strings.Join(lacks, " ")))
			continue
		}
		say(out, "ok   tree: a push that changes %s releases it and the %d chart(s) that build it in, the charts the cut's check refuses until they are released again: %s\n",
			lib, len(cutSet), strings.Join(cutSet, " "))
		if _, extra := diff(append([]string{bp}, cutSet...), got); len(extra) > 0 {
			say(out, "     beyond the cut's check (they build it in through another dependency): %s\n", strings.Join(extra, " "))
		}
	}
	for i, chart := range charts {
		if i == nobodyTries {
			return nil, fmt.Errorf("the first %d charts in name order are each built in by another chart: no chart nobody builds in to judge", nobodyTries)
		}
		got, cutSet, err := probe(detect, cut, tree, base, chart, already)
		if err != nil {
			return nil, err
		}
		bp := strings.TrimSuffix(chart, "/chart")
		if len(cutSet) != 1 || cutSet[0] != bp {
			continue
		}
		if len(got) != 1 || got[0] != bp {
			findings = append(findings, fmt.Sprintf("tree: a push that changes %s/%s, a chart nobody builds in: the release set is [%s], want [%s]",
				chart, probeRel, strings.Join(got, " "), bp))
		} else {
			say(out, "ok   tree: a push that changes %s, which nobody builds in, releases it alone\n", chart)
		}
		break
	}
	return findings, nil
}

// probe commits a file in chart on top of base and returns the release set of that push and the
// Blueprint folders the cut's check then refuses, less those it refused already.
func probe(detect step, cut, tree, base, chart string, already []string) ([]string, []string, error) {
	head, err := changeChart(tree, base, chart)
	if err != nil {
		return nil, nil, err
	}
	got, res, err := releaseSet(detect, tree, pushVals(base, head))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v\n%s", chart, err, tail(res.out, 12))
	}
	cutSet, err := refusedBeyond(cut, tree, head, already)
	if err != nil {
		return nil, nil, err
	}
	return got, cutSet, nil
}

// changeChart commits a file in chart on top of base and returns the commit.
func changeChart(tree, base, chart string) (string, error) {
	if _, err := command(tree, gitEnv(tree), "git", "checkout", "-q", "--detach", base); err != nil {
		return "", err
	}
	return commitChange(tree, chart+"/"+probeRel)
}

// refusedBeyond returns, in name order, the Blueprint folders the cut's check refuses at commit
// less already. At the commit of changeChart these are the charts that build the changed chart
// in, as the cut reads each chart's file:// dependencies: the tree check judges a release set
// against them.
func refusedBeyond(cut, tree, commit string, already []string) ([]string, error) {
	set, err := refused(cut, tree, commit)
	if err != nil {
		return nil, err
	}
	_, set = diff(already, set)
	return set, nil
}

var failRe = regexp.MustCompile(`(?m)^FAIL ((?:platform|products)/[^/:]+)/chart: `)

// refused runs the cut's check at commit and returns the Blueprint folders it refuses.
func refused(cut, tree, commit string) ([]string, error) {
	cmd := exec.Command("python3", cut, "--repo", tree, commit) // #nosec G204 -- the cut's check of the tree on the guard's own copy
	cmd.Env = gitEnv(tree)
	b, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		return nil, fmt.Errorf("%s at %s: %v\n%s", cutRel, commit, err, tail(string(b), 12))
	}
	var set []string
	for _, m := range failRe.FindAllStringSubmatch(string(b), -1) {
		set = append(set, m[1])
	}
	sort.Strings(set)
	return set, nil
}

// ── the step, read from the workflow ─────────────────────────────────────────

type step struct {
	id, body string
	env      []envVar
}

// envVar is an entry of the step's env, its value as the workflow writes it.
type envVar struct{ key, value string }

var exprRe = regexp.MustCompile(`\$\{\{\s*([^}]*?)\s*\}\}`)

// script gives the run and env of the step, each ${{ <expr> }} replaced by vals[<expr>]; an
// expression vals has no value for stops the command.
func (s step) script(vals map[string]string) (string, []string, error) {
	var bad error
	resolve := func(text string) string {
		return exprRe.ReplaceAllStringFunc(text, func(m string) string {
			k := exprRe.FindStringSubmatch(m)[1]
			v, ok := vals[k]
			if !ok && bad == nil {
				bad = fmt.Errorf("the step %s uses ${{ %s }}, which this command has no value for", s.id, k)
			}
			return v
		})
	}
	body := resolve(s.body)
	var env []string
	for _, kv := range s.env {
		v := strings.TrimSpace(kv.value)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env = append(env, kv.key+"="+resolve(v))
	}
	return body, env, bad
}

// extractStep reads the step with the given id from a workflow: its env and its run script. It
// reads the block YAML workflows are written in by indentation: the step is the item that holds
// `id: <id>`, and its run is a block scalar (`run: |`), dedented as YAML does.
func extractStep(text, id string) (step, error) {
	lines := strings.Split(text, "\n")
	at := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "id: "+id {
			if at >= 0 {
				return step{}, fmt.Errorf("two steps have the id %s (lines %d and %d)", id, at+1, i+1)
			}
			at = i
		}
	}
	if at < 0 {
		return step{}, fmt.Errorf("no step has the id %s", id)
	}
	ind := indentOf(lines[at])
	start := at
	for start > 0 && (indentOf(lines[start]) != ind-2 || !strings.HasPrefix(strings.TrimSpace(lines[start]), "- ")) {
		start--
	}
	end := at + 1
	for end < len(lines) && (strings.TrimSpace(lines[end]) == "" || indentOf(lines[end]) >= ind) {
		end++
	}
	st := step{id: id}
	for i := start + 1; i < end; i++ {
		if indentOf(lines[i]) != ind {
			continue
		}
		switch key := strings.TrimSpace(lines[i]); {
		case key == "env:":
			for j := i + 1; j < end && (strings.TrimSpace(lines[j]) == "" || indentOf(lines[j]) > ind); j++ {
				kv := strings.TrimSpace(lines[j])
				if kv == "" || strings.HasPrefix(kv, "#") {
					continue
				}
				k, v, ok := strings.Cut(kv, ":")
				if !ok {
					return step{}, fmt.Errorf("line %d: an env entry without a colon: %q", j+1, kv)
				}
				st.env = append(st.env, envVar{key: strings.TrimSpace(k), value: v})
			}
		case key == "run: |" || key == "run: |-" || key == "run: |+":
			var body []string
			cut := -1
			for j := i + 1; j < end && (strings.TrimSpace(lines[j]) == "" || indentOf(lines[j]) > ind); j++ {
				if strings.TrimSpace(lines[j]) == "" {
					body = append(body, "")
					continue
				}
				if cut < 0 {
					cut = indentOf(lines[j])
				}
				if indentOf(lines[j]) < cut {
					return step{}, fmt.Errorf("line %d: a run line less indented than the first", j+1)
				}
				body = append(body, lines[j][cut:])
			}
			st.body = strings.TrimRight(strings.Join(body, "\n"), "\n") + "\n"
		case strings.HasPrefix(key, "run:"):
			return step{}, fmt.Errorf("line %d: the run of the step %s is not a block scalar (run: |)", i+1, id)
		}
	}
	if strings.TrimSpace(st.body) == "" {
		return step{}, fmt.Errorf("the step %s has no run script", id)
	}
	return st, nil
}

func indentOf(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

type result struct {
	out, outputs string
	err          error
}

// runStep runs the step's script in tree as a step without a shell runs (bash -e), with its env,
// a GITHUB_OUTPUT file whose text it returns, and first, when given, first on PATH.
func runStep(st step, tree string, vals map[string]string, extra []string, first string) (result, error) {
	body, env, err := st.script(vals)
	if err != nil {
		return result{}, err
	}
	script := tree + "." + st.id + ".sh"
	outputs := script + ".output"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		return result{}, err
	}
	if err := os.WriteFile(outputs, nil, 0o600); err != nil {
		return result{}, err
	}
	if err := os.MkdirAll(script+".tmp", 0o750); err != nil {
		return result{}, err
	}
	env = append(append(gitEnv(tree), env...), "GITHUB_OUTPUT="+outputs, "GITHUB_STEP_SUMMARY="+script+".summary", "TMPDIR="+script+".tmp")
	env = append(env, extra...)
	if first != "" {
		env = append(env, "PATH="+first+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-e", script) // #nosec G204 -- the step of the workflow under test, written by this command
	cmd.Dir = tree
	cmd.Env = env
	b, runErr := cmd.CombinedOutput()
	o, err := os.ReadFile(filepath.Clean(outputs))
	if err != nil {
		return result{}, err
	}
	return result{out: string(b), outputs: string(o), err: runErr}, nil
}

// pushVals are the expressions of a push run from before to after.
func pushVals(before, after string) map[string]string {
	return map[string]string{"inputs.blueprint": "", "inputs.tree": "", "github.event.before": before, "github.sha": after}
}

// ── git and files ─────────────────────────────────────────────────────────────

// gitEnv is the environment of a command of the fixture: no configuration of the host, a fixed
// identity, no cluster.
func gitEnv(dir string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir + ".home",
		"LANG=C",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=release-set", "GIT_AUTHOR_EMAIL=release-set@example.invalid",
		"GIT_COMMITTER_NAME=release-set", "GIT_COMMITTER_EMAIL=release-set@example.invalid",
		"KUBECONFIG=/dev/null",
	}
}

// newRepo writes files into a new repository at dir, on branch main, in one commit.
func newRepo(dir string, files map[string]string) error {
	if err := writeFiles(dir, files); err != nil {
		return err
	}
	env := gitEnv(dir)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "fixture"}} {
		if _, err := command(dir, env, "git", args...); err != nil {
			return err
		}
	}
	return nil
}

// commitChange appends a line to rel (a new file when absent), commits and returns the commit.
func commitChange(dir, rel string) (string, error) {
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return "", err
	}
	f, err := os.OpenFile(filepath.Clean(full), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString("# a change of this push\n"); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	env := gitEnv(dir)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "change " + rel}} {
		if _, err := command(dir, env, "git", args...); err != nil {
			return "", err
		}
	}
	head, err := command(dir, env, "git", "rev-parse", "HEAD")
	return strings.TrimSpace(head), err
}

func writeFiles(root string, files map[string]string) error {
	for rel, text := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func writeExec(path, text string) error {
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o700) // #nosec G302 -- a stand-in program of the fixture must be executable
}

// command runs name with args in dir (env nil: this process's) and returns its standard output.
func command(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...) // #nosec G204 -- git with the guard's own arguments
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

// diff returns what want has and got lacks, and what got has beyond want.
func diff(want, got []string) (missing, extra []string) {
	for _, w := range want {
		if !contains(got, w) {
			missing = append(missing, w)
		}
	}
	for _, g := range got {
		if !contains(want, g) {
			extra = append(extra, g)
		}
	}
	return missing, extra
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
