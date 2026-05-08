// Package gitea is a minimal Gitea REST client used by
// environment-controller (slice C2 of EPIC-0 #1095) to:
//
//  1. Verify the per-Org Gitea Org exists
//     (`GET /orgs/{org}` — Environments without an Organization parent
//     are surfaced as `GiteaOrgReady=False`, never panic).
//
//  2. Idempotently write per-vCluster Flux GitRepository manifests into
//     the Org's Gitea repo at
//     `clusters/<host-cluster>/environments/<env-name>/gitrepository.yaml`
//     (`GET/POST/PUT /repos/{org}/{repo}/contents/{path}` — base64
//     payload, BLOB SHA + branch parameters mirror the GitHub Git Data
//     API shape).
//
// Gitea exposes a GitHub-compatible REST API at `/api/v1`, so the same
// path/parameter shape as the existing
// `core/services/provisioning/github/client.go` works against
// `gitea.<sovereign>:3000/api/v1`. We do NOT extend that client here —
// it commits via the tree-and-ref Git Data API, which Gitea also
// supports but is heavier than what this controller needs. Per-file
// upsert via `/contents/` is the lightest idempotent path.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 the controller writes manifests
// to Gitea repos; Flux applies them. There is NO `kubectl apply`,
// `helm install`, or `git` CLI exec in this package.
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

// Client is a minimal Gitea REST client. Construct via NewClient.
type Client struct {
	BaseURL string // e.g. "https://gitea.hfmp.acme.openova.io/api/v1"
	Token   string // Gitea personal/access token with org:read + repo:write
	HTTP    *http.Client
}

// NewClient returns a Client with sensible defaults. baseURL must end
// with `/api/v1` (Gitea's REST root). token may be empty for read-only
// endpoints in development; the controller's reconcile path requires
// repo:write and will fail with 401/403 surfaced as a Condition.
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = "http://gitea-http.gitea.svc.cluster.local:3000/api/v1"
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ErrOrgNotFound is returned by GetOrg when the named Org does not
// exist. The controller maps this to a `GiteaOrgReady=False` Condition
// rather than returning it from Reconcile (per slice brief: an
// Environment whose Organization parent does not exist is invalid; it
// surfaces a Condition, it does not crashloop).
var ErrOrgNotFound = errors.New("gitea: org not found")

// ErrRepoNotFound is returned by GetFile when the Org/repo pair does
// not exist. This is a transient signal during cold-start (Org exists
// but the per-app repo has not been created by application-controller
// yet). The reconciler logs and re-queues; it does NOT auto-create the
// repo — that is application-controller (slice C4)'s responsibility.
var ErrRepoNotFound = errors.New("gitea: repo not found")

// ErrFileNotFound is returned by GetFile when the path does not exist
// on the named branch. This is the create-vs-update branching signal
// for UpsertFile.
var ErrFileNotFound = errors.New("gitea: file not found")

// Org represents the subset of `GET /orgs/{org}` we use.
type Org struct {
	Username    string `json:"username"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
}

// GetOrg returns the Gitea Org by slug. Maps 404 → ErrOrgNotFound.
func (c *Client) GetOrg(ctx context.Context, org string) (*Org, error) {
	if org == "" {
		return nil, errors.New("gitea: org slug must be non-empty")
	}
	req, err := c.newRequest(ctx, http.MethodGet, "/orgs/"+url.PathEscape(org), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitea: GET /orgs/%s: %w", org, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrOrgNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("GET /orgs", resp)
	}
	var out Org
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gitea: decode org: %w", err)
	}
	return &out, nil
}

// FileContent represents the subset of `GET /repos/{org}/{repo}/contents/{path}`
// we use. SHA is the BLOB sha (NOT a commit sha); it is required by
// the PUT-update path so Gitea can refuse fast-forward conflicts.
type FileContent struct {
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Content string `json:"content"` // base64-encoded
}

// GetFile fetches the current content of a file on a given branch.
// Returns ErrRepoNotFound or ErrFileNotFound for the two distinct 404
// cases (the API returns the same 404 status; the body distinguishes).
func (c *Client) GetFile(ctx context.Context, org, repo, branch, path string) (*FileContent, error) {
	if org == "" || repo == "" || branch == "" || path == "" {
		return nil, errors.New("gitea: GetFile requires non-empty org, repo, branch, path")
	}
	q := url.Values{}
	q.Set("ref", branch)
	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s?%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscape(path), q.Encode())
	req, err := c.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitea: GET contents: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Distinguish missing-repo vs missing-file by probing the
		// repo root: missing-repo bubbles up so the controller can
		// log a clearer message + re-queue.
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(strings.ToLower(string(body)), "repository") {
			return nil, ErrRepoNotFound
		}
		return nil, ErrFileNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError("GET contents", resp)
	}
	var out FileContent
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("gitea: decode contents: %w", err)
	}
	return &out, nil
}

// UpsertFile idempotently creates-or-updates a file on `branch`.
//
//   - If the file does not exist (ErrFileNotFound), POST with a fresh
//     create payload.
//   - If the file exists, compare bytes with the new content; if equal,
//     return without writing (idempotent re-reconcile path — no spurious
//     commits in the per-Org repo's history).
//   - Otherwise PUT with the existing blob SHA so Gitea can refuse
//     fast-forward conflicts (consistent with the GitHub Git Data API
//     contract used by `core/services/provisioning/github/client.go`).
//
// The commit author + message are mandatory per Gitea API. Returns
// (committed=true) when the controller actually wrote bytes; false when
// it short-circuited because content was unchanged.
func (c *Client) UpsertFile(
	ctx context.Context,
	org, repo, branch, path string,
	content []byte,
	message, authorName, authorEmail string,
) (committed bool, err error) {
	if message == "" || authorName == "" || authorEmail == "" {
		return false, errors.New("gitea: UpsertFile requires message + author")
	}
	existing, err := c.GetFile(ctx, org, repo, branch, path)
	switch {
	case err == nil:
		// Decode existing content; if identical, short-circuit.
		decoded, decErr := base64.StdEncoding.DecodeString(existing.Content)
		if decErr == nil && bytes.Equal(decoded, content) {
			return false, nil
		}
		// Update.
		body := contentsBody{
			Branch: branch,
			Content: base64.StdEncoding.EncodeToString(content),
			Message: message,
			SHA:     existing.SHA,
			Author:  signature{Name: authorName, Email: authorEmail},
			Committer: signature{Name: authorName, Email: authorEmail},
		}
		return true, c.contentsCall(ctx, http.MethodPut, org, repo, path, body)
	case errors.Is(err, ErrFileNotFound):
		// Create.
		body := contentsBody{
			Branch: branch,
			Content: base64.StdEncoding.EncodeToString(content),
			Message: message,
			Author:  signature{Name: authorName, Email: authorEmail},
			Committer: signature{Name: authorName, Email: authorEmail},
		}
		return true, c.contentsCall(ctx, http.MethodPost, org, repo, path, body)
	case errors.Is(err, ErrRepoNotFound):
		return false, ErrRepoNotFound
	default:
		return false, err
	}
}

type signature struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type contentsBody struct {
	Branch    string    `json:"branch"`
	Content   string    `json:"content"`
	Message   string    `json:"message"`
	SHA       string    `json:"sha,omitempty"`
	Author    signature `json:"author,omitempty"`
	Committer signature `json:"committer,omitempty"`
}

func (c *Client) contentsCall(ctx context.Context, method, org, repo, path string, body contentsBody) error {
	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscape(path))
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("gitea: marshal contents body: %w", err)
	}
	req, err := c.newRequest(ctx, method, endpoint, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("gitea: %s contents: %w", method, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusNotFound:
		return ErrRepoNotFound
	default:
		return statusError(method+" contents", resp)
	}
}

func (c *Client) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	full := c.BaseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, method, full, body)
	if err != nil {
		return nil, fmt.Errorf("gitea: build request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func statusError(op string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("gitea: %s: %s — %s", op, resp.Status, strings.TrimSpace(string(body)))
}

// pathEscape escapes a path while preserving forward slashes (Gitea
// treats the path as a directory tree; url.PathEscape would encode "/"
// as "%2F" which would 404 the entire request).
func pathEscape(p string) string {
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		parts[i] = url.PathEscape(seg)
	}
	return strings.Join(parts, "/")
}
