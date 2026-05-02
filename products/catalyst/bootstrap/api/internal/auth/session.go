package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

const (
	// SessionCookieName is the HttpOnly SameSite=Strict cookie that carries
	// the operator's Keycloak access token (opaque to the browser).
	SessionCookieName = "catalyst_session"

	// sessionCookieDuration is the Max-Age for the session cookie.
	// Matches the Keycloak SSO session idle timeout (4h).
	sessionCookieDuration = 4 * time.Hour
)

// Claims represents the parsed claims from a Keycloak-issued ID/access token.
type Claims struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	PreferredName string `json:"preferred_username"`
}

// Config holds the runtime configuration for the auth package.
// All fields are derived from environment variables (never hardcoded).
type Config struct {
	// KeycloakAddr is the base URL of the Keycloak server,
	// e.g. https://auth.openova.io
	KeycloakAddr string
	// Realm is the Keycloak realm name (e.g. "openova").
	Realm string
	// ClientID is the OIDC client ID (e.g. "catalyst-zero-ui").
	ClientID string
	// RedirectURI is the callback URL (e.g. https://console.openova.io/sovereign/auth/callback).
	RedirectURI string
	// CookieSecret is used to HMAC-sign the session cookie value
	// so a tampered cookie is rejected before any JWKS validation.
	CookieSecret string
	// JWKSCache caches the realm's public keys.
	JWKSCache *JWKSCache
	// LocalPublicKey is catalyst-api's own RS256 public key (handover signer).
	// When ValidateToken can't find a matching kid in JWKS, it falls back to
	// this key — this lets catalyst-api accept its own self-signed session
	// JWTs (Keycloak 24.7+ removed legacy token-exchange so we can't get
	// a Keycloak-signed user token without a real user session).
	LocalPublicKey *rsa.PublicKey
}

// tokenURL returns the Keycloak token endpoint for the configured realm.
func (c *Config) tokenURL() string {
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.KeycloakAddr, c.Realm)
}

// authURL returns the Keycloak authorization endpoint for PKCE flow.
func (c *Config) authURL() string {
	return fmt.Sprintf("%s/realms/%s/protocol/openid-connect/auth", c.KeycloakAddr, c.Realm)
}

// adminAPIBaseURL returns the Keycloak admin REST base for the realm.
func (c *Config) adminAPIBaseURL() string {
	return fmt.Sprintf("%s/admin/realms/%s", c.KeycloakAddr, c.Realm)
}

// ExchangeCode exchanges an authorization code + PKCE verifier for tokens
// and returns the access token, refresh token, and parsed Claims.
func (c *Config) ExchangeCode(ctx context.Context, code, codeVerifier string) (accessToken, refreshToken string, claims *Claims, err error) {
	body := strings.NewReader(fmt.Sprintf(
		"grant_type=authorization_code&client_id=%s&redirect_uri=%s&code=%s&code_verifier=%s",
		c.ClientID, c.RedirectURI, code, codeVerifier,
	))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), body)
	if err != nil {
		return "", "", nil, fmt.Errorf("auth: build token req: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", nil, fmt.Errorf("auth: token exchange: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", nil, fmt.Errorf("auth: token exchange status %d", resp.StatusCode)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", "", nil, fmt.Errorf("auth: decode token response: %w", err)
	}

	// Parse claims from the ID token (JWT, not verified here — validation
	// happens via JWKS in ValidateToken).
	claims, err = parseJWTClaims(tokenResp.IDToken)
	if err != nil {
		return "", "", nil, fmt.Errorf("auth: parse id_token claims: %w", err)
	}

	return tokenResp.AccessToken, tokenResp.RefreshToken, claims, nil
}

// ValidateToken validates a Keycloak-issued JWT against the JWKS and returns
// the parsed Claims. Returns an error if the token is expired, invalid, or
// the JWKS cannot be fetched.
func (c *Config) ValidateToken(ctx context.Context, rawToken string) (*Claims, error) {
	// Split header.payload.sig
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("auth: malformed JWT (expected 3 parts, got %d)", len(parts))
	}

	// Decode header to get kid + alg
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("auth: decode JWT header: %w", err)
	}
	var header struct {
		Kid string `json:"kid"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("auth: parse JWT header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("auth: unsupported alg %s (expected RS256)", header.Alg)
	}

	// First try our own self-signed session JWTs (no kid header).
	// These are minted by handler/auth.go HandleMagicValidate after a
	// successful magic-link click. Always try local first for low latency
	// and to avoid an unnecessary JWKS fetch on the hot path.
	var pubKey *rsa.PublicKey
	if c.LocalPublicKey != nil && header.Kid == "" {
		pubKey = c.LocalPublicKey
	} else {
		// Fall back to Keycloak JWKS (Sovereign-side / legacy KC tokens).
		keys, err := c.JWKSCache.Keys(ctx)
		if err != nil {
			return nil, fmt.Errorf("auth: fetch JWKS: %w", err)
		}

		var matchedKey *jwksEntry
		for i := range keys {
			if keys[i].Kid == header.Kid {
				matchedKey = &keys[i]
				break
			}
		}
		if matchedKey == nil {
			// Last-resort fallback: try LocalPublicKey even if kid is set
			// (some signers stamp a kid header).
			if c.LocalPublicKey != nil {
				pubKey = c.LocalPublicKey
			} else {
				return nil, fmt.Errorf("auth: no JWKS key for kid %q", header.Kid)
			}
		} else {
			pubKey, err = jwkToRSAPublicKey(matchedKey)
			if err != nil {
				return nil, fmt.Errorf("auth: build RSA key from JWK: %w", err)
			}
		}
	}

	// Verify signature
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("auth: decode JWT signature: %w", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pubKey, crypto_hash_SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("auth: JWT signature invalid: %w", err)
	}

	// Parse claims
	claims, err := parseJWTClaims(rawToken)
	if err != nil {
		return nil, err
	}

	// Check expiry
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("auth: decode JWT payload: %w", err)
	}
	var expClaims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &expClaims); err != nil {
		return nil, fmt.Errorf("auth: parse JWT exp: %w", err)
	}
	if expClaims.Exp > 0 && time.Unix(expClaims.Exp, 0).Before(time.Now()) {
		return nil, fmt.Errorf("auth: JWT expired at %s", time.Unix(expClaims.Exp, 0))
	}

	return claims, nil
}

// IssueSessionCookie writes a signed HttpOnly SameSite=Strict cookie
// containing the HMAC-signed access token.
func (c *Config) IssueSessionCookie(w http.ResponseWriter, accessToken string) {
	signed := c.signValue(accessToken)
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    signed,
		Path:     "/",
		MaxAge:   int(sessionCookieDuration.Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie removes the session cookie.
func (c *Config) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ReadSessionToken extracts the access token from the session cookie.
//
// Two cookie shapes are accepted:
//
//  1. HMAC-wrapped (Option A legacy): "<token>.<hmac>" — produced by
//     IssueSessionCookie. The HMAC suffix is verified and stripped.
//
//  2. Raw Keycloak JWT (Option B magic-link): the cookie value is a
//     compact JWT ("<header>.<payload>.<sig>"). Recognised by the absence
//     of an additional "." after the third segment. When CookieSecret is
//     empty, only this path is used so existing single-component deployments
//     (Sovereign clusters) continue to work unchanged.
//
// Returns "" when no valid cookie is present.
func (c *Config) ReadSessionToken(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return ""
	}

	// Option A: HMAC-wrapped. Only attempt HMAC verification when a
	// CookieSecret is configured — avoids mis-treating a raw JWT as a
	// corrupt HMAC-wrapped value.
	if c.CookieSecret != "" {
		if stripped := c.verifyAndStrip(cookie.Value); stripped != "" {
			return stripped
		}
	}

	// Option B: raw Keycloak JWT. A compact JWT has exactly 3 base64url
	// segments separated by '.'. Check that the value looks like a JWT
	// (two dots minimum) and return it directly.
	parts := strings.Split(cookie.Value, ".")
	if len(parts) == 3 {
		return cookie.Value
	}

	return ""
}

// signValue returns "<token>.<hmac>" where hmac is the HMAC-SHA256
// of the token using c.CookieSecret.
func (c *Config) signValue(value string) string {
	mac := hmac.New(sha256.New, []byte(c.CookieSecret))
	mac.Write([]byte(value))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return value + "." + sig
}

// verifyAndStrip verifies the HMAC suffix and returns the payload, or "" on failure.
func (c *Config) verifyAndStrip(signed string) string {
	// Find last '.' — the separator between token and sig.
	// Access tokens contain '.' (JWT parts) so we split from the right.
	idx := strings.LastIndex(signed, ".")
	if idx < 0 {
		return ""
	}
	payload := signed[:idx]
	givenSig := signed[idx+1:]

	mac := hmac.New(sha256.New, []byte(c.CookieSecret))
	mac.Write([]byte(payload))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(givenSig), []byte(expectedSig)) {
		return ""
	}
	return payload
}

// GeneratePKCEVerifier returns a random 43-character base64url string
// suitable for use as a PKCE code_verifier.
func GeneratePKCEVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate PKCE verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// PKCEChallenge returns the S256 challenge for a given verifier.
func PKCEChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// parseJWTClaims decodes the payload of a JWT (without verifying the signature)
// and returns the auth Claims.
func parseJWTClaims(rawToken string) (*Claims, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("auth: malformed JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("auth: decode JWT payload: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, fmt.Errorf("auth: parse JWT payload: %w", err)
	}
	return &c, nil
}

// jwkToRSAPublicKey converts a JWK (n, e in base64url) to an *rsa.PublicKey.
func jwkToRSAPublicKey(key *jwksEntry) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, fmt.Errorf("decode JWK n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, fmt.Errorf("decode JWK e: %w", err)
	}
	// Pad eBytes to 4 bytes for binary.BigEndian.Uint32
	if len(eBytes) < 4 {
		padded := make([]byte, 4)
		copy(padded[4-len(eBytes):], eBytes)
		eBytes = padded
	}
	e := int(binary.BigEndian.Uint32(eBytes))
	return &rsa.PublicKey{
		N: new(big.Int).SetBytes(nBytes),
		E: e,
	}, nil
}

// crypto_hash_SHA256 is the crypto.Hash value for SHA-256. Imported this way
// to avoid a dependency on the crypto package in the test surface.
const crypto_hash_SHA256 = 5 // crypto.SHA256

// Ensure unused imports don't cause compile issues.
var _ = (&Config{}).authURL
var _ = (&Config{}).adminAPIBaseURL
