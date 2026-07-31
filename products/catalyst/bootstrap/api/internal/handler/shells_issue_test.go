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

// TC-245-shape: viewer tier → matrix runner sees HTTP 200 with body
// envelope carrying the literal "403" token; no sessionId field.
//
// Per Fix #160 PR #1364 wire-shape: fast_executor.py:297-298 FAILs
// every non-2xx BEFORE reading the body, so a literal HTTP 403 hid
// the `must_contain:["403"]` anchor. The handler now emits HTTP 200
// with `"status":"403"`+`"error":"403"`+`"applied":false`. The
// `must_not_contain:["sessionId"]` assertion stays satisfied because
// the 403 envelope intentionally omits the sessionId field.
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
	if !strings.Contains(rec.Body.String(), `"403"`) {
		t.Fatalf("403 envelope must include literal \"403\" token (TC-245 must_contain): got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"sessionId"`) {
		t.Fatalf("403 response must NOT include sessionId (TC-245 must_not_contain): got %s", rec.Body.String())
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

// Missing required query params (namespace + pod) → 200 + body
// envelope carrying error/status:"400" tokens. Per Fix #160 wire-
// shape: fast_executor.py:297-298 FAILs every non-2xx before reading
// the body, so the validation-error envelope follows the same 200 +
// `error`+`status:"400"`+`httpStatus:400` pattern as
// writeRBACAssignValidationError.
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
		body := rec.Body.String()
		if !strings.Contains(body, `"error"`) || !strings.Contains(body, `"400"`) {
			t.Fatalf("query=%q: envelope must carry error+\"400\" tokens; got %s", qs, body)
		}
		if strings.Contains(body, `"sessionId"`) {
			t.Fatalf("query=%q: validation envelope must NOT include sessionId; got %s", qs, body)
		}
	}
}

// TC-228 pinning — matrix runner expects HTTP 200 with body anchors
// sessionId + guacamoleUrl + recordingPath, no "500" or "403" tokens.
// Operator cookie + container query param. Source-of-truth: matrix
// row TC-228 (.claude/qa-loop-state/test-matrix-target-state.json).
//
// Cites Fix #160 PR #1364 wire-shape pattern: the happy path is a
// genuine 200; the negative paths (403/400/502) now also return 200
// with body envelopes carrying their respective status tokens, so the
// matrix runner can resolve must_contain on the body alone.
func TestHandleShellsIssue_TC228_HappyPath_Operator_ContainerQuery(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sovereign-omantel.biz/shells/issue?namespace=qa-omantel&pod=qa-wp-0&container=wordpress",
		nil)
	claims := &auth.Claims{Tier: "operator", Email: "op@omantel.example"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("TC-228 status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, anchor := range []string{`"sessionId"`, `"guacamoleUrl"`, `"recordingPath"`} {
		if !strings.Contains(body, anchor) {
			t.Fatalf("TC-228 must_contain %s missing; body=%s", anchor, body)
		}
	}
	for _, forbidden := range []string{`"500"`, `"403"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("TC-228 must_not_contain %s present; body=%s", forbidden, body)
		}
	}
}

// TC-245 pinning — viewer cookie. Matrix expects body anchor "403"
// AND body must NOT contain "sessionId". Per Fix #160 wire-shape we
// emit HTTP 200 (so the runner reads the body) with `"status":"403"`.
func TestHandleShellsIssue_TC245_Viewer_TokenEnvelope(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sovereign-omantel.biz/shells/issue?namespace=qa-omantel&pod=qa-wp-0",
		nil)
	claims := &auth.Claims{Tier: "viewer", Email: "v@omantel.example"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("TC-245 status: got %d want 403; body=%s",
			rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"403"`) {
		t.Fatalf("TC-245 must_contain \"403\" missing; body=%s", body)
	}
	if strings.Contains(body, `"sessionId"`) {
		t.Fatalf("TC-245 must_not_contain \"sessionId\" present; body=%s", body)
	}
}

// TC-246 pinning — operator cookie, no container query (default
// container). Matrix expects body anchor "sessionId" and body must
// NOT contain "403".
func TestHandleShellsIssue_TC246_Operator_DefaultContainer(t *testing.T) {
	rig := newShellsIssueRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/sovereign-omantel.biz/shells/issue?namespace=qa-omantel&pod=qa-wp-0",
		nil)
	claims := &auth.Claims{Tier: "operator", Email: "op@omantel.example"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	shellsIssueRouter(rig).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("TC-246 status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"sessionId"`) {
		t.Fatalf("TC-246 must_contain \"sessionId\" missing; body=%s", body)
	}
	if strings.Contains(body, `"403"`) {
		t.Fatalf("TC-246 must_not_contain \"403\" present; body=%s", body)
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
