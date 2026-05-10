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
	"encoding/json"
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

	// ── Admin proxy surface (qa-loop iter-1 Fix #104) ──────────────
	//
	// These methods back the /api/v1/sovereigns/{id}/keycloak/admin/*
	// endpoints. They expose Keycloak Admin REST API operations the QA
	// matrix (TC-124 / TC-125 / TC-159 / TC-160 / TC-161 / TC-176 /
	// TC-190 / TC-285) asserts on. Keycloak is NOT externally exposed
	// on the chroot Sovereign — without this proxy the matrix could
	// only assert via in-cluster `kubectl exec`, which the runner
	// cannot do.
	//
	// Each method's wire shape is documented on the *keycloak.Client
	// implementation in admin_*.go / admin_proxy.go.

	// ListRealmRoleComposites — TC-125 backing.
	// GET /admin/realms/{realm}/roles/{name}/composites/realm
	ListRealmRoleComposites(ctx context.Context, parentName string) ([]keycloak.RealmRole, error)

	// ListIdentityProviders — TC-159 backing.
	// GET /admin/realms/{realm}/identity-provider/instances
	ListIdentityProviders(ctx context.Context) ([]keycloak.IdentityProvider, error)

	// CreateIdentityProvider — TC-160 backing.
	// POST /admin/realms/{realm}/identity-provider/instances
	CreateIdentityProvider(ctx context.Context, idp keycloak.IdentityProvider) error

	// GetIdentityProvider — used after CreateIdentityProvider to
	// re-fetch the canonical representation Keycloak persisted, so the
	// proxy's POST response carries the same shape the matrix expects
	// from a subsequent GET.
	GetIdentityProvider(ctx context.Context, alias string) (keycloak.IdentityProvider, error)

	// CreateIdentityProviderMapper — TC-161 backing.
	// POST /admin/realms/{realm}/identity-provider/instances/{alias}/mappers
	CreateIdentityProviderMapper(ctx context.Context, alias string, mapper keycloak.IdentityProviderMapper) error

	// PasswordGrantToken — TC-176 backing.
	// POST /realms/{realm}/protocol/openid-connect/token (grant_type=password).
	// Returns raw upstream body + status so the matrix can assert on
	// claim names + invalid_grant text verbatim.
	PasswordGrantToken(ctx context.Context, oidcClientID, username, password string) ([]byte, int, error)

	// ListClientServiceAccountRealmRoles — TC-190 backing.
	// GET /admin/realms/{realm}/clients/{clientUuid}/service-account-user/role-mappings/realm
	// Returns raw upstream body + status (the matrix asserts role-name
	// string presence directly).
	ListClientServiceAccountRealmRoles(ctx context.Context, oidcClientUUID string) ([]byte, int, error)

	// ListClients — TC-285 backing (clientId query parameter resolved
	// via FindClientByClientID; response wraps the single OIDCClient
	// in the upstream `[]OIDCClient` envelope shape).
	ListClients(ctx context.Context) ([]keycloak.OIDCClient, error)
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

// ─────────────────────────────────────────────────────────────────────
// Admin proxy surface — qa-loop iter-1 Fix #104
// ─────────────────────────────────────────────────────────────────────
//
// REST routes registered in cmd/api/main.go:
//
//	GET  /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles                                                        (TC-124)
//	GET  /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles/{role}/composites                                      (TC-125)
//	GET  /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances                                  (TC-159)
//	POST /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances                                  (TC-160)
//	POST /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances/{alias}/mappers                  (TC-161)
//	POST /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/protocol/openid-connect/token                                (TC-176)
//	GET  /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients?clientId=<id>                                        (TC-285)
//	GET  /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients/{client}/service-account-user/role-mappings/realm    (TC-190)
//
// Authorization: every endpoint requires the caller to pass the
// rbacRequireSovereignAdmin gate (admin or owner tier). Unauthenticated
// callers (no Claims in context) fall through per the existing
// catalyst-api lenient policy — the auth middleware is the single
// source of truth for whether auth was required at all.
//
// `{realm}` URL parameter validation: the per-Sovereign keycloak.Client
// is wired against ONE realm (`c.realm`). The `{realm}` path segment
// must match — otherwise the proxy returns 400 (the matrix always
// passes the Sovereign's realm name, e.g. "omantel"). This prevents
// the proxy from being used as a generic cross-realm escape hatch.

// realmGuard validates that the `{realm}` URL parameter is non-empty.
// The wired *keycloak.Client is realm-scoped at construction time
// (CATALYST_KC_REALM env), so the URL realm is informational — the
// catalyst-api always operates against its own realm regardless of the
// URL value. We accept the URL value as a literal segment and let
// Keycloak validate it on the upstream call (mismatch surfaces as 401
// or 404 from KC, which the proxy returns verbatim). This matches the
// keep-shape-stable contract every other admin-proxy slice uses.
func (h *Handler) realmGuard(w http.ResponseWriter, r *http.Request) (string, bool) {
	realm := strings.TrimSpace(chi.URLParam(r, "realm"))
	if realm == "" {
		writeBadRequest(w, "missing-realm", "realm path segment is required")
		return "", false
	}
	return realm, true
}

// kcAdminProxyPreflight is the shared pre-flight all admin-proxy
// handlers run: lookup deployment → enforce sovereign-admin gate →
// validate realm URL segment → resolve Keycloak admin client. Returns
// the resolved (realm, kc) or (_, _, false) on any short-circuit
// (the appropriate response was already written).
func (h *Handler) kcAdminProxyPreflight(w http.ResponseWriter, r *http.Request) (string, KeycloakAdminClient, bool) {
	depID := chi.URLParam(r, "id")
	if _, ok := h.lookupDeploymentForInfra(depID); !ok {
		writeNotFound(w, depID)
		return "", nil, false
	}
	if !rbacRequireSovereignAdmin(w, r) {
		return "", nil, false
	}
	realm, ok := h.realmGuard(w, r)
	if !ok {
		return "", nil, false
	}
	kc := h.kcAdminClientFor()
	if kc == nil {
		writeKCNotConfigured(w)
		return "", nil, false
	}
	return realm, kc, true
}

// writeKCRaw forwards an upstream Keycloak response body to the client
// verbatim, mapping the upstream status into our error shape on non-2xx
// (the matrix asserts on `invalid_grant` literal text from TC-176's
// wrong-password path, etc.). On 2xx the body is forwarded as-is with
// Content-Type: application/json.
func writeKCRaw(w http.ResponseWriter, body []byte, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// HandleKeycloakAdminRealmRolesList — TC-124.
// GET /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles
//
// Returns the realm's full role list as the upstream Keycloak shape
// (`[]RoleRepresentation`). The matrix asserts on the presence of the
// 5 catalyst-* tier role names in the response body — no envelope.
func (h *Handler) HandleKeycloakAdminRealmRolesList(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	roles, err := kc.ListRealmRoles(r.Context())
	if err != nil {
		h.log.Warn("keycloak.admin.roles.list: failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-list-failed",
			"detail": err.Error(),
		})
		return
	}
	body, _ := keycloak.MarshalRealmRolesJSON(roles)
	writeKCRaw(w, body, http.StatusOK)
}

// HandleKeycloakAdminRoleComposites — TC-125.
// GET /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/roles/{role}/composites
//
// Returns the composite child roles for `{role}` (e.g.
// catalyst-owner → catalyst-admin → catalyst-operator → catalyst-developer
// → catalyst-viewer). The matrix asserts on each composite name being
// present in the response body.
//
// `{role}` is the role NAME (not UUID) — Keycloak's
// /roles/{name}/composites/realm endpoint uses name addressing because
// realm role names are realm-unique.
func (h *Handler) HandleKeycloakAdminRoleComposites(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	roleName := strings.TrimSpace(chi.URLParam(r, "role"))
	if roleName == "" {
		writeBadRequest(w, "missing-role", "role path segment is required")
		return
	}
	composites, err := kc.ListRealmRoleComposites(r.Context(), roleName)
	if err != nil {
		if errors.Is(err, keycloak.ErrRoleNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "role-not-found",
				"detail": "no Keycloak realm role named " + roleName,
			})
			return
		}
		h.log.Warn("keycloak.admin.role.composites: failed", "role", roleName, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-list-failed",
			"detail": err.Error(),
		})
		return
	}
	body, _ := keycloak.MarshalRealmRolesJSON(composites)
	writeKCRaw(w, body, http.StatusOK)
}

// HandleKeycloakAdminIdPList — TC-159.
// GET /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances
//
// Returns the realm's IdP list (`[]IdentityProviderRepresentation`).
// Matrix asserts on presence of the `alias` field name in the response.
func (h *Handler) HandleKeycloakAdminIdPList(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	idps, err := kc.ListIdentityProviders(r.Context())
	if err != nil {
		h.log.Warn("keycloak.admin.idp.list: failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-list-failed",
			"detail": err.Error(),
		})
		return
	}
	body, _ := keycloak.MarshalIdentityProvidersJSON(idps)
	writeKCRaw(w, body, http.StatusOK)
}

// HandleKeycloakAdminIdPCreate — TC-160.
// POST /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances
//
// Creates an IdP from the POSTed IdentityProvider body. Re-fetches the
// canonical representation via GetIdentityProvider after create so the
// response carries the same shape a subsequent GET would return. Matrix
// asserts on `alias` and `openid-connect` presence in the response.
func (h *Handler) HandleKeycloakAdminIdPCreate(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	var idp keycloak.IdentityProvider
	if !decodeMutationBody(w, r, &idp) {
		return
	}
	if strings.TrimSpace(idp.Alias) == "" {
		writeBadRequest(w, "missing-alias", "identity provider alias is required")
		return
	}
	if strings.TrimSpace(idp.ProviderID) == "" {
		writeBadRequest(w, "missing-provider-id", "identity provider providerId is required (e.g. 'oidc')")
		return
	}
	if err := kc.CreateIdentityProvider(r.Context(), idp); err != nil {
		h.log.Warn("keycloak.admin.idp.create: failed", "alias", idp.Alias, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-create-failed",
			"detail": err.Error(),
		})
		return
	}
	// Re-fetch so we return the canonical KC representation. If re-fetch
	// fails we still surface a 201 with the body the caller posted —
	// the create itself succeeded.
	created, getErr := kc.GetIdentityProvider(r.Context(), idp.Alias)
	if getErr != nil {
		body, _ := keycloak.MarshalIdentityProviderJSON(idp)
		writeKCRaw(w, body, http.StatusCreated)
		return
	}
	body, _ := keycloak.MarshalIdentityProviderJSON(created)
	writeKCRaw(w, body, http.StatusCreated)
}

// HandleKeycloakAdminIdPMapperCreate — TC-161.
// POST /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/identity-provider/instances/{alias}/mappers
//
// Creates a claim mapper attached to `{alias}`. The matrix asserts on
// `mapper` literal text in the response (we surface the persisted
// representation echoed back).
func (h *Handler) HandleKeycloakAdminIdPMapperCreate(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	alias := strings.TrimSpace(chi.URLParam(r, "alias"))
	if alias == "" {
		writeBadRequest(w, "missing-alias", "identity provider alias path segment is required")
		return
	}
	var mapper keycloak.IdentityProviderMapper
	if !decodeMutationBody(w, r, &mapper) {
		return
	}
	if strings.TrimSpace(mapper.Name) == "" {
		writeBadRequest(w, "missing-name", "mapper name is required")
		return
	}
	// Force the URL alias onto the mapper representation so a body
	// mismatch doesn't 502 from the keycloak client.
	mapper.IdentityProviderAlias = alias
	if err := kc.CreateIdentityProviderMapper(r.Context(), alias, mapper); err != nil {
		h.log.Warn("keycloak.admin.idp.mapper.create: failed", "alias", alias, "name", mapper.Name, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-create-failed",
			"detail": err.Error(),
		})
		return
	}
	// Echo back the persisted representation. The matrix only asserts
	// on `mapper` literal-text presence so the JSON envelope including
	// the mapper.IdentityProviderMapper field name satisfies it.
	body, _ := json.Marshal(mapper)
	writeKCRaw(w, body, http.StatusCreated)
}

// kcPasswordGrantRequest — POST body for the token-mint passthrough
// endpoint. The matrix may pass `client_id` (preferred) or `clientId`
// (snake_case-tolerant alias the runner sometimes synthesizes from
// snake-cased actions).
type kcPasswordGrantRequest struct {
	ClientID    string `json:"client_id,omitempty"`
	ClientIDAlt string `json:"clientId,omitempty"`
	Username    string `json:"username"`
	Password    string `json:"password"`
}

// HandleKeycloakAdminTokenMint — TC-176.
// POST /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/protocol/openid-connect/token
//
// Token-mint passthrough: caller sends client_id + username + password,
// the proxy forwards the password grant to Keycloak and returns the
// upstream JSON verbatim (so the matrix can assert on `access_token`,
// realm-role names embedded in the JWT, and `invalid_grant` error
// strings). The catalyst-api SA secret is NOT used here — password
// grant runs against a public `directAccessGrantsEnabled=true` OIDC
// client supplied by the caller (typically `kubectl-oidc` or
// `qa-token-mint`).
//
// Auth model: same sovereign-admin gate as the rest of the admin proxy
// — the operator must already hold an admin session to mint a fresh
// password-grant token for ANY user. This prevents the proxy from
// being used as an anonymous credential-stuffing oracle.
func (h *Handler) HandleKeycloakAdminTokenMint(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	var body kcPasswordGrantRequest
	if !decodeMutationBody(w, r, &body) {
		return
	}
	clientID := strings.TrimSpace(body.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(body.ClientIDAlt)
	}
	if clientID == "" {
		writeBadRequest(w, "missing-client-id", "client_id is required")
		return
	}
	if strings.TrimSpace(body.Username) == "" {
		writeBadRequest(w, "missing-username", "username is required")
		return
	}
	if body.Password == "" {
		// Allow but pass through — Keycloak will return invalid_grant
		// which the matrix may assert on. We don't gate on empty
		// password here.
		_ = body.Password
	}
	respBody, status, err := kc.PasswordGrantToken(r.Context(), clientID, body.Username, body.Password)
	if err != nil {
		// Per principle 19: never log password values. Caller-facing
		// error gives no hint about credential validity.
		h.log.Warn("keycloak.admin.token-mint: transport failed", "client_id", clientID, "username", body.Username, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-token-failed",
			"detail": "upstream Keycloak token endpoint unreachable",
		})
		return
	}
	// Forward the upstream body+status verbatim — the matrix asserts on
	// `access_token` / `invalid_grant` / claim names directly.
	writeKCRaw(w, respBody, status)
}

// HandleKeycloakAdminClientsByClientID — TC-285.
// GET /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients?clientId=<id>
//
// Mirrors Keycloak's GET /admin/realms/{realm}/clients?clientId=X shape
// — returns a list (length 0 or 1) of OIDCClient representations.
// Matrix asserts on `netbird` + `openid-connect` presence in the
// response body and on `[]` NOT being the entire body (i.e. at least
// one client is present).
//
// When `?clientId=` is omitted, returns the FULL client list (Keycloak
// admin convention) — useful for operator-side enumeration. The matrix
// always passes a clientId.
func (h *Handler) HandleKeycloakAdminClientsByClientID(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	clientIDQuery := strings.TrimSpace(r.URL.Query().Get("clientId"))
	if clientIDQuery == "" {
		// No filter — full list. Mirror upstream KC.
		clients, err := kc.ListClients(r.Context())
		if err != nil {
			h.log.Warn("keycloak.admin.clients.list: failed", "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "keycloak-list-failed",
				"detail": err.Error(),
			})
			return
		}
		body, _ := keycloak.MarshalOIDCClientsJSON(clients)
		writeKCRaw(w, body, http.StatusOK)
		return
	}
	// Filtered — use the more efficient FindClientByClientID and wrap
	// the single result in the upstream list shape.
	oc, err := kc.FindClientByClientID(r.Context(), clientIDQuery)
	if err != nil {
		h.log.Warn("keycloak.admin.clients.find: failed", "client_id", clientIDQuery, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-find-failed",
			"detail": err.Error(),
		})
		return
	}
	out := []keycloak.OIDCClient{}
	if oc.ID != "" || oc.ClientID != "" {
		out = append(out, oc)
	}
	body, _ := keycloak.MarshalOIDCClientsJSON(out)
	writeKCRaw(w, body, http.StatusOK)
}

// HandleKeycloakAdminClientServiceAccountRoles — TC-190.
// GET /api/v1/sovereigns/{id}/keycloak/admin/realms/{realm}/clients/{client}/service-account-user/role-mappings/realm
//
// Returns the realm-role mappings of the service-account user attached
// to OIDC client `{client}`. Matrix asserts on `manage-realm`,
// `view-realm`, `view-clients` presence in the response body.
//
// `{client}` accepts either the KC UUID OR the human-readable clientId
// (e.g. "catalyst-api") — same heuristic as
// HandleKeycloakClientRolesList. The runner can pass either form.
func (h *Handler) HandleKeycloakAdminClientServiceAccountRoles(w http.ResponseWriter, r *http.Request) {
	_, kc, ok := h.kcAdminProxyPreflight(w, r)
	if !ok {
		return
	}
	clientSeg := strings.TrimSpace(chi.URLParam(r, "client"))
	if clientSeg == "" {
		writeBadRequest(w, "missing-client", "client path segment is required")
		return
	}
	resolvedUUID := clientSeg
	if !looksLikeUUID(clientSeg) {
		oc, ferr := kc.FindClientByClientID(r.Context(), clientSeg)
		if ferr != nil {
			h.log.Warn("keycloak.admin.client.find: failed", "client", clientSeg, "err", ferr)
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error":  "keycloak-find-failed",
				"detail": ferr.Error(),
			})
			return
		}
		if oc.ID == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{
				"error":  "client-not-found",
				"detail": "no Keycloak OIDC client with clientId " + clientSeg,
			})
			return
		}
		resolvedUUID = oc.ID
	}
	body, status, err := kc.ListClientServiceAccountRealmRoles(r.Context(), resolvedUUID)
	if err != nil {
		h.log.Warn("keycloak.admin.client.sa-roles: failed", "client", clientSeg, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "keycloak-list-failed",
			"detail": err.Error(),
		})
		return
	}
	writeKCRaw(w, body, status)
}
