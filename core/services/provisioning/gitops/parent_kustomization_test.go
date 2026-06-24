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

// giteaEnvelope is the exact corruption observed live on omantel.biz
// (dep 4635277cae4ffed9, 2026-06-24, #4265): the Gitea contents-API JSON
// envelope was committed VERBATIM as the parent kustomization.yaml. Appending
// `  - <slug>` onto it perpetuates the corruption, kustomize can't parse it,
// and the prune=true org-tenants Kustomization builds to empty — reaping every
// Org's namespace (incl. the demo Org's org-7283eb4a app namespace).
const giteaEnvelope = `{"name":"kustomization.yaml","path":"clusters/omantel.biz/org-tenants/kustomization.yaml","encoding":"base64","content":"YXBpVmVyc2lvbjoga3VzdG9taXplLmNvbmZpZy5rOHMuaW8vdjFiZXRhMQo=","sha":"5ba0c465"}`

// TestUpdateParentKustomization_HealsJSONEnvelope is the #4265 regression: a
// JSON-envelope parent must be rebuilt into a clean canonical kustomization
// (NOT appended to), with the new slug present and helmrepositories.yaml kept.
func TestUpdateParentKustomization_HealsJSONEnvelope(t *testing.T) {
	got := UpdateParentKustomization(giteaEnvelope, "demo")
	if !isParentKustomization(got) {
		t.Fatalf("expected a healed, parseable kustomization; got:\n%s", got)
	}
	if strings.Contains(got, `{"name"`) || strings.Contains(got, `"content"`) {
		t.Fatalf("healed output still carries the JSON envelope:\n%s", got)
	}
	if !strings.Contains(got, "\n  - helmrepositories.yaml") {
		t.Fatalf("healed parent dropped the shared helmrepositories.yaml entry:\n%s", got)
	}
	if !strings.Contains(got, "\n  - demo\n") && !strings.HasSuffix(got, "\n  - demo\n") {
		t.Fatalf("expected the new 'demo' slug in the healed parent; got:\n%s", got)
	}
}

// TestUpdateParentKustomization_HealsGarbage covers a non-JSON, non-
// kustomization parent (e.g. a truncated write or an unrelated blob): it must
// also be rebuilt into a clean canonical file rather than appended to.
func TestUpdateParentKustomization_HealsGarbage(t *testing.T) {
	got := UpdateParentKustomization("this is not yaml at all\nrandom bytes", "alpha")
	if !isParentKustomization(got) {
		t.Fatalf("expected a healed kustomization from garbage input; got:\n%s", got)
	}
	if !strings.Contains(got, "\n  - alpha\n") {
		t.Fatalf("expected 'alpha' in the healed parent; got:\n%s", got)
	}
}

// TestRemoveTenantFromParentKustomization_HealsJSONEnvelope ensures a teardown
// can never re-commit a JSON-envelope parent: a corrupt input is healed first,
// and removing a slug that isn't present is an idempotent no-op on the healed
// (clean) file. helmrepositories.yaml must survive.
func TestRemoveTenantFromParentKustomization_HealsJSONEnvelope(t *testing.T) {
	got := RemoveTenantFromParentKustomization(giteaEnvelope, "demo")
	if !isParentKustomization(got) {
		t.Fatalf("expected a healed kustomization after teardown; got:\n%s", got)
	}
	if strings.Contains(got, `"content"`) {
		t.Fatalf("teardown re-committed the JSON envelope:\n%s", got)
	}
	if !strings.Contains(got, "\n  - helmrepositories.yaml") {
		t.Fatalf("healed teardown parent dropped helmrepositories.yaml:\n%s", got)
	}
}

// TestHealParentKustomization_ValidPassThrough proves a healthy parent is
// returned byte-for-byte unchanged (no spurious rewrites / diffs on every
// provision).
func TestHealParentKustomization_ValidPassThrough(t *testing.T) {
	current := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - helmrepositories.yaml
  - demo
  - test
`
	if got := healParentKustomization(current); got != current {
		t.Fatalf("valid parent must pass through unchanged; got:\n%s", got)
	}
}

// TestParentKustomizationSlugs_ExcludesHelmRepos verifies slug salvage skips
// the shared helmrepositories.yaml file entry.
func TestParentKustomizationSlugs_ExcludesHelmRepos(t *testing.T) {
	current := `resources:
  - helmrepositories.yaml
  - demo
  - test
`
	slugs := parentKustomizationSlugs(current)
	if len(slugs) != 2 || slugs[0] != "demo" || slugs[1] != "test" {
		t.Fatalf("expected [demo test]; got %v", slugs)
	}
}
