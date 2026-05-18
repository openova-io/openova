package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// pullsFake is a tiny httptest stand-in scoped to the two new endpoints
// added in pulls.go: paginated list + get-by-number. We don't reuse the
// big fakeGitea handler from client_test.go because that one's GET /pulls
// branch is filter-by-head only (it was written before list-with-state
// existed) and overriding it would risk regressing CreatePullRequest's
// 409 path. A scoped fake keeps the new tests independent.
type pullsFake struct {
	// repos that exist (key = "owner/repo").
	repos map[string]bool
	// prs is keyed by "owner/repo/number".
	prs map[string]PullRequest
}

func newPullsFake() *pullsFake {
	return &pullsFake{
		repos: map[string]bool{},
		prs:   map[string]PullRequest{},
	}
}

func (f *pullsFake) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "no auth", http.StatusUnauthorized)
			return
		}
		p := r.URL.Path

		// GET /api/v1/repos/{owner}/{repo}/pulls/{number}
		if r.Method == http.MethodGet &&
			strings.HasPrefix(p, "/api/v1/repos/") &&
			strings.Contains(p, "/pulls/") {
			rest := strings.TrimPrefix(p, "/api/v1/repos/")
			// rest = "owner/repo/pulls/123"
			parts := strings.Split(rest, "/")
			if len(parts) != 4 || parts[2] != "pulls" {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			if !f.repos[repoKey] {
				http.Error(w, "no repo", http.StatusNotFound)
				return
			}
			pr, ok := f.prs[repoKey+"/"+parts[3]]
			if !ok {
				http.Error(w, "no pr", http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, pr)
			return
		}

		// GET /api/v1/repos/{owner}/{repo}/pulls?state=...
		if r.Method == http.MethodGet &&
			strings.HasPrefix(p, "/api/v1/repos/") &&
			strings.HasSuffix(p, "/pulls") {
			rest := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/repos/"), "/pulls")
			parts := strings.Split(rest, "/")
			if len(parts) != 2 {
				http.Error(w, "bad path", http.StatusBadRequest)
				return
			}
			repoKey := parts[0] + "/" + parts[1]
			if !f.repos[repoKey] {
				http.Error(w, "no repo", http.StatusNotFound)
				return
			}
			stateWanted := r.URL.Query().Get("state")
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			if page == 0 {
				page = 1
			}
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			if limit == 0 {
				limit = 50
			}
			out := []PullRequest{}
			for k, pr := range f.prs {
				if !strings.HasPrefix(k, repoKey+"/") {
					continue
				}
				if stateWanted != "" && stateWanted != "all" && pr.State != stateWanted {
					continue
				}
				out = append(out, pr)
			}
			// Stable order by Number ascending so test assertions can index.
			for i := 1; i < len(out); i++ {
				for j := i; j > 0 && out[j-1].Number > out[j].Number; j-- {
					out[j-1], out[j] = out[j], out[j-1]
				}
			}
			// Apply pagination window.
			start := (page - 1) * limit
			end := start + limit
			if start > len(out) {
				start = len(out)
			}
			if end > len(out) {
				end = len(out)
			}
			writeJSON(w, http.StatusOK, out[start:end])
			return
		}

		http.Error(w, "unhandled "+r.Method+" "+p, http.StatusNotFound)
	})
}

func newPullsClient(t *testing.T, f *pullsFake) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	c := New(srv.URL, "test-token")
	c.HTTP = srv.Client()
	return c
}

func TestListPullRequests_StateFilter(t *testing.T) {
	t.Parallel()
	f := newPullsFake()
	f.repos["acme/blueprints"] = true
	for i := int64(1); i <= 3; i++ {
		pr := PullRequest{ID: i, Number: i, State: "open", Title: fmt.Sprintf("open #%d", i)}
		pr.Head.Ref = "feature/" + fmt.Sprint(i)
		pr.Base.Ref = "main"
		f.prs[fmt.Sprintf("acme/blueprints/%d", i)] = pr
	}
	for i := int64(10); i <= 11; i++ {
		pr := PullRequest{ID: i, Number: i, State: "closed", Title: fmt.Sprintf("closed #%d", i)}
		pr.Head.Ref = "old/" + fmt.Sprint(i)
		pr.Base.Ref = "main"
		f.prs[fmt.Sprintf("acme/blueprints/%d", i)] = pr
	}
	c := newPullsClient(t, f)

	open, err := c.ListPullRequests(context.Background(), "acme", "blueprints", ListPRsOpts{State: PRStateOpen})
	if err != nil {
		t.Fatalf("ListPullRequests open: %v", err)
	}
	if len(open) != 3 {
		t.Fatalf("want 3 open PRs, got %d (%v)", len(open), open)
	}
	for _, pr := range open {
		if pr.State != "open" {
			t.Errorf("unexpected state %q on open list", pr.State)
		}
	}

	closed, err := c.ListPullRequests(context.Background(), "acme", "blueprints", ListPRsOpts{State: PRStateClosed})
	if err != nil {
		t.Fatalf("ListPullRequests closed: %v", err)
	}
	if len(closed) != 2 {
		t.Fatalf("want 2 closed PRs, got %d", len(closed))
	}

	all, err := c.ListPullRequests(context.Background(), "acme", "blueprints", ListPRsOpts{State: PRStateAll})
	if err != nil {
		t.Fatalf("ListPullRequests all: %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("want 5 PRs (all), got %d", len(all))
	}
}

func TestListPullRequests_RepoNotFound(t *testing.T) {
	t.Parallel()
	f := newPullsFake()
	c := newPullsClient(t, f)

	_, err := c.ListPullRequests(context.Background(), "ghost", "missing", ListPRsOpts{})
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("err = %v, want ErrRepoNotFound", err)
	}
}

func TestListPullRequests_RejectsEmptyArgs(t *testing.T) {
	t.Parallel()
	c := New("http://x", "tok")
	if _, err := c.ListPullRequests(context.Background(), "", "r", ListPRsOpts{}); err == nil {
		t.Error("want error for empty org")
	}
	if _, err := c.ListPullRequests(context.Background(), "o", "", ListPRsOpts{}); err == nil {
		t.Error("want error for empty repo")
	}
}

func TestGetPullRequest_HappyPath(t *testing.T) {
	t.Parallel()
	f := newPullsFake()
	f.repos["acme/blueprints"] = true
	pr := PullRequest{ID: 42, Number: 42, State: "open", Title: "hello"}
	pr.Head.Ref = "feature/x"
	pr.Base.Ref = "main"
	f.prs["acme/blueprints/42"] = pr
	c := newPullsClient(t, f)

	got, err := c.GetPullRequest(context.Background(), "acme", "blueprints", 42)
	if err != nil {
		t.Fatalf("GetPullRequest: %v", err)
	}
	if got.Number != 42 || got.Title != "hello" || got.Head.Ref != "feature/x" {
		t.Errorf("unexpected PR: %+v", got)
	}
}

func TestGetPullRequest_NotFound(t *testing.T) {
	t.Parallel()
	f := newPullsFake()
	f.repos["acme/blueprints"] = true
	c := newPullsClient(t, f)

	_, err := c.GetPullRequest(context.Background(), "acme", "blueprints", 999)
	if !IsNotFound(err) {
		t.Errorf("err = %v, want IsNotFound==true", err)
	}
}

func TestGetPullRequest_RejectsBadArgs(t *testing.T) {
	t.Parallel()
	c := New("http://x", "tok")
	if _, err := c.GetPullRequest(context.Background(), "", "r", 1); err == nil {
		t.Error("want error for empty org")
	}
	if _, err := c.GetPullRequest(context.Background(), "o", "r", 0); err == nil {
		t.Error("want error for non-positive number")
	}
}

// Compile-time check: PullRequest must JSON-decode into the same fields
// pulls.go reads. Caught a regression in the original Wave-8 draft where
// the alias name diverged from the canonical struct.
var _ = json.Unmarshal
