package keycloak

// admin_roles.go — Keycloak realm-role + role-mapping CRUD.
//
// Slice D1b of EPIC-0 #1095. useraccess-controller (slice C5) calls
// these to materialize the 5 catalog tier roles (viewer / developer /
// operator / admin / owner) per Sovereign realm at startup, and to
// bind realm roles to per-Organization Keycloak groups so a user's
// `groups` claim resolves to the catalog tier via Keycloak's group→role
// inheritance.
//
// Endpoints used (Keycloak Admin REST API, version 24.x):
//
//   Realm roles:
//   GET    /admin/realms/{realm}/roles
//   GET    /admin/realms/{realm}/roles/{role-name}
//   POST   /admin/realms/{realm}/roles
//   PUT    /admin/realms/{realm}/roles/{role-name}
//   DELETE /admin/realms/{realm}/roles/{role-name}
//
//   User role-mappings:
//   GET    /admin/realms/{realm}/users/{user-uuid}/role-mappings/realm
//   POST   /admin/realms/{realm}/users/{user-uuid}/role-mappings/realm
//   DELETE /admin/realms/{realm}/users/{user-uuid}/role-mappings/realm
//
//   Group role-mappings:
//   GET    /admin/realms/{realm}/groups/{group-uuid}/role-mappings/realm
//   POST   /admin/realms/{realm}/groups/{group-uuid}/role-mappings/realm
//   DELETE /admin/realms/{realm}/groups/{group-uuid}/role-mappings/realm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// ErrRoleNotFound is returned by GetRealmRole / DeleteRealmRole when
// the role name does not resolve. Mirrors ErrClientNotFound.
var ErrRoleNotFound = errors.New("keycloak: realm role not found")

// errRoleAlreadyExists is the internal sentinel for the 409 path on
// CreateRealmRole. EnsureRealmRole catches it and re-finds.
var errRoleAlreadyExists = errors.New("keycloak: realm role already exists")

// RealmRole is the slice of fields catalyst-api consumes/sets when
// reconciling the catalog tier roles. Mirrors the upstream
// RoleRepresentation but only with the fields useraccess-controller
// needs.
type RealmRole struct {
	// ID is the Keycloak-internal UUID. Empty before CreateRealmRole.
	ID string `json:"id,omitempty"`

	// Name is the role name (e.g. "catalyst-admin", "catalyst-developer").
	// Catalyst's convention is to prefix with `catalyst-` so realm-role
	// lookup is unambiguous when other Keycloak features create their
	// own (e.g. "uma_authorization", "default-roles-sovereign").
	Name string `json:"name"`

	// Description is free-form prose for the Keycloak admin UI.
	Description string `json:"description,omitempty"`

	// Composite = true means this role inherits from one or more other
	// realm roles. Catalyst tier hierarchy uses this: `developer`
	// composes `viewer`, `operator` composes `developer`, etc. so a
	// user assigned `admin` automatically gets `developer` and
	// `viewer` access via Keycloak's role-mapping resolver.
	Composite bool `json:"composite,omitempty"`

	// ClientRole = false for realm roles. Catalyst stays at realm scope
	// for the 5 tier roles; per-OIDC-client roles are a future tier.
	ClientRole bool `json:"clientRole,omitempty"`

	// ContainerID is the realm UUID for realm roles. Keycloak populates
	// this on read; we never set it on write.
	ContainerID string `json:"containerId,omitempty"`

	// Attributes is a map of role attributes (Keycloak v24+). Catalyst
	// uses the `tier-level` attribute to encode the integer ordering
	// (viewer=10, developer=20, operator=30, admin=40, owner=50) so
	// the access-matrix UI (EPIC-3 #1098) can sort tiers without a
	// hardcoded list.
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// ListRealmRoles returns every realm role. Used by access-matrix UI.
// Realm role count is bounded (5 tier roles + per-Application roles
// the application-controller creates), so no pagination is needed.
func (c *Client) ListRealmRoles(ctx context.Context) ([]RealmRole, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.ListRealmRoles: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/roles", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET roles: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET roles %d: %s", resp.StatusCode, body)
	}
	var roles []RealmRole
	if err := json.Unmarshal(body, &roles); err != nil {
		return nil, fmt.Errorf("keycloak: decode roles: %w", err)
	}
	return roles, nil
}

// GetRealmRole looks up a realm role by name. Returns ErrRoleNotFound
// on 404 so reconciliation loops can branch on absence.
func (c *Client) GetRealmRole(ctx context.Context, name string) (RealmRole, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return RealmRole{}, fmt.Errorf("keycloak.GetRealmRole: service account token: %w", err)
	}
	return c.getRealmRole(ctx, saToken, name)
}

func (c *Client) getRealmRole(ctx context.Context, saToken, name string) (RealmRole, error) {
	u := fmt.Sprintf("%s/admin/realms/%s/roles/%s",
		c.addr, c.realm, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return RealmRole{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return RealmRole{}, fmt.Errorf("keycloak: GET role: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return RealmRole{}, ErrRoleNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return RealmRole{}, fmt.Errorf("keycloak: GET role %d: %s", resp.StatusCode, body)
	}
	var rr RealmRole
	if err := json.Unmarshal(body, &rr); err != nil {
		return RealmRole{}, fmt.Errorf("keycloak: decode role: %w", err)
	}
	return rr, nil
}

// CreateRealmRole creates a realm role. The Keycloak API returns 201
// with no body and no Location header for role creation (unlike clients);
// the caller must follow up with GetRealmRole if it needs the UUID.
//
// On 409 returns errRoleAlreadyExists so EnsureRealmRole can re-find.
func (c *Client) CreateRealmRole(ctx context.Context, rr RealmRole) error {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.CreateRealmRole: service account token: %w", err)
	}
	return c.createRealmRole(ctx, saToken, rr)
}

func (c *Client) createRealmRole(ctx context.Context, saToken string, rr RealmRole) error {
	body, err := json.Marshal(rr)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/roles", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: POST role: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusCreated, http.StatusNoContent:
		return nil
	case http.StatusConflict:
		return errRoleAlreadyExists
	default:
		return fmt.Errorf("keycloak: POST role %d: %s", resp.StatusCode, respBody)
	}
}

// UpdateRealmRole replaces a role's representation. The role is identified
// by name (path segment) — Keycloak's PUT /roles/{role-name} replaces the
// full body. Caller must call GetRealmRole first, mutate, then UpdateRealmRole.
func (c *Client) UpdateRealmRole(ctx context.Context, name string, rr RealmRole) error {
	if name == "" {
		return errors.New("keycloak.UpdateRealmRole: name is required")
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.UpdateRealmRole: service account token: %w", err)
	}
	body, err := json.Marshal(rr)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/roles/%s",
		c.addr, c.realm, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT role: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrRoleNotFound
	default:
		return fmt.Errorf("keycloak: PUT role %d: %s", resp.StatusCode, respBody)
	}
}

// DeleteRealmRole removes a realm role by name. Returns ErrRoleNotFound
// on 404 so reconciliation loops can treat absence-as-success.
func (c *Client) DeleteRealmRole(ctx context.Context, name string) error {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.DeleteRealmRole: service account token: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s/roles/%s",
		c.addr, c.realm, url.PathEscape(name))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: DELETE role: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrRoleNotFound
	default:
		return fmt.Errorf("keycloak: DELETE role %d: %s", resp.StatusCode, respBody)
	}
}

// EnsureRealmRole is the find-or-create shorthand useraccess-controller
// (slice C5) calls per catalog tier role at startup. If the role exists
// the existing representation is returned (so the caller can compare
// composite/attributes and call UpdateRealmRole if drift is detected);
// otherwise CreateRealmRole runs and a fresh GetRealmRole follows.
func (c *Client) EnsureRealmRole(ctx context.Context, rr RealmRole) (RealmRole, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return RealmRole{}, fmt.Errorf("keycloak.EnsureRealmRole: service account token: %w", err)
	}

	existing, err := c.getRealmRole(ctx, saToken, rr.Name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, ErrRoleNotFound) {
		return RealmRole{}, fmt.Errorf("keycloak.EnsureRealmRole: get: %w", err)
	}

	if err := c.createRealmRole(ctx, saToken, rr); err != nil {
		if !errors.Is(err, errRoleAlreadyExists) {
			return RealmRole{}, fmt.Errorf("keycloak.EnsureRealmRole: create: %w", err)
		}
		// 409 race — fall through to the GET below.
	}
	created, err := c.getRealmRole(ctx, saToken, rr.Name)
	if err != nil {
		return RealmRole{}, fmt.Errorf("keycloak.EnsureRealmRole: re-get after create: %w", err)
	}
	return created, nil
}

// ── Role mappings ──────────────────────────────────────────────────────

// ListUserRealmRoles returns the realm roles directly assigned to a user
// (NOT including transitively-inherited roles via group membership).
// Use ListUserEffectiveRealmRoles for the full effective set.
func (c *Client) ListUserRealmRoles(ctx context.Context, userUUID string) ([]RealmRole, error) {
	return c.listRoleMappingsRealm(ctx, "users", userUUID, false)
}

// ListUserEffectiveRealmRoles returns the realm roles a user effectively
// has — direct + group-inherited + composite-expanded. This is what
// the /token endpoint embeds in `realm_access.roles` claim.
func (c *Client) ListUserEffectiveRealmRoles(ctx context.Context, userUUID string) ([]RealmRole, error) {
	return c.listRoleMappingsRealm(ctx, "users", userUUID, true)
}

// AssignUserRealmRoles binds the given realm roles to a user. The role
// list is additive — Keycloak's POST role-mappings/realm semantics.
func (c *Client) AssignUserRealmRoles(ctx context.Context, userUUID string, roles []RealmRole) error {
	return c.postRoleMappingsRealm(ctx, "users", userUUID, roles)
}

// UnassignUserRealmRoles removes the given realm roles from a user.
func (c *Client) UnassignUserRealmRoles(ctx context.Context, userUUID string, roles []RealmRole) error {
	return c.deleteRoleMappingsRealm(ctx, "users", userUUID, roles)
}

// ListGroupRealmRoles returns the realm roles directly assigned to a
// group. Used by access-matrix UI to render the per-Org tier mapping.
func (c *Client) ListGroupRealmRoles(ctx context.Context, groupUUID string) ([]RealmRole, error) {
	return c.listRoleMappingsRealm(ctx, "groups", groupUUID, false)
}

// AssignGroupRealmRoles binds realm roles to a Keycloak group.
// useraccess-controller calls this after creating the per-Org group:
// every member of the group inherits the bound tier role.
func (c *Client) AssignGroupRealmRoles(ctx context.Context, groupUUID string, roles []RealmRole) error {
	return c.postRoleMappingsRealm(ctx, "groups", groupUUID, roles)
}

// UnassignGroupRealmRoles removes realm roles from a group.
func (c *Client) UnassignGroupRealmRoles(ctx context.Context, groupUUID string, roles []RealmRole) error {
	return c.deleteRoleMappingsRealm(ctx, "groups", groupUUID, roles)
}

// listRoleMappingsRealm is the shared GET helper for both user and group
// role-mapping reads. resourceKind is "users" or "groups"; effective=true
// switches to the /composite endpoint Keycloak exposes for transitive
// resolution.
func (c *Client) listRoleMappingsRealm(ctx context.Context, resourceKind, uuid string, effective bool) ([]RealmRole, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.listRoleMappingsRealm: service account token: %w", err)
	}
	suffix := ""
	if effective {
		suffix = "/composite"
	}
	u := fmt.Sprintf("%s/admin/realms/%s/%s/%s/role-mappings/realm%s",
		c.addr, c.realm, resourceKind, url.PathEscape(uuid), suffix)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET role-mappings: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET role-mappings %d: %s", resp.StatusCode, body)
	}
	var roles []RealmRole
	if err := json.Unmarshal(body, &roles); err != nil {
		return nil, fmt.Errorf("keycloak: decode role-mappings: %w", err)
	}
	return roles, nil
}

// postRoleMappingsRealm assigns realm roles to a user or group. Both
// the user and group endpoints accept a JSON array of RoleRepresentation
// objects; the empty list is a no-op (Keycloak returns 204 immediately).
func (c *Client) postRoleMappingsRealm(ctx context.Context, resourceKind, uuid string, roles []RealmRole) error {
	if len(roles) == 0 {
		return nil
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.postRoleMappingsRealm: service account token: %w", err)
	}
	body, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/%s/%s/role-mappings/realm",
		c.addr, c.realm, resourceKind, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: POST role-mappings: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusCreated:
		return nil
	default:
		return fmt.Errorf("keycloak: POST role-mappings %d: %s", resp.StatusCode, respBody)
	}
}

// deleteRoleMappingsRealm removes realm roles from a user or group.
// DELETE with a JSON body is unusual but standard for this Keycloak
// endpoint. Empty list is a no-op.
func (c *Client) deleteRoleMappingsRealm(ctx context.Context, resourceKind, uuid string, roles []RealmRole) error {
	if len(roles) == 0 {
		return nil
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.deleteRoleMappingsRealm: service account token: %w", err)
	}
	body, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/%s/%s/role-mappings/realm",
		c.addr, c.realm, resourceKind, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: DELETE role-mappings: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return fmt.Errorf("keycloak: DELETE role-mappings %d: %s", resp.StatusCode, respBody)
	}
}
