package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	head   = `{"schema": "openova.io/guard-baseline/v1", "allow": [], "counts": {"compliance_live_reads": `
	list   = "\tlist, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})"
	absent = "\x00absent"
)

// baseline is a valid baseline that allows the given entries ("<path>": <n>, ...).
func baseline(entries string) string { return head + "{" + entries + "}}}" }

// TestRules runs one case per rule in a throw-away tree that holds
// products/catalyst/bootstrap/api/internal and, unless the case writes it, the
// baseline that allows zero everywhere. A file whose content is absent is removed.
func TestRules(t *testing.T) {
	model := Package + "/model.go"
	adapter := Package + "/adapters/vuln.go"
	cases := []struct {
		name  string
		want  int
		text  string // the output holds this text
		files map[string]string
	}{
		{"an exact baseline passes and prints the total", 0, "compliance_live_reads=2",
			map[string]string{Runtime: list + "\n" + list, Baseline: baseline(`"` + Runtime + `": 2`)}},
		{"no read and an empty metric", 0, "compliance_live_reads=0",
			map[string]string{Runtime: "package handler"}},
		{"one List more than the baseline allows", 1, "FAIL, " + Runtime + " reads the cluster directly on 2 lines, the baseline allows 1",
			map[string]string{Runtime: list + "\n" + list, Baseline: baseline(`"` + Runtime + `": 1`)}},
		{"a Get of one object counts", 1, "the baseline allows 0",
			map[string]string{Runtime: "\titem, err := dyn.Resource(ClusterPolicyGVR()).Get(r.Context(), name, metav1.GetOptions{})"}},
		{"a Watch counts", 1, "the baseline allows 0",
			map[string]string{Runtime: "\tw, err := dyn.Resource(g).Watch(ctx, opts)"}},
		{"a read in the compliance package counts", 1, "FAIL, " + model + " reads the cluster directly",
			map[string]string{model: list}},
		{"a read in a sub-folder of the compliance package counts", 1, "FAIL, " + adapter + " reads the cluster directly",
			map[string]string{adapter: list}},
		{"a client call in the compliance package counts", 1, "FAIL, " + model + " reads the cluster directly on 1 lines",
			map[string]string{model: "\tdyn, err := f.DynamicClientFor(cluster)"}},
		{"CoreClient in the compliance package counts", 1, "FAIL, " + model,
			map[string]string{model: "\tcs := f.CoreClient(cluster)"}},
		{"RestConfigFor in the compliance package counts", 1, "FAIL, " + model,
			map[string]string{model: "\trc, err := f.RestConfigFor(cluster)"}},
		{"a client call in compliance_runtime.go is not a read by itself", 0, "compliance_live_reads=0",
			map[string]string{Runtime: "\tdyn, err := h.k8sCache.DynamicClientFor(clusterID)"}},
		{"a comment line never counts", 0, "compliance_live_reads=0",
			map[string]string{Runtime: "\t// " + list, model: "  // dyn := f.DynamicClientFor(c)"}},
		{"a test file never counts", 0, "compliance_live_reads=0",
			map[string]string{Package + "/model_test.go": list}},
		{"two reads on one line count once", 0, "compliance_live_reads=1",
			map[string]string{Runtime: list + " " + list, Baseline: baseline(`"` + Runtime + `": 1`)}},
		{"a read in another handler file does not count", 0, "compliance_live_reads=0",
			map[string]string{Internal + "/handler/other.go": list}},
		{"a read that left without a lower baseline", 1, "the baseline still says 2; lower the baseline in the same change",
			map[string]string{Runtime: list, Baseline: baseline(`"` + Runtime + `": 2`)}},
		{"a deleted file that is still in the baseline", 1, "reads the cluster directly on 0 lines, the baseline still says 6",
			map[string]string{Baseline: baseline(`"` + Runtime + `": 6`)}},
		{"no internal folder: nothing can be judged", 1, "nothing can be judged",
			map[string]string{Internal: absent}},
		{"a missing baseline", 1, Baseline + " is missing",
			map[string]string{Baseline: absent}},
		{"a baseline that is not JSON", 1, "cannot be read",
			map[string]string{Baseline: "counts: none"}},
		{"a baseline with text after its JSON value", 1, "cannot be read",
			map[string]string{Baseline: baseline("") + " {}"}},
		{"a baseline of another schema", 1, "has the schema",
			map[string]string{Baseline: `{"schema": "other", "allow": [], "counts": {"compliance_live_reads": {}}}`}},
		{"a baseline with an unknown key", 1, "exactly the keys",
			map[string]string{Baseline: head + `{}}, "extra": 1}`}},
		{"a baseline with an allow entry", 1, "has allow entries",
			map[string]string{Baseline: `{"schema": "openova.io/guard-baseline/v1", "allow": [{"metric": "compliance_live_reads", "path": "x", "count": 1, "reason": "kept for a while", "owner": "CMP"}], "counts": {"compliance_live_reads": {}}}`}},
		{"a baseline with another metric", 1, "exactly the metric",
			map[string]string{Baseline: `{"schema": "openova.io/guard-baseline/v1", "allow": [], "counts": {"compliance_live_reads": {}, "other": {}}}`}},
		{"a baseline entry of 0", 1, "every entry is a whole number of 1 or more",
			map[string]string{Baseline: baseline(`"` + Runtime + `": 0`)}},
		{"a baseline entry that is text", 1, "every entry is a whole number of 1 or more",
			map[string]string{Runtime: list, Baseline: baseline(`"` + Runtime + `": "1"`)}},
		{"a baseline entry that is a fraction", 1, "every entry is a whole number of 1 or more",
			map[string]string{Runtime: list, Baseline: baseline(`"` + Runtime + `": 1.0`)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, Internal+"/.keep", "")
			write(t, root, Baseline, baseline(""))
			for p, text := range c.files {
				if text == absent {
					if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(p))); err != nil {
						t.Fatal(err)
					}
					continue
				}
				write(t, root, p, text+"\n")
			}
			var out, errb bytes.Buffer
			got := run([]string{"--root", root}, &out, &errb)
			if got != c.want || !strings.Contains(out.String(), c.text) {
				t.Errorf("exit code %d (want %d), output must hold %q:\n%s%s", got, c.want, c.text, out.String(), errb.String())
			}
		})
	}
}

// TestTotalIsTheLastLine: every run that can read the tree ends with the total, a
// finding or not; a run that cannot read the tree prints no total.
func TestTotalIsTheLastLine(t *testing.T) {
	root := t.TempDir()
	write(t, root, Runtime, list+"\n"+list+"\n")
	write(t, root, Baseline, "not json")
	var out bytes.Buffer
	if got := run([]string{"--root", root}, &out, &out); got != 1 {
		t.Fatalf("exit code %d, want 1", got)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if lines[len(lines)-1] != "compliance_live_reads=2" {
		t.Errorf("last line %q, want compliance_live_reads=2:\n%s", lines[len(lines)-1], out.String())
	}
	out.Reset()
	if got := run([]string{"--root", t.TempDir()}, &out, &out); got != 1 || strings.Contains(out.String(), "compliance_live_reads=") {
		t.Errorf("an empty tree: exit code %d, want 1 and no total:\n%s", got, out.String())
	}
}

// TestUsage: an argument the command does not know is wrong usage.
func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"--self-test"}, {"extra"}} {
		var out bytes.Buffer
		if got := run(args, &out, &out); got != 2 {
			t.Errorf("%v: exit code %d, want 2", args, got)
		}
	}
}

func write(t *testing.T, root, p, text string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
