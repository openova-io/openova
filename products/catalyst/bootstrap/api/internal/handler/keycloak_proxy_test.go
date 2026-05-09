// keycloak_proxy_test.go — coverage for the EPIC-3 (#1098) U2/U3/U4
// Keycloak proxy handlers. Stubs the KeycloakAdminClient interface so
// the test can exercise the full handler surface without a Keycloak.
//
// Authorization paths exercised:
//   - U2 user search: tier-admin gate via realm role + tier claim
//   - U3 + U4: stricter sovereign-admin gate (admin/owner only)
//   - 503 path when h.kc is nil
//   - federated user surface (federationLink → source mapping)
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
)

// ── Stub KC client ───────────────────────────────────────────────────

type fakeKCAdminClient struct {
	users []keycloak.User
	// SearchUsers
	searchErr  error
	lastSearch string
	lastLimit  int
	// ListGroups
	groups    []keycloak.Group
	listGrpErr error
	// CreateGroup / CreateSubGroup
	createUUID    string
	createErr     error
	lastParentID  string
	lastCreatedG  keycloak.Group
	// GetGroup
	getGroup    keycloak.Group
	getGroupErr error
	// UpdateGroup
	updateErr error
	updatedG  keycloak.Group
	// DeleteGroup
	deleteErr error
	deletedID string
	// FindGroupByPath — used by the idempotency path of
	// HandleKeycloakGroupsCreate when CreateGroup returns
	// keycloak.ErrGroupAlreadyExists. The handler MUST recover the
	// existing group representation rather than 502'ing.
	findByPathGroup    keycloak.Group
	findByPathErr      error
	lastFindByPathPath string
	// ListRealmRoles
	roles      []keycloak.RealmRole
	listRolesErr error
	// GetRealmRole
	getRole    keycloak.RealmRole
	getRoleErr error
	// ListRealmRoleMembers
	roleMembers   []keycloak.User
	roleMembersErr error
	// ListClientRoles
	clientRoles    []keycloak.RealmRole
	clientRolesErr error
}

func (f *fakeKCAdminClient) SearchUsers(_ context.Context, search string, limit int) ([]keycloak.User, error) {
	f.lastSearch = search
	f.lastLimit = limit
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.users, nil
}

func (f *fakeKCAdminClient) ListGroups(_ context.Context) ([]keycloak.Group, error) {
	if f.listGrpErr != nil {
		return nil, f.listGrpErr
	}
	return f.groups, nil
}

func (f *fakeKCAdminClient) CreateGroup(_ context.Context, g keycloak.Group) (string, error) {
	f.lastParentID = ""
	f.lastCreatedG = g
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.createUUID, nil
}

func (f *fakeKCAdminClient) CreateSubGroup(_ context.Context, parentUUID string, g keycloak.Group) (string, error) {
	f.lastParentID = parentUUID
	f.lastCreatedG = g
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.createUUID, nil
}

func (f *fakeKCAdminClient) UpdateGroup(_ context.Context, g keycloak.Group) error {
	f.updatedG = g
	return f.updateErr
}

func (f *fakeKCAdminClient) DeleteGroup(_ context.Context, uuid string) error {
	f.deletedID = uuid
	return f.deleteErr
}

func (f *fakeKCAdminClient) GetGroup(_ context.Context, _ string) (keycloak.Group, error) {
	if f.getGroupErr != nil {
		return keycloak.Group{}, f.getGroupErr
	}
	return f.getGroup, nil
}

func (f *fakeKCAdminClient) FindGroupByPath(_ context.Context, path string) (keycloak.Group, error) {
	f.lastFindByPathPath = path
	if f.findByPathErr != nil {
		return keycloak.Group{}, f.findByPathErr
	}
	return f.findByPathGroup, nil
}

func (f *fakeKCAdminClient) ListRealmRoles(_ context.Context) ([]keycloak.RealmRole, error) {
	if f.listRolesErr != nil {
		return nil, f.listRolesErr
	}
	return f.roles, nil
}

func (f *fakeKCAdminClient) GetRealmRole(_ context.Context, _ string) (keycloak.RealmRole, error) {
	if f.getRoleErr != nil {
		return keycloak.RealmRole{}, f.getRoleErr
	}
	return f.getRole, nil
}

func (f *fakeKCAdminClient) ListRealmRoleMembers(_ context.Context, _ string) ([]keycloak.User, error) {
	if f.roleMembersErr != nil {
		return nil, f.roleMembersErr
	}
	return f.roleMembers, nil
}

func (f *fakeKCAdminClient) ListClientRoles(_ context.Context, _ string) ([]keycloak.RealmRole, error) {
	if f.clientRolesErr != nil {
		return nil, f.clientRolesErr
	}
	return f.clientRoles, nil
}

// ── Test scaffolding ─────────────────────────────────────────────────

func registerKCProxyRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/sovereigns/{id}/keycloak/users", h.HandleKeycloakUsersSearch)
	r.Get("/api/v1/sovereigns/{id}/keycloak/groups", h.HandleKeycloakGroupsList)
	r.Post("/api/v1/sovereigns/{id}/keycloak/groups", h.HandleKeycloakGroupsCreate)
	r.Put("/api/v1/sovereigns/{id}/keycloak/groups/{groupId}", h.HandleKeycloakGroupsUpdate)
	r.Delete("/api/v1/sovereigns/{id}/keycloak/groups/{groupId}", h.HandleKeycloakGroupsDelete)
	r.Get("/api/v1/sovereigns/{id}/keycloak/roles", h.HandleKeycloakRolesList)
	r.Get("/api/v1/sovereigns/{id}/keycloak/roles/{name}/members", h.HandleKeycloakRoleMembers)
	r.Get("/api/v1/sovereigns/{id}/keycloak/clients/{clientId}/roles", h.HandleKeycloakClientRolesList)
}

func newKCProxyHandler(t *testing.T) (*Handler, *Deployment, *fakeKCAdminClient) {
	t.Helper()
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installUserAccessDeployment(t, h, "dep-kcproxy")
	stub := &fakeKCAdminClient{}
	h.SetKCAdminClient(stub)
	return h, dep, stub
}

func reqWithClaims(method, path string, body string, claims *auth.Claims) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	return req
}

// adminClaims returns claims that pass both the tier-admin gate
// (rbacAssignCallerAuthorized) and the sovereign-admin gate
// (policyModeCallerAuthorized).
func adminClaims() *auth.Claims {
	return &auth.Claims{Tier: "admin"}
}

// developerClaims pass NEITHER gate — used to exercise 403 paths.
func developerClaims() *auth.Claims {
	return &auth.Claims{Tier: "developer"}
}

// ── U2: User search ──────────────────────────────────────────────────

func TestKeycloakUsersSearch_HappyPath(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.users = []keycloak.User{
		{ID: "u1", Username: "alice", Email: "alice@acme.com", FirstName: "Alice", LastName: "A"},
		{ID: "u2", Username: "bob.fed", Email: "bob@corp.com", FederationLink: "azure-sso-acme"},
	}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/users?search=ali&limit=10",
		"", adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if stub.lastSearch != "ali" {
		t.Errorf("search query: got %q want %q", stub.lastSearch, "ali")
	}
	if stub.lastLimit != 10 {
		t.Errorf("limit: got %d want 10", stub.lastLimit)
	}
	var resp kcUserListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items: got %d want 2", len(resp.Items))
	}
	if resp.Items[0].Source != "keycloak" {
		t.Errorf("native user source: got %q want keycloak", resp.Items[0].Source)
	}
	if resp.Items[1].Source != "azure_ad_federated" {
		t.Errorf("federated user source: got %q want azure_ad_federated", resp.Items[1].Source)
	}
}

func TestKeycloakUsersSearch_RequiresSearchParam(t *testing.T) {
	h, dep, _ := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/users", "", adminClaims()))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestKeycloakUsersSearch_DefaultLimit(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/users?search=foo",
		"", adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	if stub.lastLimit != 20 {
		t.Errorf("default limit: got %d want 20", stub.lastLimit)
	}
}

func TestKeycloakUsersSearch_LimitClampedTo100(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/users?search=foo&limit=10000",
		"", adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	if stub.lastLimit != 100 {
		t.Errorf("clamped limit: got %d want 100", stub.lastLimit)
	}
}

func TestKeycloakUsersSearch_403WhenDeveloper(t *testing.T) {
	h, dep, _ := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/users?search=foo",
		"", developerClaims()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestKeycloakUsersSearch_503WhenKCUnconfigured(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installUserAccessDeployment(t, h, "dep-kcproxy-unconfigured")
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/users?search=foo",
		"", adminClaims()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

// ── U3: Group browser ────────────────────────────────────────────────

func TestKeycloakGroupsList_HappyPath(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.groups = []keycloak.Group{
		{ID: "g1", Name: "acme", Path: "/acme", Attributes: map[string][]string{"org": {"acme"}}},
		{ID: "g2", Name: "platform", Path: "/platform", SubGroups: []keycloak.Group{
			{ID: "g3", Name: "sre", Path: "/platform/sre"},
		}},
	}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", "", adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp kcGroupListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items: got %d want 2", len(resp.Items))
	}
	if len(resp.Items[1].SubGroups) != 1 || resp.Items[1].SubGroups[0].Name != "sre" {
		t.Errorf("subgroups not preserved: %+v", resp.Items[1].SubGroups)
	}
}

func TestKeycloakGroupsList_403WhenDeveloper(t *testing.T) {
	h, dep, _ := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", "", developerClaims()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403", rec.Code)
	}
}

func TestKeycloakGroupsCreate_TopLevel(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.createUUID = "new-uuid"
	stub.getGroup = keycloak.Group{ID: "new-uuid", Name: "billing", Path: "/billing"}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	body := `{"name":"billing","attributes":{"org":["billing"]}}`
	r.ServeHTTP(rec, reqWithClaims(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", body, adminClaims()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	if stub.lastCreatedG.Name != "billing" {
		t.Errorf("created name: got %q want billing", stub.lastCreatedG.Name)
	}
	if stub.lastParentID != "" {
		t.Errorf("parent id should be empty; got %q", stub.lastParentID)
	}
}

func TestKeycloakGroupsCreate_SubGroup(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.createUUID = "child-uuid"
	stub.getGroup = keycloak.Group{ID: "child-uuid", Name: "sre", Path: "/platform/sre"}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	body := `{"name":"sre","parentId":"parent-uuid"}`
	r.ServeHTTP(rec, reqWithClaims(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", body, adminClaims()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d; body=%s", rec.Code, rec.Body.String())
	}
	if stub.lastParentID != "parent-uuid" {
		t.Errorf("parent id: got %q want parent-uuid", stub.lastParentID)
	}
}

// TestKeycloakGroupsCreate_IdempotentTopLevel proves the qa-loop iter-7
// TC-141 fix: a SECOND POST of the same group name MUST recover the
// existing group's representation and return 201, NOT bubble Keycloak's
// 409 up as a 502. This is the canonical idempotency contract for the
// HTTP API surface (mirrors keycloak.EnsureGroup's controller-side
// contract).
//
// Sequence under test:
//   1. POST {"name":"qa-test-group"}      → 201, body {id, name, path}
//   2. POST {"name":"qa-test-group"} again → 201, SAME id in body
func TestKeycloakGroupsCreate_IdempotentTopLevel(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)

	// First create — succeeds normally.
	stub.createUUID = "first-uuid"
	stub.getGroup = keycloak.Group{ID: "first-uuid", Name: "qa-test-group", Path: "/qa-test-group"}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	body := `{"name":"qa-test-group"}`
	r.ServeHTTP(rec, reqWithClaims(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", body, adminClaims()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first POST status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	var first kcGroupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("first POST decode: %v", err)
	}
	if first.ID != "first-uuid" {
		t.Fatalf("first POST id: got %q want first-uuid", first.ID)
	}

	// Second create — KC returns 409, handler must recover.
	stub.createErr = keycloak.ErrGroupAlreadyExists
	stub.findByPathGroup = keycloak.Group{
		ID: "first-uuid", Name: "qa-test-group", Path: "/qa-test-group",
		Attributes: map[string][]string{},
	}
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, reqWithClaims(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", body, adminClaims()))
	if rec2.Code != http.StatusCreated {
		t.Fatalf("second POST status: got %d want 201; body=%s", rec2.Code, rec2.Body.String())
	}
	var second kcGroupResult
	if err := json.Unmarshal(rec2.Body.Bytes(), &second); err != nil {
		t.Fatalf("second POST decode: %v", err)
	}
	if second.ID != "first-uuid" {
		t.Fatalf("second POST id: got %q want first-uuid (idempotency violated)", second.ID)
	}
	if second.Name != "qa-test-group" {
		t.Errorf("second POST name: got %q want qa-test-group", second.Name)
	}
	// Sanity: handler asked for the group by its full path, not by name.
	if stub.lastFindByPathPath != "/qa-test-group" {
		t.Errorf("FindGroupByPath called with %q; want /qa-test-group", stub.lastFindByPathPath)
	}
}

// TestKeycloakGroupsCreate_IdempotentSubGroup proves the same idempotency
// contract for the sub-group path (POST with parentId set). Recovery
// uses GET /groups/{parentId} → walk SubGroups by name, since
// /group-by-path doesn't accept partial parent traversal.
func TestKeycloakGroupsCreate_IdempotentSubGroup(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	// Simulate KC returning 409 on the create AND the parent's GET
	// returning the existing child inline.
	stub.createErr = keycloak.ErrGroupAlreadyExists
	stub.getGroup = keycloak.Group{
		ID: "parent-uuid", Name: "platform", Path: "/platform",
		SubGroups: []keycloak.Group{
			{ID: "existing-child", Name: "sre", Path: "/platform/sre"},
		},
	}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	body := `{"name":"sre","parentId":"parent-uuid"}`
	r.ServeHTTP(rec, reqWithClaims(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", body, adminClaims()))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	var got kcGroupResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != "existing-child" {
		t.Fatalf("recovered child id: got %q want existing-child", got.ID)
	}
}

// TestKeycloakGroupsCreate_409ResolveEmpty exercises the defensive
// branch where Keycloak returns 409 but the existing group can't be
// found via FindGroupByPath. The handler MUST surface a 502 (rather
// than 201 with an empty body) so the operator gets a clear signal
// that KC state is inconsistent.
func TestKeycloakGroupsCreate_409ResolveEmpty(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.createErr = keycloak.ErrGroupAlreadyExists
	stub.findByPathGroup = keycloak.Group{} // empty — not found
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	body := `{"name":"phantom"}`
	r.ServeHTTP(rec, reqWithClaims(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", body, adminClaims()))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502; body=%s", rec.Code, rec.Body.String())
	}
}

func TestKeycloakGroupsCreate_RejectsEmptyName(t *testing.T) {
	h, dep, _ := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups", `{"name":""}`, adminClaims()))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

func TestKeycloakGroupsUpdate_HappyPath(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.getGroup = keycloak.Group{ID: "g1", Name: "acme", Path: "/acme"}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	body := `{"attributes":{"tier":["admin"]}}`
	r.ServeHTTP(rec, reqWithClaims(http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups/g1", body, adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d; body=%s", rec.Code, rec.Body.String())
	}
	if got := stub.updatedG.Attributes["tier"]; len(got) != 1 || got[0] != "admin" {
		t.Errorf("attrs: got %v want tier=[admin]", stub.updatedG.Attributes)
	}
	// Name preserved from prior GET.
	if stub.updatedG.Name != "acme" {
		t.Errorf("name: got %q want acme", stub.updatedG.Name)
	}
}

func TestKeycloakGroupsUpdate_404OnMissingGroup(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.getGroupErr = keycloak.ErrGroupNotFound
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodPut,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups/missing", `{}`, adminClaims()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestKeycloakGroupsDelete_HappyPath(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodDelete,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups/g1", "", adminClaims()))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d; body=%s", rec.Code, rec.Body.String())
	}
	if stub.deletedID != "g1" {
		t.Errorf("deleted id: got %q want g1", stub.deletedID)
	}
}

func TestKeycloakGroupsDelete_404(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.deleteErr = keycloak.ErrGroupNotFound
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodDelete,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/groups/g1", "", adminClaims()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
}

// ── U4: Role browser ─────────────────────────────────────────────────

func TestKeycloakRolesList_HappyPath(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.roles = []keycloak.RealmRole{
		{ID: "r1", Name: "catalyst-viewer", Composite: false, Attributes: map[string][]string{"tier-level": {"10"}}},
		{ID: "r2", Name: "catalyst-developer", Composite: true, Attributes: map[string][]string{"tier-level": {"20"}}},
	}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/roles", "", adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp kcRoleListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items: got %d want 2", len(resp.Items))
	}
	if resp.Items[0].Attributes["tier-level"][0] != "10" {
		t.Errorf("tier-level attr not preserved: %+v", resp.Items[0].Attributes)
	}
}

func TestKeycloakRoleMembers_HappyPath(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.roleMembers = []keycloak.User{
		{ID: "u1", Username: "alice", Email: "alice@acme.com"},
	}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/roles/catalyst-admin/members", "", adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d; body=%s", rec.Code, rec.Body.String())
	}
	var resp kcRoleMembersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Role != "catalyst-admin" {
		t.Errorf("role: got %q want catalyst-admin", resp.Role)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items: got %d want 1", len(resp.Items))
	}
}

func TestKeycloakRoleMembers_404OnMissingRole(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.roleMembersErr = keycloak.ErrRoleNotFound
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/roles/missing/members", "", adminClaims()))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
}

func TestKeycloakClientRoles_HappyPath(t *testing.T) {
	h, dep, stub := newKCProxyHandler(t)
	stub.clientRoles = []keycloak.RealmRole{
		{ID: "cr1", Name: "wordpress-editor", ClientRole: true},
	}
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/clients/client-uuid/roles", "", adminClaims()))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	var resp kcRoleListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 || !resp.Items[0].ClientRole {
		t.Errorf("client role surface mismatch: %+v", resp.Items)
	}
}

func TestKeycloakRolesList_403WhenDeveloper(t *testing.T) {
	h, dep, _ := newKCProxyHandler(t)
	r := chi.NewRouter()
	registerKCProxyRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, reqWithClaims(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/keycloak/roles", "", developerClaims()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403", rec.Code)
	}
}

// ── Mappers ──────────────────────────────────────────────────────────

func TestKCUserToResult_FederationMapping(t *testing.T) {
	cases := []struct {
		name, link, want string
	}{
		{"native", "", "keycloak"},
		{"azure-sso", "azure-sso-acme", "azure_ad_federated"},
		{"azure-ad", "azure-ad-corp", "azure_ad_federated"},
		{"okta", "okta-acme", "okta-acme"},
		{"generic-oidc", "oidc-customer", "oidc-customer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := kcUserToResult(keycloak.User{ID: "u1", FederationLink: tc.link})
			if r.Source != tc.want {
				t.Errorf("source: got %q want %q", r.Source, tc.want)
			}
		})
	}
}

func TestKCParseLimit(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 20},
		{"abc", 20},
		{"-5", 20},
		{"0", 20},
		{"15", 15},
		{"99", 99},
		{"500", 100},
	}
	for _, tc := range cases {
		if got := kcParseLimit(tc.in); got != tc.want {
			t.Errorf("kcParseLimit(%q): got %d want %d", tc.in, got, tc.want)
		}
	}
}

// Sanity: the *keycloak.Client satisfies the KeycloakAdminClient interface.
// Compile-time assertion via blank assignment.
var _ KeycloakAdminClient = (*keycloak.Client)(nil)

// Compile-time check on the errors export used in this file.
var _ = errors.Is
