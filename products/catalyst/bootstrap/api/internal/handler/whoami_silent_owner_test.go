package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// TestWhoami_SilentOwner_EndToEnd is the North-Star #3 acceptance at the
// Go layer: a request with NO session cookie, routed through the real
// auth.RequireSession middleware wired in front of the real
// HandleWhoami, returns 200 with email=OPERATOR_EMAIL and tier=owner —
// the exact body the live `curl -sk .../api/v1/whoami` (no cookie) must
// produce on hw165 after the fix.
//
// This is the integration counterpart to the auth-package middleware
// unit tests: it proves the owner Claims the middleware injects flow all
// the way to the whoami wire shape (tier=owner + the catalyst-* role
// projection) rather than only proving the context value is set.
func TestWhoami_SilentOwner_EndToEnd(t *testing.T) {
	const owner = "emrah.baysal@openova.io"
	const fqdn = "hw165.omani.works"

	t.Setenv("CATALYST_SOVEREIGN_SILENT_OWNER", "true")
	t.Setenv("OPERATOR_EMAIL", owner)
	t.Setenv("SOVEREIGN_FQDN", fqdn)
	// Keep the mother-only paths out: only the silent-owner front door
	// should be exercised here.
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// A non-nil auth.Config arms RequireSession (a nil cfg is a
	// transparent passthrough that would never reach the silent-owner
	// branch). Realm-only is enough: the no-token path never calls
	// ValidateToken.
	gate := auth.RequireSession(&auth.Config{Realm: "sovereign"},
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
	srv := gate(http.HandlerFunc(h.HandleWhoami))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil) // no cookie
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	if got["email"] != owner {
		t.Errorf("email: want %s, got %v", owner, got["email"])
	}
	if got["verified"] != true {
		t.Errorf("verified: want true, got %v", got["verified"])
	}
	if got["tier"] != "owner" {
		t.Errorf("tier: want owner, got %v (body=%s)", got["tier"], rr.Body.String())
	}
	// Sovereign-context enrichment must populate from SOVEREIGN_FQDN.
	if got["mode"] != "sovereign" {
		t.Errorf("mode: want sovereign, got %v", got["mode"])
	}
	if got["sovereignFQDN"] != fqdn {
		t.Errorf("sovereignFQDN: want %s, got %v", fqdn, got["sovereignFQDN"])
	}
	// The whoami tier→role projection must light up the owner role chain
	// so the SPA's admin-nav route guards pass.
	ra, ok := got["realm_access"].(map[string]any)
	if !ok {
		t.Fatalf("realm_access missing/wrong type: %v", got["realm_access"])
	}
	roles, ok := ra["roles"].([]any)
	if !ok {
		t.Fatalf("realm_access.roles missing: %v", ra)
	}
	hasOwner := false
	for _, r := range roles {
		if r == "catalyst-owner" {
			hasOwner = true
		}
	}
	if !hasOwner {
		t.Errorf("realm_access.roles missing catalyst-owner: %v", roles)
	}
}

// TestWhoami_SilentOwner_FlagOff_401_EndToEnd — with the flag off, the
// same no-cookie request 401s through the real handler stack. Guards the
// default-off contract end-to-end.
func TestWhoami_SilentOwner_FlagOff_401_EndToEnd(t *testing.T) {
	t.Setenv("CATALYST_SOVEREIGN_SILENT_OWNER", "")
	t.Setenv("OPERATOR_EMAIL", "emrah.baysal@openova.io")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	gate := auth.RequireSession(&auth.Config{Realm: "sovereign"},
		slog.New(slog.NewTextHandler(os.Stderr, nil)))
	srv := gate(http.HandlerFunc(h.HandleWhoami))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401 with flag off, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}
