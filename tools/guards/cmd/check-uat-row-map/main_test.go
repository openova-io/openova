package main

import (
	"bytes"
	"strings"
	"testing"
)

// Every rule fails on a tree built by the test, and a clean tree passes.
func TestEveryRuleCanFail(t *testing.T) {
	rules := map[string]bool{}
	for _, c := range selfCases() {
		c := c
		t.Run(c.name, func(t *testing.T) {
			found, err := runCase(t.TempDir(), c)
			if ok, got := judge(c, found, err); !ok {
				t.Fatalf("want %q, got %s", c.rule, got)
			}
		})
		rules[c.rule] = true
	}
	for _, r := range []string{"", "form", "twice", "missing", "count", "check", "record", "file", "title", "untested"} {
		if !rules[r] {
			t.Errorf("no case shows the rule %q failing", r)
		}
	}
}

// The command's --self-test runs the same cases and says so.
func TestSelfTestFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if rc := run([]string{"--self-test"}, &out, &errOut); rc != 0 {
		t.Fatalf("exit %d\n%s%s", rc, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "SELF-TEST OK") {
		t.Fatalf("no SELF-TEST OK line:\n%s", out.String())
	}
}

// A finding names the rule, the line of the map and the row.
func TestFindingNamesTheLine(t *testing.T) {
	c := selfCase{"twice", "twice", map[string]string{mapFile: "# head\n" +
		"R1,test:FND-G20,products/catalyst/bootstrap/ui/e2e/gate/applications/grid.spec.ts\n" +
		"2,test:FND-G21\nW3,covered:FND-G11\nG4,retire:ADR-0022\nR1,covered:FND-G11\n"}}
	found, err := runCase(t.TempDir(), c)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range found {
		got = append(got, f.String())
	}
	want := "twice: " + mapFile + ":6: row R1 has a line already, line 2"
	if !containsLine(got, want) {
		t.Fatalf("want a finding %q, got %q", want, got)
	}
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}
