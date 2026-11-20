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

const strict = "#!/usr/bin/env bash\nset -euo pipefail\n"

// Each case is the body of a script that sets pipefail; want is the rule each
// finding breaks, in order.
func TestScan(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		// The shapes that break when the text grows.
		{"awk with exit after echo (the release-writer failure of 19 November)", `b64host=$(echo "$out" | awk '/smtp-host: /{print $2; exit}' | tr -d '"')`, []string{"awk-exit"}},
		{"awk with exit inside a quoted substitution", `route_block="$(echo "$out" | awk '/^kind: HTTPRoute$/{f=1} f && /^---$/{exit} f')"`, []string{"awk-exit"}},
		{"awk with exit after printf on a continuation line", "role=$(printf '%s' \"${seed_out}\" \\\n  | awk '/roles:/{f=1} f&&/name:/{print $2;exit}')", []string{"awk-exit"}},
		{"awk with exit after a line that ends in a pipe", "echo \"$out\" |\n  awk '/x/{exit}'", []string{"awk-exit"}},
		{"head after echo", `echo "$out" | head -3 >&2`, []string{"head"}},
		{"head after printf of a braced variable", `first_map="$(printf '%s\n' "${maps}" | head -1)"`, []string{"head"}},
		{"head after echo of a positional parameter", `echo "$1" | head -n1`, []string{"head"}},
		{"head in a condition", `if echo "$out" | head -1 | grep -q x; then :; fi`, []string{"head"}},
		{"head in a one-line function", `first() { echo "$1" | head -1; }`, []string{"head"}},
		{"head in a case arm", "case \"$x\" in\n  a) echo \"$out\" | head -1 ;;\nesac", []string{"head"}},
		{"head after a here-document whose body has a quote", "cat <<EOF >x\ndon't\nEOF\necho \"$out\" | head -1", []string{"head"}},
		{"sed with a q command after an address", `echo "$out" | sed -n '/kind: Secret/{p;q}'`, []string{"sed-q"}},
		{"sed with a line-number q", `echo "$out" | sed 5q`, []string{"sed-q"}},
		{"two findings on one line", `a=$(echo "$x" | head -1); b=$(echo "$y" | awk '{print; exit}')`, []string{"head", "awk-exit"}},
		// grep that stops at its first match (the umbrella chart's allow-list
		// test, 19 and 20 November 2026).
		{"grep -q after printf (the allow-list test)", `if ! printf '%s' "$_ing14" | grep -q '"cnpg-system"'; then :; fi`, []string{"grep-q"}},
		{"grep -qE after printf of a line", `if printf '%s\n' "$ingress_block" | grep -qE '^\s*-\s*world\s*$'; then :; fi`, []string{"grep-q"}},
		{"grep -Eq, q not first in the cluster", `echo "$out" | grep -Eq 'a|b'`, []string{"grep-q"}},
		{"grep -q after an option with its argument", `echo "$out" | grep -A 3 -q x`, []string{"grep-q"}},
		{"grep -q after the pattern", `echo "$out" | grep x -q`, []string{"grep-q"}},
		{"grep --quiet", `echo "$out" | grep --quiet x`, []string{"grep-q"}},
		{"egrep --silent", `echo "$out" | egrep --silent x`, []string{"grep-q"}},
		{"grep -q and -l: -q wins", `echo "$out" | grep -ql x`, []string{"grep-q"}},
		{"grep -m1", `v=$(printf '%s\n' "$NE_OUT" | grep -m1 '^agents=')`, []string{"grep-m"}},
		{"grep -m with its count as the next word", `echo "$out" | grep -m 2 x`, []string{"grep-m"}},
		{"grep --max-count=1", `echo "$out" | grep --max-count=1 x`, []string{"grep-m"}},
		{"grep --max-count 1", `echo "$out" | grep --max-count 1 x`, []string{"grep-m"}},
		{"grep -l", `echo "$out" | grep -l x`, []string{"grep-l"}},
		{"grep -L", `echo "$out" | grep -L x`, []string{"grep-l"}},
		{"grep -il", `echo "$out" | grep -il x`, []string{"grep-l"}},
		{"grep --files-with-matches", `echo "$out" | grep --files-with-matches x`, []string{"grep-l"}},
		{"fgrep -q in a negated condition", `! echo "$out" | fgrep -q x`, []string{"grep-q"}},

		// The repaired form: the reader takes a here-string.
		{"awk with exit on a here-string", `b64host=$(awk '/smtp-host: /{print $2; exit}' <<<"$out" | tr -d '"')`, nil},
		{"head on a here-string", `first_map="$(head -1 <<<"${maps}")"`, nil},
		{"sed with q on a here-string", `sed -n '/kind: Secret/{p;q}' <<<"$out"`, nil},
		{"grep -q on a here-string", `if ! grep -q '"cnpg-system"' <<<"$_ing14"; then :; fi`, nil},
		{"grep -m1 on a here-string", `v=$(grep -m1 '^agents=' <<<"$NE_OUT")`, nil},
		{"grep -l on a here-string", `grep -l x <<<"$out"`, nil},
		{"grep -q on a here-string of a command", `grep -q x <<<"$(awk '/egress:/,0' "$f")"`, nil},

		// grep that reads to the end.
		{"grep -c", `n=$(printf '%s\n' "$dns_block" | grep -c "protocol: UDP")`, nil},
		{"grep -v", `echo "$out" | grep -v '^#'`, nil},
		{"grep -A, a context", `echo "$out" | grep -A1 x`, nil},
		{"grep to /dev/null", `echo "$out" | grep x >/dev/null`, nil},
		{"-q as the pattern of -e", `echo "$out" | grep -e -q`, nil},
		{"-q as the pattern of a cluster that ends in e", `echo "$out" | grep -Ee -q`, nil},
		{"-q as the pattern of --regexp", `echo "$out" | grep --regexp -q`, nil},
		{"-q after --", `echo "$out" | grep -- -q`, nil},
		{"-l as the argument of -A", `echo "$out" | grep -A1l x`, nil},
		{"-m as the argument of --label", `echo "$out" | grep --label -m x`, nil},
		{"a quoted -q is the pattern after -e", `echo "$out" | grep -e '-q'`, nil},

		// Readers that read to the end.
		{"awk whose only exit is in END", `if echo "$g" | awk 'BEGIN{RS="\n---\n"} /kind: HTTPRoute/{found=1} END{exit !found}'; then :; fi`, nil},
		{"awk without exit", `echo "$out" | awk '/^kind: Deployment$/{f=1} f'`, nil},
		{"sed without q", `echo "$out" | sed 's/^/  /'`, nil},
		{"sed that prints a substitution", `n=$(printf '%s\n' "${OUT}" | sed -n 's/^READ=\([0-9]*\).*/\1/p')`, nil},
		{"sed with q in the text of a substitution", `printf '%s' "${repo}" | sed 's#%2Fq/#/#g'`, nil},

		// Not covered by this guard (see the comment of main.go).
		{"a command as the writer is not judged", `helm template x . | head -1`, nil},
		{"a command as the writer of grep -q is not judged", `if ! awk '/egress:/,0' "$f" | grep -q x; then :; fi`, nil},
		{"a filter between the variable and the reader is not judged", `echo "$out" | grep -A1 X | head -5 >&2`, nil},
		{"a filter between the variable and grep -q is not judged", `printf '%s' "$x" | grep -A1 k | grep -q v`, nil},

		// Not a variable piped into a reader.
		{"a literal", `echo hello | head -1`, nil},
		{"a short constant into grep -q", `printf 'a\nb\n' | grep -q a`, nil},
		{"or, not a pipe", `echo "$out" || head -1`, nil},
		{"a comment", `# echo "$out" | head -1`, nil},
		{"a comment after a command", `x=1 # echo "$out" | head -1`, nil},
		{"a single-quoted string", `echo 'echo "$out" | head -1'`, nil},
		{"the body of a here-document", "cat <<'EOF' >x.sh\necho \"$out\" | head -1\nEOF", nil},
		{"the body of a tab-indented here-document", "cat <<-EOF >x.sh\n\techo \"$out\" | head -1\n\tEOF", nil},
		{"a redirection to stderr is not a separator", `echo "$out" >&2 | true`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, f := range scan(strict + c.text + "\n") {
				got = append(got, f.Rule)
			}
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("rules %q, want %q", got, c.want)
			}
		})
	}
}

// A script without pipefail has no finding: the status of its pipe is the
// status of the last reader.
func TestWithoutPipefail(t *testing.T) {
	if f := scan("#!/usr/bin/env bash\nset -eu\necho \"$out\" | head -1\n"); len(f) != 0 {
		t.Errorf("findings %v, want none", f)
	}
	if f := scan("#!/usr/bin/env bash\n# set -o pipefail\necho \"$out\" | head -1\n"); len(f) != 0 {
		t.Errorf("a commented set line turned pipefail on: %v", f)
	}
	if f := scan("#!/usr/bin/env bash\nset -o errexit -o pipefail\necho \"$out\" | head -1\n"); len(f) != 1 {
		t.Errorf("findings %v, want one", f)
	}
}

func TestScanLineNumbers(t *testing.T) {
	text := strict + "x=1\nr=$(printf '%s' \"${s}\" \\\n  | awk '{print; exit}')\n"
	f := scan(text)
	if len(f) != 1 || f[0].Line != 5 {
		t.Fatalf("findings %+v, want one on line 5 (the line of the reader)", f)
	}
	if !strings.Contains(f[0].Text, `printf '%s' "${s}"`) || !strings.Contains(f[0].Text, "awk '{print; exit}'") {
		t.Errorf("text %q does not name the writer and the reader", f[0].Text)
	}
}

// Each shape this guard fails on breaks in bash once the text passes the pipe
// buffer, with SIGPIPE ignored as on a GitHub runner; the repaired form does
// not. This is the measurement behind the guard, run where the guard runs.
func TestTheShapesBreakInBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	// About 200 KiB, matched on its first line; the shape comes in $SHAPE.
	const script = "set -uo pipefail; trap '' PIPE; big=\"$(seq -f 'line-%06g xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx' 1 4000)\"; eval \"$SHAPE\""
	for _, c := range []struct {
		line  string
		fails bool
	}{
		{`echo "$big" | awk 'NR==1{exit}'`, true},
		{`echo "$big" | head -1 >/dev/null`, true},
		{`echo "$big" | sed -n '1p;q' >/dev/null`, true},
		{`echo "$big" | grep -q line-000001`, true},
		{`echo "$big" | grep -m1 line-000001 | cat >/dev/null`, true},
		{`echo "$big" | grep -l line-000001 | cat >/dev/null`, true},
		{`awk 'NR==1{exit}' <<<"$big"`, false},
		{`head -1 <<<"$big" >/dev/null`, false},
		{`sed -n '1p;q' <<<"$big" >/dev/null`, false},
		{`grep -q line-000001 <<<"$big"`, false},
		{`grep -m1 line-000001 <<<"$big" >/dev/null`, false},
		{`grep -l line-000001 <<<"$big" >/dev/null`, false},
		// grep that reads to the end does not cut its writer off.
		{`echo "$big" | grep -c line-000001 >/dev/null`, false},
	} {
		if got := len(scan(strict+c.line+"\n")) > 0; got != c.fails {
			t.Errorf("guard on %q: finding %v, want %v", c.line, got, c.fails)
		}
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "SHAPE="+c.line)
		err := cmd.Run()
		if (err != nil) != c.fails {
			t.Errorf("bash %q: error %v, want failure %v", c.line, err, c.fails)
		}
	}
}

// A tree with one finding of each rule, the same text out of scope, and a
// clean script: the guard fails naming exactly the three, and passes once
// they are repaired.
func TestRunOnATree(t *testing.T) {
	root := t.TempDir()
	put := func(p, text string) {
		t.Helper()
		full := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	awkShape := strict + "out=\"$(helm template x .)\"\nv=$(echo \"$out\" | awk '/a: /{print $2; exit}')\n"
	headShape := strict + "echo \"$out\" | head -3 >&2\n"
	sedShape := strict + "echo \"$out\" | sed 5q\n"
	grepShape := strict + "if ! printf '%s' \"$out\" | grep -q x; then exit 1; fi\n"
	put("platform/x/chart/tests/render.sh", awkShape)
	put("products/y/chart/tests/render.sh", headShape)
	put("products/z/charts/w/tests/render.sh", sedShape)
	put("platform/g/chart/tests/allow.sh", grepShape)
	put("platform/x/chart/tests/clean.sh", strict+"v=$(awk '/a: /{print $2; exit}' <<<\"$out\")\n")
	put("platform/x/chart/templates/job.yaml", headShape)
	put("platform/x/chart/tests/README.md", headShape)
	put("scripts/render.sh", headShape)
	gitInit(t, root)

	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 1 {
		t.Fatalf("exit %d, want 1\n%s%s", code, out.String(), errw.String())
	}
	for _, want := range []string{
		"FAIL awk-exit platform/x/chart/tests/render.sh:4:",
		"FAIL head products/y/chart/tests/render.sh:3:",
		"FAIL sed-q products/z/charts/w/tests/render.sh:3:",
		"FAIL grep-q platform/g/chart/tests/allow.sh:3:",
		"4 findings in 5 files read",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	for _, unwanted := range []string{"templates/job.yaml", "README.md", "scripts/render.sh", "clean.sh"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("%s was judged:\n%s", unwanted, out.String())
		}
	}

	put("platform/x/chart/tests/render.sh", strict+"out=\"$(helm template x .)\"\nv=$(awk '/a: /{print $2; exit}' <<<\"$out\")\n")
	put("products/y/chart/tests/render.sh", strict+"head -3 <<<\"$out\" >&2\n")
	put("products/z/charts/w/tests/render.sh", strict+"sed 5q <<<\"$out\"\n")
	put("platform/g/chart/tests/allow.sh", strict+"if ! grep -q x <<<\"$out\"; then exit 1; fi\n")
	out.Reset()
	errw.Reset()
	if code := run([]string{"--root", root}, &out, &errw); code != 0 {
		t.Fatalf("exit %d after the repair, want 0\n%s%s", code, out.String(), errw.String())
	}
}

// The baseline holds the findings of one rule in one file that a story in
// progress owns, up to its count, and nothing else.
func TestBaseline(t *testing.T) {
	two := strict + "printf '%s' \"$a\" | grep -q x\nprintf '%s' \"$b\" | grep -q y\n"
	entry := func(path, rule string, count int) string {
		return fmt.Sprintf(`{"path": %q, "rule": %q, "count": %d, "owner": "S-1", "reason": "the story S-1 owns the file"}`, path, rule, count)
	}
	for _, c := range []struct {
		name     string
		baseline string
		code     int
		want     []string
	}{
		{"no baseline: both fail", "", 1, []string{"FAIL grep-q platform/a/chart/tests/x.sh:3:", "FAIL grep-q platform/a/chart/tests/x.sh:4:", "2 findings"}},
		{"both held", `{"allow": [` + entry("platform/a/chart/tests/x.sh", "grep-q", 2) + `]}`, 0, []string{"HELD grep-q platform/a/chart/tests/x.sh:3:", "(owner S-1)", "2 held by"}},
		{"one more than allowed: both fail", `{"allow": [` + entry("platform/a/chart/tests/x.sh", "grep-q", 1) + `]}`, 1, []string{"FAIL grep-q platform/a/chart/tests/x.sh:4:", "allows 1 grep-q findings in platform/a/chart/tests/x.sh (owner S-1); there are 2"}},
		{"fewer than allowed: passes and says so", `{"allow": [` + entry("platform/a/chart/tests/x.sh", "grep-q", 3) + `]}`, 0, []string{"the baseline can be lowered", "allows 3 grep-q findings in platform/a/chart/tests/x.sh, there are 2"}},
		{"another rule holds nothing", `{"allow": [` + entry("platform/a/chart/tests/x.sh", "head", 2) + `]}`, 1, []string{"FAIL grep-q platform/a/chart/tests/x.sh:3:", "allows 2 head findings in platform/a/chart/tests/x.sh, there are 0"}},
		{"another file holds nothing", `{"allow": [` + entry("platform/b/chart/tests/x.sh", "grep-q", 2) + `]}`, 1, []string{"FAIL grep-q platform/a/chart/tests/x.sh:3:"}},
		{"an unknown field", `{"allow": [], "extra": 1}`, 2, nil},
		{"a path that is not a chart test", `{"allow": [` + entry("scripts/x.sh", "grep-q", 1) + `]}`, 2, nil},
		{"an unknown rule", `{"allow": [` + entry("platform/a/chart/tests/x.sh", "grep-x", 1) + `]}`, 2, nil},
		{"a count of zero", `{"allow": [` + entry("platform/a/chart/tests/x.sh", "grep-q", 0) + `]}`, 2, nil},
		{"no owner", `{"allow": [{"path": "platform/a/chart/tests/x.sh", "rule": "grep-q", "count": 1, "reason": "r"}]}`, 2, nil},
		{"an entry twice", `{"allow": [` + entry("platform/a/chart/tests/x.sh", "grep-q", 1) + `, ` + entry("platform/a/chart/tests/x.sh", "grep-q", 1) + `]}`, 2, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			put := func(p, text string) {
				t.Helper()
				full := filepath.Join(root, p)
				if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			put("platform/a/chart/tests/x.sh", two)
			if c.baseline != "" {
				put(baselinePath, c.baseline)
			}
			gitInit(t, root)
			var out, errw bytes.Buffer
			if code := run([]string{"--root", root}, &out, &errw); code != c.code {
				t.Fatalf("exit %d, want %d\n%s%s", code, c.code, out.String(), errw.String())
			}
			for _, w := range c.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out.String())
				}
			}
		})
	}
}

func gitInit(t *testing.T, root string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
}

// A tree that holds no chart test script cannot be judged.
func TestNothingReadIsNotAPass(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("echo \"$out\" | head -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitInit(t, root)
	var out, errw bytes.Buffer
	if code := run([]string{"--root", root}, &out, &errw); code != 2 {
		t.Fatalf("exit %d, want 2\n%s%s", code, out.String(), errw.String())
	}
}
