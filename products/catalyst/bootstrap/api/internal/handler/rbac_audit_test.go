// rbac_audit_test.go — coverage for the EPIC-3 (#1098) slice U8 audit
// handler:
//
//   - GET /audit/rbac listing endpoint (filter by audit-type, since,
//     actor; pagination; 503 when bus not wired)
//   - audit-emit on /rbac/assign create + update + no-op paths
//   - SSE /audit/rbac/stream connect + heartbeat (basic shape only —
//     deeper SSE tests sit under audit_test.go for the Bus itself)
package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
)

func registerRBACAuditRoutes(r chi.Router, h *Handler) {
	r.Get("/api/v1/sovereigns/{id}/audit/rbac", h.HandleRBACAuditList)
	r.Get("/api/v1/sovereigns/{id}/audit/rbac/stream", h.HandleRBACAuditStream)
}

func TestHandleRBACAuditList_503WhenBusNotWired(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	dep := installUserAccessDeployment(t, h, "dep-audit-503")

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRBACAuditList_FiltersAndPagination(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 50})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-list")

	// Seed 5 RBAC events on this sovereign + 1 unrelated event on another.
	for i := 0; i < 5; i++ {
		bus.Publish(context.Background(), audit.Event{
			AuditType:   audit.AuditTypeRBACGrantCreated,
			SovereignID: dep.ID,
			Actor:       "alice@acme.io",
			Tier:        "developer",
			Timestamp:   time.Now().UTC().Add(time.Duration(i) * time.Second),
		})
	}
	bus.Publish(context.Background(), audit.Event{
		AuditType:   audit.AuditTypeRBACGrantCreated,
		SovereignID: "other-sov",
		Actor:       "bob@acme.io",
	})

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	// First page (limit=3) should yield 3 items + nextOffset=3.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac?limit=3", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first page: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var page1 rbacAuditListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &page1); err != nil {
		t.Fatalf("decode page1: %v", err)
	}
	if len(page1.Items) != 3 {
		t.Errorf("page1 items: got %d want 3", len(page1.Items))
	}
	if page1.NextOffset != 3 {
		t.Errorf("page1 nextOffset: got %d want 3", page1.NextOffset)
	}
	if page1.Total != 5 {
		t.Errorf("page1 total: got %d want 5", page1.Total)
	}
	// All items must be from THIS sovereign.
	for _, item := range page1.Items {
		if item.SovereignID != dep.ID {
			t.Errorf("got cross-sovereign item: %+v", item)
		}
	}

	// Actor filter narrows.
	req2 := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac?actor=alice", nil)
	rec2 := httptest.NewRecorder()
	r.ServeHTTP(rec2, req2)
	var page2 rbacAuditListResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &page2); err != nil {
		t.Fatalf("decode page2: %v", err)
	}
	if page2.Total != 5 {
		t.Errorf("actor=alice total want 5; got %d", page2.Total)
	}

	// Actor filter excludes when no match.
	req3 := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac?actor=charlie", nil)
	rec3 := httptest.NewRecorder()
	r.ServeHTTP(rec3, req3)
	var page3 rbacAuditListResponse
	_ = json.Unmarshal(rec3.Body.Bytes(), &page3)
	if page3.Total != 0 {
		t.Errorf("actor=charlie total want 0; got %d", page3.Total)
	}
}

// TestHandleRBACAssign_EmitsCreatedEvent verifies the audit-emit on
// the create path. Mirrors the existing TestHandleRBACAssign_CreatesNewWhenNoMatch
// shape — uses the same fake dynamic client and Deployment install
// helpers. After a successful POST /rbac/assign, the audit Bus must
// hold one event of type AuditTypeRBACGrantCreated.
func TestHandleRBACAssign_EmitsCreatedEvent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, _ := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 10})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-create")

	body := rbacAssignRequest{
		User: rbacAssignUserBody{KeycloakSubject: "alice", Email: "alice@acme.io"},
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
	events := bus.List(dep.ID, audit.IsRBACAuditType, 10)
	if len(events) != 1 {
		t.Fatalf("emitted events: got %d want 1; events=%+v", len(events), events)
	}
	ev := events[0]
	if ev.AuditType != audit.AuditTypeRBACGrantCreated {
		t.Errorf("auditType: got %q want %q", ev.AuditType, audit.AuditTypeRBACGrantCreated)
	}
	if ev.TargetUser != "alice" {
		t.Errorf("targetUser: got %q want alice", ev.TargetUser)
	}
	if ev.TargetUserEmail != "alice@acme.io" {
		t.Errorf("targetUserEmail: got %q want alice@acme.io", ev.TargetUserEmail)
	}
	if ev.Tier != "developer" {
		t.Errorf("tier: got %q want developer", ev.Tier)
	}
	if ev.TargetApplication != "wordpress" {
		t.Errorf("targetApp: got %q want wordpress", ev.TargetApplication)
	}
}

// TestHandleRBACAssign_EmitsUpdatedAndTierChanged verifies that on the
// update path, BOTH AuditTypeRBACGrantUpdated and AuditTypeRBACTierChanged
// are emitted (because the tier actually moved).
func TestHandleRBACAssign_EmitsUpdatedAndTierChanged(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 10})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-update")

	// Pre-seed a UserAccess CR for "alice" with tier=viewer + same scope.
	scopes := []rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
	}
	existing := rbacUserAccessFromAssign("rbac-alice-deadbeef", "alice", "viewer", scopes)
	if _, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Create(
		context.Background(), existing, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed CR: %v", err)
	}

	body := rbacAssignRequest{
		User:  rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier:  "operator",
		Scope: scopes,
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	events := bus.List(dep.ID, audit.IsRBACAuditType, 10)
	// Expect 2 events: one rbac-grant-updated + one rbac-tier-changed.
	if len(events) != 2 {
		t.Fatalf("emitted events: got %d want 2; events=%+v", len(events), events)
	}
	types := map[string]bool{}
	for _, ev := range events {
		types[ev.AuditType] = true
		if ev.AuditType == audit.AuditTypeRBACTierChanged && ev.PreviousTier != "viewer" {
			t.Errorf("previousTier: got %q want viewer", ev.PreviousTier)
		}
	}
	if !types[audit.AuditTypeRBACGrantUpdated] {
		t.Errorf("missing rbac-grant-updated event; got %v", types)
	}
	if !types[audit.AuditTypeRBACTierChanged] {
		t.Errorf("missing rbac-tier-changed event; got %v", types)
	}
}

// TestHandleRBACAssign_NoOp_DoesNotEmit asserts that the no-op path
// (idempotent re-grant of the same tier on the same scope) does NOT
// emit an audit event.
func TestHandleRBACAssign_NoOp_DoesNotEmit(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	factory, client := fakeUserAccessDynamicFactory()
	h.dynamicFactory = factory
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 10})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-noop")

	scopes := []rbacAssignScopeBody{
		{Key: "openova.io/application", Value: "wordpress"},
	}
	existing := rbacUserAccessFromAssign("rbac-alice-feedface", "alice", "developer", scopes)
	if _, err := client.Resource(UserAccessGVR()).Namespace(rbacAssignNamespace).Create(
		context.Background(), existing, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed CR: %v", err)
	}

	body := rbacAssignRequest{
		User:  rbacAssignUserBody{KeycloakSubject: "alice"},
		Tier:  "developer",
		Scope: scopes,
	}
	rec := callUserAccess(t, h, http.MethodPost,
		"/api/v1/sovereigns/"+dep.ID+"/rbac/assign", body, registerRBACAssignRoute)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}

	events := bus.List(dep.ID, audit.IsRBACAuditType, 10)
	if len(events) != 0 {
		t.Errorf("no-op should not emit; got %d events: %+v", len(events), events)
	}
}

func TestHandleRBACAuditStream_HeartbeatAndConnect(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-stream")

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	srv := httptest.NewServer(r)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/v1/sovereigns/"+dep.ID+"/audit/rbac/stream", nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status: got %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("content-type: got %q want text/event-stream", got)
	}
	// Publish an event mid-stream to ensure SSE delivery works.
	go func() {
		time.Sleep(50 * time.Millisecond)
		bus.Publish(context.Background(), audit.Event{
			AuditType:   audit.AuditTypeRBACGrantCreated,
			SovereignID: dep.ID,
			Actor:       "alice@acme.io",
		})
	}()
	buf := make([]byte, 4096)
	got := bytes.Buffer{}
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			got.Write(buf[:n])
			if bytes.Contains(got.Bytes(), []byte("data:")) {
				break
			}
		}
		if err != nil {
			break
		}
	}
	if !bytes.Contains(got.Bytes(), []byte("connected sovereign=")) {
		t.Errorf("missing connect frame; got=%s", got.String())
	}
	if !bytes.Contains(got.Bytes(), []byte("data:")) {
		t.Errorf("missing data frame; got=%s", got.String())
	}
}

// ── qa-loop iter-1 prov #8 Fix #97 — compliance audit type filter ────

// TestIsComplianceAuditType verifies the compliance audit-type prefix
// predicate (TC-052). Matches every audit type with the canonical
// "compliance" prefix (compliance-policy-mode-changed, compliance-…)
// and rejects everything else.
func TestIsComplianceAuditType(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"compliance-policy-mode-changed", true},
		{"compliance-policy-deleted", true},
		{"compliance", true},
		{"continuum-switchover-completed", false},
		{"rbac-assign", false},
		{"", false},
		{"compliancex-something", true}, // prefix-only by design
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := IsComplianceAuditType(c.in); got != c.want {
				t.Fatalf("IsComplianceAuditType(%q): got %v want %v", c.in, got, c.want)
			}
		})
	}
}
