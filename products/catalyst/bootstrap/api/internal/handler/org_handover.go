// org_handover.go — GET /auth/org-handover?token=<member-session-jwt>
//
// The SECURE marketplace→console session handoff (issue #4182 + #4186).
//
// THE BUG this file closes (live on omantel.biz dep 4635277cae4ffed9,
// 2026-06-22/23):
//
//   - The marketplace post-checkout redirect went straight to
//     `https://console.<slug>.<pool>/jobs?token=<JWT>&refresh_token=<uuid>`.
//     The bearer + refresh sat in the URL query → leaked via browser
//     history, proxy/CDN access logs, and the Referer header on every
//     subsequent asset/XHR (#4182).
//   - Worse, NOTHING redeemed that token into a session cookie. The
//     `/jobs` landing is the catalyst-ui Sovereign-console SPA, which
//     authenticates with the HttpOnly RS256 `catalyst_session` cookie via
//     /api/v1/whoami. With no cookie set, whoami 401'd and the SPA bounced
//     /jobs → /login (#4186).
//
// Why the existing /auth/handover (auth_handover.go) could NOT be reused:
// that endpoint validates an RS256 sovereign-admin handover JWT minted by
// the mothership. The marketplace member session is an HS256 token signed
// with the Organization mesh's `org-services-secrets/JWT_SECRET` (the same secret
// catalyst-api already mints bridge tokens with — h.orgJWTSecret). The two
// are different signers and different identities (sovereign-admin vs Org
// member). auth.ValidateToken even hard-rejects any non-RS256 alg, so a
// member token can never be a valid catalyst_session on its own.
//
// THE FIX (root cause, permanent): this handler accepts the member token,
// validates it against h.orgJWTSecret, resolves the Org scope from the
// REQUEST HOST (the trust anchor — the Cilium Gateway routes the Org
// console host here and sets X-Forwarded-Host, which a browser on the Org
// console can never forge), then mints an RS256 Org-scoped catalyst_session
// — byte-for-byte the same shape HandlePinVerify mints for a customer who
// PIN-logs-in on their Org console (auth.go #4110 Org-scoping). It sets the
// HttpOnly Secure SameSite=Lax cookie scoped to the request host, stamps
// `Referrer-Policy: no-referrer` so the (now token-free) URL never leaks,
// and 302s to a clean /jobs.
//
// Result: the marketplace redirects to /auth/org-handover?token=<member>
// (NO refresh_token in the URL — it stays in client storage via
// setAuthTokens), the browser lands signed-in on a clean /jobs, the cookie
// is established, and no credential is ever in the address bar after the
// hop. The single remaining `?token=` is burned into a cookie immediately
// and the redirect lands on a clean path; future hardening can swap it for
// a one-time code without a contract change (the marketplace only knows the
// /auth/org-handover URL).
package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// orgHandoverClaims is the subset of the marketplace member-session token
// (HS256, signed with org-services-secrets/JWT_SECRET) this handler relies on. The
// marketplace `auth` service stamps {sub, email, role:member, typ:session,
// iat, exp}; we only need sub/email + standard registered claims.
type orgHandoverClaims struct {
	jwt.RegisteredClaims
	Email string `json:"email"`
	Role  string `json:"role"`
	Typ   string `json:"typ"`
}

// AuthOrgHandover handles GET /auth/org-handover?token=<member-jwt>.
//
// It MUST live OUTSIDE the RequireSession middleware group (the token IS
// the auth — there is no catalyst_session cookie yet; this handler MINTS
// one), exactly like AuthHandover.
func (h *Handler) AuthOrgHandover(w http.ResponseWriter, r *http.Request) {
	// Never leak the (token-bearing) URL to downstream assets, history-
	// scraping extensions, or third-party Referer sinks. Stamped before any
	// early return so even the error paths are no-referrer.
	w.Header().Set("Referrer-Policy", "no-referrer")

	raw := strings.TrimSpace(r.URL.Query().Get("token"))
	if raw == "" {
		// A bare visit by an already-signed-in browser is benign — bounce
		// to the dashboard rather than an error. Otherwise send the SPA
		// (it will probe whoami and redirect to /login if truly signed-out).
		if h.hasValidCatalystSession(r) {
			http.Redirect(w, r, "/jobs", http.StatusFound)
			return
		}
		writeAuthError(w, "missing token parameter")
		return
	}

	// ── 1. The Org JWT secret must be wired ─────────────────────────────
	// CATALYST_ORG_JWT_SECRET is fed from the reflector-mirrored
	// org-services-secrets/JWT_SECRET (SetOrgJWTSecret in main.go). Without it this
	// catalyst-api cannot validate a member token — surface a terse 401
	// (no secret material in the body, per INVIOLABLE-PRINCIPLES #10).
	if len(h.orgJWTSecret) == 0 {
		h.log.Error("auth_org_handover: CATALYST_ORG_JWT_SECRET unset — cannot validate member token")
		writeAuthError(w, "server misconfiguration: org session secret unavailable")
		return
	}

	// ── 2. Resolve Org scope from the REQUEST HOST (trust anchor) ───────
	// The session minted here is ALWAYS Org-scoped (tier=org-admin) and is
	// bound to the Org whose console host the browser walked through. A
	// handoff that lands on the Sovereign's own front door
	// (console.<sov-fqdn>) — or any host the tenant registry does not know
	// as an Org console — is REFUSED: we never mint an Org session on a
	// non-Org host, and we never escalate a member token to a
	// sovereign-admin session.
	scope, ok := h.resolveOrgScope(r)
	if !ok {
		// On-demand registry sync (the #3376 terminal-step race): a fresh
		// MARKETPLACE funnel Org is registered only by the periodic 60s
		// ReconcileTenantRegistryFromOrgCRs loop — the funnel door never runs
		// the BSS pipeline that writes the row synchronously. A stranger who
		// completes checkout is redirected here IMMEDIATELY, so the row may not
		// exist yet. Resolve the Org CR for THIS exact request host and register
		// it now, then retry the scope resolution once. If the host still has no
		// matching Org CR (or we're out-of-cluster), we refuse exactly as before
		// — no regression for a genuinely-unknown host.
		if h.ensureTenantRegisteredForHost(r.Context(), requestHost(r)) {
			scope, ok = h.resolveOrgScope(r)
		}
	}
	if !ok {
		h.log.Warn("auth_org_handover: refused — request host is not a registered Org console",
			"host", requestHost(r))
		writeAuthError(w, "invalid audience: not an org console host")
		return
	}

	// ── 3. Validate the member token (HS256, org-services-secrets/JWT_SECRET) ────
	var claims orgHandoverClaims
	tok, perr := jwt.ParseWithClaims(raw, &claims,
		func(t *jwt.Token) (any, error) {
			if _, methodOK := t.Method.(*jwt.SigningMethodHMAC); !methodOK {
				return nil, jwt.ErrTokenSignatureInvalid
			}
			return h.orgJWTSecret, nil
		},
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithExpirationRequired(),
	)
	if perr != nil || tok == nil || !tok.Valid {
		h.log.Warn("auth_org_handover: member token invalid", "err", perr)
		writeAuthError(w, "invalid token")
		return
	}

	email := strings.TrimSpace(claims.Email)
	if email == "" {
		// Some marketplace tokens carry the email only in `sub`. Fall back
		// to the subject when it looks like an address.
		if sub := strings.TrimSpace(claims.Subject); strings.Contains(sub, "@") {
			email = sub
		}
	}
	if email == "" {
		writeAuthError(w, "missing email claim")
		return
	}

	// ── 4. Mint an RS256 Org-scoped catalyst_session ────────────────────
	// Byte-for-byte the shape HandlePinVerify mints for an Org-scoped PIN
	// session (auth.go #4110): tier=org-admin, realm_access.roles=[org-admin],
	// the org slug in BOTH wire tags (org + org_id), typ=session. The
	// OrgScopeGuard middleware then confines this session to its own Org's
	// surfaces (deny-by-default outside the Org-safe allowlist).
	if h.handoverSigner == nil {
		h.log.Error("auth_org_handover: handover signer unavailable (CATALYST_HANDOVER_SIGNER_PATH not set)")
		writeAuthError(w, "server misconfiguration: session signer unavailable")
		return
	}
	now := time.Now()
	sessionClaims := jwt.MapClaims{
		"iss":            pinIssuer,
		"sub":            email,
		"email":          email,
		"email_verified": true,
		"role":           pinSessionRole,
		"tier":           orgScopedTier,
		"realm_access": map[string]any{
			"roles": []string{orgScopedRealmRole},
		},
		"iat":    now.Unix(),
		"exp":    now.Add(pinSessionTTL).Unix(),
		"jti":    uuid.NewString(),
		"typ":    "session",
		"org":    scope.Org,
		"org_id": scope.Org,
	}
	accessToken, serr := h.handoverSigner.SignCustomClaims(sessionClaims)
	if serr != nil {
		h.log.Error("auth_org_handover: SignCustomClaims failed", "err", serr)
		writeAuthError(w, "internal error")
		return
	}

	// ── 5. Set the cookie + redirect to a clean /jobs ───────────────────
	cookieMaxAge := int(pinSessionTTL.Seconds())
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	// Derive the Domain from the REQUEST HOST (#4101) so the cookie is
	// accepted by the Org pool-domain console (console.<slug>.<pool>) and
	// covers console./api. of the same Org family — never the unrelated
	// Sovereign zone (which the browser would reject for this host).
	cookieDomain := sessionCookieDomain(r)

	// Drop any stale Set-Cookie an upstream middleware may have appended so
	// the freshly minted JWT is the ONLY catalyst_session the browser sees
	// (same defence as HandlePinVerify, TBD-F7/#1730).
	w.Header().Del("Set-Cookie")
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

	h.log.Info("auth_org_handover: org session established",
		"email", email, "org", scope.Org, "host", scope.Host)

	http.Redirect(w, r, "/jobs", http.StatusFound)
}
