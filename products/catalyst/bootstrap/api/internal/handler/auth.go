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
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/smtp"
	"net/textproto"
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
//
// #2940 (2026-06-03): was `const pinIssuer = "https://console.openova.io"`
// — a Pillar 5 anti-tether hardcode. A franchised Sovereign post-
// bp-self-sovereign-cutover (e.g. console.<franchise-fqdn>) still
// stamped the mothership URL as `iss` claim, so downstream JWT
// validation rejected the session as foreign-issuer. Now reads from
// env CATALYST_PIN_ISSUER; falls back to the legacy hardcode for
// back-compat with pre-#2940 Sovereigns. Per-Sovereign overlays set
// the env var via the catalystApi.env additional-env patch in the
// per-Sovereign HelmRelease overlay (dual-mode contract — see
// chart/templates/api-deployment.yaml line 1267+ for context).
var pinIssuer = func() string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_PIN_ISSUER")); v != "" {
		return v
	}
	return "https://console.openova.io"
}()

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
// viewer < developer < operator < admin < owner.
//
// #3374 Layer-C (2026-06-17): the PIN session tier is NO LONGER a hard
// `owner` constant. The prior `const pinSessionTier = "owner"` +
// `const pinSessionRealmRole = "catalyst-owner"` made EVERY email that
// could receive a PIN a self-signed Sovereign owner, ignoring the
// user's real Keycloak group state — System A in #3374 §3-c, the
// self-signed assertion that ignored the realm (law #4 violation). The
// tier is now derived from live Keycloak group membership by
// pinSessionAuthority(): membership in /sovereign-admins → admin (the
// ONE decision point, #3374 §2 law #4); otherwise viewer. A non-owner
// who PIN-authenticates is NO LONGER silently granted owner.
//
// pinSessionAdminTier is what a member of /sovereign-admins receives.
// `admin` (not `owner`) is the group-conferred tier: it carries the
// catalyst-admin realm role (now composited onto the /sovereign-admins
// group in configmap-sovereign-realm.yaml) and lights up the BSS + RBAC
// console nav. `owner` remains reserved for the handover-authenticated
// Sovereign owner (auth_handover.go, RS256-signed by the mothership),
// the genuine ownership proof — never minted from a bare PIN.
const pinSessionAdminTier = "admin"

// pinSessionDefaultTier is what a PIN-authenticated user NOT in
// /sovereign-admins receives — read-only viewer. Never owner.
const pinSessionDefaultTier = "viewer"

// pinSovereignAdminsGroupPath is the canonical KC group whose
// membership confers console admin (mirrors the realm import's
// /sovereign-admins group + every app's groups-claim JMESPath).
const pinSovereignAdminsGroupPath = "/sovereign-admins"

// userGroupReader is the optional capability the concrete Keycloak
// client (*keycloak.Client) satisfies via UserGroupPaths. The narrow
// keycloakClient interface (EnsureUser/ImpersonateToken) is preserved
// for the existing test fakes; group-derived authority is reached by a
// runtime type-assert so a fake without the method degrades to the
// OPERATOR_EMAIL fallback rather than failing to compile or panicking.
type userGroupReader interface {
	UserGroupPaths(ctx context.Context, email string) ([]string, error)
}

// isConfiguredOperatorEmail reports whether `email` (already lowercased +
// trimmed) is the exactly-configured Sovereign owner (OPERATOR_EMAIL /
// orgEmail). This is the ONE identity that is admin-by-ownership: their
// mailbox proved control of the Sovereign's mail domain at PIN-issue, and
// they are the franchise owner the deployment was provisioned for. Anything
// else returns false — a NON-owner is NEVER elevated by this check.
func isConfiguredOperatorEmail(email string) bool {
	opEmail := strings.ToLower(strings.TrimSpace(os.Getenv("OPERATOR_EMAIL")))
	return opEmail != "" && opEmail == email
}

// pinSessionAuthority resolves the (tier, realmRoles) a PIN-authenticated
// email is entitled to, from live Keycloak group membership — the ONE
// admin-authority source (#3374 §2 law #4, Layer C) — PLUS an
// owner-is-always-admin guarantee for the configured Sovereign owner.
//
// Resolution:
//  1. If the Keycloak client exposes UserGroupPaths and the email is in
//     /sovereign-admins → admin tier + [catalyst-admin] (the group also
//     composites catalyst-admin in the realm import, so a federated
//     login of the same user is admin too — one source, both paths).
//  2. If KC is reachable but the email is NOT in /sovereign-admins →
//     admin ONLY when the email is the configured Sovereign owner
//     (OPERATOR_EMAIL); otherwise viewer (read-only). A non-owner is
//     NEVER silently granted admin.
//  3. If KC is unreachable / the client lacks the capability (CI, a
//     just-installed Sovereign whose realm hasn't converged) → fall
//     back to the configured operator email: an exact match yields
//     admin (the chroot owner whose mailbox proved control), anything
//     else viewer. This keeps the owner working through a realm-import
//     race WITHOUT the old blanket owner-for-everyone grant.
//
// #4280 (2026-06-25): the owner-is-admin guarantee was previously confined
// to branch 3 (the KC-unreachable fallback). On a converged Sovereign whose
// realm import had NOT seeded the owner into /sovereign-admins (a seeding
// race, a pre-#3263 realm, or a manual realm reset), branch 2 returned
// `viewer` for the owner — silently stripping every catalog-edit affordance
// (useCatalogAdmin → false) so the dual-logo picker + name editor vanished
// with no operator-visible reason. The owner is the owner regardless of KC
// reachability; the OPERATOR_EMAIL elevation now applies on BOTH the
// KC-answered-not-in-group branch and the fallback branch. "Non-owner never
// silently admin" is preserved — only the exactly-configured owner email is
// elevated, never any other PIN-authenticated user.
func (h *Handler) pinSessionAuthority(ctx context.Context, email string) (tier string, realmRoles []string) {
	email = strings.ToLower(strings.TrimSpace(email))
	if reader, ok := h.keycloakClientFor().(userGroupReader); ok && reader != nil {
		paths, err := reader.UserGroupPaths(ctx, email)
		if err != nil {
			h.log.Warn("pin/verify: KC group lookup failed; using operator-email fallback",
				"email", email, "err", err)
		} else {
			for _, p := range paths {
				if strings.EqualFold(strings.TrimSpace(p), pinSovereignAdminsGroupPath) {
					return pinSessionAdminTier, []string{"catalyst-admin"}
				}
			}
			// KC answered authoritatively: not in /sovereign-admins. The
			// configured Sovereign owner is admin-by-ownership even so — the
			// realm seed may not have converged (#4280); a non-owner stays
			// viewer.
			if isConfiguredOperatorEmail(email) {
				h.log.Info("pin/verify: owner not yet in /sovereign-admins; granting admin by OPERATOR_EMAIL ownership (#4280)",
					"email", email)
				return pinSessionAdminTier, []string{"catalyst-admin"}
			}
			return pinSessionDefaultTier, nil
		}
	}
	// Fallback: KC unavailable or capability absent.
	if isConfiguredOperatorEmail(email) {
		return pinSessionAdminTier, []string{"catalyst-admin"}
	}
	return pinSessionDefaultTier, nil
}

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

// requestHost returns the originating host of a request, preferring the
// X-Forwarded-Host header set by the upstream proxy (Envoy / Cilium
// Gateway) over r.Host, with any :port suffix stripped. The Sovereign
// catalyst-api sits behind a gateway, so r.Host can be the in-cluster
// Service authority; X-Forwarded-Host carries the real browser host
// (console.demo.omani.homes) the cookie must be scoped to.
func requestHost(r *http.Request) string {
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	// X-Forwarded-Host may carry a comma-separated proxy chain; take the
	// first (client-facing) entry.
	if i := strings.IndexByte(host, ','); i >= 0 {
		host = strings.TrimSpace(host[:i])
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.ToLower(host)
}

// sessionCookieDomain derives the Domain attribute for the catalyst_session
// cookie from the REQUEST HOST (#4101, 2026-06-22).
//
// Resolution order:
//  1. CATALYST_SESSION_COOKIE_DOMAIN env — explicit operator override
//     (used on Catalyst-Zero / contabo, e.g. "console.openova.io"). Wins
//     unconditionally so existing deployments are unaffected.
//  2. Request host under the Sovereign FQDN (console.<fqdn>, api.<fqdn>,
//     the bare <fqdn>, or any *.<fqdn>): scope to ".<SOVEREIGN_FQDN>" so
//     the cookie covers console./api./marketplace. of the Sovereign's own
//     front door (the G113-followup hw86 behaviour — preserved).
//  3. Pool-domain Org host (console.<org>.<pool-zone> on a Sovereign whose
//     FQDN is a DIFFERENT registrable domain — e.g. console.demo.omani.homes
//     on an omantel.biz Sovereign): strip the leading service label
//     (console./api./marketplace.) and scope to ".<org>.<pool-zone>"
//     (".demo.omani.homes"). This covers the Org's console + api +
//     marketplace hosts WITHOUT bleeding the session to sibling Orgs on the
//     same pool zone (a ".omani.homes" wide cookie would let demo's session
//     reach acme.omani.homes — a cross-Org leak we deliberately avoid).
//  4. No SOVEREIGN_FQDN and an unrecognised host (CI, local dev): empty
//     Domain → host-only cookie (unchanged legacy fallback).
//
// The empty-Domain return is also used when the host can't be sensibly
// scoped (a bare hostname with no dot), keeping the cookie host-only rather
// than emitting an invalid Domain attribute the browser would reject.
func sessionCookieDomain(r *http.Request) string {
	if v := strings.TrimSpace(os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN")); v != "" {
		return v
	}
	host := requestHost(r)
	sovFQDN := strings.ToLower(strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")))

	// Request host belongs to the Sovereign's own zone → .<SOVEREIGN_FQDN>.
	if sovFQDN != "" {
		if host == sovFQDN || strings.HasSuffix(host, "."+sovFQDN) {
			return "." + sovFQDN
		}
	}

	// Pool-domain Org host (or any host outside the Sovereign zone): scope
	// to the per-Org suffix = host minus its leading service label. For
	// "console.demo.omani.homes" that yields ".demo.omani.homes".
	if suffix := orgCookieSuffix(host); suffix != "" {
		return "." + suffix
	}

	// Last-resort fallback: preserve the legacy .<SOVEREIGN_FQDN> when we
	// have one (e.g. an odd host shape on the Sovereign) so we never widen
	// behaviour for the Sovereign's own console.
	if sovFQDN != "" {
		return "." + sovFQDN
	}
	return ""
}

// orgCookieSuffix strips a single leading service label (console / api /
// marketplace / auth) from a per-Org host and returns the remaining
// suffix, which is the correct cookie Domain seed for that Org's family of
// subdomains. Returns "" when the host has no recognisable service label
// or is too short to scope safely (≤2 labels → would over-widen to the
// registrable domain).
//
// Examples:
//
//	console.demo.omani.homes   → demo.omani.homes
//	api.demo.omani.homes       → demo.omani.homes
//	marketplace.acme.omani.rest→ acme.omani.rest
//	demo.omani.homes           → "" (no service label; don't over-scope)
//	omani.homes                → "" (registrable apex; never set)
func orgCookieSuffix(host string) string {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" {
		return ""
	}
	labels := strings.Split(host, ".")
	if len(labels) < 4 {
		// Need at least <service>.<org>.<zone-l2>.<zone-tld> to safely
		// scope to .<org>.<zone>. Fewer labels can't be narrowed without
		// risking a registrable-apex cookie.
		return ""
	}
	switch labels[0] {
	case "console", "api", "marketplace", "auth":
		return strings.Join(labels[1:], ".")
	}
	return ""
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
	Sent         bool   `json:"sent"`
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
	//
	// tryReserve (not canIssue) is used so check-and-reserve is atomic
	// under a single mutex acquisition. The earlier canIssue+put split
	// allowed three concurrent goroutines to all observe "no entry",
	// pass the gate, and then race EnsureUser. tryReserve stamps a
	// placeholder entry on success; put(...) below overwrites it with
	// the actual PIN, and drop(...) rolls back the cooldown on a
	// downstream failure.
	store := h.pinStoreFor()
	if ok, retry := store.tryReserve(email); !ok {
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
		// Rollback the reservation: this gate's failure isn't the
		// operator's fault and a follow-up retry must not be punished by
		// the 60s cooldown.
		store.drop(email)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "auth-unconfigured",
			"detail": "CATALYST_OPENOVA_KC_SA_CLIENT_SECRET not set",
		})
		return
	}
	if _, err := kc.EnsureUser(r.Context(), email, "openova-users"); err != nil {
		// Rollback so a Keycloak transient (502 from KC, etc) doesn't
		// punish the operator for the next 60 seconds.
		store.drop(email)
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
		// Rollback: a crypto/rand failure is a server-side issue, not an
		// operator-driven retry storm.
		store.drop(email)
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

		// Categorise the failure so the test bench + operator can tell
		// "relay broken" apart from "recipient mailbox does not exist".
		//
		// Go's net/smtp surfaces the server's reply line as
		// *textproto.Error{Code, Msg}. RFC 5321 §4.2.1: 5xx = permanent
		// caller fault (bad recipient, mailbox not found, policy reject);
		// 4xx = transient relay fault (greylisted, rate-limited); the
		// non-textproto path covers TCP/TLS/auth failures upstream of any
		// reply (relay unreachable, certificate invalid, AUTH refused).
		//
		// We distinguish these because the bench (WBS Cov-bench C6-010)
		// runs synthetic addresses like wbs-cov-redeem-<ts>@openova.io
		// which Stalwart rejects 550 "Mailbox does not exist" — that's a
		// well-formed bench result for the test design, NOT a sign that
		// the relay is broken for real customer addresses. The previous
		// opaque 502 swallowed this distinction and made every relay
		// transient look identical to a bad recipient.
		var smtpErr *textproto.Error
		if errors.As(err, &smtpErr) {
			h.log.Error("pin/issue: SMTP rejected",
				"email", email, "requestID", requestID,
				"smtpCode", smtpErr.Code, "smtpMsg", smtpErr.Msg)
			// 5xx from the server = permanent recipient/policy failure.
			// Surface 422 so the caller knows retry won't help.
			if smtpErr.Code >= 500 && smtpErr.Code < 600 {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
					"error":    "email-rejected",
					"detail":   "mail server rejected this recipient",
					"smtpCode": smtpErr.Code,
				})
				return
			}
			// 4xx (transient) → 502 so the operator can retry.
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error":    "email-send-failed",
				"detail":   "mail server temporarily unavailable",
				"smtpCode": smtpErr.Code,
			})
			return
		}

		// Non-protocol error (TCP/TLS/auth/DNS) — the relay itself is
		// unreachable or misconfigured. 502 is correct.
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
		Sent:         true,
		RequestID:    requestID,
		ExpiresInSec: int(pinTTL.Seconds()),
	})
}

// sendPinEmail is a package-level var so tests can swap in a stub
// without requiring an SMTP relay. The default implementation sends
// via Stalwart per smtpAddr().
var sendPinEmail = sendPinEmailDefault

// pinEmailMessage assembles the full RFC 5322 message (headers + RFC
// 2046 multipart/alternative body) for a sign-in-code mail. Factored
// out of sendPinEmailDefault so tests can assert on the wire format
// without an SMTP dial.
func pinEmailMessage(from, to, subject, plain, html, boundary string) string {
	headers := strings.Join([]string{
		// RFC 5322 §3.6.1: Date is a REQUIRED originator field; Stalwart
		// relays as-is, so its absence breaks client sorting/threading
		// and feeds spam scoring (#5104 facet C).
		"Date: " + time.Now().Format(time.RFC1123Z),
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
	return headers + "\r\n" + body
}

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
	loginURL := pinEmailLoginURL()
	plain := pinEmailPlainText(pin, loginURL)
	html := pinEmailHTML(pin, loginURL)

	// RFC 2046 multipart/alternative — clients render HTML when they
	// support it, fall back to plain otherwise. Boundary is a UUID so it
	// can never collide with body content.
	boundary := strings.ReplaceAll(uuid.NewString(), "-", "")
	msg := pinEmailMessage(from, to, subject, plain, html, boundary)

	smtpUser := os.Getenv("CATALYST_SMTP_USER")
	smtpPass := os.Getenv("CATALYST_SMTP_PASS")

	var authMethod smtp.Auth
	if smtpUser != "" && smtpPass != "" {
		host := strings.Split(addr, ":")[0]
		authMethod = smtp.PlainAuth("", smtpUser, smtpPass, host)
	}

	return smtp.SendMail(addr, authMethod, from, []string{to}, []byte(msg))
}

// pinEmailLoginURL returns the console login URL the PIN email body
// should direct the operator to. Chroot mode (SOVEREIGN_FQDN env set)
// sends tenant users to THEIR Sovereign — `https://console.<fqdn>/login`
// — so the PIN they're about to copy actually unlocks the tenant
// workspace they signed up for. Mothership mode (SOVEREIGN_FQDN unset)
// keeps the historical `https://console.openova.io/sovereign/login`
// target because that's where operator sign-in lands on Catalyst-Zero.
//
// TBD-A68 (#1994, 2026-05-19): pre-fix the URL was hardcoded to
// `console.openova.io/sovereign/login` regardless of which deployment
// mode the catalyst-api ran in, which sent every Sovereign tenant
// straight back to the mothership login form and made PIN sign-in a
// dead end on franchised Sovereigns.
func pinEmailLoginURL() string {
	if fqdn := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN")); fqdn != "" {
		return "https://console." + fqdn + "/login"
	}
	return "https://console.openova.io/sovereign/login"
}

// pinEmailPlainText is the text/plain alternative — terse, copy-friendly,
// single-shot copy of the 6-digit code.
func pinEmailPlainText(pin, loginURL string) string {
	return "Your OpenOva sign-in code is:\r\n\r\n" +
		"  " + pin + "\r\n\r\n" +
		"Enter it at " + loginURL + "\r\n" +
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
func pinEmailHTML(pin, loginURL string) string {
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
	// Display host = the URL stripped of scheme + path so the visible
	// hyperlink text reads `console.<fqdn>` (or `console.openova.io` on
	// the mothership) rather than the full URL.
	loginHost := loginURL
	loginHost = strings.TrimPrefix(loginHost, "https://")
	loginHost = strings.TrimPrefix(loginHost, "http://")
	if i := strings.Index(loginHost, "/"); i >= 0 {
		loginHost = loginHost[:i]
	}
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
      <p style="margin:0 0 24px 0;font-size:14px;line-height:1.55;color:` + textSec + `;">Enter this 6-digit code at <a href="` + loginURL + `" style="color:` + brand + `;text-decoration:none;font-weight:500;">` + loginHost + `</a> to finish signing in.</p>
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
	// G113 #2725 Option B Step 3 (2026-06-02): when the OIDC provider is
	// enabled (CATALYST_OIDC_PROVIDER_ENABLED=true), HandlePinVerify
	// includes a `kcBrokerURL` that catalyst-ui should hard-navigate the
	// browser to via window.location.assign() immediately after a
	// successful PIN verify. Hitting that URL triggers KC's
	// identity-broker, which does an OIDC roundtrip against catalyst-api
	// (using the catalyst_session cookie we JUST set as proof of PIN-
	// authentication), and drops a real KC realm session cookie on
	// auth.<sov-fqdn>. After that, every per-app SSO redirect succeeds
	// silently. The catalyst-ui handler should still set up its own
	// session state BEFORE navigating away (the broker flow eventually
	// returns the browser to /auth/callback on the UI origin via KC's
	// post-broker redirect).
	//
	// Empty when CATALYST_OIDC_PROVIDER_ENABLED is unset — catalyst-ui
	// then falls back to the existing /wizard navigation.
	KCBrokerURL string `json:"kcBrokerURL,omitempty"`
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
	// #3374 Layer-C (2026-06-17): the tier + realm-role claims are
	// DERIVED FROM LIVE KEYCLOAK GROUP MEMBERSHIP, not hardcoded to
	// owner. pinSessionAuthority() reads /sovereign-admins membership —
	// the ONE admin-authority source (#3374 §2 law #4). A member is
	// stamped tier=admin + [catalyst-admin] (the same realm role the
	// group composites, so a federated login of the same user is admin
	// too — one source, both paths); a NON-member who PIN-authenticates
	// is stamped tier=viewer and is NOT silently granted owner (the
	// System-A self-signed-owner defect, killed). The genuine Sovereign
	// owner is established by the handover path (auth_handover.go),
	// RS256-signed by the mothership — never by a bare PIN.
	//
	// Per CLAUDE.md INVIOLABLE-PRINCIPLES #5 (least privilege): the
	// stamp happens ONLY at PIN-verify (i.e. only after the operator
	// proved IMAP control); pre-PIN sessions never carry these claims.
	sessionTier, sessionRealmRoles := h.pinSessionAuthority(r.Context(), email)

	// ── #4110 Org-console scoping (THE security fix) ────────────────────
	//
	// pinSessionAuthority() above resolves authority purely from the
	// SOVEREIGN realm's /sovereign-admins membership and IGNORES the
	// request host. On a Sovereign that also serves Org pool-domain
	// consoles (console.<org>.<pool-zone> e.g. console.demo.omani.homes),
	// that meant a customer PIN-authenticating on THEIR Org console was
	// minted the SAME sovereign-admin session as the operator on the
	// Sovereign's own front door — full god-mode (provisioning wizard,
	// deployment-stream, every Org, cloud view, sovereign DNS/parent-
	// domain/BSS settings). Live privilege escalation on omantel.biz.
	//
	// The trust anchor is the REQUEST HOST: the Cilium Gateway routes the
	// Org console host to this catalyst-api and sets X-Forwarded-Host to
	// the real browser host, which a browser on the Org console can never
	// forge. When the host resolves (via the tenant registry) to a
	// tenant_kind=org console, the session is FORCED Org-scoped: a
	// non-sovereign tier (org-admin) + the org slug, NEVER catalyst-admin/
	// sovereign-admin — regardless of what /sovereign-admins says. The
	// org slug is also stamped so org-scoped handlers + the console confine
	// to this one Organization. The API authz gate (OrgScopeGuard) then
	// 403s any sovereign/other-org endpoint for this session (deny-by-
	// default outside the Org-safe allowlist), so the confinement holds
	// even if the SPA is bypassed.
	orgClaim := ""
	if scope, ok := h.resolveOrgScope(r); ok {
		sessionTier = orgScopedTier
		sessionRealmRoles = []string{orgScopedRealmRole}
		orgClaim = scope.Org
		h.log.Info("pin/verify: Org-scoped session",
			"email", email, "org", scope.Org, "host", scope.Host)
	}

	sessionClaims := jwt.MapClaims{
		"iss":            pinIssuer,
		"sub":            email, // email == subject; downstream reads claims.Email
		"email":          email,
		"email_verified": true,
		"role":           pinSessionRole,
		"tier":           sessionTier,
		"realm_access": map[string]any{
			"roles": sessionRealmRoles,
		},
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(pinSessionTTL).Unix(),
		"jti": uuid.NewString(),
		"typ": "session",
	}
	// Stamp the Org slug only for an Org-scoped session — both wire tags
	// (`org` legacy + `org_id` Wave-1b) so auth.Claims.UnmarshalJSON picks
	// it up regardless of which it reads. A Sovereign-admin session never
	// carries an org claim.
	if orgClaim != "" {
		sessionClaims["org"] = orgClaim
		sessionClaims["org_id"] = orgClaim
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
	// #4101 (2026-06-22): derive the cookie Domain from the REQUEST HOST,
	// not blindly from SOVEREIGN_FQDN. A pool-domain Org console is served
	// at console.<org>.<pool-zone> (e.g. console.demo.omani.homes) by the
	// SAME Sovereign catalyst-api whose SOVEREIGN_FQDN is the Sovereign
	// front door (omantel.biz). The previous logic stamped Domain=.omantel.biz
	// on a Set-Cookie issued for console.demo.omani.homes — the browser
	// REJECTS a cookie whose Domain doesn't cover the current host, so the
	// catalyst_session cookie was silently dropped and the very next whoami
	// on the same host returned 401 (BUG 1, live on omantel.biz 2026-06-22).
	cookieDomain := sessionCookieDomain(r)

	// ── Session-replay defence (TBD-F7 / #1730, Wave 28-F discovery) ─────
	//
	// `http.SetCookie` APPENDS a `Set-Cookie` header rather than replacing
	// any prior `Set-Cookie` on the same ResponseWriter. If an upstream
	// middleware (chroot proxy session-refresh, TestSession injector,
	// auth-gate fall-through) had pre-stamped a stale `catalyst_session`
	// Set-Cookie before HandlePinVerify ran, the response would carry
	// TWO `Set-Cookie: catalyst_session=...` headers. curl honours the
	// LAST one (so curl-driven PIN cycles work). Playwright's cookie
	// jar follows the WHATWG spec and stores both in arrival order, but
	// some Chromium versions surface the FIRST one to scripts via
	// `context.cookies()`, producing the observed "OLD JWT" symptom
	// even though our freshly-minted JWT is on the wire.
	//
	// Defensive fix: drop any `Set-Cookie` header set by earlier code on
	// this response before emitting the canonical pair below. The freshly
	// minted JWT in `accessToken` (built from `SignCustomClaims` above)
	// is the ONLY catalyst_session value the browser receives.
	w.Header().Del("Set-Cookie")

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

	// G113 #2725 followup (hw86 2026-06-02 20:35Z): the kcBrokerURL
	// plumbing originally introduced in PR #2727 emitted a URL of the
	// form `/realms/<realm>/broker/<alias>/login?kc_idp_hint=...` which
	// Keycloak REJECTS as `invalidRequestMessage` on direct invocation
	// (that endpoint is session-scoped — needs session_code/client_id/
	// tab_id from an active KC auth flow). Live evidence from hw86 KC
	// log: `IDENTITY_PROVIDER_LOGIN_ERROR error="invalidRequestMessage"
	// identity_provider="catalyst-pin"` when catalyst-ui hard-navigated
	// the user there post-PIN.
	//
	// With the bp-keycloak realm import now binding the Identity
	// Provider Redirector execution to `defaultProvider=catalyst-pin`
	// (PR #2730), the realm-session bootstrap happens naturally on the
	// FIRST per-app SSO redirect (Grafana/Gitea/Harbor/OpenBao) — KC
	// auto-delegates to catalyst-pin IdP which finds the catalyst_session
	// cookie set by PIN verify, mints a code, and KC drops the realm
	// session cookie. No separate pre-flight broker call needed.
	//
	// Returning empty `KCBrokerURL` makes catalyst-ui's VerifyPinPage
	// skip the (broken) hard-navigation and proceed to /dashboard. The
	// field stays in the JSON shape (omitempty) so a stale catalyst-ui
	// image that still checks for it continues to work.
	resp := pinVerifyResponse{OK: true}

	writeJSON(w, http.StatusOK, resp)
}

// buildKCBrokerURL constructs the Keycloak identity-broker URL that
// catalyst-ui should hard-navigate to after PIN verify in order to
// federate the catalyst-api OIDC IdP into the operator's KC realm
// session.
//
// G113 #2725 Option B Step 3 (2026-06-02): URL shape is
//
//	https://auth.<sov-fqdn>/realms/sovereign/broker/<alias>/login
//	  ?kc_idp_hint=<alias>
//	  &redirect_uri=https://console.<sov-fqdn>/auth/callback
//
// The `<alias>` matches the identityProviders[] entry added to the
// bp-keycloak realm-config ConfigMap (Step 4 of this chain). KC's
// broker handler does the OIDC roundtrip against catalyst-api at
// /oidc/auth → /oidc/token, then 302s the browser to redirect_uri
// with a one-time KC code that catalyst-ui's existing
// AuthCallbackPage handles via handleCallback().
//
// SOVEREIGN_FQDN env is set on every Sovereign by the bp-catalyst-
// platform chart from .Values.sovereignFqdn. Returns empty string on
// Catalyst-Zero (no per-Sovereign realm to broker into) or when
// SOVEREIGN_FQDN is unset.
func buildKCBrokerURL(r *http.Request) string {
	sovFQDN := strings.TrimSpace(os.Getenv("SOVEREIGN_FQDN"))
	if sovFQDN == "" {
		return ""
	}
	alias := os.Getenv("CATALYST_PIN_BROKER_CLIENT_ID")
	if alias == "" {
		alias = "catalyst-pin"
	}
	realm := os.Getenv("CATALYST_KC_REALM")
	if realm == "" {
		realm = "sovereign"
	}
	redirectURI := "https://console." + sovFQDN + "/auth/callback"
	return "https://auth." + sovFQDN + "/realms/" + realm + "/broker/" + alias + "/login" +
		"?kc_idp_hint=" + alias + "&redirect_uri=" + url.QueryEscape(redirectURI)
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
	// #4101: mirror the request-host-derived domain from HandlePinVerify so
	// logout clears the SAME cookie the login set — including pool-domain Org
	// consoles where the cookie is scoped to .<org>.<pool-zone> rather than
	// .<SOVEREIGN_FQDN>. Browsers require an exact Domain match to delete.
	cookieDomain := sessionCookieDomain(r)
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
// whoamiRealmAccess mirrors the Keycloak `realm_access` claim shape
// the wire emits to clients. Kept as its own type (not the auth.RealmAccess
// re-export) so a future addition there doesn't accidentally widen the
// public whoami contract.
type whoamiRealmAccess struct {
	Roles []string `json:"roles,omitempty"`
}

type whoamiResponse struct {
	Email         string             `json:"email"`
	Sub           string             `json:"sub"`
	Verified      bool               `json:"verified"`
	DeploymentID  string             `json:"deploymentId,omitempty"`
	SovereignFQDN string             `json:"sovereignFQDN,omitempty"`
	Mode          string             `json:"mode,omitempty"`
	Tier          string             `json:"tier,omitempty"`
	// #4110 — Org-scope context for the SPA. When the session is an
	// Org-scoped customer session (minted on console.<org>.<pool-zone>),
	// OrgScoped=true and Org carries the Organization slug. The console
	// reads OrgScoped to render the scoped chrome (hide the provisioning
	// wizard, deployment-stream, cross-org directory, sovereign DNS/parent-
	// domain/BSS settings) and scope every estate query to Org. Both are
	// omitempty so the Sovereign-admin whoami shape is byte-unchanged.
	OrgScoped bool   `json:"orgScoped,omitempty"`
	Org       string `json:"org,omitempty"`
	// RealmAccess is a pointer so `omitempty` actually drops the key when
	// the operator's session carries no RBAC claims. A struct value would
	// always serialize as `{}` regardless of omitempty (Go encoding/json
	// honours omitempty for structs only via `omitzero` in Go 1.24+, and
	// the catalyst-api still builds against earlier toolchains). Without
	// the pointer, pre-RBAC clients that decode the response into a typed
	// struct see `realm_access:{roles:null}` and break.
	RealmAccess *whoamiRealmAccess `json:"realm_access,omitempty"`
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
		Tier:     strings.ToLower(strings.TrimSpace(claims.Tier)),
	}
	// #4110 — surface the Org-scope so the SPA renders the scoped console.
	// The org-scoped marker is the tier (set by HandlePinVerify on an Org
	// pool-domain host); the Org slug rides the `org`/`org_id` claim.
	if claimsAreOrgScoped(claims) {
		resp.OrgScoped = true
		resp.Org = strings.TrimSpace(claims.Org)
	}
	if len(claims.RealmAccess.Roles) > 0 {
		resp.RealmAccess = &whoamiRealmAccess{
			Roles: append([]string(nil), claims.RealmAccess.Roles...),
		}
	}
	// EPIC-3 (#1184) tier→catalyst-* role projection: when the operator
	// holds a `tier` claim but the realm-access role list lacks the
	// matching `catalyst-<tier>` + `catalyst-viewer` entries (typical
	// on PIN-derived sessions and chroot-internal JWTs that don't run
	// through the protocol mapper), synthesize them so downstream UI
	// authorization checks (which look at realm_access.roles) work
	// regardless of how the session was minted.
	//
	// Pre-RBAC sessions (no tier, no realm_access roles) skip this entire
	// step so the `omitempty` on resp.RealmAccess drops the field — old
	// callers parsing the legacy {email,sub,verified} shape stay clean.
	if resp.Tier != "" || resp.RealmAccess != nil {
		if resp.RealmAccess == nil {
			resp.RealmAccess = &whoamiRealmAccess{}
		}
		whoamiInjectTierRoles(resp.RealmAccess, resp.Tier)
		// If the projection ended up empty (unknown tier, no upstream
		// roles), drop the field so the omitempty contract holds.
		if len(resp.RealmAccess.Roles) == 0 {
			resp.RealmAccess = nil
		}
	}

	// #4110 host-anchor (defense against a STALE / replayed sovereign-admin
	// cookie): the REQUEST HOST is the trust anchor, not the session JWT.
	// When this whoami arrived on a tenant_kind=org console host
	// (resolveOrgScope ok — the gateway-stamped X-Forwarded-Host an
	// Org-console browser can never forge), the response MUST render scoped
	// REGARDLESS of what the JWT claims — force orgScoped + the Org tier +
	// the Org realm role, overriding any sovereign tier/role the projection
	// above synthesized. This makes the SPA hide every sovereign-admin
	// surface even for a god-mode cookie minted before this fix shipped, so
	// the customer never needs to clear cookies. The Sovereign's own console
	// host (tenant_kind=otech) makes resolveOrgScope return false → the
	// operator whoami is byte-unchanged → ZERO regression.
	if scope, ok := h.resolveOrgScope(r); ok {
		resp.OrgScoped = true
		if strings.TrimSpace(resp.Org) == "" {
			resp.Org = scope.Org
		}
		resp.Tier = orgScopedTier
		resp.RealmAccess = &whoamiRealmAccess{Roles: []string{orgScopedRealmRole}}
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

// whoamiTierInheritance is the EPIC-3 §6.2 tier-inherit chain. Each
// tier inherits every entry below itself in the slice, so an operator
// at tier=admin holds catalyst-{viewer,developer,operator,admin}
// realm roles in addition to the tier-* RBAC ClusterRoles.
//
// owner inherits admin which inherits operator which inherits developer
// which inherits viewer. Same chain enforced by the
// platform/crossplane-claims chart's tierActions[*].inherits values.
var whoamiTierInheritance = []string{
	"viewer",
	"developer",
	"operator",
	"admin",
	"owner",
}

// whoamiInjectTierRoles guarantees that every catalyst-<inherited-tier>
// realm role is present in resp.RealmAccess.Roles when the operator's
// `tier` claim is set. PIN-derived sessions and a few chroot-internal
// JWT mint paths set the `tier` claim but skip the full role-list
// projection — without this enrichment, the EPIC-3 access-matrix UI's
// per-user role chips render as "viewer only" even for admins.
//
// Idempotent: existing roles are preserved; missing ones are appended
// in inheritance order so the chip list reads bottom-up (viewer first,
// then developer, ..., then the operator's own tier).
func whoamiInjectTierRoles(ra *whoamiRealmAccess, tier string) {
	tier = strings.ToLower(strings.TrimSpace(tier))
	if tier == "" {
		return
	}
	// Find the operator's tier index in the inheritance chain. Unknown
	// tiers (legacy `application-admin`, future tiers) are no-ops here.
	tierIdx := -1
	for i, t := range whoamiTierInheritance {
		if t == tier {
			tierIdx = i
			break
		}
	}
	if tierIdx < 0 {
		return
	}
	have := map[string]struct{}{}
	for _, r := range ra.Roles {
		have[strings.ToLower(strings.TrimSpace(r))] = struct{}{}
	}
	// If the operator's own tier role is already present, the upstream
	// (Keycloak protocol mapper, PIN-minted JWT, etc) authoritatively
	// shaped the role list — don't synthesize lower-tier inherited
	// entries on top of it. This preserves the TC-R-089-style PIN flow
	// where the JWT is minted with `[catalyst-owner]` only and the SPA
	// route-guard reads exactly that list. The inheritance projection
	// only fires when the caller passed a `tier` claim *without* the
	// matching catalyst-<tier> role (typical of chroot-internal JWTs
	// that set tier= but skip the role-list projection).
	tierRole := "catalyst-" + tier
	if _, ok := have[tierRole]; ok {
		return
	}
	for i := 0; i <= tierIdx; i++ {
		want := "catalyst-" + whoamiTierInheritance[i]
		if _, ok := have[want]; !ok {
			ra.Roles = append(ra.Roles, want)
			have[want] = struct{}{}
		}
	}
}

// HandleAuthSessionLogout is the SPA-driven POST logout that clears
// catalyst_session + catalyst_refresh cookies with Max-Age=0 + SameSite=Strict
// (same-origin XHR — strictest posture). Mirrors the wire shape contracted by
// auth_logout_test.go's TestHandleAuthSessionLogout_PostWireShape (qa-loop iter-4
// auth-session-logout-405 fix; restored 2026-05-10 after a cherry-pick lost it).
func (h *Handler) HandleAuthSessionLogout(w http.ResponseWriter, r *http.Request) {
	domain := strings.TrimSpace(os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN"))
	secure := strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	for _, name := range []string{"catalyst_session", "catalyst_refresh"} {
		var b strings.Builder
		fmt.Fprintf(&b, "%s=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict", name)
		if domain != "" {
			fmt.Fprintf(&b, "; Domain=%s", domain)
		}
		if secure {
			b.WriteString("; Secure")
		}
		w.Header().Add("Set-Cookie", b.String())
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"ok":true,"loggedOut":true}`+"\n")
}
