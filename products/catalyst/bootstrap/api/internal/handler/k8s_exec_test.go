// k8s_exec_test.go — coverage for the EPIC-4 Slice E (#1099) endpoints:
//
//	POST /k8s/exec/.../session
//	GET  /sessions (paginated + filter + RBAC)
//	GET  /sessions/{id}/replay
//
// Strategy: stand up the bare Handler shell with an audit Bus (so
// emit-paths exercise) and chi router, then drive HTTP tests both with
// and without a wired GuacamoleClient. The in-memory fallback path is
// the canonical chroot Sovereign behaviour, so it must round-trip the
// session shape end-to-end.
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// quietK8sExecLogger discards log output for clean test runs.
func quietK8sExecLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// k8sExecRig is a thin Handler harness for /k8s/exec tests.
type k8sExecRig struct {
	h     *Handler
	bus   *audit.Bus
	guac  *fakeGuacamoleClient // nil when the test exercises the fallback
}

func newExecRig(t *testing.T, withGuac bool) *k8sExecRig {
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

func (r *k8sExecRig) router() chi.Router {
	rt := chi.NewRouter()
	rt.Post("/api/v1/sovereigns/{id}/k8s/exec/{ns}/{pod}/{container}/session", r.h.HandleK8sExecSession)
	rt.Get("/api/v1/sovereigns/{id}/sessions", r.h.HandleK8sSessionsList)
	rt.Get("/api/v1/sovereigns/{id}/sessions/{sessionId}/replay", r.h.HandleK8sSessionReplay)
	return rt
}

// fakeGuacamoleClient is the test stub for GuacamoleClient.
type fakeGuacamoleClient struct {
	createCalls []GuacamoleSessionParams
	listCalls   []GuacamoleListFilter
	replayCalls []string

	createErr error
	listErr   error
	replayErr error

	sessions []GuacamoleSession
	replay   GuacamoleReplay
}

func (f *fakeGuacamoleClient) CreateSession(_ string, p GuacamoleSessionParams) (GuacamoleSession, error) {
	f.createCalls = append(f.createCalls, p)
	if f.createErr != nil {
		return GuacamoleSession{}, f.createErr
	}
	sess := GuacamoleSession{
		SessionID:    "sess-" + p.Pod,
		ConnectionID: "conn-" + p.Pod,
		EmbedURL:     "https://guac.test/#/client/conn-" + p.Pod,
		Recording:    true,
		Started:      time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		User:         p.User,
		Namespace:    p.Namespace,
		Pod:          p.Pod,
		Container:    p.Container,
	}
	f.sessions = append(f.sessions, sess)
	return sess, nil
}

func (f *fakeGuacamoleClient) ListSessions(_ string, filter GuacamoleListFilter) ([]GuacamoleSession, int, error) {
	f.listCalls = append(f.listCalls, filter)
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	out := make([]GuacamoleSession, 0, len(f.sessions))
	for _, ss := range f.sessions {
		if filter.Pod != "" && ss.Pod != filter.Pod {
			continue
		}
		if filter.User != "" && ss.User != filter.User {
			continue
		}
		out = append(out, ss)
	}
	total := len(out)
	if filter.Offset >= len(out) {
		return nil, total, nil
	}
	out = out[filter.Offset:]
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, total, nil
}

func (f *fakeGuacamoleClient) GetReplay(_, sessionID string) (GuacamoleReplay, error) {
	f.replayCalls = append(f.replayCalls, sessionID)
	if f.replayErr != nil {
		return GuacamoleReplay{}, f.replayErr
	}
	if f.replay.EmbedURL != "" || f.replay.Reason != "" {
		return f.replay, nil
	}
	for _, ss := range f.sessions {
		if ss.SessionID == sessionID {
			return GuacamoleReplay{
				EmbedURL:  "https://guac.test/#/replay/" + sessionID,
				Available: true,
			}, nil
		}
	}
	return GuacamoleReplay{Available: false, Reason: "not-found"}, nil
}

// ── POST /session ────────────────────────────────────────────────────

func TestHandleK8sExecSession_HappyPath_WithGuac(t *testing.T) {
	rig := newExecRig(t, true)
	body, _ := json.Marshal(k8sExecSessionRequest{Command: []string{"/bin/bash"}})
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/alpha/k8s/exec/default/wp-1/web/session",
		bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	claims := &auth.Claims{Tier: "developer", Email: "alice@example.com"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))

	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got k8sExecSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SessionID != "sess-wp-1" {
		t.Fatalf("sessionId: got %q want sess-wp-1", got.SessionID)
	}
	if got.EmbedURL == "" {
		t.Fatalf("embedURL must not be empty")
	}
	if got.FallbackWebSocketURL == "" {
		t.Fatalf("fallback URL must be populated for E2 contract")
	}
	if !got.Recording {
		t.Fatalf("recording flag must be true when Guacamole is wired")
	}
	// Verify the audit emit.
	events := rig.bus.List("alpha", IsGuacamoleAuditType, 10)
	if len(events) != 1 || events[0].AuditType != AuditTypeGuacamoleSessionOpened {
		t.Fatalf("audit: got %#v want guacamole-session-opened", events)
	}
	if rig.guac.createCalls[0].Container != "web" {
		t.Fatalf("container forwarded as %q want web", rig.guac.createCalls[0].Container)
	}
}

func TestHandleK8sExecSession_FallbackPath_NoGuacWired(t *testing.T) {
	rig := newExecRig(t, false)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/beta/k8s/exec/ns/pod/c/session", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got k8sExecSessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EmbedURL == "" {
		t.Fatalf("synthesized embedURL must not be empty")
	}
	if !strings.HasPrefix(got.EmbedURL, "https://guacamole.") {
		t.Fatalf("embedURL must use guacamole.<sov-fqdn> shape: got %q", got.EmbedURL)
	}
	if got.Recording {
		t.Fatalf("recording must be false in in-memory fallback")
	}
	if got.FallbackWebSocketURL == "" {
		t.Fatalf("fallback URL must be populated")
	}
}

func TestHandleK8sExecSession_RBACForbidden_Viewer(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/alpha/k8s/exec/default/wp-1/web/session", nil)
	claims := &auth.Claims{Tier: "viewer"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; body=%s", rec.Code, rec.Body.String())
	}
	if len(rig.guac.createCalls) != 0 {
		t.Fatalf("create must not be called on viewer-tier denial")
	}
}

func TestHandleK8sExecSession_RBACAllowed_Developer(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/alpha/k8s/exec/default/wp-1/web/session", nil)
	claims := &auth.Claims{Tier: "developer"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleK8sExecSession_RBACAllowed_Admin(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/alpha/k8s/exec/default/wp-1/web/session", nil)
	claims := &auth.Claims{Tier: "admin"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
}

func TestHandleK8sExecSession_GuacError_BadGateway(t *testing.T) {
	rig := newExecRig(t, true)
	rig.guac.createErr = errors.New("guac is down")
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/alpha/k8s/exec/default/wp-1/web/session", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleK8sExecSession_DefaultsCommandToShell(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/alpha/k8s/exec/default/wp-1/web/session", nil)
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	if got := rig.guac.createCalls[0].Command; len(got) != 1 || got[0] != "/bin/sh" {
		t.Fatalf("default command: got %v want [/bin/sh]", got)
	}
}

// ── GET /sessions list ───────────────────────────────────────────────

func TestHandleK8sSessionsList_HappyPath_Pagination(t *testing.T) {
	rig := newExecRig(t, true)
	// Seed 3 sessions.
	for i := 0; i < 3; i++ {
		rig.guac.sessions = append(rig.guac.sessions, GuacamoleSession{
			SessionID: fmt.Sprintf("s-%d", i),
			Pod:       fmt.Sprintf("p-%d", i),
			User:      "alice@example.com",
			Started:   time.Date(2026, 5, 9, 12, i, 0, 0, time.UTC),
			RecordingAvailable: true,
		})
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/alpha/sessions?pageSize=2&page=1", nil)
	claims := &auth.Claims{Tier: "admin"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got k8sExecSessionList
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Total != 3 {
		t.Fatalf("total: got %d want 3", got.Total)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items: got %d want 2", len(got.Items))
	}
	if got.NextPage != 2 {
		t.Fatalf("nextPage: got %d want 2", got.NextPage)
	}
}

func TestHandleK8sSessionsList_FilterByPod(t *testing.T) {
	rig := newExecRig(t, true)
	rig.guac.sessions = []GuacamoleSession{
		{SessionID: "a", Pod: "wp-1", Started: time.Now()},
		{SessionID: "b", Pod: "api-2", Started: time.Now()},
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/alpha/sessions?pod=wp-1", nil)
	claims := &auth.Claims{Tier: "admin"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	var got k8sExecSessionList
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got.Items) != 1 || got.Items[0].SessionID != "a" {
		t.Fatalf("filter: got %v want [a]", got.Items)
	}
}

func TestHandleK8sSessionsList_RBACForbidden_Viewer(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/sessions", nil)
	claims := &auth.Claims{Tier: "viewer"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403", rec.Code)
	}
}

func TestHandleK8sSessionsList_RBACForbidden_Developer(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/sessions", nil)
	claims := &auth.Claims{Tier: "developer"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; developer should not see /sessions", rec.Code)
	}
}

func TestHandleK8sSessionsList_BadFromTimestamp_400(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/alpha/sessions?from=NOT-A-DATE", nil)
	claims := &auth.Claims{Tier: "admin"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400", rec.Code)
	}
}

// ── GET /sessions/{id}/replay ────────────────────────────────────────

func TestHandleK8sSessionReplay_HappyPath(t *testing.T) {
	rig := newExecRig(t, true)
	rig.guac.sessions = []GuacamoleSession{
		{SessionID: "s-1", Pod: "p-1", Started: time.Now(), RecordingAvailable: true},
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/alpha/sessions/s-1/replay", nil)
	claims := &auth.Claims{Tier: "admin"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got k8sExecSessionReplay
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Available || got.EmbedURL == "" {
		t.Fatalf("replay: got %#v want {available:true, embedURL:non-empty}", got)
	}
}

func TestHandleK8sSessionReplay_RBACForbidden_TierAdminInsufficient_Developer(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/alpha/sessions/s-1/replay", nil)
	claims := &auth.Claims{Tier: "developer"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status: got %d want 403; replay requires admin/owner only", rec.Code)
	}
}

func TestHandleK8sSessionReplay_NotFound_404_Fallback(t *testing.T) {
	rig := newExecRig(t, false)
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/alpha/sessions/missing/replay", nil)
	claims := &auth.Claims{Tier: "admin"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d want 404", rec.Code)
	}
}

func TestHandleK8sSessionReplay_AuditEmit(t *testing.T) {
	rig := newExecRig(t, true)
	rig.guac.sessions = []GuacamoleSession{{SessionID: "s-1", Pod: "p", RecordingAvailable: true}}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/alpha/sessions/s-1/replay", nil)
	claims := &auth.Claims{Tier: "admin", Email: "alice@example.com"}
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
	rec := httptest.NewRecorder()
	rig.router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rec.Code)
	}
	events := rig.bus.List("alpha", IsGuacamoleAuditType, 10)
	if len(events) != 1 || events[0].AuditType != AuditTypeGuacamoleSessionReplayed {
		t.Fatalf("audit: got %#v want guacamole-session-replayed", events)
	}
}

// ── Audit predicate ──────────────────────────────────────────────────

func TestIsGuacamoleAuditType(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{AuditTypeGuacamoleSessionOpened, true},
		{AuditTypeGuacamoleSessionClosed, true},
		{AuditTypeGuacamoleSessionReplayed, true},
		{audit.AuditTypeRBACGrantCreated, false},
		{"continuum-switchover-requested", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsGuacamoleAuditType(c.name); got != c.want {
			t.Errorf("IsGuacamoleAuditType(%q) = %v want %v", c.name, got, c.want)
		}
	}
}

// ── In-memory store unit coverage ────────────────────────────────────

func TestInMemoryGuacamoleStore_FilterAndPaginate(t *testing.T) {
	store := newInMemoryGuacamoleStore()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		store.Add("alpha", GuacamoleSession{
			SessionID: fmt.Sprintf("s-%d", i),
			Pod:       fmt.Sprintf("p-%d", i%2),
			User:      []string{"alice", "bob"}[i%2],
			Started:   now.Add(time.Duration(i) * time.Minute),
		})
	}
	// Pod filter
	got, total := store.List("alpha", GuacamoleListFilter{Pod: "p-0", Limit: 10})
	if total != 3 || len(got) != 3 {
		t.Fatalf("pod filter: got total=%d items=%d want 3/3", total, len(got))
	}
	// User filter + paginate
	got, total = store.List("alpha", GuacamoleListFilter{User: "alice", Offset: 1, Limit: 1})
	if total != 3 {
		t.Fatalf("user total: got %d want 3", total)
	}
	if len(got) != 1 {
		t.Fatalf("paginate: got %d want 1", len(got))
	}
	// Newest-first ordering
	got, _ = store.List("alpha", GuacamoleListFilter{Limit: 5})
	for i := 0; i < len(got)-1; i++ {
		if got[i].Started.Before(got[i+1].Started) {
			t.Fatalf("ordering: %v is before %v", got[i].Started, got[i+1].Started)
		}
	}
	// Get
	if _, ok := store.Get("alpha", "s-0"); !ok {
		t.Fatalf("Get must find s-0")
	}
	if _, ok := store.Get("alpha", "missing"); ok {
		t.Fatalf("Get must miss missing")
	}
}

// ── execSessionCallerAuthorized predicate ────────────────────────────

func TestExecSessionCallerAuthorized(t *testing.T) {
	cases := []struct {
		name   string
		claims *auth.Claims
		want   bool
	}{
		{"nil", nil, true}, // lenient fall-through
		{"viewer", &auth.Claims{Tier: "viewer"}, false},
		{"developer", &auth.Claims{Tier: "developer"}, true},
		{"operator", &auth.Claims{Tier: "operator"}, true},
		{"admin", &auth.Claims{Tier: "admin"}, true},
		{"owner", &auth.Claims{Tier: "owner"}, true},
		{"DEVELOPER-mixed-case", &auth.Claims{Tier: "DEVELOPER"}, true},
		{"unknown", &auth.Claims{Tier: "wat"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := execSessionCallerAuthorized(c.claims); got != c.want {
				t.Errorf("got %v want %v", got, c.want)
			}
		})
	}
}
