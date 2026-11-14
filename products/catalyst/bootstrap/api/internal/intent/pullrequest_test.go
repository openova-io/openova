package intent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
)

// TestSubmitPullRequest: in pull-request mode the change is committed to
// its own branch with a pull request into main, main keeps its files, and
// Submit answers 202 awaiting-approval without waiting on the cache.
func TestSubmitPullRequest(t *testing.T) {
	f, c, d := newTest(t)
	f.off = true // Flux applies main only, and main does not change here
	d.SyncWait = 30 * time.Second
	start := time.Now()
	out, err := Submit(context.Background(), d, testScope, testRepo, []*unstructured.Unstructured{app(t, "site")}, nil, appPath, "Change applications", testUser, gitwrite.ViaAPI, gitwrite.AbsentSHA, nil, gitwrite.ModePullRequest)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != StateAwaitingApproval || out.Status(http.StatusOK) != http.StatusAccepted || out.Applied || out.Noop || out.Commit == "" || len(out.ChangeID) != 26 {
		t.Fatalf("outcome: %+v", out)
	}
	if !strings.HasPrefix(out.PullRequestURL, "https://gitea.double.invalid/acme/catalyst-tenant/pulls/") {
		t.Fatalf("pull request address %q", out.PullRequestURL)
	}
	if took := time.Since(start); took > 10*time.Second || c.watches != 0 {
		t.Fatalf("Submit took %s and opened %d watches; it must not wait for a merge", took, c.watches)
	}
	branch := gitwrite.ChangeBranch(out.ChangeID)
	if commits := f.Commits(); len(commits) != 1 || commits[0].Repo.Branch != branch {
		t.Fatalf("commits: %+v", commits)
	}
	if _, _, err := f.Read(context.Background(), testScope, testRepo, "intent/applications/site.yaml"); !errors.Is(err, gitwrite.ErrNotFound) {
		t.Fatalf("main holds the file before any merge: %v", err)
	}
	onBranch := gitwrite.Repo{Owner: testRepo.Owner, Name: testRepo.Name, Branch: branch}
	content, sha, err := f.Read(context.Background(), testScope, onBranch, "intent/applications/site.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := parseFile(content); sha != out.ETag || got.GetAnnotations()[ChangeIDAnnotation] != out.ChangeID {
		t.Errorf("file on the branch: sha %s, ETag %s, annotations %v", sha, out.ETag, got.GetAnnotations())
	}
}

// TestSubmitStatus: an applied change answers with the route's own status
// and no state; a change the cache did not show in time answers 202
// committed; a mode given twice or unknown is refused.
func TestSubmitStatus(t *testing.T) {
	f, _, d := newTest(t)
	ups := []*unstructured.Unstructured{app(t, "site")}
	applied, err := Submit(context.Background(), d, testScope, testRepo, ups, nil, appPath, "s", testUser, gitwrite.ViaAPI, "", nil, gitwrite.ModeDirect)
	if err != nil || !applied.Applied || applied.State != "" || applied.PullRequestURL != "" || applied.Status(http.StatusCreated) != http.StatusCreated {
		t.Fatalf("applied: %+v %v", applied, err)
	}
	f.off = true
	d.SyncWait = 50 * time.Millisecond
	late, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "web")}, nil, "")
	if err != nil || late.Applied || late.State != StateCommitted || late.Status(http.StatusOK) != http.StatusAccepted {
		t.Fatalf("not applied in time: %+v %v", late, err)
	}
	for name, mode := range map[string][]gitwrite.Mode{"two modes": {gitwrite.ModeDirect, gitwrite.ModePullRequest}, "unknown mode": {"merge"}} {
		if _, err := Submit(context.Background(), d, testScope, testRepo, []*unstructured.Unstructured{app(t, "db")}, nil, appPath, "s", testUser, gitwrite.ViaAPI, "", nil, mode...); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := len(f.Commits()); n != 2 {
		t.Fatalf("%d commits, want 2", n)
	}
}
