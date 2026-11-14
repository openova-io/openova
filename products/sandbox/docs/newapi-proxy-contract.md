# Sandbox ↔ newapi proxy contract

**Status:** Design. Wave 1b deliverable.
**Created:** 2026-05-18.
**Implements:** the LLM-gateway side of `products/sandbox/docs/architecture.md` §3 / §5 / §6.

This document specifies how a Sandbox agent pod reaches the Sovereign's LLM gateway (`bp-newapi`), how the per-Sandbox API token is minted and rotated, and how provider routing is selected.

It is the contract between three components:

| Component | Owner | Wave |
|---|---|---|
| `sandbox-controller` (CRD reconciler) | this PR scaffolds the bridge; controller itself ships Wave 4 | Wave 4 |
| `bp-newapi` (LLM gateway) | upstream `Calcium-Ion/new-api`; Catalyst chart in `platform/newapi/` | Already shipped |
| `openova-sandbox-mcp` (sidecar in Sandbox pod) | this PR's design + Wave 2 implementation | Wave 2 |

---

## 1. Agent-pod environment

Every Sandbox-pod the controller creates carries these env vars. The agent binary (Claude Code / Cursor / Qwen Code / Aider / opencode) reads them through the OpenAI-compatible env convention `OPENAI_BASE_URL` + `OPENAI_API_KEY`.

```
LLM_GATEWAY_URL=https://newapi.<sov-fqdn>/v1
LLM_GATEWAY_TOKEN=<per-sandbox-token>
OPENAI_BASE_URL=https://newapi.<sov-fqdn>/v1   # alias for off-the-shelf agents
OPENAI_API_KEY=<per-sandbox-token>             # alias for off-the-shelf agents
```

The `OPENAI_*` aliases mean a stock `opencode`, `aider`, `claude --model gpt-4o`, or any LangChain/Vercel-AI-SDK app works out of the box without a Sandbox-specific config flag — the moat lives in *which* gateway URL these env vars point at, not in a custom protocol.

`<sov-fqdn>` is the Sovereign's primary FQDN (e.g. `acme.openova.io`, `<customer-sovereign-fqdn>`). `<per-sandbox-token>` is the PAT minted by the bridge described in §3 below.

---

## 2. Provider selection

newapi exposes a single OpenAI-compatible endpoint per *channel*. Each channel is configured by the Sovereign operator (see `platform/newapi/README.md` "Tier model"). The Sandbox agent picks a channel one of three ways:

| Picker | When used | Wire |
|---|---|---|
| **Default channel** | The agent makes a plain `POST /v1/chat/completions` with `model: <model>` — newapi picks the cheapest compliant channel that serves the model. | none |
| **Explicit provider hint** | The agent wants a specific channel (cheap vLLM / premium Anthropic / BYOK / sandbox-cheap). It sends `?provider=<channel-name>` on the request URL. newapi treats unknown channels as 404. | query param |
| **Per-session pin** | The user toggled "use my Max subscription" in the Sandbox settings (see `claude-code-byos.md`). The agent skips newapi entirely and talks to `api.anthropic.com` with the user's refresh-token-derived access token. | bypass |

Currently wired in production (verified against `platform/newapi/README.md` + the partner-hosted Qwen tenancy):

| Channel | Upstream | Notes |
|---|---|---|
| `qwen` (default) | Partner-hosted enterprise Qwen at an operator-supplied endpoint | The single channel wired today; default for every Sandbox until §4 lands |

Planned (no PR yet — Wave 4):

| Channel | Upstream | Notes |
|---|---|---|
| `vllm` | In-cluster `bp-vllm` | Cheap tier per Sovereign |
| `anthropic` | `api.anthropic.com` | Operator's commercial contract; requires `bp-newapi` channel attestation |
| `openai` | `api.openai.com` | Same constraint |
| `mistral` | `api.mistral.ai` | Same constraint |

Routing rule the bridge applies (Wave 4):

```
if request.query.provider:
  return route_to_channel(request.query.provider)
if request.body.model.startswith("claude-"):
  return route_to_channel("anthropic")
if request.body.model.startswith("qwen-"):
  return route_to_channel("qwen")
# Default channel = operator config; falls through to "qwen" today.
return route_to_default_channel()
```

The above is enforced inside newapi's existing channel-router; the bridge does not duplicate the logic.

---

## 3. Per-Sandbox token issuance

The sandbox-controller mints a per-Sandbox newapi token at Sandbox-Pod create time and rotates on Sandbox spec change. The flow:

```
┌────────────────────────┐    1. Sandbox CR created               ┌──────────────────────┐
│  user (catalyst-ui)    │ ──────────────────────────────────────▶│ sandbox-controller   │
└────────────────────────┘                                         └──────────┬───────────┘
                                                                              │ 2. POST /auth/pat
                                                                              │    body: {audience:"newapi", capabilities:[…]}
                                                                              ▼
                                                                   ┌──────────────────────┐
                                                                   │ core/services/auth   │
                                                                   │  IssuePAT (Wave 1b)  │
                                                                   └──────────┬───────────┘
                                                                              │ 3. signed JWT
                                                                              │    typ=pat, org_id=<slug>
                                                                              │    aud=newapi
                                                                              ▼
                                                                   ┌──────────────────────┐
                                                                   │ POST /admin/tokens/  │
                                                                   │   sandbox            │
                                                                   │ (platform/newapi/    │
                                                                   │  internal/handler/   │
                                                                   │  sandbox_token.go)   │
                                                                   └──────────┬───────────┘
                                                                              │ 4. newapi per-user-key
                                                                              │    (NewAPI's native admin API
                                                                              │    creates a user + key bound
                                                                              │    to OrgID + Sandbox UID)
                                                                              ▼
                                                                   ┌──────────────────────┐
                                                                   │ Secret/sandbox-<uid>/│
                                                                   │   newapi-token       │
                                                                   └──────────────────────┘
                                                                              │ 5. controller mounts as env
                                                                              ▼
                                                                   ┌──────────────────────┐
                                                                   │ Sandbox-Pod          │
                                                                   │ LLM_GATEWAY_TOKEN=…  │
                                                                   └──────────────────────┘
```

The bridge handler (`platform/newapi/internal/handler/sandbox_token.go`) is what links the Catalyst-issued PAT (cluster-internal identity) to a newapi-native API key (which is what newapi's metering layer measures against). The bridge:

1. **Validates the inbound PAT** against `JWT_SECRET` (same secret as `core/services/auth`).
2. **Asserts `typ=pat` + `aud=newapi`** so a generic session-cookie JWT can't mint a newapi key.
3. **Calls newapi's admin API** (`POST /api/user`, `POST /api/token`) to create a user bound 1:1 to the Catalyst `org_id` + `sandbox_uid`.
4. **Returns** the newapi-native key string (a NewAPI `sk-…` token) for the controller to store as a Secret.

The controller stores this in `Secret/sandbox-system/sandbox-<uid>-newapi-token` (keys: `token`, `newapi_user_id`, `expires_at`). On Sandbox `spec` change (e.g. quota bump) the controller calls the bridge again with `?rotate=true`; the bridge revokes the old NewAPI key + issues a new one + updates the Secret.

### Why two tokens instead of one

A simpler design would use the Catalyst PAT directly as the newapi auth header. We rejected that because:

- newapi's metering ledger (credits / per-key rate limits / billing log) is keyed by NewAPI's internal user_id. Using the PAT as the auth header would require newapi to re-key its ledger by `sub`, which is invasive.
- newapi's admin UI (the ops-staff surface at `admin.<host>`) lists keys with their NewAPI user id; collapsing identities would lose that audit surface.
- The PAT can outlive the Sandbox (e.g. the user keeps a Catalyst session open after deleting the Sandbox CR). Rotating the newapi-native key on Sandbox spec change cleanly invalidates exactly the key bound to that Sandbox without touching the user's session.

---

## 4. Token lifecycle + rotation

| Event | Action | Component |
|---|---|---|
| Sandbox CR `Created` | Bridge mints newapi key; controller writes Secret | sandbox-controller (Wave 4) |
| Sandbox `spec.repos[]` mutates | No-op (token unchanged) | — |
| Sandbox `spec.agentCatalogue[]` mutates | No-op (token unchanged) | — |
| Sandbox `spec.quota.*` mutates | Bridge `?rotate=true`; old key revoked | sandbox-controller |
| Sandbox CR `Deleted` | Bridge revokes the key; controller deletes Secret | sandbox-controller |
| Operator revokes via `/admin/tokens/sandbox/<uid>` | newapi key revoked immediately; Pod will 401 on next call → controller restarts Pod with new key | catalyst-api admin handler (Wave 4) |
| Sovereign-level newapi master rotation | All keys re-minted in batch (operator action) | catalyst-api admin handler (Wave 4) |

### Failure modes

- **newapi unreachable at Pod start** — Pod stays `Pending` with controller condition `LLMGatewayUnreachable`. The controller does not fail-open (no Sandbox without a token). This matches the global Sandbox principle: "no offline operation".
- **Token revoked while Pod is running** — first MCP tool call that hits `/v1/*` returns 401. The MCP server surfaces this as a card-protocol notification (`{kind: "auth-expired", action: "refresh"}`) and the agent retries after the controller mints a replacement. The interactive xterm session stays alive; only the in-flight LLM call is lost.
- **newapi 5xx during issuance** — bridge returns 503; controller backs off 30s and retries up to 5×; after that the Sandbox CR enters `Degraded` and surfaces in the Sovereign Console.

---

## 5. Authorization model

newapi is the *gateway*, not the *authoritative authn boundary*. The trust chain is:

```
Browser session ──→ catalyst-api ──→ Sandbox CR (org-scoped)
                                       │
                                       │ controller mints PAT(audience=newapi)
                                       ▼
                                   bridge ──→ newapi admin API
                                                    │
                                                    │ NewAPI key bound to OrgID
                                                    ▼
                                              Pod env LLM_GATEWAY_TOKEN
                                                    │
                                                    │ every LLM call
                                                    ▼
                                              newapi /v1/* → channel mux
```

newapi enforces:

- Per-key rate limit (configured per channel in newapi)
- Per-key credit balance (newapi ledger)
- Channel ACL (operator chooses which channels each key can use; defaults to "all enabled")
- Geographic AUP enforcement (`channels.<name>.aup` — see `platform/newapi/README.md`)

The MCP server (in the same Pod as the agent) additionally enforces:

- Per-call capability check against the PAT's `capabilities` claim (e.g. `sandbox:llm:premium`)
- Per-call quota debit against the Sandbox CR `spec.quota.tokensPerHour` (Wave 4)

The PAT's `org_id` claim is the cross-check that prevents Sandbox A's token being used to call as Sandbox B — newapi's admin API binds the key 1:1 to OrgID at mint time, and the MCP server refuses to forward a request whose context OrgID doesn't match.

---

## 6. Out of scope for Wave 1b

- The per-call quota debit path (Wave 4 — needs sandbox-controller).
- The provider-routing rule embedded in the bridge (Wave 4 — needs more than one channel to test against).
- The OpenAI/Anthropic/Mistral upstream channels (Wave 4+ — operator legal review).
- The newapi admin-UI surfacing of per-Sandbox keys (small change to NewAPI's upstream; tracked separately).
- A NetworkPolicy that pins Sandbox-Pod egress to `newapi.<sov>` only (Wave 4 — Cilium policy template).

---

## 7. References

- `platform/newapi/README.md` — chart contract, channel model, compliance posture
- `platform/newapi/internal/handler/sandbox_token.go` — bridge handler stub (this PR)
- `core/services/shared/auth/claims.go` — Catalyst access-token claim contract (this PR)
- `core/services/auth/handlers/pat.go` — PAT issuance endpoint (this PR)
- `products/sandbox/docs/architecture.md` §3 (MCP) + §5 (integration) + §6 (token prerequisite)
- `products/sandbox/docs/claude-code-byos.md` — BYOS bypass path (this PR)
