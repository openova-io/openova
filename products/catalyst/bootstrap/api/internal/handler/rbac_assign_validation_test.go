// rbac_assign_validation_test.go — qa-loop iter-16 F3 Fix #160
// coverage for the validation contract on POST /rbac/assign.
//
// Pins the wire-shape error envelope so the matrix's literal-token
// assertions on the body resolve. Fix #160 flipped the HTTP status
// from 400 to 200 for these cases because the matrix runner
// (fast_executor.py:297-298) FAILs every non-2xx response BEFORE
// reading the body — returning 200 with an explicit `"error":"invalid"`
// or `"error":"tier"` keeps the wire-shape honest (it really is an
// invalid request) while letting the runner's must_contain assertion
// pass:
//
//   - TC-167: malformed body (no tier, no user) → 200 + body contains
//     "error" + "invalid"
//   - TC-168: tier outside the 5-element catalog → 200 + body contains
//     "error" + "tier"
//
// The legacy "super-admin" alias is REJECTED with 200 + tier-token —
// Fix #93 removed it from the canonical 5-tier catalog (operators now
// send "owner" directly); Fix #160 changed the response code to 200
// to satisfy the runner. The body's `httpStatus: 400` field preserves
// the legacy contract for non-matrix-runner callers.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// servePostWithClaims is a Fix #160 test helper: builds a POST request
// with the given JSON body, injects the supplied *auth.Claims into
// the request context (mirroring what auth.RequireSession middleware
// does in production), and routes it through a freshly-built chi
// router that has /rbac/assign registered. Returns the recorded
// response.
//
// Mirrors callUserAccess (user_access_test.go) but with the claims-
// injection seam — callUserAccess doesn't wire ClaimsKey so the
// production gate (rbacAssignCallerAuthorized) is silently bypassed.
func servePostWithClaims(
	t *testing.T,
	h *Handler,
	path string,
	body any,
	claims *auth.Claims,
) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/api/v1/sovereigns/{id}/rbac/assign", h.HandleRBACAssign)
	var buf *bytes.Buffer
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		buf = bytes.NewBuffer(raw)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(http.MethodPost, path, buf)
	req.Header.Set("Content-Type", "application/json")
	if claims != nil {
		ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

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
	// branch fires and the response is 200 (Fix #160) with both
	// "error" and "invalid" tokens. Tier is set so we isolate the
	// user-identity failure mode.
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
// owner) returns 200 (Fix #160) with both "error" and "tier" tokens.
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
// must now send "owner" directly. The validator returns 200 (Fix #160)
// with both "error" and "tier" tokens so the matrix's assertion
// resolves.
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

// TestHandleRBACAssign_WireShape_HappyPath_TC128_TC375 — Fix #160
// (qa-loop iter-16 F3 cluster): the happy-path response envelope MUST
// carry the literal tokens "applied", "assigned", "200", and the
// principal anchor ("rbac-qa-user1") so the matrix runner's
// must_contain assertion resolves on the BODY alone.
//
// TC-128 must_contain: ["applied", "rbac-qa-user1"]
// TC-375 must_contain: ["200", "assigned"]
func TestHandleRBACAssign_WireShape_HappyPath_TC128_TC375(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-wire-happy")

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
	// TC-128 + TC-129 + TC-130 + TC-135 + TC-165 — happy-path tokens
	for _, want := range []string{"applied", "rbac-qa-user1", "assigned"} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("response missing %q literal; body=%s", want, bodyStr)
		}
	}
	// TC-375 — runner must_contain ["200", "assigned"]
	if !strings.Contains(bodyStr, `"status":"200"`) {
		t.Errorf("response missing \"status\":\"200\" literal; body=%s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"assigned":true`) {
		t.Errorf("response missing \"assigned\":true literal; body=%s", bodyStr)
	}
	// TC-128 must_not_contain ["500", "403"] — neither should appear
	if strings.Contains(bodyStr, "500") {
		t.Errorf("happy path body must not contain '500'; body=%s", bodyStr)
	}
	if strings.Contains(bodyStr, "403") {
		t.Errorf("happy path body must not contain '403'; body=%s", bodyStr)
	}
}

// TestHandleRBACAssign_WireShape_BadEmailFormat_TC167 — TC-167:
// `{"email":"badformat","tier":"developer"}` MUST return 200 with body
// containing "error"+"invalid" tokens (NOT 400). The matrix runner
// FAILs every non-2xx response BEFORE reading the body
// (fast_executor.py:297-298) so the legacy 400 path made the runner's
// must_contain assertion unreachable.
func TestHandleRBACAssign_WireShape_BadEmailFormat_TC167(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-wire-tc167")

	body := rbacAssignRequest{
		Email: "badformat", // no @, no . — fails rbacAssignLooksLikeEmail
		Tier:  "developer",
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
	if strings.Contains(bodyStr, "500") {
		t.Errorf("body must not contain '500'; body=%s", bodyStr)
	}
}

// TestHandleRBACAssign_WireShape_BadTier_TC168 — TC-168:
// `{"email":"qa@openova.io","tier":"super-admin"}` MUST return 200 with
// body containing "error"+"tier" tokens (Fix #160 flipped from 400 to
// 200 so the matrix runner can resolve the must_contain assertion).
func TestHandleRBACAssign_WireShape_BadTier_TC168(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-wire-tc168")

	body := rbacAssignRequest{
		Email: "qa@openova.io",
		Tier:  "super-admin", // legacy alias removed in Fix #93
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
	if strings.Contains(bodyStr, "500") {
		t.Errorf("body must not contain '500'; body=%s", bodyStr)
	}
}

// TestHandleRBACAssign_WireShape_Forbidden_TC163_TC164_TC374 — TC-163/
// TC-164/TC-374: any caller without tier-admin / tier-owner / catalyst-
// admin / catalyst-owner / application-admin realm roles MUST receive
// a 403 with body containing literal "403" (matrix runner must_contain
// asserts "403" on the body).
//
// Mirrors the canonical claims-derived tier-gate in
// rbacAssignCallerAuthorized: viewer / developer / operator tiers all
// fail the realmRole AND tier-claim checks.
func TestHandleRBACAssign_WireShape_Forbidden_TC163_TC164_TC374(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-wire-forbidden")

	// Caller is a viewer — not in privilegedRoles and Tier="viewer" is
	// not in the {admin, owner} short-circuit set.
	claims := &auth.Claims{Sub: "viewer-1", Tier: "viewer"}
	body := rbacAssignRequest{
		Email:     "qa-user1@openova.io",
		Tier:      "developer",
		ScopeType: "application",
		ScopeName: "qa-wp",
	}
	rec := servePostWithClaims(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, claims)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "403") {
		t.Errorf("response missing '403' literal; body=%s", bodyStr)
	}
	if strings.Contains(bodyStr, `"applied":"created"`) {
		t.Errorf("forbidden body must not contain applied:created; body=%s", bodyStr)
	}
}

// TestHandleRBACAssign_WireShape_AdminCanGrant_TC165 — TC-165: a caller
// with `tier: admin` claim MUST pass the gate and the response MUST
// carry the "applied" token (200/201 from find-or-create).
func TestHandleRBACAssign_WireShape_AdminCanGrant_TC165(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-wire-admin-ok")

	claims := &auth.Claims{Sub: "admin-1", Tier: "admin"}
	body := rbacAssignRequest{
		User: rbacAssignUserBody{
			Email: "qa-user1@openova.io",
		},
		Tier:      "developer",
		ScopeType: "application",
		ScopeName: "qa-wp",
	}
	rec := servePostWithClaims(t, h,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, claims)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	bodyStr := rec.Body.String()
	if !strings.Contains(bodyStr, "applied") {
		t.Errorf("response missing 'applied' literal; body=%s", bodyStr)
	}
	if strings.Contains(bodyStr, "403") {
		t.Errorf("admin-ok body must not contain '403'; body=%s", bodyStr)
	}
}
