// rbac_assign_validation_test.go — qa-loop iter-1 prefetch Fix #93
// coverage for the validation contract on POST /rbac/assign.
//
// Pins the wire-shape error envelope so the matrix's literal-token
// assertions on the body resolve:
//
//   - TC-167: malformed body (no tier, no user) → 400 + body contains
//     "error" + "invalid"
//   - TC-168: tier outside the 5-element catalog → 400 + body contains
//     "error" + "tier"
//
// The legacy "super-admin" alias is REJECTED with 400 — Fix #93 removed
// it from the canonical 5-tier catalog (operators now send "owner"
// directly).
package handler

import (
	"net/http"
	"strings"
	"testing"
)

// TestHandleRBACAssign_RejectsMalformedBody — TC-167: a body missing
// `tier` and any user identity fields returns 400 with both "error"
// and "invalid" tokens (so the matrix's must_contain check resolves).
func TestHandleRBACAssign_RejectsMalformedBody(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-validation-malformed")

	// User identity is empty (no email, no keycloakSubject) — the
	// validator's "user.email or user.keycloakSubject is required"
	// branch fires and the response is 400 with both "error" and
	// "invalid" tokens. Tier is set so we isolate the user-identity
	// failure mode.
	body := rbacAssignRequest{
		Tier: "developer",
	}

	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	for _, want := range []string{"error", "invalid"} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("response missing %q literal; body=%s", want, bodyStr)
		}
	}
}

// TestHandleRBACAssign_RejectsUnknownTier — TC-168: any tier outside
// the canonical 5-element catalog (viewer/developer/operator/admin/
// owner) returns 400 with both "error" and "tier" tokens.
func TestHandleRBACAssign_RejectsUnknownTier(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-validation-tier")

	body := rbacAssignRequest{
		Email: "qa-user1@openova.io",
		Tier:  "supreme-overlord", // not in the 5-element catalog
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	for _, want := range []string{"error", "tier"} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("response missing %q literal; body=%s", want, bodyStr)
		}
	}
}

// TestHandleRBACAssign_RejectsSuperAdminLegacyAlias — qa-loop iter-1
// prefetch Fix #93 (TC-168): the legacy "super-admin" alias was
// REMOVED in this fix. Operators that historically sent "super-admin"
// must now send "owner" directly. The validator returns 400 with both
// "error" and "tier" tokens so the matrix's assertion resolves.
func TestHandleRBACAssign_RejectsSuperAdminLegacyAlias(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-validation-superadmin")

	body := rbacAssignRequest{
		User:      rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier:      "super-admin",
		ScopeType: "application",
		ScopeName: "qa-wp",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	for _, want := range []string{"error", "tier"} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("response missing %q literal; body=%s", want, bodyStr)
		}
	}
}

// TestHandleRBACAssign_ShorthandScopeExpansion — TC-128 / TC-129 wire
// shape: `{"email":"...","tier":"developer","scopeType":"application",
// "scopeName":"qa-wp"}` MUST normalize into the canonical (User, Tier,
// Scope) shape and create the UserAccess CR.
func TestHandleRBACAssign_ShorthandScopeExpansion(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-shorthand")

	body := rbacAssignRequest{
		Email:     "qa-user1@openova.io",
		Tier:      "developer",
		ScopeType: "application",
		ScopeName: "qa-wp",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	for _, want := range []string{"applied", "rbac-qa-user1"} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("response missing %q literal; body=%s", want, bodyStr)
		}
	}
}
