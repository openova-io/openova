// byos_claude_code.go — POST /api/v1/sandbox/byos/claude-code/start,
//
//	GET  /api/v1/sandbox/byos/claude-code/callback,
//	DELETE /api/v1/sandbox/byos/claude-code,
//	GET  /api/v1/sandbox/byos/claude-code/status,
//	GET  /api/v1/sandbox/byos/claude-code/config
//
// Wave 1b SCAFFOLD ONLY — design + handler shape. The actual outbound
// OAuth client + Secret-store writes land in Wave 4 alongside the
// sandbox-controller. See products/sandbox/docs/claude-code-byos.md for
// the full design.
//
// What this file IS:
//   - The four BYOS HTTP endpoints with input validation + auth gating.
//   - The placeholder-client-id refusal path so the bridge surfaces a
//     clear 503 until the founder registers an Anthropic OAuth client.
//   - The PKCE verifier + state CSRF generation shape.
//
// What this file is NOT (deferred to Wave 4):
//   - The actual HTTP client to Anthropic's /oauth/token endpoint
//     (today the callback handler validates state + records the code
//     but does NOT perform the exchange).
//   - The KMS-encrypted Secret write
//     (`Secret/catalyst-system/sandbox-byos-claude-code-<user-uid>`).
//   - The pod-spawn injection of `ANTHROPIC_API_KEY` (controller-side).
//   - The Anthropic /oauth/revoke call on DELETE.
package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

// byosPlaceholderClientID is the sentinel value the catalyst-api chart
// stamps when the operator has not yet registered an Anthropic OAuth
// client. Every BYOS endpoint returns 503 with a clear message when
// this is the active value. See claude-code-byos.md §8 for the founder
// action that flips this live.
const byosPlaceholderClientID = "PLACEHOLDER-AWAITING-FOUNDER-REGISTRATION"

// byosStartRequest is the body for POST /sandbox/byos/claude-code/start.
type byosStartRequest struct {
	// PostConnectRedirect is the catalyst-ui path the popup-opener
	// will navigate to after the callback completes. Defaults to
	// "/sandbox?byos=ok" when empty. Restricted to relative paths
	// to prevent open-redirect.
	PostConnectRedirect string `json:"post_connect_redirect,omitempty"`
}

// byosStartResponse is what POST /start returns. The caller (catalyst-ui)
// opens this URL in a popup; the user signs in at Anthropic; the popup
// redirects to /callback which completes the handshake.
type byosStartResponse struct {
	// AuthorizationURL is the fully-formed Anthropic OAuth URL with
	// client_id + redirect_uri + state + PKCE challenge. The caller
	// opens this in `window.open(url, "anthropic-oauth")`.
	AuthorizationURL string `json:"authorization_url"`

	// State is the CSRF token the caller MAY mirror in its popup
	// message handshake. The callback validates state server-side so
	// echoing it is not required for security.
	State string `json:"state"`
}

// byosStatusResponse is what GET /status returns. Lets the Settings UI
// poll for connection state without round-tripping the Secret API.
type byosStatusResponse struct {
	// Connected is true when a non-revoked Secret exists for this user.
	Connected bool `json:"connected"`

	// AnthropicAccountEmail — populated when Connected=true. Lifted
	// from the Secret's anthropic_account_email key.
	AnthropicAccountEmail string `json:"anthropic_account_email,omitempty"`

	// ConnectedAt — populated when Connected=true. RFC3339.
	ConnectedAt string `json:"connected_at,omitempty"`

	// Disabled is true when the operator has set
	// sandbox.byos.claudeCode.enabled=false in the chart. When true,
	// the Settings card SHOULD hide the Connect button.
	Disabled bool `json:"disabled,omitempty"`

	// ClientIDPending is true when the chart's client_id is still the
	// placeholder. Distinct from `Disabled`: when ClientIDPending=true,
	// the founder has not yet registered an OAuth client — the
	// Settings card should show a "feature pending operator setup"
	// notice rather than just hiding it.
	ClientIDPending bool `json:"client_id_pending,omitempty"`
}

// ── env-var helpers ────────────────────────────────────────────────────

func byosClientID() string {
	id := strings.TrimSpace(os.Getenv("SANDBOX_BYOS_CLAUDE_CODE_CLIENT_ID"))
	if id == "" {
		return byosPlaceholderClientID
	}
	return id
}

func byosEnabled() bool {
	v := strings.TrimSpace(os.Getenv("SANDBOX_BYOS_CLAUDE_CODE_ENABLED"))
	// Default true: when the chart hasn't set the env, the feature is
	// available pending the founder OAuth registration. Operators
	// disable explicitly via the chart value.
	if v == "" {
		return true
	}
	return v == "true" || v == "1"
}

func byosAuthorizationURL() string {
	v := strings.TrimSpace(os.Getenv("SANDBOX_BYOS_CLAUDE_CODE_AUTHZ_URL"))
	if v == "" {
		return "https://console.anthropic.com/oauth/authorize"
	}
	return v
}

func byosCallbackPath() string {
	v := strings.TrimSpace(os.Getenv("SANDBOX_BYOS_CLAUDE_CODE_CALLBACK_PATH"))
	if v == "" {
		return "/api/v1/sandbox/byos/claude-code/callback"
	}
	return v
}

func byosScopes() []string {
	v := strings.TrimSpace(os.Getenv("SANDBOX_BYOS_CLAUDE_CODE_SCOPES"))
	if v == "" {
		return []string{"read:user", "write:claude-code"}
	}
	out := []string{}
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ── HandleByosClaudeCodeStart — POST /api/v1/sandbox/byos/claude-code/start
//
// Wave 1b: generates a PKCE verifier + state CSRF token, stores them
// (Wave 4 — Valkey TTL 10m), and returns the Anthropic authorization
// URL. The verifier is opaque to the caller; the callback handler
// retrieves it from the same store via the state value.
//
// Returns 503 when:
//   - The feature is disabled by operator policy.
//   - The client_id is still the placeholder.
func (h *Handler) HandleByosClaudeCodeStart(w http.ResponseWriter, r *http.Request) {
	if !byosEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "byos disabled by operator policy",
		})
		return
	}
	clientID := byosClientID()
	if clientID == byosPlaceholderClientID {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "byos pending operator setup: Anthropic OAuth client_id not yet registered (see products/sandbox/docs/claude-code-byos.md §8)",
		})
		return
	}

	// Resolve the caller — RequireSession middleware has installed Claims.
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	var req byosStartRequest
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	// Open-redirect guard — only allow relative paths.
	if req.PostConnectRedirect != "" && !strings.HasPrefix(req.PostConnectRedirect, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "post_connect_redirect must be a relative path",
		})
		return
	}

	// PKCE verifier (43-128 chars unreserved per RFC 7636 §4.1).
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// S256 PKCE challenge per RFC 7636 §4.2.
	challengeRaw := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeRaw[:])

	state := uuid.NewString()

	// Wave 4: persist {state → {verifier, sub, post_connect_redirect}}
	// with 10m TTL in Valkey. For Wave 1b the handler emits the URL
	// but does NOT persist; the callback handler returns 501 until
	// Wave 4 wires the store + Anthropic exchange.
	h.log.Info("byos: start (Wave 1b — state NOT persisted; callback returns 501)",
		"user_sub", claims.Sub,
		"state", state,
	)

	// Build the canonical Anthropic authorization URL.
	host := strings.TrimSpace(r.Host)
	if host == "" {
		host = h.sovereignFQDN()
	}
	// The redirect_uri is registered with Anthropic per Sovereign FQDN
	// (see claude-code-byos.md §8) — we MUST mirror what the founder
	// pasted into the OAuth-app console.
	redirectURI := "https://" + host + byosCallbackPath()

	q := url.Values{
		"client_id":             []string{clientID},
		"response_type":         []string{"code"},
		"redirect_uri":          []string{redirectURI},
		"state":                 []string{state},
		"code_challenge":        []string{challenge},
		"code_challenge_method": []string{"S256"},
		"scope":                 []string{strings.Join(byosScopes(), " ")},
	}
	authURL := byosAuthorizationURL() + "?" + q.Encode()

	writeJSON(w, http.StatusOK, byosStartResponse{
		AuthorizationURL: authURL,
		State:            state,
	})
}

// ── HandleByosClaudeCodeCallback — GET /api/v1/sandbox/byos/claude-code/callback?code=…&state=…
//
// Wave 1b: validates the inbound shape + the placeholder gate; returns
// 501 because the actual token exchange + Secret write are Wave 4. The
// shape is correct so the popup-opener flow can be exercised end-to-end
// against a mock Anthropic OAuth server during integration tests.
//
// Wave 4 will:
//  1. Look up {verifier, sub} by state in Valkey; 401 if missing or
//     expired.
//  2. POST to byosTokenURL() with code + verifier + client_id.
//  3. Encrypt refresh_token with the Sovereign KMS key.
//  4. Write Secret/catalyst-system/sandbox-byos-claude-code-<user-uid>.
//  5. 302 redirect to the stored post_connect_redirect (default
//     `/sandbox?byos=ok`).
func (h *Handler) HandleByosClaudeCodeCallback(w http.ResponseWriter, r *http.Request) {
	if !byosEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "byos disabled by operator policy",
		})
		return
	}
	clientID := byosClientID()
	if clientID == byosPlaceholderClientID {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "byos pending operator setup: Anthropic OAuth client_id not yet registered",
		})
		return
	}

	q := r.URL.Query()
	code := strings.TrimSpace(q.Get("code"))
	state := strings.TrimSpace(q.Get("state"))
	errCode := strings.TrimSpace(q.Get("error"))

	if errCode != "" {
		// Anthropic surfaced an error (user denied consent, etc.).
		// Redirect to a friendly UX page so the popup-opener can surface
		// the message instead of seeing raw JSON.
		http.Redirect(w, r,
			"/sandbox?byos=error&reason="+url.QueryEscape(errCode),
			http.StatusFound)
		return
	}
	if code == "" || state == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "callback missing code or state",
		})
		return
	}

	// Wave 4: state lookup → verifier; POST to Anthropic; encrypt + persist.
	h.log.Info("byos: callback received (Wave 1b — exchange NOT performed; returning 501)",
		"state", state,
		"code_len", len(code),
	)
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "byos token exchange not yet implemented (Wave 4 — controller wires Anthropic /oauth/token + Secret write)",
	})
}

// ── HandleByosClaudeCodeDisconnect — DELETE /api/v1/sandbox/byos/claude-code
//
// Wave 1b: validates auth + gates; returns 501 because the actual
// revocation + Secret delete are Wave 4.
//
// Wave 4 will:
//  1. Read Secret/catalyst-system/sandbox-byos-claude-code-<user-uid>.
//  2. POST to byosRevokeURL() with the refresh_token to revoke at
//     Anthropic. Best-effort: if the revoke call fails (e.g.
//     Anthropic 503), proceed with local delete and log a Warn — the
//     local Secret deletion is the authoritative source of truth on
//     the Sovereign.
//  3. Delete the Secret.
//  4. Emit a `sandbox.byos.revoked` audit event with by=user.
func (h *Handler) HandleByosClaudeCodeDisconnect(w http.ResponseWriter, r *http.Request) {
	if !byosEnabled() {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "byos disabled by operator policy",
		})
		return
	}
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}

	h.log.Info("byos: disconnect requested (Wave 1b — Secret delete NOT performed; returning 501)",
		"user_sub", claims.Sub,
	)
	writeJSON(w, http.StatusNotImplemented, map[string]string{
		"error": "byos disconnect not yet implemented (Wave 4 — controller wires Anthropic /oauth/revoke + Secret delete)",
	})
}

// ── HandleByosClaudeCodeStatus — GET /api/v1/sandbox/byos/claude-code/status
//
// Wave 1b: returns honest state — Disabled / ClientIDPending /
// Connected=false. The Connected=true branch is Wave 4 (needs the
// Secret read path).
//
// This endpoint is cheap + cacheable; the catalyst-ui Settings card
// polls it every few seconds while the popup is open.
func (h *Handler) HandleByosClaudeCodeStatus(w http.ResponseWriter, r *http.Request) {
	resp := byosStatusResponse{
		Disabled:        !byosEnabled(),
		ClientIDPending: byosClientID() == byosPlaceholderClientID,
		Connected:       false, // Wave 4: actual Secret read.
	}
	if resp.Disabled {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	// Whether or not the client_id is pending, the response shape is
	// the same — the UI distinguishes Disabled vs ClientIDPending vs
	// not-yet-connected purely by the flag values.
	claims := auth.ClaimsFromContext(r.Context())
	if claims == nil || claims.Sub == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
		return
	}
	// Wave 4: read Secret/catalyst-system/sandbox-byos-claude-code-<sub>.
	// When present + not RevokedAtProvider, set Connected=true and
	// populate AnthropicAccountEmail + ConnectedAt.
	writeJSON(w, http.StatusOK, resp)
}

// byosConfigResponse is what GET /config returns. The SandboxSettings UI
// reads it before showing the Connect button so the placeholder client_id
// state surfaces as a disabled-with-tooltip control instead of letting the
// user fire an OAuth URL that would 400 at Anthropic. See PR #1619 chart
// wire + claude-code-byos.md §8.
type byosConfigResponse struct {
	// ClientIDConfigured is true when the chart's
	// SANDBOX_BYOS_CLAUDE_CODE_CLIENT_ID env var is set to a non-empty,
	// non-placeholder value. The Settings card MUST disable the
	// "Connect Claude Max" button when this is false.
	ClientIDConfigured bool `json:"clientIdConfigured"`

	// OauthAuthorizeURL is the canonical Anthropic OAuth authorization
	// URL the FE will hit via POST /start. Returned as an informational
	// hint only when ClientIDConfigured=true; omitted from the JSON
	// otherwise (`omitempty`) so the UI cannot accidentally fall back
	// to it when the client_id is still the placeholder.
	OauthAuthorizeURL string `json:"oauthAuthorizeURL,omitempty"`
}

// ── HandleByosClaudeCodeConfig — GET /api/v1/sandbox/byos/claude-code/config
//
// Returns the configuration-state the SandboxSettings card needs to
// render its Connect button correctly:
//
//   - When the chart's client_id is still the placeholder
//     (`PLACEHOLDER-AWAITING-FOUNDER-REGISTRATION` per PR #1619), the FE
//     renders the button as DISABLED with the amber "API pending" pill +
//     tooltip "Anthropic OAuth client_id not yet registered — contact
//     your Sovereign operator". This avoids letting the user click a
//     button whose OAuth URL would 400 at Anthropic.
//   - When the operator has set the real client_id, the FE shows the
//     button as enabled and POSTing /start returns the authorization URL
//     the popup opens.
//
// This endpoint is intentionally read-only and unauthenticated by virtue
// of sitting inside RequireSession — the response carries no secrets
// (the client_id itself is a public OAuth identifier; we just gate
// surface based on whether it's been registered yet).
func (h *Handler) HandleByosClaudeCodeConfig(w http.ResponseWriter, r *http.Request) {
	configured := byosClientID() != byosPlaceholderClientID
	resp := byosConfigResponse{ClientIDConfigured: configured}
	if configured {
		resp.OauthAuthorizeURL = byosAuthorizationURL()
	}
	writeJSON(w, http.StatusOK, resp)
}

// ──────────────────────────────────────────────────────────────────────
// Wave 1b helper — not yet called; documents the per-Sandbox user UID
// the Secret name uses. Sandbox-controller imports this constant from
// Wave 4 to keep the Secret-name convention in lock-step.
// ──────────────────────────────────────────────────────────────────────

// ByosSecretName returns the canonical Secret name for storing the
// user's encrypted Anthropic refresh token. The catalyst-api handler
// (Wave 4) and the sandbox-controller (Wave 4) both use this to
// construct the same name without a string-format drift.
//
// userUID is the Catalyst user uid (Claims.Sub) — stable across
// Keycloak group changes and email rename events.
func ByosSecretName(userUID string) string {
	// Replace characters that are illegal in K8s names. userUID is
	// almost always a UUID, but the helper is defensive.
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-':
			return r
		default:
			return '-'
		}
	}, strings.ToLower(userUID))
	return fmt.Sprintf("sandbox-byos-claude-code-%s", clean)
}
