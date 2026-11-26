// Command acceptance-exit keeps an acceptance command from exiting 0 while a
// tested command in it failed: a tested command is piped only after
// set -o pipefail.
//
// An acceptance command is the sh block of an item A1, A2, ... of field 18 of
// a ticket, docs/ledger/plan/tickets/<ID>.md; it passes when it exits 0. The
// guard fails on:
//
//	pipe        a pipe whose writer is a tested command, with no
//	            `set -o pipefail` (-eo, -euo pipefail) at the top of the
//	            command before it: a pipeline has the status of its last
//	            command (FND-191 A3 piped go test through tail)
//	unreadable  a command it cannot read (an unclosed quote, an unbalanced
//	            parenthesis or keyword, a backtick, a case statement, a
//	            here-document), an empty command and a field 18 without items: never a pass
//
// A tested command is any command but those of untested (text tools such as
// printf, grep, sed and git, builtins such as cd): a test runner, a build, an
// interpreter, a script, a program named by a path or a variable, and a
// subshell, group or loop that runs one. A pipe inside a command substitution
// is judged only when a variable is assigned the substitution
// (out="$(go test | tail)"), whose status the assignment has; as the argument
// of a command (test "$(...)" = 3) or as a process substitution its status is
// lost with or without pipefail.
//
// A merged ticket keeps its text and is not judged: its done marker
// docs/ledger/plan/done/<ID>.yaml is the three lines the finish step writes,
// schema openova.io/plan-done/v1, its id and its ticket_rev (scripts/plan.py
// reads a package as merged so, and scripts/ticket-lint.py holds that
// ticket_rev to the plan's); a marker of another form is judged. The other rules of a ticket are
// in scripts/ticket-lint.py; this one is Go so that the Python does not grow (cmd/python-shrinks).
//
// Usage, from tools/guards:
//
//	go run ./cmd/acceptance-exit [-root R]   exit 1 on a finding, 2 when no ticket or a file cannot be read
package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	tickets = "docs/ledger/plan/tickets"
	done    = "docs/ledger/plan/done"
	field   = "18. Acceptance checks"
)

// untested are the text tools and shell builtins whose status no acceptance command tests through a pipe.
var untested = words(": awk base64 basename cat cd column comm cut date dirname echo egrep export expand fgrep find fold git gofmt grep " +
	"gunzip gzip head jq join ls md5sum nl od paste printf readlink realpath rev sed seq set sha1sum sha256sum sha512sum sort " +
	"stat strings tac tail tar tee tr trap true uniq unset unzip wc xxd yq zcat")

// wrappers run the command after them; their flags, the argument of a flag of wrapArgs and a duration are not that command.
var wrappers, wrapArgs = words("builtin command env exec nice nohup stdbuf time timeout"), words("-C -S -k -n -s -u")

var (
	itemRe   = regexp.MustCompile(`^\*\*(A\d+)\.\*\*`)
	fenceRe  = regexp.MustCompile("^(`{3,}|~{3,})")
	shRe     = regexp.MustCompile("^```(sh|bash)?\\s*$")
	revRe    = regexp.MustCompile(`\A---\n(?:[^-\n][^\n]*\n)*ticket_rev: ([^\n]+)\n`) // in the front matter
	assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\+?=`)
	optRe    = regexp.MustCompile(`^-[a-zA-Z]*o$`)
	waitRe   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)
)

func words(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("acceptance-exit", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			say(errw, "acceptance-exit: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(string(top))
	}
	tree := os.DirFS(*root)
	files, _ := fs.Glob(tree, tickets+"/*.md")
	if len(files) == 0 {
		say(errw, "acceptance-exit: no ticket under %s\n", filepath.Join(*root, tickets))
		return 2
	}
	judged, findings := 0, 0
	for _, f := range files {
		id := strings.TrimSuffix(path.Base(f), ".md")
		text, err := fs.ReadFile(tree, f)
		marker, merr := fs.ReadFile(tree, done+"/"+id+".yaml")
		if errors.Is(merr, fs.ErrNotExist) {
			merr = nil
		}
		if err := errors.Join(err, merr); err != nil {
			say(errw, "acceptance-exit: %v\n", err)
			return 2
		}
		if merged(id, string(text), string(marker)) {
			continue
		}
		judged++
		for _, line := range lint(string(text)) {
			findings++
			say(out, "FAIL %s %s\n", f, line)
		}
	}
	if findings > 0 {
		say(out, "acceptance-exit: %d finding(s) in the %d tickets not merged. A tested command is piped only after `set -o pipefail`.\n", findings, judged)
		return 1
	}
	say(out, "acceptance-exit: the acceptance commands of the %d tickets not merged (of %d) lose the status of no piped tested command\n", judged, len(files))
	return 0
}

func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

// merged says whether the done marker is the one the finish step writes for the ticket's id and ticket_rev.
func merged(id, text, marker string) bool {
	rev := revRe.FindStringSubmatch(text)
	return rev != nil && marker == "schema: openova.io/plan-done/v1\nid: "+id+"\nticket_rev: "+rev[1]+"\n"
}

// lint judges the acceptance commands of one ticket: one line "<item> <rule>: <why>" per finding.
func lint(text string) []string {
	type item struct {
		id  string
		run []string
	}
	var items []item
	head, fence, open := "", "", false
	for _, l := range strings.Split(text, "\n") {
		switch m := fenceRe.FindStringSubmatch(l); {
		case fence != "" && strings.TrimRight(l, " \t") == fence:
			fence, open = "", false
		case fence != "" && open:
			items[len(items)-1].run = append(items[len(items)-1].run, l)
		case fence != "":
		case m != nil:
			fence = strings.Repeat(m[1][:1], len(m[1]))
			if open = head == field && len(items) > 0 && items[len(items)-1].run == nil && shRe.MatchString(l); open {
				items[len(items)-1].run = []string{}
			}
		case strings.HasPrefix(l, "## "):
			head = strings.TrimSpace(l[3:])
		case head == field && itemRe.MatchString(l):
			items = append(items, item{id: itemRe.FindStringSubmatch(l)[1]})
		}
	}
	if len(items) == 0 {
		return []string{"unreadable: field 18 holds no item A1, A2, ..."}
	}
	var out []string
	for _, it := range items {
		for _, f := range judge(strings.Join(it.run, "\n")) { // an item without an sh block holds no command
			out = append(out, it.id+" "+f)
		}
	}
	return out
}

// tok is a word without its quotes, or an operator: ; && || | |& & ( $( <( ) ;; a newline or a redirection.
type tok struct {
	text   string
	op     bool
	quoted bool   // a part is quoted, escaped or expanded: never a keyword
	bare   string // the text before the first such part
}

type lexer struct {
	s    string
	i    int
	toks []tok
	err  string
}

func (l *lexer) fail(why string) { l.err, l.i = cmp.Or(l.err, why), len(l.s) } // the first error stops the reading

func (l *lexer) op(text string) { l.toks = append(l.toks, tok{text: text, op: true}) }

// list reads tokens to the end of the command, or to the ) that closes a subshell or substitution.
func (l *lexer) list(sub bool) {
	for l.i < len(l.s) {
		c, rest := l.s[l.i], l.s[l.i:]
		switch {
		case c == ' ' || c == '\t':
			l.i++
		case strings.HasPrefix(rest, "\\\n"):
			l.i += 2
		case c == '#': // a comment, to the end of its line
			l.i += strings.IndexByte(rest+"\n", '\n')
		case c == ')' && sub:
			l.i++
			l.op(")")
			return
		case c == ')':
			l.fail("an unbalanced )")
		case strings.HasPrefix(rest, "(("):
			l.toks = append(l.toks, tok{text: l.match('(', ')'), quoted: true})
		case c == '(':
			l.i++
			l.op("(")
			l.list(true)
		case strings.HasPrefix(rest, "<(") || strings.HasPrefix(rest, ">("):
			l.i += 2
			l.op("<(")
			l.list(true)
		case strings.IndexByte("\n;&|<>", c) >= 0:
			for _, o := range []string{"&&", "||", "|&", ";;", "&>>", "&>", "<<<", "<<", ">>", ">&", "<&", ">|", "<>", "\n", ";", "&", "|", "<", ">"} {
				if strings.HasPrefix(rest, o) {
					l.i += len(o)
					l.op(o)
					break
				}
			}
		default:
			l.word()
		}
	}
	if sub {
		l.fail("an unclosed (")
	}
}

// match reads from an open byte to the close byte that balances it, and returns that text.
func (l *lexer) match(open, close byte) string {
	start, depth := l.i, 0
	for ; l.i < len(l.s); l.i++ {
		depth += map[byte]int{open: 1, close: -1}[l.s[l.i]]
		if depth == 0 && l.s[l.i] == close {
			l.i++
			return l.s[start:l.i]
		}
	}
	l.fail("an unclosed " + string(open))
	return ""
}

func (l *lexer) word() {
	var b strings.Builder
	t := tok{}
	quote := func() {
		if !t.quoted {
			t.quoted, t.bare = true, b.String()
		}
	}
	for l.i < len(l.s) && strings.IndexByte(" \t\n;&|()<>", l.s[l.i]) < 0 {
		switch c := l.s[l.i]; c {
		case '\\':
			quote()
			b.WriteString(l.s[min(l.i+1, len(l.s)):min(l.i+2, len(l.s))])
			l.i = min(l.i+2, len(l.s))
		case '\'':
			quote()
			j := strings.IndexByte(l.s[l.i+1:], '\'')
			if j < 0 {
				l.fail("an unclosed '")
				return
			}
			b.WriteString(l.s[l.i+1 : l.i+1+j])
			l.i += j + 2
		case '"':
			quote()
			l.i++
			l.dquote(&b)
		case '$':
			quote()
			l.dollar(&b)
		case '`':
			l.fail("a backtick substitution; write $( )")
		default:
			b.WriteByte(c)
			l.i++
		}
	}
	if t.text = b.String(); !t.quoted {
		t.bare = t.text
		if t.text != "" && strings.Trim(t.text, "0123456789") == "" && l.i < len(l.s) && strings.IndexByte("<>", l.s[l.i]) >= 0 {
			return // the file descriptor of a redirection
		}
	}
	l.toks = append(l.toks, t)
}

func (l *lexer) dquote(b *strings.Builder) {
	for l.i < len(l.s) {
		switch c := l.s[l.i]; {
		case c == '"':
			l.i++
			return
		case c == '\\' && l.i+1 < len(l.s):
			b.WriteByte(l.s[l.i+1])
			l.i += 2
		case c == '$':
			l.dollar(b)
		case c == '`':
			l.fail("a backtick substitution; write $( )")
		default:
			b.WriteByte(c)
			l.i++
		}
	}
	l.fail(`an unclosed "`)
}

// dollar reads $((...)) and ${...} as text, and a command substitution $( ) as tokens of its own.
func (l *lexer) dollar(b *strings.Builder) {
	switch rest := l.s[l.i:]; {
	case strings.HasPrefix(rest, "$(("), strings.HasPrefix(rest, "${"):
		l.i++
		b.WriteString("$" + l.match(rest[1], map[byte]byte{'(': ')', '{': '}'}[rest[1]]))
	case strings.HasPrefix(rest, "$("):
		l.i += 2
		b.WriteByte('$')
		l.op("$(")
		l.list(true)
	default:
		b.WriteByte('$')
		l.i++
	}
}

// frame is the top of the command, a subshell "(", a substitution "$(" or "<(", a group "{", an "if" or a loop.
type frame struct {
	kind             string
	cmd, redir, wrap bool     // a substitution: the state of the command around it
	header, body     bool     // a for loop before its do; a loop after its do
	pipefail         bool     // the top: set -o pipefail was read
	stage, any       string   // the first tested command of the current pipeline stage, and of this frame
	pipes            []string // a substitution: its pipe findings, which count only if a variable is assigned it
}

func (f *frame) loop() bool { return f.kind == "for" || f.kind == "while" || f.kind == "until" }

// judge reads one command and returns one line "<rule>: <why>" per finding.
func judge(command string) []string {
	l := &lexer{s: command}
	l.list(false) // on an error the tokens before it are judged, and a keyword this guard does not read is named first
	stack := []*frame{{kind: "top"}}
	cmd, redir, wrap, skip, bad := true, false, false, false, ""
	var out, pending []string
	report := func(msgs ...string) {
		for k := len(stack) - 1; k > 0; k-- {
			if s := stack[k]; s.kind == "$(" || s.kind == "<(" {
				s.pipes = append(s.pipes, msgs...)
				return
			}
		}
		out = append(out, msgs...)
	}
	pop := func() *frame {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if p := stack[len(stack)-1]; f.kind == "$(" || f.kind == "<(" {
			cmd, redir, wrap = f.cmd, f.redir, f.wrap
			pending = append(pending, f.pipes...) // a process substitution is an argument, never assigned: its findings fall
		} else if f.any != "" {
			p.stage, p.any = cmp.Or(p.stage, f.any), cmp.Or(p.any, f.any)
		}
		return f
	}
	for i := 0; i < len(l.toks) && bad == ""; i++ {
		t, f := l.toks[i], stack[len(stack)-1]
		kw := !t.op && !t.quoted && cmd && !skip
		if !t.op && len(pending) > 0 {
			if cmd && !redir && assignRe.MatchString(t.bare) {
				report(pending...) // out="$(a | b)": the assignment has the status of the substitution
			}
			pending = nil
		}
		switch {
		case skip:
			skip = t.op || t.quoted || t.text != "]]"
			cmd = skip
		case t.op && (t.text == "(" || t.text == "$(" || t.text == "<("):
			if t.text == "(" && !cmd {
				bad = "a ( that opens no subshell (a function or an array)"
			}
			stack = append(stack, &frame{kind: t.text, cmd: cmd, redir: redir, wrap: wrap})
			cmd, redir, wrap = true, false, false
		case t.op && t.text == ")":
			if f.kind != "(" && f.kind != "$(" && f.kind != "<(" {
				bad = "an unbalanced )"
			} else if pop().kind == "(" {
				cmd = false
			}
		case redir:
			redir = false
		case t.op && (t.text == "|" || t.text == "|&"):
			if f.stage != "" && !stack[0].pipefail {
				report(fmt.Sprintf("pipe: %s, a tested command, is piped with no `set -o pipefail` before it; the pipeline has the status of its last command", f.stage))
			}
			f.stage, cmd, wrap = "", true, false
		case t.op && (t.text == ";;" || t.text == "<<"):
			bad = map[string]string{";;": "a case statement", "<<": "a here-document"}[t.text]
		case t.op && (t.text == "&&" || t.text == "||" || t.text == ";" || t.text == "&" || t.text == "\n"):
			f.stage, cmd, wrap = "", true, false
		case t.op:
			redir = true
		case kw && (t.text == "for" || t.text == "while" || t.text == "until" || t.text == "if" || t.text == "{"):
			stack = append(stack, &frame{kind: t.text, header: t.text == "for"})
			cmd = t.text != "for"
		case kw && t.text == "do":
			if !f.loop() || f.body {
				bad = "a do outside a loop"
			}
			f.header, f.body, f.stage = false, true, ""
		case kw && (t.text == "done" || t.text == "fi" || t.text == "}"):
			if !map[string]bool{"done": f.loop() && f.body, "fi": f.kind == "if", "}": f.kind == "{"}[t.text] {
				bad = "an unbalanced " + t.text
			} else {
				pop()
			}
			cmd = false
		case kw && (t.text == "then" || t.text == "else" || t.text == "elif"):
			if f.kind != "if" {
				bad = "a " + t.text + " outside an if"
			}
			f.stage = ""
		case kw && (t.text == "case" || t.text == "select" || t.text == "function" || t.text == "coproc"):
			bad = "a " + t.text + " statement"
		case kw && t.text == "[[":
			skip = true
		case f.header || !cmd || kw && t.text == "!" || assignRe.MatchString(t.bare) || wrap && (strings.HasPrefix(t.text, "-") || waitRe.MatchString(t.text) || wrapArgs[l.toks[i-1].text]):
		case !t.quoted && wrappers[t.text]:
			wrap = true
		default:
			for j := i + 1; t.text == "set" && !t.quoted && len(stack) == 1 && j < len(l.toks) && !l.toks[j].op; j++ {
				f.pipefail = f.pipefail || l.toks[j].text == "pipefail" && optRe.MatchString(l.toks[j-1].text)
			}
			if t.quoted || strings.ContainsAny(t.text, "$/") || !untested[t.text] {
				f.stage, f.any = cmp.Or(f.stage, t.text), cmp.Or(f.any, t.text)
			}
			cmd, wrap = false, false
		}
	}
	if bad == "" && l.err != "" {
		bad = l.err
	} else if bad == "" && (skip || len(stack) > 1) {
		bad = "an unclosed " + map[bool]string{true: "[[", false: stack[len(stack)-1].kind}[skip]
	} else if bad == "" && !slices.ContainsFunc(l.toks, func(t tok) bool { return !t.op }) {
		bad = "it holds no command" // an empty or comment-only block exits 0 and tests nothing
	}
	if bad != "" {
		out = append(out, "unreadable: "+bad)
	}
	return out
}
