package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

const pkceVerifierCookieName = "catalyst_pkce_verifier"

// postAuthRedirect returns the URL the browser is sent to after a successful
// magic-link callback. Defaults to "/wizard"; can be overridden via
// CATALYST_POST_AUTH_REDIRECT for deployments where the UI lives under a
// path prefix (e.g. /sovereign/wizard on Catalyst-Zero / contabo-mkt).
func postAuthRedirect() string {
	if v := os.Getenv("CATALYST_POST_AUTH_REDIRECT"); v != "" {
		return v
	}
	return "/wizard"
}

// loginRedirect returns the URL the browser is sent to on auth failure.
// Derived from postAuthRedirect: replaces the last path component with "login"
// so the prefix stays consistent.
func loginRedirect(reason string) string {
	base := postAuthRedirect()
	// Strip the trailing component (e.g. /sovereign/wizard → /sovereign)
	// and append /login?error=<reason>.
	idx := strings.LastIndex(base, "/")
	if idx < 0 {
		return "/login?error=" + url.QueryEscape(reason)
	}
	prefix := base[:idx] // e.g. "" or "/sovereign"
	return prefix + "/login?error=" + url.QueryEscape(reason)
}

// HandleMagicLink handles POST /api/v1/auth/magic-link.
//
// It accepts {"email":"<addr>"}, ensures the user exists in the Keycloak
// openova realm, and triggers Keycloak's built-in VERIFY_EMAIL execute-actions
// email (which renders as a passwordless "sign in" link). No password is ever
// set or required — the link itself is the credential.
//
// On success: 200 {"ok":true}
// On bad request: 400 {"error":"<msg>"}
// On backend failure: 502 {"error":"<msg>"}
func (h *Handler) HandleMagicLink(w http.ResponseWriter, r *http.Request) {
	cfg := h.authConfig
	if cfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "auth not configured"})
		return
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email required"})
		return
	}

	// Generate PKCE verifier and store in HttpOnly cookie.
	verifier, err := auth.GeneratePKCEVerifier()
	if err != nil {
		h.log.Error("magic-link: PKCE generation failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "pkce generation failed"})
		return
	}
	challenge := auth.PKCEChallenge(verifier)

	// Store verifier in HttpOnly cookie so HandleAuthCallback can use it.
	http.SetCookie(w, &http.Cookie{
		Name:     pkceVerifierCookieName,
		Value:    verifier,
		Path:     "/",
		MaxAge:   3600,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode, // Lax so the Keycloak redirect back carries it
	})

	// Obtain an admin token against the master realm.
	adminToken, err := authAdminToken(cfg)
	if err != nil {
		h.log.Error("magic-link: admin token failed", "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "keycloak admin unavailable"})
		return
	}

	// Ensure the user exists in the realm.
	userID, err := ensureUser(cfg, adminToken, body.Email)
	if err != nil {
		h.log.Error("magic-link: ensureUser failed", "email", body.Email, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "user provisioning failed"})
		return
	}

	// Build the authorisation URL with PKCE so the email link lands on our callback.
	authURL := cfg.KeycloakAddr + "/realms/" + cfg.Realm + "/protocol/openid-connect/auth" +
		"?response_type=code" +
		"&client_id=" + url.QueryEscape(cfg.ClientID) +
		"&redirect_uri=" + url.QueryEscape(cfg.RedirectURI) +
		"&scope=openid+email+profile" +
		"&code_challenge=" + url.QueryEscape(challenge) +
		"&code_challenge_method=S256"

	// Trigger VERIFY_EMAIL execute-actions-email — Keycloak sends the user
	// a link that completes the PKCE auth flow and lands on our callback.
	if err := executeActionsEmail(cfg, adminToken, userID, authURL); err != nil {
		h.log.Error("magic-link: executeActionsEmail failed", "user_id", userID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "email dispatch failed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleAuthCallback handles GET /api/v1/auth/callback.
//
// The Keycloak magic-link redirects here with ?code=... after the user clicks
// the email link. This handler:
//  1. Reads the PKCE verifier from the catalyst_pkce_verifier cookie.
//  2. Exchanges the code for tokens via cfg.ExchangeCode.
//  3. Issues an HMAC-signed session cookie via cfg.IssueSessionCookie.
//  4. Redirects the browser to /wizard (Catalyst-Zero operator entry-point).
func (h *Handler) HandleAuthCallback(w http.ResponseWriter, r *http.Request) {
	cfg := h.authConfig
	if cfg == nil {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code parameter", http.StatusBadRequest)
		return
	}

	// Read the PKCE verifier stored in the cookie during magic-link dispatch.
	verifierCookie, err := r.Cookie(pkceVerifierCookieName)
	if err != nil || verifierCookie.Value == "" {
		http.Error(w, "missing pkce verifier cookie", http.StatusBadRequest)
		return
	}
	verifier := verifierCookie.Value

	// Clear the PKCE cookie — single use.
	http.SetCookie(w, &http.Cookie{
		Name:     pkceVerifierCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})

	accessToken, _, claims, err := cfg.ExchangeCode(r.Context(), code, verifier)
	if err != nil {
		h.log.Debug("auth callback: code exchange failed", "err", err)
		http.Redirect(w, r, loginRedirect("callback_failed"), http.StatusSeeOther)
		return
	}

	cfg.IssueSessionCookie(w, accessToken)

	_ = claims
	http.Redirect(w, r, postAuthRedirect(), http.StatusSeeOther)
}

// HandleAuthLogout handles DELETE /api/v1/auth/session.
//
// Clears the session cookie and returns 204 No Content.
// The Catalyst-Zero UI calls this when the operator clicks Sign Out.
func (h *Handler) HandleAuthLogout(w http.ResponseWriter, r *http.Request) {
	cfg := h.authConfig
	if cfg != nil {
		cfg.ClearSessionCookie(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleWhoami handles GET /api/v1/whoami.
//
// Returns the authenticated operator's claims from context. The
// RequireSession middleware has already validated the session cookie
// before this handler is reached, so a nil claims is a logic error.
// Returns {"email":"...","sub":"...","verified":true/false}.
func (h *Handler) HandleWhoami(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		// Should never happen — RequireSession gates this handler.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"email":    claims.Email,
		"sub":      claims.Sub,
		"verified": claims.EmailVerified,
	})
}

// ── Keycloak Admin REST helpers ───────────────────────────────────────────────

// authAdminToken obtains a short-lived admin access token via the
// Keycloak master realm password grant for the admin-cli client.
// Credentials are read from CATALYST_KC_ADMIN_USER / CATALYST_KC_ADMIN_PASS.
func authAdminToken(cfg *auth.Config) (string, error) {
	adminUser := os.Getenv("CATALYST_KC_ADMIN_USER")
	adminPass := os.Getenv("CATALYST_KC_ADMIN_PASS")
	if adminUser == "" || adminPass == "" {
		return "", fmt.Errorf("CATALYST_KC_ADMIN_USER or CATALYST_KC_ADMIN_PASS not set")
	}

	tokenURL := cfg.KeycloakAddr + "/realms/master/protocol/openid-connect/token"
	resp, err := http.PostForm(tokenURL, url.Values{
		"grant_type": {"password"},
		"client_id":  {"admin-cli"},
		"username":   {adminUser},
		"password":   {adminPass},
	})
	if err != nil {
		return "", fmt.Errorf("admin token request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("admin token: status %d body %s", resp.StatusCode, string(body))
	}

	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("admin token decode: %w", err)
	}
	if tr.AccessToken == "" {
		return "", fmt.Errorf("admin token: empty access_token in response")
	}
	return tr.AccessToken, nil
}

// ensureUser looks up a Keycloak user by exact email address in the realm.
// If the user doesn't exist it creates them. Returns the Keycloak user ID.
func ensureUser(cfg *auth.Config, adminToken, email string) (string, error) {
	// Check if user exists.
	lookupURL := cfg.KeycloakAddr + "/admin/realms/" + cfg.Realm + "/users?email=" + url.QueryEscape(email) + "&exact=true"
	req, _ := http.NewRequest(http.MethodGet, lookupURL, nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ensureUser lookup: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ensureUser lookup: status %d", resp.StatusCode)
	}

	var users []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &users); err != nil {
		return "", fmt.Errorf("ensureUser decode: %w", err)
	}
	if len(users) > 0 {
		return users[0].ID, nil
	}

	// Create the user. Keycloak 24.7+ requires "username" — use the email
	// as the username for email-only magic-link login UX.
	createURL := cfg.KeycloakAddr + "/admin/realms/" + cfg.Realm + "/users"
	payload := fmt.Sprintf(`{"username":%q,"email":%q,"enabled":true,"emailVerified":false}`, email, email)
	req2, _ := http.NewRequest(http.MethodPost, createURL, strings.NewReader(payload))
	req2.Header.Set("Authorization", "Bearer "+adminToken)
	req2.Header.Set("Content-Type", "application/json")

	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		return "", fmt.Errorf("ensureUser create: %w", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp2.Body)
		return "", fmt.Errorf("ensureUser create: status %d body %s", resp2.StatusCode, string(b))
	}

	// The ID is in the Location header: .../users/<uuid>
	location := resp2.Header.Get("Location")
	parts := strings.Split(location, "/")
	if len(parts) == 0 {
		return "", fmt.Errorf("ensureUser create: empty Location header")
	}
	return parts[len(parts)-1], nil
}

// executeActionsEmail triggers Keycloak's VERIFY_EMAIL execute-actions email
// for the given user. lifespan is 3600 seconds (1 hour). The redirectUri is
// the PKCE-decorated authorisation URL so Keycloak renders the magic-link
// as a one-click sign-in.
func executeActionsEmail(cfg *auth.Config, adminToken, userID, redirectURI string) error {
	u := cfg.KeycloakAddr + "/admin/realms/" + cfg.Realm + "/users/" + userID +
		"/execute-actions-email" +
		"?client_id=" + url.QueryEscape(cfg.ClientID) +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&lifespan=3600"

	req, _ := http.NewRequest(http.MethodPut, u, strings.NewReader(`["VERIFY_EMAIL"]`))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("executeActionsEmail: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("executeActionsEmail: status %d body %s", resp.StatusCode, string(b))
	}
	return nil
}
