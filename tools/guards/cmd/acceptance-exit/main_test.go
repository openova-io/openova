package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pipe(cmd string) string {
	return "pipe: " + cmd + ", a tested command, is piped with no `set -o pipefail` before it; the pipeline has the status of its last command"
}

func loop(kind string) string {
	return "loop: a " + kind + " loop whose body holds no `|| exit 1` has the status of its last pass, not of its first failing one"
}

func subst(cmd string) string {
	return "substitution: " + cmd + ", a tested command, runs in a command substitution whose status is lost with or without `set -o pipefail`; only the last substitution of a command of assignments alone keeps it"
}

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
		{`test "$(NO_COLOR=1 go test -v ./... | grep -c '^--- PASS: ')" = 3`, subst("go")},
		{`test "$(bash scripts/check-kyverno-legacy-policies.sh | grep -c -x 'legacy_policy_objects=26')" = 1`, subst("bash")}, // CMP-064 A1
		{`set -o pipefail; test "$("$d/x" | grep -c y)" = 1`, subst("$d/x")},
		{`set -o pipefail; n="$(bash x.sh | grep -c -x y)" && test "$n" = 1`, ""},
		{`n="$(bash x.sh | grep -c -x y)" && test "$n" = 1`, pipe("bash")},
		{`test "$(grep -c x f)" = 1 && test "$(git ls-files | wc -l)" = 0 && test "$(jq -r .a f)" = b`, ""},
		{`for f in $(go run .); do test -e "$f" || exit 1; done`, subst("go")},
		{`export n="$(go run .)" && cat "$(helm template x c)" > "$(bash y.sh)"`, subst("go") + "\n" + subst("helm") + "\n" + subst("bash")},
		{`a="$(go test)" b="$(echo x)" && c="$(go vet)$(echo y)" && d="$(helm lint)" bash z.sh`, subst("go") + "\n" + subst("go") + "\n" + subst("helm")},
		{`test "$(go test ./...)$(echo)" = ok`, subst("go")}, // two in one word: the first is judged at the word, past the second
		{`a="$(go test)$(echo)" bash x.sh`, subst("go")},
		{`test "$(go test)$(bash x.sh)" = ok`, subst("go") + "\n" + subst("bash")},
		{`a="$(go test)" > f && b="$(go vet)" c=1 && test -n "$a$b"`, ""},
		{`test "$(echo "$(cd a && go test)")" = ok && (test "$(bash x.sh)" = ok)`, subst("go") + "\n" + subst("bash")},
		{`out="$(test "$(go test)" = 1 && echo y)" && test "$out" = y`, subst("go")},
		{`diff <(go run . | sort) want.txt`, ""},
		{`grep -q x <<<"$(go test ./... | tail -1)"`, subst("go")},
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
		{"# only a comment\n\n# and another", "unreadable: it holds no command"},
		{`go test | tail; for f in a; do go vet "$f"; done; bash x.sh | head`, pipe("go") + "\n" + loop("for") + "\n" + pipe("bash")}, // in the order of the text
		{`for f in a b c; do test "$(grep -c -F "$f" x.json)" = 1; done && bash y.sh`, loop("for")},                                   // CTL-232 A4
		{`for f in off on; do test "$(grep -c '^kind: ' "$t/$f.yaml")" = 0; done; test -s x`, loop("for")},                            // PRD-304 A1
		{`while read -r f; do bash "$f"; done < list.txt`, loop("while")},
		{`until go test ./...; do sleep 1; done`, loop("until")},
		{`for f in a; do echo "$f"; done > list.txt`, loop("for")},
		{`for f in a; do go test "$f" && exit 1; done`, loop("for")},
		{`for f in a; do go test "$f" || exit 0; done`, loop("for")},
		{`for f in a; do go test "$f" || 'exit' 1; done`, loop("for")},
		{`for f in a; do (go test "$f" || exit 1); done`, loop("for")}, // the exit leaves the subshell only
		{`for f in a; do go test "$f" || { (exit 1); }; done`, loop("for")},
		{`for f in a; do { go test "$f"; exit 1; }; done`, loop("for")},
		{`while go test || exit 1; do sleep 1; done`, loop("while")}, // in the condition, not the body
		{`for f in a; do for g in b; do go test "$g"; done || exit 1; done`, loop("for")},
		{`out="$(for f in a; do go test "$f"; done)" && test -n "$out"`, loop("for")},
		{`test "$(for f in a; do echo "$f"; done)" = a`, ""}, // an argument that runs no tested command
		{`for f in a; do go test "$f" || { echo "$f failed"; exit 1; }; done`, ""},
		{"for f in a; do go test \"$f\" ||\n  exit 1\ndone", ""},
		{`for f in a; do if test -e "$f"; then bash "$f" || exit 1; fi; done`, ""},
		{`for f in a; do for g in b; do go test "$g" || exit 1; done; done`, ""}, // the exit leaves both loops
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
	put(tickets+"/AAA-007.md", ticket("1", "**A1.** No block.\n\n"+item("A2", "go test | tail")+item("A3", "")+item("A4", `for f in a; do test -e "$f"; done`)+item("A5", `test "$(go test | grep -c ok)" = 1`)))
	var out, errw bytes.Buffer
	code := run([]string{"-root", root}, &out, &errw)
	want := []string{
		"FAIL docs/ledger/plan/tickets/AAA-002.md A2 " + pipe("bash"),
		"FAIL docs/ledger/plan/tickets/AAA-003.md A2 " + pipe("bash"),
		"FAIL docs/ledger/plan/tickets/AAA-004.md A2 " + pipe("bash"),
		"FAIL docs/ledger/plan/tickets/AAA-006.md unreadable: field 18 holds no item A1, A2, ...",
		"FAIL docs/ledger/plan/tickets/AAA-007.md A1 unreadable: it holds no command",
		"FAIL docs/ledger/plan/tickets/AAA-007.md A2 " + pipe("go"),
		"FAIL docs/ledger/plan/tickets/AAA-007.md A3 unreadable: it holds no command",
		"FAIL docs/ledger/plan/tickets/AAA-007.md A4 " + loop("for"),
		"FAIL docs/ledger/plan/tickets/AAA-007.md A5 " + subst("go"),
		"acceptance-exit: 9 finding(s) in the 6 tickets not merged. A tested command is piped only after `set -o pipefail`; a loop body holds `|| exit 1`; a substitution that runs one is assigned.",
	}
	if code != 1 || out.String() != strings.Join(want, "\n")+"\n" {
		t.Fatalf("exit %d, output\n%s\nwant exit 1 and\n%s", code, out.String(), strings.Join(want, "\n"))
	}
	for _, id := range []string{"AAA-002", "AAA-003", "AAA-004", "AAA-006", "AAA-007"} {
		put(tickets+"/"+id+".md", ticket("3", item("A1", "set -o pipefail; for f in a; do go test \"$f\" || exit 1; done | tail")))
	}
	out.Reset()
	if code := run([]string{"-root", root}, &out, &errw); code != 0 || out.String() != "acceptance-exit: the acceptance commands of the 6 tickets not merged (of 7) lose the status of no piped tested command, of no loop pass and of no substitution\n" {
		t.Fatalf("exit %d, output %q; want exit 0 with 6 of 7 judged", code, out.String())
	}
	errw.Reset()
	if code := run([]string{"-root", t.TempDir()}, &out, &errw); code != 2 || !strings.Contains(errw.String(), "no ticket under") {
		t.Fatalf("a tree without tickets: exit %d, %q; want exit 2", code, errw.String())
	}
}
