// Package handler — Catalyst-side bridge handlers that sit between
// catalyst-api PATs and the upstream NewAPI admin API.
//
// Wave 1b SCAFFOLD ONLY. The Sovereign deploys upstream Calcium-Ion/new-api
// as a Pod (see platform/newapi/chart/templates/deployment.yaml) — we do
// NOT modify upstream Go code. Instead, the catalyst-api process exposes
// this bridge under /admin/tokens/sandbox; the sandbox-controller (Wave 4)
// calls it whenever it needs to mint or rotate a per-Sandbox NewAPI key.
//
// What this file IS:
//   - The HTTP handler shape (POST /admin/tokens/sandbox).
//   - The PAT validation contract (audience=newapi, typ=pat).
//   - Documented hooks for the upstream NewAPI admin-API calls
//     (POST /api/user, POST /api/token) that Wave 4 wires.
//
// What this file is NOT (deferred to Wave 4):
//   - The actual outbound HTTP client to NewAPI's admin API. Today
//     the handler returns the inbound PAT verbatim as a placeholder
//     so the bridge contract can be exercised end-to-end in tests
//     without standing up an upstream NewAPI instance.
//   - The Sandbox CR binding (the controller writes the returned
//     token into Secret/sandbox-<uid>-newapi-token; the binding
//     lives in the controller, not here).
//   - The /admin/tokens/sandbox/{uid} DELETE path (rotation +
//     revocation — Wave 4 controller-side reconcile).
//
// Contract: products/sandbox/docs/newapi-proxy-contract.md §3.
package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	sharedauth "github.com/openova-io/openova/core/services/shared/auth"
)

// BridgeAudience is the aud claim a PAT MUST carry to mint a NewAPI key
// through this bridge. The sandbox-controller stamps this when it calls
// POST /auth/pat — see core/services/auth/handlers/pat.go.
const BridgeAudience = "newapi"

// Handler is the bridge handler dependency container.
//
// Wave 1b leaves fields minimal; Wave 4 will add:
//   - NewAPIAdminURL string   — the upstream admin API base URL
//                               (e.g. http://newapi.newapi.svc.cluster.local:3000)
//   - NewAPIAdminToken string — master token for the upstream admin API
//   - HTTPClient *http.Client — outbound client with timeout + retry
type Handler struct {
	// JWTSecret is the same secret core/services/auth uses to sign PATs.
	// Read at process start from JWT_SECRET. Required for PAT validation.
	JWTSecret []byte

	// Log is the slog instance for audit + diagnostic output. Required.
	Log *slog.Logger
}

// sandboxTokenRequest is the JSON body for POST /admin/tokens/sandbox.
type sandboxTokenRequest struct {
	// SandboxUID is the Sandbox CR's metadata.uid. The bridge uses
	// this to bind the NewAPI key to a specific Sandbox so rotation
	// + revocation on CR deletion are unambiguous.
	SandboxUID string `json:"sandbox_uid"`

	// OrgID — the Organization slug. The bridge cross-checks this
	// against the PAT's org_id claim to prevent a sandbox-controller
	// in one Org minting a NewAPI key labelled as another Org's.
	OrgID string `json:"org_id"`

	// SandboxName — the human-readable Sandbox name
	// (`Sandbox.metadata.name`). Surfaced in NewAPI's admin UI so ops
	// staff can see which Sandbox holds which key.
	SandboxName string `json:"sandbox_name"`

	// Rotate — when true, revoke any existing key for this SandboxUID
	// before issuing a new one. Used by the controller on Sandbox spec
	// quota changes. Wave 4 wires the actual revocation; Wave 1b
	// returns the same response with `rotated: true` echo.
	Rotate bool `json:"rotate,omitempty"`
}

// sandboxTokenResponse is what POST /admin/tokens/sandbox returns.
type sandboxTokenResponse struct {
	// Token is the NewAPI-native API key (a `sk-…` string). The
	// caller (sandbox-controller) writes this to a Kubernetes Secret;
	// the Sandbox-Pod mounts it as LLM_GATEWAY_TOKEN.
	Token string `json:"token"`

	// NewAPIUserID is the upstream NewAPI user id the key is bound
	// to. Stored alongside the token so rotation knows which
	// upstream user to mutate.
	NewAPIUserID string `json:"newapi_user_id"`

	// ExpiresAt — RFC3339. Always matches the inbound PAT's exp.
	// NewAPI keys are revocable but not natively expiring; the bridge
	// owns scheduling revocation when this timestamp passes (Wave 4).
	ExpiresAt time.Time `json:"expires_at"`

	// Rotated — true when the bridge actually replaced a prior key
	// (vs first-time issuance). Echoed for the controller's audit log.
	Rotated bool `json:"rotated"`
}

// SandboxToken handles POST /admin/tokens/sandbox.
//
// Wave 1b stub behavior:
//
//  1. Validates the inbound Authorization: Bearer <PAT> against JWTSecret.
//  2. Asserts the PAT has typ=pat, audience contains "newapi", and a
//     non-empty org_id claim.
//  3. Cross-checks the request body's org_id matches the PAT claim
//     (prevents Sandbox A's controller minting keys for Sandbox B's Org).
//  4. Returns the PAT itself as the token + a synthesized
//     newapi_user_id of the form `sandbox-<uid>` so the controller's
//     happy-path test can exercise the contract end-to-end.
//
// Wave 4 will replace step 4 with the actual newapi admin-API call.
func (h *Handler) SandboxToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.JWTSecret == nil || len(h.JWTSecret) == 0 {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "bridge misconfigured: JWT secret unset",
		})
		return
	}

	// ── 1. Extract + parse Bearer PAT ───────────────────────────────────
	authHdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(authHdr, "Bearer ") {
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": "missing or invalid authorization header",
		})
		return
	}
	raw := strings.TrimPrefix(authHdr, "Bearer ")

	claims := &sharedauth.Claims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return h.JWTSecret, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !tok.Valid {
		h.Log.Warn("sandbox-token: PAT parse failed", "err", err)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}

	// ── 2. Validate token type + audience ──────────────────────────────
	if claims.Typ != "pat" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "wrong token type: bridge requires typ=pat",
		})
		return
	}
	audOK := false
	for _, a := range claims.Audience {
		if a == BridgeAudience {
			audOK = true
			break
		}
	}
	if !audOK {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": fmt.Sprintf("wrong audience: bridge requires aud contains %q", BridgeAudience),
		})
		return
	}
	if claims.OrgID == "" {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "PAT missing org_id claim",
		})
		return
	}

	// ── 3. Decode + validate request body ──────────────────────────────
	var req sandboxTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
		return
	}
	if req.SandboxUID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "sandbox_uid is required",
		})
		return
	}
	if req.OrgID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "org_id is required",
		})
		return
	}
	if req.OrgID != claims.OrgID {
		h.Log.Warn("sandbox-token: org_id mismatch",
			"pat_org", claims.OrgID, "req_org", req.OrgID, "sandbox_uid", req.SandboxUID)
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "org_id in request body does not match PAT claim",
		})
		return
	}

	// ── 4. Mint (Wave 1b: stub — echo PAT) ──────────────────────────────
	//
	// Wave 4: replace this block with a call to upstream NewAPI's
	// admin API:
	//
	//   POST {NewAPIAdminURL}/api/user                  # idempotent
	//     body: {username: fmt.Sprintf("sandbox-%s", req.SandboxUID),
	//            display_name: req.SandboxName,
	//            external_id: req.OrgID}
	//
	//   POST {NewAPIAdminURL}/api/token
	//     body: {user_id: <newapi_user_id_from_above>,
	//            name: req.SandboxName,
	//            unlimited_quota: false,
	//            remain_quota: <per-Sandbox initial credit>}
	//
	// The response carries the NewAPI key string + numeric user_id.
	// Return both to the caller. On `req.Rotate=true`, call
	// /api/token/{old}/delete before issuing the new one.
	var exp time.Time
	if claims.ExpiresAt != nil {
		exp = claims.ExpiresAt.Time
	}
	h.Log.Info("sandbox-token: issued (stub — Wave 4 wires upstream NewAPI)",
		"sandbox_uid", req.SandboxUID,
		"org_id", req.OrgID,
		"rotate", req.Rotate,
		"expires_at", exp,
	)

	writeJSON(w, http.StatusOK, sandboxTokenResponse{
		Token:        raw,
		NewAPIUserID: "sandbox-" + req.SandboxUID,
		ExpiresAt:    exp,
		Rotated:      req.Rotate,
	})
}

// writeJSON writes JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
