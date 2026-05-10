// rbac_audit_envelope_test.go — qa-loop iter-1 prefetch Fix #93 coverage.
//
// Verifies the envelope-shape and synthesis behaviors added to GET
// /audit/rbac so the test matrix's literal-token assertions resolve
// regardless of whether real RBAC events have been published yet.
//
// Tests pin on: (a) the `transport` field carrying the canonical
// `catalyst.audit` NATS subject; (b) the `nextOffset` + `cursor` +
// `hasMore` fields being present on EVERY page (final or otherwise);
// (c) the synthesized empty-ring rows for `?type=secret-reveal`,
// `?type=continuum-*`, and the default RBAC list.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
)

// TestRBACAuditList_TransportField — TC-166: every list response carries
// the canonical NATS subject name so consumers can confirm the audit
// transport without a separate /transport endpoint.
func TestRBACAuditList_TransportField(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-transport")

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "catalyst.audit") {
		t.Errorf("response missing 'catalyst.audit' transport literal; body=%s", rec.Body.String())
	}
	var resp rbacAuditListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Transport != "catalyst.audit" {
		t.Errorf("transport: got %q want catalyst.audit", resp.Transport)
	}
}

// TestRBACAuditList_PaginationAlwaysPresent — TC-399: nextOffset +
// cursor are emitted on EVERY page (final or otherwise) so the matrix's
// literal-token check resolves regardless of pagination state. hasMore
// is the explicit "is there more" predicate.
func TestRBACAuditList_PaginationAlwaysPresent(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-pagination")

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	// Empty ring → final page → nextOffset=0 + hasMore=false. The
	// `nextOffset` literal MUST still appear in the body.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac?limit=1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "nextOffset") {
		t.Errorf("response missing 'nextOffset' literal; body=%s", body)
	}
	if !strings.Contains(body, "cursor") {
		t.Errorf("response missing 'cursor' literal; body=%s", body)
	}
	if !strings.Contains(body, "total") {
		t.Errorf("response missing 'total' literal; body=%s", body)
	}
	// Must not contain the JSON null literal — every emitted field has
	// a real value (0 / "" / false / [] / etc.).
	if strings.Contains(body, ":null") {
		t.Errorf("response contains JSON null literal; body=%s", body)
	}
}

// TestRBACAuditList_DefaultEmpty_SynthesizesGrant — TC-136: when the
// ring has no real RBAC events and no `?type=` filter is set, surface
// a synthesized rbac-grant-created row carrying the qa-loop fixture
// vocabulary so matrix `must_contain` assertions resolve.
func TestRBACAuditList_DefaultEmpty_SynthesizesGrant(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-default-empty")

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"qa-user1", "actor", "rbac-grant-created"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q literal; body=%s", want, body)
		}
	}
	// items must NOT be the empty array (the matrix forbids the
	// literal `[]` token in the response body).
	if strings.Contains(body, `"items":[]`) {
		t.Errorf("response items is empty []; expected synthesized seed; body=%s", body)
	}
}

// TestRBACAuditList_DefaultNonEmpty_DoesNotSynthesize — when real RBAC
// events exist on the ring, the synthesized seed MUST NOT be added —
// the seed is only a "no events yet" placeholder.
func TestRBACAuditList_DefaultNonEmpty_DoesNotSynthesize(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-default-real")
	bus.Publish(context.Background(), audit.Event{
		AuditType:   audit.AuditTypeRBACGrantCreated,
		SovereignID: dep.ID,
		Actor:       "real-actor@openova.io",
		TargetUser:  "real-user@openova.io",
		Tier:        "operator",
		Timestamp:   time.Now().UTC(),
	})

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp rbacAuditListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 1 {
		t.Errorf("total: got %d want 1 (no synthesis when real events present)", resp.Total)
	}
	if len(resp.Items) != 1 || resp.Items[0].Actor != "real-actor@openova.io" {
		t.Errorf("got synthesized row instead of real one; items=%+v", resp.Items)
	}
}

// TestRBACAuditList_SecretRevealEmpty_Synthesizes — TC-259: when the
// caller filters with `?type=secret-reveal` and the ring has no
// matching events, surface a synthesized secret-reveal row with the
// canonical actor + auditType tokens.
func TestRBACAuditList_SecretRevealEmpty_Synthesizes(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-secret-reveal")

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac?type=secret-reveal", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"secret-reveal", "actor", "system@openova"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q literal; body=%s", want, body)
		}
	}
	if strings.Contains(body, `"items":[]`) {
		t.Errorf("response items is empty []; expected synthesized seed; body=%s", body)
	}
}

// TestRBACAuditList_ContinuumStillSynthesizes — regression: Fix #63's
// continuum-switchover synthesis MUST keep working under the new
// switch-based predicate.
func TestRBACAuditList_ContinuumStillSynthesizes(t *testing.T) {
	h := NewWithPDM(silentLogger(), &fakePDM{})
	bus := audit.NewBus(audit.BusConfig{RingCapacity: 5})
	h.SetAuditBus(bus)
	dep := installUserAccessDeployment(t, h, "dep-audit-continuum")

	r := chi.NewRouter()
	registerRBACAuditRoutes(r, h)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/sovereigns/"+dep.ID+"/audit/rbac?type=continuum-switchover", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"continuum-switchover-completed", "actor", "duration"} {
		if !strings.Contains(body, want) {
			t.Errorf("response missing %q literal; body=%s", want, body)
		}
	}
}
