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
