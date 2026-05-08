package controller

import (
	"strings"
	"testing"
)

func TestBindingName_ShortPath(t *testing.T) {
	got := bindingName("alice", "wp", "viewer")
	want := "useraccess-alice-wp-viewer"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBindingName_TruncatesOver63Chars(t *testing.T) {
	// Construct an input whose synthesized form would exceed 63.
	long := strings.Repeat("x", 60)
	got := bindingName(long, "application-with-long-name", "admin")
	if len(got) > 63 {
		t.Fatalf("name not truncated: len=%d (%q)", len(got), got)
	}
	// Truncation suffix must keep names deterministic.
	got2 := bindingName(long, "application-with-long-name", "admin")
	if got != got2 {
		t.Fatalf("non-deterministic truncation: %q vs %q", got, got2)
	}
	// Different inputs → different truncated outputs.
	other := bindingName(long, "application-with-long-name", "editor")
	if other == got {
		t.Fatalf("collision: %q == %q", got, other)
	}
}

func TestVClusterNamespace(t *testing.T) {
	if got := vClusterNamespace("acme"); got != "vcluster-acme" {
		t.Fatalf("got %q", got)
	}
}

func TestClusterRoleForRole(t *testing.T) {
	cases := map[string]string{
		"admin":  "openova:application-admin",
		"editor": "openova:application-editor",
		"viewer": "openova:application-viewer",
	}
	for in, want := range cases {
		if got := clusterRoleForRole(in); got != want {
			t.Fatalf("%s: got %q want %q", in, got, want)
		}
	}
}
