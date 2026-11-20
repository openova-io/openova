// Command coherence-credential keeps every workflow step that runs the
// coherence check of a release candidate supplied with the origin credential
// that check 5 of that check needs.
//
// Check 5 of scripts/check-train-coherent.sh reads the origin's hosted project
// (every first-party image the kit pins must be there with the digest ghcr
// has) with ORIGIN_USER and ORIGIN_TOKEN, and fails closed without them: the
// verdict is CANNOT READ. scripts/release-candidate.sh runs that check for
// cut and for verify. Pull request 8494 added check 5 and gave the credential
// to the cut workflow only, so the gate's own verification of every candidate
// cut after it ended "could not be measured" before any measurement (gate run
// 44782748548, 20 November 2026). This guard holds the class: a check that
// gains a required input in one caller only.
//
// It reads the workflows Git lists under .github/workflows (tracked, and new
// files Git does not ignore) and, in each job, each step whose run text has a
// line (not a shell comment) that calls
//
//	scripts/release-candidate.sh   with any command but prune
//	scripts/check-train-coherent.sh   without --self-test (its self-test
//	                                  serves stand-in registries itself)
//
// The step's environment is the env of the workflow, of the job and of the
// step, the later winning, as on the runner. It fails on
//
//	missing    ORIGIN_USER or ORIGIN_TOKEN is not in that environment, or
//	           is empty
//	literal    ORIGIN_TOKEN is not exactly one ${{ secrets.<NAME> }}
//	differ     two steps name a different ORIGIN_USER or a different secret
//	           (the credential is one robot account and one secret)
//	call       a job that calls one of these workflows (uses:
//	           ./.github/workflows/<file>) neither passes secrets: inherit
//	           nor the secret by name, so the called step reads it empty
//
// Known limits, what it does NOT cover (review holds them): a composite action
// under .github/actions that runs the check (none does), a call through
// another script than these two, and an env written as a flow mapping
// ({A: b}): that env is read as empty, so the step fails as missing.
//
// Usage, from tools/guards:
//
//	go run ./cmd/coherence-credential   exit 1 on a finding, 2 when it cannot read or finds no step
package main

import (
	"bytes"
	"errors"
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

var (
	workflowFile = regexp.MustCompile(`^\.github/workflows/[^/]+\.ya?ml$`)
	// The two entry points of the coherence check, as a file name that does not
	// continue another one (scripts/test-release-candidate.sh is not a caller).
	releaseCandidate = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])release-candidate\.sh\b`)
	trainCoherent    = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])check-train-coherent\.sh\b`)
	pruneCommand     = regexp.MustCompile(`release-candidate\.sh["']?[ \t]+prune\b`)
	selfTest         = regexp.MustCompile(`check-train-coherent\.sh["']?[ \t]+[^#\n]*--self-test\b`)
	secretRef        = regexp.MustCompile(`^\$\{\{\s*secrets\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)
	keyLine          = regexp.MustCompile(`^('[^']*'|"[^"]*"|[^\s#'"{\[-][^:#]*?|-[^\s:#][^:#]*?)[ \t]*:([ \t]+(.*)|)$`)
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// caller is one step that runs the coherence check.
type caller struct {
	file, job, step string
	line            int
	user, token     envValue
	secret          string
}

type envValue struct {
	value string
	set   bool
}

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("coherence-credential", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		top, err := git(".", "rev-parse", "--show-toplevel")
		if err != nil {
			say(errw, "coherence-credential: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(top)
	}
	listed, err := git(*root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", ".github/workflows")
	if err != nil {
		say(errw, "coherence-credential: cannot list the files: %v\n", err)
		return 2
	}
	tree := os.DirFS(*root)
	docs := map[string]*node{}
	var files []string
	for _, p := range strings.Split(listed, "\x00") {
		if !workflowFile.MatchString(p) {
			continue
		}
		b, err := fs.ReadFile(tree, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			say(errw, "coherence-credential: %v\n", err)
			return 2
		}
		docs[p] = parse(string(b))
		files = append(files, p)
	}
	sort.Strings(files)
	if len(files) == 0 {
		say(errw, "coherence-credential: no workflow was read: nothing was measured\n")
		return 2
	}

	var callers []caller
	for _, f := range files {
		callers = append(callers, coherenceSteps(f, docs[f])...)
	}
	if len(callers) == 0 {
		say(errw, "coherence-credential: %d workflows read and no step runs scripts/release-candidate.sh or scripts/check-train-coherent.sh: nothing was measured\n", len(files))
		return 2
	}

	bad := 0
	fail := func(rule string, c caller, format string, args ...any) {
		bad++
		say(out, "FAIL %s %s:%d job %s step %q: %s\n", rule, c.file, c.line, c.job, c.step, fmt.Sprintf(format, args...))
	}
	users, secrets := map[string][]string{}, map[string][]string{}
	for _, c := range callers {
		ok := true
		for _, v := range []struct {
			name string
			v    envValue
		}{{"ORIGIN_USER", c.user}, {"ORIGIN_TOKEN", c.token}} {
			if !v.v.set || v.v.value == "" {
				fail("missing", c, "%s is not in the env of the step, its job or its workflow (or is empty): check 5 of the coherence check ends CANNOT READ", v.name)
				ok = false
			}
		}
		if c.token.set && c.token.value != "" && c.secret == "" {
			fail("literal", c, "ORIGIN_TOKEN is not one ${{ secrets.<NAME> }} (its value is not printed)")
			ok = false
		}
		if !ok {
			continue
		}
		where := fmt.Sprintf("%s:%d", c.file, c.line)
		users[c.user.value] = append(users[c.user.value], where)
		secrets[c.secret] = append(secrets[c.secret], where)
		say(out, "OK %s job %s step %q: ORIGIN_USER %s, ORIGIN_TOKEN from secrets.%s\n", where, c.job, c.step, c.user.value, c.secret)
	}
	for _, m := range []struct {
		what string
		seen map[string][]string
	}{{"ORIGIN_USER", users}, {"the secret of ORIGIN_TOKEN", secrets}} {
		if len(m.seen) > 1 {
			var parts []string
			for _, k := range sortedKeys(m.seen) {
				parts = append(parts, fmt.Sprintf("%s at %s", k, strings.Join(m.seen[k], ", ")))
			}
			bad++
			say(out, "FAIL differ %s is not one value: %s\n", m.what, strings.Join(parts, "; "))
		}
	}
	bad += reusableCalls(files, docs, callers, out)

	if bad > 0 {
		say(out, "coherence-credential: %d findings, %d steps run the coherence check in %d workflows read. Give each such step the env of the cut (.github/workflows/cut-release-candidate.yaml): ORIGIN_USER: 'robot$openova-warmup' and ORIGIN_TOKEN: ${{ secrets.BASTION_HARBOR_ROBOT_TOKEN }}; check 5 stays fail-closed without them.\n", bad, len(callers), len(files))
		return 1
	}
	say(out, "coherence-credential: OK, %d steps run the coherence check in %d workflows read, each with ORIGIN_USER and ORIGIN_TOKEN from one secret\n", len(callers), len(files))
	return 0
}

// coherenceSteps returns the steps of one workflow that run the coherence
// check, with the environment each step sees.
func coherenceSteps(file string, doc *node) []caller {
	var out []caller
	wfEnv := envOf(doc.get("env"))
	jobs := doc.get("jobs")
	if jobs == nil {
		return nil
	}
	for _, job := range jobs.children {
		jobEnv := envOf(job.get("env"))
		steps := job.get("steps")
		if steps == nil {
			continue
		}
		for i, step := range steps.children {
			line, ok := callsCoherence(step.get("run"))
			if !ok {
				continue
			}
			env := map[string]envValue{}
			for _, e := range []map[string]envValue{wfEnv, jobEnv, envOf(step.get("env"))} {
				for k, v := range e {
					env[k] = v
				}
			}
			name := step.get("name").scalar()
			if name == "" {
				name = fmt.Sprintf("#%d", i+1)
			}
			c := caller{file: file, job: job.key, step: name, line: line, user: env["ORIGIN_USER"], token: env["ORIGIN_TOKEN"]}
			if m := secretRef.FindStringSubmatch(c.token.value); m != nil {
				c.secret = m[1]
			}
			out = append(out, c)
		}
	}
	return out
}

// callsCoherence reports whether a run text calls the coherence check, and
// the line of the first such call.
func callsCoherence(r *node) (int, bool) {
	if r == nil {
		return 0, false
	}
	lines := r.block
	if len(lines) == 0 {
		lines = []rawLine{{no: r.line, text: r.value}}
	}
	for _, l := range lines {
		t := strings.TrimSpace(l.text)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if releaseCandidate.MatchString(t) && !pruneCommand.MatchString(t) {
			return l.no, true
		}
		if trainCoherent.MatchString(t) && !selfTest.MatchString(t) {
			return l.no, true
		}
	}
	return 0, false
}

// reusableCalls fails each job that calls a workflow holding a coherence step
// without handing it that step's secret.
func reusableCalls(files []string, docs map[string]*node, callers []caller, out io.Writer) int {
	need := map[string]map[string]bool{}
	for _, c := range callers {
		if c.secret == "" {
			continue
		}
		if need[c.file] == nil {
			need[c.file] = map[string]bool{}
		}
		need[c.file][c.secret] = true
	}
	bad := 0
	for _, f := range files {
		jobs := docs[f].get("jobs")
		if jobs == nil {
			continue
		}
		for _, job := range jobs.children {
			uses := job.get("uses").scalar()
			if !strings.HasPrefix(uses, "./.github/workflows/") {
				continue
			}
			called := path.Clean(strings.TrimPrefix(uses, "./"))
			s := job.get("secrets")
			if s.scalar() == "inherit" {
				continue
			}
			for _, secret := range sortedKeys(need[called]) {
				passed := false
				if s != nil {
					for _, k := range s.children {
						if strings.EqualFold(k.key, secret) {
							passed = true
						}
					}
				}
				if !passed {
					bad++
					say(out, "FAIL call %s:%d job %s calls %s without secrets: inherit or %s: its coherence step reads ORIGIN_TOKEN empty\n", f, job.line, job.key, called, secret)
				}
			}
		}
	}
	return bad
}

func envOf(n *node) map[string]envValue {
	m := map[string]envValue{}
	if n == nil {
		return m
	}
	for _, c := range n.children {
		if c.key != "" {
			m[c.key] = envValue{value: c.scalar(), set: true}
		}
	}
	return m
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

// ── a reader of the block YAML that workflows are written in ──────────────
//
// It reads mappings and sequences by indentation, plain and quoted scalars and
// block scalars (| and >, whose lines it keeps with their line numbers). A
// flow collection ({...} or [...]) is kept as the text of a scalar.

type rawLine struct {
	no     int
	indent int
	text   string // without the indentation
	blank  bool   // empty or only a comment
}

type node struct {
	line     int
	key      string // the key of a mapping entry; empty for a sequence item
	value    string // an inline scalar
	block    []rawLine
	children []*node // the entries of a mapping, or the items of a sequence
}

func (n *node) get(key string) *node {
	if n == nil {
		return nil
	}
	for _, c := range n.children {
		if c.key == key {
			return c
		}
	}
	return nil
}

func (n *node) scalar() string {
	if n == nil {
		return ""
	}
	return n.value
}

type parser struct {
	lines []rawLine
	i     int
}

func parse(text string) *node {
	p := &parser{}
	for i, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimLeft(l, " ")
		p.lines = append(p.lines, rawLine{
			no:     i + 1,
			indent: len(l) - len(t),
			text:   strings.TrimRight(t, " \t"),
			blank:  strings.TrimSpace(t) == "" || strings.HasPrefix(t, "#") || t == "---",
		})
	}
	return &node{children: p.block(0, false)}
}

// cur skips blank and comment lines and returns the next line, or nil.
func (p *parser) cur() *rawLine {
	for p.i < len(p.lines) && p.lines[p.i].blank {
		p.i++
	}
	if p.i >= len(p.lines) {
		return nil
	}
	return &p.lines[p.i]
}

// block reads the entries or items that start at the indentation of the next
// line, which must be at least minIndent. With seqOnly it stops at a line that
// is not a sequence item (an indentless sequence under a key). A deeper line
// that no entry took (the next line of a plain scalar) is passed over.
func (p *parser) block(minIndent int, seqOnly bool) []*node {
	l := p.cur()
	if l == nil || l.indent < minIndent {
		return nil
	}
	ind := l.indent
	var out []*node
	for l = p.cur(); l != nil && l.indent >= ind; l = p.cur() {
		if l.indent > ind {
			p.i++
			continue
		}
		if l.text == "-" || strings.HasPrefix(l.text, "- ") {
			item := &node{line: l.no}
			rest := strings.TrimLeft(l.text[1:], " ")
			col := ind + len(l.text) - len(rest)
			p.i++
			switch {
			case rest == "":
				item.children = p.block(ind+1, false)
			case keyLine.MatchString(rest):
				item.children = append(item.children, p.entry(rest, l.no, col))
				item.children = append(item.children, p.block(col, false)...)
			default:
				item.value = scalarText(rest)
			}
			out = append(out, item)
			continue
		}
		if seqOnly {
			break
		}
		if keyLine.MatchString(l.text) {
			text, no := l.text, l.no
			p.i++
			out = append(out, p.entry(text, no, ind))
			continue
		}
		p.i++ // a line this reader does not know; it carries nothing the guard reads
	}
	return out
}

// entry reads one mapping entry whose key line (already consumed) is text at
// indentation ind.
func (p *parser) entry(text string, no, ind int) *node {
	m := keyLine.FindStringSubmatch(text)
	n := &node{line: no, key: unquote(strings.TrimSpace(m[1]))}
	val := strings.TrimSpace(m[3])
	switch {
	case strings.HasPrefix(val, "|") || strings.HasPrefix(val, ">"):
		for p.i < len(p.lines) {
			l := p.lines[p.i]
			if strings.TrimSpace(l.text) != "" && l.indent <= ind {
				break
			}
			n.block = append(n.block, l)
			p.i++
		}
	case val != "" && !strings.HasPrefix(val, "#"):
		n.value = scalarText(val)
	default:
		next := p.cur()
		if next != nil && next.indent == ind && (next.text == "-" || strings.HasPrefix(next.text, "- ")) {
			n.children = p.block(ind, true)
		} else {
			n.children = p.block(ind+1, false)
		}
	}
	return n
}

// scalarText reads an inline scalar: a quoted one up to its closing quote, a
// plain one up to a comment.
func scalarText(v string) string {
	switch {
	case strings.HasPrefix(v, "'"):
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			if v[i] == '\'' {
				if i+1 < len(v) && v[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String()
			}
			b.WriteByte(v[i])
		}
		return b.String()
	case strings.HasPrefix(v, `"`):
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			switch v[i] {
			case '\\':
				if i+1 < len(v) {
					i++
					b.WriteByte(v[i])
				}
			case '"':
				return b.String()
			default:
				b.WriteByte(v[i])
			}
		}
		return b.String()
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

func unquote(k string) string {
	if len(k) >= 2 && (k[0] == '\'' || k[0] == '"') && k[len(k)-1] == k[0] {
		return k[1 : len(k)-1]
	}
	return k
}
