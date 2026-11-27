// Command umbrella-deps holds that every renderer of the umbrella chart
// products/catalyst/chart builds the chart's dependencies before Helm renders it.
//
// The umbrella depends on the library platform/secrets-lib (SEC-216) through a
// file:// path. charts/ and Chart.lock are gitignored, so on a clean checkout helm
// template, package and install refuse the chart ("found in Chart.yaml, but missing in
// charts/ directory: secrets-lib") until `helm dependency build` ran.
//
// A unit is a script (.sh, .bash, .py, .go, .awk) or one job of a workflow or action
// under .github (a job is judged alone: a build in another job ran on another runner).
//
//	renders  a code line runs helm template, lint, package, install or upgrade: in a
//	         shell (`helm template …`, `"$helm" template …`) or as an argument list
//	         (exec.Command(helmBin, "template", …), []string{"template", …},
//	         ["helm", "template", …]);
//	names    a code line holds the chart's folder (products/catalyst/chart, not a path
//	         below it, or the parts "products", "catalyst", "chart"), or the unit is a
//	         chart test of the umbrella (products/catalyst/chart/tests/*.sh);
//	builds   a code line runs helm dependency (or dep) build or update, passes
//	         --dependency-update, holds the argument list "dependency", "build" (or
//	         "update"), or, in Go, calls a function of its package whose body builds.
//
// A code line is not a comment (#, //, /*, *) and prints no message (echo, printf,
// log, t.Fatalf, a step name, …): a message that names a command neither renders nor
// builds.
//
// Rules, each shown to fail by main_test.go:
//
//	U1  a unit that renders and names the umbrella builds;
//	U2  an exemption names a file that renders and names the umbrella and does not
//	    build (any other exemption is stale);
//	U3  at least one unit renders and names the umbrella: none means the patterns no
//	    longer read the tree, which is exit 2, never a pass.
//
// What it cannot see: the order of a build and a render inside one unit (a script that
// builds for one render and not for another passes); a renderer that reaches the
// umbrella only through a glob (products/*/chart), an argument its caller passes or a
// path made of other parts; a build whose failure is ignored (`|| true`). The renderers
// over products/*/chart build each chart that declares dependencies (measured 27 November
// 2026).
//
// Usage (from tools/guards): go run ./cmd/umbrella-deps [--root <repository root>]
// Exit 0 when every rule holds, 1 when one does not, 2 when the tree cannot be read.
package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"sort"
	"strings"
)

const (
	// Umbrella is the chart every renderer must build the dependencies of.
	Umbrella = "products/catalyst/chart"
	// selfDir holds this command: its tests are fixtures of the patterns.
	selfDir = "tools/guards/cmd/umbrella-deps"
)

// Exemptions are files that render and name the umbrella and need no build, each with
// the reason.
var Exemptions = map[string]string{
	"scripts/tests/test_generate_catalog.py": "renders a chart it writes in a scratch tree from a Chart.yaml without dependencies; the umbrella's path there is a value of its fixture chart-locations.yaml",
}

const helmWord = `(?:"?\$\{?[A-Za-z_]*(?:helm|HELM)[A-Za-z_]*\}?"?|helm)`

var (
	shellRender = regexp.MustCompile(`(?:^|[\s;&|(` + "`" + `{=])` + helmWord + `\s+(?:[^\s|;&]+\s+)*?(?:template|lint|package|install|upgrade)(?:[\s");&|]|$)`)
	argRender   = regexp.MustCompile(`(?:\bhelm\w*|\bHELM\w*|"helm"\)?|\[\]string\{)\s*,?\s*"(?:template|lint|package|install|upgrade)"`)
	shellBuild  = regexp.MustCompile(`(?:^|[\s;&|(` + "`" + `{=])` + helmWord + `\s+(?:[^\s|;&]+\s+)*?(?:dependency|dep)\s+(?:build|update)(?:[\s");&|]|$)|--dependency-update\b`)
	argBuild    = regexp.MustCompile(`"(?:dependency|dep)"\s*,\s*"(?:build|update)"`)
	names       = regexp.MustCompile(regexp.QuoteMeta(Umbrella) + `/?(?:$|[^A-Za-z0-9_./*{-])|"products"\s*,\s*"catalyst"\s*,\s*"chart"\s*\)`)
	goFunc      = regexp.MustCompile(`^func (?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[\[(]`)
	jobHeader   = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*(?:#.*)?$`)
	messages    = []string{"#", "//", "/*", "*", "echo ", "printf ", "log ", "say ", "die ", "fail ", "warn ", "bad ", "ok ",
		"::", "- name:", "name:", "description:", "t.Fatal", "t.Error", "t.Log", "t.Skip", "fmt.", "return fmt.", "print(", "cannot(", "raise "}
)

// Unit is one script or one workflow job that renders and names the umbrella.
type Unit struct {
	Path   string
	Job    string
	Render int // first render line, 1-based
	Build  int // first build line, 0 when none
}

func (u Unit) String() string {
	if u.Job != "" {
		return u.Path + " job " + u.Job
	}
	return u.Path
}

// Finding is one broken rule.
type Finding struct {
	Rule string
	Unit string
	Why  string
}

func (f Finding) String() string {
	return fmt.Sprintf("umbrella-deps: FAIL %s %s: %s", f.Rule, f.Unit, f.Why)
}

// code reports whether a line is code: not a comment and no message.
func code(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	for _, p := range messages {
		if strings.HasPrefix(t, p) {
			return false
		}
	}
	return true
}

func first(lines []string, from int, match func(string) bool) int {
	for i, l := range lines {
		if code(l) && match(l) {
			return from + i + 1
		}
	}
	return 0
}

func rendersLine(l string) bool { return shellRender.MatchString(l) || argRender.MatchString(l) }
func buildsLine(l string) bool  { return shellBuild.MatchString(l) || argBuild.MatchString(l) }

// Check judges every unit of the tree at root; exempt maps a path to its reason.
func Check(root string, exempt map[string]string) ([]Unit, []Finding, error) {
	fsys := os.DirFS(root)
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch {
			case p == "docs", p == selfDir, d.Name() == ".git", d.Name() == "node_modules", d.Name() == "vendor":
				return fs.SkipDir
			}
			return nil
		}
		ext := path.Ext(p)
		switch {
		case ext == ".sh", ext == ".bash", ext == ".py", ext == ".go", ext == ".awk":
			files = append(files, p)
		case strings.HasPrefix(p, ".github/") && (ext == ".yaml" || ext == ".yml"):
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	builders := map[string]map[string]bool{} // Go package folder -> functions that build
	var units []Unit
	var findings []Finding
	seen := map[string]bool{}
	for _, p := range files {
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, nil, err
		}
		lines := strings.Split(string(b), "\n")
		isTest := strings.HasPrefix(p, Umbrella+"/tests/") && strings.HasSuffix(p, ".sh")
		var helpers map[string]bool
		if strings.HasSuffix(p, ".go") {
			dir := path.Dir(p)
			if builders[dir] == nil {
				if builders[dir], err = goBuilders(fsys, dir); err != nil {
					return nil, nil, err
				}
			}
			helpers = builders[dir]
		}
		for _, s := range sections(p, lines) {
			part := lines[s.from:s.to]
			render := first(part, s.from, rendersLine)
			if render == 0 || (!isTest && first(part, s.from, names.MatchString) == 0) {
				continue
			}
			build := first(part, s.from, func(l string) bool { return buildsLine(l) || callsAny(l, helpers) })
			u := Unit{Path: p, Job: s.job, Render: render, Build: build}
			if reason, ok := exempt[p]; ok {
				seen[p] = true
				if build == 0 {
					continue
				}
				findings = append(findings, Finding{"U2", u.String(), "exempt (" + reason + ") but it builds the dependencies (line " + fmt.Sprint(build) + "): remove the exemption"})
				continue
			}
			units = append(units, u)
			if build == 0 {
				findings = append(findings, Finding{"U1", u.String(), fmt.Sprintf("renders %s (line %d) and builds no dependency: Helm refuses the chart until `helm dependency build %s` ran (SEC-216)", Umbrella, render, Umbrella)})
			}
		}
	}
	var stale []string
	for p := range exempt {
		if !seen[p] {
			stale = append(stale, p)
		}
	}
	sort.Strings(stale)
	for _, p := range stale {
		findings = append(findings, Finding{"U2", p, "exempt (" + exempt[p] + ") but it does not render and name " + Umbrella + " (or does not exist): remove the exemption"})
	}
	return units, findings, nil
}

type section struct {
	job      string
	from, to int
}

// sections splits a workflow into its jobs; any other file is one section.
func sections(p string, lines []string) []section {
	if !strings.HasPrefix(p, ".github/") || (!strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml")) {
		return []section{{"", 0, len(lines)}}
	}
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t\r") == "jobs:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return []section{{"", 0, len(lines)}}
	}
	var out []section
	for i := start; i < len(lines); i++ {
		if m := jobHeader.FindStringSubmatch(lines[i]); m != nil {
			if len(out) > 0 {
				out[len(out)-1].to = i
			}
			out = append(out, section{m[1], i, len(lines)})
		}
	}
	if len(out) == 0 { // jobs not indented by two spaces: judge them as one, never skip them
		return []section{{"", start, len(lines)}}
	}
	return out
}

// goBuilders lists the functions of the Go package in dir whose body builds, directly
// or through another such function.
func goBuilders(fsys fs.FS, dir string) (map[string]bool, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	bodies := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		name := ""
		for _, l := range strings.Split(string(b), "\n") {
			if m := goFunc.FindStringSubmatch(l); m != nil {
				name = m[1]
				continue
			}
			if name == "" {
				continue
			}
			if l == "}" {
				name = ""
				continue
			}
			bodies[name] = append(bodies[name], l)
		}
	}
	out := map[string]bool{}
	for grew := true; grew; {
		grew = false
		for name, body := range bodies {
			if !out[name] && first(body, 0, func(l string) bool { return buildsLine(l) || callsAny(l, out) }) > 0 {
				out[name], grew = true, true
			}
		}
	}
	return out, nil
}

func callsAny(line string, funcs map[string]bool) bool {
	if len(funcs) == 0 || goFunc.MatchString(line) {
		return false
	}
	for name := range funcs {
		if i := strings.Index(line, name+"("); i >= 0 && (i == 0 || !isWord(line[i-1])) {
			return true
		}
	}
	return false
}

func isWord(c byte) bool {
	return c == '_' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func repositoryRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root with git: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("umbrella-deps", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "repository root (default: the root Git reports)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		r, err := repositoryRoot()
		if err != nil {
			say(stderr, "umbrella-deps: %v\n", err)
			return 2
		}
		*root = r
	}
	units, findings, err := Check(*root, Exemptions)
	if err != nil {
		say(stderr, "umbrella-deps: cannot read the tree: %v\n", err)
		return 2
	}
	for _, f := range findings {
		say(stdout, "%s\n", f)
	}
	if len(findings) > 0 {
		say(stdout, "umbrella-deps: FAIL — %d finding(s) over %d renderer(s) of %s\n", len(findings), len(units), Umbrella)
		return 1
	}
	if len(units) == 0 {
		say(stdout, "umbrella-deps: U3 no unit renders and names %s: the patterns no longer read this tree; nothing was judged\n", Umbrella)
		return 2
	}
	for _, u := range units {
		say(stdout, "umbrella-deps: ok %s (renders line %d, builds line %d)\n", u, u.Render, u.Build)
	}
	say(stdout, "umbrella-deps: PASS — %d renderer(s) of %s, each builds its dependencies; %d exempt\n", len(units), Umbrella, len(Exemptions))
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// say writes one line of the report; a failed write cannot change the verdict, which
// is the exit code.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
