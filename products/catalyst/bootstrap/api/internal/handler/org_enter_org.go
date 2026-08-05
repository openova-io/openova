// org_enter_org.go — the B2 "Enter org" support session (issue #3378
// §6 B2 + DoD 6).
//
// One endpoint mints the audited, time-boxed (≤60min) impersonation into
// a sub-organization's OWN console as a SUPPORT PRINCIPAL in the org's
// realm (never the owner's identity). This ABSORBS what OSS's SEE+SUPPORT
// would have been (§2.4): the org's own limited-similar console IS the
// view; there is no bespoke parent-side estate read.
//
// Recursion (§2.5): the SAME handover primitive that signs the operator
// from mothership into a child Sovereign (auth_handover.go) signs the
// sovereign-admin one level further down, into a sub-org's console. The
// minted token reuses the EXISTING handover-redemption flow — the org's
// catalyst-api redeems it at /auth/handover exactly as a Sovereign
// redeems the mothership's token — and inherits the #3374/#3385 handover
// cookie-domain fix (now on main) without duplicating it.
//
// Route (registered in cmd/api/main.go), session-gated + sovereign-admin:
//
//   POST /api/v1/org/organizations/{slug}/enter
//     → { "handoverURL": "...", "supportPrincipal": "...",
//         "expiresAt": "...", "auditEventId": "..." }
//
// The caller (the Enter-org button) opens handoverURL in a new tab; the
// org's console redeems it and lands the support principal logged in with
// the in-console support banner. The audit event (who/when/org/duration)
// is written before the URL is returned. The button is HIDDEN on the
// parent row (§5: "you are already inside it") — the handler also rejects
// an attempt to enter the parent as a guard.

package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	auth "github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/handoverjwt"
)

// enterOrgMaxTTL caps the support session at 60 minutes (§6 B2: "TTL ≤
// 60min"). The session is deliberately short — support, not a standing
// login.
const enterOrgMaxTTL = 60 * time.Minute

// EnterOrgResponse is the wire shape the Enter-org button consumes.
type EnterOrgResponse struct {
	// HandoverURL — the org-console URL the button opens; redeeming it
	// lands the support principal logged into the org's console.
	HandoverURL string `json:"handoverURL"`
	// SupportPrincipal — the dedicated support identity in the org's
	// realm (NEVER the owner's identity, §2.4).
	SupportPrincipal string `json:"supportPrincipal"`
	// Org — the entered org slug.
	Org string `json:"org"`
	// ExpiresAt — the session expiry (≤60min from now).
	ExpiresAt time.Time `json:"expiresAt"`
	// AuditEventID — the id of the audit event written for this entry.
	AuditEventID string `json:"auditEventId"`
}

// HandleEnterOrg — POST /api/v1/org/organizations/{slug}/enter.
func (h *Handler) HandleEnterOrg(w http.ResponseWriter, r *http.Request) {
	slug := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "slug")))
	if slug == "" {
		writeBadRequest(w, "missing-slug", "org slug is required")
		return
	}

	// Caller must be a sovereign-admin — the support session is a
	// privileged operator action. ClaimsFromContext carries the authed
	// session; an unauthenticated caller never reaches here (session
	// gate), but we re-check the role explicitly.
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	if !enterOrgCallerIsAdmin(claims) {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":  "insufficient-role",
			"detail": "Enter-org requires a sovereign-admin session",
		})
		return
	}

	sovFQDN := h.sovereignFQDN()
	if sovFQDN == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "sovereign-fqdn-unconfigured",
			"detail": "this catalyst-api Pod cannot resolve its Sovereign FQDN",
		})
		return
	}

	// Guard: the parent org IS the Sovereign — entering it is a no-op
	// ("you are already inside it", §5). The button is hidden on the
	// parent row; reject a forged attempt.
	if strings.EqualFold(slug, sovFQDN) || slug == "sovereign" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "cannot-enter-parent",
			"detail": "the parent organization is the Sovereign itself — you are already inside it",
		})
		return
	}

	if h.handoverSigner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "handover-signer-unwired",
			"detail": "this catalyst-api Pod has no handover signer; the support session cannot be minted",
		})
		return
	}

	// The org's own console host. Per-org consoles are served at
	// console.<subdomain>.<parent-zone> — and the parent zone is the
	// pool/BYO domain captured at create time (epic #825), which is NOT
	// necessarily the Sovereign FQDN. A free-subdomain Org created on a
	// pool domain (e.g. omani.homes) on an omantel.biz Sovereign has its
	// console at console.<org>.omani.homes, not console.<org>.omantel.biz.
	// Re-synthesising the host from sovFQDN (#4075) minted a handover URL
	// for the wrong host and an aud/sovereign_fqdn claim the org console
	// would reject. Resolve the real console host from the stored provision
	// record and fall back to the legacy synthesis only if no record/host
	// is available.
	//
	// orgDomainSuffix is the host minus the leading "console." prefix
	// (i.e. <subdomain>.<parent-zone>) — used to build the aud /
	// sovereign_fqdn claim and the support principal so they match the
	// host the browser actually lands on.
	orgConsoleHost := ""
	if h.orgTenantDeps.Store != nil {
		for _, rec := range h.orgTenantDeps.Store.List() {
			if strings.EqualFold(strings.TrimSpace(rec.Subdomain), slug) {
				orgConsoleHost = deriveConsoleHost(rec)
				break
			}
		}
	}
	if orgConsoleHost == "" {
		orgConsoleHost = "console." + slug + "." + sovFQDN
	}
	orgDomainSuffix := strings.TrimPrefix(orgConsoleHost, "console.")
	orgConsoleURL := "https://" + orgConsoleHost

	// The support principal — a dedicated support identity in the org's
	// realm, NEVER the owner's identity (§2.4). Derived from the
	// operator's email so the audit trail ties the session to a real
	// person while the principal that lands in the org is a support role.
	supportPrincipal := "support+" + sanitizeEmailLocal(claims.Email) + "@" + orgDomainSuffix

	now := time.Now()
	expiresAt := now.Add(enterOrgMaxTTL)
	jti := uuid.NewString()

	// Mint the handover token targeting the ORG's console (aud =
	// console.<org>.<sov>). The org's catalyst-api redeems it at
	// /auth/handover with the SAME validation the Sovereign uses for the
	// mothership's token (role=sovereign-admin, email_verified). The
	// support_session marker drives the in-console banner; impersonated_org
	// records which org was entered.
	//
	// #5614: `iss` was the bare literal "https://console.openova.io" — a
	// Pillar-5 mothership tether stamped by a Sovereign into its OWN token,
	// and the last unconditional duplicate of the #2940 seam in the auth
	// path. It now resolves through handoverjwt.DefaultIssuer(), the same
	// single source the signer uses, so a cut-over Sovereign stamps its own
	// console. Safe in both directions because the redeemer
	// (acceptedHandoverIssuers, auth_handover.go) accepts the configured
	// issuer AND the mothership origin — and this token is minted and
	// redeemed by the same pod, so there is no version-skew window.
	tokenClaims := jwt.MapClaims{
		"iss":            handoverjwt.DefaultIssuer(),
		"sub":            supportPrincipal,
		"email":          supportPrincipal,
		"email_verified": true,
		"role":           "sovereign-admin",
		"sovereign_fqdn": orgDomainSuffix,
		"deployment_id":  "",
		"iat":            now.Unix(),
		"exp":            expiresAt.Unix(),
		"jti":            jti,
		// Support-session markers (§6 B2): the org console renders the
		// banner from support_session + records the auditing context.
		"support_session":   true,
		"support_principal": supportPrincipal,
		"impersonated_org":  slug,
		"initiated_by":      claims.Email,
	}
	token, err := h.handoverSigner.SignCustomClaims(tokenClaims)
	if err != nil {
		h.log.Error("enter-org: SignCustomClaims failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "mint-failed"})
		return
	}

	auditID := "enter-org-" + slug + "-" + jti

	// Write the audit event (who/when/org/duration) BEFORE returning the
	// URL. Structured log line is the durable audit record; downstream
	// log shipping persists it. (#3378 §6 B2: "writes the audit event".)
	h.log.Info("enter-org: support session minted",
		"audit", auditID,
		"initiatedBy", claims.Email,
		"org", slug,
		"supportPrincipal", supportPrincipal,
		"ttlSeconds", int(enterOrgMaxTTL.Seconds()),
		"expiresAt", expiresAt.Format(time.RFC3339),
	)

	handoverURL := orgConsoleURL + "/auth/handover?token=" + token

	writeJSON(w, http.StatusOK, EnterOrgResponse{
		HandoverURL:      handoverURL,
		SupportPrincipal: supportPrincipal,
		Org:              slug,
		ExpiresAt:        expiresAt,
		AuditEventID:     auditID,
	})
}

// enterOrgCallerIsAdmin reports whether the authed caller is a
// sovereign-admin (or owner). Mirrors the role vocabulary the rest of the
// Sovereign console authz uses.
func enterOrgCallerIsAdmin(claims *auth.Claims) bool {
	if claims == nil {
		return false
	}
	if strings.EqualFold(claims.Tier, "owner") {
		return true
	}
	for _, role := range claims.RealmAccess.Roles {
		switch strings.ToLower(role) {
		case "sovereign-admin", "sovereign-admins", "catalyst-admin", "catalyst-owner":
			return true
		}
	}
	return false
}

// sanitizeEmailLocal extracts a DNS/identity-safe local part from an
// email so the support principal is a valid identifier.
func sanitizeEmailLocal(email string) string {
	local := email
	if i := strings.IndexByte(email, '@'); i >= 0 {
		local = email[:i]
	}
	var b strings.Builder
	for _, c := range strings.ToLower(local) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.':
			b.WriteRune(c)
		default:
			b.WriteByte('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "operator"
	}
	return out
}
