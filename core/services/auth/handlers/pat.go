// pat.go — POST /auth/pat
//
// Wave 1b — Personal Access Token (PAT) issuance for Sandbox + automation.
//
// Why this exists
// ───────────────
// The existing /auth/refresh path mints a 15-minute JWT. That's right for
// browser sessions but wrong for:
//
//   - A Sandbox coding session (hours, possibly multi-day) where the
//     Claude/Cursor/Qwen agent inside the user's vcluster needs a stable
//     token to call MCP tools.
//   - A sandbox-controller minting a per-Sandbox token to inject into a
//     newapi proxy URL — the controller can't refresh it on the fly.
//   - CI / kubectl / catalystctl wrappers that need a stable token to
//     script against the Sovereign API.
//
// This handler issues a PAT signed with the same JWT_SECRET as the
// session token, but with:
//   - longer TTL (default 7d; admin-configurable up to MaxPATTTL)
//   - `typ=pat` claim so downstream consumers can audit-log PATs
//     distinctly from interactive sessions
//   - `org_id`, `groups`, `capabilities` baked in (Wave 1b prerequisite
//     per products/sandbox/docs/architecture.md §6)
//
// Token contract: products/sandbox/docs/architecture.md §6 +
// core/services/shared/auth/claims.go.
//
// NOT yet implemented in this PR (Wave 4 work):
//   - Capability projection from the bearer's tier + per-Org grants —
//     today we copy whatever Keycloak/auth-service has on the bearer.
//   - Per-PAT revocation lookup (jti → revoked set) — today the only
//     revocation paths are TTL expiry and /auth/logout-all (which
//     revokes refresh tokens, not PATs).
//   - Per-Sandbox token issuance bound to a Sandbox CR — see
//     platform/newapi/internal/handler/sandbox_token.go for the bridge
//     handler that calls this endpoint with `audience=newapi`.
package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
	"github.com/openova-io/openova/core/services/shared/middleware"
	"github.com/openova-io/openova/core/services/shared/respond"
)

// PAT issuance configuration. Wave 1b defaults; admin can tune via env
// (Wave 4 work to wire ADMIN_PAT_MAX_TTL_HOURS / ADMIN_PAT_DEFAULT_TTL_HOURS
// onto the Handler struct).
const (
	// DefaultPATTTL is the TTL applied when the request omits ttl_hours.
	// 7 days matches a typical coding-week session.
	DefaultPATTTL = 7 * 24 * time.Hour

	// MaxPATTTL is the upper bound enforced server-side; requests for
	// longer TTLs are clamped (not rejected). Sovereign admin can
	// override via env in Wave 4.
	MaxPATTTL = 90 * 24 * time.Hour
)

// patRequest is the JSON body for POST /auth/pat.
type patRequest struct {
	// Name is a human-readable label for the PAT (e.g. "sandbox-emrah-vscode").
	// Surfaced in the future /auth/pats list endpoint (Wave 4).
	Name string `json:"name"`

	// TTLHours is the requested lifetime. Clamped to [1, MaxPATTTL].
	// Zero → DefaultPATTTL.
	TTLHours int `json:"ttl_hours,omitempty"`

	// Audience is the optional aud claim. When the caller is a service
	// minting a PAT for a downstream (e.g. sandbox-controller minting
	// a PAT for newapi), it sets this so newapi can validate aud.
	// Empty → audience claim omitted.
	Audience string `json:"audience,omitempty"`

	// Capabilities — optional override. When non-empty, the PAT carries
	// exactly this list (subject to subset check against the bearer's
	// current capabilities — Wave 4 work). When empty, the PAT inherits
	// the bearer's current capabilities.
	//
	// Today the bearer's own access-token claims have no `capabilities`
	// list (auth-service mints {sub, email, role} only), so this field
	// is the only way to attach capabilities to a Wave-1b PAT. Once
	// the auth-service learns to project capabilities from Keycloak
	// groups (Wave 4), inheriting from the bearer becomes the default.
	Capabilities []string `json:"capabilities,omitempty"`
}

// patResponse is what POST /auth/pat returns.
type patResponse struct {
	// Token is the signed JWT string. The caller MUST treat this as a
	// secret — it's the only time the raw token is returned.
	Token string `json:"token"`

	// JTI is the per-token unique id, suitable for storing in the
	// caller's secret store and using as a revocation handle (Wave 4
	// adds DELETE /auth/pat/{jti}).
	JTI string `json:"jti"`

	// ExpiresAt is the absolute expiry timestamp (RFC3339).
	ExpiresAt time.Time `json:"expires_at"`

	// Typ — always "pat" for tokens minted by this endpoint. Returned
	// so the caller can sanity-check before storing.
	Typ string `json:"typ"`
}

// IssuePAT handles POST /auth/pat.
//
// Authentication: requires the JWTAuth middleware (the user must have a
// valid session token). Wired in main.go via the `needsAuth` predicate.
//
// Wave-1b limitations (documented in PR body):
//   - Capabilities come from the request body, not projected from
//     Keycloak. Subset-check against the bearer's actual capabilities is
//     Wave 4 work.
//   - No org-membership check yet — any authenticated user can mint a
//     PAT bound to any OrgID they claim. The MCP server does its own
//     org-scoped authz on every tool call so a forged OrgID claim only
//     gets you tokens the MCP server then rejects.
//   - Issued PATs are NOT persisted; there's no /auth/pats list yet.
//     Revocation is TTL-only until Wave 4 ships the jti store.
func (h *Handler) IssuePAT(w http.ResponseWriter, r *http.Request) {
	userID := middleware.UserIDFromContext(r.Context())
	if userID == "" {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req patRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		respond.Error(w, http.StatusBadRequest, "name is required")
		return
	}

	// Resolve the user from the bearer's `sub` claim so we can copy
	// email/role into the PAT. A stale sub (user deleted between session
	// mint and PAT request) is a 401.
	user, err := h.Store.GetUserByID(r.Context(), userID)
	if err != nil {
		slog.Error("pat: GetUserByID failed", "userID", userID, "err", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}
	if user == nil {
		respond.Error(w, http.StatusUnauthorized, "user not found")
		return
	}

	// Clamp TTL into [1h, MaxPATTTL]. Zero → default.
	ttl := time.Duration(req.TTLHours) * time.Hour
	if ttl <= 0 {
		ttl = DefaultPATTTL
	}
	if ttl > MaxPATTTL {
		ttl = MaxPATTTL
	}
	now := time.Now()
	exp := now.Add(ttl)
	jti := uuid.NewString()

	claims := sharedauth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "openova-auth",
			Subject:   user.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
			ID:        jti,
		},
		Email: user.Email,
		Role:  user.Role,
		// OrgID + Groups left empty in this Wave 1b mint path; they
		// will be populated by the Keycloak-driven projector landing
		// in the same wave's tenant-realm + sovereign-realm mapper
		// patches. The shape is correct; the values flow once the
		// chart bump rolls.
		Capabilities: req.Capabilities,
		Typ:          "pat",
	}
	if req.Audience != "" {
		claims.Audience = jwt.ClaimStrings{req.Audience}
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(h.JWTSecret)
	if err != nil {
		slog.Error("pat: sign failed", "err", err)
		respond.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	slog.Info("pat: issued",
		"userID", user.ID,
		"jti", jti,
		"ttl_hours", int(ttl/time.Hour),
		"audience", req.Audience,
		"name", req.Name,
	)

	respond.OK(w, patResponse{
		Token:     signed,
		JTI:       jti,
		ExpiresAt: exp,
		Typ:       "pat",
	})
}

// patAudFromString is a convenience for callers that need to assemble the
// audience JSON claim manually. Kept exported so tests in this package +
// downstream consumers can shape requests the same way.
func patAudFromString(aud string) (any, error) {
	if aud == "" {
		return nil, nil
	}
	if strings.ContainsAny(aud, "{}[]") {
		return nil, fmt.Errorf("invalid audience: %q", aud)
	}
	return aud, nil
}
