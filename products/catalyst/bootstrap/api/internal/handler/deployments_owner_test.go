// Tests for the per-deployment ownership check (issue #689).
//
// The contract: a signed-in operator who is NOT the original creator
// MUST receive 404 (NEVER 403) on every /deployments/{id}* read or
// mutate handler. Empty OwnerEmail (legacy pre-#689 record) MUST be
// treated as "passthrough — skip". Empty session header (test/CI
// environment without RequireSession middleware) MUST also be treated
// as passthrough so existing tests run unchanged.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/provisioner"
)

// newDepWithOwner constructs a Deployment with the given OwnerEmail and
// registers it in h.deployments under a synthetic id. Returns the id so
// the caller can build URLs against it.
func newDepWithOwner(h *Handler, owner string) string {
	id := "dep-owner-test-" + owner
	if id == "dep-owner-test-" {
		id = "dep-owner-test-empty"
	}
	dep := &Deployment{
		ID:         id,
		Status:     "ready",
		Request:    provisioner.Request{SovereignFQDN: "test.omani.works"},
		StartedAt:  time.Now(),
		eventsCh:   make(chan provisioner.Event),
		done:       make(chan struct{}),
		OwnerEmail: owner,
	}
	close(dep.eventsCh)
	close(dep.done)
	h.deployments.Store(id, dep)
	return id
}

// chiReq builds an *http.Request with chi's URLParam("id") set so a
// handler that reads chi.URLParam(r, "id") resolves the synthetic id
// without needing a full router setup.
func chiReq(method, url, id string) *http.Request {
	r := httptest.NewRequest(method, url, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// TestCheckOwnership_LegacyRecord proves that a Deployment with empty
// OwnerEmail (a record persisted before #689 landed) passes the check
// even when the request carries a session email. Required for in-place
// upgrades not to break.
func TestCheckOwnership_LegacyRecord(t *testing.T) {
	h := &Handler{log: slog.Default()}
	id := newDepWithOwner(h, "")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id, nil)
	r.Header.Set("X-User-Email", "anyone@example.com")
	val, _ := h.deployments.Load(id)
	if !h.checkOwnership(w, r, val.(*Deployment)) {
		t.Fatalf("legacy record (empty OwnerEmail) must pass; got body %s", w.Body.String())
	}
}

// TestCheckOwnership_NoSession proves a request with no session header
// (CI / tests / Sovereign-side bootstrap without RequireSession in the
// chain) passes through. The deployment has an owner; the request has
// no session — we must not block it because off-prod environments are
// allowed to bypass the check.
func TestCheckOwnership_NoSession(t *testing.T) {
	h := &Handler{log: slog.Default()}
	id := newDepWithOwner(h, "real-owner@example.com")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id, nil)
	val, _ := h.deployments.Load(id)
	if !h.checkOwnership(w, r, val.(*Deployment)) {
		t.Fatalf("no session header must pass through; got body %s", w.Body.String())
	}
}

// TestCheckOwnership_OwnerMatch proves that the original creator is
// allowed through. Case-insensitive comparison so a user who registered
// as Foo@Example.com but sends a session that carries foo@example.com
// still passes.
func TestCheckOwnership_OwnerMatch(t *testing.T) {
	h := &Handler{log: slog.Default()}
	id := newDepWithOwner(h, "Foo@Example.com")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id, nil)
	r.Header.Set("X-User-Email", "foo@example.com")
	val, _ := h.deployments.Load(id)
	if !h.checkOwnership(w, r, val.(*Deployment)) {
		t.Fatalf("owner-match (case-insensitive) must pass; got body %s", w.Body.String())
	}
}

// TestCheckOwnership_RejectsCrossTenantWith404 proves the canonical
// rejection path: a different signed-in user gets 404, NEVER 403. The
// 404 is the load-bearing assertion — switching to 403 would leak
// existence via the response code.
func TestCheckOwnership_RejectsCrossTenantWith404(t *testing.T) {
	h := &Handler{log: slog.Default()}
	id := newDepWithOwner(h, "owner@example.com")

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id, nil)
	r.Header.Set("X-User-Email", "intruder@example.com")
	val, _ := h.deployments.Load(id)
	if h.checkOwnership(w, r, val.(*Deployment)) {
		t.Fatalf("cross-tenant must be rejected; got pass")
	}
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant rejection MUST be 404 (not 403 — never leak existence); got %d", w.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("body did not decode as JSON: %v", err)
	}
	if body["error"] != "deployment not found" {
		t.Fatalf("body must say 'deployment not found' (NOT 'forbidden' or similar); got %q", body["error"])
	}
}

// TestGetDeployment_OwnerSeesState proves the wired-in handler path:
// GetDeployment returns 200 with the State() body when the session
// matches OwnerEmail. End-to-end through the chi-routed handler.
func TestGetDeployment_OwnerSeesState(t *testing.T) {
	h := &Handler{log: slog.Default()}
	id := newDepWithOwner(h, "owner@example.com")

	w := httptest.NewRecorder()
	r := chiReq(http.MethodGet, "/api/v1/deployments/"+id, id)
	r.Header.Set("X-User-Email", "owner@example.com")
	h.GetDeployment(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("owner GET must succeed; got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["id"] != id {
		t.Errorf("body.id mismatch: got %v want %s", body["id"], id)
	}
	if body["ownerEmail"] != "owner@example.com" {
		t.Errorf("State() must surface ownerEmail; got %v", body["ownerEmail"])
	}
}

// TestGetDeployment_CrossTenantReturns404 proves the wired-in handler
// path emits 404 for cross-tenant access. The 404 distinction is the
// load-bearing assertion of issue #689 — switching to 403 leaks
// existence.
func TestGetDeployment_CrossTenantReturns404(t *testing.T) {
	h := &Handler{log: slog.Default()}
	id := newDepWithOwner(h, "owner@example.com")

	w := httptest.NewRecorder()
	r := chiReq(http.MethodGet, "/api/v1/deployments/"+id, id)
	r.Header.Set("X-User-Email", "intruder@example.com")
	h.GetDeployment(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant GET MUST be 404 (not 403); got %d body=%s", w.Code, w.Body.String())
	}
}

// TestGetDeploymentEvents_CrossTenantReturns404 proves the same
// 404-not-403 contract for the buffered events surface. The wizard's
// FailureCard polls this endpoint.
func TestGetDeploymentEvents_CrossTenantReturns404(t *testing.T) {
	h := &Handler{log: slog.Default()}
	id := newDepWithOwner(h, "owner@example.com")

	w := httptest.NewRecorder()
	r := chiReq(http.MethodGet, "/api/v1/deployments/"+id+"/events", id)
	r.Header.Set("X-User-Email", "intruder@example.com")
	h.GetDeploymentEvents(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant /events MUST be 404; got %d body=%s", w.Code, w.Body.String())
	}
}
