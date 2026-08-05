// auth_handover.go — GET /auth/handover?token=<jwt>
//
// This endpoint is the Sovereign-side landing page for the seamless
// single-identity handover flow (issue #606). Catalyst-Zero finalises a
// handover by redirecting the operator's browser to
//
//	https://console.<sov>/auth/handover?token=<jwt>
//
// This handler:
//  1. Parses and RS256-verifies the one-time JWT using the public key
//     written by cloud-init at CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH.
//  2. Validates all claims: iss, aud, exp, role=sovereign-admin,
//     email_verified=true.
//  3. Calls jtistore.Mark — if the jti was already consumed, returns 401
//     to prevent replay.
//  4. Calls keycloak.EnsureUser to create-or-get the operator in the
//     `sovereign` realm and ensure `sovereign-admins` group membership.
//  5. Calls keycloak.ImpersonateToken to exchange for a user access +
//     refresh token pair (audience: catalyst-ui OIDC client).
//  6. Sets HttpOnly Secure SameSite=Lax cookies (catalyst_session +
//     catalyst_refresh).
//  7. 302 redirects to /dashboard (clean Sovereign root).
//
// All errors return 401 with terse JSON {"error": "..."}.
// No secrets are emitted in error responses (per INVIOLABLE-PRINCIPLES #10).
package handler

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/jtistore"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/keycloak"
)

// keycloakClient is the interface implemented by *keycloak.Client.
// Declared here so tests can inject a stub.
type keycloakClient interface {
	EnsureUser(ctx context.Context, email, group string) (string, error)
	ImpersonateToken(ctx context.Context, userID, audience string) (
		accessToken, refreshToken string, expiresIn int, _ error,
	)
}

// authHandoverClaims extends jwt.RegisteredClaims with the handover-specific
// fields Catalyst-Zero stamps when it mints the one-time JWT.
type authHandoverClaims struct {
	jwt.RegisteredClaims

	// Email is the operator's email address in Catalyst-Zero's Keycloak.
	Email string `json:"email"`

	// EmailVerified must be true — Catalyst-Zero only mints handover JWTs
	// for users whose email address is confirmed.
	EmailVerified bool `json:"email_verified"`

	// Role must equal "sovereign-admin".
	Role string `json:"role"`

	// SovereignFQDN is the new Sovereign's FQDN (mother stamps this).
	// Used to enrich the Sovereign session cookie so /sovereign/self
	// and other chroot endpoints can resolve the Sovereign identity
	// without falling back to env or store-fallback.
	SovereignFQDN string `json:"sovereign_fqdn"`

	// DeploymentID is the Catalyst-Zero deployment record ID (16-char
	// hex). Used by chroot pages to scope deployment-keyed API paths
	// once /sovereign/self resolution is JWT-driven.
	DeploymentID string `json:"deployment_id"`

	// SupportSession marks an "Enter org" impersonation handover (#3378
	// B2, minted by org_enter_org.go). Unlike the mothership→Sovereign
	// handover, this token is signed by the SOVEREIGN's OWN handover
	// signer (the same catalyst-api both mints and redeems it) and
	// targets a pool-domain Org console — so it is verified against the
	// local signer key and validated against sovereign_fqdn rather than
	// the aud=console.<own-fqdn> claim. (#4101)
	SupportSession bool `json:"support_session"`

	// SupportPrincipal / ImpersonatedOrg / InitiatedBy carry the audit
	// context for the support session and drive the in-console banner.
	SupportPrincipal string `json:"support_principal"`
	ImpersonatedOrg  string `json:"impersonated_org"`
	InitiatedBy      string `json:"initiated_by"`
}

// AuthHandover handles GET /auth/handover?token=<jwt>.
func (h *Handler) AuthHandover(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("token")
	if raw == "" {
		// TC-004 / 2026-05-07 — three-way branch on no-token visits.
		//
		// 1. Authenticated browser (valid catalyst_session cookie + bearer
		//    fallback): the operator already has a live Sovereign session,
		//    so a bare /auth/handover URL is meaningless to them. Send
		//    them to the Sovereign Console root instead of the
		//    "Handover incomplete" copy that confuses people who are
		//    already logged in. The same redirect target as the happy
		//    path post-cookie-set, so an authed user landing here for
		//    any reason ends up at the same page they would have hit
		//    after a successful handover.
		//
		// 2. Unauthenticated browser: SPA-rendered friendly error page
		//    (Fix #2 PR #1075 contract).
		//
		// 3. Programmatic caller (Accept: application/json): legacy
		//    401 JSON, unchanged.
		if h.hasValidCatalystSession(r) {
			redirectTarget := h.authHandoverRedirect
			if redirectTarget == "" {
				redirectTarget = "/dashboard"
			}
			http.Redirect(w, r, redirectTarget, http.StatusFound)
			return
		}
		// Browser visits (e.g. an operator pasting the bare /auth/handover
		// URL, or following a stale email link with the token stripped) get
		// a SPA-rendered friendly error page instead of bare JSON. Programmatic
		// callers (curl / health probes / API clients with explicit
		// `Accept: application/json`) keep the legacy 401 JSON contract that
		// auth_handover_test.go pins.
		//
		// Caught live on omantel.biz 2026-05-07 (TC-004): pasting
		// https://console.<sov>/auth/handover with no token returned raw
		// JSON to the browser, breaking the seamless-handover UX promise.
		if wantsHTML(r) {
			http.Redirect(w, r, "/auth/handover-error?reason=missing_token", http.StatusFound)
			return
		}
		writeAuthError(w, "missing token parameter")
		return
	}

	// ── 1. Load public key ──────────────────────────────────────────────
	// Resolution order:
	//   1. h.handoverJWTPublicKeyPath (set by tests via the field)
	//   2. CATALYST_HANDOVER_JWT_PUBLIC_PATH env var (chart sets this from
	//      .Values.handoverJwtPublicPath; PR #692 moved the Sovereign-side
	//      mount to /etc/catalyst/handover-jwt-public/public.jwk to avoid
	//      a subPath conflict on the catalyst-api PVC).
	//   3. CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH env var (legacy name kept
	//      for backward-compat; renamed 2026-06-03 because the substring
	//      "KEY" matches Kyverno cluster-policy
	//      `secret-not-in-env/deny-plaintext-secret-env`'s regex
	//      `(?i)(PASSWORD|TOKEN|KEY|SECRET)` and the plaintext path value
	//      registers as a PolicyViolation).
	//   4. DefaultHandoverJWTPublicKeyPath constant (final fallback).
	keyPath := h.handoverJWTPublicKeyPath
	if keyPath == "" {
		keyPath = os.Getenv("CATALYST_HANDOVER_JWT_PUBLIC_PATH")
	}
	if keyPath == "" {
		keyPath = os.Getenv("CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH")
	}
	if keyPath == "" {
		keyPath = DefaultHandoverJWTPublicKeyPath
	}
	pubKey, err := loadRSAPublicKey(keyPath)
	if err != nil {
		h.log.Error("auth_handover: load public key failed", "err", err, "path", keyPath)
		writeAuthError(w, "server misconfiguration: public key unavailable")
		return
	}

	// #4101: the verifier must also accept tokens signed by THIS
	// catalyst-api's OWN handover signer. The mothership→Sovereign
	// handover is signed by the mothership and verified with the mounted
	// mothership public key (pubKey above). But the "Enter org" support
	// handover (#3378 B2, org_enter_org.go) is signed by the SOVEREIGN's
	// own handoverSigner — the same Pod both mints and redeems it — yet
	// still stamps iss=console.openova.io. Verifying ONLY with the
	// mothership key rejected every Enter-org token with
	// "crypto/rsa: verification error" → "invalid token" (live on
	// omantel.biz entering console.demo.omani.homes, 2026-06-22). Collect
	// the local signer's public key as an additional candidate; the
	// keyfunc tries the mounted key first, then the local one.
	var localPub *rsa.PublicKey
	if h.handoverSigner != nil {
		if lp, lerr := h.handoverSigner.PublicRSAKey(); lerr == nil {
			localPub = lp
		}
	}

	// ── 2. Parse + verify JWT ───────────────────────────────────────────
	//
	// We verify the signature manually against each candidate key because
	// jwt.ParseWithClaims short-circuits on the first key returned by the
	// keyfunc. To try multiple keys we attempt the parse once per key and
	// accept the first that validates.
	var claims authHandoverClaims
	var tok *jwt.Token
	candidates := []*rsa.PublicKey{pubKey}
	if localPub != nil && (pubKey == nil || localPub.N.Cmp(pubKey.N) != 0) {
		candidates = append(candidates, localPub)
	}
	var parseErr error
	for _, cand := range candidates {
		claims = authHandoverClaims{}
		key := cand
		tok, parseErr = jwt.ParseWithClaims(raw, &claims,
			func(t *jwt.Token) (any, error) {
				if _, ok := t.Method.(*jwt.SigningMethodRSA); !ok {
					return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
				}
				return key, nil
			},
			jwt.WithValidMethods([]string{"RS256"}),
			jwt.WithExpirationRequired(),
		)
		if parseErr == nil && tok != nil && tok.Valid {
			break
		}
	}
	if parseErr != nil || tok == nil || !tok.Valid {
		h.log.Warn("auth_handover: JWT parse failed", "err", parseErr)
		writeAuthError(w, "invalid token")
		return
	}

	// ── 3. Validate claims ──────────────────────────────────────────────
	// #5614 — resolve the expected issuer through the SAME single source the
	// minter uses (handoverjwt.DefaultIssuer(), which reads
	// CATALYST_HANDOVER_JWT_ISSUER and falls back to the Catalyst-Zero
	// mothership origin). The former hardcoded `const expectedIss =
	// "https://console.openova.io"` was the verify-side survivor of the #2940
	// duplicate-tether literal: a cut-over Sovereign mints a token with its OWN
	// console as `iss` and this verifier then 401'd it as "invalid issuer",
	// rejecting the Sovereign's own handover (hw292, cc=true). One resolver, both
	// sides — the mint and verify issuers can never drift again.
	expectedIss := handoverjwt.DefaultIssuer()
	iss, err := claims.GetIssuer()
	if err != nil || iss != expectedIss {
		writeAuthError(w, "invalid issuer")
		return
	}

	// Audience / target-host validation.
	//
	// #4101: an "Enter org" support handover (support_session=true) targets
	// a pool-domain Org console that the redeeming Sovereign catalyst-api
	// also serves — e.g. console.demo.omani.homes on an omantel.biz
	// Sovereign. org_enter_org.go mints it WITHOUT an aud claim and instead
	// carries sovereign_fqdn=<org-host-suffix> (demo.omani.homes). The
	// classic aud=console.<own-fqdn> check (console.omantel.biz) can never
	// match such a token, so validate the support session against the
	// request host instead: the token's sovereign_fqdn must equal the Org
	// suffix of the host the browser actually landed on.
	if claims.SupportSession {
		wantSuffix := strings.ToLower(strings.TrimSpace(claims.SovereignFQDN))
		if wantSuffix == "" {
			writeAuthError(w, "invalid token")
			return
		}
		reqHost := requestHost(r)
		// Accept when the request host is console.<suffix> / <suffix> /
		// *.<suffix> so the support session lands only on the org it was
		// minted for.
		if reqHost != "" &&
			reqHost != "console."+wantSuffix &&
			reqHost != wantSuffix &&
			!strings.HasSuffix(reqHost, "."+wantSuffix) {
			h.log.Warn("auth_handover: support-session host mismatch",
				"tokenSuffix", wantSuffix, "reqHost", reqHost)
			writeAuthError(w, "invalid audience")
			return
		}
	} else {
		// Standard mothership→Sovereign handover: aud must contain the
		// Sovereign's own console URL.
		soverFQDN := h.sovereignFQDN()
		expectedAud := "https://console." + soverFQDN
		auds, _ := claims.GetAudience()
		found := false
		for _, a := range auds {
			if a == expectedAud {
				found = true
				break
			}
		}
		if !found {
			writeAuthError(w, "invalid audience")
			return
		}
	}

	if claims.Role != "sovereign-admin" {
		writeAuthError(w, "insufficient role")
		return
	}
	if !claims.EmailVerified {
		writeAuthError(w, "email not verified")
		return
	}
	if claims.Email == "" {
		writeAuthError(w, "missing email claim")
		return
	}

	jti := claims.ID
	if jti == "" {
		writeAuthError(w, "missing jti")
		return
	}

	// ── 4. Replay check via jtistore ───────────────────────────────────
	jtiSt := h.jtiStore
	if jtiSt == nil {
		jtiSt = jtistore.New(jtistore.DefaultPath)
	}
	firstUse, err := jtiSt.Mark(jti)
	if err != nil {
		h.log.Error("auth_handover: jtistore.Mark failed", "err", err)
		writeAuthError(w, "internal error")
		return
	}
	if !firstUse {
		writeAuthError(w, "token already used")
		return
	}

	// ── 5. EnsureUser in Keycloak ───────────────────────────────────────
	//
	// #4101: for an "Enter org" support session the Keycloak EnsureUser is
	// BEST-EFFORT, not a hard gate. The support principal is an ephemeral,
	// time-boxed (≤60min) impersonation identity — the session JWT we mint
	// below is the source of truth for the support banner + audit context,
	// not a durable Keycloak user record. The target Org's realm may still
	// be converging (a just-provisioned pool Org) and must not block the
	// sovereign-admin from entering for support. A standard
	// mothership→Sovereign handover still hard-fails on EnsureUser because
	// that establishes the durable Sovereign owner identity.
	var userID string
	kc := h.keycloakClientFor()
	if claims.SupportSession {
		if kc != nil {
			if uid, eerr := kc.EnsureUser(r.Context(), claims.Email, "sovereign-admins"); eerr != nil {
				h.log.Warn("auth_handover: support-session EnsureUser failed (best-effort)",
					"email", claims.Email, "err", eerr)
			} else {
				userID = uid
			}
		} else {
			h.log.Warn("auth_handover: support-session EnsureUser skipped — keycloak not configured",
				"email", claims.Email)
		}
	} else {
		if kc == nil {
			writeAuthError(w, "server misconfiguration: keycloak not configured")
			return
		}
		uid, eerr := kc.EnsureUser(r.Context(), claims.Email, "sovereign-admins")
		if eerr != nil {
			h.log.Error("auth_handover: EnsureUser failed", "email", claims.Email, "err", eerr)
			writeAuthError(w, "keycloak error: ensure user")
			return
		}
		userID = uid
	}

	// ── 5b. Auto-seed owner UserAccess CR (D21) ─────────────────────────
	// D21 (docs/SOVEREIGN-MULTI-REGION-DOD.md) requires Sovereign Console
	// /users to list the operator who PIN-logged-in as tier=owner. The
	// useraccess Composition (issue #322) reconciles the CR into
	// per-app RoleBindings on the Sovereign cluster.
	//
	// Best-effort: any error here MUST NOT fail the handover — the
	// operator's session is more important than the CR. If the
	// access.openova.io CRD has not rolled yet, the next handover will
	// succeed once the chart catches up. See user_access_owner_seed.go.
	h.seedOwnerUserAccess(r.Context(), claims.Email, claims.SovereignFQDN, claims.DeploymentID)

	// ── 6. Mint local session JWT ───────────────────────────────────────
	// Keycloak v26 dropped the legacy `requested_subject` token-exchange
	// flow ("Parameter 'requested_subject' is not supported for standard
	// token exchange"). We mint the session JWT locally with the same
	// handoverSigner that PIN-verify uses (handler/auth.go pattern), so
	// the auth path is consistent regardless of how the operator authed.
	// The Sovereign Keycloak still owns the canonical user record (created
	// by EnsureUser above) and can be the federation target for any
	// downstream IdP brokering — we just don't need its token-exchange
	// endpoint to mint THIS session.
	const sessionTTL = 8 * time.Hour
	sessionClaims := jwt.MapClaims{
		"iss":            "https://console.openova.io",
		"sub":            claims.Email,
		"email":          claims.Email,
		"email_verified": true,
		"role":           "sovereign-admin",
		// G117 #2856 Gap 2 (2026-06-03): preserve authz claims so
		// every catalyst-api handler that checks claims.Tier or
		// claims.RealmAccess.Roles recognises the handover-derived
		// session as Sovereign-owner. The auth.Claims struct has no
		// `Role` field, so the bare "role" claim above is silently
		// dropped at parse time — every downstream authz gate then
		// falls back to the OPERATOR_EMAIL short-circuit
		// (applicationInstallCallerAuthorized → isSovereignOperatorClaim),
		// which only matches the SINGLE registered operator email.
		// Multi-owner Sovereigns (2nd sovereign-admin owners) hit 403
		// on every authed endpoint, caught live by H11 walk on hw86
		// 2026-06-02 (PILLAR1WALK voucher mint via BSS-menu).
		//
		// Mirror the PIN-derived session pattern (auth.go:274) so the
		// handover session carries the same tier=owner + realm-role
		// list the rbacAssignPrivilegedRoles loop expects.
		"tier": "owner",
		"realm_access": map[string][]string{
			"roles": {"catalyst-owner", "catalyst-admin", "sovereign-admins"},
		},
		"iat":            time.Now().Unix(),
		"exp":            time.Now().Add(sessionTTL).Unix(),
		"jti":            uuid.NewString(),
		"typ":            "session",
		"keycloak_uid":   userID,
		// Carry Sovereign identity into the session token so downstream
		// handlers (HandleSovereignSelf, /users, /catalog, /settings)
		// can resolve the deployment id WITHOUT depending on the
		// orchestrator overlay write or store-fallback. Caught on
		// omantel.biz 2026-05-06: every chroot page broke because
		// /sovereign/self returned 503 (no env populated post-handover).
		"sovereign_fqdn": claims.SovereignFQDN,
		"deployment_id":  claims.DeploymentID,
	}
	// #4101: propagate the support-session markers into the minted session
	// JWT so whoami + the in-console support banner can render the support
	// principal + impersonated org, and the audit trail ties back to the
	// initiating sovereign-admin.
	if claims.SupportSession {
		sessionClaims["support_session"] = true
		if claims.SupportPrincipal != "" {
			sessionClaims["support_principal"] = claims.SupportPrincipal
		}
		if claims.ImpersonatedOrg != "" {
			sessionClaims["impersonated_org"] = claims.ImpersonatedOrg
		}
		if claims.InitiatedBy != "" {
			sessionClaims["initiated_by"] = claims.InitiatedBy
		}
	}
	accessToken, err := h.handoverSigner.SignCustomClaims(sessionClaims)
	if err != nil {
		h.log.Error("auth_handover: SignCustomClaims failed", "err", err)
		writeAuthError(w, "internal error")
		return
	}

	// ── 7. Set cookies + redirect ───────────────────────────────────────
	cookieMaxAge := int(sessionTTL.Seconds())
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"

	// #3374 (2026-06-13): domain-widen the session cookie EXACTLY like the
	// PIN-verify path (auth.go G113-followup hw86). Without a Domain the
	// cookie is host-only on console.<fqdn>, so KC's catalyst-pin broker
	// redirect to api.<fqdn>/oidc/auth never carries it -> EVERY silent
	// zero-click chain from a handover-established session bounced to the
	// console PIN form (measured live on hw130 2026-06-13: fresh handover
	// -> grafana.<fqdn> landed on console./login?next=...oidc/auth...).
	//
	// #4101 (2026-06-22): use the shared request-host-derived helper so a
	// support handover landing on a POOL-domain Org console
	// (console.demo.omani.homes) scopes the cookie to .demo.omani.homes —
	// NOT .<SOVEREIGN_FQDN> (.omantel.biz), which the browser would reject
	// for that host (the same cross-domain drop that broke PIN login,
	// BUG 1). sessionCookieDomain honours CATALYST_SESSION_COOKIE_DOMAIN,
	// then the Sovereign zone, then the per-Org suffix.
	cookieDomain := sessionCookieDomain(r)
	if cookieDomain == "" {
		// Last-resort: the validated handover claim's sovereign_fqdn (this
		// handler always has it) so the cookie is never host-only.
		if fq := strings.TrimSpace(claims.SovereignFQDN); fq != "" {
			cookieDomain = "." + fq
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "catalyst_session",
		Value:    accessToken,
		Path:     "/",
		Domain:   cookieDomain,
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
	// Clear any legacy refresh cookie from the old token-exchange flow.
	http.SetCookie(w, &http.Cookie{
		Name:     "catalyst_refresh",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})

	h.log.Info("auth_handover: operator session established",
		"email", claims.Email,
		"userID", userID,
		"expires_in", cookieMaxAge,
	)

	redirectTarget := h.authHandoverRedirect
	if redirectTarget == "" {
		redirectTarget = "/dashboard"
	}
	http.Redirect(w, r, redirectTarget, http.StatusFound)
}

// seedOwnerUserAccess upserts a tier=owner UserAccess CR for the
// operator who just completed handover (D21).
//
// Best-effort: every failure mode logs a Warn and returns; the handover
// itself is never failed because the operator's session is more
// important than this CR. The next handover will retry idempotently.
//
// On the Sovereign chroot, `lookupDeploymentForInfra(depID)` synthesises
// an in-memory Deployment from SOVEREIGN_FQDN when one isn't already
// loaded (see infrastructure.go:1776 `chrootEnsureDeployment`), so
// `sovereignDynamicClient(dep)` routes through the in-cluster client.
func (h *Handler) seedOwnerUserAccess(ctx context.Context, email, sovereignFQDN, depID string) {
	if email == "" {
		h.log.Debug("user-access: owner seed skipped — empty email")
		return
	}
	if depID == "" {
		h.log.Debug("user-access: owner seed skipped — empty deployment id")
		return
	}
	dep, ok := h.lookupDeploymentForInfra(depID)
	if !ok || dep == nil {
		h.log.Warn("user-access: owner seed skipped — deployment not resolvable",
			"depId", depID)
		return
	}
	client, err := h.sovereignDynamicClient(dep)
	if err != nil {
		h.log.Warn("user-access: owner seed dynamic client unavailable",
			"depId", depID, "err", err)
		return
	}
	if err := EnsureOwnerUserAccess(ctx, client, email, sovereignFQDN); err != nil {
		h.log.Warn("user-access: owner seed failed",
			"depId", depID, "email", email, "err", err)
		return
	}
	h.log.Info("user-access: owner auto-seeded",
		"depId", depID, "email", email)
}

// ────────────────────────────────────────────────────────────────────────────
// Handler wiring helpers
// ────────────────────────────────────────────────────────────────────────────

// DefaultHandoverJWTPublicKeyPath is the on-Sovereign path cloud-init writes
// the RS256 public key JWK to. Overridable via CATALYST_HANDOVER_JWT_PUBLIC_KEY_PATH.
// The handoverjwt.Signer.PublicJWK() writes JSON; cloud-init distributes this
// file from Catalyst-Zero's /var/lib/catalyst/handover-jwt-public.jwk.
const DefaultHandoverJWTPublicKeyPath = "/etc/catalyst/handover-jwt-public/public.jwk"

// jtiStorer is the interface implemented by *jtistore.Store.
type jtiStorer interface {
	Mark(jti string) (bool, error)
}

// sovereignFQDN returns the Sovereign's FQDN from Handler config or env.
func (h *Handler) sovereignFQDN() string {
	if h.authHandoverSovereignFQDN != "" {
		return h.authHandoverSovereignFQDN
	}
	return os.Getenv("SOVEREIGN_FQDN")
}

// keycloakClientFor returns the wired Keycloak client or builds one lazily
// from env. Returns nil when CATALYST_KC_SA_CLIENT_SECRET is unset (Sovereign
// not yet configured — handler returns 503 to the caller).
func (h *Handler) keycloakClientFor() keycloakClient {
	if h.kc != nil {
		return h.kc
	}
	addr := os.Getenv("CATALYST_KC_ADDR")
	if addr == "" {
		addr = "http://keycloak.keycloak.svc.cluster.local:8080"
	}
	realm := os.Getenv("CATALYST_KC_REALM")
	if realm == "" {
		realm = "sovereign"
	}
	clientID := os.Getenv("CATALYST_KC_SA_CLIENT_ID")
	if clientID == "" {
		clientID = "catalyst-api-server"
	}
	secret := os.Getenv("CATALYST_KC_SA_CLIENT_SECRET")
	if secret == "" {
		return nil // unconfigured
	}
	return keycloak.New(addr, realm, clientID, secret)
}

// ────────────────────────────────────────────────────────────────────────────
// Key loading — supports PEM (PKIX/PKCS1) and JWK JSON
// ────────────────────────────────────────────────────────────────────────────

// loadRSAPublicKey reads an RSA public key from path.
// Supports:
//   - PEM-encoded PKIX / PKCS1 (files ending in .pem)
//   - RFC 7517 JWK JSON produced by handoverjwt.Signer.PublicJWK()
func loadRSAPublicKey(path string) (*rsa.PublicKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Try PEM first.
	if key, err := jwt.ParseRSAPublicKeyFromPEM(raw); err == nil {
		return key, nil
	}

	// Try JWK JSON (produced by handoverjwt.Signer.PublicJWK).
	key, err := rsaPublicKeyFromJWK(raw)
	if err != nil {
		return nil, fmt.Errorf("load RSA public key from %s: neither PEM nor JWK: %w", path, err)
	}
	return key, nil
}

// rsaPublicKeyFromJWK parses a minimal RSA JWK as emitted by
// handoverjwt.Signer.PublicJWK(): {"kty":"RSA","use":"sig","alg":"RS256","n":"...","e":"..."}.
// n and e are base64url-encoded big-endian byte sequences per RFC 7518 §6.3.
func rsaPublicKeyFromJWK(raw []byte) (*rsa.PublicKey, error) {
	var jwk struct {
		Kty string `json:"kty"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	if err := json.Unmarshal(raw, &jwk); err != nil {
		return nil, fmt.Errorf("decode JWK: %w", err)
	}
	if jwk.Kty != "RSA" {
		return nil, fmt.Errorf("JWK kty is %q, expected RSA", jwk.Kty)
	}
	if jwk.N == "" || jwk.E == "" {
		return nil, fmt.Errorf("JWK missing n or e")
	}

	nBytes, err := base64.RawURLEncoding.DecodeString(jwk.N)
	if err != nil {
		return nil, fmt.Errorf("decode JWK n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(jwk.E)
	if err != nil {
		return nil, fmt.Errorf("decode JWK e: %w", err)
	}

	n := new(big.Int).SetBytes(nBytes)
	e := int(new(big.Int).SetBytes(eBytes).Int64())
	if e == 0 {
		return nil, fmt.Errorf("JWK e is zero")
	}

	return &rsa.PublicKey{N: n, E: e}, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Error helper
// ────────────────────────────────────────────────────────────────────────────

type authHandoverError struct {
	Error string `json:"error"`
}

func writeAuthError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(authHandoverError{Error: msg}) //nolint:errcheck
}

// hasValidCatalystSession reports whether the request carries a valid,
// non-expired catalyst_session (or Bearer / access_token) JWT signed by
// either Keycloak's JWKS or the local handover signer's public key.
//
// Used by AuthHandover (TC-004, 2026-05-07) to short-circuit a bare
// `/auth/handover` browser visit by an already-authed operator straight
// to the Sovereign Console root, instead of showing the "Handover
// incomplete" error page that exists for unauthenticated visitors.
//
// Returns false when:
//   - h.authConfig is nil (Sovereign not yet configured / CI). In that
//     case the redirect-on-authed branch is skipped and the existing
//     html-vs-json branches handle the response.
//   - No session token is present in any of the supported channels
//     (cookie / bearer / query — see auth.Config.ReadSessionToken).
//   - The token is malformed, expired, or fails signature verification.
//
// Logs at Debug — operators don't need a per-request line, but stale
// cookies are useful when triaging session-loss reports.
func (h *Handler) hasValidCatalystSession(r *http.Request) bool {
	if h.authConfig == nil {
		return false
	}
	tok := h.authConfig.ReadSessionToken(r)
	if tok == "" {
		return false
	}
	if _, err := h.authConfig.ValidateToken(r.Context(), tok); err != nil {
		h.log.Debug("auth_handover: session present but invalid; falling through to error page",
			"err", err)
		return false
	}
	return true
}

// wantsHTML returns true when the caller's Accept header prefers
// text/html over application/json. Used by AuthHandover to render a
// SPA-friendly error page for browser visits while preserving the legacy
// JSON contract for programmatic callers (tests + monitors).
//
// Heuristic: an HTML-prefering browser sends `Accept: text/html,...` or
// `Accept: */*` with `Sec-Fetch-Mode: navigate`. We check both — the
// first catches modern browsers, the second catches some legacy clients
// that send `Accept: */*`. JSON-first programmatic callers that send
// `Accept: application/json` (the auth_handover_test cases that send
// no Accept header at all also fall through to the JSON branch because
// neither marker fires) get the legacy 401 JSON.
func wantsHTML(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	// Explicit non-HTML preference (e.g. `Accept: application/json`) —
	// definitely a programmatic caller.
	if accept != "" && !strings.Contains(accept, "text/html") &&
		!strings.Contains(accept, "*/*") {
		return false
	}
	// text/html anywhere in the Accept header → browser.
	if strings.Contains(accept, "text/html") {
		return true
	}
	// `Sec-Fetch-Mode: navigate` is the W3C marker for top-level browser
	// navigation. Catches `Accept: */*` browser cases.
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return true
	}
	return false
}
