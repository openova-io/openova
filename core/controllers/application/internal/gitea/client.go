// Package gitea — minimal HTTP client for the Sovereign-local Gitea
// instance, scoped to the operations the application-controller needs.
//
// Per docs/EPICS-1-6-unified-design.md §3.2.3 + §3.3, every Application
// CR maps to exactly ONE per-Org Gitea repo:
//
//	gitea.<location-code>.<sovereign-domain>/<org>/<app-name>
//
// On each reconcile pass the application-controller:
//
//   1. EnsureRepo(org, app)      — create-if-missing, idempotent
//   2. PutFile(org, app, branch, path, bytes, msg)
//                                — write a manifest under
//                                  clusters/<host-cluster>/applications/<app>/{kustomization,helmrelease}.yaml
//   3. DeleteFile(org, app, branch, path, msg)
//                                — cascade-delete on Application CR
//                                  removal (Flux sees the missing
//                                  manifest and drains the workload).
//
// Why a separate package (and not a shared `core/controllers/internal/
// gitea/`): the canon §1 + §2 prescribes a shared internal/. The 4
// sibling Group C controllers each shipped their own copy because
// no one of them was first-writer in time to claim the shared seam.
// CC1 (Coordinator-led consolidation slice) will promote the union
// surface to `core/controllers/internal/gitea/` after C4 lands. Until
// then, each controller carries its own copy with a stable surface so
// the consolidation is a renames-only patch.
//
// The surface here intentionally MIRRORS the blueprint-controller's
// gitea.Client byte-for-byte for the operations C4 also needs
// (GetFile / PutFile / DeleteFile / EnsureRepo) — see slice C3 brief
// note in `core/controllers/blueprint/internal/gitea/client.go`.
package gitea

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a thin wrapper around http.Client targeting a Gitea
// instance's `/api/v1` surface.
type Client struct {
	// BaseURL is the Gitea root, e.g. "http://gitea-http.gitea:3000"
	// or "https://gitea.hfmp.openova.io".
	BaseURL string

	// Token is a personal-access token with `repo` + `write:repository`
	// scopes for the per-Org Gitea Orgs the controller writes to. In
	// production this is wired from CATALYST_GITEA_TOKEN.
	Token string

	// HTTP is the underlying client. Tests inject a httptest server.
	// Default: a 30s timeout client.
	HTTP *http.Client

	// User-Agent emitted on every request. Defaults to
	// "openova-application-controller/1.0".
	UserAgent string
}

// NewClient returns a Client with sensible defaults.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: "openova-application-controller/1.0",
	}
}

// FileResponse is the subset of Gitea's contents-API response we use.
// Full schema:
// https://docs.gitea.com/api#tag/repository/operation/repoGetContents
type FileResponse struct {
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Content string `json:"content"` // base64-encoded
	Type    string `json:"type"`    // "file" | "dir" | "symlink" | "submodule"
}

// commitFilePayload — body for create / update / delete file operations.
type commitFilePayload struct {
	Message string `json:"message"`
	Content string `json:"content,omitempty"` // base64-encoded; omitempty for delete
	SHA     string `json:"sha,omitempty"`     // required for update + delete
	Branch  string `json:"branch,omitempty"`
}

// HTTPError reports a non-2xx response from the Gitea API.
type HTTPError struct {
	Method string
	URL    string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gitea: %s %s: HTTP %d: %s", e.Method, e.URL, e.Status, e.Body)
}

// IsNotFound reports whether err is a 404 response.
func IsNotFound(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.Status == http.StatusNotFound
}

// IsConflict reports whether err is a 409 response (used by EnsureRepo
// when a parallel reconcile / a stale watch already created the repo).
func IsConflict(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.Status == http.StatusConflict
}

// do builds, sends, and decodes a Gitea API request. dst may be nil
// when the caller doesn't care about the response body.
func (c *Client) do(ctx context.Context, method, path string, body interface{}, dst interface{}) error {
	if c.BaseURL == "" {
		return errors.New("gitea: BaseURL is empty")
	}
	if c.Token == "" {
		return errors.New("gitea: Token is empty")
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("gitea: marshal body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	fullURL := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return fmt.Errorf("gitea: build request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("gitea: %s %s: %w", method, fullURL, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{
			Method: method,
			URL:    fullURL,
			Status: resp.StatusCode,
			Body:   string(respBody),
		}
	}
	if dst != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, dst); err != nil {
			return fmt.Errorf("gitea: decode response from %s: %w", fullURL, err)
		}
	}
	return nil
}

// GetFile reads a file from a repo at the given branch.
func (c *Client) GetFile(ctx context.Context, org, repo, branch, path string) (*FileResponse, error) {
	q := url.Values{}
	if branch != "" {
		q.Set("ref", branch)
	}
	endpoint := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
	if qs := q.Encode(); qs != "" {
		endpoint += "?" + qs
	}
	out := &FileResponse{}
	if err := c.do(ctx, http.MethodGet, endpoint, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutFile creates or updates a file at path. If the file already
// exists, the call performs an update by passing the current SHA.
// Idempotent: if content matches the existing file byte-for-byte, no
// API call is made (saves a write to Gitea + the etcd watch event).
//
// Returns (newSHA, committed). committed=false when the existing file
// was already byte-equal — surfaces to the controller's idempotency
// counter so re-reconcile on a steady state = 0 writes.
func (c *Client) PutFile(ctx context.Context, org, repo, branch, path string, content []byte, message string) (sha string, committed bool, err error) {
	encoded := base64.StdEncoding.EncodeToString(content)

	// Probe existing.
	existing, err := c.GetFile(ctx, org, repo, branch, path)
	switch {
	case err == nil:
		// Decode existing content; skip the write if identical.
		decoded, decErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(existing.Content, "\n", ""))
		if decErr == nil && bytes.Equal(decoded, content) {
			return existing.SHA, false, nil
		}
		// Update path.
		body := commitFilePayload{
			Message: message,
			Content: encoded,
			SHA:     existing.SHA,
			Branch:  branch,
		}
		out := &FileResponse{}
		endpoint := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s",
			url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
		if err := c.do(ctx, http.MethodPut, endpoint, body, out); err != nil {
			return "", false, err
		}
		return out.SHA, true, nil
	case IsNotFound(err):
		// Create path.
		body := commitFilePayload{
			Message: message,
			Content: encoded,
			Branch:  branch,
		}
		out := &FileResponse{}
		endpoint := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s",
			url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
		if err := c.do(ctx, http.MethodPost, endpoint, body, out); err != nil {
			return "", false, err
		}
		return out.SHA, true, nil
	default:
		return "", false, err
	}
}

// DeleteFile removes path from the repo at branch. Idempotent: a 404
// from the probe returns (true, nil) — the file is already absent.
//
// Returns (deleted, err). deleted=true when the file existed and was
// deleted (or was already absent); deleted=false only on transport
// failures.
func (c *Client) DeleteFile(ctx context.Context, org, repo, branch, path, message string) (bool, error) {
	existing, err := c.GetFile(ctx, org, repo, branch, path)
	switch {
	case IsNotFound(err):
		return true, nil
	case err != nil:
		return false, err
	}
	body := commitFilePayload{
		Message: message,
		SHA:     existing.SHA,
		Branch:  branch,
	}
	endpoint := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
	if err := c.do(ctx, http.MethodDelete, endpoint, body, nil); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureRepo creates the repo if it doesn't exist. Idempotent.
//
// Per docs/NAMING-CONVENTION.md §11.2, the per-Org Gitea Org holds one
// repo per Application at `<app-name>`. The application-controller
// pre-creates this via this call before issuing the first PutFile.
//
// Returns nil on success. A 404 on the org itself is surfaced via
// ErrOrgNotFound so the caller can re-queue with an explicit Pending
// condition (organization-controller, slice C1, owns Org creation).
func (c *Client) EnsureRepo(ctx context.Context, org, repo string) error {
	endpoint := fmt.Sprintf("/api/v1/repos/%s/%s",
		url.PathEscape(org), url.PathEscape(repo))
	if err := c.do(ctx, http.MethodGet, endpoint, nil, nil); err == nil {
		return nil
	} else if !IsNotFound(err) {
		return err
	}
	// Create.
	createEndpoint := fmt.Sprintf("/api/v1/orgs/%s/repos", url.PathEscape(org))
	body := map[string]interface{}{
		"name":           repo,
		"description":    "Application manifests — auto-managed by application-controller. Do not edit manually.",
		"private":        true, // per-Org per-App repo: tenant-scoped
		"auto_init":      true, // ensures branch exists for first PutFile
		"default_branch": "main",
	}
	if err := c.do(ctx, http.MethodPost, createEndpoint, body, nil); err != nil {
		// 404 on the create-under-org endpoint means the Org itself
		// doesn't exist yet — surface a typed error so the controller
		// re-queues with `OrgMissing` Pending instead of looping on a
		// transient.
		if IsNotFound(err) {
			return ErrOrgNotFound
		}
		// 409 = repo created by a parallel call between our GET probe
		// and the POST. Treat as success — the next GetFile/PutFile
		// will see the existing repo.
		if IsConflict(err) {
			return nil
		}
		return err
	}
	return nil
}

// ErrOrgNotFound is returned by EnsureRepo when the Org itself doesn't
// exist on the Gitea instance. Callers use this to surface a Pending
// condition with reason `OrgMissing`.
var ErrOrgNotFound = errors.New("gitea: org not found")

// EnsureBranch ensures the named branch exists on the repo by branching
// from the repo's default. Idempotent: 409 / 422 (already exists) is
// treated as success.
//
// Used so writes to Environment-mapped branches (`develop`, `staging`)
// don't fail when the auto-init path only created `main`.
func (c *Client) EnsureBranch(ctx context.Context, org, repo, branch string) error {
	if branch == "" || branch == "main" {
		// `main` is created by EnsureRepo's auto_init.
		return nil
	}
	// Probe for the branch first.
	probeEndpoint := fmt.Sprintf("/api/v1/repos/%s/%s/branches/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(branch))
	if err := c.do(ctx, http.MethodGet, probeEndpoint, nil, nil); err == nil {
		return nil
	} else if !IsNotFound(err) {
		return err
	}
	// Create from main.
	createEndpoint := fmt.Sprintf("/api/v1/repos/%s/%s/branches",
		url.PathEscape(org), url.PathEscape(repo))
	body := map[string]interface{}{
		"new_branch_name": branch,
		"old_branch_name": "main",
	}
	if err := c.do(ctx, http.MethodPost, createEndpoint, body, nil); err != nil {
		if IsConflict(err) {
			return nil
		}
		// 422 (Unprocessable Entity) — Gitea returns this when the
		// branch already exists OR the source branch doesn't exist.
		// We've already confirmed by probe that the branch doesn't
		// exist, so 422 means a parallel create won.
		var he *HTTPError
		if errors.As(err, &he) && he.Status == http.StatusUnprocessableEntity {
			return nil
		}
		return err
	}
	return nil
}

// pathEscapeSegments escapes each path segment but preserves slashes.
// `url.PathEscape` would encode the slashes, breaking Gitea's path
// resolution. We need per-segment escaping for cases where a path
// component contains a space or '#' / '?'.
func pathEscapeSegments(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
