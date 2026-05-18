// Wave-11 tests for MergePullRequest + Issue CRUD (issues.go).
//
// We don't extend the existing pullsFake / fakeGitea handlers — they were
// frozen to lock the surface they cover. A focused per-feature fake
// keeps the assertion radius small and avoids regressing the existing
// list/get coverage when this file's expectations evolve.
package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// mergeIssuesFake handles ONLY the endpoints touched by MergePullRequest
// + the four Issue methods. Anything else 404s so a test catching a typo
// gets a clear "unhandled" failure instead of a silent pass.
type mergeIssuesFake struct {
	mu sync.Mutex

	// merged is keyed by "<org>/<repo>/<number>" → the Do style the
	// client posted. Lets a test assert "we passed style=squash".
	merged map[string]string

	// issuesByRepo is keyed by "<org>/<repo>" → ordered issue list.
	// Mutation: CreateIssue appends a new entry with an
	// auto-incrementing Number; GET /issues/{idx} reads it back.
	issuesByRepo map[string][]Issue

	// comments is keyed by "<org>/<repo>/<number>" → comment count.
	// We don't store the comment bodies — the test asserts the
	// returned IssueComment shape end-to-end.
	comments map[string]int
}

func newMergeIssuesFake() *mergeIssuesFake {
	return &mergeIssuesFake{
		merged:       map[string]string{},
		issuesByRepo: map[string][]Issue{},
		comments:     map[string]int{},
	}
}

func (f *mergeIssuesFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path
		// POST /api/v1/repos/{owner}/{repo}/pulls/{number}/merge
		if r.Method == http.MethodPost && strings.HasSuffix(p, "/merge") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			rest = strings.TrimSuffix(rest, "/merge")
			parts := strings.Split(rest, "/")
			if len(parts) != 4 || parts[2] != "pulls" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			var body mergePullRequestPayload
			_ = json.NewDecoder(r.Body).Decode(&body)
			key := parts[0] + "/" + parts[1] + "/" + parts[3]
			f.mu.Lock()
			f.merged[key] = body.Do
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			return
		}
		// GET /api/v1/repos/{owner}/{repo}/issues/{index}
		if r.Method == http.MethodGet && strings.Contains(p, "/issues/") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			parts := strings.Split(rest, "/")
			if len(parts) != 4 || parts[2] != "issues" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			idx, _ := strconv.Atoi(parts[3])
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, i := range f.issuesByRepo[repoKey] {
				if i.Number == int64(idx) {
					writeJSON(w, http.StatusOK, i)
					return
				}
			}
			http.Error(w, "no issue", http.StatusNotFound)
			return
		}
		// POST /api/v1/repos/{owner}/{repo}/issues/{index}/comments
		if r.Method == http.MethodPost && strings.HasSuffix(p, "/comments") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			rest = strings.TrimSuffix(rest, "/comments")
			parts := strings.Split(rest, "/")
			if len(parts) != 4 || parts[2] != "issues" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			idx, _ := strconv.Atoi(parts[3])
			var body issueCommentCreate
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.comments[repoKey+"/"+parts[3]] = f.comments[repoKey+"/"+parts[3]] + 1
			cid := int64(f.comments[repoKey+"/"+parts[3]])
			f.mu.Unlock()
			writeJSON(w, http.StatusCreated, IssueComment{
				ID:   cid + int64(idx)*100,
				Body: body.Body,
				URL:  "http://gitea/x",
			})
			return
		}
		// GET /api/v1/repos/{owner}/{repo}/issues?state=...&type=...
		if r.Method == http.MethodGet && strings.HasSuffix(p, "/issues") {
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/issues")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			f.mu.Lock()
			defer f.mu.Unlock()
			issues, ok := f.issuesByRepo[repoKey]
			if !ok {
				http.Error(w, "no repo", http.StatusNotFound)
				return
			}
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page == 0 {
				page = 1
			}
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit == 0 {
				limit = 50
			}
			start := (page - 1) * limit
			end := start + limit
			if start > len(issues) {
				start = len(issues)
			}
			if end > len(issues) {
				end = len(issues)
			}
			writeJSON(w, http.StatusOK, issues[start:end])
			return
		}
		// POST /api/v1/repos/{owner}/{repo}/issues
		if r.Method == http.MethodPost && strings.HasSuffix(p, "/issues") {
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/issues")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			var body issueCreate
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			defer f.mu.Unlock()
			n := int64(len(f.issuesByRepo[repoKey]) + 1)
			issue := Issue{
				ID:     n + 1000,
				Number: n,
				Title:  body.Title,
				Body:   body.Body,
				State:  "open",
				URL:    "http://gitea/x/" + strconv.Itoa(int(n)),
			}
			f.issuesByRepo[repoKey] = append(f.issuesByRepo[repoKey], issue)
			writeJSON(w, http.StatusCreated, issue)
			return
		}
		http.Error(w, "unhandled "+r.Method+" "+p, http.StatusNotFound)
	})
}

func newMergeIssuesClient(t *testing.T, f *mergeIssuesFake) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c := New(srv.URL, "test-token")
	c.HTTP = srv.Client()
	return c
}

func TestMergePullRequest_DefaultStyle(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	c := newMergeIssuesClient(t, f)

	if err := c.MergePullRequest(context.Background(), "acme", "blueprints", 42, MergePROpts{}); err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if got := f.merged["acme/blueprints/42"]; got != "merge" {
		t.Errorf("default style: got %q, want merge", got)
	}
}

func TestMergePullRequest_ExplicitStyle(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	c := newMergeIssuesClient(t, f)

	if err := c.MergePullRequest(context.Background(), "acme", "blueprints", 7, MergePROpts{Style: "squash"}); err != nil {
		t.Fatalf("MergePullRequest: %v", err)
	}
	if got := f.merged["acme/blueprints/7"]; got != "squash" {
		t.Errorf("got %q, want squash", got)
	}
}

func TestMergePullRequest_InvalidStyleRejected(t *testing.T) {
	t.Parallel()
	c := New("http://x", "tok")
	err := c.MergePullRequest(context.Background(), "acme", "r", 1, MergePROpts{Style: "wat"})
	if err == nil || !strings.Contains(err.Error(), "invalid style") {
		t.Errorf("err = %v, want invalid style", err)
	}
}

func TestMergePullRequest_RejectsEmptyArgs(t *testing.T) {
	t.Parallel()
	c := New("http://x", "tok")
	if err := c.MergePullRequest(context.Background(), "", "r", 1, MergePROpts{}); err == nil {
		t.Error("want error for empty org")
	}
	if err := c.MergePullRequest(context.Background(), "o", "r", 0, MergePROpts{}); err == nil {
		t.Error("want error for non-positive number")
	}
}

func TestCreateIssue_HappyPath(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	c := newMergeIssuesClient(t, f)

	issue, err := c.CreateIssue(context.Background(), "acme", "blueprints", "hello", "world")
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	if issue.Title != "hello" || issue.Body != "world" || issue.Number != 1 {
		t.Errorf("unexpected issue: %+v", issue)
	}
}

func TestCreateIssue_RejectsEmptyTitle(t *testing.T) {
	t.Parallel()
	c := New("http://x", "tok")
	if _, err := c.CreateIssue(context.Background(), "o", "r", "", "body"); err == nil {
		t.Error("want error for empty title")
	}
}

func TestListIssues_AfterCreate(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	c := newMergeIssuesClient(t, f)

	for _, ti := range []string{"a", "b", "c"} {
		if _, err := c.CreateIssue(context.Background(), "acme", "blueprints", ti, ""); err != nil {
			t.Fatalf("CreateIssue %q: %v", ti, err)
		}
	}
	out, err := c.ListIssues(context.Background(), "acme", "blueprints", ListIssuesOpts{})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("want 3 issues, got %d", len(out))
	}
	for i, want := range []string{"a", "b", "c"} {
		if out[i].Title != want {
			t.Errorf("issues[%d].Title = %q, want %q", i, out[i].Title, want)
		}
		if out[i].IsPullRequest() {
			t.Errorf("issues[%d] reported IsPullRequest", i)
		}
	}
}

func TestListIssues_RepoNotFound(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	c := newMergeIssuesClient(t, f)
	_, err := c.ListIssues(context.Background(), "ghost", "missing", ListIssuesOpts{})
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("err = %v, want ErrRepoNotFound", err)
	}
}

func TestGetIssue_AfterCreate(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	c := newMergeIssuesClient(t, f)
	issue, _ := c.CreateIssue(context.Background(), "acme", "blueprints", "first", "body")

	got, err := c.GetIssue(context.Background(), "acme", "blueprints", issue.Number)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if got.Title != "first" {
		t.Errorf("got Title=%q, want first", got.Title)
	}
}

func TestGetIssue_NotFound(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	// Seed an empty issue list so the repo exists in the fake's eyes,
	// but the requested number is absent → 404.
	f.issuesByRepo["acme/blueprints"] = []Issue{}
	c := newMergeIssuesClient(t, f)
	_, err := c.GetIssue(context.Background(), "acme", "blueprints", 999)
	if !IsNotFound(err) {
		t.Errorf("err = %v, want IsNotFound==true", err)
	}
}

func TestCommentOnIssue_HappyPath(t *testing.T) {
	t.Parallel()
	f := newMergeIssuesFake()
	c := newMergeIssuesClient(t, f)
	cm, err := c.CommentOnIssue(context.Background(), "acme", "blueprints", 7, "looks good")
	if err != nil {
		t.Fatalf("CommentOnIssue: %v", err)
	}
	if cm.Body != "looks good" {
		t.Errorf("Body = %q, want looks good", cm.Body)
	}
}

func TestCommentOnIssue_RejectsEmptyBody(t *testing.T) {
	t.Parallel()
	c := New("http://x", "tok")
	if _, err := c.CommentOnIssue(context.Background(), "o", "r", 1, ""); err == nil {
		t.Error("want error for empty body")
	}
}
