package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
)

// #4250 — the per-Org org-tenants overlay parent index
// (clusters/<fqdn>/org-tenants/kustomization.yaml) is rebuilt via a
// read-modify-write each commit attempt. These tests freeze the invariant
// that a SIBLING Org is NEVER dropped from that list because of a transient
// read failure — dropping a sibling means Flux's prune=true Kustomization
// garbage-collects that sibling Org's namespace, the chronic teardown the
// issue describes.

// giteaParentStub stands up an httptest server that mimics the Gitea
// contents API for exactly one file (the parent kustomization). status
// controls the GET response: 200 (serve body), 404 (not found), or any 5xx
// (transient). All non-GET requests 200 so a commit can complete when the
// test reaches that far.
func giteaParentStub(t *testing.T, parentPath, parentBody string, status int) (*httptest.Server, *int32) {
	t.Helper()
	var getHits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/") && strings.Contains(r.URL.Path, parentPath):
			atomic.AddInt32(&getHits, 1)
			switch {
			case status == http.StatusOK:
				w.Header().Set("Content-Type", "application/json")
				env := map[string]string{
					"content":  base64.StdEncoding.EncodeToString([]byte(parentBody)),
					"encoding": "base64",
				}
				_ = json.NewEncoder(w).Encode(env)
			case status == http.StatusNotFound:
				http.NotFound(w, r)
			default:
				http.Error(w, "gitea is having a bad day", status)
			}
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			// Any other content GET (per-Org manifest existence probe) → 404
			// so commitOnceContents treats it as create.
			http.NotFound(w, r)
		default:
			// blob/tree/commit/updateRef and ChangeFiles batch — succeed.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha0000000000000000000000000000"},"object":{"sha":"newcommitsha0000000000000000000000000000"}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &getHits
}

const twoSiblingParent = `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - helmrepositories.yaml
  - org-1111aaaa
  - org-2222bbbb
`

// TestProvisionParentRebuild_TransientReadDoesNotPruneSiblings is the core
// #4250 regression: a transient (503) read of the parent index MUST fail the
// rebuild rather than fabricate an empty parent that lists only the new Org.
func TestProvisionParentRebuild_TransientReadDoesNotPruneSiblings(t *testing.T) {
	parentPath := "clusters/sovereign.example/org-tenants/kustomization.yaml"
	srv, _ := giteaParentStub(t, parentPath, twoSiblingParent, http.StatusServiceUnavailable)

	h := &Handler{
		GitHubClient: ghclient.NewClientWithAPIURL("token", "owner", "repo", srv.URL),
	}
	manifests := map[string]string{
		"clusters/sovereign.example/org-tenants/org-3333cccc/namespace.yaml": "kind: Namespace\n",
	}

	files, err := h.provisionParentRebuild(context.Background(), "main", parentPath, "org-3333cccc", manifests)
	if err == nil {
		t.Fatalf("expected an error on transient parent read so the commit is refused; got nil and files=%v", files)
	}
	if files != nil {
		t.Fatalf("expected nil files on transient read error (commit must be aborted), got %v", files)
	}
	if !strings.Contains(err.Error(), "4250") {
		t.Errorf("error should reference the #4250 guard, got: %v", err)
	}
}

// TestProvisionParentRebuild_HealthyReadPreservesSiblings asserts the happy
// path still merges the new Org WITHOUT dropping the two existing siblings.
func TestProvisionParentRebuild_HealthyReadPreservesSiblings(t *testing.T) {
	parentPath := "clusters/sovereign.example/org-tenants/kustomization.yaml"
	srv, _ := giteaParentStub(t, parentPath, twoSiblingParent, http.StatusOK)

	h := &Handler{
		GitHubClient: ghclient.NewClientWithAPIURL("token", "owner", "repo", srv.URL),
	}
	manifests := map[string]string{
		"clusters/sovereign.example/org-tenants/org-3333cccc/namespace.yaml": "kind: Namespace\n",
	}

	files, err := h.provisionParentRebuild(context.Background(), "main", parentPath, "org-3333cccc", manifests)
	if err != nil {
		t.Fatalf("healthy read should succeed, got: %v", err)
	}
	merged, ok := files[parentPath]
	if !ok {
		t.Fatalf("rebuild did not emit the parent kustomization at %s", parentPath)
	}
	for _, sibling := range []string{"  - org-1111aaaa", "  - org-2222bbbb"} {
		if !strings.Contains(merged, sibling) {
			t.Errorf("merged parent dropped sibling %q — sibling Org would be pruned:\n%s", sibling, merged)
		}
	}
	if !strings.Contains(merged, "  - org-3333cccc") {
		t.Errorf("merged parent missing the newly-provisioned Org:\n%s", merged)
	}
	if !strings.Contains(merged, "  - helmrepositories.yaml") {
		t.Errorf("merged parent dropped the shared helmrepositories.yaml entry:\n%s", merged)
	}
}

// TestProvisionParentRebuild_GenuineNotFoundSeedsEmpty asserts the safe
// first-Org path: a genuine 404 seeds an empty parent and adds only this
// Org (there are no siblings to preserve on a brand-new Sovereign).
func TestProvisionParentRebuild_GenuineNotFoundSeedsEmpty(t *testing.T) {
	parentPath := "clusters/sovereign.example/org-tenants/kustomization.yaml"
	srv, _ := giteaParentStub(t, parentPath, "", http.StatusNotFound)

	h := &Handler{
		GitHubClient: ghclient.NewClientWithAPIURL("token", "owner", "repo", srv.URL),
	}
	manifests := map[string]string{
		"clusters/sovereign.example/org-tenants/org-firstone/namespace.yaml": "kind: Namespace\n",
	}

	files, err := h.provisionParentRebuild(context.Background(), "main", parentPath, "org-firstone", manifests)
	if err != nil {
		t.Fatalf("genuine 404 (first Org) should seed empty + succeed, got: %v", err)
	}
	merged, ok := files[parentPath]
	if !ok {
		t.Fatalf("rebuild did not emit the parent kustomization at %s", parentPath)
	}
	if !strings.Contains(merged, "  - org-firstone") {
		t.Errorf("first-Org parent missing the Org entry:\n%s", merged)
	}
}

// TestReadFile_NotFoundSentinel asserts the sentinel the #4250 guard keys
// off: a 404 is errors.Is(ErrFileNotFound), a 5xx is NOT.
func TestReadFile_NotFoundSentinel(t *testing.T) {
	// 404 path.
	srv404, _ := giteaParentStub(t, "x/kustomization.yaml", "", http.StatusNotFound)
	c404 := ghclient.NewClientWithAPIURL("token", "owner", "repo", srv404.URL)
	_, err := c404.ReadFile(context.Background(), "main", "x/kustomization.yaml")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if got := isFileNotFound(err); !got {
		t.Errorf("404 must satisfy errors.Is(err, ErrFileNotFound); err=%v", err)
	}

	// 5xx path.
	srv503, _ := giteaParentStub(t, "x/kustomization.yaml", "", http.StatusBadGateway)
	c503 := ghclient.NewClientWithAPIURL("token", "owner", "repo", srv503.URL)
	_, err = c503.ReadFile(context.Background(), "main", "x/kustomization.yaml")
	if err == nil {
		t.Fatal("expected error on 502")
	}
	if isFileNotFound(err) {
		t.Errorf("502 must NOT satisfy errors.Is(err, ErrFileNotFound); err=%v", err)
	}
}

// TestUpdateParentKustomization_OnEmptyDropsSiblings documents WHY the guard
// matters: feeding UpdateParentKustomization an empty parent (the pre-#4250
// fallback) yields a single-Org list — the exact prune trigger. Pure-function
// proof, no network.
func TestUpdateParentKustomization_OnEmptyDropsSiblings(t *testing.T) {
	empty := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n"
	got := gitops.UpdateParentKustomization(empty, "org-3333cccc")
	for _, sibling := range []string{"org-1111aaaa", "org-2222bbbb"} {
		if strings.Contains(got, sibling) {
			t.Fatalf("test setup wrong: empty seed should not contain %q", sibling)
		}
	}
	if !strings.Contains(got, "org-3333cccc") {
		t.Fatalf("expected the new Org in the result:\n%s", got)
	}
	// The result lists ONLY org-3333cccc → committing it prunes every sibling.
	// This is precisely what provisionParentRebuild now refuses to do on a
	// transient read.
}

func isFileNotFound(err error) bool {
	return errors.Is(err, ghclient.ErrFileNotFound)
}
