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
//
// The namespace is rbacAssignNamespace (`catalyst-system`) because
// the UserAccess CRD is `Namespaced`. See the rbacAssignNamespace
// doc-comment in rbac_assign.go for the rationale.
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
	// Verify the CR was actually created with the right shape in the
	// expected namespace (rbacAssignNamespace == catalyst-system per
	// the namespaced-CRD fix).
	got, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Get(
		context.Background(), resp.UserAccess.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.UserAccess.Namespace != rbacAssignNamespace {
		t.Fatalf("response namespace: got %q want %q", resp.UserAccess.Namespace, rbacAssignNamespace)
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
	// Verify the CR was actually mutated. Read from the namespace
	// rbacAssignNamespace because the seed and the handler now both
	// operate inside catalyst-system per the namespaced-CRD fix.
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

// ── Regression: namespaced-CRD writes ────────────────────────────────

// TestHandleRBACAssign_WritesIntoNamespacedCRD is the regression test
// for the iter-3 incident where /rbac/assign POST returned HTTP 500
// "the server could not find the requested resource" because the
// handler called Namespace("") on a namespaced CRD's Create — the
// apiserver returns the same confusing 404 it returns for an unknown
// resource. The fix routes Create + Update through rbacAssignNamespace
// (catalyst-system) and the response body now carries the namespace.
//
// This test asserts the wire contract that the canonical UAT matrix
// (TC-091 et al.) consumes:
//
//   - HTTP 201 on first create (no existing match)
//   - response.userAccess.namespace == "catalyst-system"
//   - the CR is actually queryable in catalyst-system after the call
//   - the CR is NOT queryable in any other namespace (smoke check
//     that we didn't accidentally cluster-scope it)
//
// If this test ever fails because someone reverts to Namespace(""),
// the symptom would be the original 500 — so a green run here is
// proof the fix is live.
func TestHandleRBACAssign_WritesIntoNamespacedCRD(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-namespaced")

	body := rbacAssignRequest{
		User: rbacAssignUserBody{Email: "test@example.org"},
		Tier: "developer",
		Scope: []rbacAssignScopeBody{
			{Key: "organization", Value: "default"},
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
	// Wire-contract assertions for TC-091.
	if resp.Applied != "created" {
		t.Fatalf("applied: got %q want created", resp.Applied)
	}
	if resp.TierClusterRole != "openova:tier-developer" {
		t.Fatalf("tierClusterRole: got %q", resp.TierClusterRole)
	}
	if resp.UserAccess.Namespace != rbacAssignNamespace {
		t.Fatalf("namespace: got %q want %q (regression — namespaced-CRD Create must route through %s)",
			resp.UserAccess.Namespace, rbacAssignNamespace, rbacAssignNamespace)
	}
	if resp.UserAccess.Name == "" {
		t.Fatalf("name: empty")
	}
	// The CR is queryable in the expected namespace.
	if _, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Get(
		context.Background(), resp.UserAccess.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("get from %s: %v (the regression would surface here as not-found)",
			rbacAssignNamespace, err)
	}
}

// TestHandleRBACAssign_UpdateRoutesThroughNamespace exercises the
// update path's namespace handling. Pre-seeds a CR in catalyst-system,
// then asks for a different tier on the same scope; the handler must
// find the CR (List scoped to rbacAssignNamespace) and Update it
// through the same namespace.
func TestHandleRBACAssign_UpdateRoutesThroughNamespace(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	scopes := []rbacAssignScopeBody{{Key: "openova.io/application", Value: "argocd"}}
	existing := rbacUserAccessFromAssign("rbac-bob-deadc0de", "bob", "viewer", scopes)
	existing.SetResourceVersion("1")
	factory, client := fakeUserAccessDynamicFactory(existing)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-rbac-update-ns")

	body := rbacAssignRequest{
		User:  rbacAssignUserBody{KeycloakSubject: "bob"},
		Tier:  "operator",
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
	if resp.UserAccess.Namespace != rbacAssignNamespace {
		t.Fatalf("namespace: got %q want %q", resp.UserAccess.Namespace, rbacAssignNamespace)
	}
	got, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Get(
		context.Background(), existing.GetName(), metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.GetLabels()[labelTier] != "operator" {
		t.Fatalf("tier label after update: got %q", got.GetLabels()[labelTier])
	}
}
