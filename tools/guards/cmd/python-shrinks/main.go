// Command python-shrinks keeps Python in this repository from growing, file by
// file.
//
// The languages of this repository are Go, Rust where a design record says so,
// TypeScript for the consoles and shell for a few lines of glue
// (docs/PRINCIPLES.md IV.4). The Python that exists is debt to port: each file
// that holds Python is listed in the baseline with its own number of lines,
// and that number only falls. A file the exception list names, with a
// one-line reason, is not counted.
//
// It reads the files Git lists (the tracked files, and new files Git does not
// ignore) and compares them with scripts/baselines/python.json:
//
//	python_files  every file that holds Python -> its lines of Python
//	sites         the places where Python runs on a Sovereign or is put into a
//	              shipped image: kind -> path -> number of places
//
// The lines of Python of a file:
//
//	a Python file  a path that ends in .py, or a file whose first line is a #!
//	               line that names python: every line.
//	another file   a shell, YAML, template, Terraform, HCL or make file, a
//	               container file, a file of a scope below, or a file whose
//	               first line is a #! line (never a .md or .txt file):
//	               - the body of a here-document opened on a line that runs
//	                 python or writes a .py file (<< or <<-, quoted or not);
//	               - the quoted program of python -c, and of a here-string (<<<)
//	                 given to python, from its quote to the closing quote;
//	               - the body of a YAML block scalar (| or >) whose key ends in
//	                 .py, or whose first line is a #! line that names python.
//	               A comment line (#) opens nothing; lines that end in a
//	               backslash are read as one line.
//
// The files read for places (the scope):
//
//	chart      a file under templates/ or files/ of a chart (a folder that holds
//	           a Chart.yaml Git lists), other than a .md or .txt file
//	cloudinit  a file under infra/providers/ whose name starts with cloudinit or
//	           cloud-init and ends in .tftpl, .tpl, .yaml or .yml
//	image      a file whose name starts with Containerfile or Dockerfile
//
// The kinds of place. A line that starts with # (a comment) and a Go template
// comment {{/* */}} hold none. Lines that end in a backslash are read as one
// line, which is split into commands at &&, ||, ; and |; each command counts
// at most once:
//
//	shebang  a line that starts with #! and names python (in a .py file of the
//	         scope only its first line is read, and only for this kind)
//	install  a package install that names a python package (apk add, apt-get
//	         install, apt install, dnf, yum, microdnf or tdnf install, zypper
//	         install); a pip install (pip, pip3, python3 -m pip); an image whose
//	         name starts with python (a FROM line of an image file, or image: in
//	         a chart)
//	invoke   the word python, python3 or python3.N as a command: before it the
//	         start of the command, a space, a quote, [, (, =, { or :- (a path
//	         such as /usr/bin/ may stand between); after it the end, a space, a
//	         quote, ], ), }, a comma, <, >, |, ; or &. A LABEL line of an image
//	         file is not read.
//
// The rules. Exit 1 when one fails:
//
//  1. A file that holds Python and that python_files does not list is new.
//  2. No file holds more lines of Python than python_files lists for it.
//  3. No path holds more places of a kind than sites lists for it; a place in
//     a path that sites does not list for that kind is a new place.
//  4. The baseline is not stale: a file that holds fewer lines of Python, or a
//     path that holds fewer places, than the baseline lists fails until the
//     baseline is lowered with --write-baseline. Room Python gave up cannot be
//     taken again later.
//  5. An exception names a file Git lists that holds Python, once, with a
//     reason of one line and ten characters or more. A file it names is not
//     counted by rules 1 to 4; since every file has its own number, it frees
//     no room for any other file.
//  6. With --base <commit>: no number of the baseline file is higher than in
//     the same file at <commit>, and it lists no file or place that it did not
//     list there. When the file does not exist at <commit> there is nothing to
//     compare.
//
// --write-baseline writes the baseline from the tree and keeps the exception
// list; it refuses (exit 1) when rule 1, 2, 3 or 5 fails, so a baseline is
// never raised and never gains a file or a place. Exit 2 when the tree, the
// baseline or the base commit cannot be read. The baseline file has a code
// owner (.github/CODEOWNERS), so an exception is added only with that review.
package main

import (
	"bytes"
	"encoding/json"
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
	"sort"
	"strconv"
	"strings"
)

const (
	me           = "python-shrinks"
	baselinePath = "scripts/baselines/python.json"
	schema       = "openova.io/guard-baseline/v1"
	goLine       = "New tooling is written in Go (tools/guards, docs/PRINCIPLES.md IV.4); Python is added only as a named exception in " + baselinePath + "."

	scopeChart     = "chart"
	scopeCloudInit = "cloudinit"
	scopeImage     = "image"
)

// kinds are the kinds of place, in the order they are reported.
var kinds = []string{"install", "invoke", "shebang"}

var (
	templateComment = regexp.MustCompile(`(?s)\{\{-?\s*/\*.*?\*/\s*-?\}\}`)
	shebangRe       = regexp.MustCompile(`^#!.*?(?:/|\s)python[0-9]*(?:\.[0-9]+)?(?:\s|$)`)
	invokeRe        = regexp.MustCompile("(?:^|[\\s\"'\\[(=`{]|:-)(?:[A-Za-z0-9_.-]*/)*python[0-9]*(?:\\.[0-9]+)?(?:[\\s\"'\\]),`}<>|;&]|$)")
	pkgInstallRe    = regexp.MustCompile(`(?:^|[\s"'(])(?:apk\s+add|apt-get\s+install|apt\s+install|dnf\s+install|yum\s+install|microdnf\s+install|tdnf\s+install|zypper\s+(?:in|install))(?:\s|$)`)
	pythonPkgRe     = regexp.MustCompile(`(?:^|\s)(?:python[A-Za-z0-9_.+-]*|py3-[A-Za-z0-9_.+-]+)(?:\s|$)`)
	pipInstallRe    = regexp.MustCompile(`(?:^|[\s"'(/])(?:pip[0-9.]*|python[0-9.]*\s+-m\s+pip)\s+install(?:\s|$)`)
	imageRe         = regexp.MustCompile(`(?:^|[\s"'{,-])image:\s*["']?(?:[A-Za-z0-9_.:-]*/)*python[A-Za-z0-9_.-]*(?:[:@"'\s]|$)`)
	fromPythonRe    = regexp.MustCompile(`(?i)^FROM\s+(?:--\S+\s+)*(?:[A-Za-z0-9_.:-]*/)*python[A-Za-z0-9_.-]*(?:[:@\s]|$)`)
	labelRe         = regexp.MustCompile(`(?i)^LABEL\s`)
	cloudInitName   = regexp.MustCompile(`^cloud-?init[^/]*\.(?:tftpl|tpl|ya?ml)$`)
	commandSep      = regexp.MustCompile(`&&|\|\||;|\|`)

	// Python inside a file of another language.
	heredocRe    = regexp.MustCompile(`(?:^|[^<])<<-?[ \t]*(?:'([^'\s]+)'|"([^"\s]+)"|\\?([A-Za-z_][A-Za-z0-9_]*))`)
	writesPyRe   = regexp.MustCompile(`(?:>|\btee\s+(?:-a\s+)?)\s*["']?[^\s"'<>|;&]*\.py["']?(?:\s|<|$)`)
	pyProgramRe  = regexp.MustCompile("(?:^|[\\s\"'\\[(=`{]|:-)(?:[A-Za-z0-9_.-]*/)*python[0-9]*(?:\\.[0-9]+)?[\"']?(?:(?:[\\s,]+[\"']?-[A-Za-z]+[\"']?)*?[\\s,]+[\"']?-c[\"']?[\\s,]*|(?:\\s+-[A-Za-z]*)*\\s*<<<\\s*)\\$?(\\\\?['\"])")
	programVar   = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
	blockRe      = regexp.MustCompile(`^([ \t]*)(?:-[ \t]+)?(?:["']?([^"'\s:#][^"':]*?)["']?[ \t]*:[ \t]+)?[|>][-+1-9]*[ \t]*(?:#.*)?$`)
	embeddedExts = map[string]bool{".sh": true, ".bash": true, ".zsh": true, ".ksh": true, ".bats": true, ".yaml": true, ".yml": true,
		".tpl": true, ".tftpl": true, ".tf": true, ".hcl": true, ".mk": true, ".j2": true}
	embeddedNames = []string{"Makefile", "makefile", "GNUmakefile", "Justfile", "justfile"}
)

type exception struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// baseline is scripts/baselines/python.json. The fields are in the order of
// their keys, so the file is written with its keys sorted.
type baseline struct {
	Exceptions  []exception               `json:"exceptions"`
	PythonFiles map[string]int            `json:"python_files"`
	Schema      string                    `json:"schema"`
	Sites       map[string]map[string]int `json:"sites"`
}

type place struct {
	kind string
	path string
	line int
}

// tree is what the guard measured.
type tree struct {
	listed   map[string]bool // every regular file Git lists
	python   map[string]int  // file that holds Python -> its lines of Python
	embedded map[string]bool // a file of python whose Python sits inside another language
	places   []place
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// say writes one message; a failed write to the output cannot be reported anywhere else.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func run(args []string, out io.Writer) int {
	flags := flag.NewFlagSet(me, flag.ContinueOnError)
	flags.SetOutput(out)
	root := flags.String("root", "", "the checkout to read (default: the top of the Git work tree of the current folder)")
	base := flags.String("base", "", "also fail when the baseline file is higher than at this commit")
	write := flags.Bool("write-baseline", false, "write "+baselinePath+" from the tree; refuses to raise it")
	list := flags.Bool("list", false, "print every Python file and every place, then judge")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		say(out, "%s: unexpected argument %q\n", me, flags.Arg(0))
		return 2
	}
	code, err := guard(*root, *base, *write, *list, out)
	if err != nil {
		say(out, "%s: cannot judge: %v\n", me, err)
		return 2
	}
	return code
}

func guard(root, base string, write, list bool, out io.Writer) (int, error) {
	if root == "" {
		top, err := topLevel()
		if err != nil {
			return 0, err
		}
		root = top
	}
	t, err := measure(root)
	if err != nil {
		return 0, err
	}
	if list {
		printList(t, out)
	}
	if write {
		code, err := writeBaseline(root, t, out)
		if err != nil || code != 0 {
			return code, err
		}
	}
	data, err := fs.ReadFile(os.DirFS(root), baselinePath)
	if err != nil {
		return 0, fmt.Errorf("%s cannot be read: %v", baselinePath, err)
	}
	b, err := parseBaseline(data, baselinePath)
	if err != nil {
		return 0, err
	}
	fails, stale := judge(t, b)
	fails = append(fails, stale...)
	var notes []string
	if base != "" {
		more, note, err := compareBase(root, base, b)
		if err != nil {
			return 0, err
		}
		fails = append(fails, more...)
		if note != "" {
			notes = append(notes, note)
		}
	}
	say(out, "%s: %s\n", me, summary(t, excepted(b, t)))
	for _, n := range notes {
		say(out, "%s: note: %s\n", me, n)
	}
	for _, f := range fails {
		say(out, "%s: FAIL %s\n", me, f)
	}
	if len(fails) > 0 {
		say(out, "%s: %d failure(s). %s\n", me, len(fails), goLine)
		return 1, nil
	}
	return 0, nil
}

// ---- measuring the tree

func topLevel() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("the current folder is not in a Git work tree: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(b)), nil
}

// listFiles returns the files Git lists below root: tracked, and new files Git does not ignore.
func listFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %v %s", root, err, strings.TrimSpace(stderr.String()))
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range strings.Split(string(b), "\x00") {
		if p != "" && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func measure(root string) (*tree, error) {
	paths, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("git lists no file in %s", root)
	}
	charts := map[string]bool{}
	for _, p := range paths {
		if path.Base(p) == "Chart.yaml" {
			charts[path.Dir(p)] = true
		}
	}
	t := &tree{listed: map[string]bool{}, python: map[string]int{}, embedded: map[string]bool{}}
	fsys := os.DirFS(root)
	for _, p := range paths {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the work tree and not yet in the index
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		t.listed[p] = true
		scope := scopeOf(p, charts)
		isPy := strings.HasSuffix(p, ".py")
		var text []byte
		if isPy || scope != "" {
			text, err = fs.ReadFile(fsys, p)
		} else {
			text, err = readHead(fsys, p, 512)
		}
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(text, 0) >= 0 {
			continue // not text
		}
		first := firstLine(text)
		isPy = isPy || shebangRe.MatchString(first)
		inner := !isPy && readsEmbedded(p, scope, first)
		if (isPy || inner) && scope == "" && !strings.HasSuffix(p, ".py") {
			if text, err = fs.ReadFile(fsys, p); err != nil {
				return nil, err
			}
		}
		switch {
		case isPy:
			t.python[p] = countLines(text)
		case inner:
			if n := embedded(string(text)); n > 0 {
				t.python[p] = n
				t.embedded[p] = true
			}
		}
		if scope != "" {
			t.places = append(t.places, scan(scope, p, string(text))...)
		}
	}
	return t, nil
}

func readHead(fsys fs.FS, name string, n int) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	closeErr := f.Close()
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return buf[:got], nil
}

func firstLine(text []byte) string {
	if i := bytes.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSuffix(string(text), "\r")
}

func countLines(text []byte) int {
	n := bytes.Count(text, []byte{'\n'})
	if len(text) > 0 && text[len(text)-1] != '\n' {
		n++
	}
	return n
}

// scopeOf names the scope of a path, or "" when the path is not read for places.
func scopeOf(p string, charts map[string]bool) string {
	name := path.Base(p)
	if strings.HasPrefix(name, "Containerfile") || strings.HasPrefix(name, "Dockerfile") {
		return scopeImage
	}
	if strings.HasPrefix(p, "infra/providers/") && cloudInitName.MatchString(name) {
		return scopeCloudInit
	}
	parts := strings.Split(p, "/")
	for i := len(parts) - 1; i >= 1; i-- {
		if !charts[strings.Join(parts[:i], "/")] {
			continue
		}
		// The nearest chart above the path decides.
		if (parts[i] == "templates" || parts[i] == "files") && i < len(parts)-1 &&
			!strings.HasSuffix(name, ".md") && !strings.HasSuffix(name, ".txt") {
			return scopeChart
		}
		return ""
	}
	return ""
}

// readsEmbedded is true when a file of another language is read for Python inside it.
func readsEmbedded(p, scope, first string) bool {
	name := path.Base(p)
	if strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".txt") {
		return false
	}
	if scope != "" || embeddedExts[path.Ext(name)] || strings.HasPrefix(first, "#!") {
		return true
	}
	for _, prefix := range embeddedNames {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// embedded returns the lines of Python inside a file of another language.
func embedded(text string) int {
	lines := strings.Split(text, "\n")
	starts := make([]int, len(lines))
	for i, off := 0, 0; i < len(lines); i++ {
		starts[i] = off
		off += len(lines[i]) + 1
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	if last := len(lines) - 1; last > 0 && lines[last] == "" {
		lines, starts = lines[:last], starts[:last] // the end of the last line is not a line
	}
	n := 0
	vars := map[string]bool{} // shell variables that hold a program given to python -c
	for i := 0; i < len(lines); {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			i++
			continue
		}
		if body, end, ok := pythonBlock(lines, i); ok {
			n += body
			i = end
			continue
		}
		k := logicalEnd(lines, i)
		line, next := logicalLine(lines, i, k), k+1
		// The quoted programs of python -c and of here-strings given to python. A << inside one
		// is Python, not a here-document.
		end, programs := starts[k]+len(lines[k]), 0
		for from := starts[i]; from < end; programs++ {
			m := pyProgramRe.FindStringSubmatchIndex(text[from:end])
			if m == nil {
				break
			}
			open := from + m[2]
			quote := text[open : from+m[3]]
			count, closing := quotedLines(text, open, quote, end)
			n += count
			if v := programVar.FindStringSubmatch(text[open+len(quote) : closing]); v != nil {
				vars[v[1]] = true
			}
			if last := sort.SearchInts(starts, closing+1) - 1; last+1 > next {
				next = last + 1
			}
			from = closing + len(quote)
		}
		if programs == 0 && (invokeRe.MatchString(line) || writesPyRe.MatchString(line)) {
			var body int
			body, next = heredocs(line, lines, next)
			n += body
		}
		i = next
	}
	// A program given as python -c "$NAME": the quoted text or the here-document assigned to NAME.
	for name := range vars {
		assign := regexp.MustCompile(`(?:^|[\s;(&|])` + name + `=(\$?\\?['"]|\$\()|\bread\b[^<]*\s` + name + `\s[^<]*<<`)
		for i := 0; i < len(lines); {
			k := logicalEnd(lines, i)
			m := assign.FindStringSubmatchIndex(text[starts[i] : starts[k]+len(lines[k])])
			next := k + 1
			switch {
			case m == nil || strings.HasPrefix(strings.TrimSpace(lines[i]), "#"):
			case m[2] >= 0 && !strings.HasSuffix(text[starts[i]+m[2]:starts[i]+m[3]], "("):
				open := starts[i] + m[3] - 1
				quote := text[open : starts[i]+m[3]]
				if strings.HasSuffix(text[starts[i]+m[2]:starts[i]+m[3]], `\`+quote) {
					open, quote = open-1, `\`+quote
				}
				body, closing := quotedLines(text, open, quote, starts[k]+len(lines[k]))
				n += body
				next = sort.SearchInts(starts, closing+1)
			default:
				var body int
				body, next = heredocs(logicalLine(lines, i, k), lines, next)
				n += body
			}
			i = max(next, i+1)
		}
	}
	return n
}

// logicalEnd returns the last line of the logical line that starts at line i: lines that end in
// a backslash are read as one line.
func logicalEnd(lines []string, i int) int {
	k := i
	for k < len(lines)-1 && strings.HasSuffix(strings.TrimSpace(lines[k]), "\\") {
		k++
	}
	return k
}

func logicalLine(lines []string, i, k int) string {
	var b strings.Builder
	for j := i; j <= k; j++ {
		b.WriteString(strings.TrimSuffix(strings.TrimSpace(lines[j]), "\\"))
		b.WriteString(" ")
	}
	return b.String()
}

// quotedLines counts the lines of the quoted text opened at open, and returns its closing quote. A
// quote of two characters (\") is a quote inside a quoted string of YAML or JSON: the text ends
// with the line, and a \n in it is a line.
func quotedLines(text string, open int, quote string, lineEnd int) (int, int) {
	limit := len(text)
	if len(quote) == 2 {
		limit = lineEnd
	}
	closing := closingQuote(text[:limit], open+len(quote), quote)
	program := text[open:closing]
	n := strings.Count(program, "\n") + 1
	if len(quote) == 2 {
		n += escapedNewlines(program)
	}
	return n, closing
}

// heredocs counts the bodies of the here-documents opened on a logical line, in order: inside the
// line when it is a quoted string of YAML or JSON (lines written \n), else on the lines from next
// on. A << inside a body that was read is part of that body. It returns the line after the last
// body.
func heredocs(line string, lines []string, next int) (int, int) {
	n, read := 0, 0
	for _, m := range heredocRe.FindAllStringSubmatchIndex(line, -1) {
		if m[0] < read {
			continue
		}
		delim := ""
		for g := 2; g < len(m); g += 2 {
			if m[g] >= 0 {
				delim = line[m[g]:m[g+1]]
			}
		}
		if body, size, ok := inlineBody(line[m[1]:], delim); ok {
			n += body
			read = m[1] + size
			continue
		}
		j := next
		for j < len(lines) && strings.TrimSpace(lines[j]) != delim {
			j++
		}
		n += j - next
		next = j + 1
	}
	return n, next
}

// inlineBody counts the lines of a here-document written inside one quoted line as
// "<<'PY'\n...\nPY": the \n before the delimiter that ends it, and the lines before it. size is
// the length of the body with its delimiter.
func inlineBody(rest, delim string) (lines, size int, ok bool) {
	for from := 0; ; {
		at := strings.Index(rest[from:], `\n`+delim)
		if at < 0 {
			return 0, 0, false
		}
		at += from
		size = at + 2 + len(delim)
		after := rest[size:]
		if after == "" || strings.HasPrefix(after, `\n`) || strings.HasPrefix(after, `"`) || strings.HasPrefix(after, `'`) ||
			strings.HasPrefix(after, " ") {
			return escapedNewlines(rest[:at]), size, true
		}
		from = at + 2
	}
}

// closingQuote returns the offset of the quote that closes a quoted text, or the end of the text.
// A quote of two characters (\" or \') is a quote inside a quoted string of YAML or JSON.
func closingQuote(text string, from int, quote string) int {
	for i := from; i < len(text); i++ {
		switch {
		case quote == `'`:
			if text[i] == '\'' {
				return i
			}
		case quote == `"` && text[i] == '\\', len(quote) == 2 && strings.HasPrefix(text[i:], `\\`):
			i++ // an escaped character
		case strings.HasPrefix(text[i:], quote):
			return i
		}
	}
	return len(text)
}

// pythonBlock reads a YAML block scalar opened at line i that holds Python: its key ends in
// .py, or its first line is a #! line that names python. A body that is one template action at
// any indent ({{ .Files.Get ... | default "...\n..." | indent 4 }}) is its line and each \n in it.
// It returns the lines of the body and the line after it.
func pythonBlock(lines []string, i int) (int, int, bool) {
	m := blockRe.FindStringSubmatch(lines[i])
	if m == nil {
		return 0, 0, false
	}
	indent, last, first := indentOf(lines[i]), i, ""
	j := i + 1
	for ; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		if indentOf(lines[j]) <= indent {
			break
		}
		if first == "" {
			first = strings.TrimSpace(lines[j])
		}
		last = j
	}
	isPy := strings.HasSuffix(m[2], ".py")
	if last == i && isPy && j < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[j]), "{{") {
		return 1 + escapedNewlines(lines[j]), j + 1, true
	}
	if !isPy && !shebangRe.MatchString(first) {
		return 0, 0, false
	}
	return last - i, last + 1, true
}

// scan returns the places of one file of a scope.
func scan(scope, p, text string) []place {
	var out []place
	if strings.HasSuffix(p, ".py") {
		if shebangRe.MatchString(firstLine([]byte(text))) {
			out = append(out, place{"shebang", p, 1})
		}
		return out
	}
	text = templateComment.ReplaceAllStringFunc(text, func(m string) string {
		return strings.Repeat("\n", strings.Count(m, "\n"))
	})
	logical, start := "", 0
	flush := func() {
		if start > 0 {
			for _, kind := range classify(scope, logical) {
				out = append(out, place{kind, p, start})
			}
		}
		logical, start = "", 0
	}
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if strings.HasPrefix(line, "#!") && shebangRe.MatchString(line) {
			out = append(out, place{"shebang", p, i + 1})
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue // a comment, also inside a continued line
		}
		if start == 0 {
			start = i + 1
		}
		if strings.HasSuffix(line, "\\") {
			logical += strings.TrimSuffix(line, "\\") + " "
			continue
		}
		logical += line
		flush()
	}
	flush()
	sort.SliceStable(out, func(i, j int) bool { return out[i].line < out[j].line })
	return out
}

// classify returns the kinds of the commands of one logical line.
func classify(scope, line string) []string {
	if scope == scopeImage {
		if labelRe.MatchString(line) {
			return nil
		}
		if fromPythonRe.MatchString(line) {
			return []string{"install"}
		}
	}
	var out []string
	for _, command := range commandSep.Split(line, -1) {
		switch {
		case pipInstallRe.MatchString(command), imageRe.MatchString(command),
			pkgInstallRe.MatchString(command) && pythonPkgRe.MatchString(command):
			out = append(out, "install")
		case invokeRe.MatchString(command):
			out = append(out, "invoke")
		}
	}
	return out
}

// ---- the baseline

func parseBaseline(data []byte, where string) (*baseline, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var b baseline
	if err := dec.Decode(&b); err != nil {
		return nil, fmt.Errorf("%s: %v", where, err)
	}
	if b.Schema != schema || b.Exceptions == nil || b.PythonFiles == nil || b.Sites == nil {
		return nil, fmt.Errorf("%s: need schema %s and the keys exceptions, python_files and sites", where, schema)
	}
	for p, n := range b.PythonFiles {
		if p == "" || n < 1 {
			return nil, fmt.Errorf("%s: python_files.%s is %d; a listed file holds 1 line of Python or more", where, p, n)
		}
	}
	if len(b.Sites) != len(kinds) {
		return nil, fmt.Errorf("%s: sites holds exactly the kinds %s", where, strings.Join(kinds, ", "))
	}
	for _, kind := range kinds {
		paths, ok := b.Sites[kind]
		if !ok || paths == nil {
			return nil, fmt.Errorf("%s: sites holds exactly the kinds %s", where, strings.Join(kinds, ", "))
		}
		for p, n := range paths {
			if n < 1 {
				return nil, fmt.Errorf("%s: sites.%s.%s is %d; a listed path holds 1 place or more", where, kind, p, n)
			}
		}
	}
	return &b, nil
}

// holdsPython is true when a path is a Python file or holds a place.
func (t *tree) holdsPython(p string) bool {
	if _, ok := t.python[p]; ok {
		return true
	}
	for _, pl := range t.places {
		if pl.path == p {
			return true
		}
	}
	return false
}

// checkExceptions returns the paths the exception list takes out and a failure per bad entry (rule 4).
func checkExceptions(b *baseline, t *tree) (map[string]bool, []string) {
	out, seen := map[string]bool{}, map[string]bool{}
	var fails []string
	for i, e := range b.Exceptions {
		where := fmt.Sprintf("exceptions[%d] (%s)", i, e.Path)
		reason := strings.TrimSpace(e.Reason)
		switch {
		case e.Path == "":
			fails = append(fails, fmt.Sprintf("%s: an exception names a file", where))
		case seen[e.Path]:
			fails = append(fails, fmt.Sprintf("%s: a second exception for the same file", where))
		case len(reason) < 10 || strings.ContainsAny(e.Reason, "\r\n"):
			fails = append(fails, fmt.Sprintf("%s: an exception has a reason of one line and ten characters or more: why the file cannot be Go", where))
		case !t.listed[e.Path]:
			fails = append(fails, fmt.Sprintf("%s: Git lists no such file; remove the exception", where))
		case !t.holdsPython(e.Path):
			fails = append(fails, fmt.Sprintf("%s: the file holds no Python and no place; remove the exception", where))
		default:
			out[e.Path] = true
		}
		seen[e.Path] = true
	}
	return out, fails
}

func excepted(b *baseline, t *tree) map[string]bool {
	out, _ := checkExceptions(b, t)
	return out
}

// counted returns the files that hold Python with their lines, and the places, without the exceptions.
func counted(t *tree, skip map[string]bool) (map[string]int, map[string]map[string][]int) {
	files := map[string]int{}
	for p, n := range t.python {
		if !skip[p] {
			files[p] = n
		}
	}
	sites := map[string]map[string][]int{}
	for _, kind := range kinds {
		sites[kind] = map[string][]int{}
	}
	for _, pl := range t.places {
		if !skip[pl.path] {
			sites[pl.kind][pl.path] = append(sites[pl.kind][pl.path], pl.line)
		}
	}
	return files, sites
}

func summary(t *tree, skip map[string]bool) string {
	files, sites := counted(t, skip)
	lines, inner, innerLines := 0, 0, 0
	for p, n := range files {
		lines += n
		if t.embedded[p] {
			inner++
			innerLines += n
		}
	}
	total, per := 0, []string{}
	for _, kind := range kinds {
		n := 0
		for _, at := range sites[kind] {
			n += len(at)
		}
		total += n
		per = append(per, fmt.Sprintf("%s=%d", kind, n))
	}
	return fmt.Sprintf("python_files=%d python_lines=%d (inside other files: %d files, %d lines) places=%d (%s) exceptions=%d",
		len(files), lines, inner, innerLines, total, strings.Join(per, " "), len(skip))
}

const lower = "lower the baseline with --write-baseline"

// judge applies rules 1 to 5: grown is a file or place above the baseline (rules 1, 2, 3, 5),
// stale is one below it (rule 4).
func judge(t *tree, b *baseline) (grown, stale []string) {
	skip, grown := checkExceptions(b, t)
	files, sites := counted(t, skip)
	for _, p := range unionKeys(files, b.PythonFiles) {
		now, was := files[p], b.PythonFiles[p]
		what := "a Python file"
		if t.embedded[p] {
			what = "a file with Python inside"
		}
		switch {
		case now > was && was == 0:
			grown = append(grown, fmt.Sprintf("a new file that holds Python: %s (%d lines, %s)", p, now, what))
		case now > was:
			grown = append(grown, fmt.Sprintf("%s holds %d lines of Python, the baseline allows %d", p, now, was))
		case now < was && now == 0:
			stale = append(stale, fmt.Sprintf("%s is listed with %d lines but holds no counted Python now; %s", p, was, lower))
		case now < was:
			stale = append(stale, fmt.Sprintf("%s holds %d lines of Python, the baseline still says %d; %s", p, now, was, lower))
		}
	}
	for _, kind := range kinds {
		at := map[string]int{}
		for p, lines := range sites[kind] {
			at[p] = len(lines)
		}
		for _, p := range unionKeys(at, b.Sites[kind]) {
			now, was := at[p], b.Sites[kind][p]
			switch {
			case now > was && was == 0:
				grown = append(grown, fmt.Sprintf("a new place where Python runs or is installed: %s %s (line %s)", kind, p, joinInts(sites[kind][p])))
			case now > was:
				grown = append(grown, fmt.Sprintf("%s: %s has %d places, the baseline allows %d (lines %s)", kind, p, now, was, joinInts(sites[kind][p])))
			case now < was:
				stale = append(stale, fmt.Sprintf("%s: %s has %d places, the baseline still says %d; %s", kind, p, now, was, lower))
			}
		}
	}
	return grown, stale
}

func unionKeys(a, b map[string]int) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func joinInts(xs []int) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ", ")
}

// writeBaseline writes the baseline from the tree, or refuses when that would raise it.
func writeBaseline(root string, t *tree, out io.Writer) (int, error) {
	target := filepath.Join(root, baselinePath)
	keep := []exception{}
	data, err := fs.ReadFile(os.DirFS(root), baselinePath)
	switch {
	case err == nil:
		old, err := parseBaseline(data, baselinePath)
		if err != nil {
			return 0, err
		}
		if fails, _ := judge(t, old); len(fails) > 0 {
			for _, f := range fails {
				say(out, "%s: FAIL %s\n", me, f)
			}
			say(out, "%s: the baseline was not written: a baseline only falls, and it never gains a file or a place. %s\n", me, goLine)
			return 1, nil
		}
		keep = old.Exceptions
	case errors.Is(err, fs.ErrNotExist):
		// The first write: the exception list starts empty.
	default:
		return 0, fmt.Errorf("%s cannot be read: %v", baselinePath, err)
	}
	skip, _ := checkExceptions(&baseline{Exceptions: keep}, t)
	files, sites := counted(t, skip)
	nb := baseline{Exceptions: keep, PythonFiles: files, Schema: schema, Sites: map[string]map[string]int{}}
	for _, kind := range kinds {
		nb.Sites[kind] = map[string]int{}
		for p, at := range sites[kind] {
			nb.Sites[kind][p] = len(at)
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(nb); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, err
	}
	if err := os.WriteFile(target, buf.Bytes(), 0o600); err != nil {
		return 0, err
	}
	say(out, "%s: wrote %s\n", me, baselinePath)
	return 0, nil
}

// ---- rule 5: the baseline at the base commit

// catFile reads one object of the repository at root; ok is false when it does not exist.
func catFile(root, object string) (kind string, body []byte, ok bool, err error) {
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(object + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", nil, false, fmt.Errorf("git cat-file in %s: %v %s", root, err, strings.TrimSpace(stderr.String()))
	}
	nl := bytes.IndexByte(b, '\n')
	if nl < 0 {
		return "", nil, false, fmt.Errorf("git cat-file gave no answer for %s", object)
	}
	header := string(b[:nl])
	if strings.HasSuffix(header, " missing") || strings.HasSuffix(header, " ambiguous") {
		return "", nil, false, nil
	}
	fields := strings.Fields(header)
	if len(fields) != 3 {
		return "", nil, false, fmt.Errorf("git cat-file: unexpected answer %q for %s", header, object)
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil || len(b) < nl+1+size {
		return "", nil, false, fmt.Errorf("git cat-file: unexpected answer %q for %s", header, object)
	}
	return fields[1], b[nl+1 : nl+1+size], true, nil
}

func compareBase(root, base string, cur *baseline) (fails []string, note string, err error) {
	kind, _, ok, err := catFile(root, base+"^{commit}")
	if err != nil {
		return nil, "", err
	}
	if !ok || kind != "commit" {
		return nil, "", fmt.Errorf("the base %s is not a commit of this checkout", base)
	}
	kind, data, ok, err := catFile(root, base+":"+baselinePath)
	if err != nil {
		return nil, "", err
	}
	if !ok || kind != "blob" {
		return nil, fmt.Sprintf("%s does not exist at %s; there is nothing to compare", baselinePath, base), nil
	}
	old, err := parseBaseline(data, baselinePath+" at "+base)
	if err != nil {
		return nil, "", err
	}
	for _, p := range sortedKeys(cur.PythonFiles) {
		now, was := cur.PythonFiles[p], old.PythonFiles[p]
		switch {
		case was == 0:
			fails = append(fails, fmt.Sprintf("%s: python_files lists %s, which it did not list at %s. A baseline only falls", baselinePath, p, base))
		case now > was:
			fails = append(fails, fmt.Sprintf("%s: python_files lists %s with %d, higher than %d at %s. A baseline only falls", baselinePath, p, now, was, base))
		}
	}
	for _, kind := range kinds {
		for _, p := range sortedKeys(cur.Sites[kind]) {
			if now, was := cur.Sites[kind][p], old.Sites[kind][p]; now > was {
				fails = append(fails, fmt.Sprintf("%s: sites.%s lists %s with %d, %d at %s. A baseline only falls", baselinePath, kind, p, now, was, base))
			}
		}
	}
	return fails, "", nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func printList(t *tree, out io.Writer) {
	files := make([]string, 0, len(t.python))
	for p := range t.python {
		files = append(files, p)
	}
	sort.Strings(files)
	for _, p := range files {
		how := "file  "
		if t.embedded[p] {
			how = "inside"
		}
		say(out, "%s %6d %s\n", how, t.python[p], p)
	}
	places := append([]place(nil), t.places...)
	sort.SliceStable(places, func(i, j int) bool {
		if places[i].kind != places[j].kind {
			return places[i].kind < places[j].kind
		}
		if places[i].path != places[j].path {
			return places[i].path < places[j].path
		}
		return places[i].line < places[j].line
	})
	for _, pl := range places {
		say(out, "place %-7s %s:%d\n", pl.kind, pl.path, pl.line)
	}
}

// escapedNewlines counts the \n of a quoted string of YAML, JSON or a template: a \\n is a
// backslash and an n, not a line.
func escapedNewlines(s string) int {
	n := 0
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '\\' {
			if s[i+1] == 'n' {
				n++
			}
			i++
		}
	}
	return n
}
