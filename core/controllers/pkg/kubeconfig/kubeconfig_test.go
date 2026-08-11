// kubeconfig_test.go — the shared usability contract must answer BOTH ways.
//
// This package exists so the producer of a secondary-region kubeconfig and the
// consumers that read it back cannot disagree about what "usable" means. A
// contract that only ever says "unusable" would be as broken as the
// presence-only checks it replaces — it would red-flag every correctly-wired
// Sovereign on the estate — so every case below is paired against a control
// that answers the other way.
//
// Refs #6015 #6054 #6107 #6112.
package kubeconfig

import (
	"strings"
	"testing"
)

// hw293Stub is the measured live artefact: valid YAML, one cluster with a
// server URL, and nothing else. The absent trailing newline is part of the
// evidence — the document was cut mid-token rather than generated short.
const hw293Stub = "apiVersion: v1\n" +
	"kind: Config\n" +
	"clusters:\n" +
	"- cluster:\n" +
	"    server: https://212.72.24.6:6443\n" +
	"  name: c"

// completeSameCluster keeps the stub's exact cluster block and adds ONLY the
// three sections it lacks. If brevity, the server URL, or the cluster count
// were what made the stub unusable, this control would be refused too.
// Carries no credential material: an empty `user: {}` is enough.
const completeSameCluster = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.6:6443
  name: c
users:
- name: c
  user: {}
contexts:
- name: c
  context:
    cluster: c
    user: c
current-context: c
`

func TestDefects_NamesEachGapAndAcceptsTheControl(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"empty", "", []string{"empty"}},
		{"whitespace only", "   \n\t\n", []string{"empty"}},
		{"not a kubeconfig at all", "kubeconfig-for-me-east-215-b", []string{"unparseable"}},
		{"the hw293 artefact", hw293Stub, []string{"contexts", "current-context", "users"}},
		{
			"cluster KEY present but server VALUE empty",
			"apiVersion: v1\nkind: Config\nclusters:\n- cluster:\n    server: \"\"\n  name: c\n",
			[]string{"clusters", "contexts", "current-context", "users"},
		},
		// The vacuity arm. Without it, an implementation that returned a
		// defect for every input would pass every case above.
		{"complete", completeSameCluster, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Defects(tc.raw)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("Defects() = %v, want %v", got, tc.want)
			}
			if usable := Usable(tc.raw); usable != (len(tc.want) == 0) {
				t.Fatalf("Usable() = %v, but Defects() reported %v", usable, got)
			}
		})
	}
}

// TestDefects_AnonymousClientIsRefused pins the reason the contract is
// deliberately stricter than "the parser returned no error": a kubeconfig with
// a context but NO user parses fine and builds an ANONYMOUS client, which the
// peer apiserver refuses 403 on every write. A client that builds and can
// never write the listener is the exact silent-success shape this chain exists
// to kill, so it must count as unusable.
func TestDefects_AnonymousClientIsRefused(t *testing.T) {
	noUser := `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://212.72.24.6:6443
  name: c
contexts:
- name: c
  context:
    cluster: c
    user: ""
current-context: c
`
	got := Defects(noUser)
	if len(got) == 0 {
		t.Fatal("a kubeconfig with no users section was accepted; it builds an anonymous client that is 403 on every write")
	}
	if strings.Join(got, ",") != "users" {
		t.Fatalf("Defects() = %v, want exactly [users] — the other sections are present", got)
	}

	// CONTROL: the same document with a user entry is accepted, so the check
	// discriminates on the users section and not on something incidental.
	if !Usable(completeSameCluster) {
		t.Fatalf("the control with a users entry was refused: %v", Defects(completeSameCluster))
	}
}

func TestDescribeDefects_NamesTheEmptyCase(t *testing.T) {
	if got := DescribeDefects(nil); got != "none" {
		t.Fatalf("DescribeDefects(nil) = %q, want %q — an operator must not read a bare [] ", got, "none")
	}
	if got := DescribeDefects([]string{"users", "contexts"}); got != "users,contexts" {
		t.Fatalf("DescribeDefects() = %q", got)
	}
}
