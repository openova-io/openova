// Command acceptance-tools holds the tools that a precondition and an
// acceptance command of a work package may use to the steps that put them on
// the runner.
//
// .github/workflows/work-package.yaml runs the precondition of a package in its
// job precondition and the acceptance commands in its job acceptance. Each of
// the two jobs names, in one comment line, the tools its commands may use:
//
//	# The tools an acceptance command may use: git, the shell, Python with PyYAML, Go, ...
//
// A ticket is written against that line; a command runs with what the steps
// install. On 21 Nov 2026 the acceptance command A4 of NET-185 (pull request
// 8595) ran `tofu test` for a provider module and the job answered
// `tofu: command not found`: OpenTofu was neither in the line nor in the steps.
// The guard fails on:
//
//	comment  the job has no such line, or more than one, or a name in it is
//	         empty or repeated
//	named    the line names a tool that no step of the job installs and that
//	         the runner does not give
//	unnamed  a step of the job installs a tool that the line does not name
//	unknown  a step uses an action, or installs a package or a tool, that this
//	         guard cannot name (add the action or the package to the tables
//	         below)
//	tofu     the acceptance job does not install OpenTofu exactly once, with the
//	         action reference and the version that the tests of the provider
//	         modules use (every opentofu/setup-opentofu step of
//	         .github/workflows/infra-*-tofu.yaml, which must agree): an
//	         acceptance command of a package that changes a provider module runs
//	         that module's offline tests, and they run under one OpenTofu only
//
// A list item "A with B" names A and B. The runner gives git and the shell;
// npm comes with Node. The line must be one line. The YAML is read by
// indentation, as pinned-sources reads a workflow; a form it does not read
// leaves a step without the tool, which is a finding (named), never a pass.
//
// Usage, from tools/guards:
//
//	go run ./cmd/acceptance-tools [-root R]   exit 1 on a finding, 2 when a file cannot be read
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"sort"
	"strings"
)

const (
	workflow  = ".github/workflows/work-package.yaml"
	providers = ".github/workflows/infra-*-tofu.yaml"
	tofuSetup = "opentofu/setup-opentofu"
)

// judged is a job of the workflow and the opening of its line of tools.
type judged struct{ job, phrase string }

var jobs = []judged{
	{"precondition", "The tools a precondition command may use:"},
	{"acceptance", "The tools an acceptance command may use:"},
}

// actions maps the action of a step to the tool it installs.
var actions = map[string]string{
	"actions/setup-python": "Python",
	"actions/setup-go":     "Go",
	"actions/setup-node":   "Node",
	"azure/setup-helm":     "Helm",
	tofuSetup:              "OpenTofu",
}

// ignored are actions that install no tool.
var ignored = map[string]bool{"actions/checkout": true}

// pipPackages maps a package of `pip install` to the name the line uses.
var pipPackages = map[string]string{"pyyaml": "PyYAML"}

// runner is what the runner gives every job without a step.
var runner = []string{"git", "the shell"}

// comesWith maps a tool to the tool that brings it.
var comesWith = map[string]string{"npm": "Node"}

// otherInstalls are the shapes of a run step that install a tool this guard
// does not name.
var otherInstalls = []string{"apt-get install", "apt install", "go install", "npm install -g", "npm i -g", "brew install", "snap install", "curl ", "wget "}

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
	flags := flag.NewFlagSet("acceptance-tools", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			say(errw, "acceptance-tools: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(string(top))
	}
	tree := os.DirFS(*root)
	doc, err := fs.ReadFile(tree, workflow)
	if err != nil {
		say(errw, "acceptance-tools: %v\n", err)
		return 2
	}
	ls := lines(string(doc))
	r := &report{out: out}
	for _, j := range jobs {
		body, err := job(ls, j.job)
		if err != nil {
			r.fail("comment", workflow, "%v", err)
			continue
		}
		judgeJob(r, j, body)
		if j.job == "acceptance" {
			if err := judgeTofu(r, tree, body); err != nil {
				say(errw, "acceptance-tools: %v\n", err)
				return 2
			}
		}
	}
	if r.findings > 0 {
		say(out, "acceptance-tools: %d finding(s). The line of tools of each job of %s names exactly what its steps install.\n", r.findings, workflow)
		return 1
	}
	say(out, "acceptance-tools: the precondition and acceptance jobs install exactly the tools their lines name\n")
	return 0
}

// judgeJob compares the line of tools of one job with what its steps install.
func judgeJob(r *report, j judged, body []line) {
	before := r.findings
	var notes []line
	for _, l := range body {
		if l.note && strings.HasPrefix(l.text, j.phrase) {
			notes = append(notes, l)
		}
	}
	if len(notes) != 1 {
		r.fail("comment", workflow, "job %s has %d comment lines that start %q; it needs exactly one", j.job, len(notes), j.phrase)
		return
	}
	at := fmt.Sprintf("%s:%d", workflow, notes[0].n)
	named, err := names(strings.TrimPrefix(notes[0].text, j.phrase))
	if err != nil {
		r.fail("comment", at, "job %s: %v", j.job, err)
		return
	}
	have := installs(r, j.job, steps(body))
	for _, t := range named {
		if !have[t] {
			r.fail("named", at, "job %s names %s, which no step of the job installs and the runner does not give", j.job, t)
		}
	}
	listed := map[string]bool{}
	for _, t := range named {
		listed[t] = true
	}
	for _, t := range sorted(have) {
		if !listed[t] {
			r.fail("unnamed", at, "job %s has %s, which its line of tools does not name", j.job, t)
		}
	}
	if r.findings == before {
		say(r.out, "OK %s: the line and the steps name %s\n", j.job, strings.Join(named, ", "))
	}
}

// names reads the list of a line of tools: items separated by commas, an item
// "A with B" names A and B, a full stop at the end.
func names(list string) ([]string, error) {
	list = strings.TrimSuffix(strings.TrimSpace(list), ".")
	var out []string
	seen := map[string]bool{}
	for _, item := range strings.Split(list, ",") {
		for _, t := range strings.Split(item, " with ") {
			t = strings.TrimSpace(t)
			if t == "" {
				return nil, errors.New("the line of tools holds an empty name")
			}
			if seen[t] {
				return nil, fmt.Errorf("the line of tools names %s twice", t)
			}
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// installs returns the tools a job has: the runner's, those its steps install
// and those that come with them.
func installs(r *report, job string, ss []step) map[string]bool {
	have := map[string]bool{}
	for _, t := range runner {
		have[t] = true
	}
	for _, s := range ss {
		at := fmt.Sprintf("%s:%d", workflow, s.n)
		if s.uses != "" {
			action, _, _ := strings.Cut(s.uses, "@")
			if t, ok := actions[action]; ok {
				have[t] = true
			} else if !ignored[action] {
				r.fail("unknown", at, "job %s uses %s; this guard does not know which tool it installs", job, s.uses)
			}
		}
		for _, cmd := range s.run {
			if pkgs, ok := pipInstall(cmd); ok {
				for _, p := range pkgs {
					if t, ok := pipPackages[p]; ok {
						have[t] = true
					} else {
						r.fail("unknown", at, "job %s installs the Python package %s; this guard does not know its name", job, p)
					}
				}
				continue
			}
			for _, o := range otherInstalls {
				if strings.Contains(cmd, o) {
					r.fail("unknown", at, "job %s installs with %q (%s); this guard does not know which tool", job, strings.TrimSpace(o), cmd)
				}
			}
		}
	}
	for part, of := range comesWith {
		if have[of] {
			have[part] = true
		}
	}
	return have
}

// pipInstall returns the packages of a `pip install` (or `pip3 install`) command.
func pipInstall(cmd string) ([]string, bool) {
	f := strings.Fields(cmd)
	for i := 1; i < len(f); i++ {
		if f[i] != "install" || (f[i-1] != "pip" && f[i-1] != "pip3") {
			continue
		}
		var pkgs []string
		for _, a := range f[i+1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if k := strings.IndexAny(a, "=<>!~[;"); k >= 0 {
				a = a[:k]
			}
			if a != "" {
				pkgs = append(pkgs, strings.ToLower(a))
			}
		}
		return pkgs, true
	}
	return nil, false
}

// setup is one opentofu/setup-opentofu step: its reference and its version.
type setup struct{ ref, version string }

func tofuSetups(ss []step) []setup {
	var out []setup
	for _, s := range ss {
		action, ref, _ := strings.Cut(s.uses, "@")
		if action == tofuSetup {
			out = append(out, setup{ref: ref, version: s.with["tofu_version"]})
		}
	}
	return out
}

// judgeTofu holds the OpenTofu of the acceptance job to the provider modules' tests.
func judgeTofu(r *report, tree fs.FS, body []line) error {
	files, err := fs.Glob(tree, providers)
	if err != nil {
		return err
	}
	want := map[setup][]string{}
	for _, f := range files {
		doc, err := fs.ReadFile(tree, f)
		if err != nil {
			return err
		}
		for _, s := range tofuSetups(steps(lines(string(doc)))) {
			want[s] = append(want[s], f)
		}
	}
	if len(want) != 1 {
		var ways []string
		for s, where := range want {
			ways = append(ways, fmt.Sprintf("%s@%s %s in %s", tofuSetup, s.ref, s.version, strings.Join(where, ", ")))
		}
		sort.Strings(ways)
		r.fail("tofu", providers, "the tests of the provider modules must set up OpenTofu one way, found %d: %s", len(want), strings.Join(ways, "; "))
		return nil
	}
	var ref setup
	for s := range want {
		ref = s
	}
	got := tofuSetups(steps(body))
	switch {
	case len(got) != 1:
		r.fail("tofu", workflow, "job acceptance has %d %s steps; it needs one, %s@%s with tofu_version %s as in %s", len(got), tofuSetup, tofuSetup, ref.ref, ref.version, strings.Join(want[ref], ", "))
	case got[0] != ref:
		r.fail("tofu", workflow, "job acceptance sets up %s@%s with tofu_version %q; the provider modules' tests use @%s with %s (%s)", tofuSetup, got[0].ref, got[0].version, ref.ref, ref.version, strings.Join(want[ref], ", "))
	default:
		say(r.out, "OK tofu: job acceptance sets up %s@%s %s, as %s\n", tofuSetup, ref.ref, ref.version, strings.Join(want[ref], ", "))
	}
	return nil
}

// line is one line of the workflow that is not blank: its number, its indent,
// and its text without the comment, or, for a comment line (note), the text of
// the comment.
type line struct {
	n      int
	indent int
	text   string
	note   bool
}

func lines(doc string) []line {
	var out []line
	for i, raw := range strings.Split(doc, "\n") {
		t := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimLeft(t, " ")
		if trimmed == "" {
			continue
		}
		l := line{n: i + 1, indent: len(t) - len(trimmed)}
		if strings.HasPrefix(trimmed, "#") {
			l.note = true
			l.text = strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		} else {
			l.text = stripComment(trimmed)
		}
		out = append(out, l)
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

// job returns the lines of one job under the top-level jobs key, its comments
// included; a comment at the indent of the job keys or less belongs to the
// next job.
func job(ls []line, name string) ([]line, error) {
	start := -1
	for i, l := range ls {
		if !l.note && l.indent == 0 && l.text == "jobs:" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, errors.New("no top-level jobs key")
	}
	child := -1
	for i := start + 1; i < len(ls); i++ {
		l := ls[i]
		if l.note {
			continue
		}
		if l.indent == 0 {
			break
		}
		if child < 0 {
			child = l.indent
		}
		if l.indent != child || l.text != name+":" {
			continue
		}
		var body []line
		for _, b := range ls[i+1:] {
			if b.indent <= child {
				if b.note {
					continue
				}
				break
			}
			body = append(body, b)
		}
		return body, nil
	}
	return nil, fmt.Errorf("no job %s", name)
}

// step is one item of a steps list: where it starts, its action, the lines of
// its run and its with keys.
type step struct {
	n    int
	uses string
	run  []string
	with map[string]string
}

// steps returns the items of every steps key in ls.
func steps(ls []line) []step {
	var out []step
	for i, l := range ls {
		if l.note || l.text != "steps:" {
			continue
		}
		item, field := -1, -1
		block := ""
		for _, b := range ls[i+1:] {
			if b.note {
				continue
			}
			if item < 0 {
				if b.indent < l.indent || !strings.HasPrefix(b.text, "- ") {
					break
				}
				item = b.indent
			}
			if b.indent < item || (b.indent == item && !strings.HasPrefix(b.text, "- ")) {
				break
			}
			text, indent := b.text, b.indent
			if b.indent == item {
				out = append(out, step{n: b.n, with: map[string]string{}})
				text = strings.TrimSpace(strings.TrimPrefix(text, "- "))
				indent = item + 2
				field = indent
			}
			cur := &out[len(out)-1]
			if indent == field {
				k, v, _ := strings.Cut(text, ":")
				v = strings.TrimSpace(v)
				block = ""
				switch strings.TrimSpace(k) {
				case "uses":
					cur.uses = unquote(v)
				case "run":
					if strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
						block = "run"
					} else {
						cur.run = append(cur.run, unquote(v))
					}
				case "with":
					block = "with"
				}
				continue
			}
			switch block {
			case "run":
				cur.run = append(cur.run, text)
			case "with":
				if k, v, ok := strings.Cut(text, ":"); ok {
					cur.with[strings.TrimSpace(k)] = unquote(v)
				}
			}
		}
	}
	return out
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// say writes a line of the report; a failed write of a report cannot be
// reported anywhere else.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
