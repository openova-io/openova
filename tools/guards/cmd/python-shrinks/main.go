// Command python-shrinks keeps the Python of this repository from growing.
//
// The languages of this repository are Go, Rust where a design record says so,
// TypeScript for the consoles and shell for a few lines of glue
// (docs/PRINCIPLES.md IV.4). No new Python is written, neither as a file nor
// inside another file. The Python that exists stays for now, so this guard only
// prevents: it measures five totals and fails when one of them rises.
//
// It reads the files Git lists (the tracked files, and new files Git does not
// ignore). A Python file is a path that ends in .py, or a file whose first line
// is a #! line that names python. A command file is any other file that is a
// shell file (.sh .bash .zsh .ksh .bats), YAML (.yaml .yml), JSON (.json), a
// template (.tpl .tftpl .j2), Terraform or HCL (.tf .hcl), a make or just file,
// Go (.go), Rust (.rs), TypeScript or JavaScript (.ts .tsx .mts .cts .js .jsx
// .mjs .cjs), a file of a scope below, or a file whose first line is a #! line;
// never a .md or .txt file. A comment line is one that starts with # or //, but
// not a //go:generate line, which runs its command. Of a Go, Rust, TypeScript or
// JavaScript file only the lines that can run python are read: a //go:generate
// line, a line that holds a string that is only the name of python ("python3"),
// and the lines of a call that starts a process (exec.Command,
// exec.CommandContext, syscall.Exec, os.StartProcess, Command::new, spawn,
// spawnSync, exec, execSync, execFile, execFileSync, fork, execa, Bun.spawn,
// Deno.Command) from its name to its closing parenthesis; and a string that
// starts with a #! line that names python is Python wherever it stands (a stub
// that a test writes with os.WriteFile and runs). Any other string elsewhere,
// such as the fixture of a test, is data. The totals are the keys of
// scripts/baselines/python.json:
//
//	python_files       the Python files
//	python_file_lines  the lines of the Python files
//	embedded_lines     the lines of Python inside the command files:
//	                   - the body of a here-document opened on a line that runs
//	                     python or writes a .py file (<< or <<-, quoted or not);
//	                   - the body of any other here-document whose first line is
//	                     a #! line that names python (cat > "$BIN/curl" <<'PY'
//	                     then #!/usr/bin/env python3: a program saved without
//	                     the .py suffix and run later), the #! line included;
//	                   - a string of a Go, Rust, TypeScript or JavaScript file
//	                     that starts with a #! line that names python: its lines
//	                     from the #! line to the last line that holds text (a \n
//	                     of a string that is not raw ends a line);
//	                   - the quoted program of python -c (a Go raw string or a
//	                     template literal in backquotes too, and an argument
//	                     list such as ["-c", ...]), and of a here-string
//	                     (<<<) given to python, from its quote to the closing
//	                     quote; when it is "$NAME", the text assigned to NAME;
//	                   - the body of a YAML block scalar (| or >) whose key ends
//	                     in .py, or whose first line is a #! line that names
//	                     python;
//	                   - the run of a step of a workflow (.github/workflows/) or
//	                     of an action (action.yml, action.yaml) whose shell is
//	                     python (shell: python, python3, python {0}): the shell of
//	                     the step, or else the run default (defaults: run: shell:)
//	                     of its job or of the workflow.
//	                   A comment line opens nothing; lines that end in a
//	                   backslash are read as one line.
//	invoke_lines       the lines of the command files that NAME python, python3
//	                   or python3.N as a word (the pattern of the kind invoke
//	                   below), a comment line left out: exec.Command("python3",
//	                   ...) and //go:generate python3 count, and so do the text
//	                   of an echo and prose in a YAML string that name python,
//	                   which only makes the total stricter. Of a Go, Rust,
//	                   TypeScript or JavaScript file only the lines read above
//	                   count. This is how a Python program saved without the .py
//	                   suffix is run.
//	sovereign_places   the places where Python runs on a Sovereign or is
//	                   installed in a shipped image, in the files of a scope.
//
// The scopes:
//
//	chart  a file under templates/ or files/ of a chart (a folder that holds a
//	       Chart.yaml Git lists), other than a .md or .txt file
//	node   a file under infra/providers/ that builds the start-up data of the
//	       nodes: a template whose name starts with cloudinit or cloud-init and
//	       ends in .tftpl, .tpl, .yaml or .yml, and every Terraform file (.tf),
//	       whose here-documents templatefile() puts into that cloud-init
//	image  a file whose name starts with Containerfile or Dockerfile
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
// The guard fails (exit 1) only when a total is higher than its baseline. It
// names the total and by how much, and with --base <commit> it lists the files
// whose share of that total grew against <commit> (a renamed or moved file is
// compared with its old path). With --base the baseline of a total is the lower
// of the baseline file and the same file at <commit>, so a baseline raised in
// the change under test raises nothing; and when the baseline file exists at
// <commit>, also the total of <commit> itself (the total of the tree less what
// each file of the change added, a deleted file included), so room that an
// earlier change freed without lowering the baseline file is not taken again.
// A total lower than the baseline file passes, with one line that says the
// baseline can be lowered. There is no list of files and no exception list: a
// rename or a move changes no total, and a new .py file raises python_files.
//
// What the guard does not see, by design: Python assembled or fetched at run
// time (a program built from pieces, downloaded, or read from a Secret), an
// encoded payload (base64 or compressed), and a file run by an interpreter chosen
// at run time ("$INTERP" "$f"). docs/PRINCIPLES.md IV.4 forbids those all the
// same; review holds them.
//
// --write-baseline writes the five totals of the tree. It refuses (exit 1) when
// a total is higher than the baseline file, so it only lowers; when the file
// does not exist it writes the first one. Exit 2 when the tree, the baseline
// file or the base commit cannot be read, and when the baseline file is missing
// (without --write-baseline).
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
	"unicode"
	"unicode/utf8"
)

const (
	me           = "python-shrinks"
	baselinePath = "scripts/baselines/python.json"
	schema       = "openova.io/guard-baseline/v2"
	goLine       = "New code and tooling are written in Go (tools/guards, docs/PRINCIPLES.md IV.4); no new Python, neither as a file nor inside another file."
	lowerLine    = "the baseline can be lowered: cd tools/guards && go run ./cmd/python-shrinks --write-baseline"

	scopeChart = "chart"
	scopeNode  = "node"
	scopeImage = "image"
)

// The totals, in the order of the baseline file.
const (
	pyFiles = iota
	pyFileLines
	embeddedLines
	invokeLines
	sovereignPlaces
	nTotals
)

var totalNames = [nTotals]string{"python_files", "python_file_lines", "embedded_lines", "invoke_lines", "sovereign_places"}

// counts holds one number per total: of the tree, of a baseline, or the share of one file.
type counts [nTotals]int

func (c *counts) add(o counts) {
	for i := range c {
		c[i] += o[i]
	}
}

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
	pyProgramRe  = regexp.MustCompile("(?:^|[\\s\"'\\[(=`{]|:-)(?:[A-Za-z0-9_.-]*/)*python[0-9]*(?:\\.[0-9]+)?[\"'`]?(?:(?:[\\s,\\[]+[\"'`]?-[A-Za-z]+[\"'`]?)*?[\\s,\\[]+(?:[\"'`]-c[\"'`]|-c)[\\s,]*|(?:\\s+-[A-Za-z]*)*\\s*<<<\\s*)\\$?(\\\\?['\"]|`)")
	programVar   = regexp.MustCompile(`^\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?$`)
	blockRe      = regexp.MustCompile(`^([ \t]*)(?:-[ \t]+)?(?:["']?([^"'\s:#][^"':]*?)["']?[ \t]*:[ \t]+)?[|>][-+1-9]*[ \t]*(?:#.*)?$`)
	embeddedExts = map[string]bool{".sh": true, ".bash": true, ".zsh": true, ".ksh": true, ".bats": true, ".yaml": true, ".yml": true,
		".json": true, ".tpl": true, ".tftpl": true, ".tf": true, ".hcl": true, ".mk": true, ".j2": true}
	embeddedNames = []string{"Makefile", "makefile", "GNUmakefile", "Justfile", "justfile"}

	// Go, Rust, TypeScript and JavaScript: only the lines that can run python are read (runLines).
	codeExts = map[string]bool{".go": true, ".rs": true, ".ts": true, ".tsx": true, ".mts": true, ".cts": true, ".js": true, ".jsx": true,
		".mjs": true, ".cjs": true}
	processCallRe = regexp.MustCompile(`\b(?:exec\.Command(?:Context)?|syscall\.Exec|os\.StartProcess|Command::new|spawn(?:Sync)?|exec(?:Sync)?|execFile(?:Sync)?|fork|execa(?:Sync|Command|CommandSync)?|Bun\.spawn(?:Sync)?|Deno\.Command)\s*\(`)
	pyNameRe      = regexp.MustCompile(`^(?:[A-Za-z0-9_.-]*/)*python[0-9]*(?:\.[0-9]+)?$`)

	// The steps of a workflow or an action.
	yamlKeyRe  = regexp.MustCompile(`^( *)((?:- +)*)([A-Za-z_][A-Za-z0-9_-]*) *:(?: +(.*))?$`)
	blockValRe = regexp.MustCompile(`^[|>][-+0-9]*(?: +#.*)?$`)
)

type place struct {
	kind string
	path string
	line int
}

// tree is what the guard measured.
type tree struct {
	files  map[string]counts // a file with a share of a total -> its share of each total
	places []place
	total  counts
	charts map[string]bool // the folders that hold a Chart.yaml
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
	base := flags.String("base", "", "list the files that grew against this commit, and use the lower of the baseline file and the file at this commit")
	write := flags.Bool("write-baseline", false, "write "+baselinePath+" from the tree; refuses to raise it")
	list := flags.Bool("list", false, "print the share of every file and every place, then judge")
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
		top, err := gitOut("", "rev-parse", "--show-toplevel")
		if err != nil {
			return 0, fmt.Errorf("the current folder is not in a Git work tree: %v", err)
		}
		root = strings.TrimSpace(top)
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
	written, err := parseBaseline(data, baselinePath)
	if err != nil {
		return 0, err
	}
	limit := written
	var notes []string
	var grown [nTotals][]string
	if base != "" {
		var inForce bool
		if inForce, notes, err = lowerToBase(root, base, &limit); err != nil {
			return 0, err
		}
		var baseTotal counts
		if baseTotal, grown, err = againstBase(root, base, t); err != nil {
			return 0, err
		}
		for i := range limit {
			// Room that an earlier change freed without lowering the baseline file is not taken again.
			if inForce && baseTotal[i] < limit[i] {
				notes = append(notes, fmt.Sprintf("%s is %d at %s, below the baseline %d: room freed before is not taken again; %d is used",
					totalNames[i], baseTotal[i], base, limit[i], baseTotal[i]))
				limit[i] = baseTotal[i]
			}
		}
	}
	say(out, "%s: %s\n", me, summary(t))
	for _, n := range notes {
		say(out, "%s: note: %s\n", me, n)
	}
	rose, _ := judge(t.total, limit)
	if _, fell := judge(t.total, written); len(fell) > 0 {
		var parts []string
		for _, i := range fell {
			parts = append(parts, fmt.Sprintf("%s %d (baseline %d)", totalNames[i], t.total[i], written[i]))
		}
		say(out, "%s: lower than the baseline: %s; %s\n", me, strings.Join(parts, ", "), lowerLine)
	}
	if len(rose) == 0 {
		return 0, nil
	}
	for _, i := range rose {
		say(out, "%s: FAIL %s is %d, %d more than the baseline %d\n", me, totalNames[i], t.total[i], t.total[i]-limit[i], limit[i])
		if base == "" {
			continue
		}
		if len(grown[i]) == 0 {
			say(out, "%s:   no file of the change grew against %s\n", me, base)
		}
		for _, g := range grown[i] {
			say(out, "%s:   %s against %s\n", me, g, base)
		}
	}
	say(out, "%s: %d total(s) above the baseline. %s\n", me, len(rose), goLine)
	return 1, nil
}

// judge returns the totals above their baseline and the totals below it.
func judge(now, limit counts) (rose, fell []int) {
	for i := range now {
		switch {
		case now[i] > limit[i]:
			rose = append(rose, i)
		case now[i] < limit[i]:
			fell = append(fell, i)
		}
	}
	return rose, fell
}

// ---- measuring the tree

func gitOut(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %q: %v %s", strings.Join(args, " "), root, err, strings.TrimSpace(stderr.String()))
	}
	return string(b), nil
}

// splitZ splits the output of a git command run with -z.
func splitZ(s string) []string {
	var out []string
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// listFiles returns the files Git lists below root: tracked, and new files Git does not ignore.
func listFiles(root string) ([]string, error) {
	b, err := gitOut(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var paths []string
	for _, p := range splitZ(b) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

// chartsOf returns the folders that hold a Chart.yaml.
func chartsOf(paths []string) map[string]bool {
	charts := map[string]bool{}
	for _, p := range paths {
		if path.Base(p) == "Chart.yaml" {
			charts[path.Dir(p)] = true
		}
	}
	return charts
}

func measure(root string) (*tree, error) {
	paths, err := listFiles(root)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("git lists no file in %s", root)
	}
	charts := chartsOf(paths)
	t := &tree{files: map[string]counts{}, charts: charts}
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
		scope := scopeOf(p, charts)
		var text []byte
		if strings.HasSuffix(p, ".py") || readsEmbedded(p, scope, "") {
			text, err = fs.ReadFile(fsys, p)
		} else if text, err = readHead(fsys, p, 512); err == nil && bytes.HasPrefix(text, []byte("#!")) {
			text, err = fs.ReadFile(fsys, p)
		}
		if err != nil {
			return nil, err
		}
		c, places := measureFile(p, scope, text)
		if c != (counts{}) {
			t.files[p] = c
			t.total.add(c)
		}
		t.places = append(t.places, places...)
	}
	return t, nil
}

// measureFile returns the share of one file of each total, and its places.
func measureFile(p, scope string, text []byte) (counts, []place) {
	var c counts
	if bytes.IndexByte(text, 0) >= 0 {
		// Not text. A .py path is a Python file by its path all the same.
		if strings.HasSuffix(p, ".py") {
			c[pyFiles], c[pyFileLines] = 1, countLines(text)
		}
		return c, nil
	}
	first := firstLine(text)
	switch {
	case strings.HasSuffix(p, ".py") || shebangRe.MatchString(first):
		c[pyFiles], c[pyFileLines] = 1, countLines(text)
	case readsEmbedded(p, scope, first):
		s := string(text)
		var kept []bool // nil: every line is read
		if codeExts[path.Ext(p)] {
			var stubs int
			kept, s, stubs = runLines(s, path.Ext(p))
			c[embeddedLines] += stubs
		}
		if actionFile(p) {
			var steps int
			steps, s = pythonSteps(s)
			c[embeddedLines] += steps
		}
		c[embeddedLines] += embedded(s, kept)
		c[invokeLines] = invocations(string(text), kept)
	}
	var places []place
	if scope != "" {
		places = scan(scope, p, string(text))
		c[sovereignPlaces] = len(places)
	}
	return c, places
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
	if strings.HasPrefix(p, "infra/providers/") && (cloudInitName.MatchString(name) || strings.HasSuffix(name, ".tf")) {
		return scopeNode
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

// readsEmbedded is true for a command file: one that is read for Python inside it and for
// lines that run python.
func readsEmbedded(p, scope, first string) bool {
	name := path.Base(p)
	if strings.HasSuffix(name, ".md") || strings.HasSuffix(name, ".txt") {
		return false
	}
	if scope != "" || embeddedExts[path.Ext(name)] || codeExts[path.Ext(name)] || strings.HasPrefix(first, "#!") {
		return true
	}
	for _, prefix := range embeddedNames {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// actionFile is true for a workflow or an action, whose steps may name a shell.
func actionFile(p string) bool {
	ext, name := path.Ext(p), path.Base(p)
	return (strings.HasPrefix(p, ".github/workflows/") && (ext == ".yaml" || ext == ".yml")) ||
		name == "action.yml" || name == "action.yaml"
}

// commentLine is true for a trimmed line that starts with # or //, other than a //go:generate
// line, which runs its command.
func commentLine(t string) bool {
	return strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") && !strings.HasPrefix(t, "//go:generate")
}

// invocations counts the lines that name python as a command; a comment line is not read, nor a
// line that kept does not hold (nil holds every line).
func invocations(text string, kept []bool) int {
	n := 0
	for i, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || commentLine(t) || kept != nil && !kept[i] {
			continue
		}
		if invokeRe.MatchString(t) {
			n++
		}
	}
	return n
}

// runLines marks the lines of a Go, Rust, TypeScript or JavaScript file (by its extension ext)
// that can run python: a //go:generate line; a line that holds a string that is only the name of
// python ("python3", "/usr/bin/python3"); and the lines of a call that starts a process
// (exec.Command, Command::new, spawn, ...), from its name to its closing parenthesis. A string
// that starts with a #! line that names python is Python wherever it stands: runLines returns the
// number of its lines (stubs) and the text with each such string blanked, quotes included, so that
// it is not read again as a program given to python -c. Any other string elsewhere, such as the
// fixture of a test, is data. The file is read as code, strings (" ' and backquotes; in Rust also
// raw strings r"..." r#"..."#, a " string over lines, and a lifetime 'a that is code) and comments
// (// and /* */).
func runLines(text, ext string) (kept []bool, data string, stubs int) {
	rust := ext == ".rs"
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	lineAt := func(off int) int { return sort.SearchInts(starts, off+1) - 1 }
	kept = make([]bool, len(starts))
	code := make([]bool, len(text))
	var blanked []byte
	// str reads the string whose body is text[from:to] and whose quotes span text[open:end].
	str := func(open, from, to, end int, raw bool) {
		body := text[from:to]
		if pyNameRe.MatchString(body) {
			kept[lineAt(open)] = true
		}
		if n, ok := pythonStub(body, raw); ok {
			stubs += n
			if blanked == nil {
				blanked = []byte(text)
			}
			for k := open; k < end; k++ {
				if blanked[k] != '\n' {
					blanked[k] = ' '
				}
			}
		}
	}
	for i := 0; i < len(text); {
		c := text[i]
		rawOpen, hashes := 0, 0
		if rust && (c == 'r' || c == 'b') {
			rawOpen, hashes = rustRawOpen(text, i)
		}
		switch {
		case strings.HasPrefix(text[i:], "//"):
			if strings.HasPrefix(text[i:], "//go:generate") {
				kept[lineAt(i)] = true
			}
			end := strings.IndexByte(text[i:], '\n')
			if end < 0 {
				end = len(text) - i
			}
			i += end
		case strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				end = len(text) - i - 4
			}
			i += end + 4
		case rawOpen > 0:
			from := i + rawOpen
			to := len(text)
			if at := strings.Index(text[from:], `"`+strings.Repeat("#", hashes)); at >= 0 {
				to = from + at
			}
			end := min(to+1+hashes, len(text))
			str(i, from, to, end, true)
			i = end
		case rust && c == '\'' && !rustChar(text, i):
			code[i] = true // a lifetime or a label
			i++
		case c == '"' || c == '\'' || c == '`':
			// A Go raw string has no escapes; a " string of Rust and a template literal may span lines.
			raw, lines := c == '`' && ext == ".go", c == '`' || rust && c == '"'
			j := i + 1
			for j < len(text) && text[j] != c && (lines || text[j] != '\n') {
				if text[j] == '\\' && !raw {
					j++
				}
				j++
			}
			to := min(j, len(text))
			str(i, i+1, to, min(j+1, len(text)), raw)
			i = j + 1
		default:
			code[i] = true
			i++
		}
	}
	data = text
	if blanked != nil {
		data = string(blanked)
	}
	for _, m := range processCallRe.FindAllStringIndex(text, -1) {
		if !code[m[0]] {
			continue // inside a string or a comment
		}
		last, depth := len(text)-1, 0
		for k := m[1] - 1; k < len(text) && last == len(text)-1; k++ {
			switch {
			case !code[k]:
			case text[k] == '(':
				depth++
			case text[k] == ')':
				if depth--; depth == 0 {
					last = k
				}
			}
		}
		for l := lineAt(m[0]); l <= lineAt(last); l++ {
			kept[l] = true
		}
	}
	return kept, data, stubs
}

// pythonStub is true for the body of a string that starts with a #! line that names python (blank
// space and a \n before it are passed over), and returns its lines: from the #! line to the last
// line that holds text. A newline of the source ends a line, and so does a \n of a string that is
// not raw.
func pythonStub(body string, raw bool) (int, bool) {
	s := strings.TrimLeft(body, " \t\r\n")
	for !raw && strings.HasPrefix(s, `\n`) {
		s = strings.TrimLeft(s[2:], " \t\r\n")
	}
	if !strings.HasPrefix(s, "#!") {
		return 0, false
	}
	first := s
	if k := strings.IndexByte(first, '\n'); k >= 0 {
		first = first[:k]
	}
	if k := strings.Index(first, `\n`); k >= 0 && !raw {
		first = first[:k]
	}
	if !shebangRe.MatchString(strings.TrimSuffix(first, "\r")) {
		return 0, false
	}
	for {
		s = strings.TrimRight(s, " \t\r\n")
		if raw || !endsInEscapedNewline(s) {
			break
		}
		s = s[:len(s)-2]
	}
	n := 1 + strings.Count(s, "\n")
	if !raw {
		n += escapedNewlines(s)
	}
	return n, true
}

// endsInEscapedNewline is true when s ends in a \n escape, not in an escaped backslash and an n.
func endsInEscapedNewline(s string) bool {
	if !strings.HasSuffix(s, `\n`) {
		return false
	}
	k := 0
	for i := len(s) - 2; i >= 0 && s[i] == '\\'; i-- {
		k++
	}
	return k%2 == 1
}

// rustRawOpen returns the length of the opener of a Rust raw string at i (r" r#" br##" ...) and the
// number of # in it, or 0 when none starts at i.
func rustRawOpen(text string, i int) (int, int) {
	if i > 0 && (text[i-1] == '_' || unicode.IsLetter(rune(text[i-1])) || unicode.IsDigit(rune(text[i-1]))) {
		return 0, 0 // inside a name
	}
	j := i
	if text[j] == 'b' {
		j++
	}
	if j >= len(text) || text[j] != 'r' {
		return 0, 0
	}
	j++
	h := 0
	for j < len(text) && text[j] == '#' {
		j++
		h++
	}
	if j >= len(text) || text[j] != '"' {
		return 0, 0
	}
	return j + 1 - i, h
}

// rustChar is true when the ' at i opens a Rust character literal ('x', '\n', '\u{1F600}'), not
// a lifetime or a label ('a, 'static).
func rustChar(text string, i int) bool {
	if i+1 >= len(text) {
		return false
	}
	if text[i+1] == '\\' {
		return true
	}
	_, size := utf8.DecodeRuneInString(text[i+1:])
	return i+1+size < len(text) && text[i+1+size] == '\''
}

func indentOf(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// ---- the steps of a workflow or an action whose shell is python

// yamlLine is one line of a YAML file, read as far as the steps need it.
type yamlLine struct {
	indent int // the spaces before its text; -1 for a blank line or a comment line
	col    int // the column of its key; -1 when it has none
	dash   bool
	key    string
	value  string
}

func readYAML(lines []string) []yamlLine {
	out := make([]yamlLine, len(lines))
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "#") {
			out[i] = yamlLine{indent: -1, col: -1}
			continue
		}
		y := yamlLine{indent: indentOf(l), col: -1}
		if m := yamlKeyRe.FindStringSubmatch(strings.TrimRight(l, " \t\r")); m != nil {
			y.col, y.dash, y.key, y.value = len(m[1])+len(m[2]), m[2] != "", m[3], strings.TrimSpace(m[4])
		}
		out[i] = y
	}
	return out
}

// mapping returns the lines [s, e) of the mapping that holds the key of line r, at column k.
func mapping(ys []yamlLine, r, k int) (int, int) {
	s := r
	if !ys[r].dash || ys[r].col != k {
		for s = r - 1; s >= 0; s-- {
			if y := ys[s]; y.indent >= 0 && (y.dash && y.col == k || y.indent < k) {
				if !y.dash || y.col != k {
					s++
				}
				break
			}
		}
		s = max(s, 0)
	}
	e := r + 1
	for e < len(ys) && (ys[e].indent < 0 || ys[e].indent >= k) {
		e++
	}
	return s, e
}

// parentKey returns the line of the key that holds the key of line i, or -1.
func parentKey(ys []yamlLine, i int) int {
	for j := i - 1; j >= 0; j-- {
		if ys[j].indent >= 0 && ys[j].indent < ys[i].indent {
			if ys[j].col < 0 {
				return -1
			}
			return j
		}
	}
	return -1
}

// pythonShell is true when the value of a shell key runs python.
func pythonShell(v string) bool {
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return invokeRe.MatchString(strings.Trim(strings.TrimSpace(v), `"'`))
}

// pythonSteps counts the lines of the run of every step whose shell is python, and returns the
// text with those lines blanked, so that they are not read again as Python inside the file.
func pythonSteps(text string) (int, string) {
	lines := strings.Split(text, "\n")
	ys := readYAML(lines)
	type rule struct {
		s, e, depth int
		python      bool
	}
	var defaults []rule
	for i, y := range ys {
		if y.key != "shell" {
			continue
		}
		run := parentKey(ys, i)
		if run < 0 || ys[run].key != "run" {
			continue
		}
		def := parentKey(ys, run)
		if def < 0 || ys[def].key != "defaults" {
			continue
		}
		s, e := mapping(ys, def, ys[def].col)
		defaults = append(defaults, rule{s, e, ys[def].col, pythonShell(y.value)})
	}
	n := 0
	for r, y := range ys {
		if y.key != "run" || y.value == "" {
			continue // a run that is a mapping holds no program: the run default of a job
		}
		s, e := mapping(ys, r, y.col)
		python, own := false, false
		for j := s; j < e; j++ {
			if ys[j].col == y.col && ys[j].key == "shell" {
				python, own = pythonShell(ys[j].value), true
			}
		}
		if !own {
			depth := -1
			for _, d := range defaults {
				if r >= d.s && r < d.e && d.depth > depth {
					python, depth = d.python, d.depth
				}
			}
		}
		if !python {
			continue
		}
		if !blockValRe.MatchString(y.value) {
			n += 1 + escapedNewlines(y.value)
			lines[r] = ""
			continue
		}
		last := r
		for j := r + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			if indentOf(lines[j]) <= y.col {
				break
			}
			last = j
		}
		n += last - r
		for j := r; j <= last; j++ {
			lines[j] = ""
		}
	}
	return n, strings.Join(lines, "\n")
}

// ---- Python inside a file of another language

// embedded returns the lines of Python inside a file of another language. Only a line that kept
// holds opens Python (nil holds every line); what it opens is read to its end.
func embedded(text string, kept []bool) int {
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
		if trimmed == "" || commentLine(trimmed) || kept != nil && !kept[i] {
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
		if programs == 0 {
			// On a line that runs python or writes a .py file every here-document is Python; on
			// any other line only one whose body starts with a #! line that names python.
			var body int
			body, next = heredocs(line, lines, next, !invokeRe.MatchString(line) && !writesPyRe.MatchString(line))
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
			case m == nil || commentLine(strings.TrimSpace(lines[i])):
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
				body, next = heredocs(logicalLine(lines, i, k), lines, next, false)
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
// body. With stubsOnly (the line neither runs python nor writes a .py file) it counts only a body
// whose first line is a #! line that names python, blanks the lines of that body in lines so that
// they are not read again, and returns next: the other bodies are read line by line as before.
func heredocs(line string, lines []string, next int, stubsOnly bool) (int, int) {
	n, read, at := 0, 0, next
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
			if !stubsOnly || shebangRe.MatchString(inlineFirst(line[m[1]:m[1]+size-2-len(delim)])) {
				n += body
			}
			read = m[1] + size
			continue
		}
		j := at
		for j < len(lines) && strings.TrimSpace(lines[j]) != delim {
			j++
		}
		switch {
		case !stubsOnly:
			n += j - at
		case j > at && shebangRe.MatchString(strings.TrimSpace(lines[at])):
			n += j - at
			for b := at; b < j; b++ {
				lines[b] = ""
			}
		}
		at = j + 1
	}
	if stubsOnly {
		return n, next
	}
	return n, at
}

// inlineFirst returns the first line of the body of a here-document written inside one quoted
// line: the text after the first \n of what follows the opener, up to the next \n.
func inlineFirst(rest string) string {
	k := strings.Index(rest, `\n`)
	if k < 0 {
		return ""
	}
	first := rest[k+2:]
	if e := strings.Index(first, `\n`); e >= 0 {
		first = first[:e]
	}
	return strings.TrimSpace(first)
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

// ---- places where Python runs on a Sovereign or is installed in a shipped image

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

func parseBaseline(data []byte, where string) (counts, error) {
	var c counts
	keys := "the keys schema (" + schema + ") and " + strings.Join(totalNames[:], ", ")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return c, fmt.Errorf("%s: %v", where, err)
	}
	var s string
	if len(raw) != nTotals+1 || json.Unmarshal(raw["schema"], &s) != nil || s != schema {
		return c, fmt.Errorf("%s: need exactly %s", where, keys)
	}
	for i, name := range totalNames {
		v, ok := raw[name]
		if !ok || json.Unmarshal(v, &c[i]) != nil || c[i] < 0 {
			return c, fmt.Errorf("%s: need exactly %s, each a whole number of 0 or more (%s)", where, keys, name)
		}
	}
	return c, nil
}

func encodeBaseline(c counts) []byte {
	var b strings.Builder
	b.WriteString("{\n  \"schema\": \"" + schema + "\"")
	for i, name := range totalNames {
		fmt.Fprintf(&b, ",\n  %q: %d", name, c[i])
	}
	b.WriteString("\n}\n")
	return []byte(b.String())
}

// writeBaseline writes the totals of the tree, or refuses when that would raise a total.
func writeBaseline(root string, t *tree, out io.Writer) (int, error) {
	data, err := fs.ReadFile(os.DirFS(root), baselinePath)
	switch {
	case err == nil:
		old, err := parseBaseline(data, baselinePath)
		if err != nil {
			return 0, err
		}
		if rose, _ := judge(t.total, old); len(rose) > 0 {
			for _, i := range rose {
				say(out, "%s: FAIL %s is %d, %d more than the baseline %d\n", me, totalNames[i], t.total[i], t.total[i]-old[i], old[i])
			}
			say(out, "%s: the baseline was not written: it is only lowered. %s\n", me, goLine)
			return 1, nil
		}
	case errors.Is(err, fs.ErrNotExist):
		// The first write.
	default:
		return 0, fmt.Errorf("%s cannot be read: %v", baselinePath, err)
	}
	target := filepath.Join(root, filepath.FromSlash(baselinePath))
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return 0, err
	}
	if err := os.WriteFile(target, encodeBaseline(t.total), 0o600); err != nil {
		return 0, err
	}
	say(out, "%s: wrote %s\n", me, baselinePath)
	return 0, nil
}

func summary(t *tree) string {
	per := map[string]int{}
	for _, pl := range t.places {
		per[pl.kind]++
	}
	var parts, kindParts []string
	for i, name := range totalNames {
		parts = append(parts, fmt.Sprintf("%s=%d", name, t.total[i]))
	}
	for _, kind := range kinds {
		kindParts = append(kindParts, fmt.Sprintf("%s=%d", kind, per[kind]))
	}
	return strings.Join(parts, " ") + " (" + strings.Join(kindParts, " ") + ")"
}

func printList(t *tree, out io.Writer) {
	paths := make([]string, 0, len(t.files))
	for p := range t.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	say(out, "files  lines embedded invokes places path\n")
	for _, p := range paths {
		c := t.files[p]
		say(out, "%5d %6d %8d %7d %6d %s\n", c[pyFiles], c[pyFileLines], c[embeddedLines], c[invokeLines], c[sovereignPlaces], p)
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

// ---- the base commit

type object struct {
	kind string
	body []byte
	ok   bool
}

// catObjects reads objects of the repository at root, in order; ok is false for one that does not
// exist.
func catObjects(root string, names []string) ([]object, error) {
	if len(names) == 0 {
		return nil, nil
	}
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(strings.Join(names, "\n") + "\n")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file in %s: %v %s", root, err, strings.TrimSpace(stderr.String()))
	}
	out := make([]object, len(names))
	for i := range names {
		nl := bytes.IndexByte(b, '\n')
		if nl < 0 {
			return nil, fmt.Errorf("git cat-file gave no answer for %s", names[i])
		}
		header := string(b[:nl])
		b = b[nl+1:]
		if strings.HasSuffix(header, " missing") || strings.HasSuffix(header, " ambiguous") {
			continue
		}
		fields := strings.Fields(header)
		size := -1
		if len(fields) == 3 {
			size, _ = strconv.Atoi(fields[2])
		}
		if size < 0 || len(b) < size+1 {
			return nil, fmt.Errorf("git cat-file: unexpected answer %q for %s", header, names[i])
		}
		out[i] = object{fields[1], b[:size], true}
		b = b[size+1:]
	}
	return out, nil
}

// lowerToBase checks the base commit and lowers each total of limit to the baseline file at the
// base commit, when that is lower. inForce is true when the baseline file exists at the base
// commit and can be read: the guard held the base commit.
func lowerToBase(root, base string, limit *counts) (inForce bool, notes []string, err error) {
	objs, err := catObjects(root, []string{base + "^{commit}", base + ":" + baselinePath})
	if err != nil {
		return false, nil, err
	}
	if !objs[0].ok || objs[0].kind != "commit" {
		return false, nil, fmt.Errorf("the base %s is not a commit of this checkout", base)
	}
	if !objs[1].ok || objs[1].kind != "blob" {
		return false, []string{fmt.Sprintf("%s does not exist at %s; the baseline file alone is used", baselinePath, base)}, nil
	}
	old, err := parseBaseline(objs[1].body, baselinePath+" at "+base)
	if err != nil {
		return false, []string{fmt.Sprintf("%v; the baseline file alone is used", err)}, nil
	}
	for i := range limit {
		if limit[i] > old[i] {
			notes = append(notes, fmt.Sprintf("the baseline file raises %s from %d at %s to %d; %d is used", totalNames[i], old[i], base, limit[i], old[i]))
			limit[i] = old[i]
		}
	}
	return true, notes, nil
}

// againstBase compares the tree with base. It returns the totals of the base commit: the totals of
// the tree less what each file of the change added (a deleted file adds a negative share). The
// change is every file that differs from base in the work tree, the new files Git does not
// ignore, and every file whose scope differs because a Chart.yaml came or went. It also returns,
// per total, the files whose share of that total grew; a renamed or moved file is compared with
// its old path.
func againstBase(root, base string, t *tree) (counts, [nTotals][]string, error) {
	baseTotal := t.total
	var grown [nTotals][]string
	diff, err := gitOut(root, "diff", "-z", "-M", "--name-status", base, "--")
	if err != nil {
		return baseTotal, grown, err
	}
	untracked, err := gitOut(root, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return baseTotal, grown, err
	}
	listing, err := gitOut(root, "ls-tree", "-r", "-z", base)
	if err != nil {
		return baseTotal, grown, err
	}
	// The files of base: "<mode> <type> <object>\t<path>". Only a regular file was measured.
	var basePaths []string
	regular := map[string]bool{}
	for _, e := range splitZ(listing) {
		tab := strings.IndexByte(e, '\t')
		if tab < 0 {
			return baseTotal, grown, fmt.Errorf("git ls-tree %s: unexpected entry %q", base, e)
		}
		p := e[tab+1:]
		basePaths = append(basePaths, p)
		if mode := strings.Fields(e[:tab]); len(mode) > 0 && (mode[0] == "100644" || mode[0] == "100755") {
			regular[p] = true
		}
	}
	charts := chartsOf(basePaths)
	type change struct{ now, was string } // now is "" for a deleted file, was is "" for a new file
	var changes []change
	f := splitZ(diff)
	for i := 0; i+1 < len(f); {
		switch f[i][0] {
		case 'R':
			if i+2 < len(f) {
				changes = append(changes, change{f[i+2], f[i+1]})
			}
			i += 3
		case 'C':
			if i+2 < len(f) {
				changes = append(changes, change{f[i+2], ""}) // the source of a copy stays
			}
			i += 3
		case 'A':
			changes = append(changes, change{f[i+1], ""})
			i += 2
		case 'D':
			changes = append(changes, change{"", f[i+1]})
			i += 2
		default:
			changes = append(changes, change{f[i+1], f[i+1]})
			i += 2
		}
	}
	for _, p := range splitZ(untracked) {
		changes = append(changes, change{p, ""})
	}
	inChange := map[string]bool{}
	for _, c := range changes {
		inChange[c.now], inChange[c.was] = true, true
	}
	for _, p := range basePaths {
		if !inChange[p] && regular[p] && scopeOf(p, charts) != scopeOf(p, t.charts) {
			changes = append(changes, change{p, p})
		}
	}
	var objNames []string
	for _, c := range changes {
		if c.was != "" {
			objNames = append(objNames, base+":"+c.was)
		}
	}
	objs, err := catObjects(root, objNames)
	if err != nil {
		return baseTotal, grown, err
	}
	next := 0
	for _, c := range changes {
		var was, now counts
		if c.was != "" {
			if o := objs[next]; o.ok && o.kind == "blob" && regular[c.was] {
				was, _ = measureFile(c.was, scopeOf(c.was, charts), o.body)
			}
			next++
		}
		if c.now != "" {
			now = t.files[c.now]
		}
		name := c.now
		if c.was != "" && c.was != c.now {
			name += " (was " + c.was + ")"
		}
		for i := range now {
			baseTotal[i] -= now[i] - was[i]
			if now[i] > was[i] {
				grown[i] = append(grown[i], fmt.Sprintf("%s grew from %d to %d", name, was[i], now[i]))
			}
		}
	}
	for i := range grown {
		sort.Strings(grown[i])
	}
	return baseTotal, grown, nil
}
