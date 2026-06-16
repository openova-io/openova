// application_iac_git_test.go — #3687 (fold #3694). Locks the contract
// that the running-Application write seams make Git the authoring home:
// a create/update commits the desired-state Application CR into the
// per-Org `iac` repo at `applications/<name>.yaml` (best-effort, clean
// IaC, idempotent), so `kubectl get applications -A` reflects a
// Git-resident estate and a hand `git push` round-trips.
package handler

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/giteapr"
)

// sampleApplicationCR builds an Application CR carrying both clean
// desired-state fields AND server-populated runtime fields, to prove the
// YAML render strips the latter.
func sampleApplicationCR() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetAPIVersion(ApplicationGVR().Group + "/" + ApplicationGVR().Version)
	obj.SetKind("Application")
	obj.SetName("blog")
	obj.SetNamespace("acme")
	obj.SetLabels(map[string]string{"catalyst.openova.io/organization": "acme"})
	// Runtime / server fields that MUST be stripped from the IaC.
	obj.SetResourceVersion("12345")
	obj.SetUID("00000000-0000-0000-0000-000000000abc")
	obj.SetGeneration(7)
	_ = unstructured.SetNestedField(obj.Object, "Pending", "status", "phase")
	// Desired-state spec.
	_ = unstructured.SetNestedField(obj.Object, "bp-wordpress", "spec", "blueprintRef", "name")
	_ = unstructured.SetNestedField(obj.Object, "1.2.3", "spec", "blueprintRef", "version")
	_ = unstructured.SetNestedSlice(obj.Object, []any{
		map[string]any{"name": "blog-shared-pg", "namespace": "acme", "context": "db/blog"},
	}, "spec", "dependsOn")
	return obj
}

func TestApplicationCRToYAML_StripsRuntimeFieldsKeepsSpec(t *testing.T) {
	y, err := applicationCRToYAML(sampleApplicationCR())
	if err != nil {
		t.Fatalf("applicationCRToYAML: %v", err)
	}
	s := string(y)

	// Desired state survives.
	for _, want := range []string{
		"kind: Application",
		"name: blog",
		"bp-wordpress",
		"version: 1.2.3",
		"db/blog",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("committed IaC missing %q; got:\n%s", want, s)
		}
	}
	// Runtime / server fields are stripped — committed IaC is clean
	// declarative state, not an etcd snapshot.
	for _, bad := range []string{"status:", "resourceVersion", "uid:", "generation:", "managedFields"} {
		if strings.Contains(s, bad) {
			t.Errorf("committed IaC must NOT contain runtime field %q; got:\n%s", bad, s)
		}
	}
}

func TestCommitApplicationCRToGit_CommitsToPerOrgIaCRepo(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	fg := newFakeGitea()
	h.SetGiteaClient(fg)

	committed, err := h.commitApplicationCRToGit(context.Background(), "acme", sampleApplicationCR())
	if err != nil {
		t.Fatalf("commitApplicationCRToGit: %v", err)
	}
	if !committed {
		t.Fatalf("expected a git commit, got committed=false")
	}

	// The Application CR landed at acme/iac/applications/blog.yaml.
	key := giteaKey("acme", giteapr.IaCRepoName, applicationIaCBranch, "applications/blog.yaml")
	raw, ok := fg.files[key]
	if !ok {
		t.Fatalf("expected committed Application CR at %s; have keys %v", key, fileKeys(fg))
	}
	if !strings.Contains(string(raw), "kind: Application") || !strings.Contains(string(raw), "name: blog") {
		t.Errorf("committed Application CR malformed; got:\n%s", raw)
	}

	// Idempotency: a re-commit of the same CR targets the SAME single
	// path (no duplicate / drifting files). The real gitea.Client.PutFile
	// byte-equal short-circuit (committed=false on unchanged bytes) is its
	// own contract; here we prove our writer is path-stable.
	before := len(fg.files)
	if _, err := h.commitApplicationCRToGit(context.Background(), "acme", sampleApplicationCR()); err != nil {
		t.Fatalf("re-commit: %v", err)
	}
	if len(fg.files) != before {
		t.Errorf("re-commit must not create a new file: had %d, now %d", before, len(fg.files))
	}
	if string(fg.files[key]) != string(raw) {
		t.Errorf("re-commit of the same CR must produce byte-identical content")
	}
}

func TestCommitApplicationCRToGit_UnwiredIsBestEffortNoop(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	// no SetGiteaClient → h.giteaClient == nil (chroot pre-cutover / CI)
	committed, err := h.commitApplicationCRToGit(context.Background(), "acme", sampleApplicationCR())
	if err != nil {
		t.Fatalf("unwired commit must not error: %v", err)
	}
	if committed {
		t.Fatalf("unwired commit must not report a commit")
	}
}

func TestApplicationManifestPath(t *testing.T) {
	if got := applicationManifestPath("blog"); got != "applications/blog.yaml" {
		t.Errorf("applicationManifestPath(blog) = %q, want applications/blog.yaml", got)
	}
}
