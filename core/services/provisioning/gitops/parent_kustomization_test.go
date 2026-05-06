package gitops

import (
	"strings"
	"testing"
)

// TestUpdateParentKustomization_PrefixCollision regression-tests the bug
// observed live 2026-05-06: tenant "test"'s parent update silently no-op'd
// because the file already listed "test11" / "test13", and the substring
// match against "  - test" matched "  - test11" / "  - test13". The fix is
// an exact line match.
func TestUpdateParentKustomization_PrefixCollision(t *testing.T) {
	current := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - test13
  - market
  - aaa
  - bbb
  - test11
`
	got := UpdateParentKustomization(current, "test")
	if !strings.Contains(got, "\n  - test\n") {
		t.Fatalf("expected '  - test' as a fresh entry; got:\n%s", got)
	}
	// Existing entries must remain untouched.
	for _, want := range []string{"  - test13", "  - market", "  - aaa", "  - bbb", "  - test11"} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("expected %q to remain; got:\n%s", want, got)
		}
	}
}

// TestUpdateParentKustomization_AlreadyPresent ensures we don't double-add a
// slug that already has its own line.
func TestUpdateParentKustomization_AlreadyPresent(t *testing.T) {
	current := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - test
  - test11
`
	got := UpdateParentKustomization(current, "test")
	if got != current {
		t.Fatalf("expected unchanged when slug already listed; got:\n%s", got)
	}
}

// TestUpdateParentKustomization_EmptyResources adds the first entry into
// the explicit "resources: []" form.
func TestUpdateParentKustomization_EmptyResources(t *testing.T) {
	current := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: []
`
	got := UpdateParentKustomization(current, "alpha")
	if !strings.Contains(got, "resources:\n  - alpha\n") {
		t.Fatalf("expected 'resources:' block with alpha; got:\n%s", got)
	}
}
