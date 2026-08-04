package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// #5387 (hw290) — THE KEYSTONE regression. Every funnel Organization that
// installed an app failed provisioning with a terminal red step:
//
//	git commit to per-Org repo failed: commit to per-Org repo walk-two/catalyst-tenant:
//	POST …/api/v1/repos/walk-two/catalyst-tenant/contents: 500
//	{"message":"PushOutOfDate Error … ! [rejected] a480a0e7… -> main (non-fast-forward)"}
//
// Gitea's ChangeFiles handler builds the commit in a temp clone of the branch
// head and pushes it back. A concurrent writer that advanced the head in
// between makes git reject the push; Gitea wraps that in ErrPushOutOfDate,
// which is absent from its handleCreateOrUpdateFileError mapping table and so
// escapes as a bare 500 whose body spells the reason `non-fast-forward` — not
// the `not a fast forward` / 409 / 422 shapes isGiteaRefRaceError knew. The
// classifier therefore called it FATAL and CommitFilesWithPruneAndRebuild
// returned on attempt 1: the ref-race retry machinery built by #3376/#5234
// existed but NEVER ENGAGED for the shape that was actually killing the
// funnel. (The live message carried no "ref-race persisted after N attempts",
// which is the tell that zero retries ran.)
//
// This is a pure branch-head compare-and-swap loss — re-reading the head and
// re-pushing is exactly the right remedy, which is what the retry loop already
// does once the error is classified correctly.

// giteaPushOutOfDateBody is the response Gitea 1.22 actually serves for this
// case: HTTP 500, message = git.ErrPushOutOfDate.Error(), i.e.
// "PushOutOfDate Error: <err>: <git stderr>".
const giteaPushOutOfDateBody = `{"message":"PushOutOfDate Error: exit status 1: To /data/git/repositories/walk-two/catalyst-tenant.git\n ! [rejected]        a480a0e7f31c0b7a0b6b0a0d5c1f1e2a3b4c5d6e -> main (non-fast-forward)\nerror: failed to push some refs to '/data/git/repositories/walk-two/catalyst-tenant.git'\n"}`

// TestIsGiteaRefRaceError_PushOutOfDate_5387 pins the classifier against the
// VERBATIM hw290 wire shape, and against the sibling error Gitea returns for a
// genuine (non-retryable) refusal so the widened match can't turn a hook
// rejection into an infinite-ish retry.
func TestIsGiteaRefRaceError_PushOutOfDate_5387(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			// The exact hw290 error, as doRequest renders it.
			name: "hw290 live shape — 500 PushOutOfDate / non-fast-forward",
			err: fmt.Errorf("GitHub API POST http://gitea-http.gitea.svc:3000/api/v1/repos/walk-two/catalyst-tenant/contents: 500 %s",
				giteaPushOutOfDateBody),
			want: true,
		},
		{
			name: "abbreviated ledger shape (message truncated by the console)",
			err:  errors.New(`GitHub API POST .../contents: 500 {"message":"PushOutOfDate Error … ! [rejected] a480a0e7… -> main (non-fast-forward)"}`),
			want: true,
		},
		{
			// git's other fast-forward-impossible wording; same remedy.
			name: "git 'fetch first' reject wrapped as PushOutOfDate",
			err:  errors.New(`GitHub API POST .../contents: 500 {"message":"PushOutOfDate Error: exit status 1: ! [rejected] main -> main (fetch first)"}`),
			want: true,
		},
		{
			// Gitea's sibling type for a pre-receive/branch-protection refusal.
			// Retrying can NEVER convert it — it must stay fatal.
			name: "PushRejected (pre-receive hook declined) stays fatal",
			err:  errors.New(`GitHub API POST .../contents: 500 {"message":"PushRejected Error: exit status 1: ! [remote rejected] main -> main (pre-receive hook declined)"}`),
			want: false,
		},
		{
			name: "unrelated 500 stays fatal",
			err:  errors.New(`GitHub API POST .../contents: 500 {"message":"database is locked"}`),
			want: false,
		},
	}
	for _, tc := range cases {
		if got := isGiteaRefRaceError(tc.err); got != tc.want {
			t.Errorf("%s: isGiteaRefRaceError = %v, want %v\n  err: %v", tc.name, got, tc.want, tc.err)
		}
	}
}

// TestCommitFilesWithPruneAndRebuild_RetriesGiteaPushOutOfDate_5387 drives the
// real commit path against a fake Gitea that serves the hw290 500 on the first
// batch and accepts the second — the shape of a concurrent writer that landed
// once and then went quiet.
//
// Before the fix this returns the 500 immediately (1 POST, rebuild called
// once) and the funnel step goes red. After it, the loop re-reads the moved
// head, re-runs the caller's rebuild, and lands the commit.
func TestCommitFilesWithPruneAndRebuild_RetriesGiteaPushOutOfDate_5387(t *testing.T) {
	shrinkCommitRetryDelays(t)

	var (
		mu        sync.Mutex
		postCount int
		headReads int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			mu.Lock()
			headReads++
			// The head MOVES after the first read — the concurrent writer.
			sha := "aaaa0000000000000000000000000000aaaa0000"
			if headReads > 1 {
				sha = "bbbb1111111111111111111111111111bbbb1111"
			}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"` + sha + `"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r) // fresh files → create ops
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			mu.Lock()
			postCount++
			first := postCount == 1
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if first {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(giteaPushOutOfDateBody))
				return
			}
			_, _ = w.Write([]byte(`{"commit":{"sha":"ccccc22222222222222222222222222ccccc222"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var rebuilds int
	c := NewClientWithAPIURL("token", "walk-two", "catalyst-tenant", srv.URL)
	err := c.CommitFilesWithPruneAndRebuild(
		context.Background(), "main", "day-2: install wordpress on Org walk-two",
		nil, nil,
		func(context.Context) (map[string]string, error) {
			rebuilds++
			return map[string]string{
				"vcluster/apps/app-wordpress.yaml": "kind: HelmRelease\n",
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("PushOutOfDate must be retried as a ref-race, not returned as fatal (#5387); got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 {
		t.Errorf("expected 2 batch POSTs (1 PushOutOfDate + 1 win), got %d", postCount)
	}
	if rebuilds != 2 {
		t.Errorf("expected the retry to re-run the caller's rebuild against the moved head, got %d rebuild(s)", rebuilds)
	}
	if headReads < 2 {
		t.Errorf("expected the retry to re-read the branch head, got %d read(s)", headReads)
	}
}

// TestCommitFilesWithPruneAndRebuild_PushOutOfDateExhaustionIsTerminal_5387
// guards the other direction: a branch that is hot FOREVER must still fail
// loudly after commitAttemptsMax, with the descriptive exhaustion error the
// funnel turns into a red step (#5234). Retrying a real defect into silence is
// not the fix.
func TestCommitFilesWithPruneAndRebuild_PushOutOfDateExhaustionIsTerminal_5387(t *testing.T) {
	shrinkCommitRetryDelays(t)

	var (
		mu        sync.Mutex
		postCount int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"aaaa0000000000000000000000000000aaaa0000"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			mu.Lock()
			postCount++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(giteaPushOutOfDateBody))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := NewClientWithAPIURL("token", "walk-two", "catalyst-tenant", srv.URL)
	err := c.CommitFiles(context.Background(), "main", "day-2: install wordpress", map[string]string{
		"vcluster/apps/app-wordpress.yaml": "kind: HelmRelease\n",
	})
	if err == nil {
		t.Fatalf("a permanently-hot branch must still fail — silent success would drop the purchased app")
	}
	if !strings.Contains(err.Error(), "ref-race persisted") {
		t.Errorf("exhaustion must carry the descriptive ref-race error the funnel paints red, got: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if postCount != commitAttemptsMax {
		t.Errorf("expected %d attempts before giving up, got %d", commitAttemptsMax, postCount)
	}
}
