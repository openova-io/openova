// rbac_matrix_test.go — coverage for the access-matrix endpoint
// (EPIC-3 #1098 slice A2).
//
// Tests exercise:
//   - The pure aggregator buildAccessMatrix() against fixture
//     UserAccess CR slices, asserting the matrix shape, highest-tier-
//     wins on duplicate, broken-contract warnings, and the optional
//     org/application filters.
//   - The HTTP handler against a fake dynamic client.
package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

// ── Pure aggregator tests ─────────────────────────────────────────────

func TestBuildAccessMatrix_GroupsByUser(t *testing.T) {
	items := []unstructured.Unstructured{
		*rbacUserAccessFromAssign(
			"rbac-alice-1", "alice", "admin",
			[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "wordpress"}},
		),
		*rbacUserAccessFromAssign(
			"rbac-bob-1", "bob", "viewer",
			[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "vault"}},
		),
	}
	resp := buildAccessMatrix(items, "", "")
	if len(resp.Users) != 2 {
		t.Fatalf("users: got %d want 2; %+v", len(resp.Users), resp.Users)
	}
	if !sort.SliceIsSorted(resp.Users, func(i, j int) bool {
		return resp.Users[i].ID < resp.Users[j].ID
	}) {
		t.Fatalf("users should be sorted by ID; got %+v", resp.Users)
	}
	if !reflect.DeepEqual(resp.Tiers, []string{"viewer", "developer", "operator", "admin", "owner"}) {
		t.Fatalf("tiers: got %+v", resp.Tiers)
	}
}

func TestBuildAccessMatrix_HighestTierWinsOnDuplicate(t *testing.T) {
	items := []unstructured.Unstructured{
		*rbacUserAccessFromAssign(
			"rbac-alice-viewer", "alice", "viewer",
			[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "wordpress"}},
		),
		*rbacUserAccessFromAssign(
			"rbac-alice-admin", "alice", "admin",
			[]rbacAssignScopeBody{
				{Key: scopeKeyApplication, Value: "wordpress"},
				{Key: scopeKeyEnvType, Value: "prod"},
			},
		),
	}
	resp := buildAccessMatrix(items, "", "")
	if len(resp.Users) != 1 {
		t.Fatalf("users: got %d want 1", len(resp.Users))
	}
	grant := resp.Users[0].Access["wordpress"]
	if grant.Tier != "admin" {
		t.Fatalf("expected admin tier (highest); got %q", grant.Tier)
	}
}

func TestBuildAccessMatrix_DeveloperWithoutDevScopeWarning(t *testing.T) {
	items := []unstructured.Unstructured{
		*rbacUserAccessFromAssign(
			"rbac-alice-dev", "alice", "developer",
			// Developer tier MUST carry env-type=dev per the design
			// doc §6.2; this fixture omits it to trigger the warning.
			[]rbacAssignScopeBody{
				{Key: scopeKeyApplication, Value: "billing"},
			},
		),
	}
	resp := buildAccessMatrix(items, "", "")
	if len(resp.Users) != 1 {
		t.Fatalf("users: got %d want 1", len(resp.Users))
	}
	if len(resp.Users[0].Warnings) == 0 {
		t.Fatalf("expected warning for developer-missing-dev-scope; got none")
	}
	if !strings.Contains(resp.Users[0].Warnings[0], "missing env-type=dev") {
		t.Fatalf("warning text mismatch: %q", resp.Users[0].Warnings[0])
	}
}

func TestBuildAccessMatrix_DeveloperWithDevScopeNoWarning(t *testing.T) {
	items := []unstructured.Unstructured{
		*rbacUserAccessFromAssign(
			"rbac-alice-dev", "alice", "developer",
			[]rbacAssignScopeBody{
				{Key: scopeKeyApplication, Value: "billing"},
				{Key: scopeKeyEnvType, Value: "dev"},
			},
		),
	}
	resp := buildAccessMatrix(items, "", "")
	if len(resp.Users) != 1 {
		t.Fatalf("users: got %d want 1", len(resp.Users))
	}
	if len(resp.Users[0].Warnings) != 0 {
		t.Fatalf("expected no warning; got %+v", resp.Users[0].Warnings)
	}
}

func TestBuildAccessMatrix_NoAppScopeRendersAsWildcard(t *testing.T) {
	// A UserAccess with NO openova.io/application scope is a global
	// (cluster-wide) grant; the matrix surfaces it as "*" so the UI
	// renders it as the "all applications" header row.
	items := []unstructured.Unstructured{
		*rbacUserAccessFromAssign(
			"rbac-owner-global", "carol", "owner",
			[]rbacAssignScopeBody{}, // no app scope, no env scope
		),
	}
	resp := buildAccessMatrix(items, "", "")
	if len(resp.Users) != 1 {
		t.Fatalf("users: got %d want 1", len(resp.Users))
	}
	grant, ok := resp.Users[0].Access["*"]
	if !ok {
		t.Fatalf("expected wildcard grant; got %+v", resp.Users[0].Access)
	}
	if grant.Tier != "owner" {
		t.Fatalf("tier: got %q want owner", grant.Tier)
	}
}

func TestBuildAccessMatrix_OrgFilter(t *testing.T) {
	items := []unstructured.Unstructured{
		*rbacUserAccessFromAssign(
			"rbac-alice-acme", "alice", "admin",
			[]rbacAssignScopeBody{
				{Key: scopeKeyOrg, Value: "acme"},
				{Key: scopeKeyApplication, Value: "wordpress"},
			},
		),
		*rbacUserAccessFromAssign(
			"rbac-bob-other", "bob", "admin",
			[]rbacAssignScopeBody{
				{Key: scopeKeyOrg, Value: "other-corp"},
				{Key: scopeKeyApplication, Value: "vault"},
			},
		),
	}
	resp := buildAccessMatrix(items, "acme", "")
	if len(resp.Users) != 1 {
		t.Fatalf("users with org=acme filter: got %d want 1; %+v", len(resp.Users), resp.Users)
	}
	if resp.Users[0].ID != "alice" {
		t.Fatalf("expected alice; got %q", resp.Users[0].ID)
	}
}

func TestBuildAccessMatrix_ApplicationFilter(t *testing.T) {
	items := []unstructured.Unstructured{
		*rbacUserAccessFromAssign(
			"rbac-alice-wp", "alice", "admin",
			[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "wordpress"}},
		),
		*rbacUserAccessFromAssign(
			"rbac-bob-vault", "bob", "viewer",
			[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "vault"}},
		),
	}
	resp := buildAccessMatrix(items, "", "wordpress")
	if len(resp.Users) != 1 {
		t.Fatalf("users with application=wordpress filter: got %d want 1", len(resp.Users))
	}
	if resp.Users[0].ID != "alice" {
		t.Fatalf("expected alice; got %q", resp.Users[0].ID)
	}
}

func TestBuildAccessMatrix_SkipsGroupOnlyGrants(t *testing.T) {
	// A UserAccess with only keycloakGroups (no subject) should be
	// excluded from the per-user matrix. This is documented behaviour
	// — the U7 UI's group-view is a future enhancement.
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("access.openova.io/v1alpha1")
	u.SetKind("UserAccess")
	u.SetName("rbac-group-only")
	u.SetLabels(map[string]string{labelTier: "viewer"})
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"user": map[string]any{
			"keycloakGroups": []any{"sovereign-ops"},
		},
		"sovereignRef": "omantel",
	}, "spec")
	resp := buildAccessMatrix([]unstructured.Unstructured{*u}, "", "")
	if len(resp.Users) != 0 {
		t.Fatalf("expected 0 users (group-only skipped); got %d", len(resp.Users))
	}
}

func TestBuildAccessMatrix_IdentitySourceLabel(t *testing.T) {
	u := rbacUserAccessFromAssign(
		"rbac-alice-azure", "alice", "viewer",
		[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "wordpress"}},
	)
	labels := u.GetLabels()
	labels["catalyst.openova.io/identity-source"] = "azure_ad_federated"
	u.SetLabels(labels)
	resp := buildAccessMatrix([]unstructured.Unstructured{*u}, "", "")
	if len(resp.Users) != 1 {
		t.Fatalf("users: got %d want 1", len(resp.Users))
	}
	if resp.Users[0].Source != "azure_ad_federated" {
		t.Fatalf("source: got %q want azure_ad_federated", resp.Users[0].Source)
	}
}

// ── HTTP handler test ─────────────────────────────────────────────────

func TestHandleRBACAccessMatrix_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	seed := []runtime.Object{
		rbacUserAccessFromAssign(
			"rbac-alice-1", "alice", "admin",
			[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "wordpress"}},
		),
		rbacUserAccessFromAssign(
			"rbac-bob-1", "bob", "developer",
			[]rbacAssignScopeBody{
				{Key: scopeKeyApplication, Value: "billing"},
				{Key: scopeKeyEnvType, Value: "dev"},
			},
		),
	}
	factory, _ := fakeUserAccessDynamicFactory(seed...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-matrix")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/access-matrix", nil, registerRBACAccessMatrixRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp AccessMatrixResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Users) != 2 {
		t.Fatalf("users: got %d want 2; %+v", len(resp.Users), resp.Users)
	}
	if !reflect.DeepEqual(resp.Tiers, []string{"viewer", "developer", "operator", "admin", "owner"}) {
		t.Fatalf("tiers: got %+v", resp.Tiers)
	}
	wantApps := []string{"billing", "wordpress"}
	if !reflect.DeepEqual(resp.Applications, wantApps) {
		t.Fatalf("applications: got %+v want %+v", resp.Applications, wantApps)
	}
}

func TestHandleRBACAccessMatrix_404OnUnknownDeployment(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/ghost/rbac/access-matrix", nil, registerRBACAccessMatrixRoute)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRBACAccessMatrix_EmptyOnNoCRD(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-matrix-empty")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/access-matrix", nil, registerRBACAccessMatrixRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp AccessMatrixResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Users) != 0 {
		t.Fatalf("expected no users; got %+v", resp.Users)
	}
}

// ── Sovereign-prefix wrapper (TBD-F4 / C6-007) ────────────────────────

func registerSovereignRBACMatrixRoute(r chi.Router, h *Handler) {
	r.Get("/api/v1/sovereign/rbac/matrix", h.HandleSovereignRBACMatrix)
}

// TestHandleSovereignRBACMatrix_Happy verifies the Sovereign-prefix
// chroot wrapper resolves the deployment id from CATALYST_SELF_DEPLOYMENT_ID
// and serves the same AccessMatrixResponse shape as the per-deployment
// surface. Without this endpoint a Sovereign-side operator (or the QA
// test executor probing the canonical chroot path) gets a 404.
//
// Test uses CATALYST_SELF_DEPLOYMENT_ID (precedence 2 in
// resolveSovereignDeploymentID) so the dep's SovereignFQDN can be set
// to something distinct from the SOVEREIGN_FQDN env — that way
// sovereignDynamicClient's chroot-env-match check falls through and
// the test's dynamicFactory fake intercepts via the kubeconfig path.
func TestHandleSovereignRBACMatrix_Happy(t *testing.T) {
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "dep-sov-rbac")
	t.Setenv("SOVEREIGN_FQDN", "") // keep env-match path off for the fake client

	h := NewWithPDM(silentLogger(), &fakePDM{})
	seed := []runtime.Object{
		rbacUserAccessFromAssign(
			"rbac-alice-1", "alice", "admin",
			[]rbacAssignScopeBody{{Key: scopeKeyApplication, Value: "wordpress"}},
		),
	}
	factory, _ := fakeUserAccessDynamicFactory(seed...)
	h.dynamicFactory = factory
	_ = installUserAccessDeployment(t, h, "dep-sov-rbac")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereign/rbac/matrix", nil, registerSovereignRBACMatrixRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp AccessMatrixResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Users) != 1 || resp.Users[0].ID != "alice" {
		t.Fatalf("users: got %+v want [{ID:alice ...}]", resp.Users)
	}
	if !reflect.DeepEqual(resp.Tiers, []string{"viewer", "developer", "operator", "admin", "owner"}) {
		t.Fatalf("tiers: got %+v", resp.Tiers)
	}
}

// TestHandleSovereignRBACMatrix_404OffMothership verifies the wrapper
// returns a structured 404 on the mothership (no SOVEREIGN_FQDN /
// CATALYST_OTECH_FQDN / handover cookie) instead of leaking a 500 or
// silently dispatching to a synthetic deployment id that has no
// underlying cluster.
func TestHandleSovereignRBACMatrix_404OffMothership(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereign/rbac/matrix", nil, registerSovereignRBACMatrixRoute)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not-a-sovereign") {
		t.Fatalf("body should reference not-a-sovereign error code; got %s", rec.Body.String())
	}
}

// TestHandleSovereignRBACMatrix_EmptyOnNoCRD verifies a fresh
// Sovereign with the deployment record but no UserAccess CRs yet (D21
// chain still bootstrapping) serves an empty-matrix shape with 200, so
// the AccessMatrixPage renders "no grants yet" instead of an error
// toast.
func TestHandleSovereignRBACMatrix_EmptyOnNoCRD(t *testing.T) {
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "dep-sov-rbac-empty")
	t.Setenv("SOVEREIGN_FQDN", "")

	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	_ = installUserAccessDeployment(t, h, "dep-sov-rbac-empty")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/sovereign/rbac/matrix", nil, registerSovereignRBACMatrixRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp AccessMatrixResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Users) != 0 {
		t.Fatalf("expected no users; got %+v", resp.Users)
	}
	if len(resp.Tiers) != 5 {
		t.Fatalf("tiers: expected canonical 5; got %+v", resp.Tiers)
	}
}
