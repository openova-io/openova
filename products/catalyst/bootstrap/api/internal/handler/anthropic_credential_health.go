// anthropic_credential_health.go — #5956. VALIDITY classification for the
// Sovereign's Anthropic credential.
//
// THE DEFECT this file closes:
//
//	On hw292 the agentic runtime was dead — every inference returned
//	`401 OAuth access token has been revoked` — while FOUR independent
//	surfaces reported green:
//
//	 1. the ExternalSecret `uatco/agenity-anthropic-token` read
//	    `Ready=True reason=SecretSynced "Secret was synced"`. ESO answers
//	    "did the bytes arrive", never "are the bytes any good";
//	 2. the chart's #4111 `seed-claude-creds` pre-flight logged, verbatim,
//	    `claude-code OAuth token valid (~7h remaining).` — it compares
//	    `claudeAiOauth.expiresAt` against the wall clock and NOTHING else,
//	    so a token that is unexpired-but-revoked reads as valid, and it is
//	    boot-only so it never re-checks;
//	 3. the runtime's own `/api/v1/runtime/claude-status` returned
//	    `"logged_in": true`;
//	 4. THIS repo's #4877 seed reconciler — the one periodic surface —
//	    called openbaoPathHasProperty(), whose contract is
//	    "path exists AND at least one property holds a non-empty string".
//	    A revoked token is a perfectly non-empty string, so the pass
//	    concluded "healthy — no churn" every 10 minutes, forever.
//
//	All four ask about DELIVERY. None ask about VALIDITY. That is the
//	whole bug: expiry is a necessary condition for a working credential,
//	never a sufficient one, and a credential can be killed upstream at any
//	instant without a single local byte changing.
//
// THE INVARIANT (the only thing that makes this file worth having):
//
//	`AnthropicCredentialHealthy` is returned ONLY on a positive
//	authenticated response from the Anthropic API. Every other path —
//	expired, rejected, malformed, endpoint unreachable, no probe client —
//	returns a NOT-healthy class. Absence of evidence is never evidence of
//	health. If this ever grows a branch that reports healthy without a
//	2xx/429 in hand, the fix has been undone.
//
// Why 429 counts as positive evidence: Anthropic authenticates BEFORE it
// rate-limits, so a 429 is proof the credential was accepted. A 401/403
// is proof it was not. Everything else is proof of nothing.
//
// Revoked vs invalid — the distinguishing control, measured on the live
// credential 2026-08-10: a bogus token comes back `... is invalid`, the
// seeded one came back `OAuth access token has been revoked`. So the API
// itself separates "never was a token" from "was a real token, killed
// upstream", and the operator cue differs (typo vs re-seed). We keep the
// two classes apart rather than folding both into "bad".
//
// Per docs/PRINCIPLES.md #10 (credential hygiene): this file never logs,
// returns or hashes-into-a-log any credential material — only the outcome
// class, byte lengths, and HTTP status codes.
package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// AnthropicCredentialHealth — terminal validity class of one Anthropic
// credential. Exactly one value, AnthropicCredentialHealthy, means "this
// credential can currently authenticate"; see OK().
type AnthropicCredentialHealth string

const (
	// AnthropicCredentialAbsent — no credential material at all.
	AnthropicCredentialAbsent AnthropicCredentialHealth = "absent"
	// AnthropicCredentialHealthy — the API accepted it. The ONLY class
	// that may be reported as working, and it requires a live 2xx/429.
	AnthropicCredentialHealthy AnthropicCredentialHealth = "healthy"
	// AnthropicCredentialExpired — claudeAiOauth.expiresAt is in the past.
	// Decided locally; no probe is issued because none can rescue it.
	AnthropicCredentialExpired AnthropicCredentialHealth = "expired"
	// AnthropicCredentialRevoked — a real token the API says was revoked
	// upstream (#5956). Unexpired, well-formed, delivered — and dead.
	AnthropicCredentialRevoked AnthropicCredentialHealth = "revoked"
	// AnthropicCredentialInvalid — the API rejected it and did NOT say
	// "revoked" (typo, truncation, wrong credential kind).
	AnthropicCredentialInvalid AnthropicCredentialHealth = "invalid"
	// AnthropicCredentialUnverified — the probe could not be completed
	// (no client, endpoint unreachable, 5xx, unexpected status). NOT
	// healthy: this is the class that says "we still do not know", which
	// is exactly what every pre-#5956 surface reported as green.
	AnthropicCredentialUnverified AnthropicCredentialHealth = "unverified"
)

// OK reports whether the credential is proven to authenticate right now.
// Deliberately a whitelist of one — a future class is not-healthy by
// default, so adding a state can never silently widen "green".
func (c AnthropicCredentialHealth) OK() bool { return c == AnthropicCredentialHealthy }

const (
	// anthropicAPIBaseEnv overrides the probe endpoint (tests, air-gapped
	// Sovereigns fronting an internal gateway). Per Principle #4 no host
	// is hardcoded without an override.
	anthropicAPIBaseEnv     = "CATALYST_ANTHROPIC_API_BASE"
	anthropicAPIBaseDefault = "https://api.anthropic.com"
	// anthropicAPIVersion — required header on every Anthropic API call.
	anthropicAPIVersion = "2023-06-01"
	// anthropicOAuthBeta — the beta header claude-code sends when it
	// authenticates with an `sk-ant-oat…` OAuth access token rather than
	// an `sk-ant-api…` key. Without it an OAuth token is rejected even
	// when it is live, which would misclassify a healthy credential.
	anthropicOAuthBeta = "oauth-2025-04-20"
	// anthropicOAuthTokenPrefix distinguishes an OAuth access token from a
	// bare API key. They travel in DIFFERENT headers.
	anthropicOAuthTokenPrefix = "sk-ant-oat"
	// anthropicProbeTimeout bounds one probe. The reconciler must never
	// block on a hung endpoint.
	anthropicProbeTimeout = 10 * time.Second
	// anthropicRevokedMarker — the API's own wording for a token that was
	// real and was killed upstream, as opposed to `is invalid`.
	anthropicRevokedMarker = "revoked"
)

// anthropicOAuthBlob — the shape of the credentials.json the chart's
// seed-claude-creds init container materialises into
// ~/.claude/.credentials.json. Only the fields this file reasons about.
type anthropicOAuthBlob struct {
	ClaudeAIOauth struct {
		AccessToken string `json:"accessToken"`
		ExpiresAt   int64  `json:"expiresAt"` // epoch MILLIseconds
	} `json:"claudeAiOauth"`
}

// anthropicBearer picks the credential the runtime would actually
// authenticate with, and reports its expiry when it carries one.
//
// Order matters and mirrors the chart: when a credentialsJson blob is
// present the runtime authenticates with ITS accessToken and the chart
// omits ANTHROPIC_API_KEY entirely (statefulset.yaml — an OAuth token in
// ANTHROPIC_API_KEY makes claude-code fail "Invalid API key"). So probing
// the bare apiKey while the runtime uses the OAuth token would test a
// credential nothing consumes.
//
// 🛑 MEASURED 2026-08-10 on hw292 Secret uatco/agenity-anthropic-token:
// the `anthropicApiKey` property is BYTE-IDENTICAL to
// credentialsJson.claudeAiOauth.accessToken (both 108 bytes, both
// `sk-ant-oat…`, same SHA-256). The property NAME promises a long-lived
// API key and there is no such key on this Sovereign — it is a second
// copy of the same short-lived, revocable OAuth token. Anything that
// treats `apiKey` as a fallback for an expired/revoked OAuth token is
// reasoning about a credential that does not exist.
func anthropicBearer(apiKey, credsJSON string) (token string, expiresAtMillis int64) {
	credsJSON = strings.TrimSpace(credsJSON)
	if credsJSON != "" {
		var blob anthropicOAuthBlob
		if err := json.Unmarshal([]byte(credsJSON), &blob); err == nil {
			if t := strings.TrimSpace(blob.ClaudeAIOauth.AccessToken); t != "" {
				return t, blob.ClaudeAIOauth.ExpiresAt
			}
		}
	}
	return strings.TrimSpace(apiKey), 0
}

// anthropicCredentialFingerprint — a short, non-reversible digest used ONLY
// to answer "is the credential in openbao the same one the env holds?"
// without ever comparing or logging plaintext. Never sufficient to recover
// the credential; never logged next to any other credential material.
func anthropicCredentialFingerprint(apiKey, credsJSON string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey) + "\x00" + strings.TrimSpace(credsJSON)))
	return hex.EncodeToString(sum[:])[:12]
}

// classifyAnthropicCredential decides whether a credential can authenticate
// RIGHT NOW, and returns a short human detail for the log line.
//
// The order is deliberate:
//
//	absent → expired (local, cheap, and un-rescuable by any probe)
//	       → probe → healthy | revoked | invalid | unverified
//
// A nil client yields AnthropicCredentialUnverified, never healthy: "we
// could not check" must never render as "it works". That single rule is
// what separates this from the four surfaces #5956 documents.
func classifyAnthropicCredential(ctx context.Context, client *http.Client, base, apiKey, credsJSON string) (AnthropicCredentialHealth, string) {
	token, expiresAt := anthropicBearer(apiKey, credsJSON)
	if token == "" {
		return AnthropicCredentialAbsent, "no credential material (apiKey and credentialsJson both empty)"
	}

	// Expiry first: a probe cannot make an expired token live, and skipping
	// the call keeps a credless Sovereign off Anthropic's network entirely.
	if expiresAt > 0 {
		if now := time.Now().UnixMilli(); expiresAt <= now {
			return AnthropicCredentialExpired, fmt.Sprintf("claudeAiOauth.expiresAt is %dh in the past", (now-expiresAt)/3_600_000)
		}
	}

	if client == nil {
		// No probe seam wired ⇒ we know nothing about validity. The whole
		// point of #5956 is that this is NOT green.
		return AnthropicCredentialUnverified, "no probe client configured — validity unknown (expiry alone proves nothing, #5956)"
	}

	status, body, err := probeAnthropicCredential(ctx, client, base, token)
	if err != nil {
		return AnthropicCredentialUnverified, "probe transport error: " + err.Error()
	}
	switch {
	case status >= 200 && status < 300:
		return AnthropicCredentialHealthy, fmt.Sprintf("API accepted the credential (HTTP %d)", status)
	case status == http.StatusTooManyRequests:
		// Rate-limited AFTER authenticating — positive proof of validity.
		return AnthropicCredentialHealthy, "API accepted the credential (HTTP 429, rate-limited after auth)"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		if strings.Contains(strings.ToLower(body), anthropicRevokedMarker) {
			return AnthropicCredentialRevoked, fmt.Sprintf("API rejected the credential as REVOKED (HTTP %d) — re-seed required", status)
		}
		return AnthropicCredentialInvalid, fmt.Sprintf("API rejected the credential (HTTP %d, not reported as revoked)", status)
	default:
		return AnthropicCredentialUnverified, fmt.Sprintf("probe returned HTTP %d — validity unknown", status)
	}
}

// probeAnthropicCredential issues the cheapest authenticated call the API
// offers (GET /v1/models?limit=1) and returns the status plus a bounded
// slice of the body for revoked-vs-invalid discrimination.
//
// The header choice is credential-kind dependent: an `sk-ant-oat…` OAuth
// access token authenticates as a Bearer with the oauth beta header, a
// bare `sk-ant-api…` key as x-api-key. Sending the wrong one 401s a live
// credential and would manufacture a false "invalid".
func probeAnthropicCredential(ctx context.Context, client *http.Client, base, token string) (int, string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		base = anthropicAPIBaseDefault
	}
	ctx, cancel := context.WithTimeout(ctx, anthropicProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models?limit=1", nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	if strings.HasPrefix(token, anthropicOAuthTokenPrefix) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("anthropic-beta", anthropicOAuthBeta)
	} else {
		req.Header.Set("x-api-key", token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	// Bounded read: the error envelope is small and we only substring-match
	// it. Never logged verbatim beyond the classifier's own detail string.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, string(body), nil
}

// anthropicHealthClient returns the probe client. Production leaves the
// seam empty and gets a bounded-timeout client; tests inject a stub
// pointed at an httptest.Server.
//
// It never returns nil, so production can never silently fall into the
// "unverified because no client" branch — that branch exists for tests and
// for an explicitly disabled probe, not as a default.
func (h *Handler) anthropicHealthClient() *http.Client {
	if h != nil && h.anthropicHealthHTTPClient != nil {
		return h.anthropicHealthHTTPClient
	}
	return &http.Client{Timeout: anthropicProbeTimeout}
}

// anthropicAPIBase resolves the probe endpoint.
func (h *Handler) anthropicAPIBase() string {
	if h != nil && strings.TrimSpace(h.anthropicAPIBaseURL) != "" {
		return h.anthropicAPIBaseURL
	}
	if v := strings.TrimSpace(os.Getenv(anthropicAPIBaseEnv)); v != "" {
		return v
	}
	return anthropicAPIBaseDefault
}

// SetAnthropicHealthProbe wires the validity-probe seam. Production calls
// it with (nil, "") — or not at all — and gets the real api.anthropic.com
// with a bounded-timeout client; tests point it at a stub endpoint.
func (h *Handler) SetAnthropicHealthProbe(c *http.Client, baseURL string) {
	if h == nil {
		return
	}
	h.anthropicHealthHTTPClient = c
	h.anthropicAPIBaseURL = strings.TrimSpace(baseURL)
}
