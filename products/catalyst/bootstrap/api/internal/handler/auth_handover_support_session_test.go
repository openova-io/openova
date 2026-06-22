// auth_handover_support_session_test.go — #4101 BUG 2 coverage.
//
// An "Enter org" support handover (org_enter_org.go, #3378 B2) is signed
// by the SOVEREIGN's OWN handover signer (the same catalyst-api both mints
// AND redeems it) but stamps iss=https://console.openova.io and targets a
// POOL-domain Org console (console.demo.omani.homes) that the omantel.biz
// Sovereign also serves.
//
// Pre-#4101 the /auth/handover verifier accepted ONLY the mounted MOTHERSHIP
// public key, so a token signed by the local signer failed with
// "crypto/rsa: verification error" → "invalid token" (live on omantel.biz
// 2026-06-22). And the aud=console.<own-fqdn> check could never match a
// support token (no aud claim; carries sovereign_fqdn=<org-suffix>).
//
// These tests lock both halves of the fix:
//   - the local signer key is accepted EVEN WHEN the mounted verify key is a
//     DIFFERENT (mothership) key;
//   - the support session is validated against the request host's Org suffix
//     instead of the Sovereign's own console aud;
//   - the minted session cookie is scoped to the pool-domain Org host.
package handler

import (
	"crypto/rsa"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jtistore"
)

// supportSessionSetup wires a Handler whose MOUNTED verify key (the
// "mothership" public JWK) is DIFFERENT from its local handoverSigner key
// (the "Sovereign" signer). This reproduces the live omantel.biz topology
// where the Sovereign signs Enter-org tokens with its own key but the
// /auth/handover verifier has the mothership's public key mounted.
func supportSessionSetup(t *testing.T) (h *Handler, localSigner *handoverjwt.Signer) {
	t.Helper()
	dir := t.TempDir()

	// "Mothership" key — only its PUBLIC half is mounted as the verify key.
	mothershipPriv, mothershipPubJWK, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("mothership GenerateKeypair: %v", err)
	}
	_ = mothershipPriv
	keyPath := filepath.Join(dir, "mothership-public.jwk")
	if err := os.WriteFile(keyPath, mothershipPubJWK, 0o644); err != nil {
		t.Fatalf("write mothership pubJWK: %v", err)
	}

	// "Sovereign" signer — a DIFFERENT keypair, used to sign the Enter-org
	// support token (org_enter_org.go signs with h.handoverSigner).
	sovPriv, _, err := handoverjwt.GenerateKeypair()
	if err != nil {
		t.Fatalf("sovereign GenerateKeypair: %v", err)
	}
	localSigner, err = handoverjwt.New(sovPriv, "https://console.openova.io", 8*time.Hour)
	if err != nil {
		t.Fatalf("handoverjwt.New(sovereign): %v", err)
	}

	h = &Handler{
		log:                       slog.New(slog.NewTextHandler(io.Discard, nil)),
		handoverJWTPublicKeyPath:  keyPath, // mothership key mounted
		handoverSigner:            localSigner,
		authHandoverSovereignFQDN: "omantel.biz",
		authHandoverRedirect:      "/dashboard",
		jtiStore:                  jtistore.New(filepath.Join(dir, "jti.log")),
		kc:                        &stubKeycloakClient{},
	}
	return h, localSigner
}

// mintSupportToken builds the exact claim shape org_enter_org.go mints for
// entering the demo Org on a pool domain, and signs it with the given signer.
func mintSupportToken(t *testing.T, signer *handoverjwt.Signer, orgSuffix string) string {
	t.Helper()
	now := time.Now()
	principal := "support+ops@" + orgSuffix
	claims := jwt.MapClaims{
		"iss":               "https://console.openova.io",
		"sub":               principal,
		"email":             principal,
		"email_verified":    true,
		"role":              "sovereign-admin",
		"sovereign_fqdn":    orgSuffix,
		"deployment_id":     "",
		"iat":               now.Unix(),
		"exp":               now.Add(30 * time.Minute).Unix(),
		"jti":               "support-" + orgSuffix + "-" + time.Now().Format("150405.000000000"),
		"support_session":   true,
		"support_principal": principal,
		"impersonated_org":  "demo",
		"initiated_by":      "ops@omantel.biz",
	}
	tok, err := signer.SignCustomClaims(claims)
	if err != nil {
		t.Fatalf("SignCustomClaims: %v", err)
	}
	return tok
}

// TestAuthHandover_SupportSession_LocalSignerAccepted is the BUG 2 fix:
// a support token signed by the LOCAL signer is accepted at /auth/handover
// even though the mounted verify key is the (different) mothership key, and
// the request lands on the pool-domain Org console host.
func TestAuthHandover_SupportSession_LocalSignerAccepted(t *testing.T) {
	h, signer := supportSessionSetup(t)
	tok := mintSupportToken(t, signer, "demo.omani.homes")

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	// The browser lands on the pool-domain Org console host.
	req.Header.Set("X-Forwarded-Host", "console.demo.omani.homes")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status: got %d want 302 (BUG 2 regression — support handover rejected); body=%s",
			resp.StatusCode, w.Body.String())
	}
	if loc := resp.Header.Get("Location"); loc != "/dashboard" {
		t.Errorf("Location: got %q want /dashboard", loc)
	}

	sessionCookie := findCookie(resp.Cookies(), "catalyst_session")
	if sessionCookie == nil {
		t.Fatal("catalyst_session cookie not set on support handover")
	}
	// BUG 1+2: the cookie must be scoped to the pool-domain Org host suffix,
	// NOT the Sovereign FQDN — otherwise the browser drops it and the next
	// whoami on console.demo.omani.homes is 401.
	if sessionCookie.Domain != "demo.omani.homes" {
		t.Errorf("catalyst_session Domain: got %q want %q (pool-domain Org scope)",
			sessionCookie.Domain, "demo.omani.homes")
	}

	// The minted session JWT must carry the support markers.
	parsed, err := jwt.Parse(sessionCookie.Value, func(tok *jwt.Token) (interface{}, error) {
		pk, _ := signer.PublicRSAKey()
		return pk, nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("session JWT validate: err=%v", err)
	}
	m := parsed.Claims.(jwt.MapClaims)
	if m["support_session"] != true {
		t.Errorf("session.support_session: got %v want true", m["support_session"])
	}
	if m["impersonated_org"] != "demo" {
		t.Errorf("session.impersonated_org: got %v want demo", m["impersonated_org"])
	}
}

// TestAuthHandover_SupportSession_HostMismatchRejected guards against a
// support token being redeemed on a DIFFERENT Org's host than it was minted
// for (token sovereign_fqdn=demo.omani.homes redeemed on
// console.acme.omani.homes must be rejected).
func TestAuthHandover_SupportSession_HostMismatchRejected(t *testing.T) {
	h, signer := supportSessionSetup(t)
	tok := mintSupportToken(t, signer, "demo.omani.homes")

	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	req.Header.Set("X-Forwarded-Host", "console.acme.omani.homes")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status: got %d want 401 (host mismatch must be rejected); body=%s",
			w.Code, w.Body.String())
	}
}

// TestAuthHandover_NonSupportToken_LocalSignerStillRejectedWrongKey ensures
// the local-signer fallback does NOT weaken the standard mothership handover:
// a NON-support token signed by the wrong (local) key with no aud is still
// rejected (it would fail the aud check even if the signature passed). This
// documents that the local-key fallback is gated to legitimate use.
func TestAuthHandover_NonSupportToken_AudStillEnforced(t *testing.T) {
	h, signer := supportSessionSetup(t)
	// A non-support token signed by the local signer for the wrong aud.
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":            "https://console.openova.io",
		"sub":            "admin@evil.test",
		"email":          "admin@evil.test",
		"email_verified": true,
		"role":           "sovereign-admin",
		"aud":            "https://console.evil.test", // not console.omantel.biz
		"iat":            now.Unix(),
		"exp":            now.Add(5 * time.Minute).Unix(),
		"jti":            "non-support-" + time.Now().Format("150405.000000000"),
	}
	tok, err := signer.SignCustomClaims(claims)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/handover?token="+tok, nil)
	req.Header.Set("X-Forwarded-Host", "console.omantel.biz")
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	h.AuthHandover(w, req)
	// Signature now verifies (local key), but the aud gate rejects it.
	assertAuthError(t, w, http.StatusUnauthorized, "invalid audience")
}

var _ = (*rsa.PublicKey)(nil)
