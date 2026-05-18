# Sandbox — Claude Code BYOS (Bring-Your-Own-Subscription)

**Status:** Design. Wave 1b deliverable.
**Created:** 2026-05-18.
**Implements:** the "Use my Max subscription" path mentioned in `products/sandbox/docs/architecture.md` and the BYOS bypass in `products/sandbox/docs/newapi-proxy-contract.md` §2.

The Sovereign-side Sandbox typically routes every LLM call through `bp-newapi` so the operator can meter, audit, bill, and enforce AUP. BYOS is the explicit opt-in to *bypass* the gateway: the user attaches their personal Anthropic Max / Pro / Team subscription, and their Claude Code sessions talk directly to `api.anthropic.com` using a refresh-token-derived access token. This means:

- Operator absorbs zero cost for that user's Claude Code usage.
- Operator's compliance posture for that user's calls becomes "we don't see them" (audit log records "BYOS session" with no prompt/response content).
- User retains the rate-limit + model-access entitlements of their personal plan.

This document defines the OAuth flow, the secret storage, the per-session toggle, and the revocation path.

> **NO live OAuth client_id yet.** Anthropic OAuth requires registering an OAuth client at the Anthropic console (founder action — see §8). All chart values and code paths leave the client_id as `${ANTHROPIC_OAUTH_CLIENT_ID:-PLACEHOLDER-AWAITING-FOUNDER-REGISTRATION}`. Wave 4 flips this live.

---

## 1. UX

Sandbox Settings page (Sovereign Console — Wave 4 UI work) shows a Claude Code card:

```
┌──────────────────────────────────────────────────────────────┐
│  Claude Code                                                  │
│  ─────────────────────────────────────────────────────────── │
│  Default model: claude-sonnet-4-7                             │
│  Routing:        ◉ Sovereign gateway (newapi)                 │
│                  ○ My personal Anthropic subscription (BYOS)  │
│                                                               │
│  [ Connect Claude Max ]    ◀── opens Anthropic OAuth popup    │
└──────────────────────────────────────────────────────────────┘
```

After clicking *Connect Claude Max*:

1. A popup opens at `https://console.anthropic.com/oauth/authorize?...` with PKCE.
2. User signs into Anthropic and consents to `read:user` + `write:claude-code` scopes (placeholder scope names; finalized when Anthropic publishes the public OAuth spec).
3. Popup redirects back to `https://console.<sov-fqdn>/api/v1/sandbox/byos/claude-code/callback?code=...`.
4. catalyst-api exchanges the code for `{access_token, refresh_token, expires_in}`.
5. The refresh token is encrypted with the Sovereign's secret-store key and stored in `Secret/catalyst-system/sandbox-byos-claude-code-<user-uid>`.
6. The Settings card flips to:

```
┌──────────────────────────────────────────────────────────────┐
│  Claude Code                                                  │
│  ─────────────────────────────────────────────────────────── │
│  Connected as:  emrah@acme.com (Anthropic Max)                │
│  Routing:       ◉ My personal Anthropic subscription (BYOS)   │
│                 ○ Sovereign gateway (newapi)                  │
│                                                               │
│  [ Disconnect ]                                               │
└──────────────────────────────────────────────────────────────┘
```

### Per-session override

The xterm.js prompt at `/sandbox/<name>` carries a small toolbar:

```
[ Model: claude-sonnet-4-7 ▾ ]   [ Routing: BYOS ▾ ]   [ ⓘ tokens used ]
```

The user can flip *Routing* per session even when the default is BYOS. Useful for "test this prompt against the operator's discount tier" without disconnecting the BYOS account.

---

## 2. OAuth flow

```
┌──────────────────┐                ┌───────────────┐               ┌──────────────────┐
│ Browser          │                │ catalyst-api  │               │ Anthropic        │
│ (user tab)       │                │ /sandbox/byos │               │ console.         │
│                  │                │  /claude-code │               │ anthropic.com    │
└────────┬─────────┘                └───────┬───────┘               └─────────┬────────┘
         │ 1. POST /start                   │                                 │
         │ ───────────────────────────────► │                                 │
         │                                  │ generate PKCE verifier + state  │
         │                                  │ store {verifier,state} 10m TTL  │
         │ 2. {url, state}                  │                                 │
         │ ◄─────────────────────────────── │                                 │
         │                                  │                                 │
         │ 3. window.open(url)              │                                 │
         │ ─────────────────────────────────────────────────────────────────► │
         │                                  │                                 │
         │ 4. user signs in + consents      │                                 │
         │                                  │                                 │
         │ 5. redirect to /callback?code=…&state=…                            │
         │ ◄───────────────────────────────────────────────────────────────── │
         │                                  │                                 │
         │ 6. GET /callback?code=…&state=…  │                                 │
         │ ───────────────────────────────► │                                 │
         │                                  │ 7. POST /oauth/token            │
         │                                  │    client_id + code + verifier  │
         │                                  │ ──────────────────────────────► │
         │                                  │                                 │
         │                                  │ 8. {access, refresh, expires_in}│
         │                                  │ ◄────────────────────────────── │
         │                                  │                                 │
         │                                  │ 9. encrypt refresh + persist    │
         │                                  │    Secret/sandbox-byos-…        │
         │                                  │                                 │
         │ 10. redirect /sandbox?byos=ok    │                                 │
         │ ◄─────────────────────────────── │                                 │
         │                                  │                                 │
         │ 11. popup closes; settings page  │                                 │
         │     polls /status and flips card │                                 │
```

### Storage shape

`Secret/catalyst-system/sandbox-byos-claude-code-<user-uid>`:

| Key | Type | Description |
|---|---|---|
| `refresh_token` | string (encrypted with Sovereign KMS) | Anthropic-issued refresh token. Encrypted at rest. Never exposed via any API. |
| `access_token` | string (encrypted) | Latest access token. Refreshed lazily on Pod spawn (see §3). |
| `expires_at` | RFC3339 string | UTC absolute expiry of access_token. |
| `anthropic_account_email` | string (plaintext) | Surfaced on the Settings card. |
| `connected_at` | RFC3339 string | When the user first connected. |
| `last_used_at` | RFC3339 string | Last time a Sandbox pod injected this token. |

The Secret carries the standard Catalyst owner-reference + finalizer pattern so deletion via the Sandbox controller cascades cleanly.

---

## 3. Pod-spawn injection

When the sandbox-controller spawns a Claude Code Pod with BYOS enabled for the user, the controller:

1. Reads `Secret/sandbox-byos-claude-code-<user-uid>`.
2. If `expires_at < now + 5min`, calls Anthropic's `/oauth/token?grant_type=refresh_token` to mint a fresh access token; updates the Secret.
3. Injects the fresh access token as `ANTHROPIC_API_KEY` env var into the Pod.
4. Sets `ANTHROPIC_BASE_URL` to the empty string (default `api.anthropic.com`) — overriding any newapi-injected value. The Pod has NO `LLM_GATEWAY_URL` env when BYOS is the active routing.

The agent process (`claude`) sees a standard `ANTHROPIC_API_KEY` env and talks to `api.anthropic.com` directly. Zero protocol changes inside Claude Code itself; the moat is entirely in the env-var injection.

### Token refresh failures

If the refresh-token call returns 401 (user revoked at Anthropic), the controller:

1. Marks the Secret with `condition: AnthropicRevoked`.
2. Falls back to `LLM_GATEWAY_URL=https://newapi.<sov-fqdn>/v1` + the standard Sandbox PAT (per `newapi-proxy-contract.md`).
3. Surfaces a card-protocol notification to the user: `{kind: "byos-revoked", action: "reconnect"}`.

This ensures a revoked BYOS connection doesn't brick the Sandbox — it transparently downgrades to the Sovereign gateway.

---

## 4. Per-session toggle wire

When the user flips *Routing* on the session toolbar, the catalyst-ui POSTs:

```
POST /api/v1/sandbox/<name>/sessions/<id>/routing
  body: {mode: "byos" | "gateway"}
```

The catalyst-api forwards to the sandbox-controller, which:

1. Sends `SIGUSR1` to the existing Pod (Wave 4 — Claude Code currently has no signal-driven config reload; alternative: drain the session + spawn a new Pod with the alternate env).
2. The MCP server picks up the new env on next agent restart and chooses the right routing accordingly.

Per-session override does NOT mutate the user's default routing. The default lives on the user's Sandbox CR `spec.defaultRouting`; the per-session pick is ephemeral state in the session log.

---

## 5. Revocation

The user can revoke BYOS in two ways:

| Path | Effect |
|---|---|
| Sandbox Settings → **Disconnect** | catalyst-api calls Anthropic `POST /oauth/revoke` with the refresh token + deletes the Secret. Future Pod spawns fall back to the Sovereign gateway. |
| Anthropic Console → Revoke OAuth app | The next refresh-token call returns 401; the controller marks the Secret `AnthropicRevoked` and falls back to the gateway. The Sandbox Settings card shows "Disconnected — token revoked at Anthropic". |

In both cases the Secret is deleted (immediately for the first path; on next reconcile loop for the second).

The catalyst-api audit log records:

```json
{"kind": "sandbox.byos.connected", "user_uid": "...", "provider": "anthropic", "at": "..."}
{"kind": "sandbox.byos.revoked", "user_uid": "...", "provider": "anthropic", "by": "user|provider", "at": "..."}
```

---

## 6. Capability + RBAC

BYOS connection is a per-user action. The operator can disable it Sovereign-wide by setting:

```yaml
# clusters/<sovereign>/bootstrap-kit/sandbox.yaml
sandbox:
  byos:
    claudeCode:
      enabled: false   # default true
```

When disabled, the Settings card hides the *Connect Claude Max* button and the `POST /sandbox/byos/claude-code/start` endpoint returns 403 `byos disabled by operator policy`.

When enabled, BYOS is gated by the user's `sandbox:byos:claude-code` capability. Default-granted to every authenticated user; operator can pull it via the standard tier-role projection (Wave 4).

---

## 7. Chart wiring

`platform/catalyst/chart/values.yaml` (Wave 4 — not in this PR):

```yaml
sandbox:
  byos:
    claudeCode:
      enabled: true
      oauth:
        # Founder action: register an OAuth client with Anthropic and paste the
        # client_id here. See products/sandbox/docs/claude-code-byos.md §8.
        clientID: "${ANTHROPIC_OAUTH_CLIENT_ID:-PLACEHOLDER-AWAITING-FOUNDER-REGISTRATION}"
        # Anthropic's PKCE-only public clients don't require a client_secret;
        # if the OAuth product changes to confidential, wire via ExternalSecret
        # pointing at OpenBao.
        clientSecretSecretRef: ""
        scopes:
          - "read:user"
          - "write:claude-code"
        authorizationURL: "https://console.anthropic.com/oauth/authorize"
        tokenURL: "https://console.anthropic.com/oauth/token"
        revocationURL: "https://console.anthropic.com/oauth/revoke"
        # Callback always served by catalyst-api on the Sovereign primary
        # Console origin. Path is fixed; only the host varies per Sovereign.
        callbackPath: "/api/v1/sandbox/byos/claude-code/callback"
```

The catalyst-api Deployment receives these as env:

```
SANDBOX_BYOS_CLAUDE_CODE_ENABLED=true
SANDBOX_BYOS_CLAUDE_CODE_CLIENT_ID=PLACEHOLDER-AWAITING-FOUNDER-REGISTRATION
SANDBOX_BYOS_CLAUDE_CODE_AUTHZ_URL=https://console.anthropic.com/oauth/authorize
SANDBOX_BYOS_CLAUDE_CODE_TOKEN_URL=https://console.anthropic.com/oauth/token
SANDBOX_BYOS_CLAUDE_CODE_REVOKE_URL=https://console.anthropic.com/oauth/revoke
SANDBOX_BYOS_CLAUDE_CODE_SCOPES=read:user,write:claude-code
SANDBOX_BYOS_CLAUDE_CODE_CALLBACK_PATH=/api/v1/sandbox/byos/claude-code/callback
```

The handler refuses to issue an OAuth start URL when the client_id is still the placeholder, returning 503 with a clear message — the founder TODO in §8 is exactly the one knob that flips this live.

---

## 8. The one founder TODO

To flip BYOS live (after Wave 4 ships the controller + UI):

> **Register an OAuth client with Anthropic** at `https://console.anthropic.com/settings/oauth` (or the equivalent published endpoint at GA time) with:
> - **Type:** public PKCE client
> - **Redirect URI:** `https://console.<sov-fqdn>/api/v1/sandbox/byos/claude-code/callback` — one entry per Sovereign FQDN. For openova.io that's `https://console.openova.io/...`; for `acme.openova.io` add an additional entry.
> - **Scopes:** `read:user`, `write:claude-code` (or whatever Anthropic publishes when their OAuth product GAs)
>
> Paste the returned `client_id` into `clusters/<sovereign>/bootstrap-kit/sandbox.yaml` `sandbox.byos.claudeCode.oauth.clientID`. Roll the catalyst-api Deployment (Flux picks up the env change automatically).

No other founder action is needed.

---

## 9. Out of scope for Wave 1b

- The Settings UI card (Wave 4 — needs the catalyst-ui SettingsPage shell).
- The session-toolbar routing toggle (Wave 4 — needs xterm.js).
- The `SIGUSR1`-driven config reload (Wave 4 — alternative is Pod respawn).
- Cursor BYOS, Qwen Code BYOS, Aider BYOS — same pattern, different OAuth endpoints; each gets its own design doc when the vendor ships an OAuth product.

---

## 10. References

- `products/sandbox/docs/architecture.md` — overall Sandbox design
- `products/sandbox/docs/newapi-proxy-contract.md` §2 — the gateway path BYOS bypasses
- `products/catalyst/bootstrap/api/internal/handler/byos_claude_code.go` — handler stubs (this PR)
- `core/services/shared/auth/claims.go` — Catalyst access-token claim contract (this PR)
- Anthropic OAuth (as published — currently a private alpha; design tracks the public spec when it lands)
