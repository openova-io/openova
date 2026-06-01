package main

// oidc_session_validator.go — closure used by the OIDC provider
// (internal/oidcprovider/provider.go) to validate the `catalyst_session`
// cookie minted by HandlePinVerify.
//
// G113 #2725 Option B Step 2 (2026-06-02): when Keycloak's
// identity-broker redirects the operator's browser to /oidc/auth on
// catalyst-api, the provider reads `catalyst_session` to verify the
// operator is PIN-authenticated. The session cookie carries a
// self-signed JWT (handoverSigner RS256); this helper verifies the
// signature against the same signer's public key, then extracts the
// subject (operator email) and any group claims.

import (
	"crypto/rsa"
	"errors"
	"log/slog"
	"net/http"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
)

// makeSessionValidator returns a closure satisfying
// oidcprovider.Provider.SessionSubjectFromCookie.
//
// The closure reads the `catalyst_session` cookie, verifies its
// signature against the handoverSigner's public RSA key, and extracts
// `email` + `realm_access.roles` (interpreted as groups for the
// downstream OIDC id_token).
//
// Errors are logged at Debug level; the broker flow handles the
// no-session case by 302-redirecting back to the catalyst-ui /login
// page with `next=<original-oidc-auth-url>` so PIN verify resumes.
func makeSessionValidator(signer *handoverjwt.Signer, log *slog.Logger) func(*http.Request) (string, []string, error) {
	return func(r *http.Request) (string, []string, error) {
		cookie, err := r.Cookie(auth.SessionCookieName)
		if err != nil {
			return "", nil, errors.New("no catalyst_session cookie")
		}
		if cookie.Value == "" {
			return "", nil, errors.New("empty catalyst_session cookie")
		}

		pub, err := signer.PublicRSAKey()
		if err != nil {
			log.Debug("oidc-provider: signer public key unavailable", "err", err)
			return "", nil, err
		}

		parsed, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return pub, nil
		})
		if err != nil || !parsed.Valid {
			log.Debug("oidc-provider: catalyst_session JWT invalid", "err", err)
			return "", nil, errors.New("invalid catalyst_session JWT")
		}

		claims, ok := parsed.Claims.(jwt.MapClaims)
		if !ok {
			return "", nil, errors.New("unexpected claims shape")
		}

		email, _ := claims["email"].(string)
		if email == "" {
			if sub, ok := claims["sub"].(string); ok {
				email = sub
			}
		}
		if email == "" {
			return "", nil, errors.New("session has no email/sub")
		}

		// Pull realm_access.roles as a flat string slice → we project them
		// onto the OIDC id_token's `groups` claim so KC's broker mapper
		// can mirror them into the realm user record on first sign-in.
		var groups []string
		if ra, ok := claims["realm_access"].(map[string]any); ok {
			if rolesAny, ok := ra["roles"].([]any); ok {
				for _, r := range rolesAny {
					if s, ok := r.(string); ok {
						groups = append(groups, s)
					}
				}
			}
		}
		// If the PIN-issued session also stamped `groups` directly,
		// merge those in too (forward-compat with G113 Step 6+).
		if gAny, ok := claims["groups"].([]any); ok {
			for _, g := range gAny {
				if s, ok := g.(string); ok {
					groups = append(groups, s)
				}
			}
		}

		return email, groups, nil
	}
}

// Compile-time guarantee that the helper's signature matches the
// Provider's injection point. (Avoids accidental drift if either
// side's type changes.)
var _ = func() func(*rsa.PublicKey) {
	return func(*rsa.PublicKey) {}
}
