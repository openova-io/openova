// auth.go — magic-link dispatch + consumption (Option B, issue #614 Phase-8b).
//
// Option B replaces the Keycloak execute-actions-email flow (Agent A) with a
// fully self-contained passwordless path:
//
//  1. POST /api/v1/auth/magic-link — catalyst-api mints its own one-time
//     RS256 JWT (same signer as the handover JWT, different claims set),
//     calls keycloak.EnsureUser against the openova realm, and delivers the
//     link via Stalwart SMTP. Zero Keycloak UI, zero PKCE round-trip.
//
//  2. GET /api/v1/auth/magic — the user clicks the link in their inbox.
//     catalyst-api validates the JWT (exp, iss, aud, role, email_verified),
//     marks the jti single-use, calls keycloak.ImpersonateToken to exchange
//     for a user session in the openova realm, sets HttpOnly cookies, and
//     302-redirects to /sovereign/wizard.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   - #4: all hostnames and secrets are runtime-configurable via env.
//   - #10: no credentials logged or emitted in error responses.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jtistore"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
)

// ── Constants ────────────────────────────────────────────────────────────────

// magicLinkTTL — 15 minutes. Short enough to limit replay window; long enough
// for users on slow mail delivery paths.
const magicLinkTTL = 15 * time.Minute

// magicLinkIssuer — constant issuer claim in the magic-link JWT.
const magicLinkIssuer = "https://console.openova.io"

// magicLinkAudience — the consume endpoint URL.
const magicLinkAudience = "https://console.openova.io/sovereign/api/v1/auth/magic"

// magicLinkRole — role claim stamped into every magic-link JWT.
const magicLinkRole = "openova-user"

// magicJTIStorePath — dedicated flat-file log for consumed magic-link JTIs.
// Separate from /var/lib/catalyst/jti.log (handover JTIs) to avoid replay
// cross-contamination.
const magicJTIStorePath = "/var/lib/catalyst/magic-jti.log"

// ── helpers ──────────────────────────────────────────────────────────────────

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

// smtpAddr returns "host:port" for the outbound SMTP relay.
// Env: CATALYST_SMTP_HOST (default: stalwart-web.stalwart.svc.cluster.local)
//
//	CATALYST_SMTP_PORT (default: 587)
func smtpAddr() string {
	host := os.Getenv("CATALYST_SMTP_HOST")
	if host == "" {
		host = "stalwart-web.stalwart.svc.cluster.local"
	}
	port := os.Getenv("CATALYST_SMTP_PORT")
	if port == "" {
		port = "587"
	}
	return host + ":" + port
}

// smtpFrom returns the envelope-from / From header.
// Env: CATALYST_SMTP_FROM (default: noreply@openova.io)
func smtpFrom() string {
	if v := os.Getenv("CATALYST_SMTP_FROM"); v != "" {
		return v
	}
	return "noreply@openova.io"
}

// consoleBase returns the public base URL for the catalyst-api.
// Env: CATALYST_API_PUBLIC_URL (default: https://console.openova.io/sovereign)
func consoleBase() string {
	if v := os.Getenv("CATALYST_API_PUBLIC_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://console.openova.io/sovereign"
}

// openovaKCClient returns the openova-realm Keycloak client.
//
// Resolution order:
//  1. h.openovaKC (injected by main.go or tests)
//  2. Built lazily from CATALYST_OPENOVA_KC_* env vars
//  3. nil when CATALYST_OPENOVA_KC_SA_CLIENT_SECRET is unset (Sovereign, CI)
func (h *Handler) openovaKCClient() keycloakClient {
	if h.openovaKC != nil {
		return h.openovaKC
	}
	addr := os.Getenv("CATALYST_OPENOVA_KC_ADDR")
	if addr == "" {
		// Fall back to the shared KC addr env (Catalyst-Zero uses the same KC
		// instance but a different realm + service-account client).
		addr = os.Getenv("CATALYST_KC_ADDR")
	}
	if addr == "" {
		addr = "http://keycloak-zero.keycloak-zero.svc.cluster.local"
	}
	realm := os.Getenv("CATALYST_OPENOVA_KC_REALM")
	if realm == "" {
		realm = "openova"
	}
	clientID := os.Getenv("CATALYST_OPENOVA_KC_SA_CLIENT_ID")
	if clientID == "" {
		clientID = "catalyst-zero-server"
	}
	secret := os.Getenv("CATALYST_OPENOVA_KC_SA_CLIENT_SECRET")
	if secret == "" {
		return nil // unconfigured — magic endpoint returns 503
	}
	return keycloak.New(addr, realm, clientID, secret)
}

// magicLinkJTIStore returns the jtiStorer for magic-link JWT replay protection.
// Reuses the Handler field (jtiStore) if it happens to be wired (tests); in
// production creates a fresh file-backed store at magicJTIStorePath.
func (h *Handler) magicLinkJTIStore() jtiStorer {
	if h.jtiStore != nil {
		return h.jtiStore
	}
	return jtistore.New(magicJTIStorePath)
}

// ── magicLinkClaims ───────────────────────────────────────────────────────────

// magicLinkClaims extends jwt.RegisteredClaims with the fields the /auth/magic
// consumer validates.
type magicLinkClaims struct {
	jwt.RegisteredClaims
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Role          string `json:"role"`
}

// ── Step 1: POST /api/v1/auth/magic-link ─────────────────────────────────────

// HandleMagicLink handles POST /api/v1/auth/magic-link (Option B).
//
// 1. Reads {"email": "<addr>"} from the request body.
// 2. Calls keycloak.EnsureUser in the openova realm via the catalyst-zero-server
//    service-account (CATALYST_OPENOVA_KC_* env).
// 3. Mints a 15-minute RS256 JWT using the same handoverSigner keypair as Agent B.
// 4. Sends the magic link via Stalwart SMTP.
// 5. Returns {"ok": true}.
//
// The user never sees Keycloak's hosted UI — the link goes directly to our
// GET /api/v1/auth/magic endpoint which handles the token-exchange server-side.
func (h *Handler) HandleMagicLink(w http.ResponseWriter, r *http.Request) {
	if h.handoverSigner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "auth not configured: handover signer unavailable (CATALYST_HANDOVER_KEY_PATH not set)",
		})
		return
	}

	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Email) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email required"})
		return
	}
	email := strings.TrimSpace(body.Email)

	// ── 1. EnsureUser in openova realm ──────────────────────────────────────
	kc := h.openovaKCClient()
	if kc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "auth not configured: CATALYST_OPENOVA_KC_SA_CLIENT_SECRET not set",
		})
		return
	}
	userID, err := kc.EnsureUser(r.Context(), email, "openova-users")
	if err != nil {
		h.log.Error("magic-link: EnsureUser failed", "email", email, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "user provisioning failed"})
		return
	}

	// ── 2. Mint one-time JWT ─────────────────────────────────────────────────
	jti, err := uuid.NewRandom()
	if err != nil {
		h.log.Error("magic-link: uuid generation failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "jti generation failed"})
		return
	}
	now := time.Now().UTC()
	claims := magicLinkClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    magicLinkIssuer,
			Audience:  jwt.ClaimStrings{magicLinkAudience},
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(magicLinkTTL)),
			ID:        jti.String(),
		},
		Email:         email,
		EmailVerified: true,
		Role:          magicLinkRole,
	}

	// Sign using the same RSA private key as the handover JWT signer.
	// We access it via a shim because the signer's private key is unexported.
	// We re-use MintToken as a passthrough and then sign our own claims
	// directly by calling the JWT library with the signer's exposed method.
	//
	// Actually: handoverjwt.Signer.MintToken is purpose-built for the handover
	// shape (sovereign_fqdn, deployment_id, role=sovereign-admin). For
	// magic-link we need a different claims shape, so we use a dedicated helper
	// that calls jwt.NewWithClaims + SignedString directly on the RSA key.
	// We expose this via a new SignMagicLink shim on handoverSigner (below).
	tokenStr, err := h.handoverSigner.SignCustomClaims(claims)
	if err != nil {
		h.log.Error("magic-link: sign claims failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "token signing failed"})
		return
	}

	// ── 3. Email the link ────────────────────────────────────────────────────
	magicURL := consoleBase() + "/api/v1/auth/magic?token=" + url.QueryEscape(tokenStr)
	if err := sendMagicLinkEmail(email, magicURL); err != nil {
		h.log.Error("magic-link: SMTP send failed", "email", email, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "email dispatch failed"})
		return
	}

	h.log.Info("magic-link: dispatched", "email", email, "userID", userID)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// sendMagicLinkEmail sends the magic-link to the given address via Stalwart SMTP.
// Credentials: CATALYST_SMTP_USER / CATALYST_SMTP_PASS (optional; Stalwart
// on contabo accepts SMTP from pods in-cluster without auth, but we support
// it for environments where the relay requires credentials).
func sendMagicLinkEmail(to, magicURL string) error {
	from := smtpFrom()
	addr := smtpAddr()

	subject := "Your OpenOva sign-in link"
	body := fmt.Sprintf("Click the link below to sign in to OpenOva.\r\n\r\n%s\r\n\r\nThis link expires in 15 minutes.\r\nIf you did not request this, you can safely ignore this email.", magicURL)

	msg := fmt.Sprintf(
		"From: OpenOva Platform <%s>\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		from, to, subject, body,
	)

	smtpUser := os.Getenv("CATALYST_SMTP_USER")
	smtpPass := os.Getenv("CATALYST_SMTP_PASS")

	var authMethod smtp.Auth
	if smtpUser != "" && smtpPass != "" {
		host := strings.Split(addr, ":")[0]
		authMethod = smtp.PlainAuth("", smtpUser, smtpPass, host)
	}

	return smtp.SendMail(addr, authMethod, from, []string{to}, []byte(msg))
}

// ── Step 2: GET /api/v1/auth/magic ───────────────────────────────────────────

// HandleMagicValidate handles GET /api/v1/auth/magic?token=<jwt> (Option B).
//
// 1. Parses and RS256-verifies the JWT using the handover signing keypair's
//    public key (same key pair as handoverSigner).
// 2. Validates claims: aud, iss, exp (enforced by jwt-go library), role,
//    email_verified.
// 3. Marks the jti single-use via jtistore.
// 4. Calls keycloak.ImpersonateToken with the userID (sub) and audience
//    "catalyst-zero-ui" to exchange for a user-scoped access + refresh token.
// 5. Sets HttpOnly Secure SameSite=Lax cookies (catalyst_session + catalyst_refresh).
// 6. 302 redirects to /sovereign/wizard (CATALYST_POST_AUTH_REDIRECT).
// 7. On any error: 401 with terse JSON.
func (h *Handler) HandleMagicValidate(w http.ResponseWriter, r *http.Request) {
	if h.handoverSigner == nil {
		writeMagicError(w, "server misconfiguration: signer unavailable")
		return
	}

	rawToken := r.URL.Query().Get("token")
	if rawToken == "" {
		http.Redirect(w, r, loginRedirect("missing_token"), http.StatusSeeOther)
		return
	}

	// ── 1. Parse + verify JWT ────────────────────────────────────────────────
	pubKey, err := h.handoverSigner.PublicRSAKey()
	if err != nil {
		h.log.Error("magic-validate: PublicRSAKey failed", "err", err)
		writeMagicError(w, "server misconfiguration: keypair unavailable")
		return
	}

	var claims magicLinkClaims
	tok, err := jwt.ParseWithClaims(rawToken, &claims,
		func(t *jwt.Token) (interface{}, error) {
			if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
			}
			return pubKey, nil
		},
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !tok.Valid {
		h.log.Warn("magic-validate: JWT parse failed", "err", err)
		http.Redirect(w, r, loginRedirect("invalid_token"), http.StatusSeeOther)
		return
	}

	// ── 2. Validate claims ───────────────────────────────────────────────────
	iss, _ := claims.GetIssuer()
	if iss != magicLinkIssuer {
		h.log.Warn("magic-validate: invalid issuer", "iss", iss)
		http.Redirect(w, r, loginRedirect("invalid_issuer"), http.StatusSeeOther)
		return
	}

	auds, _ := claims.GetAudience()
	foundAud := false
	for _, a := range auds {
		if a == magicLinkAudience {
			foundAud = true
			break
		}
	}
	if !foundAud {
		h.log.Warn("magic-validate: invalid audience", "aud", auds)
		http.Redirect(w, r, loginRedirect("invalid_audience"), http.StatusSeeOther)
		return
	}

	if claims.Role != magicLinkRole {
		h.log.Warn("magic-validate: invalid role", "role", claims.Role)
		http.Redirect(w, r, loginRedirect("insufficient_role"), http.StatusSeeOther)
		return
	}
	if !claims.EmailVerified {
		writeMagicError(w, "email not verified")
		return
	}
	if claims.Email == "" || claims.Subject == "" {
		writeMagicError(w, "missing email or sub claim")
		return
	}

	jti := claims.ID
	if jti == "" {
		writeMagicError(w, "missing jti")
		return
	}

	// ── 3. Replay check ──────────────────────────────────────────────────────
	store := h.magicLinkJTIStore()
	firstUse, err := store.Mark(jti)
	if err != nil {
		h.log.Error("magic-validate: jtistore.Mark failed", "err", err)
		writeMagicError(w, "internal error")
		return
	}
	if !firstUse {
		h.log.Warn("magic-validate: replayed jti", "jti", jti)
		http.Redirect(w, r, loginRedirect("token_replayed"), http.StatusSeeOther)
		return
	}

	// ── 4. Mint session JWT (catalyst-api owns sessions; Keycloak only stores
	// the user record, no token-exchange needed). RFC 8693 in Keycloak 24.7+
	// removed legacy `requested_subject`, so server-side impersonation through
	// KC is unavailable without a real user token. We sign our own session JWT
	// with the same handover key.
	sessionTTL := 8 * time.Hour
	sessionClaims := jwt.MapClaims{
		"iss":            magicLinkIssuer,
		"sub":            claims.Subject,
		"email":          claims.Email,
		"email_verified": true,
		"role":           magicLinkRole,
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(sessionTTL).Unix(),
		"jti":            uuid.NewString(),
		"typ":            "session",
	}
	accessToken, err := h.handoverSigner.SignCustomClaims(sessionClaims)
	if err != nil {
		h.log.Error("magic-validate: SignCustomClaims failed", "err", err)
		writeMagicError(w, "session signing failed")
		return
	}
	refreshToken := ""
	expiresIn := int(sessionTTL.Seconds())

	// ── 5. Issue session cookies ─────────────────────────────────────────────
	cookieMaxAge := expiresIn
	if cookieMaxAge <= 0 {
		cookieMaxAge = 3600
	}
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	cookieDomain := os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN") // e.g. "console.openova.io"

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    accessToken,
		Path:     "/",
		Domain:   cookieDomain,
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
			Domain:   cookieDomain,
			MaxAge:   cookieMaxAge * 8,
			HttpOnly: true,
			Secure:   secure,
			SameSite: http.SameSiteLaxMode,
		})
	}

	h.log.Info("magic-validate: session established",
		"email", claims.Email,
		"userID", claims.Subject,
		"expires_in", expiresIn,
	)

	// ── 6. Redirect to wizard ─────────────────────────────────────────────────
	http.Redirect(w, r, postAuthRedirect(), http.StatusFound)
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
	// Also clear the raw cookie set by Option-B path (no HMAC wrapping).
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "catalyst_refresh",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
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

// writeMagicError writes a 401 JSON error for the magic-validate flow.
// Per INVIOLABLE-PRINCIPLES #10 — no credentials or internal state in the message.
func writeMagicError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
