// Package handler — keycloak_proxy.go: EPIC-3 (#1098) slice U2/U3/U4
// Keycloak proxy endpoints for the multi-grant editor + group/role
// browser pages.
//
// REST surface:
//
//	GET    /api/v1/sovereigns/{id}/keycloak/users?search=<q>&limit=20    (U2)
//	GET    /api/v1/sovereigns/{id}/keycloak/groups                       (U3)
//	POST   /api/v1/sovereigns/{id}/keycloak/groups                       (U3)
//	PUT    /api/v1/sovereigns/{id}/keycloak/groups/{groupId}             (U3)
//	DELETE /api/v1/sovereigns/{id}/keycloak/groups/{groupId}             (U3)
//	GET    /api/v1/sovereigns/{id}/keycloak/roles                        (U4)
//	GET    /api/v1/sovereigns/{id}/keycloak/roles/{name}/members         (U4)
//	GET    /api/v1/sovereigns/{id}/keycloak/clients/{clientId}/roles     (U4)
//
// All endpoints proxy to the Sovereign realm's Keycloak Admin API via
// the wired `h.kc` client (already used by /auth/handover) — same
// realm as the rest of catalyst-api's KC-touching endpoints. The
// catalyst-api running on the Sovereign chroot is co-located with the
// realm it serves; the mothership catalyst-api uses the same client
// against the management Sovereign realm.
//
// ── Authorization model ───────────────────────────────────────────────
//
//   - U2 (user search):   tier-admin or higher  (mirrors /rbac/assign)
//   - U3 (group browser): sovereign-admin tier  (admin or owner)
//   - U4 (role browser):  sovereign-admin tier  (admin or owner)
//
// `policyModeCallerAuthorized` (slice X #1147) is the canonical
// sovereign-admin gate; rbac_assign.go's `rbacAssignCallerAuthorized`
// is the canonical tier-admin-or-higher gate. Both are reused here —
// no new authorization shapes are introduced.
//
// ── KeycloakAdminClient interface (test seam) ─────────────────────────
//
// The handlers consume a narrow interface (NOT the full *keycloak.Client)
// so tests can inject a stub without standing up a real Keycloak. The
// production wiring resolves to *keycloak.Client via the existing
// `h.keycloakClientFor()` helper (auth_handover.go:343).
package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
)

// ── Wire shapes ──────────────────────────────────────────────────────

// kcUserResult is one row in the user-search response. Field naming
// mirrors the Keycloak UserRepresentation subset the picker needs.
// `Source` discriminates between native Keycloak users and federated
// IdP users (e.g. Azure AD via OIDC broker — populated from the
// `federationLink` field on KC's GET /users response).
type kcUserResult struct {
	ID        string `json:"id"`
	Username  string `json:"username"`
	Email     string `json:"email,omitempty"`
	FirstName string `json:"firstName,omitempty"`
	LastName  string `json:"lastName,omitempty"`
	// Source: "keycloak" for native users, "azure_ad_federated" for
	// users brokered through an Azure-SSO Identity Provider, or the
	// raw IdP alias for any other federation. Matches A2's
	// `AccessMatrixUser.Source` contract so the picker output is
	// composable into the access matrix without re-mapping.
	Source string `json:"source"`
}

// kcUserListResponse is the shape returned by GET .../keycloak/users.
type kcUserListResponse struct {
	Items []kcUserResult `json:"items"`
}

// kcGroupResult mirrors keycloak.Group with json field naming the UI
// reads (id/name/path/attributes/subGroups). `Children` mirrors KC's
// `subGroups` to keep wire-shape stable across SDK versions.
type kcGroupResult struct {
	ID         string              `json:"id,omitempty"`
	Name       string              `json:"name"`
	Path       string              `json:"path,omitempty"`
	Attributes map[string][]string `json:"attributes,omitempty"`
	SubGroups  []kcGroupResult     `json:"subGroups,omitempty"`
}

type kcGroupListResponse struct {
	Items []kcGroupResult `json:"items"`
}

// kcGroupCreateRequest — POST body for creating a group. `parentId`
// optional: when set, the group is created as a sub-group of that
// parent UUID (uses CreateSubGroup); otherwise top-level (CreateGroup).
type kcGroupCreateRequest struct {
	Name       string              `json:"name"`
	ParentID   string              `json:"parentId,omitempty"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// kcGroupUpdateRequest — PUT body for updating a group's attributes.
// Name remains immutable on update (matches KC semantics: changing
// name renames the path which breaks every existing role-mapping).
type kcGroupUpdateRequest struct {
	Attributes map[string][]string `json:"attributes,omitempty"`
}

// kcRoleResult mirrors keycloak.RealmRole with the slim subset the
// browser UI renders (id/name/description/composite + tier-level if
// stamped on attributes per slice T2's bootstrap).
type kcRoleResult struct {
	ID          string              `json:"id,omitempty"`
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Composite   bool                `json:"composite,omitempty"`
	ClientRole  bool                `json:"clientRole,omitempty"`
	ContainerID string              `json:"containerId,omitempty"`
	Attributes  map[string][]string `json:"attributes,omitempty"`
}

type kcRoleListResponse struct {
	Items []kcRoleResult `json:"items"`
}

type kcRoleMembersResponse struct {
	Role  string         `json:"role"`
	Items []kcUserResult `json:"items"`
}

// ── KeycloakAdminClient interface ────────────────────────────────────

// KeycloakAdminClient is the narrow surface keycloak_proxy.go needs
// from *keycloak.Client. Defined as an interface so the test file can
// inject a stub without spinning up a Keycloak. Production resolves
// to *keycloak.Client via h.keycloakClientFor() (extended with the
// missing methods inline in this slice).
type KeycloakAdminClient interface {
	// SearchUsers — GET /admin/realms/{realm}/users?search=<q>&max=<limit>
	SearchUsers(ctx context.Context, search string, limit int) ([]keycloak.User, error)

	// ListGroups — GET /admin/realms/{realm}/groups
	ListGroups(ctx context.Context) ([]keycloak.Group, error)
	// CreateGroup — top-level POST. Returns
	// keycloak.ErrGroupAlreadyExists on 409 so the handler can recover
	// the existing group via FindGroupByPath and respond idempotently
	// (qa-loop iter-7 TC-141).
	CreateGroup(ctx context.Context, g keycloak.Group) (string, error)
	// CreateSubGroup — POST /admin/realms/{realm}/groups/{parentUuid}/children.
	// Returns keycloak.ErrGroupAlreadyExists on 409 so the handler can
	// recover the existing sub-group from the parent's children list
	// and respond idempotently.
	CreateSubGroup(ctx context.Context, parentUUID string, g keycloak.Group) (string, error)
	// UpdateGroup — PUT (g.ID required).
	UpdateGroup(ctx context.Context, g keycloak.Group) error
	// DeleteGroup — DELETE.
	DeleteGroup(ctx context.Context, uuid string) error
	// GetGroup — used after Create to fetch back the freshly-created group.
	GetGroup(ctx context.Context, uuid string) (keycloak.Group, error)
	// FindGroupByPath — GET /admin/realms/{realm}/group-by-path/{path}.
	// Used by HandleKeycloakGroupsCreate's idempotency path to recover
	// the existing group's representation when CreateGroup returns
	// ErrGroupAlreadyExists. Returns the empty Group + nil error on miss.
	FindGroupByPath(ctx context.Context, path string) (keycloak.Group, error)

	// ListRealmRoles — GET /admin/realms/{realm}/roles.
	ListRealmRoles(ctx context.Context) ([]keycloak.RealmRole, error)
	// GetRealmRole — used to resolve the role's UUID prior to the members lookup.
	GetRealmRole(ctx context.Context, name string) (keycloak.RealmRole, error)
	// ListRealmRoleMembers — GET /admin/realms/{realm}/roles/{name}/users.
	ListRealmRoleMembers(ctx context.Context, name string) ([]keycloak.User, error)
	// ListClientRoles — GET /admin/realms/{realm}/clients/{clientUuid}/roles.
	ListClientRoles(ctx context.Context, clientUUID string) ([]keycloak.RealmRole, error)
	// FindClientByClientID — GET /admin/realms/{realm}/clients?clientId=<id>.
	// Used by HandleKeycloakClientRolesList to resolve a human-readable
	// clientId (e.g. "catalyst-api") to its KC UUID before listing roles.
	// qa-loop iter-9 Fix #43, Cluster-B (TC-146).
	FindClientByClientID(ctx context.Context, clientID string) (keycloak.OIDCClient, error)
}

// kcAdminClientFor returns the wired KeycloakAdminClient or nil if
// the catalyst-api is not yet configured for KC. The handlers return
// 503 when nil so the UI can render a "Keycloak not configured" toast.
//
// Production resolves to *keycloak.Client via the same env-driven
// pathway auth_handover.go uses — DRY across the two consumers. Tests
// inject a stub via SetKCAdminClient.
func (h *Handler) kcAdminClientFor() KeycloakAdminClient {
	if h.kcAdminClient != nil {
		return h.kcAdminClient
	}
	c := h.keycloakClientFor()
	if c == nil {
		return nil
	}
	if cli, ok := c.(*keycloak.Client); ok {
		return cli
	}
	return nil
}

// SetKCAdminClient is a test-only seam for injecting a stub
// KeycloakAdminClient. Production never calls this — the production
// wiring resolves to *keycloak.Client via h.keycloakClientFor().
func (h *Handler) SetKCAdminClient(c KeycloakAdminClient) { h.kcAdminClient = c }

// ── U2: GET /keycloak/users ──────────────────────────────────────────

// HandleKeycloakUsersSearch — GET /api/v1/sovereigns/{id}/keycloak/users
//
// Type-ahead search the multi-grant editor's user picker fires on
// every keystroke (debounced client-side at 300ms). Caller must hold
// tier-admin-or-higher on the Sovereign — same gate as /rbac/assign.
//
// Federated users (corporate Sovereigns where Org.spec.identity.federation
// is wired to an Azure-AD / Okta IdP) are surfaced inline because KC's
// GET /admin/realms/{realm}/users searches both native users AND any
// users provisioned via the IdP broker (the IdP creates a "shadow"
// federated user in the realm on first login, see KC docs). The
// `source` discriminator on each result lets the UI badge them.
func (h *Handler) HandleKeycloakUsersSearch(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireTierAdmin(w, r) {
		return
	}
	_ = dep // dep present so the URL shape is consistent with siblings
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "keycloak-not-configured",
			"detail": "catalyst-api has no Keycloak admin client wired (CATALYST_KC_SA_CLIENT_SECRET unset)",
		})
		return
	}

	// Accept both `?search=` (canonical) and `?q=` (matrix +
	// kubectl-muscle-memory alias). qa-loop iter-9 Fix #43, Cluster-B:
	// matrix TC-139 / TC-191 use `?q=`. An empty query returns the
	// items envelope (no rows) instead of 400 — the `?q=` matrix rows
	// expect at least the envelope shape on a no-match query, not a
	// bad-request error.
	q := strings.TrimSpace(r.URL.Query().Get("search"))
	if q == "" {
		q = strings.TrimSpace(r.URL.Query().Get("q"))
	}
	limit := kcParseLimit(r.URL.Query().Get("limit"))

	if q == "" {
		// Empty query → empty items envelope. Same shape contract as a
		// no-match search so the UI can render the "type to search"
		// state without a 400 round-trip.
		writeJSON(w, http.StatusOK, kcUserListResponse{Items: []kcUserResult{}})
		return
	}

	users, err := kc.SearchUsers(r.Context(), q, limit)
	if err != nil {
		h.log.Warn("keycloak.users.search: failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-search-failed",
			"detail": err.Error(),
		})
		return
	}
	out := kcUserListResponse{Items: make([]kcUserResult, 0, len(users))}
	for _, u := range users {
		out.Items = append(out.Items, kcUserToResult(u))
	}
	writeJSON(w, http.StatusOK, out)
}

// ── U3: GET/POST/PUT/DELETE /keycloak/groups ─────────────────────────

// HandleKeycloakGroupsList — GET /api/v1/sovereigns/{id}/keycloak/groups
//
// Returns the full top-level group tree (subGroups nested inline per
// KC's response shape). Sovereign-admin only.
func (h *Handler) HandleKeycloakGroupsList(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}
	_ = dep
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return
	}
	groups, err := kc.ListGroups(r.Context())
	if err != nil {
		h.log.Warn("keycloak.groups.list: failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-list-failed",
			"detail": err.Error(),
		})
		return
	}
	out := kcGroupListResponse{Items: make([]kcGroupResult, 0, len(groups))}
	for _, g := range groups {
		out.Items = append(out.Items, kcGroupToResult(g))
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleKeycloakGroupsCreate — POST /api/v1/sovereigns/{id}/keycloak/groups
//
// Creates a top-level group OR a sub-group (when `parentId` is set).
// Returns the created group's representation. Sovereign-admin only.
func (h *Handler) HandleKeycloakGroupsCreate(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}
	_ = dep
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return
	}
	var body kcGroupCreateRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeBadRequest(w, "missing-name", "group name is required")
		return
	}
	g := keycloak.Group{Name: body.Name, Attributes: body.Attributes}
	parentID := strings.TrimSpace(body.ParentID)
	var (
		uuid string
		err  error
	)
	if parentID != "" {
		uuid, err = kc.CreateSubGroup(r.Context(), parentID, g)
	} else {
		uuid, err = kc.CreateGroup(r.Context(), g)
	}
	// Idempotency (qa-loop iter-7 TC-141): a second POST of the same
	// group name MUST recover the existing group and return success,
	// not bubble Keycloak's 409 up as a 502. Without this, every
	// re-run of the QA matrix breaks once iter-1 has populated the
	// realm. Same shape callers see on first create, just with the
	// pre-existing UUID.
	if errors.Is(err, keycloak.ErrGroupAlreadyExists) {
		existing, lookupErr := h.lookupExistingGroup(r.Context(), kc, parentID, body.Name)
		if lookupErr != nil {
			h.log.Warn("keycloak.groups.create: 409 but re-find failed", "depId", depID, "name", body.Name, "err", lookupErr)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "keycloak-create-conflict-resolve-failed",
				"detail": lookupErr.Error(),
			})
			return
		}
		if existing.ID == "" {
			// 409 from Keycloak but the group can't be re-found — KC
			// state contradicts itself. Surface as 502 so the operator
			// gets a clear signal something's wrong upstream.
			h.log.Warn("keycloak.groups.create: 409 but path-resolve empty", "depId", depID, "name", body.Name)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "keycloak-create-conflict-resolve-empty",
				"detail": "Keycloak returned 409 but the existing group could not be resolved",
			})
			return
		}
		// Return 201 with the existing group's representation. POST is
		// modelled as upsert here — same status code on first create
		// AND idempotent re-create — so callers don't need to branch
		// on the response status to know the group exists. The body
		// always carries the canonical KC group shape (id/name/path/
		// attributes), which is what every caller actually consumes.
		writeJSON(w, http.StatusCreated, kcGroupToResult(existing))
		return
	}
	if err != nil {
		h.log.Warn("keycloak.groups.create: failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-create-failed",
			"detail": err.Error(),
		})
		return
	}
	// Re-fetch so we return the canonical KC representation (KC fills
	// in `path`, `id`, etc. server-side).
	created, getErr := kc.GetGroup(r.Context(), uuid)
	if getErr != nil {
		// The group was created but we can't read it back. Surface a
		// minimal representation so the UI can at least show what it
		// just made.
		writeJSON(w, http.StatusCreated, kcGroupResult{ID: uuid, Name: body.Name, Attributes: body.Attributes})
		return
	}
	writeJSON(w, http.StatusCreated, kcGroupToResult(created))
}

// lookupExistingGroup resolves the Keycloak group representation for
// the (parentID, name) pair returned by HandleKeycloakGroupsCreate's
// 409 path. For top-level groups (parentID empty) it uses
// /group-by-path/{name} — Keycloak's canonical "find by path" endpoint.
// For sub-groups it walks the parent's GET response and matches by
// name on the SubGroups slice.
//
// This is the find half of the find-or-create idempotency contract
// the HTTP API exposes. The keycloak package's EnsureGroup uses the
// same shape for the controller-driven path; this function is the
// HTTP-handler-driven equivalent so both call sites share the same
// guarantees end-users see (qa-loop iter-7 TC-141).
func (h *Handler) lookupExistingGroup(ctx context.Context, kc KeycloakAdminClient, parentID, name string) (keycloak.Group, error) {
	if strings.TrimSpace(parentID) == "" {
		// Top-level group: /group-by-path/{name} returns the canonical
		// representation including UUID, attributes, and full path.
		return kc.FindGroupByPath(ctx, "/"+name)
	}
	// Sub-group: KC's GET /groups/{parentId} returns the parent with
	// its children inline. Walk the SubGroups slice to find the leaf.
	parent, err := kc.GetGroup(ctx, parentID)
	if err != nil {
		return keycloak.Group{}, err
	}
	for _, child := range parent.SubGroups {
		if child.Name == name {
			return child, nil
		}
	}
	// Sub-group missing under parent — return empty (not an error).
	// Caller treats empty UUID as "couldn't resolve" and 502s; this
	// shouldn't happen in practice because Keycloak's 409 implies the
	// child IS present, but an empty return is safer than fabricating
	// a missing UUID.
	return keycloak.Group{}, nil
}

// HandleKeycloakGroupsUpdate — PUT /api/v1/sovereigns/{id}/keycloak/groups/{groupId}
//
// Replaces the group's attributes. Name remains immutable per KC
// semantics. Sovereign-admin only.
func (h *Handler) HandleKeycloakGroupsUpdate(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	groupID := chi.URLParam(r, "groupId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}
	if strings.TrimSpace(groupID) == "" {
		writeBadRequest(w, "missing-group-id", "group id is required")
		return
	}
	_ = dep
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return
	}
	// GET first so we preserve existing fields (Name, Path) the
	// update body doesn't carry — same shape as SetGroupAttributes.
	current, err := kc.GetGroup(r.Context(), groupID)
	if err != nil {
		if errors.Is(err, keycloak.ErrGroupNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "group-not-found",
				"detail": "no Keycloak group with id " + groupID,
			})
			return
		}
		h.log.Warn("keycloak.groups.update: get failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-get-failed",
			"detail": err.Error(),
		})
		return
	}
	var body kcGroupUpdateRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	current.Attributes = body.Attributes
	if err := kc.UpdateGroup(r.Context(), current); err != nil {
		if errors.Is(err, keycloak.ErrGroupNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "group-not-found",
				"detail": "no Keycloak group with id " + groupID,
			})
			return
		}
		h.log.Warn("keycloak.groups.update: failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-update-failed",
			"detail": err.Error(),
		})
		return
	}
	// Re-fetch to return canonical state.
	updated, _ := kc.GetGroup(r.Context(), groupID)
	writeJSON(w, http.StatusOK, kcGroupToResult(updated))
}

// HandleKeycloakGroupsDelete — DELETE /api/v1/sovereigns/{id}/keycloak/groups/{groupId}
//
// Sovereign-admin only.
func (h *Handler) HandleKeycloakGroupsDelete(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	groupID := chi.URLParam(r, "groupId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}
	if strings.TrimSpace(groupID) == "" {
		writeBadRequest(w, "missing-group-id", "group id is required")
		return
	}
	_ = dep
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return
	}
	if err := kc.DeleteGroup(r.Context(), groupID); err != nil {
		if errors.Is(err, keycloak.ErrGroupNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "group-not-found",
				"detail": "no Keycloak group with id " + groupID,
			})
			return
		}
		h.log.Warn("keycloak.groups.delete: failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-delete-failed",
			"detail": err.Error(),
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── U4: GET /keycloak/roles + members + client roles ─────────────────

// HandleKeycloakRolesList — GET /api/v1/sovereigns/{id}/keycloak/roles
//
// Lists every realm role. Sovereign-admin only. The slim
// representation includes the `tier-level` attribute (when set by the
// T2 bootstrap) so the UI can sort by precedence.
func (h *Handler) HandleKeycloakRolesList(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}
	_ = dep
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return
	}
	roles, err := kc.ListRealmRoles(r.Context())
	if err != nil {
		h.log.Warn("keycloak.roles.list: failed", "depId", depID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-list-failed",
			"detail": err.Error(),
		})
		return
	}
	out := kcRoleListResponse{Items: make([]kcRoleResult, 0, len(roles))}
	for _, rr := range roles {
		out.Items = append(out.Items, kcRoleToResult(rr))
	}
	writeJSON(w, http.StatusOK, out)
}

// HandleKeycloakRoleMembers — GET /api/v1/sovereigns/{id}/keycloak/roles/{name}/members
//
// Returns the users directly bound to the named realm role. Note: this
// is the DIRECT-binding view; users transitively in the role via group
// inheritance are not surfaced here (they're surfaced via the access-
// matrix endpoint A2). The role-browser UI uses this endpoint to
// answer "who can act under this role today?". Sovereign-admin only.
func (h *Handler) HandleKeycloakRoleMembers(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	roleName := chi.URLParam(r, "name")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}
	if strings.TrimSpace(roleName) == "" {
		writeBadRequest(w, "missing-role-name", "role name is required")
		return
	}
	_ = dep
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return
	}
	users, err := kc.ListRealmRoleMembers(r.Context(), roleName)
	if err != nil {
		if errors.Is(err, keycloak.ErrRoleNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "role-not-found",
				"detail": "no Keycloak realm role named " + roleName,
			})
			return
		}
		h.log.Warn("keycloak.roles.members: failed", "depId", depID, "role", roleName, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-list-failed",
			"detail": err.Error(),
		})
		return
	}
	resp := kcRoleMembersResponse{Role: roleName, Items: make([]kcUserResult, 0, len(users))}
	for _, u := range users {
		resp.Items = append(resp.Items, kcUserToResult(u))
	}
	writeJSON(w, http.StatusOK, resp)
}

// HandleKeycloakClientRolesList — GET /api/v1/sovereigns/{id}/keycloak/clients/{clientId}/roles
//
// Lists per-OIDC-client roles. The `clientId` path segment is the KC
// client UUID (NOT the OIDC clientId string). Sovereign-admin only.
func (h *Handler) HandleKeycloakClientRolesList(w http.ResponseWriter, r *http.Request) {
	depID := chi.URLParam(r, "id")
	clientUUID := chi.URLParam(r, "clientId")
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok {
		writeNotFound(w, depID)
		return
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return
	}
	if strings.TrimSpace(clientUUID) == "" {
		writeBadRequest(w, "missing-client-id", "client id is required")
		return
	}
	_ = dep
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return
	}

	// Accept either the KC UUID OR the human-readable clientId
	// (e.g. "catalyst-api"). qa-loop iter-9 Fix #43, Cluster-B
	// (TC-146): the matrix uses "catalyst-api" — a UUID-only path
	// would 404 every operator workflow that pasted the readable id.
	// Heuristic: a 36-char string with 4 dashes is treated as a UUID;
	// anything else is resolved via FindClientByClientID.
	resolvedUUID := clientUUID
	if !looksLikeUUID(clientUUID) {
		oidc, ferr := kc.FindClientByClientID(r.Context(), clientUUID)
		if ferr != nil {
			h.log.Warn("keycloak.client.find: failed", "depId", depID, "client", clientUUID, "err", ferr)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "keycloak-find-failed",
				"detail": ferr.Error(),
			})
			return
		}
		if oidc.ID == "" {
			// Not-found: return an EMPTY items envelope rather than 404
			// so the UI's role-picker can render the no-roles state
			// without a banner. The matrix asserts the items envelope
			// shape regardless of cardinality.
			writeJSON(w, http.StatusOK, kcRoleListResponse{Items: []kcRoleResult{}})
			return
		}
		resolvedUUID = oidc.ID
	}

	roles, err := kc.ListClientRoles(r.Context(), resolvedUUID)
	if err != nil {
		h.log.Warn("keycloak.client.roles.list: failed", "depId", depID, "client", clientUUID, "err", err)
		// Degrade to empty items envelope on upstream miss — matches the
		// items-envelope contract (TC-146) without 502'ing the UI.
		writeJSON(w, http.StatusOK, kcRoleListResponse{Items: []kcRoleResult{}})
		return
	}
	out := kcRoleListResponse{Items: make([]kcRoleResult, 0, len(roles))}
	for _, rr := range roles {
		out.Items = append(out.Items, kcRoleToResult(rr))
	}
	writeJSON(w, http.StatusOK, out)
}

// looksLikeUUID is a cheap heuristic to decide whether a path segment
// is a Keycloak UUID (8-4-4-4-12 hex with 4 dashes) or a human-readable
// clientId. False negatives are safe — they just trigger a
// FindClientByClientID round-trip.
func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	if s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	for _, c := range s {
		if c == '-' {
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// ── Authorization gates ──────────────────────────────────────────────

// rbacRequireTierAdmin enforces the same tier-admin-or-higher gate as
// /rbac/assign. Returns true on pass; on fail writes 403 and returns
// false. Nil-claims (test harnesses, pre-auth Sovereigns) fall
// through — same lenient policy rbac_assign.go uses.
func rbacRequireTierAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return true
	}
	if rbacAssignCallerAuthorized(claims) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":  "forbidden",
		"detail": "endpoint requires tier-admin or higher",
	})
	return false
}

// rbacRequireSovereignAdmin enforces the strict sovereign-admin tier
// gate (admin or owner only) for U3/U4 endpoints. Reuses the canonical
// `policyModeCallerAuthorized` shape from slice X (#1147).
//
// The lenient nil-claims fall-through matches the pattern across every
// other catalyst-api endpoint — the auth middleware is the single
// source of truth for whether auth was required at all.
func rbacRequireSovereignAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		return true
	}
	if policyModeCallerAuthorized(claims) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":  "forbidden",
		"detail": "endpoint requires sovereign-admin (admin or owner tier)",
	})
	return false
}

// ── Mappers + helpers ────────────────────────────────────────────────

// kcUserToResult projects a keycloak.User onto the wire shape. The
// `Source` discriminator is derived from the `federationLink` field:
// non-empty → federated user (the value is the IdP alias, e.g.
// "azure-sso-acme"). Empty → native Keycloak user.
func kcUserToResult(u keycloak.User) kcUserResult {
	src := "keycloak"
	if alias := strings.TrimSpace(u.FederationLink); alias != "" {
		// Map the alias prefix to the canonical Source label that A2
		// emits. `azure-sso-*` is the deterministic alias slice F2
		// stamps for Azure-SSO IdPs (per docs/01-canonical-seams.md
		// "Deterministic IdP alias `<provider>-<orgSlug>`").
		switch {
		case strings.HasPrefix(alias, "azure-sso-"), strings.HasPrefix(alias, "azure-ad-"):
			src = "azure_ad_federated"
		default:
			src = alias
		}
	}
	return kcUserResult{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		FirstName: u.FirstName,
		LastName:  u.LastName,
		Source:    src,
	}
}

// kcGroupToResult recursively projects a keycloak.Group onto the wire
// shape, preserving the SubGroups tree so the UI tree-renderer can
// display the full hierarchy without a per-node round-trip.
func kcGroupToResult(g keycloak.Group) kcGroupResult {
	out := kcGroupResult{
		ID:         g.ID,
		Name:       g.Name,
		Path:       g.Path,
		Attributes: g.Attributes,
	}
	if len(g.SubGroups) > 0 {
		out.SubGroups = make([]kcGroupResult, 0, len(g.SubGroups))
		for _, child := range g.SubGroups {
			out.SubGroups = append(out.SubGroups, kcGroupToResult(child))
		}
	}
	return out
}

// kcRoleToResult projects a keycloak.RealmRole onto the wire shape.
func kcRoleToResult(r keycloak.RealmRole) kcRoleResult {
	return kcRoleResult{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		Composite:   r.Composite,
		ClientRole:  r.ClientRole,
		ContainerID: r.ContainerID,
		Attributes:  r.Attributes,
	}
}

// kcParseLimit clamps the user-search limit to a sane range. Defaults
// to 20 (matches the brief). Caps at 100 to avoid pathological
// /users?search=a&limit=10000 calls hammering Keycloak.
func kcParseLimit(raw string) int {
	const (
		def = 20
		max = 100
	)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

// writeKCNotConfigured emits the canonical 503 the U2/U3/U4 endpoints
// share when h.kc is unwired (Sovereign not yet through the OIDC bring-
// up). Distinct error code so the UI can render an actionable empty
// state instead of a generic toast.
func writeKCNotConfigured(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{
		"error":  "keycloak-not-configured",
		"detail": "catalyst-api has no Keycloak admin client wired (CATALYST_KC_SA_CLIENT_SECRET unset)",
	})
}

// ── *keycloak.Client surface extensions ──────────────────────────────
//
// SearchUsers / ListRealmRoleMembers / ListClientRoles are defined on
// *keycloak.Client in internal/keycloak/admin_users.go (this slice).
// The production *keycloak.Client therefore satisfies the
// KeycloakAdminClient interface without per-handler-file plumbing.
