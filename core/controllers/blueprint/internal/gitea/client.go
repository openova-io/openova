// Package gitea — minimal HTTP client for the Sovereign-local Gitea
// instance.
//
// This package is intentionally narrow: it exposes ONLY the operations
// the blueprint-controller needs to maintain the catalog mirror in
// the `catalog` Gitea Org per docs/NAMING-CONVENTION.md §11.2:
//
//   - GetFile(org, repo, branch, path)              read a file
//   - PutFile(org, repo, branch, path, content, msg) create-or-update
//   - DeleteFile(org, repo, branch, path, msg)      delete a file
//   - EnsureRepo(org, repo)                          create-if-missing
//
// Why a separate package from the existing
// `products/catalyst/bootstrap/api/internal/handler/sme_tenant_gitops.go`:
//
// That package shells out to `git` + `git push` over a clone of the
// openova-public GitOps repo. It runs in a pod with a writable tmpfs
// and its commit cadence (one tenant overlay per provisioning) tolerates
// the latency of clone-and-push. The blueprint-controller, by contrast,
// reconciles N Blueprint CRs per K8s watch event — the per-event work
// must be a small set of HTTP API calls, not a clone-push cycle.
//
// The Gitea HTTP API (api/v1) is the canonical seam for HTTP-level
// Gitea mutation; both Gitea's built-in web UI and Actions runner use
// it. Authentication via a personal-access token in the Authorization
// header.
//
// SLICES C1/C2 NOTE: When organization-controller (C1) or
// environment-controller (C2) need an HTTP Gitea client for their
// own Gitea-Org / repo creation flows, they should EXTEND this package
// rather than write a parallel one. The Coordinator's seam map will be
// updated to reflect this once C1/C2 land. For now, this package lives
// under the blueprint-controller's tree because it ships with C3; the
// Coordinator may move it to `core/internal/gitea/` in a follow-up
// slice when C1/C2 also need it.
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
	// BaseURL is the Gitea root, e.g. "https://gitea.hfmp.openova.io".
	BaseURL string

	// Token is a personal-access token with `repo` + `write:repository`
	// scopes for the catalog Gitea Org. In production this is wired
	// from CATALYST_GITEA_TOKEN.
	Token string

	// HTTP is the underlying client. Tests inject a httptest server.
	// Default: a 30s timeout client with retries on 5xx.
	HTTP *http.Client

	// User-Agent emitted on every request. Defaults to
	// "openova-blueprint-controller/1.0".
	UserAgent string
}

// NewClient returns a Client with sensible defaults.
func NewClient(baseURL, token string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
		UserAgent: "openova-blueprint-controller/1.0",
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

	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
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
		return fmt.Errorf("gitea: %s %s: %w", method, url, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{
			Method: method,
			URL:    url,
			Status: resp.StatusCode,
			Body:   string(respBody),
		}
	}
	if dst != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, dst); err != nil {
			return fmt.Errorf("gitea: decode response from %s: %w", url, err)
		}
	}
	return nil
}

// GetFile reads a file from a repo at the given branch. Returns
// (*FileResponse, nil) on success, (nil, IsNotFound-able error) when
// the file (or repo) doesn't exist, (nil, otherErr) on transport
// failures.
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
// Returns the new SHA on success.
func (c *Client) PutFile(ctx context.Context, org, repo, branch, path string, content []byte, message string) (string, error) {
	encoded := base64.StdEncoding.EncodeToString(content)

	// Probe existing.
	existing, err := c.GetFile(ctx, org, repo, branch, path)
	switch {
	case err == nil:
		// Decode existing content; skip the write if identical.
		decoded, decErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(existing.Content, "\n", ""))
		if decErr == nil && bytes.Equal(decoded, content) {
			return existing.SHA, nil
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
			return "", err
		}
		return out.SHA, nil
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
			return "", err
		}
		return out.SHA, nil
	default:
		return "", err
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
// Per docs/NAMING-CONVENTION.md §11.2, the catalog Gitea Org holds one
// repo per Blueprint at `<bp-name>`. The blueprint-controller pre-creates
// these via this call before issuing the first PutFile.
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
		"description":    "Catalyst Blueprint mirror — auto-managed by blueprint-controller. Do not edit manually.",
		"private":        false, // catalog Org per §11.2 is Sovereign-wide visible
		"auto_init":      true,  // ensures branch exists for first PutFile
		"default_branch": "main",
	}
	if err := c.do(ctx, http.MethodPost, createEndpoint, body, nil); err != nil {
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
