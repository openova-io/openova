package auth

import (
	"context"
	"log/slog"
	"net/http"
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

func writeAuthError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write([]byte(`{"error":"` + msg + `"}`)) //nolint:errcheck
}
