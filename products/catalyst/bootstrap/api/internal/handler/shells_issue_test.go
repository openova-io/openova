// shells_issue_test.go — coverage for the matrix-canonical
// /shells/issue surface. Same business logic as HandleK8sExecSession
// (k8s_exec_test.go), so the tests focus on URL-shape + response-field
// vocabulary parity with the qa-loop test matrix
// (TC-228 / TC-230 / TC-245 / TC-246).

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

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// shellsIssueRig mirrors k8sExecRig but mounts the matrix-canonical
// /shells/issue route. Reuses the existing fakeGuacamoleClient stub.
func newShellsIssueRig(t *testing.T, withGuac bool) *k8sExecRig {
	t.Helper()
	h := NewWithPDM(quietK8sExecLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 100})
	h.SetAuditBus(bus)
	rig := &k8sExecRig{h: h, bus: bus}
	if withGuac {
		rig.guac = &fakeGuacamoleClient{}
		h.SetGuacamoleClient(rig.guac)
	}
	return rig
}

func shellsIssueRouter(rig *k8sExecRig) chi.Router {
	rt := chi.NewRouter()
	rt.Post("/api/v1/sovereigns/{id}/shells/issue", rig.h.HandleShellsIssue)
	return rt
}

// TC-246-shape: operator cookie → 200 with sessionId + guacamoleUrl +
// recordingPath. Validates the URL form + response-field vocabulary
// match the qa-loop matrix verbatim.
func TestHandleShellsIssue_HappyPath_OperatorCookie(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sov-omantel/shells/issue?namespace=qa-omantel&pod=qa-wp-0&container=wordpress",
		nil)
	claims := &auth.Claims{Tier: "operator", Email: "op@omantel.example"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got shellsIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Matrix-canonical fields. Each missing field would be a TC FAIL.
	if got.SessionID == "" {
		t.Fatalf("sessionId: must not be empty")
	}
	if got.GuacamoleURL == "" {
		t.Fatalf("guacamoleUrl: must not be empty")
	}
	if got.RecordingPath == "" {
		t.Fatalf("recordingPath: must not be empty")
	}
	if !strings.HasPrefix(got.RecordingPath, "/recordings/") {
		t.Fatalf("recordingPath must start with /recordings/: got %q", got.RecordingPath)
	}
	if got.Namespace != "qa-omantel" || got.Pod != "qa-wp-0" || got.Container != "wordpress" {
		t.Fatalf("path metadata not echoed: %+v", got)
	}
	// Audit row must land on the same guacamole-session-opened type so
	// SREs see one stream regardless of URL shape.
	events := rig.bus.List("sov-omantel", IsGuacamoleAuditType, 10)
	if len(events) != 1 || events[0].AuditType != AuditTypeGuacamoleSessionOpened {
		t.Fatalf("audit: got %#v want guacamole-session-opened", events)
	}
}

// TC-245-shape: viewer tier → 403, no sessionId.
func TestHandleShellsIssue_RBAC_Viewer_403(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sov-omantel/shells/issue?namespace=qa-omantel&pod=qa-wp-0",
		nil)
	claims := &auth.Claims{Tier: "viewer", Email: "v@omantel.example"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"sessionId"`) {
		t.Fatalf("403 response must NOT include sessionId: got %s", rec.Body.String())
	}
}

// In-memory fallback (no Guacamole wired) — same matrix-canonical
// fields populated from the synthesized session.
func TestHandleShellsIssue_FallbackPath_NoGuacWired(t *testing.T) {
	rig := newShellsIssueRig(t, false)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sov-beta/shells/issue?namespace=ns&pod=pod1&container=app",
		nil)
	claims := &auth.Claims{Tier: "developer", Email: "dev@example.com"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got shellsIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SessionID == "" {
		t.Fatalf("synthesized session must populate sessionId")
	}
	if !strings.HasPrefix(got.GuacamoleURL, "https://guacamole.") {
		t.Fatalf("guacamoleUrl must use guacamole.<sov-fqdn> shape: got %q", got.GuacamoleURL)
	}
	if !strings.HasPrefix(got.RecordingPath, "/recordings/") {
		t.Fatalf("recordingPath must start with /recordings/: got %q", got.RecordingPath)
	}
}

// Container is optional per the matrix's TC-245/246 (no container
// query param) — handler must accept the request and forward an empty
// container field through to the underlying GuacamoleClient.
func TestHandleShellsIssue_ContainerOptional(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sov-omantel/shells/issue?namespace=qa-omantel&pod=qa-wp-0",
		nil)
	claims := &auth.Claims{Tier: "developer", Email: "d@example.com"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got shellsIssueResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Container != "" {
		t.Fatalf("container should remain empty when not in query: got %q", got.Container)
	}
}

// Missing required query params (namespace + pod) → 400.
func TestHandleShellsIssue_MissingQueryParams_400(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	for _, qs := range []string{
		"",                          // both missing
		"?namespace=qa-omantel",     // pod missing
		"?pod=qa-wp-0",              // namespace missing
	} {
		req := httptest.NewRequest(http.MethodPost,
			"/api/v1/sovereigns/sov-omantel/shells/issue"+qs, nil)
		claims := &auth.Claims{Tier: "developer", Email: "d@example.com"}
		req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

		rec := httptest.NewRecorder()
		shellsIssueRouter(rig).ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query=%q: status got %d want 400; body=%s", qs, rec.Code, rec.Body.String())
		}
	}
}

// Optional JSON body with a custom command argv flows through to the
// underlying GuacamoleClient (parity with HandleK8sExecSession).
func TestHandleShellsIssue_BodyCommand_FlowsThrough(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	body, _ := json.Marshal(k8sExecSessionRequest{Command: []string{"/bin/zsh"}})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sov-omantel/shells/issue?namespace=ns&pod=pod1&container=c",
		bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	claims := &auth.Claims{Tier: "developer", Email: "d@example.com"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(rig.guac.createCalls) != 1 {
		t.Fatalf("guacamole CreateSession should fire once; got %d", len(rig.guac.createCalls))
	}
	if got := rig.guac.createCalls[0].Command; len(got) != 1 || got[0] != "/bin/zsh" {
		t.Fatalf("command argv not forwarded: got %#v", got)
	}
}
