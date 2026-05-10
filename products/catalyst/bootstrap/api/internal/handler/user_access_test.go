// user_access_test.go — coverage for the UserAccess Claim CRUD
// endpoints (issue #323). Mirrors the infrastructure_crud_test.go
// pattern: a fake dynamic client seeded with the UserAccess GVR's
// list-kind, an installed Deployment with a temp-file kubeconfig
// path so sovereignDynamicClient resolves, and a per-test chi router
// that registers only the endpoint under test.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

func userAccessListKinds() map[schema.GroupVersionResource]string {
	return map[schema.GroupVersionResource]string{
		UserAccessGVR(): "UserAccessList",
	}
}

func fakeUserAccessDynamicFactory(seed ...runtime.Object) (func(string) (dynamic.Interface, error), *dynamicfake.FakeDynamicClient) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, userAccessListKinds(), seed...)
	return func(_ string) (dynamic.Interface, error) {
		return client, nil
	}, client
}

func installUserAccessDeployment(t *testing.T, h *Handler, id string) *Deployment {
	t.Helper()
	path := filepath.Join(t.TempDir(), id+".yaml")
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config"), 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	dep := &Deployment{
		ID:     id,
		Status: "ready",
		Request: provisioner.Request{
			SovereignFQDN: "omantel.omani.works",
		},
		Result: &provisioner.Result{
			SovereignFQDN:  "omantel.omani.works",
			KubeconfigPath: path,
		},
		mu: sync.Mutex{},
	}
	h.deployments.Store(id, dep)
	return dep
}

// newUserAccessUnstructured composes a sample UserAccess Claim
// matching the canonical #322 shape — sovereignRef + applications
// list with one app/role/namespaces grant.
func newUserAccessUnstructured(name, sovereign, subject, app, role, ns string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("access.openova.io/v1alpha1")
	u.SetKind("UserAccess")
	u.SetName(name)
	user := map[string]any{}
	if subject != "" {
		user["keycloakSubject"] = subject
	}
	apps := []any{
		map[string]any{
			"app":        app,
			"role":       role,
			"namespaces": []any{ns},
		},
	}
	_ = unstructured.SetNestedMap(u.Object, map[string]any{
		"user":         user,
		"sovereignRef": sovereign,
		"applications": apps,
	}, "spec")
	return u
}

func callUserAccess(t *testing.T, h *Handler, method, path string, body any, register func(r chi.Router, h *Handler)) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	register(r, h)
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
	req := httptest.NewRequest(method, path, buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func registerUserAccessRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/deployments/{depId}/admin/user-access", h.ListUserAccess)
	r.Post("/api/v1/deployments/{depId}/admin/user-access", h.CreateUserAccess)
	r.Put("/api/v1/deployments/{depId}/admin/user-access/{name}", h.UpdateUserAccess)
	r.Delete("/api/v1/deployments/{depId}/admin/user-access/{name}", h.DeleteUserAccess)
}

/* ── GET (list) ────────────────────────────────────────────────── */

func TestListUserAccess_Empty(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-list-empty")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", nil, registerUserAccessRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out userAccessListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 0 {
		t.Fatalf("expected empty list; got %d items", len(out.Items))
	}
}

func TestListUserAccess_Populated(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	seed := []runtime.Object{
		newUserAccessUnstructured("alice-helmwatch", "omantel", "alice", "helmwatch", "editor", "tenant-foo"),
		newUserAccessUnstructured("bob-platform", "omantel", "bob", "catalyst", "admin", "catalyst"),
	}
	factory, _ := fakeUserAccessDynamicFactory(seed...)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-list-populated")

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", nil, registerUserAccessRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var out userAccessListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Items) != 2 {
		t.Fatalf("expected 2 items; got %d", len(out.Items))
	}
	hasAlice := false
	hasBob := false
	for _, it := range out.Items {
		if it.Name == "alice-helmwatch" && it.Spec.SovereignRef == "omantel" && len(it.Spec.Applications) == 1 && it.Spec.Applications[0].App == "helmwatch" {
			hasAlice = true
		}
		if it.Name == "bob-platform" && it.Spec.User.KeycloakSubject == "bob" {
			hasBob = true
		}
	}
	if !hasAlice {
		t.Fatalf("alice-helmwatch missing from list: %+v", out.Items)
	}
	if !hasBob {
		t.Fatalf("bob-platform missing from list: %+v", out.Items)
	}
}

func TestListUserAccess_404UnknownDeployment(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/deployments/ghost/admin/user-access", nil, registerUserAccessRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestListUserAccess_503WhenKubeconfigMissing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := &Deployment{
		ID:      "dep-ua-no-kubeconfig",
		Status:  "ready",
		Request: provisioner.Request{SovereignFQDN: "x.example"},
		Result:  &provisioner.Result{},
		mu:      sync.Mutex{},
	}
	h.deployments.Store(dep.ID, dep)

	rec := callUserAccess(t, h, http.MethodGet,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", nil, registerUserAccessRoutes)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "sovereign-cluster-unreachable") {
		t.Fatalf("expected sovereign-cluster-unreachable; got %s", rec.Body.String())
	}
}

/* ── POST (create) ─────────────────────────────────────────────── */

func TestCreateUserAccess_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-create-happy")

	body := map[string]any{
		"name": "alice-helmwatch",
		"spec": map[string]any{
			"user": map[string]any{
				"keycloakSubject": "alice",
			},
			"sovereignRef": "omantel",
			"applications": []map[string]any{
				{
					"app":        "helmwatch",
					"role":       "editor",
					"namespaces": []string{"tenant-foo"},
				},
			},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", body, registerUserAccessRoutes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	got, err := client.Resource(UserAccessGVR()).Namespace("").Get(context.Background(), "alice-helmwatch", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after create: %v", err)
	}
	role, _, _ := unstructured.NestedString(got.Object, "spec", "applications")
	_ = role
	apps, ok, _ := unstructured.NestedSlice(got.Object, "spec", "applications")
	if !ok || len(apps) != 1 {
		t.Fatalf("expected 1 application; got %v", apps)
	}
	first, _ := apps[0].(map[string]any)
	if r, _ := first["role"].(string); r != "editor" {
		t.Fatalf("role: got %q want editor", r)
	}
	if a, _ := first["app"].(string); a != "helmwatch" {
		t.Fatalf("app: got %q want helmwatch", a)
	}
}

func TestCreateUserAccess_KeycloakGroupsOnly(t *testing.T) {
	// keycloakSubject + keycloakGroups are "either or both" per
	// CRD; the api accepts a Claim with only groups.
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-groups-only")

	body := map[string]any{
		"name": "ops-team",
		"spec": map[string]any{
			"user": map[string]any{
				"keycloakGroups": []string{"sovereign-ops"},
			},
			"sovereignRef": "omantel",
			"applications": []map[string]any{
				{"app": "helmwatch", "role": "viewer"},
			},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", body, registerUserAccessRoutes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateUserAccess_BadRoleRejected(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-bad-role")

	body := map[string]any{
		"name": "alice-bogus",
		"spec": map[string]any{
			"user":         map[string]any{"keycloakSubject": "alice"},
			"sovereignRef": "omantel",
			"applications": []map[string]any{
				{"app": "helmwatch", "role": "superuser"},
			},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", body, registerUserAccessRoutes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "must be one of admin, editor, viewer") {
		t.Fatalf("expected role validation error; got %s", rec.Body.String())
	}
}

func TestCreateUserAccess_MissingUserRejected(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-no-user")

	body := map[string]any{
		"name": "noone",
		"spec": map[string]any{
			"user":         map[string]any{},
			"sovereignRef": "omantel",
			"applications": []map[string]any{
				{"app": "helmwatch", "role": "viewer"},
			},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", body, registerUserAccessRoutes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateUserAccess_409Conflict(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	existing := newUserAccessUnstructured("alice-existing", "omantel", "alice", "helmwatch", "editor", "tenant-foo")
	factory, _ := fakeUserAccessDynamicFactory(existing)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-409")

	body := map[string]any{
		"name": "alice-existing",
		"spec": map[string]any{
			"user":         map[string]any{"keycloakSubject": "alice"},
			"sovereignRef": "omantel",
			"applications": []map[string]any{
				{"app": "helmwatch", "role": "editor", "namespaces": []string{"tenant-foo"}},
			},
		},
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", body, registerUserAccessRoutes)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── PUT (update) ──────────────────────────────────────────────── */

func TestUpdateUserAccess_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	existing := newUserAccessUnstructured("alice-helmwatch", "omantel", "alice", "helmwatch", "viewer", "tenant-foo")
	existing.SetResourceVersion("1")
	factory, client := fakeUserAccessDynamicFactory(existing)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-update-happy")

	// Promote alice from viewer → editor.
	body := map[string]any{
		"name": "alice-helmwatch",
		"spec": map[string]any{
			"user":         map[string]any{"keycloakSubject": "alice"},
			"sovereignRef": "omantel",
			"applications": []map[string]any{
				{"app": "helmwatch", "role": "editor", "namespaces": []string{"tenant-foo"}},
			},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access/alice-helmwatch",
		body, registerUserAccessRoutes)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	got, err := client.Resource(UserAccessGVR()).Namespace("").Get(context.Background(), "alice-helmwatch", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	apps, _, _ := unstructured.NestedSlice(got.Object, "spec", "applications")
	if len(apps) != 1 {
		t.Fatalf("expected 1 app; got %d", len(apps))
	}
	first, _ := apps[0].(map[string]any)
	if r, _ := first["role"].(string); r != "editor" {
		t.Fatalf("role: got %q want editor", r)
	}
}

func TestUpdateUserAccess_404Missing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-update-missing")

	body := map[string]any{
		"name": "ghost",
		"spec": map[string]any{
			"user":         map[string]any{"keycloakSubject": "ghost"},
			"sovereignRef": "omantel",
			"applications": []map[string]any{
				{"app": "helmwatch", "role": "viewer"},
			},
		},
	}
	rec := callUserAccess(t, h, http.MethodPut,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access/ghost",
		body, registerUserAccessRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── DELETE ────────────────────────────────────────────────────── */

func TestDeleteUserAccess_Happy(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	existing := newUserAccessUnstructured("alice-helmwatch", "omantel", "alice", "helmwatch", "editor", "tenant-foo")
	factory, client := fakeUserAccessDynamicFactory(existing)
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-delete-happy")

	rec := callUserAccess(t, h, http.MethodDelete,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access/alice-helmwatch",
		nil, registerUserAccessRoutes)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status: got %d want 204; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := client.Resource(UserAccessGVR()).Namespace("").Get(context.Background(), "alice-helmwatch", metav1.GetOptions{}); err == nil {
		t.Fatalf("expected error after delete; got nil")
	}
}

func TestDeleteUserAccess_404Missing(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-delete-missing")

	rec := callUserAccess(t, h, http.MethodDelete,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access/ghost",
		nil, registerUserAccessRoutes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404; body=%s", rec.Code, rec.Body.String())
	}
}

/* ── Validation unit tests (table-driven) ──────────────────────── */

func TestValidateUserAccess_Cases(t *testing.T) {
	cases := []struct {
		name    string
		req     userAccessRequest
		wantOK  bool
		wantSub string
	}{
		{
			name: "happy-path-keycloak-subject",
			req: userAccessRequest{
				Name: "alice-foo",
				Spec: userAccessSpecBody{
					User:         userAccessUserBody{KeycloakSubject: "alice"},
					SovereignRef: "omantel",
					Applications: []userAccessAppGrantBody{
						{App: "helmwatch", Role: "editor", Namespaces: []string{"tenant-foo"}},
					},
				},
			},
			wantOK: true,
		},
		{
			name: "happy-path-keycloak-groups-only",
			req: userAccessRequest{
				Name: "ops-team",
				Spec: userAccessSpecBody{
					User:         userAccessUserBody{KeycloakGroups: []string{"sovereign-ops"}},
					SovereignRef: "omantel",
					Applications: []userAccessAppGrantBody{
						{App: "helmwatch", Role: "viewer"},
					},
				},
			},
			wantOK: true,
		},
		{
			name:    "missing-name",
			req:     userAccessRequest{},
			wantOK:  false,
			wantSub: "name",
		},
		{
			name: "missing-user-identity",
			req: userAccessRequest{
				Name: "alice",
				Spec: userAccessSpecBody{
					SovereignRef: "omantel",
					Applications: []userAccessAppGrantBody{
						{App: "helmwatch", Role: "viewer"},
					},
				},
			},
			wantOK:  false,
			wantSub: "keycloakSubject",
		},
		{
			name: "missing-sovereign-ref",
			req: userAccessRequest{
				Name: "alice",
				Spec: userAccessSpecBody{
					User: userAccessUserBody{KeycloakSubject: "alice"},
					Applications: []userAccessAppGrantBody{
						{App: "helmwatch", Role: "viewer"},
					},
				},
			},
			wantOK:  false,
			wantSub: "sovereignRef",
		},
		{
			name: "no-applications",
			req: userAccessRequest{
				Name: "alice",
				Spec: userAccessSpecBody{
					User:         userAccessUserBody{KeycloakSubject: "alice"},
					SovereignRef: "omantel",
				},
			},
			wantOK:  false,
			wantSub: "applications",
		},
		{
			name: "bad-role",
			req: userAccessRequest{
				Name: "alice",
				Spec: userAccessSpecBody{
					User:         userAccessUserBody{KeycloakSubject: "alice"},
					SovereignRef: "omantel",
					Applications: []userAccessAppGrantBody{
						{App: "helmwatch", Role: "superuser"},
					},
				},
			},
			wantOK:  false,
			wantSub: "admin",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg, ok := validateUserAccess(c.req)
			if ok != c.wantOK {
				t.Fatalf("ok: got %v want %v (msg=%s)", ok, c.wantOK, msg)
			}
			if !ok && c.wantSub != "" && !strings.Contains(msg, c.wantSub) {
				t.Fatalf("msg %q must contain %q", msg, c.wantSub)
			}
		})
	}
}

// TestCreateUserAccess_AcceptsErgonomicEmailTierBody — TC-156 regression
// (qa-loop iter-15). The matrix POSTs the ergonomic shape
// {"email":"qa-user2@openova.io","tier":"viewer"} and expects HTTP 201
// with qa-user2 echoed in the response body.
func TestCreateUserAccess_AcceptsErgonomicEmailTierBody(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	dep := installUserAccessDeployment(t, h, "dep-ua-ergonomic")
	body := map[string]any{
		"email": "qa-user2@openova.io",
		"tier":  "viewer",
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/deployments/"+dep.ID+"/admin/user-access", body, registerUserAccessRoutes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d want 201; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "qa-user2") {
		t.Fatalf("expected body to contain qa-user2; body=%s", rec.Body.String())
	}
}

// TestNormalizeUserAccessErgonomicShape_TierMapping verifies the tier
// → role mapping that lets the qa-loop matrix exercise /admin/user-access
// with the 5-tier vocabulary while the CRD's per-app grant accepts only
// {viewer, editor, admin}.
func TestNormalizeUserAccessErgonomicShape_TierMapping(t *testing.T) {
	cases := []struct {
		tier     string
		wantRole string
	}{
		{"viewer", "viewer"},
		{"developer", "viewer"},
		{"operator", "admin"},
		{"admin", "admin"},
		{"owner", "admin"},
		{"BOGUS", ""},
	}
	for _, c := range cases {
		t.Run(c.tier, func(t *testing.T) {
			got := userAccessTierToRole(c.tier)
			if got != c.wantRole {
				t.Fatalf("userAccessTierToRole(%q): got %q want %q", c.tier, got, c.wantRole)
			}
		})
	}
}
