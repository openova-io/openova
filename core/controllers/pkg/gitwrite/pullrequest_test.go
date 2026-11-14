package gitwrite_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/internal/giteadouble"
)

// prChange is change() in ModePullRequest.
func prChange(files ...gitwrite.FileChange) gitwrite.Change {
	ch := change(files...)
	ch.Mode = gitwrite.ModePullRequest
	return ch
}

const firstPR = giteadouble.URLBase + "openova/sovereign-regions/pulls/1"

// TestPullRequestModeCommitsToChangeBranch: one commit on change/<ChangeID>,
// main unchanged, one pull request into main with the summary as title
// and the commit message as body.
func TestPullRequestModeCommitsToChangeBranch(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 1\n"))
	mainHead := d.Head("openova", "sovereign-regions", "main")
	ch := prChange(upsert(p("a.yaml"), "a: 2\n"), upsert(p("b.yaml"), "b: 1\n"))
	ch.Author = gitwrite.Identity{Name: "Ann Lee", Email: "ann@example.org"}
	res, err := c.Commit(context.Background(), ch)
	branch := gitwrite.ChangeBranch(ch.ChangeID)
	if err != nil || branch != "change/"+ch.ChangeID || !res.Committed || res.PullRequestURL != firstPR || res.Commit != d.Head("openova", "sovereign-regions", branch) {
		t.Fatalf("branch %q, result %+v, err %v", branch, res, err)
	}
	if res.Files[0].BlobSHA != giteadouble.BlobSHA([]byte("a: 2\n")) || res.Files[1].BlobSHA != giteadouble.BlobSHA([]byte("b: 1\n")) {
		t.Fatalf("files = %+v", res.Files)
	}
	onMain := d.Files("openova", "sovereign-regions", "main")
	if string(onMain[p("a.yaml")]) != "a: 1\n" || len(onMain) != 1 || d.Head("openova", "sovereign-regions", "main") != mainHead {
		t.Fatalf("main changed: %v", onMain)
	}
	if b := d.Files("openova", "sovereign-regions", branch); string(b[p("a.yaml")]) != "a: 2\n" || string(b[p("b.yaml")]) != "b: 1\n" {
		t.Fatalf("branch files = %v", b)
	}
	commits, prs := d.Commits(), d.PullRequests()
	if len(commits) != 1 || commits[0].Branch != branch || commits[0].AuthorEmail != "ann@example.org" || commits[0].CommitterName != "catalyst-fleet-bot" {
		t.Fatalf("commits = %+v", commits)
	}
	if len(prs) != 1 || prs[0].Head != branch || prs[0].Base != "main" || prs[0].Title != ch.Summary || prs[0].Body != commits[0].Message || prs[0].Token != "tok-catalyst-fleet-bot" {
		t.Fatalf("pull requests = %+v", prs)
	}
	if !strings.Contains(prs[0].Body, "Change-Id: "+ch.ChangeID) || !strings.Contains(prs[0].Body, "Requested-By: ann@example.org") {
		t.Fatalf("body = %q", prs[0].Body)
	}
}

// TestPullRequestModeNoOpAndIfMatch: a no-op and an IfMatch conflict on
// main create no branch, no commit and no pull request.
func TestPullRequestModeNoOpAndIfMatch(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 1\n"))
	head := d.Head("openova", "sovereign-regions", "main")
	res, err := c.Commit(context.Background(), prChange(upsert(p("a.yaml"), "a: 1\n"), gitwrite.FileChange{Path: p("gone.yaml"), Op: gitwrite.OpDelete}))
	if err != nil || res.Committed || res.Commit != head || res.PullRequestURL != "" {
		t.Fatalf("no-op: result %+v, err %v", res, err)
	}
	f := upsert(p("a.yaml"), "a: 2\n")
	f.IfMatch = giteadouble.BlobSHA([]byte("a: 0\n"))
	_, err = c.Commit(context.Background(), prChange(f))
	var ce *gitwrite.ConflictError
	if !errors.As(err, &ce) || ce.Path != p("a.yaml") || ce.Current != giteadouble.BlobSHA([]byte("a: 1\n")) {
		t.Fatalf("want a conflict on a.yaml, got %v", err)
	}
	if d.Calls("new-branch") != 0 || d.Calls("change") != 0 || d.Calls("pull") != 0 {
		t.Fatal("a no-op or a conflict created a branch, a commit or a pull request")
	}
	f.IfMatch = giteadouble.BlobSHA([]byte("a: 1\n"))
	if res, err := c.Commit(context.Background(), prChange(f)); err != nil || !res.Committed || res.PullRequestURL != firstPR {
		t.Fatalf("a matching IfMatch: result %+v, err %v", res, err)
	}
}

// TestPullRequestModeRepeat: when the pull request cannot be opened the
// error is ErrUnavailable; the same Change again opens it, and once more
// returns the open one, with one commit in all.
func TestPullRequestModeRepeat(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Intercept = func(method, kind string) int {
		if kind == "pull" {
			return http.StatusServiceUnavailable
		}
		return 0
	}
	ch := prChange(upsert(p("a.yaml"), "a: 1\n"))
	if _, err := c.Commit(context.Background(), ch); !errors.Is(err, gitwrite.ErrUnavailable) || len(d.Commits()) != 1 || len(d.PullRequests()) != 0 {
		t.Fatalf("want ErrUnavailable after one commit, got %v", err)
	}
	d.Intercept = nil
	for i := 0; i < 2; i++ {
		res, err := c.Commit(context.Background(), ch)
		if err != nil || res.Committed || res.PullRequestURL != firstPR || res.Commit != d.Head("openova", "sovereign-regions", gitwrite.ChangeBranch(ch.ChangeID)) {
			t.Fatalf("call %d: result %+v, err %v", i+2, res, err)
		}
	}
	if len(d.Commits()) != 1 || len(d.PullRequests()) != 1 {
		t.Fatalf("%d commits and %d pull requests, want 1 and 1", len(d.Commits()), len(d.PullRequests()))
	}
}

// TestPullRequestModeRefusals: an unknown mode and a base other than main
// are refused before any request; the Secret scan and the path policy
// hold; mode "" and "direct" commit to main and open no pull request.
func TestPullRequestModeRefusals(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	ctx := context.Background()
	unknown := change(upsert(p("a.yaml"), "a: 1\n"))
	unknown.Mode = "merge"
	dev := prChange(upsert(p("a.yaml"), "a: 1\n"))
	dev.Repo.Branch = "dev"
	for name, tc := range map[string]struct {
		ch   gitwrite.Change
		want error
	}{
		"unknown mode": {unknown, gitwrite.ErrInvalid},
		"base dev":     {dev, gitwrite.ErrInvalid},
		"Secret":       {prChange(upsert(p("s.yaml"), "apiVersion: v1\nkind: Secret\n")), gitwrite.ErrSecretContent},
		"path":         {prChange(upsert("clusters/other/intent/a.yaml", "a: 1\n")), gitwrite.ErrForbiddenPath},
	} {
		if _, err := c.Commit(ctx, tc.ch); !errors.Is(err, tc.want) {
			t.Errorf("%s: want %v, got %v", name, tc.want, err)
		}
	}
	if n := d.Calls(""); n != 0 {
		t.Fatalf("%d requests for refused changes, want 0", n)
	}
	for _, m := range []gitwrite.Mode{"", gitwrite.ModeDirect} {
		ch := change(upsert(p(string(m)+"x.yaml"), "x: 1\n"))
		ch.Mode = m
		res, err := c.Commit(ctx, ch)
		if err != nil || !res.Committed || res.PullRequestURL != "" || res.Commit != d.Head("openova", "sovereign-regions", "main") {
			t.Fatalf("mode %q: result %+v, err %v", m, res, err)
		}
	}
	if d.Calls("new-branch") != 0 || d.Calls("pull") != 0 {
		t.Fatal("a direct commit created a branch or a pull request")
	}
}
