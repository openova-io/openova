// Issue CRUD on top of the canonical pkg/gitea Client.
//
// pulls.go covers the PR read surface (Wave 8) + merge (Wave 11). This
// file covers the Issue read+write surface needed by Wave 11's
// openova-sandbox-mcp tools (`gitea.issue.list / get / create /
// comment`). Same client envelope; same error mapping (ErrRepoNotFound
// on 404; *HTTPError otherwise).
//
// New endpoints (Gitea Admin REST API):
//
//	GET    /api/v1/repos/{owner}/{repo}/issues?state=...&page=...&limit=50
//	GET    /api/v1/repos/{owner}/{repo}/issues/{index}
//	POST   /api/v1/repos/{owner}/{repo}/issues
//	POST   /api/v1/repos/{owner}/{repo}/issues/{index}/comments
//
// Gitea conflates issues + PRs on the same /issues collection (PRs have
// `pull_request != nil`); the MCP `gitea.pr.*` family uses the dedicated
// /pulls endpoint, so callers wanting true issues only should either
// pass `type=issues` (Gitea ≥1.20) or filter on Issue.IsPullRequest().
package gitea

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// IssueState constrains the `state` query param on ListIssues. Gitea
// accepts "open" | "closed" | "all"; empty defaults server-side to
// "open" but we make the wire shape explicit.
type IssueState string

const (
	// IssueStateOpen lists only open issues (Gitea default).
	IssueStateOpen IssueState = "open"
	// IssueStateClosed lists only closed issues.
	IssueStateClosed IssueState = "closed"
	// IssueStateAll lists every issue regardless of state.
	IssueStateAll IssueState = "all"
)

// Issue is the slice of Gitea Issue fields the MCP tools surface.
//
// Gitea's `pull_request` field is non-nil when the row is actually a PR
// — callers wanting true issues should `.IsPullRequest()` filter, or
// pass `type=issues` to ListIssues for server-side scoping.
type Issue struct {
	ID          int64      `json:"id,omitempty"`
	Number      int64      `json:"number,omitempty"`
	URL         string     `json:"html_url,omitempty"`
	State       string     `json:"state,omitempty"`
	Title       string     `json:"title,omitempty"`
	Body        string     `json:"body,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	PullRequest *struct {
		// Only set when the row is a PR. Non-nil → IsPullRequest()==true.
		Merged bool `json:"merged,omitempty"`
	} `json:"pull_request,omitempty"`
}

// IsPullRequest reports whether the row is actually a Pull Request on
// Gitea's shared /issues collection. Callers wanting true issues only
// should `if !i.IsPullRequest()` filter.
func (i Issue) IsPullRequest() bool { return i.PullRequest != nil }

// IssueComment is the slice of Gitea Issue-comment fields the MCP
// `gitea.issue.comment` tool surfaces back to the agent.
type IssueComment struct {
	ID        int64      `json:"id,omitempty"`
	URL       string     `json:"html_url,omitempty"`
	Body      string     `json:"body,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// ListIssuesOpts threads optional filters through ListIssues without
// growing the positional signature.
type ListIssuesOpts struct {
	// State filters by open/closed/all. Empty → server default ("open").
	State IssueState
	// Type filters by "issues" | "pulls" | "" (both). Empty → both, with
	// PRs distinguishable via Issue.IsPullRequest().
	Type string
}

// issueCreate is the body of POST /repos/{owner}/{repo}/issues.
type issueCreate struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
}

// issueCommentCreate is the body of POST /issues/{index}/comments.
type issueCommentCreate struct {
	Body string `json:"body"`
}

// ListIssues returns every issue on the repo matching opts, walking
// Gitea's pagination (page=1..N, limit=50). Result order matches what
// Gitea returns (typically newest-first by creation).
//
// Returns ErrRepoNotFound on a first-page 404. Subsequent pagination
// failures bubble up as *HTTPError.
//
// Added Wave 11 for openova-sandbox-mcp `gitea.issue.list`.
func (c *Client) ListIssues(ctx context.Context, org, repo string, opts ListIssuesOpts) ([]Issue, error) {
	if org == "" || repo == "" {
		return nil, errors.New("gitea: ListIssues requires non-empty org, repo")
	}
	const pageSize = 50
	out := make([]Issue, 0, pageSize)
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("page", fmt.Sprintf("%d", page))
		if opts.State != "" {
			q.Set("state", string(opts.State))
		}
		if opts.Type != "" {
			q.Set("type", opts.Type)
		}
		endpoint := fmt.Sprintf("/repos/%s/%s/issues?%s",
			url.PathEscape(org), url.PathEscape(repo), q.Encode())
		var batch []Issue
		status, _, err := c.do(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			if page == 1 && status == http.StatusNotFound {
				return nil, ErrRepoNotFound
			}
			return nil, err
		}
		out = append(out, batch...)
		if len(batch) < pageSize {
			break
		}
	}
	return out, nil
}

// GetIssue fetches a single Issue by number. Returns ErrRepoNotFound on
// any 404 (Gitea doesn't distinguish "repo gone" from "issue number gone"
// cleanly on this endpoint), or *HTTPError otherwise. Callers can
// `IsNotFound(err)` to fold the 404 case.
//
// Added Wave 11 for openova-sandbox-mcp `gitea.issue.get`.
func (c *Client) GetIssue(ctx context.Context, org, repo string, number int64) (Issue, error) {
	if org == "" || repo == "" {
		return Issue{}, errors.New("gitea: GetIssue requires non-empty org, repo")
	}
	if number <= 0 {
		return Issue{}, errors.New("gitea: GetIssue requires positive issue number")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d",
		url.PathEscape(org), url.PathEscape(repo), number)
	var out Issue
	status, _, err := c.do(ctx, http.MethodGet, endpoint, nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return Issue{}, ErrRepoNotFound
		}
		return Issue{}, err
	}
	return out, nil
}

// CreateIssue opens a new Issue on (org, repo). Returns ErrRepoNotFound
// on 404 (repo doesn't exist); other non-2xx surface as *HTTPError.
// Idempotency: Gitea does NOT de-duplicate issues by title — calling
// CreateIssue twice opens TWO issues. The MCP tool exposes this verbatim;
// callers wanting find-or-create semantics should ListIssues first.
//
// Added Wave 11 for openova-sandbox-mcp `gitea.issue.create`.
func (c *Client) CreateIssue(ctx context.Context, org, repo, title, body string) (Issue, error) {
	if org == "" || repo == "" {
		return Issue{}, errors.New("gitea: CreateIssue requires non-empty org, repo")
	}
	if title == "" {
		return Issue{}, errors.New("gitea: CreateIssue requires non-empty title")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/issues",
		url.PathEscape(org), url.PathEscape(repo))
	var out Issue
	status, _, err := c.do(ctx, http.MethodPost, endpoint, issueCreate{Title: title, Body: body}, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return Issue{}, ErrRepoNotFound
		}
		return Issue{}, err
	}
	return out, nil
}

// CommentOnIssue posts a comment on issue #number. Works on both true
// issues and PR rows (Gitea conflates them on the /comments endpoint).
// Returns ErrRepoNotFound on 404; other non-2xx surface as *HTTPError.
//
// Added Wave 11 for openova-sandbox-mcp `gitea.issue.comment`.
func (c *Client) CommentOnIssue(ctx context.Context, org, repo string, number int64, body string) (IssueComment, error) {
	if org == "" || repo == "" {
		return IssueComment{}, errors.New("gitea: CommentOnIssue requires non-empty org, repo")
	}
	if number <= 0 {
		return IssueComment{}, errors.New("gitea: CommentOnIssue requires positive issue number")
	}
	if body == "" {
		return IssueComment{}, errors.New("gitea: CommentOnIssue requires non-empty body")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/issues/%d/comments",
		url.PathEscape(org), url.PathEscape(repo), number)
	var out IssueComment
	status, _, err := c.do(ctx, http.MethodPost, endpoint, issueCommentCreate{Body: body}, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return IssueComment{}, ErrRepoNotFound
		}
		return IssueComment{}, err
	}
	return out, nil
}
