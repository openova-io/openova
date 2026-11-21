// Command pinned-sources keeps a pin test running when the file it pins
// changes.
//
// A pin test compares a figure in Go with the file outside its module that
// sets it (a chart's values.yaml, a template, another module's Go source). Go
// cannot see those reads, so the workflow that runs the test has to: its
// pull_request path filter must cover every such file, or a change to the
// file merges without the test that it breaks. On 21 Nov 2026 an agenity
// chart change (pull request 8518) did not run the organization controller's
// workflow, and TestPlatformStack_PinnedToSources was red on main for a day.
//
// Each pin below names a Go test file, the variable in it that lists the files
// the tests read (a []string of repo-root-relative paths, string literals
// only; the tests' reader refuses any path not in it) and the workflow that
// runs those tests. The guard reads the list with go/parser and fails on:
//
//	table     the variable is missing, empty, holds anything but string
//	          literals, or names a path twice
//	missing   a listed path is not a file of the tree (the list is stale)
//	event     the workflow does not run on pull_request
//	uncovered the workflow's pull_request paths filter (or paths-ignore)
//	          does not let a change to a listed path run it
//
// Filter patterns follow GitHub's: `**` matches any characters, `*` any but
// `/`, a leading `!` excludes, and the last matching pattern decides. A
// pattern with any other special character (`?`, `+`, `[`) is not read and is
// a finding (pattern), so the guard never guesses. No filter for the event
// means every path runs it. The push filter is not judged: a push run of a
// build workflow builds and ships an image, which a chart change does not need.
//
// Usage, from tools/guards:
//
//	go run ./cmd/pinned-sources [-root R]   exit 1 on a finding, 2 when a file cannot be read
package main

import (
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// pin is one table of pinned sources and the workflow that runs its tests.
type pin struct {
	table, variable, workflow string
}

var pins = []pin{{
	table:    "core/controllers/organization/internal/gitops/plan_quota_plus_platform_stack_test.go",
	variable: "pinnedSources",
	workflow: ".github/workflows/build-organization-controller.yaml",
}}

const event = "pull_request"

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type report struct {
	out      io.Writer
	findings int
}

func (r *report) fail(rule, at, format string, args ...any) {
	r.findings++
	say(r.out, "FAIL %s %s: %s\n", rule, at, fmt.Sprintf(format, args...))
}

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("pinned-sources", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			say(errw, "pinned-sources: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(string(top))
	}
	tree := os.DirFS(*root)
	r := &report{out: out}
	for _, p := range pins {
		if err := judge(r, tree, p); err != nil {
			say(errw, "pinned-sources: %v\n", err)
			return 2
		}
	}
	if r.findings > 0 {
		say(out, "pinned-sources: %d finding(s). Every file a pin test reads must run that test when it changes: list it in the pull_request paths of the workflow.\n", r.findings)
		return 1
	}
	say(out, "pinned-sources: every pinned source runs the tests that pin it\n")
	return 0
}

func judge(r *report, tree fs.FS, p pin) error {
	src, err := fs.ReadFile(tree, p.table)
	if err != nil {
		return err
	}
	sources, err := readTable(src, p.table, p.variable)
	if err != nil {
		r.fail("table", p.table, "%v", err)
		return nil
	}
	wf, err := fs.ReadFile(tree, p.workflow)
	if err != nil {
		return err
	}
	f, err := eventFilter(string(wf), event)
	if err != nil {
		r.fail("event", p.workflow, "%v", err)
		return nil
	}
	for _, bad := range f.unreadable {
		r.fail("pattern", p.workflow, "pattern %q holds a character this guard does not read (only *, ** and a leading !)", bad)
	}
	covered := 0
	for _, s := range sources {
		if st, err := fs.Stat(tree, s); err != nil || st.IsDir() {
			r.fail("missing", p.table, "%s lists %s, which is not a file of the tree", p.variable, s)
			continue
		}
		if !f.runs(s) {
			r.fail("uncovered", p.workflow, "a change to %s (read by the tests of %s) does not run this workflow on %s", s, p.table, event)
			continue
		}
		covered++
	}
	say(r.out, "OK %s: %d of %d sources in %s run %s on %s\n", p.variable, covered, len(sources), p.table, p.workflow, event)
	return nil
}

// readTable returns the string literals of `var <name> = []string{...}`.
func readTable(src []byte, file, name string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		return nil, err
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.VAR {
			continue
		}
		for _, sp := range g.Specs {
			vs := sp.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name != name {
					continue
				}
				if i >= len(vs.Values) {
					return nil, fmt.Errorf("var %s has no value", name)
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					return nil, fmt.Errorf("var %s is not a composite literal", name)
				}
				arr, ok := lit.Type.(*ast.ArrayType)
				if id, isID := arrayElem(arr); !ok || !isID || id != "string" {
					return nil, fmt.Errorf("var %s is not a []string", name)
				}
				var out []string
				seen := map[string]bool{}
				for _, e := range lit.Elts {
					b, ok := e.(*ast.BasicLit)
					if !ok || b.Kind != token.STRING {
						return nil, fmt.Errorf("%s: an element of %s is not a string literal", fset.Position(e.Pos()), name)
					}
					s, err := strconv.Unquote(b.Value)
					if err != nil {
						return nil, err
					}
					if seen[s] {
						return nil, fmt.Errorf("%s names %s twice", name, s)
					}
					seen[s] = true
					out = append(out, s)
				}
				if len(out) == 0 {
					return nil, fmt.Errorf("var %s is empty", name)
				}
				return out, nil
			}
		}
	}
	return nil, fmt.Errorf("no var %s", name)
}

func arrayElem(a *ast.ArrayType) (string, bool) {
	if a == nil || a.Len != nil {
		return "", false
	}
	id, ok := a.Elt.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

// filter is the path filter of one event of a workflow.
type filter struct {
	any        bool     // the event runs on every path
	paths      []string // the paths patterns, in order
	ignore     []string // the paths-ignore patterns
	unreadable []string
}

func (f filter) runs(p string) bool {
	if f.any {
		return true
	}
	if len(f.ignore) > 0 {
		for _, g := range f.ignore {
			if strings.HasPrefix(g, "!") {
				continue
			}
			if match(g, p) {
				return false
			}
		}
		return true
	}
	in := false
	for _, g := range f.paths {
		if neg := strings.TrimPrefix(g, "!"); neg != g {
			if match(neg, p) {
				in = false
			}
			continue
		}
		if match(g, p) {
			in = true
		}
	}
	return in
}

// match is GitHub's filter pattern: ** any characters, * any but '/'.
func match(pattern, p string) bool {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch {
		case strings.HasPrefix(pattern[i:], "**"):
			b.WriteString(".*")
			i++
		case pattern[i] == '*':
			b.WriteString("[^/]*")
		default:
			b.WriteString(regexp.QuoteMeta(pattern[i : i+1]))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String()).MatchString(p)
}

// line is one meaningful line of the workflow: its indent and its text with
// the comment removed.
type line struct {
	indent int
	text   string
}

func lines(doc string) []line {
	var out []line
	for _, raw := range strings.Split(doc, "\n") {
		t := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimLeft(t, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, line{indent: len(t) - len(trimmed), text: stripComment(trimmed)})
	}
	return out
}

// stripComment removes a YAML comment: a # after a space, outside quotes.
func stripComment(s string) string {
	var quote byte
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && i > 0 && s[i-1] == ' ':
			return strings.TrimRight(s[:i], " ")
		}
	}
	return s
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"') {
		return s[1 : len(s)-1]
	}
	return s
}

// eventFilter reads `on:` of a workflow and the filter of one event: the
// block form (on: / event: / paths: / - pattern), and the inline forms
// `on: event` and `on: [a, b]`.
func eventFilter(doc, ev string) (filter, error) {
	ls := lines(doc)
	start := -1
	for i, l := range ls {
		if l.indent == 0 && (strings.HasPrefix(l.text, "on:") || strings.HasPrefix(l.text, `"on":`) || strings.HasPrefix(l.text, "'on':")) {
			start = i
			break
		}
	}
	if start < 0 {
		return filter{}, errors.New("no top-level on key")
	}
	inline := strings.TrimSpace(ls[start].text[strings.Index(ls[start].text, ":")+1:])
	if inline != "" {
		for _, e := range strings.Split(strings.Trim(inline, "[]"), ",") {
			if unquote(e) == ev {
				return filter{any: true}, nil
			}
		}
		return filter{}, fmt.Errorf("on: %s does not name %s", inline, ev)
	}
	var block []line
	for _, l := range ls[start+1:] {
		if l.indent == 0 {
			break
		}
		block = append(block, l)
	}
	if len(block) == 0 {
		return filter{}, errors.New("on: is empty")
	}
	child := block[0].indent
	for i, l := range block {
		k, rest, isKey := strings.Cut(l.text, ":")
		if l.indent != child || !isKey || unquote(k) != ev {
			continue
		}
		var body []line
		for _, b := range block[i+1:] {
			if b.indent <= child {
				break
			}
			body = append(body, b)
		}
		if rest = strings.TrimSpace(rest); rest != "" && rest != "{}" && rest != "null" {
			return filter{}, fmt.Errorf("%s: %s is not read", ev, rest)
		}
		return filterOf(body), nil
	}
	return filter{}, fmt.Errorf("the workflow does not run on %s", ev)
}

func filterOf(body []line) filter {
	var f filter
	if len(body) == 0 {
		f.any = true
		return f
	}
	key := body[0].indent
	have := false
	for i, l := range body {
		if l.indent != key {
			continue
		}
		var into *[]string
		switch l.text {
		case "paths:":
			into = &f.paths
		case "paths-ignore:":
			into = &f.ignore
		default:
			continue
		}
		have = true
		for _, item := range body[i+1:] {
			if item.indent <= key {
				break
			}
			if !strings.HasPrefix(item.text, "- ") {
				continue
			}
			g := unquote(strings.TrimPrefix(item.text, "- "))
			if strings.ContainsAny(strings.TrimPrefix(g, "!"), "?+[]") {
				f.unreadable = append(f.unreadable, g)
				continue
			}
			*into = append(*into, g)
		}
	}
	f.any = !have
	return f
}

// say writes a line of the report; a failed write of a report cannot be
// reported anywhere else.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
