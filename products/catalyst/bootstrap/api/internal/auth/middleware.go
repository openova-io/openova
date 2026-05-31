package auth

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

type contextKey string

const (
	// ClaimsKey is the context key for the authenticated Claims.
	ClaimsKey contextKey = "auth.claims"
)

// RequireSession returns a chi-compatible middleware that validates the
// session cookie. If the cookie is missing or invalid:
//   - returns HTTP 401 with JSON {"error":"unauthenticated"}
//
// If the cookie is valid, it injects into the request context:
//   - auth.ClaimsKey → *Claims
//
// And sets request headers for downstream handlers:
//   - X-User-Email → claims.Email
//   - X-User-Sub   → claims.Sub
//
// Nil-safe: when cfg is nil (Sovereign clusters, CI without Keycloak),
// the middleware is a transparent passthrough — all requests proceed
// without any auth check. Production Catalyst-Zero always sets
// CATALYST_KC_ADDR which causes New() to populate cfg.
//
// The CORS AllowedHeaders must include "Cookie" for SameSite=Strict to
// work across the Traefik ingress path. The middleware does not interfere
// with CORS pre-flight (OPTIONS) requests.
func RequireSession(cfg *Config, log *slog.Logger) func(http.Handler) http.Handler {
	// When cfg is nil the entire middleware is a transparent passthrough.
	// This preserves existing behaviour for Sovereign catalyst-api instances
	// and for CI runs that don't set CATALYST_KC_ADDR.
	if cfg == nil {
		return func(next http.Handler) http.Handler { return next }
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// OPTIONS pre-flights must pass through (CORS middleware handles them).
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			rawToken := cfg.ReadSessionToken(r)
			if rawToken == "" {
				writeAuthError(w, "unauthenticated", http.StatusUnauthorized)
				return
			}

			claims, err := cfg.ValidateToken(r.Context(), rawToken)
			if err != nil {
				log.Debug("session validation failed", "err", err)
				// Clear the invalid cookie so the UI doesn't loop.
				cfg.ClearSessionCookie(w)
				writeAuthError(w, "unauthenticated", http.StatusUnauthorized)
				return
			}

			// G97 (Refs #2647, 2026-06-01) — Sovereign operator role
			// stamping. The deployment's request.OrgEmail (stamped by the
			// orchestrator into OPERATOR_EMAIL env at chroot install time)
			// is the canonical Sovereign operator. Their JWT email
			// matches; upstream Keycloak may or may not have stamped
			// catalyst-owner role + tier=owner. This enrichment guarantees
			// the operator's downstream session always carries owner-tier
			// authority, independent of Keycloak realm configuration drift
			// or PIN-derived session shape. Idempotent: if Keycloak
			// already stamped the role + tier, the operations are no-ops.
			//
			// Replaces the per-handler isSovereignOperatorClaim email-
			// override (G78b) as the canonical authz seam. Email-override
			// remains in place as defense-in-depth, but tier+role-based
			// gates (the canonical pattern) now also accept the operator
			// without a separate email check.
			stampSovereignOperatorClaim(claims)

			// Inject identity headers for downstream handlers (e.g. handover
			// JWT signing in handler/handover_jwt.go reads X-User-Email).
			r.Header.Set("X-User-Email", claims.Email)
			r.Header.Set("X-User-Sub", claims.Sub)

			// Inject into context for handlers that prefer context lookup.
			ctx := context.WithValue(r.Context(), ClaimsKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext extracts the Claims from a request context injected by
// RequireSession. Returns nil if no claims are present (unauthenticated request
// that bypassed the middleware — should not happen in production).
func ClaimsFromContext(ctx context.Context) *Claims {
	c, _ := ctx.Value(ClaimsKey).(*Claims)
	return c
}

// sovereignOperatorRoleName is the canonical realm role name for the
// Sovereign operator. Matches the EPIC-3 §6.2 catalyst-<tier> projection
// (whoamiInjectTierRoles in handler/auth.go) so downstream UI checks that
// look for catalyst-owner pass.
const sovereignOperatorRoleName = "catalyst-owner"

// sovereignOperatorTier is the canonical tier value for the Sovereign
// operator. Top of the inheritance chain in handler/auth.go
// whoamiTierInheritance — owner inherits admin/operator/developer/viewer.
const sovereignOperatorTier = "owner"

// stampSovereignOperatorClaim enriches the JWT-derived Claims when the
// bearer matches the Sovereign operator's email. The operator email comes
// from OPERATOR_EMAIL env (orchestrator-stamped at chroot install time
// from deployment.request.OrgEmail). Idempotent: when Keycloak already
// stamped tier=owner + catalyst-owner role, this is a no-op.
//
// G97 (Refs #2647, 2026-06-01) — Closes the gap where Keycloak realm
// configuration drift, PIN-derived sessions, or chroot-internal JWT mint
// paths leave the operator's session with empty tier + empty roles. The
// per-handler isSovereignOperatorClaim email-override (G78b) is the
// belt; this is the suspenders — tier+role gates (the canonical pattern)
// now accept the operator without a per-handler email check.
func stampSovereignOperatorClaim(claims *Claims) {
	if claims == nil {
		return
	}
	opEmail := strings.ToLower(strings.TrimSpace(os.Getenv("OPERATOR_EMAIL")))
	if opEmail == "" {
		return
	}
	if strings.ToLower(strings.TrimSpace(claims.Email)) != opEmail {
		return
	}
	if claims.Tier == "" || strings.ToLower(claims.Tier) != sovereignOperatorTier {
		claims.Tier = sovereignOperatorTier
	}
	for _, r := range claims.RealmAccess.Roles {
		if r == sovereignOperatorRoleName {
			return
		}
	}
	claims.RealmAccess.Roles = append(claims.RealmAccess.Roles, sovereignOperatorRoleName)
}

func writeAuthError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(`{"error":"` + msg + `"}`)) //nolint:errcheck
}
