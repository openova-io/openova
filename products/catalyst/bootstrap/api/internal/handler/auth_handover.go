// auth_handover.go — GET /auth/handover?token=<jwt>
//
// This endpoint is the Sovereign-side landing page for the seamless
// single-identity handover flow (issue #606). Catalyst-Zero finalises a
// handover by redirecting the operator's browser to
//
//	https://console.<sov>/auth/handover?token=<jwt>
//
// This handler:
//  1. Parses and RS256-verifies the one-time JWT using the public key
//     written by cloud-init at CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH.
//  2. Validates all claims: iss, aud, exp, role=sovereign-admin,
//     email_verified=true.
//  3. Calls jtistore.Mark — if the jti was already consumed, returns 401
//     to prevent replay.
//  4. Calls keycloak.EnsureUser to create-or-get the operator in the
//     `sovereign` realm and ensure `sovereign-admins` group membership.
//  5. Calls keycloak.ImpersonateToken to exchange for a user access +
//     refresh token pair (audience: catalyst-ui OIDC client).
//  6. Sets HttpOnly Secure SameSite=Lax cookies (catalyst_session +
//     catalyst_refresh).
//  7. 302 redirects to /console/dashboard.
//
// All errors return 401 with terse JSON {"error": "..."}.
// No secrets are emitted in error responses (per INVIOLABLE-PRINCIPLES #10).
package handler

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jtistore"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
)

// keycloakClient is the interface implemented by *keycloak.Client.
// Declared here so tests can inject a stub.
type keycloakClient interface {
	EnsureUser(ctx context.Context, email, group string) (string, error)
	ImpersonateToken(ctx context.Context, userID, audience string) (
		accessToken, refreshToken string, expiresIn int, _ error,
	)
}

// authHandoverClaims extends jwt.RegisteredClaims with the handover-specific
// fields Catalyst-Zero stamps when it mints the one-time JWT.
type authHandoverClaims struct {
	jwt.RegisteredClaims

	// Email is the operator's email address in Catalyst-Zero's Keycloak.
	Email string `json:"email"`

	// EmailVerified must be true — Catalyst-Zero only mints handover JWTs
	// for users whose email address is confirmed.
	EmailVerified bool `json:"email_verified"`

	// Role must equal "sovereign-admin".
	Role string `json:"role"`
}

// AuthHandover handles GET /auth/handover?token=<jwt>.
func (h *Handler) AuthHandover(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("token")
	if raw == "" {
		writeAuthError(w, "missing token parameter")
		return
	}

	// ── 1. Load public key ──────────────────────────────────────────────
	keyPath := h.handoverJWTPublicKeyPath
	if keyPath == "" {
		keyPath = DefaultHandoverJWTPublicKeyPath
	}
	pubKey, err := loadRSAPublicKey(keyPath)
	if err != nil {
		h.log.Error("auth_handover: load public key failed", "err", err, "path", keyPath)
		writeAuthError(w, "server misconfiguration: public key unavailable")
		return
	}

	// ── 2. Parse + verify JWT ───────────────────────────────────────────
	var claims authHandoverClaims
	tok, err := jwt.ParseWithClaims(raw, &claims,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return pubKey, nil
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !tok.Valid {
		h.log.Warn("auth_handover: JWT parse failed", "err", err)
		writeAuthError(w, "invalid token")
		return
	}

	// ── 3. Validate claims ──────────────────────────────────────────────
	const expectedIss = "https://console.openova.io"
	iss, err := claims.GetIssuer()
	if err != nil || iss != expectedIss {
		writeAuthError(w, "invalid issuer")
		return
	}

	// aud must contain the Sovereign's console URL.
	soverFQDN := h.sovereignFQDN()
	expectedAud := "https://console." + soverFQDN
	auds, _ := claims.GetAudience()
	found := false
	for _, a := range auds {
		if a == expectedAud {
			found = true
			break
		}
	}
	if !found {
		writeAuthError(w, "invalid audience")
		return
	}

	if claims.Role != "sovereign-admin" {
		writeAuthError(w, "insufficient role")
		return
	}
	if !claims.EmailVerified {
		writeAuthError(w, "email not verified")
		return
	}
	if claims.Email == "" {
		writeAuthError(w, "missing email claim")
		return
	}

	jti := claims.ID
	if jti == "" {
		writeAuthError(w, "missing jti")
		return
	}

	// ── 4. Replay check via jtistore ───────────────────────────────────
	jtiSt := h.jtiStore
	if jtiSt == nil {
		jtiSt = jtistore.New(jtistore.DefaultPath)
	}
	firstUse, err := jtiSt.Mark(jti)
	if err != nil {
		h.log.Error("auth_handover: jtistore.Mark failed", "err", err)
		writeAuthError(w, "internal error")
		return
	}
	if !firstUse {
		writeAuthError(w, "token already used")
		return
	}

	// ── 5. EnsureUser in Keycloak ───────────────────────────────────────
	kc := h.keycloakClientFor()
	if kc == nil {
		writeAuthError(w, "server misconfiguration: keycloak not configured")
		return
	}
	userID, err := kc.EnsureUser(r.Context(), claims.Email, "sovereign-admins")
	if err != nil {
		h.log.Error("auth_handover: EnsureUser failed", "email", claims.Email, "err", err)
		writeAuthError(w, "keycloak error: ensure user")
		return
	}

	// ── 6. Token-exchange ───────────────────────────────────────────────
	audience := h.kcAudience
	if audience == "" {
		audience = "catalyst-ui"
	}
	accessToken, refreshToken, expiresIn, err := kc.ImpersonateToken(r.Context(), userID, audience)
	if err != nil {
		h.log.Error("auth_handover: ImpersonateToken failed", "userID", userID, "err", err)
		writeAuthError(w, "keycloak error: token exchange")
		return
	}

	// ── 7. Set cookies + redirect ───────────────────────────────────────
	cookieMaxAge := expiresIn
	if cookieMaxAge <= 0 {
		cookieMaxAge = 3600
	}
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	http.SetCookie(w, &http.Cookie{
		Name:     "catalyst_session",
		Value:    accessToken,
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	if refreshToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "catalyst_refresh",
			Value:    refreshToken,
			Path:     "/",
			MaxAge:   cookieMaxAge * 8, // refresh token lives longer
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}

	h.log.Info("auth_handover: operator session established",
		"email", claims.Email,
		"userID", userID,
		"expires_in", expiresIn,
	)

	redirectTarget := h.authHandoverRedirect
	if redirectTarget == "" {
		redirectTarget = "/console/dashboard"
	}
	http.Redirect(w, r, redirectTarget, http.StatusFound)
}

// ────────────────────────────────────────────────────────────────────────────
// Handler wiring helpers
// ────────────────────────────────────────────────────────────────────────────

// DefaultHandoverJWTPublicKeyPath is the on-Sovereign path cloud-init writes
// the RS256 public key JWK to. Overridable via CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH.
// The handoverjwt.Signer.PublicJWK() writes JSON; cloud-init distributes this
// file from Catalyst-Zero's /var/lib/catalyst/handover-jwt-public.jwk.
const DefaultHandoverJWTPublicKeyPath = "/var/lib/catalyst/handover-jwt-public.jwk"

// jtiStorer is the interface implemented by *jtistore.Store.
type jtiStorer interface {
	Mark(jti string) (bool, error)
}

// sovereignFQDN returns the Sovereign's FQDN from Handler config or env.
func (h *Handler) sovereignFQDN() string {
	if h.authHandoverSovereignFQDN != "" {
		return h.authHandoverSovereignFQDN
	}
	return os.Getenv("SOVEREIGN_FQDN")
}

// keycloakClientFor returns the wired Keycloak client or builds one lazily
// from env. Returns nil when CATALYST_KC_SA_CLIENT_SECRET is unset (Sovereign
// not yet configured — handler returns 503 to the caller).
func (h *Handler) keycloakClientFor() keycloakClient {
	if h.kc != nil {
		return h.kc
	}
	addr := os.Getenv("CATALYST_KC_ADDR")
	if addr == "" {
		addr = "http://keycloak.keycloak.svc.cluster.local:8080"
	}
	realm := os.Getenv("CATALYST_KC_REALM")
	if realm == "" {
		realm = "sovereign"
	}
	clientID := os.Getenv("CATALYST_KC_SA_CLIENT_ID")
	if clientID == "" {
		clientID = "catalyst-api-server"
	}
	secret := os.Getenv("CATALYST_KC_SA_CLIENT_SECRET")
	if secret == "" {
		return nil // unconfigured
	}
	return keycloak.New(addr, realm, clientID, secret)
}

// ────────────────────────────────────────────────────────────────────────────
// Key loading — supports PEM (PKIX/PKCS1) and JWK JSON
// ────────────────────────────────────────────────────────────────────────────

// loadRSAPublicKey reads an RSA public key from path.
// Supports:
//   - PEM-encoded PKIX / PKCS1 (files ending in .pem)
//   - RFC 7517 JWK JSON produced by handoverjwt.Signer.PublicJWK()
func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Try PEM first.
	if key, err := jwt.ParseRSAPublicKeyFromPEM(raw); err == nil {
		return key, nil
	}

	// Try JWK JSON (produced by handoverjwt.Signer.PublicJWK).
	key, err := rsaPublicKeyFromJWK(raw)
	if err != nil {
		return nil, fmt.Errorf("load RSA public key from %s: neither PEM nor JWK: %w", path, err)
	}
	return key, nil
}

// rsaPublicKeyFromJWK parses a minimal RSA JWK as emitted by
// handoverjwt.Signer.PublicJWK(): {"kty":"RSA","use":"sig","alg":"RS256","n":"...","e":"..."}.
// n and e are base64url-encoded big-endian byte sequences per RFC 7518 §6.3.
func rsaPublicKeyFromJWK(raw []byte) (*rsa.PublicKey, error) {
	var jwk struct {
		Kty string `json:"kty"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return nil, fmt.Errorf("decode JWK: %w", err)
	}
	if jwk.Kty != "RSA" {
		return nil, fmt.Errorf("JWK kty is %q, expected RSA", jwk.Kty)
	}
	if jwk.N == "" || jwk.E == "" {
		return nil, fmt.Errorf("JWK missing n or e")
	}

	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("decode JWK n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("decode JWK e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	if e == 0 {
		return nil, fmt.Errorf("JWK e is zero")
	}

	return &rsa.PublicKey{N: n, E: e}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Error helper
// ────────────────────────────────────────────────────────────────────────────

type authHandoverError struct {
	Error string `json:"error"`
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(authHandoverError{Error: msg}) //nolint:errcheck
}
