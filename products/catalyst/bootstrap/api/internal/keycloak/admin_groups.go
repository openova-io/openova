package keycloak

// admin_groups.go — Keycloak group CRUD + attribute setters.
//
// Slice D1c of EPIC-0 #1095. organization-controller (slice C1) calls
// these to materialize a Keycloak group per Organization. The group's
// attributes carry the Catalyst custom claims (`org`, `tier`,
// `openova_scopes`) that the auth/Claims fields parse on every token —
// see slice D2's auth/session.go and the Keycloak group-mapper config
// in platform/keycloak/chart/templates/configmap-sovereign-realm.yaml.
//
// Endpoints used (Keycloak Admin REST API, version 24.x):
//
//   GET    /admin/realms/{realm}/groups
//   GET    /admin/realms/{realm}/groups/{group-uuid}
//   POST   /admin/realms/{realm}/groups
//   PUT    /admin/realms/{realm}/groups/{group-uuid}
//   DELETE /admin/realms/{realm}/groups/{group-uuid}
//   POST   /admin/realms/{realm}/groups/{parent-uuid}/children   (sub-groups)
//
// The existing client.go has unexported findGroupByName + createGroup +
// addUserToGroup — those remain in service of EnsureUser and are not
// touched here. This file adds the public Group CRUD surface for the
// reconciler use case.

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

// ErrGroupNotFound is returned by GetGroup / DeleteGroup on 404.
var ErrGroupNotFound = errors.New("keycloak: group not found")

// errGroupAlreadyExists is the internal sentinel for the EnsureGroup
// 409 race path.
var errGroupAlreadyExists = errors.New("keycloak: group already exists")

// Group is the slice of fields catalyst-api consumes/sets when
// reconciling per-Org groups. Mirrors the upstream GroupRepresentation
// but only with the fields organization-controller actually uses.
//
// Attributes is the Keycloak-side join key for Catalyst RBAC: a per-Org
// group typically carries `org=<slug>`, `tier=<viewer|developer|...>`,
// and one or more `openova_scope=<key=value>` entries. The Keycloak
// realm's group-mapper renders these into the access token's `org`,
// `tier`, and `openova_scopes` claims (parsed by auth/Claims in slice D2).
type Group struct {
	// ID is the Keycloak-internal UUID. Empty before CreateGroup.
	ID string `json:"id,omitempty"`

	// Name is the leaf group name (e.g. "acme", "acme-admins"). For
	// sub-groups, the parent context is implicit in the API path.
	Name string `json:"name"`

	// Path is the full Keycloak path with leading slash and slashes
	// between sub-group levels (e.g. "/acme/admins"). Read-only —
	// Keycloak populates this on read and ignores it on write.
	Path string `json:"path,omitempty"`

	// Attributes is a map of group attributes. Keycloak supports
	// multi-value attributes — Catalyst uses single-value semantics
	// for `org` and `tier`, multi-value for `openova_scope`.
	Attributes map[string][]string `json:"attributes,omitempty"`

	// SubGroups is populated on read for parent groups. Catalyst
	// rarely uses nested groups in practice but the data shape is
	// preserved so the access-matrix UI can render the hierarchy.
	SubGroups []Group `json:"subGroups,omitempty"`

	// RealmRoles is the legacy per-group realm-role list (Keycloak
	// returns this when the group has direct role bindings via the
	// pre-v24 API). For new code prefer ListGroupRealmRoles in
	// admin_roles.go which uses the canonical role-mappings endpoint.
	RealmRoles []string `json:"realmRoles,omitempty"`
}

// ListGroups returns all groups in the realm. The Keycloak Admin API
// returns top-level groups only; sub-groups appear under each parent's
// SubGroups field. Used by access-matrix UI to render the per-Org
// group inventory.
//
// Pagination: realms with >1000 groups would need /count + paginated
// reads, but Catalyst caps at 1 group per Organization per Sovereign,
// so practical realms stay well under that bound.
func (c *Client) ListGroups(ctx context.Context) ([]Group, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("keycloak.ListGroups: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/groups?first=0&max=1000", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keycloak: GET groups: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keycloak: GET groups %d: %s", resp.StatusCode, body)
	}
	var groups []Group
	if err := json.Unmarshal(body, &groups); err != nil {
		return nil, fmt.Errorf("keycloak: decode groups: %w", err)
	}
	return groups, nil
}

// GetGroup looks up a group by Keycloak UUID. Returns ErrGroupNotFound
// on 404 so reconciliation loops can branch on absence.
func (c *Client) GetGroup(ctx context.Context, uuid string) (Group, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return Group{}, fmt.Errorf("keycloak.GetGroup: service account token: %w", err)
	}

	u := fmt.Sprintf("%s/admin/realms/%s/groups/%s", c.addr, c.realm, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Group{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return Group{}, fmt.Errorf("keycloak: GET group: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusNotFound {
		return Group{}, ErrGroupNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return Group{}, fmt.Errorf("keycloak: GET group %d: %s", resp.StatusCode, body)
	}
	var g Group
	if err := json.Unmarshal(body, &g); err != nil {
		return Group{}, fmt.Errorf("keycloak: decode group: %w", err)
	}
	return g, nil
}

// FindGroupByPath resolves a group by its full path (e.g. "/acme" or
// "/acme/admins") to a Group representation including the UUID. Returns
// the empty struct + nil error on miss, mirroring FindClientByClientID.
//
// The Keycloak Admin API exposes /group-by-path for this exactly.
func (c *Client) FindGroupByPath(ctx context.Context, path string) (Group, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return Group{}, fmt.Errorf("keycloak.FindGroupByPath: service account token: %w", err)
	}

	// Keycloak's /group-by-path expects the path WITHOUT URL-escaping
	// the leading slash (it's part of the path segment, not a separator).
	// Normalize to ensure exactly one leading slash, then percent-encode
	// the remaining segments.
	if path == "" {
		return Group{}, errors.New("keycloak.FindGroupByPath: empty path")
	}
	if path[0] != '/' {
		path = "/" + path
	}

	u := fmt.Sprintf("%s/admin/realms/%s/group-by-path/%s",
		c.addr, c.realm, url.PathEscape(path[1:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Group{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return Group{}, fmt.Errorf("keycloak: GET group-by-path: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var g Group
		if err := json.Unmarshal(body, &g); err != nil {
			return Group{}, fmt.Errorf("keycloak: decode group-by-path: %w", err)
		}
		return g, nil
	case http.StatusNotFound:
		return Group{}, nil
	default:
		return Group{}, fmt.Errorf("keycloak: GET group-by-path %d: %s", resp.StatusCode, body)
	}
}

// CreateGroup creates a top-level group. Returns the new UUID extracted
// from the Location header (mirrors CreateClient).
//
// On 409 returns errGroupAlreadyExists so EnsureGroup can re-find via
// FindGroupByPath.
func (c *Client) CreateGroup(ctx context.Context, g Group) (string, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return "", fmt.Errorf("keycloak.CreateGroup: service account token: %w", err)
	}
	return c.createGroupV2(ctx, saToken, g)
}

func (c *Client) createGroupV2(ctx context.Context, saToken string, g Group) (string, error) {
	body, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/groups", c.addr, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: POST group: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", errors.New("keycloak: POST group 201 without Location header")
		}
		if seg := lastSegment(loc); seg != "" {
			return seg, nil
		}
		return "", fmt.Errorf("keycloak: POST group 201 Location parse failed: %s", loc)
	case http.StatusConflict:
		return "", errGroupAlreadyExists
	default:
		return "", fmt.Errorf("keycloak: POST group %d: %s", resp.StatusCode, respBody)
	}
}

// CreateSubGroup creates a child group under the given parent UUID.
// Catalyst uses this rarely (typical Org-mapping is flat) but
// access-matrix UI supports rendering hierarchical groups so the
// API is surfaced for completeness.
func (c *Client) CreateSubGroup(ctx context.Context, parentUUID string, g Group) (string, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return "", fmt.Errorf("keycloak.CreateSubGroup: service account token: %w", err)
	}

	body, err := json.Marshal(g)
	if err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/groups/%s/children",
		c.addr, c.realm, url.PathEscape(parentUUID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak: POST sub-group: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated:
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", errors.New("keycloak: POST sub-group 201 without Location header")
		}
		if seg := lastSegment(loc); seg != "" {
			return seg, nil
		}
		return "", fmt.Errorf("keycloak: POST sub-group 201 Location parse failed: %s", loc)
	case http.StatusConflict:
		return "", errGroupAlreadyExists
	default:
		return "", fmt.Errorf("keycloak: POST sub-group %d: %s", resp.StatusCode, respBody)
	}
}

// UpdateGroup replaces the group representation. Used by SetGroupAttributes
// internally. Caller must populate g.ID with the Keycloak UUID.
func (c *Client) UpdateGroup(ctx context.Context, g Group) error {
	if g.ID == "" {
		return errors.New("keycloak.UpdateGroup: g.ID (Keycloak UUID) is required")
	}
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.UpdateGroup: service account token: %w", err)
	}
	body, err := json.Marshal(g)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/groups/%s",
		c.addr, c.realm, url.PathEscape(g.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT group: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrGroupNotFound
	default:
		return fmt.Errorf("keycloak: PUT group %d: %s", resp.StatusCode, respBody)
	}
}

// DeleteGroup removes a group by UUID. Returns ErrGroupNotFound on 404.
func (c *Client) DeleteGroup(ctx context.Context, uuid string) error {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return fmt.Errorf("keycloak.DeleteGroup: service account token: %w", err)
	}
	u := fmt.Sprintf("%s/admin/realms/%s/groups/%s", c.addr, c.realm, url.PathEscape(uuid))
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: DELETE group: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrGroupNotFound
	default:
		return fmt.Errorf("keycloak: DELETE group %d: %s", resp.StatusCode, respBody)
	}
}

// EnsureGroup is the find-or-create shorthand organization-controller
// (slice C1) calls per Organization. Returns the existing group when
// path resolves; otherwise creates and re-finds. The 409-race path
// re-finds via FindGroupByPath.
//
// The first parameter is the full path the caller wants to ensure
// (e.g. "/acme"); the Group's Name is derived from the path's last
// segment if not set explicitly.
func (c *Client) EnsureGroup(ctx context.Context, path string, attrs map[string][]string) (Group, error) {
	saToken, err := c.serviceAccountToken(ctx)
	if err != nil {
		return Group{}, fmt.Errorf("keycloak.EnsureGroup: service account token: %w", err)
	}

	if path == "" {
		return Group{}, errors.New("keycloak.EnsureGroup: empty path")
	}
	if path[0] != '/' {
		path = "/" + path
	}

	// The leaf segment becomes the group's Name.
	leaf := path
	if i := lastSlashIndex(path); i >= 0 && i < len(path)-1 {
		leaf = path[i+1:]
	}

	existing, err := c.findGroupByPathInternal(ctx, saToken, path)
	if err != nil {
		return Group{}, fmt.Errorf("keycloak.EnsureGroup: find: %w", err)
	}
	if existing.ID != "" {
		// Already exists. If attrs differ, update; otherwise return as-is.
		if !attributesEqual(existing.Attributes, attrs) && attrs != nil {
			existing.Attributes = attrs
			if err := c.updateGroupInternal(ctx, saToken, existing); err != nil {
				return Group{}, fmt.Errorf("keycloak.EnsureGroup: update attrs: %w", err)
			}
		}
		return existing, nil
	}

	uuid, err := c.createGroupV2(ctx, saToken, Group{Name: leaf, Attributes: attrs})
	if errors.Is(err, errGroupAlreadyExists) {
		// 409 race — re-find.
		existing, ferr := c.findGroupByPathInternal(ctx, saToken, path)
		if ferr != nil {
			return Group{}, fmt.Errorf("keycloak.EnsureGroup: re-find after 409: %w", ferr)
		}
		if existing.ID == "" {
			return Group{}, errors.New("keycloak.EnsureGroup: 409 conflict but path-resolve returned empty")
		}
		return existing, nil
	}
	if err != nil {
		return Group{}, fmt.Errorf("keycloak.EnsureGroup: create: %w", err)
	}

	created, ferr := c.findGroupByPathInternal(ctx, saToken, path)
	if ferr != nil {
		return Group{}, fmt.Errorf("keycloak.EnsureGroup: re-find after create: %w", ferr)
	}
	if created.ID == "" {
		// Defensive — extremely unlikely.
		return Group{ID: uuid, Name: leaf, Attributes: attrs}, nil
	}
	return created, nil
}

// SetGroupAttributes is a convenience wrapper that GETs the group, sets
// the attribute map, and PUTs. Per the Keycloak Admin API the attributes
// field is a full replace (not a merge), so callers must merge themselves
// if they want to preserve existing attributes — this helper takes the
// caller's map as authoritative.
func (c *Client) SetGroupAttributes(ctx context.Context, uuid string, attrs map[string][]string) error {
	g, err := c.GetGroup(ctx, uuid)
	if err != nil {
		return fmt.Errorf("keycloak.SetGroupAttributes: get: %w", err)
	}
	g.Attributes = attrs
	if err := c.UpdateGroup(ctx, g); err != nil {
		return fmt.Errorf("keycloak.SetGroupAttributes: update: %w", err)
	}
	return nil
}

// ── internal helpers ────────────────────────────────────────────────────

func (c *Client) findGroupByPathInternal(ctx context.Context, saToken, path string) (Group, error) {
	if path == "" {
		return Group{}, errors.New("findGroupByPathInternal: empty path")
	}
	if path[0] != '/' {
		path = "/" + path
	}
	u := fmt.Sprintf("%s/admin/realms/%s/group-by-path/%s",
		c.addr, c.realm, url.PathEscape(path[1:]))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Group{}, err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return Group{}, fmt.Errorf("keycloak: GET group-by-path: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		var g Group
		if err := json.Unmarshal(body, &g); err != nil {
			return Group{}, fmt.Errorf("keycloak: decode group-by-path: %w", err)
		}
		return g, nil
	case http.StatusNotFound:
		return Group{}, nil
	default:
		return Group{}, fmt.Errorf("keycloak: GET group-by-path %d: %s", resp.StatusCode, body)
	}
}

func (c *Client) updateGroupInternal(ctx context.Context, saToken string, g Group) error {
	body, err := json.Marshal(g)
	if err != nil {
		return err
	}
	u := fmt.Sprintf("%s/admin/realms/%s/groups/%s",
		c.addr, c.realm, url.PathEscape(g.ID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+saToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("keycloak: PUT group: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrGroupNotFound
	default:
		return fmt.Errorf("keycloak: PUT group %d: %s", resp.StatusCode, respBody)
	}
}

// lastSlashIndex returns the index of the last `/` in s, or -1 if none.
func lastSlashIndex(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// attributesEqual compares two attribute maps for set-equality (length
// + key-by-key value-slice equality). Used by EnsureGroup to detect
// drift before issuing an UPDATE call.
func attributesEqual(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || len(va) != len(vb) {
			return false
		}
		for i := range va {
			if va[i] != vb[i] {
				return false
			}
		}
	}
	return true
}
