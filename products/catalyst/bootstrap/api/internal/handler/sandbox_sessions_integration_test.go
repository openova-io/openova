// Package handler — sandbox_sessions_integration_test.go drives the
// /api/v1/sandbox/sessions REST surface end-to-end against a
// dynamic/fake apiserver. Where sandbox_sessions_test.go pins the
// unstructured→sandboxItem projection in isolation, this file
// exercises the full HTTP round-trip:
//
//   chi router → HandleCreateSandboxSession → fake apiserver Create
//   chi router → HandleGetSandboxSession    → fake apiserver Get + .status reflection
//
// The fake dynamic client is wired via SetSovereignDepsFactory so
// the handler's resolution path mirrors what a chroot Sovereign
// runs in production (sandbox_sessions.go's "Path 2" — in-cluster
// fallback). The dynamic-client-backed seam means the readback
// after Create is functionally identical to what a real
// sandbox-controller informer would observe.
//
// Wave 15 follow-up to PR #1637 (sessions API) + PR #1673
// (status-reflect): the FE's actionable-failure rendering depends
// on the wire shape preserving Failed/{TokenMintFailed,
// GitopsWriteFailed, ManifestRenderFailed} conditions verbatim.
// These tests cover the canonical failure-reason taxonomy from
// core/controllers/sandbox/internal/controller/sandbox_controller.go.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// ── Fixtures ────────────────────────────────────────────────────────

// newSandboxHandler wires a Handler with a fake dynamic client
// seeded with the supplied Sandbox CRs. The SandboxGVR's list-kind
// is registered so List/Get calls land on a typed
// *unstructured.UnstructuredList rather than panicking.
//
// Seeding happens via direct Create on the dynamic interface (rather
// than the objects-arg of NewSimpleDynamicClientWithCustomListKinds)
// because the tracker's Add path requires the non-list GVK to be
// scheme-registered with UnsafeGuessKindToResource agreeing — for a
// CRD like sandbox.openova.io/v1 Sandboxes the heuristic doesn't
// align cleanly. Create-based seeding sidesteps the entire scheme
// resolution by passing the GVR through the action explicitly.
func newSandboxHandler(t *testing.T, seed ...*unstructured.Unstructured) *Handler {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		SandboxGVR(): "SandboxList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
	for _, obj := range seed {
		if _, err := dyn.Resource(SandboxGVR()).Namespace(obj.GetNamespace()).Create(
			context.Background(), obj, metav1.CreateOptions{},
		); err != nil {
			t.Fatalf("seed Sandbox %s/%s: %v", obj.GetNamespace(), obj.GetName(), err)
		}
	}
	core := fakek8s.NewSimpleClientset()

	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core, dyn: dyn}, nil
	})
	return h
}

// withSandboxClaims injects the Sandbox-specific claim shape into
// the request context: Sub (required) + Email (stamped on
// spec.owner.email) + Org (the namespace the CR lands in).
func withSandboxClaims(req *http.Request, sub, email, org string) *http.Request {
	claims := &auth.Claims{Sub: sub, Email: email, Org: org}
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, claims)
	return req.WithContext(ctx)
}

// registerSandboxRoutes mirrors main.go's RequireSession group so
// the chi URL params ({id}) resolve the same way as production.
func registerSandboxRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/sandbox/sessions", h.HandleListSandboxSessions)
	r.Post("/api/v1/sandbox/sessions", h.HandleCreateSandboxSession)
	r.Get("/api/v1/sandbox/sessions/{id}", h.HandleGetSandboxSession)
	r.Delete("/api/v1/sandbox/sessions/{id}", h.HandleDeleteSandboxSession)
}

// callSandbox executes a single request through a fresh router so
// each test starts with a clean URL-param dispatcher.
func callSandbox(t *testing.T, h *Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	registerSandboxRoutes(r, h)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// mkSandboxCR builds a Sandbox unstructured for seeding the fake
// apiserver. Optional status block lets a test simulate the
// controller having already reconciled (or hit a Failed condition).
func mkSandboxCR(name, ns, agent string, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "sandbox.openova.io",
		Version: "v1",
		Kind:    "Sandbox",
	})
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Second)))
	_ = unstructured.SetNestedSlice(u.Object, []any{agent}, "spec", "agentCatalogue")
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

// ── POST → GET round-trip ───────────────────────────────────────────

// TestSandboxIntegration_PostThenGet — drives the canonical FE flow:
//
//  1. POST /api/v1/sandbox/sessions with {agent: claude-code}
//  2. fake apiserver stores the unstructured CR
//  3. GET /api/v1/sandbox/sessions/{id} reads it back
//  4. response shape matches the FE's expected sandboxItem wire
//
// Proves the dynamic-client wiring (Create + Get) round-trips
// through the same fake without losing fields — what a real chroot
// Sovereign would do at runtime, without needing one to exist.
func TestSandboxIntegration_PostThenGet(t *testing.T) {
	h := newSandboxHandler(t)

	body := sandboxCreateRequest{
		Agent: "claude-code",
		Name:  "my-sandbox",
		Repo:  "acme/site",
	}
	raw, _ := json.Marshal(body)
	postReq := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	postReq.Header.Set("Content-Type", "application/json")
	postReq = withSandboxClaims(postReq, "user-sub-abcdef12", "operator@acme.com", "acme")

	postRec := callSandbox(t, h, postReq)
	if postRec.Code != http.StatusCreated {
		t.Fatalf("POST: status = %d, body = %s", postRec.Code, postRec.Body.String())
	}

	var created sandboxItem
	if err := json.Unmarshal(postRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode POST body: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("POST response missing ID: %+v", created)
	}
	if created.Agent != "claude-code" {
		t.Errorf("POST response Agent = %q, want claude-code", created.Agent)
	}

	// GET the just-created sandbox — round-trips through the same
	// fake apiserver Create wrote to.
	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions/"+created.ID, nil)
	getReq = withSandboxClaims(getReq, "user-sub-abcdef12", "operator@acme.com", "acme")
	getRec := callSandbox(t, h, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET: status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	var fetched sandboxItem
	if err := json.Unmarshal(getRec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}

	if fetched.ID != created.ID {
		t.Errorf("GET ID = %q, want %q", fetched.ID, created.ID)
	}
	if fetched.Agent != "claude-code" {
		t.Errorf("GET Agent = %q, want claude-code", fetched.Agent)
	}
	if fetched.Repo != "acme/site" {
		t.Errorf("GET Repo = %q, want acme/site", fetched.Repo)
	}
	// Display-name preserved across the round-trip via the
	// catalyst.openova.io/sandbox-display-name annotation.
	if fetched.Name != "my-sandbox" {
		t.Errorf("GET Name = %q, want my-sandbox", fetched.Name)
	}
	// No controller has touched status yet, so Phase is empty and
	// the FE-facing Status projects to "pending" (spinner).
	if fetched.Status != "pending" {
		t.Errorf("GET Status = %q, want pending (no phase yet)", fetched.Status)
	}
	// Defensive contract: Previews / Conditions must be empty
	// slices (not nil) so the FE never has to `?? []`.
	if fetched.Previews == nil {
		t.Errorf("GET Previews must be empty slice, not nil")
	}
	if fetched.Conditions == nil {
		t.Errorf("GET Conditions must be empty slice, not nil")
	}
}

// ── GET reflects controller status ──────────────────────────────────

// TestSandboxIntegration_GetReflectsReadyStatus — seed a Sandbox CR
// with status.phase=Ready + sessions=2 + storage + spend + previews
// + a Ready=True condition. GET projects the live runtime back to
// the FE wire shape verbatim.
func TestSandboxIntegration_GetReflectsReadyStatus(t *testing.T) {
	seed := mkSandboxCR("sandbox-ready", "acme", "claude-code", map[string]any{
		"phase":       "Ready",
		"sessions":    int64(2),
		"storageUsed": "12Gi",
		"spend30d":    "$4.21",
		"previews": []any{
			map[string]any{
				"prNumber": int64(42),
				"url":      "https://pr-42.preview.acme.example",
				"sha":      "abc1234",
			},
		},
		"conditions": []any{
			map[string]any{
				"type":               "Ready",
				"status":             "True",
				"reason":             "GitopsReconciled",
				"message":            "manifests written and applied",
				"lastTransitionTime": "2026-05-18T11:00:00Z",
			},
		},
	})
	h := newSandboxHandler(t, seed)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions/sandbox-ready", nil)
	req = withSandboxClaims(req, "user-sub-aaa", "ops@acme.com", "acme")
	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var got sandboxItem
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Phase != "Ready" {
		t.Errorf("Phase = %q, want Ready", got.Phase)
	}
	if got.Status != "running" {
		t.Errorf("Status = %q, want running (Ready → running)", got.Status)
	}
	if got.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2", got.Sessions)
	}
	if got.StorageUsed != "12Gi" {
		t.Errorf("StorageUsed = %q, want 12Gi", got.StorageUsed)
	}
	if got.Spend30d != "$4.21" {
		t.Errorf("Spend30d = %q, want $4.21", got.Spend30d)
	}
	if len(got.Previews) != 1 || got.Previews[0].PRNumber != 42 ||
		got.Previews[0].URL != "https://pr-42.preview.acme.example" {
		t.Errorf("Previews = %#v", got.Previews)
	}
	if len(got.Conditions) != 1 ||
		got.Conditions[0].Type != "Ready" ||
		got.Conditions[0].Status != "True" ||
		got.Conditions[0].Reason != "GitopsReconciled" {
		t.Errorf("Conditions = %#v", got.Conditions)
	}
}

// TestSandboxIntegration_FailedConditionsTaxonomy — drives each of
// the canonical controller failure reasons through the round-trip
// and asserts the FE-facing wire shape (Status=failed, Phase=Failed,
// Conditions[0].Reason preserved verbatim).
//
// Sources for the reason list: sandbox_controller.go's fail()
// calls — TokenMintFailed, ManifestRenderFailed, GitopsWriteFailed.
// The FE renders these as actionable error states ("Token mint
// failed — retrying" inline) instead of a generic red pill, so
// drift here is user-visible.
func TestSandboxIntegration_FailedConditionsTaxonomy(t *testing.T) {
	reasons := []struct {
		name    string
		reason  string
		message string
	}{
		{"TokenMintFailed", "TokenMintFailed", "newapi bridge unreachable"},
		{"GitopsWriteFailed", "GitopsWriteFailed", "gitea: 502 Bad Gateway"},
		{"ManifestRenderFailed", "ManifestRenderFailed", "helm template: parse error at line 42"},
	}
	for _, tc := range reasons {
		t.Run(tc.reason, func(t *testing.T) {
			crName := "sandbox-fail-" + tc.reason
			seed := mkSandboxCR(crName, "acme", "claude-code", map[string]any{
				"phase": "Failed",
				"conditions": []any{
					map[string]any{
						"type":               "Ready",
						"status":             "False",
						"reason":             tc.reason,
						"message":            tc.message,
						"lastTransitionTime": "2026-05-18T11:00:00Z",
					},
				},
			})
			h := newSandboxHandler(t, seed)

			req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions/"+crName, nil)
			req = withSandboxClaims(req, "user-sub-zzz", "ops@acme.com", "acme")
			rec := callSandbox(t, h, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: status = %d, body = %s", tc.reason, rec.Code, rec.Body.String())
			}

			var got sandboxItem
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s: decode: %v", tc.reason, err)
			}

			if got.Phase != "Failed" {
				t.Errorf("%s: Phase = %q, want Failed", tc.reason, got.Phase)
			}
			if got.Status != "failed" {
				t.Errorf("%s: Status = %q, want failed (FE projection)", tc.reason, got.Status)
			}
			if len(got.Conditions) != 1 {
				t.Fatalf("%s: Conditions count = %d, want 1", tc.reason, len(got.Conditions))
			}
			c := got.Conditions[0]
			if c.Reason != tc.reason {
				t.Errorf("%s: Conditions[0].Reason = %q, want %q", tc.reason, c.Reason, tc.reason)
			}
			if c.Message != tc.message {
				t.Errorf("%s: Conditions[0].Message = %q, want %q", tc.reason, c.Message, tc.message)
			}
			if c.Status != "False" {
				t.Errorf("%s: Conditions[0].Status = %q, want False", tc.reason, c.Status)
			}
		})
	}
}

// TestSandboxIntegration_PostInvalidAgent — agent not in the
// canonical catalogue must 400 before any apiserver call. Protects
// the FE from CRs that would always fail openAPIV3Schema validation
// at the apiserver and surface as confusing 422s on the next list.
func TestSandboxIntegration_PostInvalidAgent(t *testing.T) {
	h := newSandboxHandler(t)
	body := sandboxCreateRequest{Agent: "rogue-agent-9000"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req = withSandboxClaims(req, "user-sub-x", "x@acme.com", "acme")

	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (invalid agent); body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "invalid-agent" {
		t.Errorf("error = %v, want invalid-agent", resp["error"])
	}
}

// TestSandboxIntegration_GetUnknownSandbox — 404 with a clear
// "sandbox-not-found" error code so the FE can render an
// actionable empty state instead of a generic toast.
func TestSandboxIntegration_GetUnknownSandbox(t *testing.T) {
	h := newSandboxHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions/sandbox-ghost", nil)
	req = withSandboxClaims(req, "user-sub-z", "z@acme.com", "acme")
	rec := callSandbox(t, h, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "sandbox-not-found" {
		t.Errorf("error = %v, want sandbox-not-found", resp["error"])
	}
}

// TestSandboxIntegration_ListThenDelete — POST 3 sandboxes, LIST
// returns all 3, DELETE one, LIST returns 2.
//
// The list ordering check is intentionally lenient (count-only)
// because the fake apiserver's List ordering matches its insertion
// order but the FE doesn't depend on that — the FE sorts by
// createdAt itself.
func TestSandboxIntegration_ListThenDelete(t *testing.T) {
	h := newSandboxHandler(t)

	for _, name := range []string{"a", "b", "c"} {
		body := sandboxCreateRequest{Agent: "claude-code", Name: "sb-" + name}
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sandbox/sessions", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req = withSandboxClaims(req, "user-sub-"+name, name+"@acme.com", "acme")
		rec := callSandbox(t, h, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST %s: status %d, body=%s", name, rec.Code, rec.Body.String())
		}
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil)
	listReq = withSandboxClaims(listReq, "user-sub-list", "list@acme.com", "acme")
	listRec := callSandbox(t, h, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("LIST: status %d, body=%s", listRec.Code, listRec.Body.String())
	}
	var listResp sandboxListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listResp); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listResp.Sandboxes) != 3 {
		t.Fatalf("LIST sandbox count = %d, want 3", len(listResp.Sandboxes))
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/sandbox/sessions/sb-a", nil)
	delReq = withSandboxClaims(delReq, "user-sub-list", "list@acme.com", "acme")
	delRec := callSandbox(t, h, delReq)
	if delRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE: status %d, body=%s", delRec.Code, delRec.Body.String())
	}

	listRec2 := callSandbox(t, h, withSandboxClaims(
		httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil),
		"user-sub-list", "list@acme.com", "acme"))
	if listRec2.Code != http.StatusOK {
		t.Fatalf("LIST after delete: status %d, body=%s", listRec2.Code, listRec2.Body.String())
	}
	var listResp2 sandboxListResponse
	if err := json.Unmarshal(listRec2.Body.Bytes(), &listResp2); err != nil {
		t.Fatalf("decode list2: %v", err)
	}
	if len(listResp2.Sandboxes) != 2 {
		t.Fatalf("LIST after delete count = %d, want 2", len(listResp2.Sandboxes))
	}
}

// TestSandboxIntegration_DeleteBackendUnavailableDegradesTo503 — #5140
// follow-up. List/Get/Create already degrade a transient
// backend-unavailable error (401/403/timeout/discovery-race during a
// cutover / region-kill recovery window — the hw261 symptom the issue
// was filed against) to the honest 503 "API pending" instead of a raw
// 500. Delete shares the exact same dynamic-client + error surface but
// was not wired through the same sandboxBackendUnavailable classifier,
// so the identical transient window still 500'd on the mutating
// Start-session → Stop/Delete path. This pins the fix: a token-rejected
// (Unauthorized) error on Delete now degrades to 503, matching the
// other three verbs, while a genuine success (204, no error injected)
// is unaffected — see TestSandboxIntegration_ListThenDelete.
func TestSandboxIntegration_DeleteBackendUnavailableDegradesTo503(t *testing.T) {
	scheme := runtime.NewScheme()
	gvrToList := map[schema.GroupVersionResource]string{
		SandboxGVR(): "SandboxList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
	seed := mkSandboxCR("sandbox-flaky", "acme", "claude-code", nil)
	if _, err := dyn.Resource(SandboxGVR()).Namespace("acme").Create(
		context.Background(), seed, metav1.CreateOptions{},
	); err != nil {
		t.Fatalf("seed Sandbox: %v", err)
	}

	// Inject the exact transient class from the issue: a clustermesh
	// peer apiserver rejecting the catalyst-api token during a
	// region-kill recovery window surfaces as Unauthorized on the
	// dynamic client call.
	dyn.PrependReactor("delete", "sandboxes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("token rejected by peer apiserver")
	})

	core := fakek8s.NewSimpleClientset()
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: core, dyn: dyn}, nil
	})

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/sandbox/sessions/sandbox-flaky", nil)
	req = withSandboxClaims(req, "user-sub-flaky", "ops@acme.com", "acme")
	rec := callSandbox(t, h, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE on transient-unauthorized: status = %d, want 503 (sandbox-backend-unavailable); body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "sandbox-backend-unavailable" {
		t.Errorf("error = %v, want sandbox-backend-unavailable", resp["error"])
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Errorf("Retry-After header missing on sandbox 503 (#5140 retryable contract)")
	}
}

// TestSandboxIntegration_ListTransient503VsGenuine500 — #5140. Pins the
// full HTTP contract on the primary GET /api/v1/sandbox/sessions route:
//
//   - a TRANSIENT backend error (peer-apiserver token rejection during a
//     cutover / region-kill recovery window) → 503 with the Retry-After
//     back-off hint, so clients treat the hiccup as retryable;
//   - a GENUINE internal error (a fault the classifier does not
//     recognise as backend-unavailable) → 500 with NO Retry-After, so a
//     real server fault is never dressed up as a transient one.
func TestSandboxIntegration_ListTransient503VsGenuine500(t *testing.T) {
	cases := []struct {
		name          string
		injected      error
		wantCode      int
		wantError     string
		wantRetryHint bool
	}{
		{
			name:          "transient-unauthorized-degrades-503",
			injected:      apierrors.NewUnauthorized("token rejected by peer apiserver"),
			wantCode:      http.StatusServiceUnavailable,
			wantError:     "sandbox-backend-unavailable",
			wantRetryHint: true,
		},
		{
			name:          "genuine-fault-stays-500",
			injected:      errors.New("json: cannot unmarshal number into Go value"),
			wantCode:      http.StatusInternalServerError,
			wantError:     "list-failed",
			wantRetryHint: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			gvrToList := map[schema.GroupVersionResource]string{
				SandboxGVR(): "SandboxList",
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToList)
			dyn.PrependReactor("list", "sandboxes", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, tc.injected
			})

			core := fakek8s.NewSimpleClientset()
			h := NewWithPDM(silentLogger(), &fakePDM{})
			h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
				return &sovereignDeps{core: core, dyn: dyn}, nil
			})

			req := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil)
			req = withSandboxClaims(req, "user-sub-list", "ops@acme.com", "acme")
			rec := callSandbox(t, h, req)

			if rec.Code != tc.wantCode {
				t.Fatalf("LIST status = %d, want %d; body = %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			var resp map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if resp["error"] != tc.wantError {
				t.Errorf("error = %v, want %s", resp["error"], tc.wantError)
			}
			got := rec.Header().Get("Retry-After")
			if tc.wantRetryHint && got == "" {
				t.Errorf("Retry-After header missing on sandbox 503 (#5140 retryable contract)")
			}
			if !tc.wantRetryHint && got != "" {
				t.Errorf("Retry-After = %q on a genuine 500 — a real fault must not advertise retryability", got)
			}
		})
	}
}

// TestSandboxIntegration_OrgScopeIsolation — sandboxes in
// org-acme MUST NOT leak into org-beta's list. The handler scopes
// the apiserver Resource(...).Namespace(...) call by claims.Org;
// the fake namespacing enforces the boundary the same way the
// real apiserver does.
func TestSandboxIntegration_OrgScopeIsolation(t *testing.T) {
	h := newSandboxHandler(t,
		mkSandboxCR("sandbox-acme-1", "acme", "claude-code", nil),
		mkSandboxCR("sandbox-acme-2", "acme", "cursor-agent", nil),
		mkSandboxCR("sandbox-beta-1", "beta", "claude-code", nil),
	)

	// org=acme MUST see exactly the 2 acme sandboxes.
	acmeReq := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil)
	acmeReq = withSandboxClaims(acmeReq, "user-acme", "ops@acme.com", "acme")
	acmeRec := callSandbox(t, h, acmeReq)
	if acmeRec.Code != http.StatusOK {
		t.Fatalf("acme LIST: status %d, body=%s", acmeRec.Code, acmeRec.Body.String())
	}
	var acmeResp sandboxListResponse
	if err := json.Unmarshal(acmeRec.Body.Bytes(), &acmeResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(acmeResp.Sandboxes) != 2 {
		t.Errorf("acme sandbox count = %d, want 2", len(acmeResp.Sandboxes))
	}
	for _, s := range acmeResp.Sandboxes {
		if s.ID == "sandbox-beta-1" {
			t.Errorf("beta sandbox leaked into acme list: %+v", s)
		}
	}

	// org=beta MUST see exactly the 1 beta sandbox.
	betaReq := httptest.NewRequest(http.MethodGet, "/api/v1/sandbox/sessions", nil)
	betaReq = withSandboxClaims(betaReq, "user-beta", "ops@beta.com", "beta")
	betaRec := callSandbox(t, h, betaReq)
	if betaRec.Code != http.StatusOK {
		t.Fatalf("beta LIST: status %d, body=%s", betaRec.Code, betaRec.Body.String())
	}
	var betaResp sandboxListResponse
	if err := json.Unmarshal(betaRec.Body.Bytes(), &betaResp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(betaResp.Sandboxes) != 1 || betaResp.Sandboxes[0].ID != "sandbox-beta-1" {
		t.Errorf("beta sandboxes = %#v, want exactly [sandbox-beta-1]", betaResp.Sandboxes)
	}
}
