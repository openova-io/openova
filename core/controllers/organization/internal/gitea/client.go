// Package gitea is a minimal Gitea Admin REST API client used by
// organization-controller (slice C1) to ensure a per-Organization Gitea
// Org exists. The client is intentionally narrow — only the surface
// the reconciler needs — and follows the exact idiom of
// products/catalyst/bootstrap/api/internal/keycloak/client.go:
//
//   - HTTP client supplied at New (tests inject a fake roundtripper)
//   - 200/201/204 success cases parsed explicitly
//   - 404 surfaces as a typed error sentinel
//   - 409 from "create" surfaces as a typed sentinel so callers can
//     re-find for idempotency
//
// Slice C2 (environment-controller) will likely need to write Gitea
// repos as well; if so the contract is captured here so the client can
// be extracted to core/pkg/gitea-client/ without an API change.
//
// Endpoints used (Gitea Admin API, version 1.22):
//
//	GET    /api/v1/orgs/{org}
//	POST   /api/v1/admin/orgs              (admin-only — full Org create)
//	GET    /api/v1/repos/{owner}/{repo}
//	POST   /api/v1/orgs/{org}/repos
//
// Authentication: a static admin access-token (the catalyst-api
// service-account token managed by Sovereign-admin) — passed via
// `Authorization: token <hex>` header per Gitea convention.
package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrOrgNotFound is returned by GetOrg when the slug does not resolve.
var ErrOrgNotFound = errors.New("gitea: organization not found")

// ErrRepoNotFound is returned by GetRepo when the slug does not resolve.
var ErrRepoNotFound = errors.New("gitea: repository not found")

// errOrgAlreadyExists is the internal sentinel for the EnsureOrg 422/409
// race path. Gitea returns 422 (not 409) on duplicate-name; we accept
// either for forward-compatibility.
var errOrgAlreadyExists = errors.New("gitea: organization already exists")

// errRepoAlreadyExists mirrors errOrgAlreadyExists for repos.
var errRepoAlreadyExists = errors.New("gitea: repository already exists")

// Client wraps the Gitea Admin REST API.
type Client struct {
	addr  string // e.g. "https://gitea.hfmp.openova.io"
	token string // admin personal-access token
	http  *http.Client
}

// New returns a Client with a 30s default timeout.
func New(addr, token string) *Client {
	return NewWithHTTP(addr, token, &http.Client{Timeout: 30 * time.Second})
}

// NewWithHTTP returns a Client using the supplied http.Client (tests
// inject a fake roundtripper).
func NewWithHTTP(addr, token string, hc *http.Client) *Client {
	return &Client{
		addr:  strings.TrimRight(addr, "/"),
		token: token,
		http:  hc,
	}
}

// Org is the slice of Gitea Organization fields organization-controller
// reads/writes. Gitea's full OrganizationRepresentation has dozens of
// fields; we surface only the few that drive create + status.
type Org struct {
	ID          int64  `json:"id,omitempty"`
	Username    string `json:"username,omitempty"` // the slug (Gitea calls it "username" for orgs)
	FullName    string `json:"full_name,omitempty"`
	Description string `json:"description,omitempty"`
	Visibility  string `json:"visibility,omitempty"` // public|limited|private
}

// adminOrgCreate is the payload for POST /admin/orgs which requires a
// `username` (the org slug) and an admin-supplied user owner. The
// catalyst-api service-account is the owner — it owns every Org until
// the reconciler explicitly transfers; future slices add owner
// re-bind once Keycloak federation lands.
type adminOrgCreate struct {
	Username    string `json:"username"`
	FullName    string `json:"full_name,omitempty"`
	Description string `json:"description,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
}

// Repo is the slice of Gitea Repository fields the controller reads/writes.
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
	u := fmt.Sprintf("%s/api/v1/orgs/%s", c.addr, slug)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Org{}, err
	}
	c.auth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return Org{}, fmt.Errorf("gitea: GET org: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var o Org
		if err := json.Unmarshal(body, &o); err != nil {
			return Org{}, fmt.Errorf("gitea: decode org: %w", err)
		}
		return o, nil
	case http.StatusNotFound:
		return Org{}, ErrOrgNotFound
	default:
		return Org{}, fmt.Errorf("gitea: GET org %d: %s", resp.StatusCode, body)
	}
}

// CreateOrg creates a Gitea Org via the admin endpoint. On 422/409 the
// internal errOrgAlreadyExists sentinel is returned so EnsureOrg can
// re-find idempotently.
func (c *Client) CreateOrg(ctx context.Context, slug, fullName, description, visibility string) (Org, error) {
	if visibility == "" {
		visibility = "private" // default — per-Org content stays private until explicit promotion
	}
	body, err := json.Marshal(adminOrgCreate{
		Username:    slug,
		FullName:    fullName,
		Description: description,
		Visibility:  visibility,
	})
	if err != nil {
		return Org{}, err
	}
	u := fmt.Sprintf("%s/api/v1/admin/orgs", c.addr)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return Org{}, err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Org{}, fmt.Errorf("gitea: POST org: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		var o Org
		if err := json.Unmarshal(respBody, &o); err != nil {
			return Org{}, fmt.Errorf("gitea: decode created org: %w", err)
		}
		return o, nil
	case http.StatusUnprocessableEntity, http.StatusConflict:
		// Gitea returns 422 on duplicate-username and 409 in some
		// historical versions; treat both as "already exists".
		return Org{}, errOrgAlreadyExists
	default:
		return Org{}, fmt.Errorf("gitea: POST org %d: %s", resp.StatusCode, respBody)
	}
}

// EnsureOrg is the find-or-create shorthand. Returns the Org with its
// numeric ID populated. Mirrors keycloak.EnsureGroup semantics — an
// existing Org's metadata is NOT mutated to match the desired
// fullName/description; the controller treats those as soft-attributes
// (operators rename via the Gitea UI without conflict).
func (c *Client) EnsureOrg(ctx context.Context, slug, fullName, description, visibility string) (Org, error) {
	existing, err := c.GetOrg(ctx, slug)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrOrgNotFound) {
		return Org{}, fmt.Errorf("gitea.EnsureOrg: get: %w", err)
	}

	created, err := c.CreateOrg(ctx, slug, fullName, description, visibility)
	if errors.Is(err, errOrgAlreadyExists) {
		// 422/409 race — re-find.
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
	u := fmt.Sprintf("%s/api/v1/repos/%s/%s", c.addr, owner, name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Repo{}, err
	}
	c.auth(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return Repo{}, fmt.Errorf("gitea: GET repo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusOK:
		var r Repo
		if err := json.Unmarshal(body, &r); err != nil {
			return Repo{}, fmt.Errorf("gitea: decode repo: %w", err)
		}
		return r, nil
	case http.StatusNotFound:
		return Repo{}, ErrRepoNotFound
	default:
		return Repo{}, fmt.Errorf("gitea: GET repo %d: %s", resp.StatusCode, body)
	}
}

// CreateRepo creates a repo under the given Org. autoInit=true makes
// Gitea seed an initial empty commit so the default branch exists.
func (c *Client) CreateRepo(ctx context.Context, org, name, description string, private bool, autoInit bool, defaultBranch string) (Repo, error) {
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	body, err := json.Marshal(repoCreate{
		Name:          name,
		Description:   description,
		Private:       private,
		AutoInit:      autoInit,
		DefaultBranch: defaultBranch,
	})
	if err != nil {
		return Repo{}, err
	}
	u := fmt.Sprintf("%s/api/v1/orgs/%s/repos", c.addr, org)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return Repo{}, err
	}
	c.auth(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Repo{}, fmt.Errorf("gitea: POST repo: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		var r Repo
		if err := json.Unmarshal(respBody, &r); err != nil {
			return Repo{}, fmt.Errorf("gitea: decode created repo: %w", err)
		}
		return r, nil
	case http.StatusUnprocessableEntity, http.StatusConflict:
		return Repo{}, errRepoAlreadyExists
	default:
		return Repo{}, fmt.Errorf("gitea: POST repo %d: %s", resp.StatusCode, respBody)
	}
}

// EnsureRepo is the find-or-create shorthand for repos.
func (c *Client) EnsureRepo(ctx context.Context, org, name, description string, private bool) (Repo, error) {
	existing, err := c.GetRepo(ctx, org, name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrRepoNotFound) {
		return Repo{}, fmt.Errorf("gitea.EnsureRepo: get: %w", err)
	}
	created, err := c.CreateRepo(ctx, org, name, description, private, true, "main")
	if errors.Is(err, errRepoAlreadyExists) {
		r, ferr := c.GetRepo(ctx, org, name)
		if ferr != nil {
			return Repo{}, fmt.Errorf("gitea.EnsureRepo: re-find after 422/409: %w", ferr)
		}
		return r, nil
	}
	if err != nil {
		return Repo{}, fmt.Errorf("gitea.EnsureRepo: create: %w", err)
	}
	return created, nil
}

// auth sets the Authorization header. Gitea accepts both
// `token <pat>` and `Bearer <pat>`; the canonical Gitea form is
// `token <pat>`.
func (c *Client) auth(r *http.Request) {
	r.Header.Set("Authorization", "token "+c.token)
	r.Header.Set("Accept", "application/json")
}
