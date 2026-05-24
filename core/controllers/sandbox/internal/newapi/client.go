// Package newapi is the thin HTTP client the sandbox-controller uses
// to mint per-Sandbox LLM-gateway tokens via the catalyst-api bridge
// handler shipped in PR #1638 — POST /admin/tokens/sandbox.
//
// Wire shape mirrors platform/newapi/internal/handler/sandbox_token.go
// EXACTLY (request fields org_id/user_id/sandbox_id/allowed_channels,
// response fields token/expires_at). If the handler's contract evolves
// both sides must change in the same PR — there is no schema
// generator between them on purpose: the bridge endpoint is the
// authoritative spec, this client is its only known caller, and a
// thin manual binding is easier to audit than yet-another generated
// surface.
//
// Per Inviolable Principle #4 (no hardcoded values) every operational
// knob (base URL, admin secret, HTTP timeout) is injected by the
// caller — no defaults baked into the package.
package newapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is the surface the sandbox-controller's reconciler depends
// on. Defined as an interface so the controller's unit tests can
// substitute an in-process stub without standing up a httptest.Server
// per case.
type Client interface {
	// MintSandboxToken POSTs the request to /admin/tokens/sandbox and
	// returns the issued bearer + its absolute expiry. Caller-supplied
	// context governs cancellation + the outbound HTTP deadline.
	MintSandboxToken(ctx context.Context, req MintRequest) (*MintResponse, error)
}

// MintRequest is the wire body for POST /admin/tokens/sandbox.
//
// Field names match handler.sandboxTokenRequest one-for-one — change
// them in lockstep with platform/newapi/internal/handler/
// sandbox_token.go.
type MintRequest struct {
	// OrgID is the parent Organization slug (e.g. "acme"). The handler
	// stamps this as the `org` claim on the minted JWT.
	OrgID string `json:"org_id"`

	// UserID is the Sandbox owner's stable identity — Keycloak sub or
	// owner email. Forwarded as `X-User-Id` on every NewAPI /v1/* call
	// for per-user billing-ledger attribution.
	UserID string `json:"user_id"`

	// SandboxID is the opaque per-Sandbox identifier the
	// sandbox-controller assigns. We pass the Sandbox CR's
	// metadata.uid — stable across spec mutations and 1:1 with a
	// rendered Pod's identity.
	SandboxID string `json:"sandbox_id"`

	// AllowedChannels is the list of NewAPI channels the issued token
	// is restricted to. Empty rejected with 400 by the handler.
	AllowedChannels []string `json:"allowed_channels"`

	// Capabilities is the MCP capability allowlist the issued token's
	// `capabilities` claim carries. Sourced from the Sandbox CR's
	// spec.capabilities (falling back to the plan→capabilities map via
	// sandboxapi.ResolveCapabilities). Encoded by the bridge handler
	// as the JWT `capabilities` claim which `Claims.HasCapability`
	// reads on every MCP tool call. Wildcards (`sandbox.db.*`) are
	// supported by the matcher so this list can carry coarse grants.
	// Empty list is allowed (downgrades the token to the introspection
	// surface only, matching a pre-PR-#1671 token).
	Capabilities []string `json:"capabilities,omitempty"`
}

// MintResponse is the wire body for the 200 OK reply.
type MintResponse struct {
	// Token is the HS256-signed JWT the Sandbox Pod presents to NewAPI
	// on every /v1/* call as the bearer credential.
	Token string `json:"token"`

	// ExpiresAt is the absolute expiry instant of Token (RFC3339).
	ExpiresAt time.Time `json:"expires_at"`
}

// HTTPClient is the net/http.Client subset we need — narrowed for
// dependency-injection in tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// liveClient is the production implementation. Constructed with a
// pre-configured *http.Client + the shared admin bearer + base URL.
type liveClient struct {
	baseURL     string
	adminSecret string
	http        HTTPClient
}

// New returns a live client. baseURL is the catalyst-api root the
// bridge handler is mounted on (e.g.
// "http://newapi.newapi.svc.cluster.local:8080" post-Wave-5.57). adminSecret
// is the value of NEWAPI_ADMIN_SECRET — chart-emitted by the
// newapi-token-signing-key Secret. httpClient may be nil; in that
// case a default 60s-timeout client is used.
//
// Wave 5.57e (#2303, 2026-05-24): timeout bumped 30s -> 60s. On hw01
// the sandbox-bridge sidecar cold-start can exceed 30s (distroless
// pull from GHCR over Huawei egress + Go binary start + first :8080
// listen). Pre-fix, sandbox-controller hit context-deadline on every
// fresh-Pod reconcile, surfaced as TokenMintFailed even though the
// bridge was about to become ready. 60s gives one full kubelet pull-
// retry budget without breaking idempotent re-reconcile semantics.
//
// Returns an error when baseURL or adminSecret is empty so the
// controller fails-loud at process start rather than shipping a
// no-op token-mint path.
func New(baseURL, adminSecret string, httpClient HTTPClient) (Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, errors.New("newapi.New: base URL is empty")
	}
	if strings.TrimSpace(adminSecret) == "" {
		return nil, errors.New("newapi.New: admin secret is empty")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &liveClient{
		baseURL:     baseURL,
		adminSecret: adminSecret,
		http:        httpClient,
	}, nil
}

// MintSandboxToken implements Client.
//
// Failure modes surfaced as wrapped errors:
//   - request marshalling   — caller bug (programmer error)
//   - transport error       — retry-worthy
//   - non-2xx with body     — retry-worthy unless 4xx (configuration drift)
//   - response decode error — bridge contract violation (escalate)
func (c *liveClient) MintSandboxToken(ctx context.Context, req MintRequest) (*MintResponse, error) {
	if strings.TrimSpace(req.OrgID) == "" {
		return nil, errors.New("newapi: MintRequest.OrgID is required")
	}
	if strings.TrimSpace(req.UserID) == "" {
		return nil, errors.New("newapi: MintRequest.UserID is required")
	}
	if strings.TrimSpace(req.SandboxID) == "" {
		return nil, errors.New("newapi: MintRequest.SandboxID is required")
	}
	if len(req.AllowedChannels) == 0 {
		return nil, errors.New("newapi: MintRequest.AllowedChannels is empty")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("newapi: marshal request: %w", err)
	}

	url := c.baseURL + "/admin/tokens/sandbox"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("newapi: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.adminSecret)
	// Wave 15 (PR #1674 follow-up) — stamp the tool header so the
	// bridge handler's `newapi_admin_token_mint_requests_total{tool,status}`
	// counter attributes mints to this controller. Header value must
	// match the dashboard's tool="sandbox-controller" panel filter.
	httpReq.Header.Set("X-Catalyst-Tool", "sandbox-controller")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("newapi: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		// Surface bridge error verbatim — operator log diagnoses the
		// difference between 401 (admin secret rotated out of sync),
		// 400 (controller sent malformed request) and 5xx (bridge
		// outage). Body capped to 512 bytes for sanity.
		snip := respBody
		if len(snip) > 512 {
			snip = snip[:512]
		}
		return nil, fmt.Errorf("newapi: POST %s: status %d: %s",
			url, resp.StatusCode, string(snip))
	}

	var out MintResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("newapi: decode response: %w", err)
	}
	if out.Token == "" {
		return nil, errors.New("newapi: response missing token")
	}
	if out.ExpiresAt.IsZero() {
		return nil, errors.New("newapi: response missing expires_at")
	}
	return &out, nil
}
