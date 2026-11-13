package suite

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// stubAnswer is what a stub script prints and the exit code it ends with.
type stubAnswer struct {
	out  string
	code int
}

// stubScript writes a bash script at <root>/<script> that stands in for a
// script of the repository. It appends its arguments, as one line, to the
// file <root>/<script>.args, and answers by its last argument: answers maps a
// kubeconfig ("a", "b") to the answer. Any other last argument exits 99.
func stubScript(t *testing.T, root, script string, answers map[string]stubAnswer) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(script))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("printf '%s\\n' \"$*\" >> \"$0.args\"\n")
	b.WriteString("last=\"${!#}\"\n")
	b.WriteString("case \"$last\" in\n")
	for kubeconfig, a := range answers {
		b.WriteString(kubeconfig + ") cat <<'STUB'\n" + a.out + "\nSTUB\n  exit " + strconv.Itoa(a.code) + " ;;\n")
	}
	b.WriteString("*) echo \"unexpected arguments: $*\"; exit 99 ;;\nesac\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// stubArgs returns the argument lines that the stub script at path recorded.
func stubArgs(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path + ".args"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

const nodePortClean = "== live Service sweep (current context) ==\nOK: zero live node ports in owned namespaces across 87 Service(s) (#4765),\n    and forbid-nodeport-service is Enforce in this region (#5591)."

func TestNodePortCheck(t *testing.T) {
	if _, ok := registry[nodePortID]; !ok || nodePortID != "FND-G05" {
		t.Fatalf("check %s is not registered", nodePortID)
	}
	clean := stubAnswer{out: nodePortClean}
	for name, c := range map[string]struct {
		b       stubAnswer
		missing bool
		want    string // "" = PASS, else a part of the reason of the FAIL
	}{
		"both regions are clean":            {b: clean},
		"a node port is live in region b":   {b: stubAnswer{"FAIL: a live node port in an OWNED namespace (§854, #4765/#5348):\n  demo/web\tNodePort\t80:31080", 1}, want: "exited 1 for region region-b: demo/web"},
		"region b could not be read":        {b: stubAnswer{"WARN: could not read Services (cluster unreachable / no permission).\n      Reporting NOTHING rather than a false clean.", 0}, want: "exited 0 for region region-b without its verdict line"},
		"the script stops on a setup error": {b: stubAnswer{"Unknown arg: --kubeconfig", 2}, want: "exited 2 for region region-b"},
		"the script is missing":             {missing: true, want: "exited 127 for region region-a"},
	} {
		root := t.TempDir()
		path := filepath.Join(root, nodePortScript)
		if !c.missing {
			path = stubScript(t, root, nodePortScript, map[string]stubAnswer{"a": clean, "b": c.b})
		}
		r := runCheck(t, "none", nodePortCheck{}, &CheckContext{RepoRoot: root})
		got := r.run.Checks[0]
		switch {
		case c.want == "" && got.Status != Pass:
			t.Errorf("%s: want PASS, got %+v", name, got)
		case c.want != "" && (got.Status != Fail || !strings.Contains(got.Reason, c.want)):
			t.Errorf("%s: want FAIL with %q, got %+v", name, c.want, got)
		}
		if c.missing {
			continue
		}
		// The script is run once for each region, with that region's kubeconfig.
		if args := stubArgs(t, path); strings.Join(args, "|") != "--kubeconfig a|--kubeconfig b" {
			t.Errorf("%s: the script was run with %q", name, args)
		}
	}
	// The evidence holds the script, its exit code and the end of its output.
	root := t.TempDir()
	stubScript(t, root, nodePortScript, map[string]stubAnswer{"a": clean, "b": {"line 1\nline 2\nFAIL: one", 1}})
	r := runCheck(t, "none", nodePortCheck{}, &CheckContext{RepoRoot: root})
	last := r.run.Steps[len(r.run.Steps)-1]
	out, _ := last.Evidence["output"].([]string)
	if last.ID != "FND-G05.2" || last.Evidence["script"] != nodePortScript || last.Evidence["exit_code"] != 1 ||
		strings.Join(out, "|") != "line 1|line 2|FAIL: one" {
		t.Errorf("the step of region-b does not hold the evidence: %+v", last)
	}
}

// The check depends on two facts about the scripts of this repository: both
// exist, and the node-port script prints the verdict line the check looks for.
func TestNodePortScriptsOfThisRepository(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "..")
	text, err := os.ReadFile(filepath.Clean(filepath.Join(root, nodePortScript)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), `echo "`+nodePortVerdict) {
		t.Errorf("%s prints no line that starts with %q", nodePortScript, nodePortVerdict)
	}
	if _, err := os.Stat(filepath.Join(root, encryptionScript)); err != nil {
		t.Error(err)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\n\n b \nc\n", 2); strings.Join(got, "|") != " b|c" {
		t.Errorf("lastLines: got %q", got)
	}
	if got := lastLine("\n\n"); got != "(no output)" {
		t.Errorf("lastLine of nothing: got %q", got)
	}
}
