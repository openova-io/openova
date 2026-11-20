// Command pipe-cannot-break keeps a chart test from reading its rendered text
// through a pipe that breaks when the text grows.
//
// A chart test that runs under `set -o pipefail` and writes a shell variable
// into a reader that stops before the end of its input,
//
//	echo "$out" | awk '/smtp-host: /{print $2; exit}'
//
// fails once the text is larger than the pipe's buffer (64 KiB): the reader
// has gone, the writer's next write gets EPIPE (`echo: write error: Broken
// pipe` and status 1 where SIGPIPE is ignored, as on a GitHub runner; status
// 141 where it is not), and pipefail makes that the status of the pipeline
// although the reader found what it looked for. A render only grows, so such a
// test passes until a change of its chart pushes the text past the buffer, and
// from then on the chart cannot publish: bp-stalwart-sovereign, 19 November
// 2026, release runs 44598516820 and 44622665054.
//
// It reads the files Git lists (tracked, and new files Git does not ignore)
// that are shell scripts (.sh) under the tests/ folder of a chart
// (platform/<x>/chart/tests/, products/<x>/chart/tests/,
// products/<x>/charts/<y>/tests/) and that set pipefail, and fails on
//
//	awk-exit  echo or printf of a shell variable piped into awk whose program
//	          has an exit outside its END block
//	head      echo or printf of a shell variable piped into head
//	sed-q     echo or printf of a shell variable piped into sed whose script
//	          has a q or Q command
//
// The repaired form gives the reader the text as a here-string, which is a
// file and has no writer that can be cut off:
//
//	awk '/smtp-host: /{print $2; exit}' <<<"$out"
//
// The shell is read by a small lexer: quotes, $( ), ` `, ${ }, $(( )),
// here-documents (their bodies are text, not commands), comments, a backslash
// at the end of a line, and a line that ends in a pipe. A command ends at |,
// ;, &, &&, ||, a newline, a { or } that stands alone, or a ) outside a
// substitution (the end of a case pattern).
//
// Known limits, what it does NOT cover (review holds them):
//   - grep -q, grep -m and grep -l: readers that also stop early. The chart
//     tests still pipe small texts into them (a block, a line, the output of
//     a script); a whole render goes to grep as a here-string.
//   - a command as the writer: `helm template ... | head`,
//     `uncomment <<<"$1" | awk '...exit'`, `grep x file | head`.
//   - a filter between the variable and the reader:
//     `echo "$out" | grep -A1 X | head -5`. The reader's writer is the
//     filter, whose output is a slice of the text.
//   - a file without pipefail: the status of its pipe is the last reader's.
//   - a q in a sed script that is not the command (`s/a q;/b/`) is read as
//     the command; an `exit` in an awk string is read as the statement.
//
// Usage, from tools/guards:
//
//	go run ./cmd/pipe-cannot-break   exit 1 on a finding, 2 when it cannot read
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
	"strings"
)

var (
	// A shell script of the tests of a chart.
	chartTest = regexp.MustCompile(`^(platform/[^/]+/chart|products/[^/]+/chart|products/[^/]+/charts/[^/]+)/tests/.+\.sh$`)
	// A set line that turns pipefail on: set -euo pipefail, set -o pipefail.
	pipefail = regexp.MustCompile(`(?m)^[ \t]*set[ \t][^#\n]*\bpipefail\b`)
	// An expansion of a shell variable: $x, ${x}, ${#x}, $1, $@.
	variable = regexp.MustCompile(`\$(\{[#!]?)?([A-Za-z_][A-Za-z0-9_]*|[0-9@*])`)
	endBlock = regexp.MustCompile(`\bEND[ \t\n]*\{`)
	awkExit  = regexp.MustCompile(`\bexit\b`)
	// A q or Q command of sed: after the start, a separator, an address
	// (a line number, $ or the / that closes a pattern); before the end, a
	// separator or an exit code.
	sedQuit = regexp.MustCompile(`(^|[\s;{}'"0-9$/])[qQ][0-9]*(\s|[;}'"]|$)`)
	// The words before a command that are not the command.
	keywords = map[string]bool{"!": true, "if": true, "then": true, "elif": true, "else": true, "while": true, "until": true, "do": true, "time": true, "{": true}
)

type finding struct {
	Rule string
	Line int
	Text string
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("pipe-cannot-break", flag.ContinueOnError)
	flags.SetOutput(errw)
	root := flags.String("root", "", "the repository root (default: the top of the Git tree of the working folder)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *root == "" {
		top, err := git(".", "rev-parse", "--show-toplevel")
		if err != nil {
			say(errw, "pipe-cannot-break: cannot find the repository root: %v\n", err)
			return 2
		}
		*root = strings.TrimSpace(top)
	}
	listed, err := git(*root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		say(errw, "pipe-cannot-break: cannot list the files: %v\n", err)
		return 2
	}
	tree := os.DirFS(*root)
	read, bad := 0, 0
	for _, p := range strings.Split(listed, "\x00") {
		if !chartTest.MatchString(p) {
			continue
		}
		b, err := fs.ReadFile(tree, p)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree
		}
		if err != nil {
			say(errw, "pipe-cannot-break: %v\n", err)
			return 2
		}
		read++
		for _, f := range scan(string(b)) {
			bad++
			say(out, "FAIL %s %s:%d: %s\n", f.Rule, p, f.Line, f.Text)
		}
	}
	if read == 0 {
		say(errw, "pipe-cannot-break: no shell script of the tests of a chart was read: nothing was measured\n")
		return 2
	}
	if bad > 0 {
		say(out, "pipe-cannot-break: %d findings in %d files read. Give the reader the text as a here-string (awk '...' <<<\"$out\", head -1 <<<\"$out\"): a here-string is a file, so no writer is cut off when the reader stops early, and pipefail cannot turn a found match into a failure once the text passes the 64 KiB pipe buffer.\n", bad, read)
		return 1
	}
	say(out, "pipe-cannot-break: OK, %d chart test scripts read, none pipes a shell variable into awk with exit, head or sed with q under pipefail\n", read)
	return 0
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

// scan returns the pipes of one script that write a shell variable into a
// reader that stops early. A script without pipefail has none.
func scan(text string) []finding {
	if !pipefail.MatchString(text) {
		return nil
	}
	l := &lexer{src: text}
	l.code(0)
	var out []finding
	for _, p := range l.pairs {
		if !writesVariable(p.writer) {
			continue
		}
		if rule := earlyReader(p.reader); rule != "" {
			line := 1 + strings.Count(text[:p.at], "\n")
			out = append(out, finding{rule, line, compact(p.writer) + " | " + compact(p.reader)})
		}
	}
	return out
}

// command returns the name of the command of one stage of a pipe and the rest
// of the stage.
func command(stage string) (name, rest string) {
	s := strings.TrimSpace(stage)
	for {
		w, r := s, ""
		if i := strings.IndexAny(s, " \t\n"); i >= 0 {
			w, r = s[:i], s[i:]
		}
		if keywords[w] {
			s = strings.TrimSpace(r)
			continue
		}
		return path.Base(w), r
	}
}

func writesVariable(stage string) bool {
	name, rest := command(stage)
	return (name == "echo" || name == "printf") && variable.MatchString(rest)
}

func earlyReader(stage string) string {
	name, rest := command(stage)
	switch name {
	case "head":
		return "head"
	case "awk", "gawk", "mawk", "nawk":
		if awkExit.MatchString(withoutEnd(rest)) {
			return "awk-exit"
		}
	case "sed", "gsed":
		if sedQuit.MatchString(rest) {
			return "sed-q"
		}
	}
	return ""
}

// withoutEnd removes the END blocks of an awk program: an exit there runs after
// the last line was read, so the writer has finished.
func withoutEnd(prog string) string {
	for {
		m := endBlock.FindStringIndex(prog)
		if m == nil {
			return prog
		}
		depth, j := 0, m[1]-1
		for ; j < len(prog); j++ {
			if prog[j] == '{' {
				depth++
			} else if prog[j] == '}' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if j >= len(prog) {
			return prog[:m[0]]
		}
		prog = prog[:m[0]] + prog[j+1:]
	}
}

func compact(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// pair is one pipe: the stage that writes and the stage that reads, and the
// offset of the reader in the text.
type pair struct {
	writer, reader string
	at             int
}

type heredoc struct {
	word string
	tabs bool
}

type lexer struct {
	src     string
	i       int
	pending []heredoc // here-documents whose bodies start after the next newline
	pairs   []pair
}

func (l *lexer) peek(k int) byte {
	if l.i+k < len(l.src) {
		return l.src[l.i+k]
	}
	return 0
}

func (l *lexer) wordStart() bool {
	return l.i == 0 || strings.IndexByte(" \t\n;|&(){}`", l.src[l.i-1]) >= 0
}

// code reads commands up to closer (the ) or ` that ends a substitution or a
// subshell; 0 for the end of the text) and records every pipe of its level.
func (l *lexer) code(closer byte) {
	start, prev, sep := l.i, "", ""
	// cut ends the command that began at start where the separator s begins.
	cut := func(end int, s string) {
		text := strings.TrimSpace(l.src[start:end])
		switch {
		case text != "":
			if sep == "|" && prev != "" {
				l.pairs = append(l.pairs, pair{prev, text, start + strings.Index(l.src[start:end], text)})
			}
			prev, sep = text, s
		case s == "\n" && (sep == "|" || sep == "&&" || sep == "||"):
			// a line that ends in a pipe, && or || goes on on the next line
		default:
			prev, sep = "", s
		}
	}
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == '\\':
			l.i += 2
		case c == '\'':
			l.single()
		case c == '"':
			l.i++
			l.double()
		case c == '$' && l.peek(1) == '\'':
			l.i++
			l.ansi()
		case c == '$' && l.peek(1) == '(' && l.peek(2) == '(':
			l.arith()
		case c == '$' && l.peek(1) == '(':
			l.i += 2
			l.code(')')
		case c == '$' && l.peek(1) == '{':
			l.braces()
		case c == closer:
			cut(l.i, "end")
			l.i++
			return
		case c == '`':
			l.i++
			l.code('`')
		case c == '(':
			l.i++
			l.code(')')
		case c == '#' && l.wordStart():
			for l.i < len(l.src) && l.src[l.i] != '\n' {
				l.i++
			}
		case c == '<' && strings.HasPrefix(l.src[l.i:], "<<<"):
			l.i += 3
		case c == '<' && l.peek(1) == '<':
			l.heredoc()
		case c == '\n':
			cut(l.i, "\n")
			l.i++
			l.bodies()
			start = l.i
		case c == '|' && l.peek(1) == '|':
			cut(l.i, "||")
			l.i += 2
			start = l.i
		case c == '|':
			cut(l.i, "|")
			l.i++
			if l.peek(0) == '&' {
				l.i++
			}
			start = l.i
		case c == '&' && l.peek(1) == '&':
			cut(l.i, "&&")
			l.i += 2
			start = l.i
		case c == '&' && (l.peek(1) == '>' || (l.i > 0 && strings.IndexByte("<>", l.src[l.i-1]) >= 0)):
			l.i++ // a redirection: >&2, 2>&1, &>file
		case c == '&', c == ';', c == ')':
			cut(l.i, string(c))
			l.i++
			start = l.i
		case (c == '{' || c == '}') && l.wordStart() && (l.peek(1) == 0 || strings.IndexByte(" \t\n;)|&", l.peek(1)) >= 0):
			cut(l.i, string(c))
			l.i++
			start = l.i
		default:
			l.i++
		}
	}
	cut(len(l.src), "end")
}

func (l *lexer) single() {
	j := strings.IndexByte(l.src[l.i+1:], '\'')
	if j < 0 {
		l.i = len(l.src)
		return
	}
	l.i += j + 2
}

func (l *lexer) double() {
	for l.i < len(l.src) {
		c := l.src[l.i]
		switch {
		case c == '\\':
			l.i += 2
		case c == '"':
			l.i++
			return
		case c == '$' && l.peek(1) == '(' && l.peek(2) == '(':
			l.arith()
		case c == '$' && l.peek(1) == '(':
			l.i += 2
			l.code(')')
		case c == '$' && l.peek(1) == '{':
			l.braces()
		case c == '`':
			l.i++
			l.code('`')
		default:
			l.i++
		}
	}
}

// ansi reads a $'...' string from its opening quote.
func (l *lexer) ansi() {
	for l.i++; l.i < len(l.src); l.i++ {
		switch l.src[l.i] {
		case '\\':
			l.i++
		case '\'':
			l.i++
			return
		}
	}
}

func (l *lexer) arith() {
	depth := 2
	for l.i += 3; l.i < len(l.src); l.i++ {
		switch l.src[l.i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				l.i++
				return
			}
		}
	}
}

func (l *lexer) braces() {
	depth := 1
	for l.i += 2; l.i < len(l.src); {
		c := l.src[l.i]
		switch {
		case c == '\\':
			l.i += 2
		case c == '\'':
			l.single()
		case c == '"':
			l.i++
			l.double()
		case c == '$' && l.peek(1) == '(':
			l.i += 2
			l.code(')')
		case c == '{':
			depth++
			l.i++
		case c == '}':
			depth--
			l.i++
			if depth == 0 {
				return
			}
		default:
			l.i++
		}
	}
}

// heredoc reads the word of a here-document (<<WORD, <<-'WORD', <<"WORD");
// its body is skipped after the next newline.
func (l *lexer) heredoc() {
	l.i += 2
	h := heredoc{}
	if l.peek(0) == '-' {
		h.tabs = true
		l.i++
	}
	for l.i < len(l.src) && (l.src[l.i] == ' ' || l.src[l.i] == '\t') {
		l.i++
	}
	var w strings.Builder
	for l.i < len(l.src) && strings.IndexByte(" \t\n;|&<>()", l.src[l.i]) < 0 {
		c := l.src[l.i]
		switch c {
		case '\'', '"':
			j := strings.IndexByte(l.src[l.i+1:], c)
			if j < 0 {
				l.i = len(l.src)
				continue
			}
			w.WriteString(l.src[l.i+1 : l.i+1+j])
			l.i += j + 2
		case '\\':
			if l.i+1 < len(l.src) {
				w.WriteByte(l.src[l.i+1])
			}
			l.i += 2
		default:
			w.WriteByte(c)
			l.i++
		}
	}
	if w.Len() > 0 {
		h.word = w.String()
		l.pending = append(l.pending, h)
	}
}

// bodies skips the bodies of the here-documents opened on the line that just
// ended, each up to its closing word.
func (l *lexer) bodies() {
	for _, h := range l.pending {
		for l.i < len(l.src) {
			end := strings.IndexByte(l.src[l.i:], '\n')
			line := l.src[l.i:]
			if end >= 0 {
				line = l.src[l.i : l.i+end]
				l.i += end + 1
			} else {
				l.i = len(l.src)
			}
			if h.tabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == h.word {
				break
			}
		}
	}
	l.pending = nil
}
