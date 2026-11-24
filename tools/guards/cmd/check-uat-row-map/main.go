// Command check-uat-row-map holds the row map of the console regression list
// (FND-161): every row of docs/ledger/UAT.md has exactly one disposition in
// docs/ledger/uat-row-map.csv, and every disposition names something that
// exists.
//
// The map holds one line per ledger row, the frozen denominator of 286 rows:
//
//	<row id>,<disposition>[,<test file>]
//
//	test:<check id>     a browser test of the harness asserts the row; the
//	                    check's implementation is a folder of specifications
//	                    under products/catalyst/bootstrap/ui/e2e/gate/ or
//	                    core/marketplace/e2e/gate/
//	covered:<check id>  a gate check of any area already asserts the row
//	retire:ADR-<nnnn>   a decision record under docs/adr/ retires the row
//
// The test file is a path from the repository root; only a test: line has
// one. A line that starts with # is a comment; a blank line is refused, so that
// the number of lines that are not comments is the number of rows. A package
// that converts rows (FND-162 to FND-165) deletes them from UAT.md and keeps
// their lines here: the map, not UAT.md, carries the denominator.
//
// Rules, each a finding (exit 1):
//
//	form      a line that is not <row>,<disposition>[,<file>], a blank line,
//	          a row id that is not of the ledger's form (1, R1, W1, M1, G1),
//	          or a disposition of another kind
//	twice     two lines for one row
//	missing   a row of UAT.md that has no line
//	count     a map that does not hold 286 lines
//	check     a test: or covered: line whose check is not in
//	          docs/ledger/gates.yaml, or a test: line whose check has an
//	          implementation that is not a folder of the browser harness
//	record    a retire: line whose decision record is not a file
//	          docs/adr/<nnnn>-*.md
//	file      a test file on a line that is not test:, or one that is not a
//	          .spec.ts file of the tree under the check's implementation folder
//	          (under a harness root while the check has none)
//	title     a test file that holds no test whose title starts with the row id
//	          and a space: test('<row id> <clause>', ...); test.skip, test.fixme
//	          and test.fail do not count
//	untested  a test: line without a test file whose check is not not-built,
//	          or whose row is no longer in UAT.md
//
// Usage, from tools/guards:
//
//	go run ./cmd/check-uat-row-map              exit 1 on a finding, 2 when it cannot read
//	go run ./cmd/check-uat-row-map --self-test  every rule fails on a tree built here; a clean tree passes
//	go run ./cmd/check-uat-row-map --root <dir> another tree than the repository of the working folder
package main

import (
	"bufio"
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
	"strings"
)

const (
	me          = "check-uat-row-map"
	mapFile     = "docs/ledger/uat-row-map.csv"
	ledgerFile  = "docs/ledger/UAT.md"
	gatesFile   = "docs/ledger/gates.yaml"
	adrDir      = "docs/adr"
	denominator = 286
)

// The folders of the browser harness, as rule 3 of scripts/acceptance-gate.py names them.
var browserRoots = []string{"products/catalyst/bootstrap/ui/e2e/gate/", "core/marketplace/e2e/gate/"}

var (
	ledgerRow   = regexp.MustCompile(`<tr id="row-([^"]+)"`)
	rowID       = regexp.MustCompile(`^[RWMG]?[0-9]+$`)
	checkID     = regexp.MustCompile(`^[A-Z]{3}-G[0-9]{2}$`)
	adrID       = regexp.MustCompile(`^ADR-([0-9]{4})$`)
	gateEntry   = regexp.MustCompile(`^- id: *(\S+)\s*$`)
	gateState   = regexp.MustCompile(`^  state: *(\S+)\s*$`)
	gateImpl    = regexp.MustCompile(`^  implementation: *(.+?)\s*$`)
	gateTopKey  = regexp.MustCompile(`^[a-z_]+:`)
	testOpening = `(?:^|[^\w.$])test\(\s*['"` + "`" + `]`
)

type finding struct {
	rule, where, msg string
}

func (f finding) String() string { return fmt.Sprintf("%s: %s: %s", f.rule, f.where, f.msg) }

type check struct {
	state, impl string
}

type line struct {
	n                    int
	row, kind, ref, file string
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(me, flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "the tree to read (default: the repository of the working folder)")
	selfTest := flags.Bool("self-test", false, "show that every rule fails on a tree built here, and that a clean tree passes")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *selfTest {
		return runSelfTest(stdout, stderr)
	}
	if *root == "" {
		top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			say(stderr, "%s: cannot find the repository root: %v\n", me, err)
			return 2
		}
		*root = strings.TrimSpace(string(top))
	}
	found, summary, err := checkTree(*root)
	if err != nil {
		say(stderr, "%s: %v\n", me, err)
		return 2
	}
	for _, f := range found {
		say(stdout, "%s: %s\n", me, f)
	}
	if len(found) > 0 {
		say(stdout, "%s: FAILED (%d findings)\n", me, len(found))
		return 1
	}
	say(stdout, "%s: OK (%s)\n", me, summary)
	return 0
}

// checkTree reads the map, the ledger and the manifest of the tree at root and
// returns the findings and, when there are none to report, a summary.
func checkTree(root string) ([]finding, string, error) { return checkTreeWith(root, denominator) }

// checkTreeWith is checkTree with the denominator as an argument, for the self-test.
func checkTreeWith(root string, rows int) ([]finding, string, error) {
	tree := os.DirFS(root)
	mapText, err := fs.ReadFile(tree, mapFile)
	if err != nil {
		return nil, "", err
	}
	ledger, err := fs.ReadFile(tree, ledgerFile)
	if err != nil {
		return nil, "", err
	}
	checks, err := readGates(tree)
	if err != nil {
		return nil, "", err
	}
	inLedger := map[string]bool{}
	var ledgerOrder []string
	for _, m := range ledgerRow.FindAllStringSubmatch(string(ledger), -1) {
		if !inLedger[m[1]] {
			ledgerOrder = append(ledgerOrder, m[1])
		}
		inLedger[m[1]] = true
	}

	var found []finding
	add := func(rule string, n int, format string, a ...any) {
		where := mapFile
		if n > 0 {
			where = fmt.Sprintf("%s:%d", mapFile, n)
		}
		found = append(found, finding{rule, where, fmt.Sprintf(format, a...)})
	}

	var lines []line
	sc := bufio.NewScanner(strings.NewReader(string(mapText)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(text, "#") {
			continue
		}
		if strings.TrimSpace(text) == "" {
			add("form", n, "a blank line (every line that is not a comment is one row)")
			continue
		}
		parts := strings.Split(text, ",")
		if len(parts) < 2 || len(parts) > 3 {
			add("form", n, "%q is not <row>,<disposition>[,<test file>]", text)
			continue
		}
		l := line{n: n, row: strings.TrimSpace(parts[0])}
		if !rowID.MatchString(l.row) {
			add("form", n, "%q is not a row id of the ledger (1, R1, W1, M1, G1)", l.row)
			continue
		}
		kind, ref, ok := strings.Cut(strings.TrimSpace(parts[1]), ":")
		l.kind, l.ref = kind, ref
		switch {
		case !ok:
			add("form", n, "row %s: %q is not test:<check>, covered:<check> or retire:ADR-<nnnn>", l.row, parts[1])
			continue
		case (kind == "test" || kind == "covered") && !checkID.MatchString(ref):
			add("form", n, "row %s: %q is not a check id", l.row, ref)
			continue
		case kind == "retire" && !adrID.MatchString(ref):
			add("form", n, "row %s: %q is not a decision record ADR-<nnnn>", l.row, ref)
			continue
		case kind != "test" && kind != "covered" && kind != "retire":
			add("form", n, "row %s: the disposition %q is not test, covered or retire", l.row, kind)
			continue
		}
		if len(parts) == 3 {
			l.file = strings.TrimSpace(parts[2])
		}
		lines = append(lines, l)
	}

	seen := map[string]int{}
	for _, l := range lines {
		if first, dup := seen[l.row]; dup {
			add("twice", l.n, "row %s has a line already, line %d", l.row, first)
			continue
		}
		seen[l.row] = l.n
	}
	for _, r := range ledgerOrder {
		if _, ok := seen[r]; !ok {
			add("missing", 0, "row %s of %s has no line", r, ledgerFile)
		}
	}
	if n := countRows(string(mapText)); n != rows {
		add("count", 0, "the map holds %d rows; the frozen denominator is %d", n, rows)
	}

	counts := map[string]int{}
	for _, l := range lines {
		counts[l.kind]++
		c, known := checks[l.ref]
		switch l.kind {
		case "test", "covered":
			if !known {
				add("check", l.n, "row %s: %s is not a check of %s", l.row, l.ref, gatesFile)
				continue
			}
			if l.kind == "test" && c.impl != "" && !underRoots(c.impl+"/") {
				add("check", l.n, "row %s: the implementation of %s, %s, is not a folder of the browser harness", l.row, l.ref, c.impl)
				continue
			}
		case "retire":
			m := adrID.FindStringSubmatch(l.ref)
			hits, _ := fs.Glob(tree, adrDir+"/"+m[1]+"-*.md")
			if len(hits) == 0 {
				add("record", l.n, "row %s: %s is not a decision record of %s", l.row, l.ref, adrDir)
			}
		}
		if l.file != "" && l.kind != "test" {
			add("file", l.n, "row %s: only a test: line names a test file", l.row)
			continue
		}
		if l.kind != "test" {
			continue
		}
		if l.file == "" {
			if c.state != "not-built" {
				add("untested", l.n, "row %s: %s is %s, so the line names the test file of the row", l.row, l.ref, c.state)
			} else if !inLedger[l.row] {
				add("untested", l.n, "row %s left %s, but its line names no test file", l.row, ledgerFile)
			}
			continue
		}
		if problem := testFileProblem(tree, l, c); problem != "" {
			add("file", l.n, "row %s: %s", l.row, problem)
			continue
		}
		body, err := fs.ReadFile(tree, l.file)
		if err != nil {
			add("file", l.n, "row %s: %v", l.row, err)
			continue
		}
		if !regexp.MustCompile(testOpening + regexp.QuoteMeta(l.row) + ` `).Match(body) {
			add("title", l.n, "row %s: %s has no test whose title starts with %q", l.row, l.file, l.row+" ")
		}
	}
	summary := fmt.Sprintf("%d rows: test %d, covered %d, retire %d", len(lines), counts["test"], counts["covered"], counts["retire"])
	return found, summary, nil
}

// countRows is the number of lines that are not comments, as the acceptance
// command of FND-161 counts them (grep -vc '^#').
func countRows(text string) int {
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return 0
	}
	n := 0
	for _, t := range strings.Split(text, "\n") {
		if !strings.HasPrefix(t, "#") {
			n++
		}
	}
	return n
}

func underRoots(p string) bool {
	for _, r := range browserRoots {
		if strings.HasPrefix(p, r) && p != r {
			return true
		}
	}
	return false
}

func testFileProblem(tree fs.FS, l line, c check) string {
	clean := path.Clean(l.file)
	if clean != l.file || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return fmt.Sprintf("%s is not a clean path from the repository root", l.file)
	}
	if !strings.HasSuffix(clean, ".spec.ts") {
		return fmt.Sprintf("%s is not a .spec.ts file", l.file)
	}
	if c.impl != "" {
		if !strings.HasPrefix(clean, strings.TrimSuffix(c.impl, "/")+"/") {
			return fmt.Sprintf("%s is not under %s, the implementation of %s", l.file, c.impl, l.ref)
		}
	} else if !underRoots(clean) {
		return fmt.Sprintf("%s is not under a folder of the browser harness (%s)", l.file, strings.Join(browserRoots, ", "))
	}
	st, err := fs.Stat(tree, clean)
	if err != nil || !st.Mode().IsRegular() {
		return fmt.Sprintf("%s is not a file of the tree", l.file)
	}
	return ""
}

// readGates reads the id, state and implementation of every check of the
// manifest. The manifest is written by one tool in one layout (an entry starts
// with "- id:" in the first column under the top-level key checks); the
// standard library has no YAML reader.
func readGates(tree fs.FS) (map[string]check, error) {
	text, err := fs.ReadFile(tree, gatesFile)
	if err != nil {
		return nil, err
	}
	checks := map[string]check{}
	inChecks, id := false, ""
	for _, t := range strings.Split(string(text), "\n") {
		if gateTopKey.MatchString(t) {
			inChecks, id = strings.HasPrefix(t, "checks:"), ""
			continue
		}
		if !inChecks {
			continue
		}
		if m := gateEntry.FindStringSubmatch(t); m != nil {
			id = m[1]
			checks[id] = check{}
			continue
		}
		if id == "" {
			continue
		}
		c := checks[id]
		if m := gateState.FindStringSubmatch(t); m != nil {
			c.state = m[1]
		}
		if m := gateImpl.FindStringSubmatch(t); m != nil {
			c.impl = strings.Trim(m[1], `'"`)
		}
		checks[id] = c
	}
	if len(checks) == 0 {
		return nil, errors.New(gatesFile + " holds no check")
	}
	return checks, nil
}

// ---------------------------------------------------------------- self-test

type selfCase struct {
	name  string
	rule  string // the rule that must fail; "" for a tree that must pass
	edits map[string]string
}

// baseTree is a clean tree: four ledger rows, one map line per row of each
// disposition, a manifest with a not-built and a required browser check.
func baseTree() map[string]string {
	ledger := `<tr id="row-R1"><td>a</td></tr>` + "\n" + `<tr id="row-2"><td>b</td></tr>` + "\n" +
		`<tr id="row-W3"><td>c</td></tr>` + "\n" + `<tr id="row-G4"><td>d</td></tr>` + "\n"
	return map[string]string{
		ledgerFile: ledger,
		gatesFile: "schema: openova.io/gates/v1\nretired_ids: []\nchecks:\n" +
			"- id: FND-G20\n  title: t\n  state: required\n  implementation: products/catalyst/bootstrap/ui/e2e/gate/applications\n" +
			"- id: FND-G21\n  title: t\n  state: not-built\n" +
			"- id: FND-G11\n  title: t\n  state: required\n  implementation: tests/acceptance/sovereign/internal/suite/journeys.go\n",
		"docs/adr/0022-foundation-gate.md":                                  "# ADR\n",
		"products/catalyst/bootstrap/ui/e2e/gate/applications/grid.spec.ts": "test('R1 the grid renders', async () => {})\n",
		mapFile: "# row,disposition[,test file]\n" +
			"R1,test:FND-G20,products/catalyst/bootstrap/ui/e2e/gate/applications/grid.spec.ts\n" +
			"2,test:FND-G21\nW3,covered:FND-G11\nG4,retire:ADR-0022\n",
	}
}

func selfCases() []selfCase {
	m := func(body string) map[string]string { return map[string]string{mapFile: "# head\n" + body} }
	const r1 = "R1,test:FND-G20,products/catalyst/bootstrap/ui/e2e/gate/applications/grid.spec.ts\n"
	const rest = "2,test:FND-G21\nW3,covered:FND-G11\nG4,retire:ADR-0022\n"
	return []selfCase{
		{"a clean tree passes", "", nil},
		{"a row converted out of the ledger keeps its line and passes", "", map[string]string{
			ledgerFile: `<tr id="row-2"><td>b</td></tr>` + "\n" + `<tr id="row-W3"><td>c</td></tr>` + "\n" + `<tr id="row-G4"><td>d</td></tr>` + "\n"}},
		{"a blank line", "form", m(r1 + "\n" + rest)},
		{"a line with one field", "form", m(r1 + "2\nW3,covered:FND-G11\nG4,retire:ADR-0022\n")},
		{"a row id of another form", "form", m(r1 + "X2,test:FND-G21\nW3,covered:FND-G11\nG4,retire:ADR-0022\n")},
		{"a disposition of another kind", "form", m(r1 + "2,walk:FND-G21\nW3,covered:FND-G11\nG4,retire:ADR-0022\n")},
		{"a retire that names no decision record", "form", m(r1 + "2,test:FND-G21\nW3,covered:FND-G11\nG4,retire:founder\n")},
		{"two lines for one row", "twice", m(r1 + rest + "2,covered:FND-G11\n")},
		{"a row of the ledger without a line", "missing", m(r1 + "2,test:FND-G21\nW3,covered:FND-G11\n")},
		{"a map that does not hold the denominator", "count", nil},
		{"a check that is not in the manifest", "check", m(r1 + "2,test:FND-G21\nW3,covered:FND-G99\nG4,retire:ADR-0022\n")},
		{"a test: check whose implementation is a Go file", "check", m(r1 + "2,test:FND-G11\nW3,covered:FND-G11\nG4,retire:ADR-0022\n")},
		{"a decision record that is not a file", "record", m(r1 + "2,test:FND-G21\nW3,covered:FND-G11\nG4,retire:ADR-0099\n")},
		{"a test file on a covered: line", "file", m(r1 + "2,test:FND-G21\nW3,covered:FND-G11,products/catalyst/bootstrap/ui/e2e/gate/applications/grid.spec.ts\nG4,retire:ADR-0022\n")},
		{"a test file that does not exist", "file", m("R1,test:FND-G20,products/catalyst/bootstrap/ui/e2e/gate/applications/gone.spec.ts\n" + rest)},
		{"a test file outside the check's folder", "file", map[string]string{
			"products/catalyst/bootstrap/ui/e2e/gate/jobs/grid.spec.ts": "test('R1 the grid renders', async () => {})\n",
			mapFile: "# head\nR1,test:FND-G20,products/catalyst/bootstrap/ui/e2e/gate/jobs/grid.spec.ts\n" + rest}},
		{"a test file of a not-built check outside the harness", "file", map[string]string{
			"products/catalyst/bootstrap/ui/e2e/x.spec.ts": "test('2 the row', async () => {})\n",
			mapFile: "# head\n" + r1 + "2,test:FND-G21,products/catalyst/bootstrap/ui/e2e/x.spec.ts\nW3,covered:FND-G11\nG4,retire:ADR-0022\n"}},
		{"a test file whose tests do not start with the row id", "title", map[string]string{
			"products/catalyst/bootstrap/ui/e2e/gate/applications/grid.spec.ts": "test('R12 another row', async () => {})\n"}},
		{"a skipped test does not count", "title", map[string]string{
			"products/catalyst/bootstrap/ui/e2e/gate/applications/grid.spec.ts": "test.skip('R1 the grid renders', async () => {})\n"}},
		{"a test: line of a required check without a test file", "untested", m("R1,test:FND-G20\n" + rest)},
		{"a row that left the ledger without a test file", "untested", map[string]string{
			ledgerFile: `<tr id="row-R1"><td>a</td></tr>` + "\n" + `<tr id="row-W3"><td>c</td></tr>` + "\n" + `<tr id="row-G4"><td>d</td></tr>` + "\n"}},
	}
}

// runCase builds the tree of one case under dir and checks it. The trees of the
// self-test hold four rows: every case is checked against a denominator of
// four, the case of the rule count against five.
func runCase(dir string, c selfCase) ([]finding, error) {
	files := baseTree()
	for k, v := range c.edits {
		files[k] = v
	}
	for name, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			return nil, err
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			return nil, err
		}
	}
	found, _, err := checkTreeWith(dir, map[bool]int{true: 5, false: 4}[c.rule == "count"])
	return found, err
}

func runSelfTest(stdout, stderr io.Writer) int {
	bad := 0
	for _, c := range selfCases() {
		dir, err := os.MkdirTemp("", me+"-")
		if err != nil {
			say(stderr, "%s: %v\n", me, err)
			return 2
		}
		found, err := runCase(dir, c)
		_ = os.RemoveAll(dir)
		ok, got := judge(c, found, err)
		if !ok {
			bad++
		}
		label := map[bool]string{true: "PASS", false: "FAIL"}[ok]
		want := c.rule
		if want == "" {
			want = "no finding"
		}
		say(stdout, "  %s  %s (%s)\n", label, c.name, want)
		if !ok {
			say(stdout, "        got: %s\n", got)
		}
	}
	if bad > 0 {
		say(stdout, "SELF-TEST FAILED (%d of %d)\n", bad, len(selfCases()))
		return 1
	}
	say(stdout, "SELF-TEST OK (%d cases)\n", len(selfCases()))
	return 0
}

// judge says whether a case came out as it must: a clean tree with no finding,
// any other with a finding of the case's rule (a line that breaks one rule may
// break another as a consequence: a refused line leaves its row missing).
func judge(c selfCase, found []finding, err error) (bool, string) {
	if err != nil {
		return false, err.Error()
	}
	var got []string
	for _, f := range found {
		got = append(got, f.String())
	}
	sort.Strings(got)
	if c.rule == "" {
		return len(found) == 0, strings.Join(got, "; ")
	}
	for _, f := range found {
		if f.rule == c.rule {
			return true, ""
		}
	}
	if len(got) == 0 {
		return false, "no finding"
	}
	return false, strings.Join(got, "; ")
}

// say writes one line of the guard's output; a write that fails has nowhere to be reported.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
