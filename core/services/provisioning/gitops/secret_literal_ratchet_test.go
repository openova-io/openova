package gitops

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestSecretLiteralRatchet counts where the database password that the
// provisioning service generates is written, as a literal, into the files it
// commits to Git. The count may only fall.
//
// The test renders every catalogue Application, one per cart, for every plan,
// with the password argument set to ratchetSentinel. It renders two trees:
//
//   - the per-Organization tree (GeneratePerOrgAppsTree). Its paths start
//     with "vcluster/".
//   - the legacy tree with the two-region database pair switched on
//     (GenerateAllWithAppConfigs with the postgres options
//     active_hot_standby, primary_region and replica_region), for every
//     Application that needs PostgreSQL. Its paths start with
//     "legacy-tree/ratchet/".
//
// For each output file it counts the lines that hold the sentinel and keeps
// the highest count seen over all renders. Files with no such line are not
// listed.
//
// The baseline file holds one line per file, "<path>:<count>", and nothing
// else. The test fails when
//   - a file carries the sentinel and has no line in the baseline;
//   - a file carries it on more lines than its baseline line allows;
//   - a file carries it on fewer lines than its baseline line says, or no
//     longer at all: the baseline must then be lowered in the same change;
//   - a baseline line is malformed or names a path twice.
//
// On failure it prints the baseline that matches the code. When the count
// fell, replace the baseline file with that text. When it rose, change the
// code instead: an Application's credential is not to be written to Git.
func TestSecretLiteralRatchet(t *testing.T) {
	data, err := os.ReadFile("testdata/secret-literal-baseline.txt")
	if err != nil {
		t.Fatalf("secret-literal ratchet: read the baseline: %v", err)
	}
	want := ratchetParseBaseline(t, string(data))
	got := ratchetMeasure(t)

	var problems []string
	for _, path := range ratchetSortedKeys(got) {
		n := got[path]
		w, listed := want[path]
		switch {
		case !listed:
			problems = append(problems, fmt.Sprintf("%s carries the value on %d line(s) and is not in the baseline", path, n))
		case n > w:
			problems = append(problems, fmt.Sprintf("%s rose from %d to %d line(s) that carry the value", path, w, n))
		case n < w:
			problems = append(problems, fmt.Sprintf("%s fell from %d to %d line(s): change its baseline line to %s:%d", path, w, n, path, n))
		}
	}
	for _, path := range ratchetSortedKeys(want) {
		if _, found := got[path]; !found {
			problems = append(problems, fmt.Sprintf("%s no longer carries the value: delete its baseline line %s:%d", path, path, want[path]))
		}
	}
	if len(problems) == 0 {
		return
	}
	lines := make([]string, 0, len(got))
	for _, path := range ratchetSortedKeys(got) {
		lines = append(lines, fmt.Sprintf("%s:%d", path, got[path]))
	}
	t.Errorf("secret-literal ratchet: %d finding(s):\n  %s\nThe baseline that matches the code is:\n%s",
		len(problems), strings.Join(problems, "\n  "), strings.Join(lines, "\n"))
}

// ratchetSentinel stands in for the generated password in every render.
const ratchetSentinel = "SENTINEL-0123456789abcdef0123456789abcdef"

// ratchetPlans are the plan slugs of the Organization kind, and the empty plan.
var ratchetPlans = []string{"", "s", "m", "l", "xl", "flexi"}

// ratchetApps returns every catalogue Application: the Deployment-shaped ones
// of KnownApps and the HelmRelease-shaped ones, sorted.
func ratchetApps() []string {
	seen := map[string]bool{}
	for a := range KnownApps {
		seen[a] = true
	}
	for a := range helmReleaseAppSlugs {
		seen[a] = true
	}
	apps := make([]string, 0, len(seen))
	for a := range seen {
		apps = append(apps, a)
	}
	sort.Strings(apps)
	return apps
}

// ratchetMeasure renders both trees and returns, per output file that holds
// the sentinel, the highest number of lines that hold it.
func ratchetMeasure(t *testing.T) map[string]int {
	t.Helper()
	g := NewManifestGenerator("legacy-tree")
	pair := map[string]map[string]any{
		"postgres": {
			"active_hot_standby": true,
			"primary_region":     "region-a",
			"replica_region":     "region-b",
		},
	}
	got := map[string]int{}
	record := func(files map[string]string) {
		for path, content := range files {
			n := 0
			for _, line := range strings.Split(content, "\n") {
				if strings.Contains(line, ratchetSentinel) {
					n++
				}
			}
			if n > got[path] {
				got[path] = n
			}
		}
	}
	apps := ratchetApps()
	if len(apps) == 0 {
		t.Fatal("secret-literal ratchet: the catalogue is empty, so the renders would prove nothing")
	}
	rendered := 0
	for _, plan := range ratchetPlans {
		for _, app := range apps {
			files, _ := g.GeneratePerOrgAppsTree("ratchet", plan, []string{app}, ratchetSentinel)
			rendered += len(files)
			record(files)
			if GetAppSpec(app).NeedsDB != "postgres" {
				continue
			}
			legacy := g.GenerateAllWithAppConfigs("ratchet", plan, []string{app}, ratchetSentinel, pair)
			rendered += len(legacy)
			record(legacy)
		}
	}
	if rendered == 0 {
		t.Fatal("secret-literal ratchet: no render produced a file, so the count proves nothing")
	}
	return got
}

// ratchetParseBaseline reads lines of the form "<path>:<count>".
func ratchetParseBaseline(t *testing.T, text string) map[string]int {
	t.Helper()
	want := map[string]int{}
	for i, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		cut := strings.LastIndex(line, ":")
		if cut <= 0 {
			t.Fatalf("secret-literal ratchet: baseline line %d %q is not of the form <path>:<count>", i+1, line)
		}
		path := line[:cut]
		n, err := strconv.Atoi(line[cut+1:])
		if err != nil || n < 1 || strings.TrimSpace(path) != path {
			t.Fatalf("secret-literal ratchet: baseline line %d %q is not of the form <path>:<count> with a count of 1 or more", i+1, line)
		}
		if _, dup := want[path]; dup {
			t.Fatalf("secret-literal ratchet: baseline line %d names %s a second time", i+1, path)
		}
		want[path] = n
	}
	return want
}

func ratchetSortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
