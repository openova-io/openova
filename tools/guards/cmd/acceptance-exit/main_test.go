package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pipeOf = "pipe: %s, a tested command, is piped with no `set -o pipefail` before it; the pipeline has the status of its last command"

func pipe(cmd string) string { return strings.Replace(pipeOf, "%s", cmd, 1) }

// TestJudge holds each rule to commands that break it and to the forms that keep it.
func TestJudge(t *testing.T) {
	for _, c := range []struct{ cmd, want string }{
		{`for ((i = 0; i < 2; i++)); do test "$i" || exit 1; done`, ""},
		{`echo for do done; grep -c 'for f in a; do x; done' f`, ""},
		{`go test ./... 2>&1 | tail -5 && test -z x`, pipe("go")}, // FND-191 A3
		{`set -o pipefail; go test ./... | tail -5`, ""},
		{`set -euo pipefail && go test ./... | tail -5`, ""},
		{`go test ./... | tail -5; set -o pipefail`, pipe("go")},
		{`(set -o pipefail; true) && go test | tail`, pipe("go")},
		{`set +o pipefail; go test | tail`, pipe("go")},
		{`printf '%s\n' "$out" | grep -q x && grep -c x f | grep -qx 1 && git ls-files | wc -l`, ""},
		{`NO_COLOR=1 timeout 60s env A=1 go test -v ./... | grep -c PASS`, pipe("go")},
		{`(cd a && go vet ./...) | tail`, pipe("go")},
		{`(cd a && env -u DB go test ./...) | grep -c SKIP | grep -x 1`, pipe("go")},
		{`for f in a; do bash "$f" || exit 1; done | tail -1`, pipe("bash")},
		{`"$d/sa" run | tail -1`, pipe("$d/sa")},
		{`./x.sh --self-test | tail -1`, pipe("./x.sh")},
		{`helm template x c | grep -q kind`, pipe("helm")},
		{`out="$(go test ./... | tail -5)" && test -n "$out"`, pipe("go")},
		{`test "$(NO_COLOR=1 go test -v ./... | grep -c '^--- PASS: ')" = 3`, ""},
		{`diff <(go run . | sort) want.txt`, ""},
		{`grep -q x <<<"$(go test ./... | tail -1)"`, ""},
		{`go test ./... || exit 1`, ""},
		{`go test ./... |& tail`, pipe("go")},
		{`grep -E 'a|b' f && grep a\|b f && test "$x" = "a|b"`, ""},
		{`[[ $x =~ ^(a|b)$ ]] && true`, ""},
		{`echo "x`, `unreadable: an unclosed "`},
		{`echo 'x`, `unreadable: an unclosed '`},
		{`echo $(x`, "unreadable: an unclosed ("},
		{`echo x)`, "unreadable: an unbalanced )"},
		{`for f in a; do true || exit 1`, "unreadable: an unclosed for"},
		{`done`, "unreadable: an unbalanced done"},
		{`[[ -e f`, "unreadable: an unclosed [["},
		{`case $x in a) true;; esac`, "unreadable: a case statement"},
		{"cat <<EOF\nx\nEOF", "unreadable: a here-document"},
		{"echo `date`", "unreadable: a backtick substitution; write $( )"},
		{`go test | tail; for f in a; do go vet "$f"; done; bash x.sh | head`, pipe("go") + "\n" + pipe("bash")}, // in the order of the text
	} {
		got := strings.Join(judge(c.cmd), "\n")
		if got != c.want {
			t.Errorf("judge(%q)\n got: %q\nwant: %q", c.cmd, got, c.want)
		}
	}
}

func ticket(rev, a string) string {
	return "---\nid: X\nticket_rev: " + rev + "\nplan_commit: 0\n---\n\n# X — t\n\n## 13. Pre-flight checks\n**P1.** Not an acceptance command.\n" +
		"```sh\ngo test | tail\n```\nExpected exit code: `0`\n\n## 18. Acceptance checks\n" + a + "\n## 19. Rollback\n```sh\ngo test | tail\n```\n"
}

func item(id, cmd string) string { return "**" + id + ".** Why.\n```sh\n" + cmd + "\n```\n" }

// TestRun reads a tree: a ticket whose done marker names its id and ticket_rev is not judged; any other is.
func TestRun(t *testing.T) {
	root := t.TempDir()
	put := func(path, text string) {
		p := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	marker := func(id, rev, schema string) string {
		return "schema: openova.io/plan-done/" + schema + "\nid: " + id + "\nticket_rev: " + rev + "\n"
	}
	bad := item("A1", "true") + "\n" + item("A2", "bash x.sh --self-test | tail -1")
	put(tickets+"/AAA-001.md", ticket("2", bad))
	put(done+"/AAA-001.yaml", marker("AAA-001", "2", "v1")) // merged: keeps its text
	put(tickets+"/AAA-002.md", ticket("2", bad))
	put(done+"/AAA-002.yaml", marker("AAA-002", "1", "v1")) // a marker of the revision before
	put(tickets+"/AAA-003.md", ticket("1", bad))
	put(done+"/AAA-003.yaml", marker("AAA-003", "1", "v0")) // another schema
	put(tickets+"/AAA-004.md", ticket("1", bad))
	put(done+"/AAA-004.yaml", marker("AAA-005", "1", "v1")) // another id
	put(tickets+"/AAA-005.md", ticket("1", item("A1", "test -e a")))
	put(tickets+"/AAA-006.md", ticket("1", "None.\n"))
	put(tickets+"/AAA-007.md", ticket("1", "**A1.** No block.\n\n"+item("A2", "go test | tail")))
	var out, errw bytes.Buffer
	code := run([]string{"-root", root}, &out, &errw)
	want := []string{
		"FAIL docs/ledger/plan/tickets/AAA-002.md A2 " + pipe("bash"),
		"FAIL docs/ledger/plan/tickets/AAA-003.md A2 " + pipe("bash"),
		"FAIL docs/ledger/plan/tickets/AAA-004.md A2 " + pipe("bash"),
		"FAIL docs/ledger/plan/tickets/AAA-006.md unreadable: field 18 holds no item A1, A2, ...",
		"FAIL docs/ledger/plan/tickets/AAA-007.md A1 unreadable: it has no sh block",
		"FAIL docs/ledger/plan/tickets/AAA-007.md A2 " + pipe("go"),
		"acceptance-exit: 6 finding(s) in the 6 tickets not merged. A tested command is piped only after `set -o pipefail`.",
	}
	if code != 1 || out.String() != strings.Join(want, "\n")+"\n" {
		t.Fatalf("exit %d, output\n%s\nwant exit 1 and\n%s", code, out.String(), strings.Join(want, "\n"))
	}
	for _, id := range []string{"AAA-002", "AAA-003", "AAA-004", "AAA-006", "AAA-007"} {
		put(tickets+"/"+id+".md", ticket("3", item("A1", "set -o pipefail; for f in a; do go test \"$f\" || exit 1; done | tail")))
	}
	out.Reset()
	if code := run([]string{"-root", root}, &out, &errw); code != 0 || out.String() != "acceptance-exit: the acceptance commands of the 6 tickets not merged (of 7) lose the status of no piped tested command\n" {
		t.Fatalf("exit %d, output %q; want exit 0 with 6 of 7 judged", code, out.String())
	}
	errw.Reset()
	if code := run([]string{"-root", t.TempDir()}, &out, &errw); code != 2 || !strings.Contains(errw.String(), "no ticket under") {
		t.Fatalf("a tree without tickets: exit %d, %q; want exit 2", code, errw.String())
	}
}
