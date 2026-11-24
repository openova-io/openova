// Command compliance-live-reads holds that the compliance code of catalyst-api reads
// the cluster directly no more often than it does today.
//
// The compliance routes answer from the informer cache of catalyst-api. A route that
// asks the API server of a cluster on every request can disagree with the cache, is
// slow, and hides a kind the cache does not watch. This guard counts the lines that
// read a cluster directly, and the count of each file may only fall.
//
// Files read: products/catalyst/bootstrap/api/internal/handler/compliance_runtime.go,
// and every file whose name ends in .go and not in _test.go under
// products/catalyst/bootstrap/api/internal/compliance/, sub-folders included. A file
// that does not exist counts zero. If the folder products/catalyst/bootstrap/api/internal
// does not exist, nothing can be judged.
//
// A line counts once, and only when its first characters other than blanks are not //:
//
//	R1  in every file read, when it matches  \.Resource\(.*\.(List|Get|Watch)\(
//	R2  in a file under internal/compliance/, also when it matches
//	    (DynamicClientFor|CoreClient|RestConfigFor)\(
//
// Rule, shown to fail by main_test.go: the count of each file equals its entry under
// the metric compliance_live_reads of scripts/baselines/compliance-live-reads.json; a
// file absent from the baseline is allowed zero. A count above its entry fails; a count
// below its entry fails too, so a change that removes a read lowers the baseline in the
// same change, and a file at zero leaves it. The baseline is
// {"schema": "openova.io/guard-baseline/v1", "counts": {"compliance_live_reads":
// {"<path>": <n>}}, "allow": []}: exactly these three keys, one metric, an empty allow
// list, every entry a whole number of 1 or more.
//
// Every run that can read the tree prints, as its last line, compliance_live_reads=<total>.
//
// Usage (from tools/guards): go run ./cmd/compliance-live-reads [--root <repository root>]
// Exit 0 when the rule holds, 1 on a finding or when the tree or the baseline cannot be
// read, 2 on wrong usage.
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
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	// Metric is the one metric of the baseline, and the name of the total printed last.
	Metric = "compliance_live_reads"
	// Baseline is the repository path of the baseline file.
	Baseline = "scripts/baselines/compliance-live-reads.json"
	// Schema is the schema the baseline carries.
	Schema = "openova.io/guard-baseline/v1"
	// Internal is the folder without which nothing can be judged.
	Internal = "products/catalyst/bootstrap/api/internal"
	// Runtime is the handler file of the compliance routes that reads the cluster.
	Runtime = Internal + "/handler/compliance_runtime.go"
	// Package is the folder of the compliance read model, sub-folders included.
	Package = Internal + "/compliance"

	me = "compliance-live-reads: FAIL, "
)

var (
	directRead = regexp.MustCompile(`\.Resource\(.*\.(List|Get|Watch)\(`)
	clientCall = regexp.MustCompile(`(DynamicClientFor|CoreClient|RestConfigFor)\(`)
)

// count returns the lines of text that read a cluster directly; client says whether a
// client call counts too (R2).
func count(text []byte, client bool) int {
	n := 0
	for _, line := range strings.Split(string(text), "\n") {
		if strings.HasPrefix(strings.TrimLeftFunc(line, unicode.IsSpace), "//") {
			continue
		}
		if directRead.MatchString(line) || (client && clientCall.MatchString(line)) {
			n++
		}
	}
	return n
}

// Counts reads the tree and returns the count of every file read.
func Counts(tree fs.FS) (map[string]int, error) {
	info, err := fs.Stat(tree, Internal)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory; nothing can be judged", Internal)
	}
	have := map[string]int{}
	if b, err := fs.ReadFile(tree, Runtime); err == nil {
		have[Runtime] = count(b, false)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s cannot be read: %v", Runtime, err)
	}
	err = fs.WalkDir(tree, Package, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == Package && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		name := path.Base(p)
		if d.IsDir() || !d.Type().IsRegular() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		b, err := fs.ReadFile(tree, p)
		if err != nil {
			return err
		}
		have[p] = count(b, true)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%s cannot be read: %v", Package, err)
	}
	return have, nil
}

// Allowed reads the baseline of the tree and returns the count it allows each file.
func Allowed(tree fs.FS) (map[string]int, error) {
	b, err := fs.ReadFile(tree, Baseline)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("is missing")
	}
	if err != nil {
		return nil, fmt.Errorf("cannot be read: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("cannot be read: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("cannot be read: text after the JSON value")
	}
	top, ok := doc.(map[string]any)
	if !ok || strings.Join(keys(top), " ") != "allow counts schema" {
		return nil, errors.New("must hold exactly the keys allow, counts and schema")
	}
	if s, ok := top["schema"].(string); !ok || s != Schema {
		got, _ := json.Marshal(top["schema"])
		return nil, fmt.Errorf("has the schema %s, not %s", got, Schema)
	}
	if a, ok := top["allow"].([]any); !ok || len(a) != 0 {
		return nil, errors.New("has allow entries; this guard takes none, allow is the empty list")
	}
	counts, ok := top["counts"].(map[string]any)
	if !ok || strings.Join(keys(counts), " ") != Metric {
		return nil, errors.New("must hold counts for exactly the metric " + Metric)
	}
	entries, ok := counts[Metric].(map[string]any)
	if !ok {
		return nil, errors.New("must map each path to a number under " + Metric)
	}
	allowed := map[string]int{}
	for _, p := range keys(entries) {
		n, ok := wholeNumber(entries[p])
		if !ok || n < 1 {
			got, _ := json.Marshal(entries[p])
			return nil, fmt.Errorf("allows %s for %s; every entry is a whole number of 1 or more, and a file at zero leaves the baseline", got, p)
		}
		allowed[p] = n
	}
	return allowed, nil
}

// wholeNumber accepts a JSON integer literal only: not 1.0, not 1e0, not "1", not true.
func wholeNumber(v any) (int, bool) {
	num, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(num), ".eE") {
		return 0, false
	}
	n, err := num.Int64()
	if err != nil || n > 1<<31 {
		return 0, false
	}
	return int(n), true
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Check judges the tree at root: the findings, the total and an error when the tree
// cannot be read (no total then). A baseline that cannot be read is one finding.
func Check(root string) ([]string, int, error) {
	tree := os.DirFS(root)
	have, err := Counts(tree)
	if err != nil {
		return nil, 0, err
	}
	total := 0
	for _, n := range have {
		total += n
	}
	allowed, err := Allowed(tree)
	if err != nil {
		return []string{Baseline + " " + err.Error()}, total, nil
	}
	union := map[string]bool{}
	for p := range have {
		union[p] = true
	}
	for p := range allowed {
		union[p] = true
	}
	var findings []string
	for _, p := range keys(union) {
		h, a := have[p], allowed[p]
		switch {
		case h > a:
			findings = append(findings, fmt.Sprintf("%s reads the cluster directly on %d lines, the baseline allows %d; read from the informer cache instead", p, h, a))
		case h < a:
			findings = append(findings, fmt.Sprintf("%s reads the cluster directly on %d lines, the baseline still says %d; lower the baseline in the same change", p, h, a))
		}
	}
	return findings, total, nil
}

func repositoryRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("find the repository root with git: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("compliance-live-reads", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "", "repository root (default: the root Git reports)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		say(stderr, "usage: compliance-live-reads [--root <repository root>]\n")
		return 2
	}
	if *root == "" {
		r, err := repositoryRoot()
		if err != nil {
			say(stdout, "%s%v\n", me, err)
			return 1
		}
		*root = r
	}
	findings, total, err := Check(*root)
	if err != nil {
		say(stdout, "%s%v\n", me, err)
		return 1
	}
	for _, f := range findings {
		say(stdout, "%s%s\n", me, f)
	}
	say(stdout, "%s=%d\n", Metric, total)
	if len(findings) > 0 {
		return 1
	}
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
