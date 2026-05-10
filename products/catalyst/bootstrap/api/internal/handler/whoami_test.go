package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// TestHandleWhoami_Mothership pins the original wire shape on
// Catalyst-Zero (mother): when neither the session JWT carries
// sovereign claims nor any sovereign-fqdn env is set, the response is
// the pre-#608 {email, sub, verified} shape with no sovereign fields.
//
// `omitempty` on DeploymentID/SovereignFQDN/Mode is what guarantees
// existing mother callers never see new keys.
func TestHandleWhoami_Mothership(t *testing.T) {
	// Make sure no leaked env from another test contaminates the result.
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Sub:           "kc-sub-mother",
		Email:         "ops@openova.io",
		EmailVerified: true,
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.HandleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	// Decode into a generic map so we can assert *absence* of the
	// sovereign keys (a typed struct with omitempty would silently zero
	// them and lose the regression signal).
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	if got["email"] != "ops@openova.io" {
		t.Errorf("email: want ops@openova.io, got %v", got["email"])
	}
	if got["sub"] != "kc-sub-mother" {
		t.Errorf("sub: want kc-sub-mother, got %v", got["sub"])
	}
	if got["verified"] != true {
		t.Errorf("verified: want true, got %v", got["verified"])
	}
	for _, k := range []string{"deploymentId", "sovereignFQDN", "mode"} {
		if _, ok := got[k]; ok {
			t.Errorf("mothership response must not include %q (got %v)", k, got[k])
		}
	}
}

// TestHandleWhoami_ChrootFromClaims — post-handover path. The
// session JWT minted by /auth/handover carries SovereignFQDN +
// DeploymentID and whoami surfaces them so SovereignConsoleLayout +
// chroot SPA features (TC-232) can discover the sovereign context in
// a single round trip.
func TestHandleWhoami_ChrootFromClaims(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Sub:           "kc-sub-sov",
		Email:         "operator@example.com",
		EmailVerified: true,
		SovereignFQDN: "omantel.biz",
		DeploymentID:  "sovereign-omantel.biz",
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.HandleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	var got whoamiResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Email != "operator@example.com" || got.Sub != "kc-sub-sov" || !got.Verified {
		t.Errorf("base claims wrong: %+v", got)
	}
	if got.SovereignFQDN != "omantel.biz" {
		t.Errorf("sovereignFQDN: want omantel.biz, got %q", got.SovereignFQDN)
	}
	if got.DeploymentID != "sovereign-omantel.biz" {
		t.Errorf("deploymentId: want sovereign-omantel.biz, got %q", got.DeploymentID)
	}
	if got.Mode != "sovereign" {
		t.Errorf("mode: want sovereign, got %q", got.Mode)
	}
}

// TestHandleWhoami_ChrootFromEnv — direct-OIDC chroot login (no
// /auth/handover detour) leaves the session JWT without sovereign
// claims, but the chroot pod's env (SOVEREIGN_FQDN) identifies it.
// Verifies env-fallback + synthesized "sovereign-<fqdn>" deployment id.
func TestHandleWhoami_ChrootFromEnv(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "omantel.biz")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Sub:           "kc-sub-direct",
		Email:         "operator@example.com",
		EmailVerified: true,
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.HandleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", rr.Code)
	}
	var got whoamiResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.SovereignFQDN != "omantel.biz" {
		t.Errorf("sovereignFQDN: want omantel.biz, got %q", got.SovereignFQDN)
	}
	if got.DeploymentID != "sovereign-omantel.biz" {
		t.Errorf("deploymentId: want synthesized sovereign-omantel.biz, got %q", got.DeploymentID)
	}
	if got.Mode != "sovereign" {
		t.Errorf("mode: want sovereign, got %q", got.Mode)
	}
}

// TestHandleWhoami_PinSessionRBACClaims — qa-loop iter-2 cluster B
// regression. Fix #2 (#1184) stamps tier=owner +
// realm_access.roles=[catalyst-owner] into the PIN session JWT so the
// chroot SPA route-guard admits the operator into the Sovereign
// Console after PIN login. Before this fix, HandleWhoami silently
// dropped both claims; the SPA bounced the operator back to /login.
//
// Pins the wire contract: when the session carries Tier + RealmAccess
// roles, /whoami MUST surface them with the JSON shape the SPA
// guard reads — `tier` (top-level string) and `realm_access.roles`
// (nested array). Drift on either side breaks the post-PIN-login
// flow that 4 failing test rows (TC-003, TC-091, TC-122, TC-196)
// depend on.
func TestHandleWhoami_PinSessionRBACClaims(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Sub:           "kc-sub-pin",
		Email:         "operator@openova.io",
		EmailVerified: true,
		Tier:          "owner",
		RealmAccess: auth.RealmAccess{
			Roles: []string{"catalyst-owner"},
		},
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.HandleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	// Decode into a generic map so the assertion exercises the actual
	// wire JSON shape the SPA route-guard reads (not the typed Go
	// struct, which would mask a bad json tag).
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}

	if got["tier"] != "owner" {
		t.Errorf("tier: want %q, got %v", "owner", got["tier"])
	}

	ra, ok := got["realm_access"].(map[string]any)
	if !ok {
		t.Fatalf("realm_access: want object, got %T (%v)", got["realm_access"], got["realm_access"])
	}
	roles, ok := ra["roles"].([]any)
	if !ok {
		t.Fatalf("realm_access.roles: want array, got %T (%v)", ra["roles"], ra["roles"])
	}
	if len(roles) != 1 || roles[0] != "catalyst-owner" {
		t.Errorf("realm_access.roles: want [catalyst-owner], got %v", roles)
	}
}

// TestHandleWhoami_NoRBACOmitsFields — pre-RBAC wire-shape regression.
// A session without Tier or RealmAccess roles must not introduce
// `tier` or `realm_access` keys (omitempty preserves the original
// shape so existing callers parse cleanly).
func TestHandleWhoami_NoRBACOmitsFields(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Sub:           "kc-sub-norbac",
		Email:         "ops@openova.io",
		EmailVerified: true,
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.HandleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"tier", "realm_access"} {
		if _, present := got[k]; present {
			t.Errorf("no-RBAC response must not include %q (got %v)", k, got[k])
		}
	}
}

// TestHandleWhoami_NilClaims — defensive: RequireSession should always
// inject claims, but a logic bug stripping the middleware must surface
// as 401, not a panic / empty 200.
func TestHandleWhoami_NilClaims(t *testing.T) {
	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	rr := httptest.NewRecorder()
	h.HandleWhoami(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("nil-claims path: want 401, got %d (body=%s)", rr.Code, rr.Body.String())
	}
}

// TestHandleWhoami_ProjectsTierToRealmRoles — TC-177 regression.
// When the operator's session carries a `tier` claim (e.g. PIN-derived
// session, chroot-internal JWT), whoami's response.realm_access.roles
// MUST include catalyst-<tier> + every inherited catalyst-<tier-below>
// role so the EPIC-3 access-matrix UI's per-user role chips render
// correctly regardless of how the session was minted.
func TestHandleWhoami_ProjectsTierToRealmRoles(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	t.Setenv("CATALYST_OTECH_FQDN", "")
	t.Setenv("CATALYST_SELF_DEPLOYMENT_ID", "")

	h := New(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/whoami", nil)
	ctx := context.WithValue(req.Context(), auth.ClaimsKey, &auth.Claims{
		Sub:           "kc-sub-dev",
		Email:         "qa-user1@openova.io",
		EmailVerified: true,
		Tier:          "developer",
	})
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	h.HandleWhoami(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	var got whoamiResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	if got.Tier != "developer" {
		t.Errorf("tier: want developer, got %q", got.Tier)
	}
	wantRoles := map[string]bool{"catalyst-viewer": false, "catalyst-developer": false}
	for _, r := range got.RealmAccess.Roles {
		if _, ok := wantRoles[r]; ok {
			wantRoles[r] = true
		}
		if r == "catalyst-owner" {
			t.Errorf("did not expect catalyst-owner for tier=developer; roles=%v", got.RealmAccess.Roles)
		}
	}
	for role, found := range wantRoles {
		if !found {
			t.Errorf("expected role %q in realm_access.roles=%v", role, got.RealmAccess.Roles)
		}
	}
}

// TestWhoamiInjectTierRoles_PreservesExistingRoles — defensive: when
// Keycloak already stamps the catalyst-* role list on the JWT, the
// projection MUST be idempotent (no duplicate appends).
func TestWhoamiInjectTierRoles_PreservesExistingRoles(t *testing.T) {
	ra := &whoamiRealmAccess{Roles: []string{"catalyst-viewer", "catalyst-developer", "extra-role"}}
	whoamiInjectTierRoles(ra, "developer")
	want := []string{"catalyst-viewer", "catalyst-developer", "extra-role"}
	if len(ra.Roles) != len(want) {
		t.Fatalf("roles: got %v want %v", ra.Roles, want)
	}
	for i := range want {
		if ra.Roles[i] != want[i] {
			t.Fatalf("[%d]: got %q want %q", i, ra.Roles[i], want[i])
		}
	}
}
