// Read-side PR operations on top of the canonical pkg/gitea Client.
//
// client.go already carries the write-side (CreatePullRequest + the
// `findOpenPR` race fallback) but had no public read surface for the
// MCP server's `gitea.pr.list` + `gitea.pr.get` tools (Wave 8). The two
// helpers here add exactly that: a paginated list with state + filter
// passthrough, and a single-PR fetch by number. Both reuse the existing
// `Client.do` envelope so HTTP error mapping (ErrRepoNotFound) is
// shared with the rest of the surface.
//
// New endpoints (Gitea Admin REST API):
//
//	GET /api/v1/repos/{owner}/{repo}/pulls?state=...&page=...&limit=50
//	GET /api/v1/repos/{owner}/{repo}/pulls/{number}
//
// Why a separate file: client.go is already 800+ LOC. Wave 8 review
// scope is the two new methods; isolating them keeps the diff scoped
// and the canonical surface auditable from one place.
package gitea

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// PullRequestState constrains the `state` query param on ListPullRequests.
// Gitea accepts "open" | "closed" | "all"; empty defaults server-side to
// "open" but we make the wire shape explicit here so callers don't drift.
type PullRequestState string

const (
	// PRStateOpen lists only open PRs (Gitea default).
	PRStateOpen PullRequestState = "open"
	// PRStateClosed lists only closed/merged PRs.
	PRStateClosed PullRequestState = "closed"
	// PRStateAll lists every PR regardless of state.
	PRStateAll PullRequestState = "all"
)

// ListPRsOpts threads optional filters through ListPullRequests without
// expanding the positional signature.
type ListPRsOpts struct {
	// State filters by open/closed/all. Empty → server default ("open").
	State PullRequestState
	// Head filters by source branch (matches Gitea's `head=org:branch`).
	// Empty → no head filter.
	Head string
	// Base filters by target branch. Empty → no base filter.
	Base string
}

// ListPullRequests returns every PR on the repo matching opts, walking
// Gitea's pagination (page=1..N, limit=50). Result order matches what
// Gitea returns (typically newest-first by creation).
//
// Returns ErrRepoNotFound on a first-page 404. Subsequent pagination
// failures bubble up as *HTTPError.
//
// Added Wave 8 for openova-sandbox-mcp `gitea.pr.list`.
func (c *Client) ListPullRequests(ctx context.Context, org, repo string, opts ListPRsOpts) ([]PullRequest, error) {
	if org == "" || repo == "" {
		return nil, errors.New("gitea: ListPullRequests requires non-empty org, repo")
	}
	const pageSize = 50
	out := make([]PullRequest, 0, pageSize)
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", pageSize))
		q.Set("page", fmt.Sprintf("%d", page))
		if opts.State != "" {
			q.Set("state", string(opts.State))
		}
		if opts.Head != "" {
			// Gitea's `head` filter expects `<org>:<branch>` for same-repo
			// PRs. Accept both forms — pass through verbatim when the
			// caller already included the colon.
			head := opts.Head
			if !containsRune(head, ':') {
				head = org + ":" + head
			}
			q.Set("head", head)
		}
		if opts.Base != "" {
			q.Set("base", opts.Base)
		}
		endpoint := fmt.Sprintf("/repos/%s/%s/pulls?%s",
			url.PathEscape(org), url.PathEscape(repo), q.Encode())
		var batch []PullRequest
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

// MergePROpts threads the MERGE payload fields the MCP tool exposes
// without growing the positional signature.
type MergePROpts struct {
	// Style is one of "merge" | "rebase" | "rebase-merge" | "squash".
	// Empty defaults to "merge" (the conservative, non-rewriting option
	// — keeps the head SHA + parent chain stable so CI doesn't re-run
	// on a synthetic merge commit).
	Style string
	// Title overrides the default merge-commit title (squash + merge
	// styles only). Empty → Gitea picks the PR title.
	Title string
	// Message overrides the default merge-commit body. Empty → Gitea
	// picks the PR body.
	Message string
}

// mergePullRequestPayload — POST body for /pulls/{number}/merge.
// Gitea's `MergePullRequestOption` uses CamelCase JSON field names
// (`Do`, `MergeTitleField`, `MergeMessageField`) which is exactly the
// shape this struct emits.
type mergePullRequestPayload struct {
	Do                string `json:"Do"`
	MergeTitleField   string `json:"MergeTitleField,omitempty"`
	MergeMessageField string `json:"MergeMessageField,omitempty"`
}

// MergePullRequest merges PR #number on (org, repo).
//
// Gitea returns 200 on a successful merge, 404 if the PR doesn't exist,
// 405 if the PR isn't mergeable (work-in-progress, draft, conflicting),
// 409 if the head changed between Get and Merge. We surface the typed
// sentinel for 404 (both `repo gone` and `PR gone` end up here on
// Gitea's wire) and a *HTTPError for 405/409 so callers can inspect
// Status for retry/abort decisions.
//
// Added Wave 11 for openova-sandbox-mcp `gitea.pr.merge`.
func (c *Client) MergePullRequest(ctx context.Context, org, repo string, number int64, opts MergePROpts) error {
	if org == "" || repo == "" {
		return errors.New("gitea: MergePullRequest requires non-empty org, repo")
	}
	if number <= 0 {
		return errors.New("gitea: MergePullRequest requires positive PR number")
	}
	style := opts.Style
	if style == "" {
		style = "merge"
	}
	switch style {
	case "merge", "rebase", "rebase-merge", "squash":
		// ok
	default:
		return fmt.Errorf("gitea: MergePullRequest: invalid style %q (want merge|rebase|rebase-merge|squash)", style)
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls/%d/merge",
		url.PathEscape(org), url.PathEscape(repo), number)
	payload := mergePullRequestPayload{
		Do:                style,
		MergeTitleField:   opts.Title,
		MergeMessageField: opts.Message,
	}
	status, _, err := c.do(ctx, http.MethodPost, endpoint, payload, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return ErrRepoNotFound
		}
		return err
	}
	return nil
}

// GetPullRequest fetches a single PR by number. Returns ErrRepoNotFound
// on 404 with "repository" in the body (matching GetFile's heuristic),
// otherwise a plain *HTTPError with Status==404 when the PR number itself
// doesn't resolve. Callers can `IsNotFound(err)` to fold both cases.
//
// Added Wave 8 for openova-sandbox-mcp `gitea.pr.get`.
func (c *Client) GetPullRequest(ctx context.Context, org, repo string, number int64) (PullRequest, error) {
	if org == "" || repo == "" {
		return PullRequest{}, errors.New("gitea: GetPullRequest requires non-empty org, repo")
	}
	if number <= 0 {
		return PullRequest{}, errors.New("gitea: GetPullRequest requires positive PR number")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls/%d",
		url.PathEscape(org), url.PathEscape(repo), number)
	var out PullRequest
	status, _, err := c.do(ctx, http.MethodGet, endpoint, nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return PullRequest{}, ErrRepoNotFound
		}
		return PullRequest{}, err
	}
	return out, nil
}

// containsRune is a tiny strings.ContainsRune replacement kept inline so
// pulls.go doesn't import "strings" for a single use; the rest of the
// file uses url + fmt + http + errors only.
func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
