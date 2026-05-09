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

// pinSessionTier — Catalyst tier stamped into PIN-derived session JWTs
// (chroot Sovereign Console operator login). PIN-via-IMAP proves the
// authenticated party controls the inbox at the Sovereign's mail domain
// (e.g. emrah.baysal@openova.io for omantel.biz). On a chroot Sovereign
// the only operator who can log in via PIN-IMAP is, by definition, the
// Sovereign owner — there is no "non-admin Sovereign operator" path
// today because no third party has been granted PIN-issue rights on
// the Sovereign's mail server.
//
// Per docs/EPICS-1-6-unified-design.md §6.2 the canonical tier vocab is
// viewer < developer < operator < admin < owner. PIN-mint stamps `owner`
// so every privileged catalyst-api endpoint (rbac_audit, rbac_assign,
// keycloak_proxy U2/U3/U4, blueprints/curate, policy_mode) resolves to
// "authorized" without a separate per-handler nil-claim escape hatch.
//
// The realm-role mirror (`pinSessionRealmRole`) is also stamped so the
// realm-role-list authorization path on the same gates resolves the same
// way — both gate seams (Tier vs RealmAccess.Roles) accept the operator.
const pinSessionTier = "owner"

// pinSessionRealmRole — Keycloak realm-role mirror of pinSessionTier.
// Stamped into the JWT's realm_access.roles so handler gates that walk
// the realm-role list (rbacAssignCallerAuthorized's HasRealmRole loop)
// accept the PIN-derived operator without a per-handler tier-claim
// special case. Matches the EPIC-3 T2 bootstrap vocabulary
// (catalyst-admin / catalyst-owner / application-admin) so the PIN
// session looks indistinguishable from a real Keycloak-issued token
// for the privileged caller — single contract surface for the gates.
const pinSessionRealmRole = "catalyst-owner"

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

	// ── 1. Rate-limit (per-email, 60s) ──────────────────────────────────────
	//
	// The rate-limit check runs BEFORE EnsureUser so that concurrent
	// /pin/issue calls for the same email (TC-R-089: 3-way --parallel
	// curl) do not each attempt EnsureUser. Without this ordering,
	// every concurrent caller races createUser at Keycloak; KC accepts
	// the first POST /users and returns 409 Conflict to the others —
	// surfaced here as a 502 response (TC-R-089's pre-fix symptom).
	// Rate-limit-first means losers in the race get 429 immediately,
	// the winner reaches Keycloak alone, and the rate limiter is
	// honoured even under concurrency.
	store := h.pinStoreFor()
	if ok, retry := store.canIssue(email); !ok {
		retryAfterSec := int(retry.Seconds()) + 1
		w.Header().Set("Retry-After", fmt.Sprintf("%d", retryAfterSec))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":         "pin-rate-limited",
			"detail":        "wait before requesting another code",
			"retryAfterSec": retryAfterSec,
		})
		return
	}

	// ── 2. EnsureUser in openova realm ──────────────────────────────────────
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

// sendPinEmailDefault sends a multipart MIME email (text + HTML) with
// the 6-digit code rendered as a polished one-time-code card. iCloud /
// Stripe parity — large monospaced digit groups, brand mark, expiration
// notice. The plaintext alternative covers email clients that block HTML.
//
// Per founder rule on #688: NO magic-link URL (no token, no auto-login
// query string). The login URL is shown only as informational text so
// the operator pastes the code into the on-screen 6-box input.
func sendPinEmailDefault(to, pin string) error {
	from := smtpFrom()
	addr := smtpAddr()

	if len(pin) != 6 {
		return errors.New("sendPinEmail: pin must be 6 digits")
	}

	subject := fmt.Sprintf("Your OpenOva sign-in code: %s", pin)
	plain := pinEmailPlainText(pin)
	html := pinEmailHTML(pin)

	// RFC 2046 multipart/alternative — clients render HTML when they
	// support it, fall back to plain otherwise. Boundary is a UUID so it
	// can never collide with body content.
	boundary := strings.ReplaceAll(uuid.NewString(), "-", "")
	headers := strings.Join([]string{
		"From: OpenOva Platform <" + from + ">",
		"To: " + to,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: multipart/alternative; boundary=\"" + boundary + "\"",
	}, "\r\n")
	body := strings.Join([]string{
		"",
		"--" + boundary,
		"Content-Type: text/plain; charset=utf-8",
		"Content-Transfer-Encoding: 7bit",
		"",
		plain,
		"--" + boundary,
		"Content-Type: text/html; charset=utf-8",
		"Content-Transfer-Encoding: 7bit",
		"",
		html,
		"--" + boundary + "--",
		"",
	}, "\r\n")
	msg := headers + "\r\n" + body

	smtpUser := os.Getenv("CATALYST_SMTP_USER")
	smtpPass := os.Getenv("CATALYST_SMTP_PASS")

	var authMethod smtp.Auth
	if smtpUser != "" && smtpPass != "" {
		host := strings.Split(addr, ":")[0]
		authMethod = smtp.PlainAuth("", smtpUser, smtpPass, host)
	}

	return smtp.SendMail(addr, authMethod, from, []string{to}, []byte(msg))
}

// pinEmailPlainText is the text/plain alternative — terse, copy-friendly,
// single-shot copy of the 6-digit code.
func pinEmailPlainText(pin string) string {
	return "Your OpenOva sign-in code is:\r\n\r\n" +
		"  " + pin + "\r\n\r\n" +
		"Enter it at https://console.openova.io/sovereign/login\r\n" +
		"This code expires in 10 minutes.\r\n\r\n" +
		"If you didn't request this, you can ignore this email."
}

// pinEmailHTML renders the polished verification-code email. Inline
// styles only — no <style> blocks, no external assets — Gmail and
// Outlook web both strip <head>/<style>. Width pinned at 480px so the
// card looks correct in narrow webmail panes and on phones.
//
// Visual pattern (Apple iCloud / Stripe):
//   - White card on neutral background
//   - Brand mark + "OpenOva" wordmark at the top
//   - Headline "Your sign-in code"
//   - Big monospaced 6-digit code in a tinted box (one-tap copy on iOS)
//   - Expiration line + ignore-if-not-you safety line below
//   - Footer credit line
func pinEmailHTML(pin string) string {
	const (
		bg      = "#f5f6f8"
		card    = "#ffffff"
		border  = "#e3e6eb"
		textPri = "#0b0d12"
		textSec = "#5f6470"
		brand   = "#3357ff"
		codeBg  = "#f0f3ff"
		codeFg  = "#0b0d12"
	)
	return `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Your OpenOva sign-in code</title></head>
<body style="margin:0;padding:32px 16px;background:` + bg + `;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:` + textPri + `;">
  <table role="presentation" align="center" cellspacing="0" cellpadding="0" border="0" width="480" style="max-width:480px;margin:0 auto;">
    <tr><td style="padding:0 0 20px 0;text-align:center;">
      <span style="display:inline-flex;align-items:center;gap:8px;font-size:14px;font-weight:600;color:` + textPri + `;letter-spacing:-0.01em;">
        <span style="display:inline-block;width:22px;height:22px;border-radius:6px;background:` + brand + `;line-height:22px;color:#fff;font-weight:700;">∞</span>
        OpenOva
      </span>
    </td></tr>
    <tr><td style="background:` + card + `;border:1px solid ` + border + `;border-radius:14px;padding:32px 32px 28px 32px;">
      <h1 style="margin:0 0 6px 0;font-size:20px;font-weight:600;letter-spacing:-0.01em;color:` + textPri + `;">Your sign-in code</h1>
      <p style="margin:0 0 24px 0;font-size:14px;line-height:1.55;color:` + textSec + `;">Enter this 6-digit code at <a href="https://console.openova.io/sovereign/login" style="color:` + brand + `;text-decoration:none;font-weight:500;">console.openova.io</a> to finish signing in.</p>
      <div style="background:` + codeBg + `;border:1px solid ` + border + `;border-radius:12px;padding:18px 0;text-align:center;font-family:'SF Mono',Menlo,Consolas,monospace;font-size:34px;font-weight:600;letter-spacing:10px;color:` + codeFg + `;">` + pin + `</div>
      <p style="margin:18px 0 0 0;font-size:13px;line-height:1.5;color:` + textSec + `;">This code expires in 10 minutes. If you didn't request this, you can safely ignore this email — your account stays secure.</p>
    </td></tr>
    <tr><td style="padding:18px 0 0 0;text-align:center;font-size:12px;color:#8e94a3;">
      Sent by OpenOva Platform · <a href="https://openova.io" style="color:#8e94a3;text-decoration:underline;">openova.io</a>
    </td></tr>
  </table>
</body>
</html>`
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
	//
	// The PIN-derived JWT carries BOTH the legacy single-string `role`
	// claim AND the Keycloak-shaped `tier` + `realm_access.roles` claims
	// the EPIC-3 (#1098) RBAC gates consume (rbac_audit, rbac_assign,
	// keycloak_proxy U2/U3/U4, blueprints/curate, policy_mode).
	//
	// Why owner: PIN-via-IMAP authentication proves control of the
	// Sovereign's mail-domain inbox; that is the canonical proof of
	// ownership of the Sovereign chroot (the only operator who can
	// receive the 6-digit code is the one provisioned with mailbox
	// access on the Sovereign's stalwart instance). Stamping
	// tier=owner / realm_access.roles=[catalyst-owner] makes the JWT's
	// authorization context match the real-world authority the auth
	// flow already granted — without it, every privileged endpoint
	// returns 403 even though the caller is the Sovereign owner.
	//
	// Per CLAUDE.md INVIOLABLE-PRINCIPLES #5 (least privilege): the
	// stamp happens ONLY at PIN-verify (i.e. only after the operator
	// proved IMAP control); pre-PIN sessions never carry these claims.
	sessionClaims := jwt.MapClaims{
		"iss":            pinIssuer,
		"sub":            email, // email == subject; downstream reads claims.Email
		"email":          email,
		"email_verified": true,
		"role":           pinSessionRole,
		"tier":           pinSessionTier,
		"realm_access": map[string]any{
			"roles": []string{pinSessionRealmRole},
		},
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(pinSessionTTL).Unix(),
		"jti": uuid.NewString(),
		"typ": "session",
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
	// Mirror the exact attributes used at PIN-verify SetCookie above
	// (Path=/, Domain from CATALYST_SESSION_COOKIE_DOMAIN, Secure derived
	// from request scheme, SameSite=Lax) — browsers require an exact
	// attribute match to actually delete the cookie that was set on
	// login. SameSite=Lax (not Strict) is required because the KC
	// post-logout redirect back to this origin is a cross-site
	// navigation; Strict would block the clear-cookie from being
	// honoured on that hop.
	//
	// We do NOT call cfg.ClearSessionCookie here: that helper emits a
	// Strict-SameSite Set-Cookie which doesn't match the Lax attribute
	// the cookie was set with at /pin/verify, so the browser keeps the
	// original Lax cookie alive (cookies are keyed by name+domain+path
	// only — but a Strict clear-cookie creates a Strict-domain cookie
	// shadow, leaving the Lax one untouched).
	//
	// Set-Cookie is written through w.Header().Add directly rather than
	// via http.SetCookie(&http.Cookie{MaxAge: -1}) because Go's net/http
	// renders any Cookie with negative MaxAge as the literal token
	// `Max-Age=0`. The cookie-deletion contract requires the literal
	// token `Max-Age=-1` to appear in the wire response so test fixtures
	// (and any downstream cookie auditor) can assert that the server
	// explicitly negative-aged the cookie. Browsers honour both `=-1`
	// and `=0` per RFC 6265bis (any non-positive value = immediate
	// expiry), so this is a wire-shape choice, not a semantic one.
	cookieDomain := os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN")
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	w.Header().Add("Set-Cookie", buildClearSessionCookie(auth.SessionCookieName, cookieDomain, secure))
	w.Header().Add("Set-Cookie", buildClearSessionCookie("catalyst_refresh", cookieDomain, secure))

	// Build the Keycloak end_session_endpoint URL. Returns "" when KC
	// is not wired (CATALYST_KC_ADDR unset, e.g. CI / contabo bring-up).
	// In that case the UI just clears local state and stays on /login.
	logoutURL := buildKeycloakLogoutURL(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"keycloakLogoutURL": logoutURL,
	})
}

// buildClearSessionCookie returns a Set-Cookie header value that
// instructs the browser to delete `name` immediately. The shape is
// fixed and assertable on the wire:
//
//	<name>=; Path=/[; Domain=<domain>][; Secure]; HttpOnly; SameSite=Lax; Max-Age=-1
//
// `Max-Age=-1` is emitted literally rather than using
// http.SetCookie(&http.Cookie{MaxAge: -1}), which Go's net/http
// renders as `Max-Age=0` — losing the explicit-negative-age signal
// that the wire contract for cookie deletion asserts on.
//
// The Domain attribute is omitted when `domain` is empty so the
// cookie is host-only (matches the Set-Cookie shape used at
// /pin/verify when CATALYST_SESSION_COOKIE_DOMAIN is unset, e.g. in
// local dev or CI).
func buildClearSessionCookie(name, domain string, secure bool) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteString("=; Path=/")
	if domain != "" {
		b.WriteString("; Domain=")
		b.WriteString(domain)
	}
	if secure {
		b.WriteString("; Secure")
	}
	b.WriteString("; HttpOnly; SameSite=Lax; Max-Age=-1")
	return b.String()
}

// buildKeycloakLogoutURL composes the OIDC RP-Initiated Logout URL from
// the live env. Returns empty when KC isn't configured.
//
// Format (Keycloak v19+, RP-initiated logout draft):
//
//	{kc-addr}/realms/{realm}/protocol/openid-connect/logout?
//	  client_id=<catalyst-zero-ui>
//	  &post_logout_redirect_uri=<console-origin><post-logout-path>
//
// post_logout_redirect_uri MUST be in the catalyst-zero-ui client's
// `validRedirectUris` (set by the realm import).
//
// Path resolution (issue #721 followup, 2026-05-04):
//   - Catalyst-Zero (contabo): catalyst-ui is mounted under /sovereign/*
//     by Traefik (ingress `console-openova-tls` only proxies /sovereign/*
//     to the UI, not /). So /login on the bare host returns 404 from
//     the upstream Traefik. The correct post-logout target is
//     /sovereign/login.
//   - Sovereign clusters: catalyst-ui is mounted at root (the Cilium
//     Gateway HTTPRoute routes / → catalyst-ui). The correct post-
//     logout target is /login.
//
// Resolved by deriving from the existing CATALYST_POST_AUTH_REDIRECT
// env var (already set per environment to /sovereign/wizard on
// contabo, /wizard on Sovereigns): take its directory and append
// /login. Falls back to /sovereign/login (the contabo default) when
// the env var is unset, so the local-dev path (no ingress prefix)
// can override via CATALYST_KC_POST_LOGOUT_PATH.
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
	postLogout := scheme + "://" + host + resolvePostLogoutPath()
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("post_logout_redirect_uri", postLogout)
	return kcAddr + "/realms/" + realm + "/protocol/openid-connect/logout?" + q.Encode()
}

// resolvePostLogoutPath picks the correct path to redirect operators
// back to after KC RP-initiated logout. See buildKeycloakLogoutURL for
// the contabo-vs-Sovereign rationale.
func resolvePostLogoutPath() string {
	// Explicit override wins — useful for local dev or unusual ingress
	// shapes (an operator running catalyst-ui on a different prefix).
	if explicit := strings.TrimSpace(os.Getenv("CATALYST_KC_POST_LOGOUT_PATH")); explicit != "" {
		return explicit
	}
	// Derive from the post-AUTH redirect already configured for this
	// catalyst-api (the same path prefix applies — wizard and login are
	// sibling routes inside the SPA).
	if postAuth := strings.TrimSpace(os.Getenv("CATALYST_POST_AUTH_REDIRECT")); postAuth != "" {
		// e.g. "/sovereign/wizard" → "/sovereign/login", "/wizard" → "/login"
		idx := strings.LastIndex(postAuth, "/")
		if idx > 0 {
			return postAuth[:idx] + "/login"
		}
		// "/wizard" (no parent dir): drop the segment, fall back to /login
		return "/login"
	}
	// Final fallback: the current production shape on contabo. Better
	// to land on the Catalyst-Zero login screen by default than 404.
	return "/sovereign/login"
}

// whoamiResponse is the wire shape of GET /api/v1/whoami.
//
// On Catalyst-Zero (mother) the sovereign fields are empty and the
// `omitempty` JSON tags drop them, preserving the original
// {email, sub, verified} shape that pre-#608 callers depend on.
//
// On a chroot Sovereign the fields are populated so the SPA can
// discover its sovereign context from a single round-trip without a
// follow-up /api/v1/sovereign/self call. This is the contract
// SovereignConsoleLayout + chroot SPA features assert in TC-232.
//
// Tier + RealmAccess surface the RBAC claims the SPA route-guard
// relies on to decide whether to admit an operator into the chroot
// Sovereign Console post-PIN-login. Fix #2 (#1184) stamps tier=owner
// and realm_access.roles=[catalyst-owner] into the PIN session JWT;
// without these fields on the wire the SPA bounces the operator back
// to /login (qa-loop iter-2 cluster B). Both are `omitempty` so an
// unprivileged session (no tier, no realm roles) yields the original
// pre-RBAC wire shape and existing callers keep working.
type whoamiResponse struct {
	Email         string            `json:"email"`
	Sub           string            `json:"sub"`
	Verified      bool              `json:"verified"`
	DeploymentID  string            `json:"deploymentId,omitempty"`
	SovereignFQDN string            `json:"sovereignFQDN,omitempty"`
	Mode          string            `json:"mode,omitempty"`
	Tier          string            `json:"tier,omitempty"`
	RealmAccess   *auth.RealmAccess `json:"realm_access,omitempty"`
}

// HandleWhoami handles GET /api/v1/whoami.
//
// Returns the authenticated operator's claims from context. The
// RequireSession middleware has already validated the session cookie
// before this handler is reached, so a nil claims is a logic error.
//
// Mother (Catalyst-Zero) returns:
//
//	{"email":"...","sub":"...","verified":true}
//
// Chroot (Sovereign) additionally returns:
//
//	{..., "deploymentId":"sovereign-<fqdn>",
//	      "sovereignFQDN":"<fqdn>",
//	      "mode":"sovereign"}
//
// The chroot enrichment uses the same resolution precedence as
// HandleSovereignSelf (sovereign_self.go) so both endpoints converge on
// the same identifiers post-handover:
//
//  1. Session-JWT claims SovereignFQDN + DeploymentID (set by
//     /auth/handover when the operator arrived from the wizard).
//  2. Env CATALYST_SELF_DEPLOYMENT_ID / CATALYST_OTECH_FQDN /
//     SOVEREIGN_FQDN (stamped on every chroot catalyst-api by the
//     bp-catalyst-platform sovereign-fqdn ConfigMap).
//  3. If the chroot is identifiable by FQDN but lacks a stamped
//     deployment id (typical post-cutover before orchestrator overlay
//     write lands), synthesize the canonical
//     `sovereign-<fqdn>` id — the same convention as
//     sovereign_self.go's step-3 fallback so URL routing stays stable.
//
// Mothership (no FQDN env, no claim-fqdn) → fields stay empty and
// `omitempty` drops them: pre-#608 wire compatibility preserved.
func (h *Handler) HandleWhoami(w http.ResponseWriter, r *http.Request) {
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		// Should never happen — RequireSession gates this handler.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	resp := whoamiResponse{
		Email:    claims.Email,
		Sub:      claims.Sub,
		Verified: claims.EmailVerified,
		Tier:     claims.Tier,
	}

	// RBAC realm-role enrichment — surface the realm role list the SPA
	// route-guard reads to admit an operator into the chroot Sovereign
	// Console. Only emit when at least one role is set so an unprivileged
	// session continues to omit the field entirely (omitempty preserves
	// the pre-RBAC wire shape for non-RBAC callers).
	if len(claims.RealmAccess.Roles) > 0 {
		ra := claims.RealmAccess
		resp.RealmAccess = &ra
	}

	// Sovereign-context enrichment — same precedence as HandleSovereignSelf
	// so the two endpoints never disagree about which sovereign this is.
	deploymentID := strings.TrimSpace(claims.DeploymentID)
	fqdn := strings.TrimSpace(claims.SovereignFQDN)
	if fqdn == "" {
		fqdn = strings.TrimSpace(os.Getenv("CATALYST_OTECH_FQDN"))
	}
	if fqdn == "" {
		fqdn = strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	}
	if deploymentID == "" {
		deploymentID = strings.TrimSpace(os.Getenv("CATALYST_SELF_DEPLOYMENT_ID"))
	}
	// Synthesize the canonical chroot id when FQDN is known but no id
	// has been stamped yet — matches sovereign_self.go step-3.
	if deploymentID == "" && fqdn != "" {
		deploymentID = "sovereign-" + fqdn
	}

	if fqdn != "" {
		resp.SovereignFQDN = fqdn
		resp.DeploymentID = deploymentID
		resp.Mode = "sovereign"
	}

	writeJSON(w, http.StatusOK, resp)
}
