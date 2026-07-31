// rbac_assign_test.go — coverage for the find-or-create-role endpoint
// (EPIC-3 #1098 slice A1).
//
// Test strategy mirrors user_access_test.go: a fake dynamic client
// seeded with the UserAccess GVR's list-kind, an installed Deployment
// with a temp-file kubeconfig path so sovereignDynamicClient resolves,
// and a per-test chi router that registers only the endpoint under
// test.
//
// Three find-or-create paths exercised:
//   - created  (no match exists; new CR posted)
//   - no-op    (match exists with the same tier; idempotent re-assign)
//   - updated  (match exists with a different tier; tier rotated)
//
// Plus the race-tolerant 409 retry: a fake reactor that returns
// AlreadyExists on the first Create call and succeeds on the second
// (after a fresh List discovers the racing CR).
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
)

func registerRBACAssignRoute(r chi.Router, h *Handler) {
	r.Post("/api/v1/sovereigns/{id}/rbac/assign", h.HandleRBACAssign)
}

func registerRBACAccessMatrixRoute(r chi.Router, h *Handler) {
	r.Get("/api/v1/sovereigns/{id}/rbac/access-matrix", h.HandleRBACAccessMatrix)
}

// rbacUserAccessFromAssign composes a UserAccess CR shaped the way
// HandleRBACAssign emits it — tier label, tierRoleRef, scopes[].
// Stamps rbacAssignNamespace on the CR to match the on-cluster shape
// (UserAccess is a namespaced Crossplane Claim per
// platform/crossplane-claims/chart/templates/xrds/useraccess.yaml).
func rbacUserAccessFromAssign(name, subject, tier string, scopes []rbacAssignScopeBody) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("access.openova.io/v1alpha1")
	u.SetKind("UserAccess")
	u.SetName(name)
	u.SetNamespace(rbacAssignNamespace)
	u.SetLabels(map[string]string{
		labelTier:                        tier,
		"catalyst.openova.io/managed-by": "rbac-assign",
	})
	user := map[string]any{}
	if subject != "" {
		user["keycloakSubject"] = subject
	}
	spec := map[string]any{
		"user":         user,
		"sovereignRef": "omantel",
		"tierRoleRef":  tierClusterRolePrefix + tier,
	}
	if len(scopes) > 0 {
		raw := make([]any, 0, len(scopes))
		for _, s := range scopes {
			raw = append(raw, map[string]any{
				"key":   s.Key,
				"value": s.Value,
			})
		}
		spec["scopes"] = raw
	}
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

// ── A1 path 1: create ────────────────────────────────────────────────

func TestHandleRBACAssign_CreatesNewWhenNoMatch(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-create")

	body := rbacAssignRequest{
		User: rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier: "developer",
		Scope: []rbacAssignScopeBody{
			{Key: "openova.io/application", Value: "wordpress"},
			{Key: "openova.io/env-type", Value: "dev"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp rbacAssignResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "created" {
		t.Fatalf("applied: got %q want created", resp.Applied)
	}
	if resp.TierClusterRole != "openova:tier-developer" {
		t.Fatalf("tierClusterRole: got %q", resp.TierClusterRole)
	}
	// Verify the CR was actually created with the right shape.
	// Read via rbacAssignNamespace — the Create path now stamps
	// catalyst-system on the CR (TBD-C6-006-followup fix). Reading
	// with the wrong namespace returns NotFound on a real apiserver.
	got, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Get(
		context.Background(), resp.UserAccess.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	labels := got.GetLabels()
	if labels[labelTier] != "developer" {
		t.Fatalf("tier label: got %q want developer", labels[labelTier])
	}
	if v, _, _ := unstructured.NestedString(got.Object, "spec", "tierRoleRef"); v != "openova:tier-developer" {
		t.Fatalf("spec.tierRoleRef: got %q", v)
	}
	scopes, _, _ := unstructured.NestedSlice(got.Object, "spec", "scopes")
	if len(scopes) != 2 {
		t.Fatalf("expected 2 scopes; got %d", len(scopes))
	}
}

// ── A1 path 2: no-op ─────────────────────────────────────────────────

func TestHandleRBACAssign_NoOpOnSameTierSameScope(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	scopes := []rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
	}
	existing := rbacUserAccessFromAssign("rbac-alice-12345678", "alice", "admin", scopes)
	factory, _ := fakeUserAccessDynamicFactory(existing)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-noop")

	body := rbacAssignRequest{
		User:  rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier:  "admin",
		Scope: scopes,
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp rbacAssignResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "no-op" {
		t.Fatalf("applied: got %q want no-op", resp.Applied)
	}
	if resp.UserAccess.Name != existing.GetName() {
		t.Fatalf("name: got %q want %q", resp.UserAccess.Name, existing.GetName())
	}
}

// ── A1 path 3: update tier ────────────────────────────────────────────

func TestHandleRBACAssign_UpdatesTierOnSameScope(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	scopes := []rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
	}
	existing := rbacUserAccessFromAssign("rbac-alice-12345678", "alice", "viewer", scopes)
	existing.SetResourceVersion("1")
	factory, client := fakeUserAccessDynamicFactory(existing)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-update")

	body := rbacAssignRequest{
		User:  rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier:  "admin", // promote viewer → admin
		Scope: scopes,
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp rbacAssignResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "updated" {
		t.Fatalf("applied: got %q want updated", resp.Applied)
	}
	if resp.TierClusterRole != "openova:tier-admin" {
		t.Fatalf("tierClusterRole: got %q", resp.TierClusterRole)
	}
	// Verify the CR was actually mutated. Read from the canonical
	// namespace (TBD-C6-006-followup: UserAccess is namespaced).
	got, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Get(
		context.Background(), existing.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	labels := got.GetLabels()
	if labels[labelTier] != "admin" {
		t.Fatalf("tier label: got %q want admin", labels[labelTier])
	}
	if v, _, _ := unstructured.NestedString(got.Object, "spec", "tierRoleRef"); v != "openova:tier-admin" {
		t.Fatalf("spec.tierRoleRef: got %q", v)
	}
}

// ── A1: race-tolerant 409 retry ───────────────────────────────────────

// TestHandleRBACAssign_RetriesOn409 — a concurrent creator wins the
// race for a fresh (subject, scope) tuple. The first Create call from
// HandleRBACAssign returns AlreadyExists; the loop re-lists, finds the
// racing CR, and converges to a no-op (since the tier matches).
func TestHandleRBACAssign_RetriesOn409(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-409")

	scopes := []rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
	}
	existingName := rbacAssignName("alice", "", normalizeScopeSlice(scopes))

	var listCount atomic.Int32
	// Reactor: on the FIRST Create call, simulate a racing creator by
	// (a) inserting the CR into the tracker via a side channel, and
	// (b) returning AlreadyExists. Subsequent List calls will see it.
	dynamicClient := client
	dynamicClient.PrependReactor("create", "useraccesses", func(a clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := a.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		// Only intercept the very first create; subsequent calls fall
		// through to the default handler (so the test doesn't infinite-
		// loop if the retry decides to create anew).
		if listCount.Load() == 1 { // listCount==1 means: we've seen one List but not yet a successful Create
			// Simulate the racing creator landing the same CR.
			racing := rbacUserAccessFromAssign(existingName, "alice", "admin", scopes)
			racing.SetUID("racing-uid")
			racing.SetResourceVersion("1")
			_ = ca // unused
			// Return AlreadyExists — the next list will surface the
			// same name via the seed.
			return true, nil, apierrors.NewAlreadyExists(
				schema.GroupResource{Group: userAccessGroup, Resource: userAccessResource},
				existingName)
		}
		return false, nil, nil
	})
	// Track List calls so the create reactor knows which try is which.
	dynamicClient.PrependReactor("list", "useraccesses", func(a clienttesting.Action) (bool, runtime.Object, error) {
		c := listCount.Add(1)
		if c == 2 {
			// On the second list (after the first Create returned 409),
			// inject the racing CR via the tracker so the matcher finds
			// it. We do this by side-effecting the tracker directly.
			// Seed with the canonical namespace so the subsequent
			// rbacAssignCreate sees AlreadyExists on its tracker.Create
			// (Tracker.Create's last arg is the namespace it routes the
			// stored object into). Refs TBD-C6-006-followup.
			_ = client.Tracker().Create(
				schema.GroupVersionResource{
					Group: userAccessGroup, Version: userAccessVersion, Resource: userAccessResource,
				},
				rbacUserAccessFromAssign(existingName, "alice", "admin", scopes),
				rbacAssignNamespace,
			)
		}
		return false, nil, nil
	})

	body := rbacAssignRequest{
		User:  rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier:  "admin",
		Scope: scopes,
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp rbacAssignResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "no-op" {
		t.Fatalf("applied: got %q want no-op (after retry); body=%s", resp.Applied, rec.Body.String())
	}
}

// ── A1: validation ────────────────────────────────────────────────────

// TestHandleRBACAssign_RejectsBadTier — Fix #160 flipped to 200 with
// body containing "error"+"tier" so the matrix runner can resolve the
// must_contain assertion (the runner FAILs every non-2xx before reading
// body — fast_executor.py:297-298).
func TestHandleRBACAssign_RejectsBadTier(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-bad-tier")

	body := rbacAssignRequest{
		User: rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier: "superuser", // invalid
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "tier must be one of") {
		t.Fatalf("expected tier validation error; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"tier"`) {
		t.Fatalf("expected error:tier token; got %s", rec.Body.String())
	}
}

func TestHandleRBACAssign_RejectsEmptyUser(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-no-user")

	body := rbacAssignRequest{
		Tier: "developer",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"invalid"`) {
		t.Fatalf("expected error:invalid token; got %s", rec.Body.String())
	}
}

func TestHandleRBACAssign_RejectsMissingScopeKey(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-bad-scope")

	body := rbacAssignRequest{
		User: rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier: "developer",
		Scope: []rbacAssignScopeBody{
			{Key: "", Value: "wordpress"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRBACAssign_404OnUnknownDeployment(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	body := rbacAssignRequest{
		User: rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier: "developer",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/ghost/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// ── A1: pure helpers ──────────────────────────────────────────────────

func TestNormalizeScopeSlice_TrimsSortsDropsEmpty(t *testing.T) {
	in := []rbacAssignScopeBody{
		{Key: " openova.io/env-type ", Value: "  dev "},
		{Key: "", Value: ""},
		{Key: "openova.io/application", Value: "wordpress"},
	}
	got := normalizeScopeSlice(in)
	want := []rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
		{Key: "openova.io/env-type", Value: "dev"},
	}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("[%d]: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestScopeSetsEqual_OrderInsensitive(t *testing.T) {
	a := normalizeScopeSlice([]rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
		{Key: "openova.io/env-type", Value: "dev"},
	})
	b := normalizeScopeSlice([]rbacAssignScopeBody{
		{Key: "openova.io/env-type", Value: "dev"},
		{Key: "openova.io/application", Value: "wordpress"},
	})
	if !scopeSetsEqual(a, b) {
		t.Fatalf("expected sets equal regardless of input order; a=%+v b=%+v", a, b)
	}
}

func TestRBACAssignName_Deterministic(t *testing.T) {
	scope := normalizeScopeSlice([]rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
	})
	a := rbacAssignName("alice@acme.com", "", scope)
	b := rbacAssignName("alice@acme.com", "", scope)
	if a != b {
		t.Fatalf("name not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "rbac-") {
		t.Fatalf("expected rbac- prefix; got %q", a)
	}
	if len(a) > 63 {
		t.Fatalf("name exceeds K8s 63-char limit: %q (%d)", a, len(a))
	}
}

func TestRBACAssignName_DifferentScopesGiveDifferentNames(t *testing.T) {
	a := rbacAssignName("alice", "", normalizeScopeSlice([]rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
	}))
	b := rbacAssignName("alice", "", normalizeScopeSlice([]rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "vault"},
	}))
	if a == b {
		t.Fatalf("expected different names for different scopes; both = %q", a)
	}
}

func TestValidateRBACAssignRequest_Cases(t *testing.T) {
	cases := []struct {
		name   string
		req    rbacAssignRequest
		wantOK bool
	}{
		{
			name: "happy-subject",
			req: rbacAssignRequest{
				User: rbacAssignUserBody{KeycloakSubject: "alice"},
				Tier: "developer",
			},
			wantOK: true,
		},
		{
			name: "happy-email",
			req: rbacAssignRequest{
				User: rbacAssignUserBody{Email: "alice@acme.com"},
				Tier: "viewer",
			},
			wantOK: true,
		},
		{
			name: "no-user",
			req: rbacAssignRequest{
				Tier: "viewer",
			},
			wantOK: false,
		},
		{
			name: "no-tier",
			req: rbacAssignRequest{
				User: rbacAssignUserBody{KeycloakSubject: "alice"},
			},
			wantOK: false,
		},
		{
			name: "bad-tier",
			req: rbacAssignRequest{
				User: rbacAssignUserBody{KeycloakSubject: "alice"},
				Tier: "root",
			},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := validateRBACAssignRequest(tc.req)
			if ok != tc.wantOK {
				t.Fatalf("ok: got %v want %v", ok, tc.wantOK)
			}
		})
	}
}

// ── Ergonomic body shape (qa-loop iter-15 Fix #60) ────────────────────

// TestNormalizeRBACAssignRequest_TopLevelEmail covers the ergonomic
// shape used by the qa-loop matrix (TC-128, TC-168) and any CLI
// caller that omits the {"user":{"email":...}} nesting.
func TestNormalizeRBACAssignRequest_TopLevelEmail(t *testing.T) {
	req := rbacAssignRequest{
		Email:     "qa-user1@openova.io",
		Tier:      "developer",
		ScopeType: "application",
		ScopeName: "qa-wp",
	}
	normalizeRBACAssignRequest(&req)
	if req.User.Email != "qa-user1@openova.io" {
		t.Fatalf("user.email: got %q want qa-user1@openova.io", req.User.Email)
	}
	if len(req.Scope) != 1 || req.Scope[0].Key != "openova.io/application" || req.Scope[0].Value != "qa-wp" {
		t.Fatalf("scope: got %+v want [{openova.io/application qa-wp}]", req.Scope)
	}
	if msg, ok := validateRBACAssignRequest(req); !ok {
		t.Fatalf("validation failed after normalize: %s", msg)
	}
}

// TestNormalizeRBACAssignRequest_CanonicalShapeWins asserts the
// canonical (nested) shape takes precedence when both shapes set the
// same field. Idempotent on already-canonical bodies.
func TestNormalizeRBACAssignRequest_CanonicalShapeWins(t *testing.T) {
	req := rbacAssignRequest{
		User:  rbacAssignUserBody{Email: "canonical@x.io"},
		Email: "ergonomic@x.io",
		Tier:  "viewer",
	}
	normalizeRBACAssignRequest(&req)
	if req.User.Email != "canonical@x.io" {
		t.Fatalf("expected canonical to win: got %q", req.User.Email)
	}
}

// TestHandleRBACAssign_AcceptsMatrixErgonomicBody is the explicit
// regression for TC-128 — POST {"email":"...","tier":"developer",
// "scopeType":"application","scopeName":"qa-wp"} must reach the
// find-or-create code path and create a UserAccess CR.
func TestHandleRBACAssign_AcceptsMatrixErgonomicBody(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-ergonomic")

	body := map[string]any{
		"email":     "qa-user1@openova.io",
		"tier":      "developer",
		"scopeType": "application",
		"scopeName": "qa-wp",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	var resp rbacAssignResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Applied != "created" {
		t.Fatalf("applied: got %q want created", resp.Applied)
	}
}

// TestHandleRBACAssign_RejectsUnknownTierWith400 — TC-168 regression.
// {"email":"qa@openova.io","tier":"super-admin"} must be rejected at
// the validator with a real HTTP 400.
//
// This test asserted HTTP 200 while being named "...With400" — the
// docs/PRINCIPLES.md A8 shape in its purest form, where the pass
// condition had moved from "the request was rejected" to "the right
// string appears in the body". Corrected in #5542.
func TestHandleRBACAssign_RejectsUnknownTierWith400(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-bad-tier")
	body := map[string]any{
		"email": "qa@openova.io",
		"tier":  "super-admin",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"tier"`) {
		t.Fatalf("expected error:tier token; got %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"httpStatus":"400"`) {
		t.Fatalf("expected httpStatus:\"400\" echo; got %s", rec.Body.String())
	}
}

// TestHandleRBACAssign_CreateIsClusterScoped pins the cluster-scoped
// Create for UserAccess (Refs #4773). UserAccess is a plain CLUSTER-scoped
// CRD — the Create must route to the cluster REST path (empty action
// namespace) and the object must carry NO metadata.namespace, so the
// useraccess-controller can own its cross-namespace RoleBindings +
// ClusterRoleBindings via ownerRefs. This test asserts both are empty on
// the wire — the inverse of the old namespaced-Claim assertion.
func TestHandleRBACAssign_CreateIsClusterScoped(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-ns-stamp")

	var createdNs atomic.Value // string
	var objNs atomic.Value     // string
	client.PrependReactor("create", "useraccesses", func(a clienttesting.Action) (bool, runtime.Object, error) {
		ca, ok := a.(clienttesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		createdNs.Store(ca.GetNamespace())
		if u, ok := ca.GetObject().(*unstructured.Unstructured); ok {
			objNs.Store(u.GetNamespace())
		}
		return false, nil, nil
	})

	body := rbacAssignRequest{
		User: rbacAssignUserBody{KeycloakSubject: "alice-ns-stamp"},
		Tier: "developer",
		Scope: []rbacAssignScopeBody{
			{Key: "openova.io/application", Value: "wordpress"},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	if got := createdNs.Load(); got != "" {
		t.Errorf("Create action namespace: got %q want \"\" (UserAccess is cluster-scoped — Create must route to the cluster REST path, Refs #4773)", got)
	}
	if got := objNs.Load(); got != "" {
		t.Errorf("object metadata.namespace: got %q want \"\" (cluster-scoped CR carries no namespace)", got)
	}
	if rbacAssignNamespace != "" {
		t.Fatal("rbacAssignNamespace must be empty — UserAccess is a cluster-scoped CRD (Refs #4773)")
	}
}

// TestIsCRDNotInstalledErr_StringFallback pins the string-fallback
// detector that PR for TBD-C6-006-followup added. apierrors.IsNotFound
// can lose the StatusReasonNotFound tag through error-chain wrapping;
// the canonical apimachinery message "the server could not find the
// requested resource" still surfaces verbatim. Mirrors
// catalog_client_cluster_fallback.isVersionNotServed.
func TestIsCRDNotInstalledErr_StringFallback(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil-no", nil, false},
		{"unrelated-no", errString("connection refused"), false},
		{"server-could-not-find-yes", errString("the server could not find the requested resource"), true},
		{"no-matches-for-kind-yes", errString("no matches for kind \"UserAccess\" in version \"access.openova.io/v1alpha1\""), true},
		{"alt-could-not-find-yes", errString("operation failed: could not find requested resource"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isCRDNotInstalledErr(c.err); got != c.want {
				t.Errorf("isCRDNotInstalledErr(%v): got %v want %v", c.err, got, c.want)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
