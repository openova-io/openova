// Package handler — Catalyst-side bridge handlers that sit between
// catalyst-api PATs and the upstream NewAPI admin API.
//
// SandboxToken (POST /admin/tokens/sandbox)
// =========================================
//
// Replaces the Wave 1b stub (PR #1619) with a real mint implementation
// (PR #1638). The endpoint is invoked by the sandbox-controller when it
// rolls out a fresh Sandbox Pod and needs an HS256 token the Pod can
// present to the NewAPI gateway on every `/v1/*` call.
//
// Caller authentication
// ─────────────────────
// The endpoint is admin-only. The sandbox-controller authenticates with
// a shared bearer secret loaded from the chart-emitted Secret
// `newapi-token-signing-key` key `ADMIN_SECRET` and passes it as
// `Authorization: Bearer <secret>`. The secret is the SAME persistent
// random bytes that sign the minted tokens — colocating the two keeps
// chart wiring (and rotation) atomic: one Secret, one Helm-lookup
// resource-policy:keep guard, one env-var read at process start.
//
// (Future hardening — Wave 5+: accept a per-controller ServiceAccount
// projected token via TokenReview against the host cluster. For now the
// shared admin secret matches the simplicity of the rest of the
// platform/newapi chart and keeps the bridge self-contained.)
//
// Token contract
// ──────────────
// The minted JWT is HS256, signed with the bytes in
// `NEWAPI_TOKEN_SIGNING_KEY`. Claim shape:
//
//	sub      — Sandbox CR's uid (or whatever opaque sandbox_id the
//	           controller passes; the bridge does not interpret it).
//	org      — Organization slug. NewAPI uses this for per-Org
//	           credit/quota partitioning and audit-log attribution.
//	user     — owner email or Keycloak sub of the Sandbox's owner.
//	           Forwarded into NewAPI's per-request X-User-Id for the
//	           billing ledger.
//	channels — list of channel names the Sandbox Pod is allowed to
//	           target (e.g. ["qwen3.6-bankdhofar"]). NewAPI's channel
//	           router enforces this allowlist server-side; a token
//	           with channels:["a","b"] cannot call channel "c".
//	exp      — 7 days from issuance. Matches PAT.DefaultPATTTL.
//	iat      — issued-at.
//	typ      — "sandbox" — discriminator so NewAPI audit logs can
//	           distinguish Sandbox-Pod tokens from human PATs.
//
// On rotation the caller simply re-POSTs with the same sandbox_id; the
// returned token supersedes the previous one (NewAPI has no native
// per-token revocation registry; the controller deletes the old Pod
// before exposing the new token so the in-flight blast radius is
// bounded by Pod restart cadence).
//
// Contract: products/sandbox/docs/newapi-proxy-contract.md §3.
package handler

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// SandboxTokenTTL is the lifetime of a per-Sandbox HS256 token. 7 days
// matches PAT.DefaultPATTTL in core/services/auth/handlers/pat.go — a
// typical coding-week session.
const SandboxTokenTTL = 7 * 24 * time.Hour

// SandboxTokenType is the `typ` claim discriminator on minted tokens.
const SandboxTokenType = "sandbox"

// Env var names. Centralised so deployment.yaml + test code reference
// the same strings.
const (
	// EnvSigningKey holds the raw HS256 signing key bytes. Mounted from
	// Secret `newapi-token-signing-key`, key `SIGNING_KEY`.
	EnvSigningKey = "NEWAPI_TOKEN_SIGNING_KEY"

	// EnvAdminSecret holds the admin-only shared secret the
	// sandbox-controller presents on each call. Mounted from the same
	// Secret, key `ADMIN_SECRET`.
	EnvAdminSecret = "NEWAPI_ADMIN_SECRET"
)

// Handler is the bridge handler dependency container. Fields are
// populated from env at process start; an unset signing key or admin
// secret causes the handler to refuse with 503 (visible, NOT silent).
type Handler struct {
	// SigningKey is the raw HS256 secret used to sign minted tokens.
	// Read at process start from NEWAPI_TOKEN_SIGNING_KEY. Required.
	SigningKey []byte

	// AdminSecret is the shared bearer the sandbox-controller presents
	// on every call. Read at process start from NEWAPI_ADMIN_SECRET.
	// Required. Compared with subtle.ConstantTimeCompare.
	AdminSecret []byte

	// Log is the slog instance for audit + diagnostic output. Required.
	Log *slog.Logger

	// Now is the wall-clock source. Defaults to time.Now when nil.
	// Injected for deterministic tests.
	Now func() time.Time
}

// NewHandlerFromEnv constructs a Handler with values pulled from the
// environment. Returns an error when either required env var is empty
// so the catalyst-api process can fail loudly at start instead of
// shipping a forgeable-token endpoint silently.
func NewHandlerFromEnv(log *slog.Logger) (*Handler, error) {
	signing := os.Getenv(EnvSigningKey)
	if strings.TrimSpace(signing) == "" {
		return nil, fmt.Errorf("handler: %s is empty (chart not wired with newapi-token-signing-key Secret)", EnvSigningKey)
	}
	admin := os.Getenv(EnvAdminSecret)
	if strings.TrimSpace(admin) == "" {
		return nil, fmt.Errorf("handler: %s is empty (chart not wired with newapi-token-signing-key Secret)", EnvAdminSecret)
	}
	return &Handler{
		SigningKey:  []byte(signing),
		AdminSecret: []byte(admin),
		Log:         log,
	}, nil
}

// sandboxTokenRequest is the JSON body for POST /admin/tokens/sandbox.
//
// Wire field names match products/sandbox/docs/newapi-proxy-contract.md.
type sandboxTokenRequest struct {
	// OrgID — Organization slug (matches Claims.OrgID).
	OrgID string `json:"org_id"`

	// UserID — owner email or Keycloak sub of the Sandbox owner.
	UserID string `json:"user_id"`

	// SandboxID — Sandbox CR's metadata.uid (or any opaque id the
	// controller assigns; the bridge does not interpret it).
	SandboxID string `json:"sandbox_id"`

	// AllowedChannels — list of NewAPI channel names the Sandbox Pod
	// may target. Empty list is rejected (a Sandbox with no channels
	// cannot reach an LLM, almost always a misconfiguration).
	AllowedChannels []string `json:"allowed_channels"`
}

// sandboxTokenResponse is what POST /admin/tokens/sandbox returns.
type sandboxTokenResponse struct {
	// Token is the HS256-signed JWT. The sandbox-controller writes it
	// into Secret/sandbox-<uid>-newapi-token, mounted into the Sandbox
	// Pod as LLM_GATEWAY_TOKEN.
	Token string `json:"token"`

	// ExpiresAt — absolute expiry timestamp (RFC3339).
	ExpiresAt time.Time `json:"expires_at"`
}

// SandboxToken handles POST /admin/tokens/sandbox.
//
//  1. Validates the inbound Authorization: Bearer <admin-secret>.
//  2. Decodes + validates the request body.
//  3. Mints an HS256 JWT bound to {org_id, user_id, sandbox_id,
//     allowed_channels} with a 7-day TTL.
//  4. Returns {token, expires_at}.
//
// Failure modes:
//   - 503 when SigningKey or AdminSecret is unset at process start
//     (chart wiring gap — surfaced visibly).
//   - 401 when Authorization header is missing/malformed or the bearer
//     does not match AdminSecret.
//   - 400 when request body is malformed or required fields are empty.
//   - 405 on non-POST.
func (h *Handler) SandboxToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if len(h.SigningKey) == 0 || len(h.AdminSecret) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "bridge misconfigured: signing key or admin secret unset",
		})
		return
	}

	// ── 1. Validate admin bearer ────────────────────────────────────────
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "missing or invalid authorization header",
		})
		return
	}
	presented := strings.TrimPrefix(authHdr, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(presented), h.AdminSecret) != 1 {
		h.logSafe("sandbox-token: admin secret mismatch", "remote_addr", r.RemoteAddr)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid admin credentials"})
		return
	}

	// ── 2. Decode + validate request body ───────────────────────────────
	var req sandboxTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
		return
	}
	req.OrgID = strings.TrimSpace(req.OrgID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.SandboxID = strings.TrimSpace(req.SandboxID)
	if req.OrgID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "org_id is required"})
		return
	}
	if req.UserID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id is required"})
		return
	}
	if req.SandboxID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sandbox_id is required"})
		return
	}
	if len(req.AllowedChannels) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "allowed_channels must contain at least one channel name",
		})
		return
	}
	// Strip empty + de-duplicate while preserving order. A sandbox-
	// controller bug that sends [""] would otherwise yield a token
	// that NewAPI silently accepts as "no channel restriction" — fail
	// fast.
	cleaned := make([]string, 0, len(req.AllowedChannels))
	seen := make(map[string]struct{}, len(req.AllowedChannels))
	for _, c := range req.AllowedChannels {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		cleaned = append(cleaned, c)
	}
	if len(cleaned) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "allowed_channels must contain at least one non-empty channel name",
		})
		return
	}

	// ── 3. Mint HS256 token ─────────────────────────────────────────────
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	issuedAt := now()
	exp := issuedAt.Add(SandboxTokenTTL)
	tok, err := mintSandboxToken(h.SigningKey, sandboxClaims{
		Sub:      req.SandboxID,
		Org:      req.OrgID,
		User:     req.UserID,
		Channels: cleaned,
		IssuedAt: issuedAt,
		ExpireAt: exp,
	})
	if err != nil {
		h.logSafe("sandbox-token: mint failed", "err", err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to mint token",
		})
		return
	}

	h.logSafe("sandbox-token: issued",
		"sandbox_id", req.SandboxID,
		"org_id", req.OrgID,
		"user_id", req.UserID,
		"channels", strings.Join(cleaned, ","),
		"expires_at", exp.Format(time.RFC3339),
	)

	writeJSON(w, http.StatusOK, sandboxTokenResponse{
		Token:     tok,
		ExpiresAt: exp,
	})
}

// sandboxClaims is the in-process projection of the JWT body. Kept
// separate from sandboxTokenRequest so the wire shape can evolve
// without changing how we lay out the JWT.
type sandboxClaims struct {
	Sub      string
	Org      string
	User     string
	Channels []string
	IssuedAt time.Time
	ExpireAt time.Time
}

// mintSandboxToken signs an HS256 JWT with the documented claim shape.
// Exposed as a package-private function so the unit test can verify
// the round-trip without invoking the HTTP handler.
func mintSandboxToken(secret []byte, c sandboxClaims) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("signing key empty")
	}
	claims := jwt.MapClaims{
		"sub":      c.Sub,
		"org":      c.Org,
		"user":     c.User,
		"channels": c.Channels,
		"iat":      c.IssuedAt.Unix(),
		"exp":      c.ExpireAt.Unix(),
		"typ":      SandboxTokenType,
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	out, err := t.SignedString(secret)
	if err != nil {
		return "", fmt.Errorf("sign sandbox token: %w", err)
	}
	return out, nil
}

// logSafe wraps the slog call so a nil logger doesn't panic in tests.
func (h *Handler) logSafe(msg string, args ...any) {
	if h.Log == nil {
		return
	}
	h.Log.Info(msg, args...)
}

// writeJSON writes JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
