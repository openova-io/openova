// Command payload-sweep runs the size check of every chart before a merge (FND-208):
// scripts/check-helm-release-payload-size.py --all packages and renders each chart at
// its default values, after `helm dependency update` for a chart with a dependencies
// block, and fails when a chart's Helm release Secret would pass the budget or when a
// chart cannot be measured.
//
// Two charts cannot render at their default values: each needs a value that only the
// per-cluster overlay gives. They are named in scripts/baselines/payload-sweep.json,
// each with that value, the reason and the day the list was written, 2026-11-27. The
// check runs on a copy of platform/ and products/ (the files Git lists) without them.
//
//	cd tools/guards && go run ./cmd/payload-sweep --base <commit>
//
// Exit 1, with one line per finding, when
//   - an entry lacks its chart, value or reason, is not a chart folder of the sweep,
//     names a chart twice, or carries another day than 2026-11-27;
//   - an entry is not on the list at the base commit: the list only shrinks (a base
//     that has no list yet holds nothing against it; the day does);
//   - a listed chart renders at its defaults (taken off the list, it is measured), fails
//     without asking for the value its entry names, or is not in the tree;
//   - the check fails on the copy: a chart over the budget, or one that cannot be measured.
//
// Exit 2 when it cannot run: no list, a list or a base it cannot read, no helm or python3,
// nothing to copy. The tests in main_test.go show that each rule can fail.
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
	"path/filepath"
	"regexp"
	"strings"
)

const (
	listPath = "scripts/baselines/payload-sweep.json"
	checker  = "scripts/check-helm-release-payload-size.py"
	written  = "2026-11-27"
)

// reChart is a chart folder the check discovers (discover_charts of the checker).
var reChart = regexp.MustCompile(`^(platform/[^/]+/chart|products/[^/]+/chart|products/[^/]+/charts/[^/]+)$`)

type entry struct {
	Chart  string `json:"chart"`
	Value  string `json:"value"`
	Reason string `json:"reason"`
	Since  string `json:"since"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func run(args []string, out, errw io.Writer) int {
	flags := flag.NewFlagSet("payload-sweep", flag.ContinueOnError)
	flags.SetOutput(errw)
	base := flags.String("base", "", "the commit whose list this one may only shrink (CI: HEAD^1)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		say(errw, "payload-sweep: cannot find the repository root: %v\n", err)
		return 2
	}
	return sweep(strings.TrimSpace(string(top)), *base, out, errw)
}

// readList reads a list; a list without its "charts" key is an error, never an empty list.
func readList(b []byte) ([]entry, error) {
	var l struct {
		Charts *[]entry `json:"charts"`
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return nil, err
	}
	if l.Charts == nil {
		return nil, errors.New(`no "charts" key`)
	}
	return *l.Charts, nil
}

// gitOut runs git in the repository root and returns what it prints.
func gitOut(root string, args ...string) ([]byte, error) {
	return exec.Command("git", append([]string{"-C", root}, args...)...).Output() // #nosec G204 -- git with the guard's own arguments
}

// baseList returns the charts of the list at the commit base, or nil when the file is
// not in that commit yet.
func baseList(root, base string) (map[string]bool, error) {
	if base == "" {
		return nil, errors.New("a base commit is required (--base HEAD^1 in CI)")
	}
	if _, err := gitOut(root, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return nil, fmt.Errorf("%s is not a commit of the repository: %v", base, err)
	}
	names, err := gitOut(root, "ls-tree", "--name-only", base, "--", listPath)
	if err != nil || len(bytes.TrimSpace(names)) == 0 {
		return nil, err
	}
	b, err := gitOut(root, "show", base+":"+listPath)
	if err != nil {
		return nil, err
	}
	entries, err := readList(b)
	if err != nil {
		return nil, fmt.Errorf("%s at %s: %v", listPath, base, err)
	}
	held := map[string]bool{}
	for _, e := range entries {
		held[e.Chart] = true
	}
	return held, nil
}

// copyTree copies the files Git lists under platform/ and products/ (tracked, and new
// files Git does not ignore) into dst and returns how many it copied.
func copyTree(root, dst string) (int, error) {
	ls, err := gitOut(root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "platform", "products")
	if err != nil {
		return 0, err
	}
	tree, n := os.DirFS(root), 0
	for _, rel := range strings.Split(string(ls), "\x00") {
		if rel == "" {
			continue
		}
		b, err := fs.ReadFile(tree, rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue // deleted in the working tree, still in the index
		}
		if err != nil {
			return n, err
		}
		p := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return n, err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func sweep(root, base string, out, errw io.Writer) int {
	for _, tool := range []string{"helm", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			say(errw, "payload-sweep: %s is not on PATH\n", tool)
			return 2
		}
	}
	b, err := fs.ReadFile(os.DirFS(root), listPath)
	if err != nil {
		say(errw, "payload-sweep: %v\n", err)
		return 2
	}
	entries, err := readList(b)
	if err != nil {
		say(errw, "payload-sweep: %s: %v\n", listPath, err)
		return 2
	}
	held, err := baseList(root, base)
	if err != nil {
		say(errw, "payload-sweep: %v\n", err)
		return 2
	}
	copyRoot, err := os.MkdirTemp("", "payload-sweep-")
	if err != nil {
		say(errw, "payload-sweep: %v\n", err)
		return 2
	}
	defer func() { _ = os.RemoveAll(copyRoot) }()
	if n, err := copyTree(root, copyRoot); err != nil || n == 0 {
		say(errw, "payload-sweep: copied %d files of platform/ and products/: %v\n", n, err)
		return 2
	}

	findings := 0
	fail := func(format string, args ...any) {
		findings++
		say(out, "FAIL "+format+"\n", args...)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		switch {
		case !reChart.MatchString(e.Chart) || e.Value == "" || e.Reason == "":
			fail("%s: an entry names a chart folder of the sweep, the value it asks for and the reason (%+v)", listPath, e)
			continue
		case seen[e.Chart]:
			fail("%s: %s is listed twice", listPath, e.Chart)
			continue
		case e.Since != written:
			fail("%s: %s is dated %q: the list was written on %s and only shrinks", listPath, e.Chart, e.Since, written)
		case held != nil && !held[e.Chart]:
			fail("%s is not on the list at %s: the list only shrinks; give the chart a default for %s instead", e.Chart, base, e.Value)
		}
		seen[e.Chart] = true
		dir := filepath.Join(copyRoot, e.Chart)
		if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
			fail("%s is listed but is not a chart of the tree: take it off the list", e.Chart)
			continue
		}
		var stderr bytes.Buffer
		render := exec.Command("helm", "template", "release", dir, "--namespace", "default", "--dependency-update") // #nosec G204 -- a listed chart folder of the copy
		render.Stdout, render.Stderr = io.Discard, &stderr
		switch err := render.Run(); {
		case err == nil:
			fail("%s renders at its defaults: take it off %s, the check then measures it", e.Chart, listPath)
		case !strings.Contains(stderr.String(), e.Value+" is required"):
			fail("%s does not render at its defaults, but it does not ask for %s, the value its entry names: %s", e.Chart, e.Value, strings.Join(strings.Fields(stderr.String()), " "))
		default:
			say(out, "held: %s cannot render at its defaults, %s is required (listed %s)\n", e.Chart, e.Value, e.Since)
		}
		if err := os.RemoveAll(dir); err != nil {
			say(errw, "payload-sweep: %v\n", err)
			return 2
		}
	}

	check := exec.Command("python3", checker, "--all", copyRoot) // #nosec G204 -- the checker of the tree on its own copy
	check.Dir, check.Stdout, check.Stderr = root, out, errw
	if err := check.Run(); err != nil {
		fail("%s --all on the tree without the %d listed chart(s): %v", checker, len(seen), err)
	}
	if findings > 0 {
		say(out, "payload-sweep: %d finding(s)\n", findings)
		return 1
	}
	say(out, "payload-sweep: OK, every chart measured but the %d listed in %s, each still asking for its value\n", len(seen), listPath)
	return 0
}
