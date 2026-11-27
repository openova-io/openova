package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Templates of the fixture charts: ok and dep render, fqdn and host stop at their defaults;
// dep depends on ok (file://), so the check measures it only after a dependency update.
var deps = map[string]string{"dep": "dependencies:\n  - name: ok\n    version: 0.1.0\n    repository: file://../../ok/chart\n"}

var templates = map[string]string{
	"ok":   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: ok\n",
	"fqdn": "{{ required \"sovereignFqdn is required\" .Values.sovereignFqdn }}\n",
	"host": "{{ required \"hostName is required\" .Values.hostName }}\n",
	"dep":  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: dep\n",
}

// list writes a list; each pair is a chart name and the value its entry names, dated day.
func list(day string, pairs ...string) string {
	var rows []string
	for i := 0; i < len(pairs); i += 2 {
		rows = append(rows, fmt.Sprintf(`{"chart": "platform/%s/chart", "value": %q, "reason": "a test", "since": %q}`, pairs[i], pairs[i+1], day))
	}
	return `{"charts": [` + strings.Join(rows, ", ") + "]}\n"
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...) // #nosec G204 -- the test's own temporary folder
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
}

// fixture builds a repository with the checker of this tree and the named charts, one
// commit with the list base (none when empty), and the list head in the working tree.
func fixture(t *testing.T, charts []string, base, head string) string {
	t.Helper()
	root := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(top)), checker))
	if err != nil {
		t.Fatal(err)
	}
	put(checker, string(src))
	for _, c := range charts {
		put("platform/"+c+"/chart/Chart.yaml", "apiVersion: v2\nname: "+c+"\nversion: 0.1.0\n"+deps[c])
		put("platform/"+c+"/chart/templates/cm.yaml", templates[c])
	}
	if base != "" {
		put(listPath, base)
	}
	git(t, root, "init", "-q")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "base")
	if err := os.Remove(filepath.Join(root, listPath)); err != nil && base != "" {
		t.Fatal(err)
	}
	if head != "" {
		put(listPath, head)
	}
	return root
}

func TestSweep(t *testing.T) {
	two := []string{"ok", "fqdn"}
	three := []string{"ok", "fqdn", "host"}
	for _, c := range []struct {
		name       string
		charts     []string
		base, head string
		want       int
		says       string
	}{
		{"a listed chart that cannot render is held and every other chart is measured", []string{"ok", "fqdn", "dep"},
			list(written, "fqdn", "sovereignFqdn"), list(written, "fqdn", "sovereignFqdn"), 0, "measured 2 chart(s); 1 of them after a dependency update"},
		{"the list may be new in the change", two, "", list(written, "fqdn", "sovereignFqdn"), 0, "measured 1 chart(s)"},
		{"a chart not on the list that cannot be measured fails", two,
			list(written), list(written), 1, "::error title=Chart could not be measured::platform/fqdn/chart"},
		{"a listed chart that renders again fails", two, list(written, "fqdn", "sovereignFqdn", "ok", "name"),
			list(written, "fqdn", "sovereignFqdn", "ok", "name"), 1, "FAIL platform/ok/chart renders at its defaults"},
		{"a chart that renders again passes once it is taken off", two, list(written, "fqdn", "sovereignFqdn", "ok", "name"),
			list(written, "fqdn", "sovereignFqdn"), 0, "measured 1 chart(s)"},
		{"the list cannot grow", three, list(written, "fqdn", "sovereignFqdn"),
			list(written, "fqdn", "sovereignFqdn", "host", "hostName"), 1, "FAIL platform/host/chart is not on the list at HEAD"},
		{"the same entries pass when the base holds them", three, list(written, "fqdn", "sovereignFqdn", "host", "hostName"),
			list(written, "fqdn", "sovereignFqdn", "host", "hostName"), 0, "measured 1 chart(s)"},
		{"a new list cannot hold an entry of another day", two, "", list("2026-12-01", "fqdn", "sovereignFqdn"), 1, `is dated "2026-12-01"`},
		{"a listed chart that asks for another value fails", two,
			list(written, "fqdn", "hostName"), list(written, "fqdn", "hostName"), 1, "it does not ask for hostName"},
		{"a listed chart not in the tree fails", two, list(written, "fqdn", "sovereignFqdn", "gone", "x"),
			list(written, "fqdn", "sovereignFqdn", "gone", "x"), 1, "FAIL platform/gone/chart is listed but is not a chart of the tree"},
		{"no list is not an empty list", two, list(written), "", 2, listPath},
		{"a list without its key is not an empty list", two, list(written), "{}\n", 2, `no "charts" key`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			got := sweep(fixture(t, c.charts, c.base, c.head), "HEAD", &out, &out)
			if got != c.want || !strings.Contains(out.String(), c.says) {
				t.Fatalf("exit %d, want %d with %q:\n%s", got, c.want, c.says, out.String())
			}
		})
	}
}
