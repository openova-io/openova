package handlers

import "net/http"

// Routes returns an http.Handler with all auth endpoints registered.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/login", h.Login)
	mux.HandleFunc("POST /auth/magic-link", h.SendMagicLink)
	mux.HandleFunc("POST /auth/verify", h.VerifyMagicLink)
	mux.HandleFunc("POST /auth/refresh", h.RefreshToken)
	mux.HandleFunc("GET /auth/google", h.GoogleLogin)
	mux.HandleFunc("POST /auth/google/callback", h.GoogleCallback)
	mux.HandleFunc("GET /auth/me", h.GetMe)
	mux.HandleFunc("POST /auth/logout", h.Logout)
	mux.HandleFunc("POST /auth/logout-all", h.LogoutAll)
	// Admin / service endpoints. Superadmin role required; used by
	// notification to enrich event payloads with the owner's email.
	mux.HandleFunc("GET /auth/admin/users/{id}", h.AdminGetUser)
	return mux
}
