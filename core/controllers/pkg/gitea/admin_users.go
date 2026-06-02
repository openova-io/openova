// admin_users.go — admin-only Gitea client extensions used by the
// per-Org IaC repo bootstrap pipeline (ADR-0009 + G117.3 / W2.C3).
//
// The IaC repo bootstrap needs four operations the base client never
// surfaced because no Wave-0/Wave-1 controller had a reason to call
// them:
//
//  1. Create a per-Org robot user (`<org>-iac-bot`) via the admin API.
//  2. Mint a personal access token for that robot user so external
//     callers (catalyst-api at PR open time) can author commits with
//     repo-scoped credentials.
//  3. Add the robot user as a write-permission collaborator on the
//     `<org>/iac` repo.
//  4. Enable branch protection on `main` requiring the three named
//     status checks (kyverno-admission, cert-manager-precheck,
//     dns-conflict-precheck).
//
// The matching delete operations are also exposed so the finalizer
// can unwind the per-Org footprint when an Organization tombstones.
//
// All methods are idempotent on the happy path:
//
//   - CreateAdminUser swallows 422 (user already exists) and returns
//     the existing user.
//   - CreateUserAccessToken returns the existing token name on 422 —
//     the caller MUST persist the freshly-minted plaintext token at
//     first-create (Gitea never re-emits a token plaintext after it
//     has been generated). The 422 path is therefore "already minted
//     by a prior run; the token plaintext should already be in
//     OpenBao".
//   - AddCollaborator swallows 204 (no-op) and 422 (already-added).
//   - CreateBranchProtection swallows 422 / 409 — the named rule
//     already exists.
//
// Per the Gitea v1.22 API reference, the four endpoints are:
//
//	POST   /api/v1/admin/users
//	POST   /api/v1/users/{username}/tokens
//	PUT    /api/v1/repos/{owner}/{repo}/collaborators/{collaborator}
//	POST   /api/v1/repos/{owner}/{repo}/branch_protections
//	DELETE /api/v1/admin/users/{username}
//	DELETE /api/v1/repos/{owner}/{repo}/collaborators/{collaborator}
//	DELETE /api/v1/repos/{owner}/{repo}/branch_protections/{name}

package gitea

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// AdminUser is the slice of Gitea User fields the bootstrap path needs.
type AdminUser struct {
	ID       int64  `json:"id,omitempty"`
	Username string `json:"username,omitempty"`
	Email    string `json:"email,omitempty"`
	FullName string `json:"full_name,omitempty"`
}

// adminUserCreate is the payload for POST /admin/users.
type adminUserCreate struct {
	Username           string `json:"username"`
	Email              string `json:"email"`
	Password           string `json:"password"`
	LoginName          string `json:"login_name,omitempty"`
	MustChangePassword bool   `json:"must_change_password"`
	SourceID           int64  `json:"source_id"`
}

// AccessToken is the response for POST /users/{username}/tokens.
//
// Gitea ONLY ever returns the plaintext `sha1` field on the create
// response — subsequent GETs return only the masked metadata. Callers
// MUST persist `Sha1` to OpenBao (or another secret store) on first
// create; if they don't, the only recovery is to delete + re-mint.
type AccessToken struct {
	ID             int64    `json:"id,omitempty"`
	Name           string   `json:"name,omitempty"`
	Sha1           string   `json:"sha1,omitempty"`
	TokenLastEight string   `json:"token_last_eight,omitempty"`
	Scopes         []string `json:"scopes,omitempty"`
}

// accessTokenCreate is the payload for POST /users/{username}/tokens.
type accessTokenCreate struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes,omitempty"`
}

// branchProtection is the slice of Gitea BranchProtection fields the
// bootstrap path manages.
type branchProtection struct {
	BranchName             string   `json:"branch_name"`
	EnableStatusCheck      bool     `json:"enable_status_check"`
	StatusCheckContexts    []string `json:"status_check_contexts,omitempty"`
	RequireSignedCommits   bool     `json:"require_signed_commits"`
	BlockOnRejectedReviews bool     `json:"block_on_rejected_reviews"`
}

// collaboratorPermission is the payload for PUT
// /repos/{owner}/{repo}/collaborators/{user}.
type collaboratorPermission struct {
	Permission string `json:"permission"`
}

// CreateAdminUser provisions a Gitea user via the admin endpoint and
// returns the user. Idempotent: a 422 (user already exists) is treated
// as success — the caller then proceeds to token mint + collaborator
// add against the existing user.
//
// Per ADR-0009 the robot user's email is constructed by the caller as
// `<username>@gitea.<sov-fqdn>` so the address is deterministic and
// non-routable.
func (c *Client) CreateAdminUser(ctx context.Context, username, email, password string) (AdminUser, error) {
	if username == "" || email == "" || password == "" {
		return AdminUser{}, errors.New("gitea: CreateAdminUser requires non-empty username, email, password")
	}
	body := adminUserCreate{
		Username:           username,
		Email:              email,
		Password:           password,
		LoginName:          username,
		MustChangePassword: false,
		SourceID:           0,
	}
	var out AdminUser
	status, _, err := c.do(ctx, http.MethodPost, "/admin/users", body, &out)
	if err == nil {
		return out, nil
	}
	// 422 = already exists. Idempotent re-find via GET /users/{u}.
	if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
		existing, ferr := c.GetUser(ctx, username)
		if ferr == nil {
			return existing, nil
		}
		return AdminUser{}, fmt.Errorf("gitea.CreateAdminUser: re-find after 422/409: %w", ferr)
	}
	return AdminUser{}, fmt.Errorf("gitea.CreateAdminUser: %w", err)
}

// GetUser fetches a Gitea user by login name. Returns
// ErrUserNotFound on 404.
func (c *Client) GetUser(ctx context.Context, username string) (AdminUser, error) {
	if username == "" {
		return AdminUser{}, errors.New("gitea: username must be non-empty")
	}
	var out AdminUser
	status, _, err := c.do(ctx, http.MethodGet, "/users/"+url.PathEscape(username), nil, &out)
	if err != nil {
		if status == http.StatusNotFound {
			return AdminUser{}, ErrUserNotFound
		}
		return AdminUser{}, err
	}
	return out, nil
}

// DeleteAdminUser removes the user via the admin endpoint. Idempotent:
// a 404 is treated as success (already gone).
//
// The `purge` query parameter is passed as `?purge=true` so Gitea
// cascades the user's repos / tokens / collaborator rows in one
// transaction. The bootstrap path's robot user has no other artifacts
// so purge is safe.
func (c *Client) DeleteAdminUser(ctx context.Context, username string) error {
	if username == "" {
		return errors.New("gitea: DeleteAdminUser requires non-empty username")
	}
	endpoint := "/admin/users/" + url.PathEscape(username) + "?purge=true"
	status, _, err := c.do(ctx, http.MethodDelete, endpoint, nil, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("gitea.DeleteAdminUser: %w", err)
}

// CreateUserAccessToken mints a personal-access token for the named
// user. Gitea ONLY returns the plaintext `Sha1` on the create response
// — the caller MUST persist it on first create.
//
// On 422 (token name already exists) the function returns
// ErrAccessTokenExists; the caller is then responsible for choosing
// between "this is a normal rotation, delete + re-mint" or "the
// plaintext is already in OpenBao, skip".
func (c *Client) CreateUserAccessToken(ctx context.Context, username, tokenName string, scopes []string) (AccessToken, error) {
	if username == "" || tokenName == "" {
		return AccessToken{}, errors.New("gitea: CreateUserAccessToken requires non-empty username, tokenName")
	}
	endpoint := fmt.Sprintf("/users/%s/tokens", url.PathEscape(username))
	body := accessTokenCreate{Name: tokenName, Scopes: scopes}
	var out AccessToken
	status, _, err := c.do(ctx, http.MethodPost, endpoint, body, &out)
	if err == nil {
		return out, nil
	}
	if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
		return AccessToken{}, ErrAccessTokenExists
	}
	if status == http.StatusNotFound {
		return AccessToken{}, ErrUserNotFound
	}
	return AccessToken{}, fmt.Errorf("gitea.CreateUserAccessToken: %w", err)
}

// DeleteUserAccessToken removes a token by name. Idempotent: a 404
// returns nil. Used by the rotation path: delete-then-recreate gives
// us a fresh plaintext to persist back to OpenBao.
func (c *Client) DeleteUserAccessToken(ctx context.Context, username, tokenName string) error {
	if username == "" || tokenName == "" {
		return errors.New("gitea: DeleteUserAccessToken requires non-empty username, tokenName")
	}
	endpoint := fmt.Sprintf("/users/%s/tokens/%s",
		url.PathEscape(username), url.PathEscape(tokenName))
	status, _, err := c.do(ctx, http.MethodDelete, endpoint, nil, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("gitea.DeleteUserAccessToken: %w", err)
}

// AddCollaborator adds `user` as a collaborator on `org/repo` with the
// supplied permission (typically "write"). Idempotent: a 204 response
// (Gitea's "no-op, already a collaborator at this perm") and 422 race
// both return nil.
func (c *Client) AddCollaborator(ctx context.Context, org, repo, user, permission string) error {
	if org == "" || repo == "" || user == "" {
		return errors.New("gitea: AddCollaborator requires non-empty org, repo, user")
	}
	if permission == "" {
		permission = "write"
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/collaborators/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(user))
	body := collaboratorPermission{Permission: permission}
	status, _, err := c.do(ctx, http.MethodPut, endpoint, body, nil)
	if err == nil {
		return nil
	}
	// 204 = success-no-content; the do() helper would return success
	// in that path, so this branch is for non-2xx outcomes only. 422
	// = race; 409 = already exists (rare).
	if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("gitea.AddCollaborator: %w", err)
}

// RemoveCollaborator removes `user` from `org/repo`. Idempotent: a 404
// returns nil.
func (c *Client) RemoveCollaborator(ctx context.Context, org, repo, user string) error {
	if org == "" || repo == "" || user == "" {
		return errors.New("gitea: RemoveCollaborator requires non-empty org, repo, user")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/collaborators/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(user))
	status, _, err := c.do(ctx, http.MethodDelete, endpoint, nil, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("gitea.RemoveCollaborator: %w", err)
}

// EnsureBranchProtection creates (or finds) the named-branch protection
// rule on `org/repo`. The three locked status-check contexts come from
// ADR-0009 §Branch-protection: kyverno-admission, cert-manager-precheck,
// dns-conflict-precheck. The function is idempotent — a 422 / 409 on
// create is treated as success.
//
// Per the ADR the rule's branch_name MUST equal "main" — we don't
// expose it as a parameter to discourage drift.
func (c *Client) EnsureBranchProtection(ctx context.Context, org, repo string, statusCheckContexts []string) error {
	if org == "" || repo == "" {
		return errors.New("gitea: EnsureBranchProtection requires non-empty org, repo")
	}
	if len(statusCheckContexts) == 0 {
		return errors.New("gitea: EnsureBranchProtection requires at least one status-check context")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/branch_protections",
		url.PathEscape(org), url.PathEscape(repo))
	body := branchProtection{
		BranchName:             "main",
		EnableStatusCheck:      true,
		StatusCheckContexts:    statusCheckContexts,
		RequireSignedCommits:   false,
		BlockOnRejectedReviews: false,
	}
	status, _, err := c.do(ctx, http.MethodPost, endpoint, body, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("gitea.EnsureBranchProtection: %w", err)
}

// DeleteBranchProtection removes the named-branch protection rule.
// Idempotent: a 404 returns nil.
func (c *Client) DeleteBranchProtection(ctx context.Context, org, repo, branchName string) error {
	if org == "" || repo == "" || branchName == "" {
		return errors.New("gitea: DeleteBranchProtection requires non-empty org, repo, branchName")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/branch_protections/%s",
		url.PathEscape(org), url.PathEscape(repo), url.PathEscape(branchName))
	status, _, err := c.do(ctx, http.MethodDelete, endpoint, nil, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("gitea.DeleteBranchProtection: %w", err)
}

// DeleteRepo removes `org/repo`. Idempotent: a 404 returns nil.
//
// Used by the finalizer when an Organization tombstones. The deletion
// is non-reversible (Gitea has no soft-delete) so the caller MUST
// confirm Org-tombstone intent before invoking.
func (c *Client) DeleteRepo(ctx context.Context, org, repo string) error {
	if org == "" || repo == "" {
		return errors.New("gitea: DeleteRepo requires non-empty org, repo")
	}
	endpoint := fmt.Sprintf("/repos/%s/%s",
		url.PathEscape(org), url.PathEscape(repo))
	status, _, err := c.do(ctx, http.MethodDelete, endpoint, nil, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("gitea.DeleteRepo: %w", err)
}

// trimToken returns the first 8 chars of a token plaintext for safe
// logging — used by callers that want to emit "minted token sha1=abcd1234…"
// without leaking the full secret. Exported as a small helper so all
// log statements are routed through one place.
func trimToken(plaintext string) string {
	plaintext = strings.TrimSpace(plaintext)
	if len(plaintext) <= 8 {
		return plaintext
	}
	return plaintext[:8] + "…"
}
