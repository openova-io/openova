package keycloak

// admin_users.go — Keycloak user search + role-membership lookups.
//
// EPIC-3 (#1098) slice U2/U4 surface: the multi-grant editor's user
// picker (U2) calls SearchUsers; the realm-role browser (U4) calls
// ListRealmRoleMembers + ListClientRoles. The pre-existing
// findUserByEmail in client.go (single exact-email lookup for
// EnsureUser / /auth/handover) is kept untouched — it serves a
// different contract (find-or-create one specific user) and shouldn't
// be conflated with the type-ahead search surface.
//
// Endpoints used (Keycloak Admin REST API, version 24.x):
//
//   GET /admin/realms/{realm}/users?search=<q>&max=<limit>&briefRepresentation=true
//   GET /admin/realms/{realm}/roles/{name}/users
//   GET /admin/realms/{realm}/clients/{clientUuid}/roles
//
// All three honour the same SA-token authentication pathway as the
// rest of the package via serviceAccountToken().

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// User is the slim subset of Keycloak's UserRepresentation that
// catalyst-api needs for the U2 picker + U4 role-members panel.
//
// FederationLink is the originating IdP alias for users provisioned
// via the broker (e.g. "azure-sso-acme" for an Azure-AD-federated
// user); empty for native Keycloak users. The proxy handler maps
// this to the canonical `source` discriminator in A2's contract.
type User struct {
	ID             string `json:"id"`
	Username       string `json:"username,omitempty"`
	Email          string `json:"email,omitempty"`
	FirstName      string `json:"firstName,omitempty"`
	LastName       string `json:"lastName,omitempty"`
	Enabled        bool   `json:"enabled,omitempty"`
	EmailVerified  bool   `json:"emailVerified,omitempty"`
	FederationLink string `json:"federationLink,omitempty"`
}

// SearchUsers proxies GET /admin/realms/{realm}/users?search=<q>&max=<limit>.
//
// KC's `search` parameter matches against username / email / firstName /
// lastName with a substring (LIKE) semantic. Returns up to `limit`
// users. `briefRepresentation=true` keeps the response compact —
// callers that need full attributes should follow up with GetUser.
//
// Federated IdP users are surfaced inline because KC's GET /users
// returns the union of native + federated users (the broker creates
// a "shadow" federated user on first login, with `federationLink`
// set to the IdP alias).
func (c *Client) SearchUsers(ctx context.Context, search string, limit int) ([]User, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.SearchUsers: service account token: %w", err)
	}
	if limit <= 0 {
		limit = 20
	}
	u := fmt.Sprintf("%s/admin/realms/%s/users?search=%s&max=%d&briefRepresentation=true",
		c.addr, c.realm, url.QueryEscape(search), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET users: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET users %d: %s", resp.StatusCode, body)
	}
	var users []User
	if err := json.Unmarshal(body, &users); err != nil {
		return nil, fmt.Errorf("keycloak: decode users: %w", err)
	}
	return users, nil
}

// ListRealmRoleMembers proxies GET /admin/realms/{realm}/roles/{name}/users.
//
// Returns the users DIRECTLY bound to the named realm role. Users
// transitively in the role via group membership are NOT included
// (that's the access-matrix endpoint A2's job). The role-browser UI
// uses this for "who can act under this role today?".
//
// Returns ErrRoleNotFound on 404 so the handler can surface a clean
// 404 to the caller.
func (c *Client) ListRealmRoleMembers(ctx context.Context, name string) ([]User, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.ListRealmRoleMembers: service account token: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s/roles/%s/users",
		c.addr, c.realm, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET role members: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrRoleNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET role members %d: %s", resp.StatusCode, body)
	}
	var users []User
	if err := json.Unmarshal(body, &users); err != nil {
		return nil, fmt.Errorf("keycloak: decode role members: %w", err)
	}
	return users, nil
}

// ListClientRoles proxies GET /admin/realms/{realm}/clients/{clientUuid}/roles.
//
// `clientUUID` is the Keycloak-internal UUID of the OIDC client (NOT
// the public clientId string). The U4 role-browser UI obtains this
// from the live ListClients call before drilling into per-client
// roles.
func (c *Client) ListClientRoles(ctx context.Context, clientUUID string) ([]RealmRole, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.ListClientRoles: service account token: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s/clients/%s/roles",
		c.addr, c.realm, url.PathEscape(clientUUID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET client roles: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET client roles %d: %s", resp.StatusCode, body)
	}
	var roles []RealmRole
	if err := json.Unmarshal(body, &roles); err != nil {
		return nil, fmt.Errorf("keycloak: decode client roles: %w", err)
	}
	return roles, nil
}
