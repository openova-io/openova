package handlers

import "net/http"

// Routes returns an http.Handler with all auth endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", h.Login)
	mux.HandleFunc("POST /auth/magic-link", h.SendMagicLink)
	// /auth/send-pin is the canonical UX-facing alias for the same flow:
	// the marketplace UI surfaces a 6-digit PIN ("Send PIN" button), so the
	// route name "send-pin" is more honest than "magic-link". Both paths
	// share the SendMagicLink handler — see issue #868. Likewise the
	// verifier is exposed under both /auth/verify (legacy) and
	// /auth/verify-pin (alias) so the UI can converge on the PIN naming
	// without forcing a backend rename.
	mux.HandleFunc("POST /auth/send-pin", h.SendMagicLink)
	mux.HandleFunc("POST /auth/verify", h.VerifyMagicLink)
	mux.HandleFunc("POST /auth/verify-pin", h.VerifyMagicLink)
	mux.HandleFunc("POST /auth/refresh", h.RefreshToken)
	mux.HandleFunc("GET /auth/google", h.GoogleLogin)
	mux.HandleFunc("POST /auth/google/callback", h.GoogleCallback)
	mux.HandleFunc("GET /auth/me", h.GetMe)
	mux.HandleFunc("POST /auth/logout", h.Logout)
	mux.HandleFunc("POST /auth/logout-all", h.LogoutAll)
	// Personal Access Token issuance (Wave 1b — Sandbox prerequisite).
	// Authenticated path: JWTAuth middleware required (see main.go's
	// needsAuth predicate). Mints a longer-lived JWT with `typ=pat` so
	// downstream consumers (sandbox-controller, newapi, MCP server)
	// can distinguish PATs from interactive sessions.
	//
	// Contract: pat.go. Claim shape: core/services/shared/auth/claims.go.
	mux.HandleFunc("POST /auth/pat", h.IssuePAT)
	// Admin / service endpoints. Superadmin role required; used by
	// notification to enrich event payloads with the owner's email.
	mux.HandleFunc("GET /auth/admin/users/{id}", h.AdminGetUser)
	return mux
}
