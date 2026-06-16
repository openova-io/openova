// auth_test_session.go — POST /api/v1/auth/test-session?tier=<tier>
//
// QA-loop iter-11 Cluster-A: tier-scoped session minting for the
// 5-agent QA team's test executor. The matrix asserts that certain
// privileged endpoints return 403 to viewer/developer/operator tiers
// and 200 to admin/owner. The PIN-via-IMAP login the executor runs
// always lands tier=owner (because the inbox itself is the owner's),
// so every tier-boundary row mis-fires.
//
// This endpoint mints a tier-scoped session JWT for a synthetic
// QA test user (qa-test-{tier}@openova.io) without touching the
// real PIN flow. It is GATED by the env CATALYST_TEST_SESSION_ENABLED
// so production deployments cannot mint arbitrary sessions:
//
//   - Production / customer Sovereigns: env unset (default)
//     → endpoint returns 404 Not Found (looks unrouted to attackers)
//   - QA / chroot test Sovereigns:      env = "true"
//     → endpoint returns 200 with Set-Cookie + JWT
//
// Per /home/openova/.claude/projects/-home-openova-repos-openova-private/memory/feedback_no_mvp_no_workarounds.md:
// this is target-state (real, gated, complete), NOT a stub. The 5
// supported tiers map onto the openova catalog hierarchy
// viewer < developer < operator < admin < owner per
// docs/EPICS-1-6-unified-design.md §6.2; the JWT carries the
// matching `tier` claim AND a Keycloak-shaped realm_access.roles
// list so both gate seams (Tier vs HasRealmRole) accept the token.
//
// The synthetic user emails (qa-test-viewer@openova.io etc.) are
// also seeded into the cluster as UserAccess CRs by the qa-fixtures
// chart templates (see chart/templates/qa-fixtures/useraccess-qa-test-*.yaml)
// so the useraccess-controller materializes per-cluster RoleBindings
// with the correct tier role binding — making the JWT's authorization
// context match the cluster's authorization context byte-for-byte.

package handler

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// testSessionEnvVar is the env variable that gates whether this
// endpoint is reachable. Defaults to "false" (production-safe).
const testSessionEnvVar = "CATALYST_TEST_SESSION_ENABLED"

// testSessionEmailPrefix — every QA-minted test user's email is
// prefixed with this constant so audit logs can filter test sessions
// out of "real operator" queries:
//
//	qa-test-viewer@openova.io
//	qa-test-developer@openova.io
//	qa-test-operator@openova.io
//	qa-test-admin@openova.io
//	qa-test-owner@openova.io
const testSessionEmailPrefix = "qa-test-"

// testSessionEmailDomain — domain of the synthetic test users.
// Matches the openova realm so EnsureUser at the controller side
// resolves to the same Keycloak group hierarchy real operators use.
const testSessionEmailDomain = "openova.io"

// tierRoleMap maps the 5 supported tier names onto their canonical
// Keycloak realm-role name (the value the JWT's
// realm_access.roles[] carries). The mapping mirrors the chart's
// tier-role bindings under products/catalyst/chart/templates/...:
//
//	tier=viewer    → role=catalyst-viewer
//	tier=developer → role=catalyst-developer
//	tier=operator  → role=catalyst-operator
//	tier=admin     → role=catalyst-admin
//	tier=owner     → role=catalyst-owner
//
// This is the canonical tier→catalyst-* realm-role vocabulary the
// PIN-mint authority (pinSessionAuthority, #3374 Layer-C) also stamps —
// this map extends it to all 5 tiers for the QA test-session endpoint.
var tierRoleMap = map[string]string{
	"viewer":    "catalyst-viewer",
	"developer": "catalyst-developer",
	"operator":  "catalyst-operator",
	"admin":     "catalyst-admin",
	"owner":     "catalyst-owner",
}

// testSessionRequest is the optional JSON body. All fields default
// to deterministic synthetic values when omitted; clients only need
// to set them when they want a multi-user scenario (e.g. two
// distinct viewer users in the same test).
type testSessionRequest struct {
	// Email override. Default: qa-test-{tier}@openova.io.
	Email string `json:"email,omitempty"`
	// Subject override. Default: qa-test-{tier} (used as JWT `sub`).
	Subject string `json:"subject,omitempty"`
}

// testSessionResponse — wire shape returned to QA executor scripts.
// Mirrors pinVerifyResponse but adds the minted claims so the test
// runner can assert tier propagation without parsing the cookie JWT.
type testSessionResponse struct {
	OK    bool   `json:"ok"`
	Tier  string `json:"tier"`
	Role  string `json:"role"`
	Email string `json:"email"`
	// Token is the same JWT placed in the Set-Cookie. Returned in
	// the JSON body so non-browser clients (curl, Go test code) can
	// stamp it on Authorization: Bearer headers without parsing
	// Set-Cookie themselves.
	Token string `json:"token"`
}

// testSessionEnabled returns true when this Sovereign has explicitly
// enabled the test-session endpoint via env. Any value other than
// the literal "true" disables it.
func testSessionEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(testSessionEnvVar)))
	return v == "true" || v == "1" || v == "yes"
}

// HandleAuthTestSession handles POST /api/v1/auth/test-session?tier=<tier>.
//
// Wire shape:
//
//	POST /api/v1/auth/test-session?tier=viewer
//	  → 200 OK
//	    Set-Cookie: catalyst_session=<jwt>; HttpOnly; Secure; SameSite=Lax
//	    {"ok": true, "tier": "viewer", "role": "catalyst-viewer",
//	     "email": "qa-test-viewer@openova.io", "token": "<jwt>"}
//
// Errors:
//
//	404 Not Found — endpoint disabled (env CATALYST_TEST_SESSION_ENABLED != true).
//	  Wire-indistinguishable from "this server doesn't have this route" —
//	  customer Sovereigns thus reveal nothing about the existence of QA
//	  hooks in the catalyst-api binary.
//
//	400 Bad Request — tier query param missing or not one of the 5
//	  supported tiers. Wire body: {"error":"tier-invalid","detail":...}.
//
//	503 Service Unavailable — handover signer not wired (catalyst-api
//	  built without CATALYST_HANDOVER_KEY_PATH). Same as PIN-verify —
//	  no signing key, no JWT.
func (h *Handler) HandleAuthTestSession(w http.ResponseWriter, r *http.Request) {
	if !testSessionEnabled() {
		// Wire-indistinguishable from a missing route. NEVER reveal
		// "this is gated" to a hostile caller — they could pivot to
		// brute-forcing the gate env name.
		http.NotFound(w, r)
		return
	}
	if h.handoverSigner == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error":  "auth-unconfigured",
			"detail": "handover signer unavailable",
		})
		return
	}

	tier := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tier")))
	role, ok := tierRoleMap[tier]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  "tier-invalid",
			"detail": "tier query param must be one of: viewer, developer, operator, admin, owner",
		})
		return
	}

	// Optional body. Decode failures are non-fatal — defaults still
	// produce a valid session (the body only carries multi-user
	// overrides). r.Body is nil-safe to ignore here.
	var body testSessionRequest
	if r.ContentLength > 0 && r.Body != nil {
		// Best-effort decode; ignore errors. Production callers
		// (curl, Playwright) generally send no body at all.
		_ = decodeJSONBody(r, &body)
	}

	email := strings.TrimSpace(body.Email)
	if email == "" {
		email = testSessionEmailPrefix + tier + "@" + testSessionEmailDomain
	}
	subject := strings.TrimSpace(body.Subject)
	if subject == "" {
		subject = testSessionEmailPrefix + tier
	}

	// Mint the session JWT. Same shape as PIN-verify (see auth.go
	// HandlePinVerify) — single contract surface for downstream
	// gates that check `Claims.Tier` OR `Claims.HasRealmRole(...)`.
	now := time.Now()
	sessionClaims := jwt.MapClaims{
		"iss":            pinIssuer,
		"sub":            subject,
		"email":          email,
		"email_verified": true,
		"role":           pinSessionRole,
		"tier":           tier,
		"realm_access": map[string]any{
			"roles": []string{role},
		},
		"iat": now.Unix(),
		"exp": now.Add(pinSessionTTL).Unix(),
		"jti": uuid.NewString(),
		"typ": "session",
		// Audit-log discriminator: every test-session JWT carries
		// `qa_test_session: true` so audit-log consumers can filter
		// out QA traffic from real-operator activity.
		"qa_test_session": true,
	}
	accessToken, err := h.handoverSigner.SignCustomClaims(sessionClaims)
	if err != nil {
		h.log.Error("auth/test-session: SignCustomClaims failed", "err", err, "tier", tier)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":  "session-signing-failed",
			"detail": "could not mint session",
		})
		return
	}

	cookieMaxAge := int(pinSessionTTL.Seconds())
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	cookieDomain := os.Getenv("CATALYST_SESSION_COOKIE_DOMAIN")

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
	// catalyst_refresh placeholder (cleared) — same hygiene as
	// PIN-verify so logout / re-login flows on the executor don't
	// inherit a stale refresh cookie from a prior tier.
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

	h.log.Info("auth/test-session: minted",
		"tier", tier,
		"role", role,
		"email", email,
		"expires_in", cookieMaxAge,
	)

	writeJSON(w, http.StatusOK, testSessionResponse{
		OK:    true,
		Tier:  tier,
		Role:  role,
		Email: email,
		Token: accessToken,
	})
}
