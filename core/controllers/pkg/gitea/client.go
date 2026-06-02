// Package gitea is the SUPERSET Gitea HTTP client used by every Group
// C controller. It consolidates the four divergent per-controller
// clients (slices C1-C4) into a single shared surface promoted to
// `core/controllers/pkg/gitea/` by slice CC2 (#1095) per
// `02-implementer-canon.md` §1+§2.
//
// The surface is the UNION of every operation any Group C controller
// already used:
//
//   - Org + Repo CRUD (was C1's surface):
//     GetOrg / CreateOrg / EnsureOrg / GetRepo / CreateRepo / EnsureRepo
//   - File CRUD (was C2/C3/C4's surface):
//     GetFile / PutFile / DeleteFile
//   - Branch CRUD (was C4's surface):
//     EnsureBranch
//
// CC2 deliberately preserves the canonical idempotency anchors:
//
//   - EnsureOrg / EnsureRepo: find-or-create, with 422/409 race-re-find.
//   - PutFile: byte-equal short-circuit (no API write when existing
//     content is byte-equal to the requested content). This is the
//     mechanism by which controller-runtime's resync loop produces
//     zero Gitea writes on a steady-state Application/Environment/etc.
//   - DeleteFile: 404-on-probe is treated as "already deleted".
//   - EnsureBranch: 409/422 on create is treated as "raced; success".
//
// Endpoints (Gitea Admin REST API, version 1.22):
//
//	GET    /api/v1/orgs/{org}
//	POST   /api/v1/orgs                          (org-create-as-self;
//	                                             admin-owned token →
//	                                             admin owns the new org)
//	GET    /api/v1/repos/{owner}/{repo}
//	POST   /api/v1/orgs/{org}/repos
//	GET    /api/v1/repos/{owner}/{repo}/branches/{branch}
//	POST   /api/v1/repos/{owner}/{repo}/branches
//	GET    /api/v1/repos/{owner}/{repo}/contents/{path}
//	POST   /api/v1/repos/{owner}/{repo}/contents/{path}
//	PUT    /api/v1/repos/{owner}/{repo}/contents/{path}
//	DELETE /api/v1/repos/{owner}/{repo}/contents/{path}
//
// Authentication: a static admin access-token (the catalyst-api
// service-account token managed by Sovereign-admin) — passed via
// `Authorization: token <hex>` header per Gitea convention.
//
// BaseURL semantics: the constructor takes the Gitea root WITHOUT
// `/api/v1`. The client prepends `/api/v1` to every endpoint.
// Migrating callers that previously stamped `/api/v1` into BaseURL
// must drop that suffix.
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

// ----------------------------------------------------------------------
// Errors
// ----------------------------------------------------------------------

// ErrOrgNotFound is returned by GetOrg when the slug does not resolve
// AND by EnsureRepo when the parent Org doesn't exist on the Gitea
// instance.
var ErrOrgNotFound = errors.New("gitea: org not found")

// ErrRepoNotFound is returned by GetRepo / GetFile when the repo does
// not resolve. GetFile distinguishes this from ErrFileNotFound by
// inspecting the 404 body for the substring "repository".
var ErrRepoNotFound = errors.New("gitea: repo not found")

// ErrFileNotFound is returned by GetFile when the path does not exist
// on the named branch (but the repo does).
var ErrFileNotFound = errors.New("gitea: file not found")

// ErrUserNotFound is returned by GetUser / CreateUserAccessToken when
// the user does not resolve. Used by the per-Org IaC repo bootstrap to
// distinguish "robot user never created" from "Gitea unreachable".
var ErrUserNotFound = errors.New("gitea: user not found")

// ErrAccessTokenExists is returned by CreateUserAccessToken when a
// token with the requested name already exists. Gitea cannot re-emit
// the plaintext, so the caller must choose between (a) delete + re-mint
// (rotation path) or (b) skip and trust an existing secret-store entry.
var ErrAccessTokenExists = errors.New("gitea: access token already exists")

// errAlreadyExists is the internal sentinel for the create-422/409
// race path. Mapped to typed sentinels at the call boundary.
var errAlreadyExists = errors.New("gitea: already exists")

// HTTPError reports a non-2xx response from the Gitea API that didn't
// map to a typed sentinel. Callers can inspect Status for retry
// decisions.
type HTTPError struct {
	Method string
	URL    string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gitea: %s %s: HTTP %d: %s", e.Method, e.URL, e.Status, e.Body)
}

// IsNotFound reports whether err signals a 404 — either via a typed
// sentinel (ErrOrgNotFound / ErrRepoNotFound / ErrFileNotFound) or via
// a HTTPError with Status==404.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrOrgNotFound) || errors.Is(err, ErrRepoNotFound) || errors.Is(err, ErrFileNotFound) || errors.Is(err, ErrUserNotFound) {
		return true
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusNotFound
	}
	return false
}

// IsConflict reports whether err is a 409. Used by EnsureRepo /
// EnsureBranch to absorb parallel-create races.
func IsConflict(err error) bool {
	if err == nil {
		return false
	}
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusConflict
	}
	return false
}

// ----------------------------------------------------------------------
// Client
// ----------------------------------------------------------------------

// Client wraps the Gitea Admin REST API.
type Client struct {
	// BaseURL is the Gitea root WITHOUT `/api/v1` (e.g.
	// "https://gitea.hfmp.openova.io" or
	// "http://gitea-http.gitea.svc.cluster.local:3000"). The client
	// prepends `/api/v1` to every endpoint internally.
	BaseURL string

	// Token is the personal-access token. Sent as `Authorization:
	// token <token>`.
	Token string

	// HTTP is the underlying HTTP client. Tests inject httptest.Server's
	// Client(). Default: 30s timeout.
	HTTP *http.Client

	// UserAgent is emitted on every request. Default: "openova-controllers/1.0".
	UserAgent string
}

// New returns a Client with a 30s default timeout.
func New(baseURL, token string) *Client {
	return NewWithHTTP(baseURL, token, &http.Client{Timeout: 30 * time.Second})
}

// NewWithHTTP returns a Client using the supplied http.Client.
func NewWithHTTP(baseURL, token string, hc *http.Client) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Token:     token,
		HTTP:      hc,
		UserAgent: "openova-controllers/1.0",
	}
}

// auth sets the Authorization + Accept headers on the request.
func (c *Client) auth(r *http.Request) {
	if c.Token != "" {
		r.Header.Set("Authorization", "token "+c.Token)
	}
	r.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		r.Header.Set("User-Agent", c.UserAgent)
	}
}

// do builds, sends, and decodes a Gitea API request.
//
// `endpoint` is appended to BaseURL+/api/v1; pass paths starting with
// '/' (e.g. "/orgs/foo"). dst may be nil when the caller doesn't need
// the response body.
//
// Returns nil on 2xx (decoding into dst when non-nil), or *HTTPError
// for any non-2xx.
func (c *Client) do(ctx context.Context, method, endpoint string, body interface{}, dst interface{}) (int, []byte, error) {
	if c.BaseURL == "" {
		return 0, nil, errors.New("gitea: BaseURL is empty")
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("gitea: marshal body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
	}

	fullURL := c.BaseURL + "/api/v1" + endpoint
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("gitea: build request: %w", err)
	}
	c.auth(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("gitea: %s %s: %w", method, fullURL, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, respBody, &HTTPError{
			Method: method,
			URL:    fullURL,
			Status: resp.StatusCode,
			Body:   string(respBody),
		}
	}
	if dst != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, dst); err != nil {
			return resp.StatusCode, respBody, fmt.Errorf("gitea: decode response from %s: %w", fullURL, err)
		}
	}
	return resp.StatusCode, respBody, nil
}

// ----------------------------------------------------------------------
// Org + Repo CRUD
// ----------------------------------------------------------------------

// Org is the slice of Gitea Organization fields the controllers use.
type Org struct {
	ID          int64  `json:"id,omitempty"`
	Username    string `json:"username,omitempty"`
	FullName    string `json:"full_name,omitempty"`
	Description string `json:"description,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
}

// orgCreate is the payload for POST /orgs. The authenticated user
// (the bearer of the admin access-token) becomes the new Org's owner.
// In Gitea 1.22+, the legacy POST /admin/orgs/{user} endpoint is no
// longer routed (returns 405 with `Allow: GET`); /orgs is the only
// supported create path for both admin- and user-owned tokens.
type orgCreate struct {
	Username    string `json:"username"`
	FullName    string `json:"full_name,omitempty"`
	Description string `json:"description,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
}

// Repo is the slice of Gitea Repository fields the controllers use.
type Repo struct {
	ID            int64  `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	FullName      string `json:"full_name,omitempty"`
	Description   string `json:"description,omitempty"`
	Private       bool   `json:"private,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// repoCreate is the request shape for POST /orgs/{org}/repos.
type repoCreate struct {
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Private       bool   `json:"private"`
	AutoInit      bool   `json:"auto_init"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// GetOrg fetches the Gitea Org by slug. Returns ErrOrgNotFound on 404.
func (c *Client) GetOrg(ctx context.Context, slug string) (Org, error) {
	if slug == "" {
		return Org{}, errors.New("gitea: org slug must be non-empty")
	}
	var out Org
	status, _, err := c.do(ctx, http.MethodGet, "/orgs/"+url.PathEscape(slug), nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return Org{}, ErrOrgNotFound
		}
		return Org{}, err
	}
	return out, nil
}

// CreateOrg creates a Gitea Org via POST /orgs (the org-create-as-self
// endpoint). The authenticated principal owns the new Org. Because the
// controller authenticates with a Gitea admin token, the admin user
// owns each created tenant Org — same semantic as the legacy
// /admin/orgs path. Returns errAlreadyExists (internal sentinel) on
// 422/409 so EnsureOrg can re-find idempotently.
//
// NOTE: Gitea 1.22+ no longer routes POST /api/v1/admin/orgs (returns
// HTTP 405 `Allow: GET`); the admin-namespaced create path is
// /api/v1/admin/users/{user}/orgs but is order-of-magnitude clunkier
// (requires knowing the admin username). /orgs covers every realistic
// production deployment because the controller's token is always
// owned by a sufficiently-privileged user.
func (c *Client) CreateOrg(ctx context.Context, slug, fullName, description, visibility string) (Org, error) {
	if visibility == "" {
		visibility = "private"
	}
	body := orgCreate{
		Username:    slug,
		FullName:    fullName,
		Description: description,
		Visibility:  visibility,
	}
	var out Org
	status, _, err := c.do(ctx, http.MethodPost, "/orgs", body, &out)
	if err != nil {
		if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
			return Org{}, errAlreadyExists
		}
		return Org{}, err
	}
	return out, nil
}

// EnsureOrg is the find-or-create shorthand. An existing Org's metadata
// is NOT mutated to match the desired fullName/description; the
// controller treats those as soft-attributes (operators rename via the
// Gitea UI without conflict).
func (c *Client) EnsureOrg(ctx context.Context, slug, fullName, description, visibility string) (Org, error) {
	existing, err := c.GetOrg(ctx, slug)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrOrgNotFound) {
		return Org{}, fmt.Errorf("gitea.EnsureOrg: get: %w", err)
	}
	created, err := c.CreateOrg(ctx, slug, fullName, description, visibility)
	if errors.Is(err, errAlreadyExists) {
		o, ferr := c.GetOrg(ctx, slug)
		if ferr != nil {
			return Org{}, fmt.Errorf("gitea.EnsureOrg: re-find after 422/409: %w", ferr)
		}
		return o, nil
	}
	if err != nil {
		return Org{}, fmt.Errorf("gitea.EnsureOrg: create: %w", err)
	}
	return created, nil
}

// GetRepo fetches the Gitea repo by owner/name. Returns ErrRepoNotFound on 404.
func (c *Client) GetRepo(ctx context.Context, owner, name string) (Repo, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(name))
	var out Repo
	status, _, err := c.do(ctx, http.MethodGet, endpoint, nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return Repo{}, ErrRepoNotFound
		}
		return Repo{}, err
	}
	return out, nil
}

// ListOrgRepos returns every repository under the named Org, walking
// Gitea's pagination (page=1..N, limit=50). Added for EPIC-2 Slice L
// catalog-svc (#1097) to enumerate Blueprint repos under the public
// + sovereign-curated catalog Orgs.
//
// Returns ErrOrgNotFound on 404 from the first page; partial pagination
// failures bubble up as HTTPError. Result order is the order Gitea
// returns (no client-side sort).
func (c *Client) ListOrgRepos(ctx context.Context, org string) ([]Repo, error) {
	if org == "" {
		return nil, errors.New("gitea: ListOrgRepos requires non-empty org")
	}
	const pageSize = 50
	out := make([]Repo, 0, pageSize)
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("/orgs/%s/repos?limit=%d&page=%d",
			url.PathEscape(org), pageSize, page)
		var batch []Repo
		status, _, err := c.do(ctx, http.MethodGet, endpoint, nil, &batch)
		if err != nil {
			if page == 1 && status == http.StatusNotFound {
				return nil, ErrOrgNotFound
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

// ContentEntry is one element of a Gitea contents listing.
//
// Type is "file" or "dir"; Path is the relative path (matches what
// Gitea returns); Name is the basename.
type ContentEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	SHA  string `json:"sha,omitempty"`
	Type string `json:"type"`
	Size int64  `json:"size,omitempty"`
}

// ListContents lists the contents of `path` in the repo at `branch`.
// `path` may be empty (root). Returns ErrFileNotFound if the path does
// not exist, ErrRepoNotFound if the repository does not exist.
//
// Added for EPIC-2 Slice L catalog-svc to enumerate per-Blueprint
// directories inside `<org>/shared-blueprints`.
func (c *Client) ListContents(ctx context.Context, org, repo, branch, path string) ([]ContentEntry, error) {
	if org == "" || repo == "" {
		return nil, errors.New("gitea: ListContents requires non-empty org, repo")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
	if branch != "" {
		endpoint += "?ref=" + url.QueryEscape(branch)
	}
	var out []ContentEntry
	status, body, err := c.do(ctx, http.MethodGet, endpoint, nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			if strings.Contains(strings.ToLower(string(body)), "repository") {
				return nil, ErrRepoNotFound
			}
			return nil, ErrFileNotFound
		}
		return nil, err
	}
	return out, nil
}

// CreateRepo creates a repo under the given Org. Returns ErrOrgNotFound
// when the parent Org doesn't exist, or errAlreadyExists on 422/409.
func (c *Client) CreateRepo(ctx context.Context, org, name, description string, private, autoInit bool, defaultBranch string) (Repo, error) {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	body := repoCreate{
		Name:          name,
		Description:   description,
		Private:       private,
		AutoInit:      autoInit,
		DefaultBranch: defaultBranch,
	}
	endpoint := fmt.Sprintf("/orgs/%s/repos", url.PathEscape(org))
	var out Repo
	status, _, err := c.do(ctx, http.MethodPost, endpoint, body, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return Repo{}, ErrOrgNotFound
		}
		if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
			return Repo{}, errAlreadyExists
		}
		return Repo{}, err
	}
	return out, nil
}

// EnsureRepo is the find-or-create shorthand. SUPERSET signature
// (description + private flag) — C3+C4 callers pass their canonical
// description literals + private flag; C1 already used this shape.
//
// auto_init=true is hardcoded so the default branch exists for the
// first PutFile. The default branch is "main".
//
// On a parallel-create race (409 on POST or "already exists" 422),
// re-finds the repo and returns it.
func (c *Client) EnsureRepo(ctx context.Context, org, name, description string, private bool) (Repo, error) {
	existing, err := c.GetRepo(ctx, org, name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrRepoNotFound) {
		return Repo{}, fmt.Errorf("gitea.EnsureRepo: get: %w", err)
	}
	created, err := c.CreateRepo(ctx, org, name, description, private, true, "main")
	if errors.Is(err, errAlreadyExists) {
		r, ferr := c.GetRepo(ctx, org, name)
		if ferr != nil {
			return Repo{}, fmt.Errorf("gitea.EnsureRepo: re-find after 422/409: %w", ferr)
		}
		return r, nil
	}
	if errors.Is(err, ErrOrgNotFound) {
		return Repo{}, ErrOrgNotFound
	}
	if err != nil {
		return Repo{}, fmt.Errorf("gitea.EnsureRepo: create: %w", err)
	}
	return created, nil
}

// EnsureBranch ensures `branch` exists on the repo. Idempotent on
// 409/422 (already-exists). main is a no-op (assumed created by
// EnsureRepo's auto_init).
func (c *Client) EnsureBranch(ctx context.Context, org, repo, branch string) error {
	if branch == "" || branch == "main" {
		return nil
	}
	probe := fmt.Sprintf("/repos/%s/%s/branches/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(branch))
	status, _, err := c.do(ctx, http.MethodGet, probe, nil, nil)
	if err == nil {
		return nil
	}
	if status != http.StatusNotFound {
		return err
	}
	create := fmt.Sprintf("/repos/%s/%s/branches",
		url.PathEscape(org), url.PathEscape(repo))
	body := map[string]interface{}{
		"new_branch_name": branch,
		"old_branch_name": "main",
	}
	status, _, err = c.do(ctx, http.MethodPost, create, body, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusConflict || status == http.StatusUnprocessableEntity {
		// Raced with a parallel create — success.
		return nil
	}
	return err
}

// ----------------------------------------------------------------------
// File CRUD
// ----------------------------------------------------------------------

// File is the surface returned by GetFile / PutFile.
type File struct {
	Path          string `json:"path"`
	SHA           string `json:"sha"`
	ContentBase64 string `json:"content"`
	Type          string `json:"type"`
}

// Decoded returns the file's raw bytes (decoding ContentBase64). Gitea
// embeds newlines in long base64 payloads; we strip them before decode.
func (f *File) Decoded() ([]byte, error) {
	if f == nil {
		return nil, nil
	}
	clean := strings.ReplaceAll(f.ContentBase64, "\n", "")
	if clean == "" {
		return []byte{}, nil
	}
	return base64.StdEncoding.DecodeString(clean)
}

// commitFilePayload — body for create / update / delete file ops.
type commitFilePayload struct {
	Message   string    `json:"message"`
	Content   string    `json:"content,omitempty"`
	SHA       string    `json:"sha,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Author    *signature `json:"author,omitempty"`
	Committer *signature `json:"committer,omitempty"`
}

type signature struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// PutFileOpts threads optional fields through PutFile without
// polluting the common signature. Author/Email are forwarded as both
// `author` and `committer` per Gitea convention.
type PutFileOpts struct {
	AuthorName  string
	AuthorEmail string
}

// GetFile fetches the file at path on the given branch.
//
// Returns ErrFileNotFound on a plain 404, or ErrRepoNotFound when the
// 404 body indicates the repository itself is missing (Gitea includes
// "repository" in that response).
func (c *Client) GetFile(ctx context.Context, org, repo, branch, path string) (File, error) {
	if org == "" || repo == "" || path == "" {
		return File{}, errors.New("gitea: GetFile requires non-empty org, repo, path")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
	if branch != "" {
		endpoint += "?ref=" + url.QueryEscape(branch)
	}
	var out File
	status, body, err := c.do(ctx, http.MethodGet, endpoint, nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			if strings.Contains(strings.ToLower(string(body)), "repository") {
				return File{}, ErrRepoNotFound
			}
			return File{}, ErrFileNotFound
		}
		return File{}, err
	}
	if out.Type != "" && out.Type != "file" {
		return out, fmt.Errorf("gitea: GetFile: path is %q, not file", out.Type)
	}
	return out, nil
}

// PutFile creates the file if absent, updates if present, or
// short-circuits with no API write when the existing content is byte-
// equal to `data`. The byte-equal short-circuit is the canonical
// idempotency anchor across all four pre-CC2 clients.
//
// Returns:
//   - file:      the post-write File (or pre-existing File on no-op)
//   - committed: true ONLY when the controller actually wrote bytes
//   - err:       non-nil on transport / API errors. ErrRepoNotFound is
//                surfaced as-is so callers can re-queue with a typed
//                pending condition.
//
// Optional opts pass committer attribution (used by C2 environment-
// controller for `author`/`committer` stamping).
func (c *Client) PutFile(ctx context.Context, org, repo, branch, path string, data []byte, message string, opts ...PutFileOpts) (file File, committed bool, err error) {
	var auth *signature
	if len(opts) > 0 && (opts[0].AuthorName != "" || opts[0].AuthorEmail != "") {
		auth = &signature{Name: opts[0].AuthorName, Email: opts[0].AuthorEmail}
	}

	// Probe existing.
	existing, gerr := c.GetFile(ctx, org, repo, branch, path)
	switch {
	case gerr == nil:
		// File exists. Byte-equal short-circuit.
		decoded, decErr := existing.Decoded()
		if decErr == nil && bytes.Equal(decoded, data) {
			return existing, false, nil
		}
		// Update via PUT with the existing SHA.
		body := commitFilePayload{
			Message:   message,
			Content:   base64.StdEncoding.EncodeToString(data),
			SHA:       existing.SHA,
			Branch:    branch,
			Author:    auth,
			Committer: auth,
		}
		out, werr := c.contentsWrite(ctx, http.MethodPut, org, repo, path, body)
		if werr != nil {
			return File{}, false, werr
		}
		return out, true, nil
	case errors.Is(gerr, ErrFileNotFound):
		// Create via POST.
		body := commitFilePayload{
			Message:   message,
			Content:   base64.StdEncoding.EncodeToString(data),
			Branch:    branch,
			Author:    auth,
			Committer: auth,
		}
		out, werr := c.contentsWrite(ctx, http.MethodPost, org, repo, path, body)
		if werr != nil {
			return File{}, false, werr
		}
		return out, true, nil
	case errors.Is(gerr, ErrRepoNotFound):
		return File{}, false, ErrRepoNotFound
	default:
		return File{}, false, gerr
	}
}

// contentsWrite issues a POST or PUT to the contents endpoint and
// decodes the wrapper-or-direct response shape Gitea returns.
func (c *Client) contentsWrite(ctx context.Context, method, org, repo, path string, body commitFilePayload) (File, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
	status, respBody, err := c.do(ctx, method, endpoint, body, nil)
	if err != nil {
		if status == http.StatusNotFound {
			return File{}, ErrRepoNotFound
		}
		return File{}, err
	}
	// Gitea wraps the file inside `content` on write responses; some
	// versions return the File directly.
	var wrapped struct {
		Content File `json:"content"`
	}
	if err := json.Unmarshal(respBody, &wrapped); err == nil && wrapped.Content.Path != "" {
		return wrapped.Content, nil
	}
	var direct File
	if err := json.Unmarshal(respBody, &direct); err == nil && direct.Path != "" {
		return direct, nil
	}
	// Empty body or unrecognized shape — return a minimal file.
	return File{Path: path}, nil
}

// DeleteFile removes path from the repo at branch. Idempotent: a 404
// from the probe returns (true, nil) — already absent.
func (c *Client) DeleteFile(ctx context.Context, org, repo, branch, path, message string) (bool, error) {
	existing, err := c.GetFile(ctx, org, repo, branch, path)
	switch {
	case errors.Is(err, ErrFileNotFound), errors.Is(err, ErrRepoNotFound):
		return true, nil
	case err != nil:
		return false, err
	}
	body := commitFilePayload{
		Message: message,
		SHA:     existing.SHA,
		Branch:  branch,
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/contents/%s",
		url.PathEscape(org), url.PathEscape(repo), pathEscapeSegments(path))
	if _, _, err := c.do(ctx, http.MethodDelete, endpoint, body, nil); err != nil {
		return false, err
	}
	return true, nil
}

// ----------------------------------------------------------------------
// Pull requests
// ----------------------------------------------------------------------

// PullRequest is the slice of Gitea Pull Request fields the handlers
// use. Added by EPIC-0 slice Z3 follow-up to wire YamlEditor's
// flux-managed Apply branch through a Gitea PR rather than a direct
// /apply on a flux-owned resource.
type PullRequest struct {
	ID     int64  `json:"id,omitempty"`
	Number int64  `json:"number,omitempty"`
	URL    string `json:"html_url,omitempty"`
	State  string `json:"state,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
	Head   struct {
		Ref string `json:"ref,omitempty"`
		SHA string `json:"sha,omitempty"`
	} `json:"head,omitempty"`
	Base struct {
		Ref string `json:"ref,omitempty"`
		SHA string `json:"sha,omitempty"`
	} `json:"base,omitempty"`
}

// pullRequestCreate is the body of POST /repos/{owner}/{repo}/pulls.
type pullRequestCreate struct {
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Head  string `json:"head"`
	Base  string `json:"base"`
}

// CreatePullRequest opens a PR from `head` (a branch on the same repo)
// into `base`. Returns the created PR or, if a PR already exists from
// the same head into base, the existing one (Gitea returns 409 in that
// case; we re-fetch the open PR list and pick the matching head).
//
// Caller is responsible for ensuring `head` exists (use EnsureBranch +
// PutFile to seed it).
func (c *Client) CreatePullRequest(ctx context.Context, org, repo, head, base, title, body string) (PullRequest, error) {
	if org == "" || repo == "" || head == "" || base == "" || title == "" {
		return PullRequest{}, errors.New("gitea: CreatePullRequest requires non-empty org, repo, head, base, title")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls", url.PathEscape(org), url.PathEscape(repo))
	payload := pullRequestCreate{Title: title, Body: body, Head: head, Base: base}
	var out PullRequest
	status, _, err := c.do(ctx, http.MethodPost, endpoint, payload, &out)
	if err == nil {
		return out, nil
	}
	// 409 = a PR from the same head is already open. Re-fetch + return
	// the matching one so callers see "PR already opened" as success.
	if status == http.StatusConflict {
		if existing, ferr := c.findOpenPR(ctx, org, repo, head, base); ferr == nil {
			return existing, nil
		}
	}
	if status == http.StatusNotFound {
		return PullRequest{}, ErrRepoNotFound
	}
	return PullRequest{}, err
}

// findOpenPR fetches the open PR from `head` into `base` on (org, repo)
// — used by CreatePullRequest's 409 race fallback.
func (c *Client) findOpenPR(ctx context.Context, org, repo, head, base string) (PullRequest, error) {
	endpoint := fmt.Sprintf("/repos/%s/%s/pulls?state=open&head=%s:%s&base=%s",
		url.PathEscape(org), url.PathEscape(repo),
		url.QueryEscape(org), url.QueryEscape(head),
		url.QueryEscape(base))
	var list []PullRequest
	if _, _, err := c.do(ctx, http.MethodGet, endpoint, nil, &list); err != nil {
		return PullRequest{}, err
	}
	for _, pr := range list {
		if pr.Head.Ref == head && pr.Base.Ref == base {
			return pr, nil
		}
	}
	return PullRequest{}, errors.New("gitea: no open PR matching head/base")
}

// pathEscapeSegments escapes each path segment but preserves slashes.
// `url.PathEscape` would encode "/" as "%2F", breaking Gitea's path
// resolution.
func pathEscapeSegments(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}
