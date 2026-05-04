// auth.go — 6-digit PIN dispatch + verification (issue #688).
//
// Replaces the magic-link flow rejected by the founder (looks like
// phishing). The new flow is a paste-friendly 6-box numeric PIN
// modelled on bank / Google verification screens:
//
//  1. POST /api/v1/auth/pin/issue — body {email}. catalyst-api
//     EnsureUser's the operator in the openova realm via the
//     catalyst-zero-server service account, generates a fresh 6-digit
//     PIN with crypto/rand (NEVER math/rand), stores it in the
//     in-memory pinStore keyed by email with a 10-minute TTL and an
//     attempts counter, then sends a plaintext email containing the
//     code (NO link, no clickable URL — only the digits).
//
//  2. POST /api/v1/auth/pin/verify — body {email, pin, requestId}.
//     catalyst-api looks up the entry, validates the PIN, mints a
//     self-signed session JWT (same handover signer as before since
//     KC 24.7+ removed legacy token-exchange), and sets HttpOnly
//     SameSite=Lax cookies (catalyst_session, catalyst_refresh).
//     Wrong PIN: 401 with attempts remaining. Three wrong: entry
//     destroyed, 410 attempts-exceeded. Expired: 410 pin-expired.
//
// Per docs/INVIOLABLE-PRINCIPLES.md:
//   - #4: hostnames + secrets all runtime-configurable via env.
//   - #10: PINs are credentials and never appear in info-level logs
//     or error responses. Only requestId + email reach slog.
package handler

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
)

// ── Constants ────────────────────────────────────────────────────────────────

// pinIssuer — issuer claim stamped into the self-signed session JWT.
// Matches the public origin of console.openova.io so existing JWT
// validation remains stable across the magic-link → PIN cutover.
const pinIssuer = "https://console.openova.io"

// pinSessionRole — role claim stamped into PIN-derived session JWTs.
// Same value as the previous magic-link role so downstream RBAC
// (RequireSession middleware, whoami, RBAC checks) keeps working
// without any further change.
const pinSessionRole = "openova-user"

// pinSessionTTL — how long a PIN-derived session lasts. 8 hours, matching
// the magic-link session TTL so operator-facing UX is unchanged.
const pinSessionTTL = 8 * time.Hour

// ── helpers ──────────────────────────────────────────────────────────────────

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
		return nil // unconfigured — endpoint returns 503
	}
	return keycloak.New(addr, realm, clientID, secret)
}

// pinStoreFor lazily wires h.pinStore on first use. Tests inject a
// pre-built store via h.pinStore directly so this helper is a no-op.
func (h *Handler) pinStoreFor() *pinStore {
	if h.pinStore != nil {
		return h.pinStore
	}
	s, _ := newPinStore()
	h.pinStore = s
	return h.pinStore
}

// generatePin returns a fresh, uniformly-distributed 6-digit decimal
// string. Uses crypto/rand (NEVER math/rand) — the PIN is a low-entropy
// credential and predictability would let an attacker bypass the
// 3-attempt lockout via parallel guesses across many issues.
func generatePin() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// maskPin returns "***" + last 3 digits of pin. Used for debug logging
// only. Even at debug level the full PIN never lands in the journal.
func maskPin(pin string) string {
	if len(pin) < 3 {
		return "***"
	}
	return "***" + pin[len(pin)-3:]
}

// ── Step 1: POST /api/v1/auth/pin/issue ──────────────────────────────────────

type pinIssueRequest struct {
	Email string `json:"email"`
}

type pinIssueResponse struct {
	OK           bool   `json:"ok"`
	RequestID    string `json:"requestId"`
	ExpiresInSec int    `json:"expiresInSec"`
}

// HandlePinIssue handles POST /api/v1/auth/pin/issue.
//
//  1. Reads {"email"} from the body. Empty email → 400 email-required.
//  2. Calls EnsureUser in the openova realm so the user record exists
//     before they ever type the PIN.
//  3. Per-email rate-limit check (60s) → 429 pin-rate-limited.
//  4. Generates a 6-digit PIN with crypto/rand.
//  5. Persists in pinStore with TTL + new requestId.
//  6. Sends a plaintext email (no link). The PIN is the body. SMTP
//     failure → 502 email-send-failed; the entry is rolled back so the
//     operator can re-request.
//  7. Returns {ok, requestId, expiresInSec}. The UI uses requestId on
//     /pin/verify so a stale browser tab cannot replay against a
//     newer code.
func (h *Handler) HandlePinIssue(w http.ResponseWriter, r *http.Request) {
	if h.handoverSigner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "auth-unconfigured",
			"detail": "handover signer unavailable (CATALYST_HANDOVER_KEY_PATH not set)",
		})
		return
	}

	var body pinIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "email-required",
			"detail": "request body must be JSON {email}",
		})
		return
	}
	email := strings.TrimSpace(body.Email)
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "email-required",
			"detail": "email is required",
		})
		return
	}

	// ── 1. EnsureUser in openova realm ──────────────────────────────────────
	kc := h.openovaKCClient()
	if kc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "auth-unconfigured",
			"detail": "CATALYST_OPENOVA_KC_SA_CLIENT_SECRET not set",
		})
		return
	}
	if _, err := kc.EnsureUser(r.Context(), email, "openova-users"); err != nil {
		h.log.Error("pin/issue: EnsureUser failed", "email", email, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "user-provisioning-failed",
			"detail": "could not provision user record",
		})
		return
	}

	// ── 2. Rate-limit (per-email, 60s) ──────────────────────────────────────
	store := h.pinStoreFor()
	if ok, retry := store.canIssue(email); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())+1))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":         "pin-rate-limited",
			"detail":        "wait before requesting another code",
			"retryAfterSec": int(retry.Seconds()) + 1,
		})
		return
	}

	// ── 3. Generate PIN + requestId ─────────────────────────────────────────
	pin, err := generatePin()
	if err != nil {
		h.log.Error("pin/issue: generatePin failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "pin-generation-failed",
			"detail": "could not generate code",
		})
		return
	}
	requestID := uuid.NewString()

	// Persist BEFORE sending the email so a successful send always has a
	// matching store entry. If SMTP fails we delete the entry so a retry
	// isn't blocked by the cooldown.
	store.put(email, pin, requestID)

	// ── 4. Send the email ───────────────────────────────────────────────────
	if err := sendPinEmail(email, pin); err != nil {
		// Roll back so the cooldown doesn't punish the operator for an SMTP
		// transient.
		store.drop(email)
		h.log.Error("pin/issue: SMTP send failed", "email", email, "requestID", requestID, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "email-send-failed",
			"detail": "could not deliver code",
		})
		return
	}

	// Per docs/INVIOLABLE-PRINCIPLES.md #10: info logs the email +
	// requestID only. Debug-level may add the masked tail; we use
	// log.Debug so production stays at info and the PIN bytes never
	// land anywhere persistent.
	h.log.Info("pin/issue: dispatched", "email", email, "requestID", requestID)
	h.log.Debug("pin/issue: dispatched (masked)", "email", email, "requestID", requestID, "pinTail", maskPin(pin))

	writeJSON(w, http.StatusOK, pinIssueResponse{
		OK:           true,
		RequestID:    requestID,
		ExpiresInSec: int(pinTTL.Seconds()),
	})
}

// sendPinEmail is a package-level var so tests can swap in a stub
// without requiring an SMTP relay. The default implementation sends
// via Stalwart per smtpAddr().
var sendPinEmail = sendPinEmailDefault

// sendPinEmailDefault sends a plaintext email containing the 6-digit
// code and no clickable URL.
//
// Format (per founder spec on #688):
//
//	Your OpenOva sign-in code:
//
//	    3 7 2 4 5 8
//
//	Enter this code at https://console.openova.io/sovereign/login.
//	The code expires in 10 minutes.
//
//	If you didn't request this, you can ignore this email.
//
// The bare URL on line 4 is informational only — operators must type
// the code, not click anything. Per founder rule on #688 there must be
// NO magic-link URL (no token, no auto-login query string).
func sendPinEmailDefault(to, pin string) error {
	from := smtpFrom()
	addr := smtpAddr()

	if len(pin) != 6 {
		return errors.New("sendPinEmail: pin must be 6 digits")
	}
	// "3 7 2 4 5 8" — single ASCII space between digits keeps the layout
	// stable across every plaintext client.
	spaced := strings.Join(strings.Split(pin, ""), " ")
	subject := fmt.Sprintf("Your OpenOva sign-in code: %s", pin)
	body := fmt.Sprintf(
		"Your OpenOva sign-in code:\r\n\r\n    %s\r\n\r\n"+
			"Enter this code at https://console.openova.io/sovereign/login.\r\n"+
			"The code expires in 10 minutes.\r\n\r\n"+
			"If you didn't request this, you can ignore this email.",
		spaced,
	)

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

// ── Step 2: POST /api/v1/auth/pin/verify ─────────────────────────────────────

type pinVerifyRequest struct {
	Email     string `json:"email"`
	PIN       string `json:"pin"`
	RequestID string `json:"requestId"`
}

type pinVerifyResponse struct {
	OK bool `json:"ok"`
}

// HandlePinVerify handles POST /api/v1/auth/pin/verify.
//
// Wrong-PIN response is 401 with {error:"pin-invalid", attemptsRemaining}.
// Expired PIN or third wrong attempt is 410 with
// {error:"pin-expired"} or {error:"attempts-exceeded"} so the UI can
// drop the stored requestId and route the operator back to /login.
//
// Successful verify mints a self-signed session JWT (same handover key
// used for /auth/handover; KC 24.7+ removed legacy token-exchange so
// server-side impersonation isn't available without a real user
// token), sets the catalyst_session HttpOnly Secure SameSite=Lax cookie,
// and returns {ok:true}. The UI then navigates to /wizard via
// client-side routing.
func (h *Handler) HandlePinVerify(w http.ResponseWriter, r *http.Request) {
	if h.handoverSigner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "auth-unconfigured",
			"detail": "handover signer unavailable",
		})
		return
	}

	var body pinVerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "invalid-body",
			"detail": "request body must be JSON {email, pin, requestId}",
		})
		return
	}
	email := strings.TrimSpace(body.Email)
	pin := strings.TrimSpace(body.PIN)
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "email-required",
			"detail": "email is required",
		})
		return
	}
	if len(pin) != 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "pin-invalid",
			"detail": "pin must be 6 digits",
		})
		return
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error":  "pin-invalid",
				"detail": "pin must be 6 digits",
			})
			return
		}
	}

	store := h.pinStoreFor()
	result, remaining := store.verify(email, pin, body.RequestID)
	switch result {
	case verifyOK:
		// fallthrough below; no early return
	case verifyWrongPIN:
		h.log.Warn("pin/verify: wrong code", "email", email, "requestID", body.RequestID, "remaining", remaining)
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error":             "pin-invalid",
			"detail":            "code incorrect",
			"attemptsRemaining": remaining,
		})
		return
	case verifyAttemptsLocked:
		h.log.Warn("pin/verify: attempts exceeded", "email", email, "requestID", body.RequestID)
		writeJSON(w, http.StatusGone, map[string]string{
			"error":  "attempts-exceeded",
			"detail": "too many wrong attempts; request a new code",
		})
		return
	case verifyExpired:
		h.log.Info("pin/verify: expired", "email", email, "requestID", body.RequestID)
		writeJSON(w, http.StatusGone, map[string]string{
			"error":  "pin-expired",
			"detail": "code expired; request a new code",
		})
		return
	case verifyNotFound:
		writeJSON(w, http.StatusGone, map[string]string{
			"error":  "pin-expired",
			"detail": "no active code for this email",
		})
		return
	case verifyRequestMismatch:
		// Treat as a stale tab — push the operator back to /login.
		writeJSON(w, http.StatusGone, map[string]string{
			"error":  "pin-expired",
			"detail": "request expired; reload and try again",
		})
		return
	}

	// ── On match: mint session JWT + set cookie ─────────────────────────────
	sessionClaims := jwt.MapClaims{
		"iss":            pinIssuer,
		"sub":            email, // email == subject; downstream reads claims.Email
		"email":          email,
		"email_verified": true,
		"role":           pinSessionRole,
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(pinSessionTTL).Unix(),
		"jti":            uuid.NewString(),
		"typ":            "session",
	}
	accessToken, err := h.handoverSigner.SignCustomClaims(sessionClaims)
	if err != nil {
		h.log.Error("pin/verify: SignCustomClaims failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "session-signing-failed",
			"detail": "could not mint session",
		})
		return
	}

	cookieMaxAge := int(pinSessionTTL.Seconds())
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
	// catalyst_refresh placeholder (cleared) — keeps cookie hygiene clean
	// after a magic-link-era login.
	http.SetCookie(w, &http.Cookie{
		Name:     "catalyst_refresh",
		Value:    "",
		Path:     "/",
		Domain:   cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	h.log.Info("pin/verify: session established",
		"email", email,
		"requestID", body.RequestID,
		"expires_in", cookieMaxAge,
	)

	writeJSON(w, http.StatusOK, pinVerifyResponse{OK: true})
}

// HandleAuthLogout handles DELETE /api/v1/auth/session.
//
// Clears the session cookie AND returns a JSON body containing the
// Keycloak end_session_endpoint URL so the UI can hard-navigate there
// to also drop the upstream KC SSO session. Without that second hop,
// the OIDC PKCE auth-guard on next page load silently re-authenticates
// against the still-active KC session and the operator ends up
// logged in as the same identity — the "sign-out is broken" symptom
// caught live 2026-05-04.
//
// Cookie-clear MUST mirror the EXACT same Path / Domain / Secure /
// SameSite the cookie was set with. Browsers require an exact-match
// Set-Cookie to delete; a mismatched Domain creates a NEW empty cookie
// scoped to the current host while the original cookie scoped to the
// shared parent domain stays alive (and continues to authenticate
// every request via Cookie ordering).
func (h *Handler) HandleAuthLogout(w http.ResponseWriter, r *http.Request) {
	cfg := h.authConfig
	if cfg != nil {
		cfg.ClearSessionCookie(w)
	}
	// Mirror the exact attributes used in the PIN-verify SetCookie at
	// line 478-487: Domain from CATALYST_SESSION_COOKIE_DOMAIN, Secure
	// derived from request scheme, SameSite=Lax (NOT Strict — Strict
	// blocks the cookie on cross-site navigations including the KC
	// post-logout redirect back to this origin, defeating the second
	// hop).
	cookieDomain := os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN")
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		Domain:   cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     "catalyst_refresh",
		Value:    "",
		Path:     "/",
		Domain:   cookieDomain,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	// Build the Keycloak end_session_endpoint URL. Returns "" when KC
	// is not wired (CATALYST_KC_ADDR unset, e.g. CI / contabo bring-up).
	// In that case the UI just clears local state and stays on /login.
	logoutURL := buildKeycloakLogoutURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"keycloakLogoutURL": logoutURL,
	})
}

// buildKeycloakLogoutURL composes the OIDC RP-Initiated Logout URL from
// the live env. Returns empty when KC isn't configured.
//
// Format (Keycloak v19+, RP-initiated logout draft):
//
//	{kc-addr}/realms/{realm}/protocol/openid-connect/logout?
//	  client_id=<catalyst-zero-ui>
//	  &post_logout_redirect_uri=<console-origin>/login
//
// post_logout_redirect_uri MUST be in the catalyst-zero-ui client's
// `validRedirectUris` (set by the realm import) — bp-keycloak's realm
// JSON includes `https://console.<sov>/*` so /login is covered.
func buildKeycloakLogoutURL(r *http.Request) string {
	kcAddr := strings.TrimRight(os.Getenv("CATALYST_KC_ADDR"), "/")
	if kcAddr == "" {
		return ""
	}
	realm := os.Getenv("CATALYST_KC_REALM")
	if realm == "" {
		realm = "openova"
	}
	clientID := os.Getenv("CATALYST_KC_CLIENT_ID")
	if clientID == "" {
		clientID = "catalyst-zero-ui"
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" {
		scheme = "http"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	postLogout := scheme + "://" + host + "/login"
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("post_logout_redirect_uri", postLogout)
	return kcAddr + "/realms/" + realm + "/protocol/openid-connect/logout?" + q.Encode()
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
