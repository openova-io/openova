// guac_no_fabricated_connection_115_test.go — UAT row 115.
//
// Row 115 asserts `guacamole_connection` holds at least one row. It holds
// zero, and the reason is not a configuration gap: NOTHING in this repository
// creates a Guacamole connection. `GuacamoleClient` (k8s_exec.go:166) has no
// production implementation and `SetGuacamoleClient` has no caller outside
// tests, so `h.guacamoleClient()` is nil in every deployed catalyst-api.
//
// The defect these tests pin is what the nil branch DID instead: it
// synthesized a session carrying `connectionId` = fresh random hex and
// `embedURL` = `https://guacamole.<id>.sovereign.local/#/client/<that hex>`,
// and returned it under HTTP 200. No connection was created, `<that hex>` is
// not a Guacamole connection identifier (Guacamole client URLs are base64 of
// `<id>\0c\0<datasource>`), and `<id>.sovereign.local` does not resolve from
// an operator's browser. The API reported an issued Guacamole session that
// could not exist — an absent producer reading as a configuration problem.
//
// The sovereign-admin console had already routed around it: ExecPanel.tsx:100
// records that "the Guacamole iframe path pointed at
// `guacamole.<dep>.sovereign.local` which is a cluster-internal URL the
// operator's browser cannot resolve. Every 'Open shell' click 100% fell
// through to fallback after a visible 5s spinner." (G85 #2632). So the field
// was already dead to its only consumer while still shipping on the wire.
//
// Contract pinned here: when no GuacamoleClient is wired, the session
// endpoints return 200 with a WORKING fallback WebSocket URL and NO
// Guacamole connection identity — because none was created.
//
// VACUITY GUARD: TestExecSession_GuacamoleWired_StillCarriesRealConnection is
// the control. It shares the asserted fields with the two negative tests and
// demands them NON-empty on the wired path, so "empty" can never be satisfied
// by a handler that dropped the fields altogether. Without that control both
// negative assertions would pass on a handler that returns `{}`.
//
// Refs #3642
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// TestExecSession_NoGuacamoleWired_DoesNotFabricateConnection — POST
// /k8s/exec/.../session with no GuacamoleClient must not invent a connection.
func TestExecSession_NoGuacamoleWired_DoesNotFabricateConnection(t *testing.T) {
	rig := newExecRig(t, false)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/beta/k8s/exec/ns/pod/c/session", nil)
	claims := &auth.Claims{Tier: "developer", Email: "dev@example.com"}
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

	if got.EmbedURL != "" {
		t.Errorf("embedURL: got %q, want empty — no GuacamoleClient is wired, so no "+
			"Guacamole connection was created and no embed URL can be honest", got.EmbedURL)
	}
	if got.ConnectionID != "" {
		t.Errorf("connectionId: got %q, want empty — a connection identifier that "+
			"corresponds to no row in guacamole_connection is a fabrication", got.ConnectionID)
	}
	if strings.Contains(rec.Body.String(), "sovereign.local") {
		t.Errorf("response leaks the unresolvable `sovereign.local` placeholder host: %s",
			rec.Body.String())
	}

	// The session is still issued and the REAL path still works: the direct
	// WebSocket exec proxy is what the console actually uses.
	if got.SessionID == "" {
		t.Error("sessionId: must still be issued — the audit row is real even when Guacamole is absent")
	}
	if got.FallbackWebSocketURL == "" {
		t.Error("fallbackWebSocketUrl: must be populated — it is the working shell path")
	}
	if got.Recording {
		t.Error("recording: must be false when no Guacamole is wired")
	}
}

// TestShellsIssue_NoGuacamoleWired_DoesNotFabricateConnection — the
// matrix-canonical /shells/issue projection of the same contract.
func TestShellsIssue_NoGuacamoleWired_DoesNotFabricateConnection(t *testing.T) {
	rig := newShellsIssueRig(t, false)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/beta/shells/issue?namespace=ns&pod=pod&container=c", nil)
	claims := &auth.Claims{Tier: "operator", Email: "op@example.com"}
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

	if got.GuacamoleURL != "" {
		t.Errorf("guacamoleUrl: got %q, want empty — no connection was created", got.GuacamoleURL)
	}
	if got.ConnectionID != "" {
		t.Errorf("connectionId: got %q, want empty", got.ConnectionID)
	}
	if strings.Contains(rec.Body.String(), "sovereign.local") {
		t.Errorf("response leaks the unresolvable `sovereign.local` placeholder host: %s",
			rec.Body.String())
	}
	if got.SessionID == "" {
		t.Error("sessionId: must still be issued")
	}
	if got.FallbackWebSocketURL == "" {
		t.Error("fallbackWebSocketUrl: must be populated — it is the working shell path")
	}
}

// TestExecSession_GuacamoleWired_StillCarriesRealConnection is the VACUITY
// CONTROL for the two tests above. It asserts the SAME two fields are
// NON-empty when a GuacamoleClient IS wired, so `want empty` above can only
// be satisfied by the nil-client branch specifically — not by a handler that
// stopped emitting the fields at all.
func TestExecSession_GuacamoleWired_StillCarriesRealConnection(t *testing.T) {
	rig := newExecRig(t, true)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/sovereigns/alpha/k8s/exec/default/wp-1/web/session", nil)
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
	if got.EmbedURL == "" {
		t.Error("embedURL: must be non-empty when a real GuacamoleClient issued the session")
	}
	if got.ConnectionID == "" {
		t.Error("connectionId: must be non-empty when a real GuacamoleClient issued the session")
	}
	if !got.Recording {
		t.Error("recording: must be true when Guacamole is wired")
	}
}
