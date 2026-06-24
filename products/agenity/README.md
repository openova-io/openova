# bp-agenity — Agenity as an OpenOva Application Blueprint (#4010)

**What it is.** **Agenity** is a multi-agent runtime + dashboard, built on the
upstream [agenity](https://github.com/agenity-org/agenity) (chepherd) daemon. **bp-agenity**
packages it as an installable OpenOva **Application Blueprint**: a User installs
it into their own **Organization** from the catalog, then chats with its
built-in **solo agent** (Claude Opus 4.7, token pre-configured). The agent
reaches the **OpenOva MCP** — scoped to exactly what that User can do in the
console — to **create more Applications in the User's own Organization** on the
User's behalf.

**Role in Catalyst.** An **Application Blueprint** (not control plane). It
installs into an Organization's Environment like any other catalog app, and
is the missing piece of the north-star self-service journey
([`docs/ledger/UAT.md`](../../docs/ledger/UAT.md) rows 218–223):
coupon → Org → passwordless PIN → install Agenity → chat solo-agent →
agent creates apps via the MCP.

See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) for where Blueprints
sit in the model and [`docs/GLOSSARY.md`](../../docs/GLOSSARY.md) for the
Organization / Application / Blueprint canon.

> **Brand vs runtime.** *Agenity* is the OpenOva product/catalog brand. It
> packages the upstream *chepherd* multi-agent runtime daemon; the daemon's
> own contract — the `chepherd run` CLI, the `CHEPHERD_*` env vars, the
> `chepherd` MCP-server namespace — is passed through unchanged so the
> overlaid image keeps working. Only the product identity is rebranded.

## The journey this enables (UAT 218–223)

1. A User, signed into their Org (passwordless 6-digit PIN), opens the
   catalog and installs **Agenity** (this Helm chart).
2. The Application provisions: a `StatefulSet` running `chepherd run
   --headless`, a `Service`, an `HTTPRoute` at
   `agenity.<org>.<sovereign-fqdn>`, CSI-backed PVCs.
3. The User opens the Agenity console and **chats** with the solo agent —
   Claude **Opus 4.7**, whose Anthropic token is already wired from a Secret.
4. The User asks the agent to "install WordPress in my org". The agent calls
   the **`create_application`** tool on the **openova MCP**, which forwards
   the User's session bearer to the live catalyst-api `POST
   /api/v1/sovereigns/{id}/applications`. RBAC-scoped: the tool is
   tier-admin-gated and the target Org is **pinned to the User's own Org** —
   the agent can never create apps in another Organization.
5. A new Application appears in the User's Org, created by the agent.

## How the solo agent is wired

| Concern | Wiring |
|---|---|
| **Model = Claude Opus 4.7** | `agent.model: claude-opus-4-7` → `CHEPHERD_DEFAULT_MODEL` env → the runtime passes `--model claude-opus-4-7` to the spawned `claude-code` (chepherd #4010 default-model fallback). |
| **Anthropic token (pre-configured)** | `ANTHROPIC_API_KEY` env sourced from the `agenity-anthropic-token` **Secret** (an `ExternalSecret` pulls `sk-ant-…` from the Org's openbao by default — **never hardcoded**, per Inviolable Principle #4). The runtime seeds its vault `anthropic-api` provider from this env; the agent inherits it at spawn. |
| **Solo (no supervisor)** | `agent.solo: true` → `chepherd run --no-shepherd` — a single worker agent the User chats 1:1 with. |
| **openova MCP** | `CHEPHERD_EXTRA_MCP_JSON` (set by the chart) merges an `openova` MCP-server stanza into **every** spawned agent's `.mcp.json` (chepherd #4010 merge seam), pointing at the bundled `/usr/local/bin/openova-mcp` binary. The MCP holds **no privileged token** — it forwards the caller's session bearer to the live catalyst-api, so the endpoint's own authz is the final word. |

## openova MCP — RBAC-scoped app creation

The agent's app-creation tool is the **`create_application`** tool added to
[`products/openova-mcp`](../openova-mcp/README.md) (#4010 — the first
write/mutating MCP tool):

- **Tier-gated** (`MinTier=admin`) mirroring the endpoint's
  `applicationInstallCallerAuthorized` (tier-admin/owner/sovereign-admin), so
  a viewer/developer agent never even *sees* the tool.
- **Org-pinned** — in Organization context the `organizationRef` is forced to
  the caller's own Org; an attempt to point the environment at another Org is
  denied (`ErrForbidden`).
- **Parity** — the MCP surfaces the upstream catalyst-api status verbatim
  (incl. 403), so the agent's reach == the User's console reach.

## Configuration knobs (`configSchema` highlights)

| Knob | Default | Notes |
|---|---|---|
| `agent.model` | `claude-opus-4-7` | The Claude model the solo agent runs on. |
| `agent.solo` | `true` | Single solo agent (no shepherd). |
| `anthropic.secretName` | `agenity-anthropic-token` | Secret holding `sk-ant-…`. |
| `anthropic.externalSecret.enabled` | `true` | Pull the token from openbao via ESO; set `false` to pre-create the Secret out-of-band. |
| `openovaMCP.enabled` | `true` | Inject the openova MCP into spawned agents. |
| `openovaMCP.context` | `organization` | Pins the MCP to the User's Org (defense in depth). |
| `persistence.state.size` | `5Gi` | Agenity runtime-state PVC (CSI default class — **local-path forbidden**, #3971). |

## Image

`ghcr.io/openova-io/bp-agenity` is the **upstream Agenity (chepherd) daemon**
— built from the PUBLIC [`agenity-org/agenity`](https://github.com/agenity-org/agenity)
source — with the **`openova-mcp`** binary baked in at `/usr/local/bin/openova-mcp`
(see [`Containerfile`](Containerfile) + the
[`agenity-build.yaml`](../../.github/workflows/agenity-build.yaml) workflow).
The build context is the repo root because the openova-mcp module imports
`core/services/shared/auth` from this repo. `AGENITY_REF` pins the public
agenity daemon source ref the Containerfile builds from (#4097) — the old
private base image (`ghcr.io/agenity-org/chepherd`) is gone.

## Operational notes

- **Storage**: state + per-agent repo PVCs use the cluster's **default CSI
  StorageClass** (leave `storageClass` empty). Never local-path (#3971).
- **Backups**: the Agenity state PVC holds session history + the credential
  vault; back it up with the Org's standard PVC backup policy.
- **Multi-region**: single-region `singleton` only — Agenity is a
  per-Org control surface, not a replicated data plane.
- **Auth**: the Agenity console is reached via the Cilium Gateway HTTPRoute
  with the Org's Keycloak SSO (silent login). The agent presents the User's
  session bearer to the openova MCP on each `tools/call`.

## Seeding the `catalyst/anthropic/token` openbao path (#4277 / #4111 / #4228)

The chat runtime (the spawned `claude-code` solo agent) needs a **live
Anthropic credential** in the Sovereign's openbao. The chart's
`templates/externalsecret-anthropic.yaml` reads it from the openbao path
`secret/catalyst/anthropic/token` via the `vault-region1` ClusterSecretStore
into the `agenity-anthropic-token` Secret. The chart can **never** carry the
secret itself (Inviolable Principle #4 — no hardcoded secrets).

**Auto-seeded at Org-create (#4277).** The catalyst-api producer
`seedAnthropicToken` (called from `runOrganizationPipeline`) writes
`secret/catalyst/anthropic/token` on **every** Org-create, sourcing the value
from its own env (`CATALYST_ANTHROPIC_API_KEY` /
`CATALYST_ANTHROPIC_CREDENTIALS_JSON`, wired via the
`catalyst-openova-kc-credentials` Secret). So once the **platform-level**
Anthropic credential is supplied **once per Sovereign**, every new Org is
zero-touch by construction — no per-Org `bao kv put`.

> **Why `catalyst/anthropic/token` and not bare `anthropic/token`?** A
> Sovereign has **no writer** for `secret/anthropic/*` — the `external-secrets`
> OpenBao role `vault-region1` reads with is read-only. The catalyst-api holds
> the write-capable `catalyst-api-write` policy scoped to
> `secret/{data,metadata}/catalyst/*`, so the producer writes under that
> prefix. The path is **cluster-shared** — one seed serves every Org's agenity
> install on that Sovereign.

**Supplying the platform credential (the one founder action).** Set it once per
Sovereign via either:
- the operator-rotatable `catalyst-system/sovereign-anthropic-credentials`
  Secret (keys `apiKey` / `credentialsJson`) — **source-wins**, so this is also
  the seam to re-seed the EXPIRING OAuth blob without a chart upgrade; or
- a per-Sovereign overlay setting `sovereign.anthropic.{apiKey,credentialsJson}`
  on bp-catalyst-platform.

Until it is supplied the dashboard still renders, but the seed **loud-skips**
(catalyst-api logs `anthropic seed: SKIPPED — platform Anthropic credential
unset`) and the agent reports *"runtime offline · 0 workers"* / *"no Claude
credential available"* on spawn. This is the correct pre-seed state (the
ExternalSecret renders, the key is simply absent) — never an empty seed.

A direct `bao kv put` (below) remains valid for a one-off manual seed / hot
re-seed of an already-running Sovereign.

Two properties live at that one path:

| openbao property | chart value | what it is |
|---|---|---|
| `apiKey` | `anthropic.externalSecret.remoteProperty` | the `sk-ant-…` bare key (key-only fallback) |
| `credentialsJson` | `anthropic.externalSecret.remoteCredentialsProperty` | the **full** `{"claudeAiOauth":{"accessToken":…,"refreshToken":…,"expiresAt":…,"scopes":[…]}}` blob — the channel the spawned `claude-code` actually authenticates with (#4111). A bare `sk-ant-oat01-…` OAuth token in `apiKey` alone is **rejected** by `claude-code`; seed `credentialsJson` for the OAuth journey. |

Seed it from inside the cluster (the openbao admin token is the Org's
in-vc-mgmt OpenBao root — never write the secret to a file on disk):

```bash
# exec into an in-cluster pod that can reach the region-1 OpenBao; export
# VAULT_ADDR + VAULT_TOKEN for that store, then:
bao kv put secret/catalyst/anthropic/token \
  apiKey='sk-ant-…' \
  credentialsJson='{"claudeAiOauth":{"accessToken":"…","refreshToken":"…","expiresAt":<ms>,"scopes":["user:inference"]}}'
```

ESO refreshes `agenity-anthropic-token` within `refreshInterval` (1h, or force
an immediate sync by annotating the ExternalSecret with
`force-sync=$(date +%s)`); the init container then seeds
`~/.claude/.credentials.json` from `credentialsJson` and the next agent spawn
authenticates. This path is **shared per-Sovereign** (not per-Org-namespaced) —
a single seed serves every Org's agenity install on that Sovereign. The
preferred durable seam is the platform credential the catalyst-api producer
auto-seeds (above); this `bao kv put` is a one-off / hot re-seed.

### 🛑 The OAuth access token EXPIRES — re-seed when the agent goes offline (#4111)

The `credentialsJson` blob is a **short-lived OAuth pair**: its `accessToken`
(`sk-ant-oat01-…`) has an `expiresAt` only **hours** out, and the headless
`claude-code` the chepherd BareExec path forks does **not** reliably refresh it
from the `refreshToken`. So a credential that worked at seed-time **goes stale
within hours** and every subsequent `+ spawn agent` then fails with
`401 Invalid authentication credentials` — surfacing in the dashboard as the
same *"runtime offline / 0 workers"* symptom as a never-seeded path. Confirmed
live on omantel.biz 2026-06-24: a blob seeded ~45h earlier was correctly shaped
(full `claudeAiOauth`, valid scopes, `subscriptionType: max`) yet every spawn
401'd because the token had long expired.

The chart's `seed-claude-creds` init container (chart `0.5.6`+) now **parses
`expiresAt` at pod start** and logs a loud, greppable line so the operator can
diagnose this without exec'ing into the pod:

```
🛑 WARNING (#4111): seeded claude-code OAuth token is EXPIRED (~45h ago) — the
   spawned agent will fail '401 Invalid authentication credentials' and the
   dashboard will show 'runtime offline / 0 workers'. RE-SEED openbao
   catalyst/anthropic/token with a FRESH credentialsJson and force-sync the ExternalSecret.
```

(A valid token instead logs `claude-code OAuth token valid (~Nh remaining).`)
The check is **diagnostic-only** — it never blocks the pod (the dashboard must
keep serving) and never mutates the blob (`claude-code` owns rotation).

**When the agent reports offline, re-seed:** mint a **fresh**
`credentialsJson` (a current `claude` login on a workstation writes
`~/.claude/.credentials.json`; copy that whole blob), `bao kv put
secret/catalyst/anthropic/token credentialsJson='…'` as above (or rotate the
`sovereign-anthropic-credentials` Secret so the next Org-create re-seeds it),
force-sync the ExternalSecret
(`kubectl annotate externalsecret agenity-anthropic-token
force-sync=$(date +%s) --overwrite`), and restart the StatefulSet pod (or wait
for the next restart) so the init container re-seeds the new blob. The next
spawn then authenticates. This is the standing **operator activation cost** of
the OAuth journey until upstream `claude-code` gains a reliable
non-interactive refresh — track it as a recurring per-Sovereign chore, not a
one-time seed.

> **Why not chart-seed it?** A Helm-seeded placeholder would pin an **empty**
> value forever under the reflector/ESO empty-seed trap (the bp-wordpress-tenant
> empty-password lesson) — the agent would then hold a permanently-blank
> credential. Absent-and-unseeded (the ExternalSecret renders, the key is
> simply missing) is the correct pre-seed state; the operator's one `bao kv put`
> is the activation.

## What the fresh-prov walk should see

The full live e2e walk runs in the orchestrator's wipe → prov → walk loop.
On a fresh prov, after a User installs bp-agenity:

1. `kubectl -n <org>-prod get statefulset,svc,httproute,externalsecret -l catalyst.openova.io/blueprint=bp-agenity` → all present; the StatefulSet pod becomes `Ready` (`/healthz` 200).
2. The Agenity pod env carries `CHEPHERD_DEFAULT_MODEL=claude-opus-4-7` and `CHEPHERD_EXTRA_MCP_JSON` with the `openova` server stanza. In OAuth mode (`anthropic.credentialsKey` set, the default) the spawned agent authenticates from the init-seeded `~/.claude/.credentials.json` blob — `ANTHROPIC_API_KEY` is deliberately **omitted** (an `sk-ant-oat01-…` OAuth token in that env is rejected by `claude-code`, and it would shadow the valid `credentials.json`); only key-only mode (no `credentialsKey`) sets `ANTHROPIC_API_KEY` from the Secret. The `seed-claude-creds` init log states whether the seeded OAuth token is valid or expired.
3. The Agenity console loads at `agenity.<org>.<sovereign-fqdn>` (SSO).
4. Spawning the solo agent → its `.mcp.json` lists **both** the `chepherd` runtime MCP and the `openova` MCP servers, and the agent was launched with `--model claude-opus-4-7`.
5. Asking the agent to install an app → a new `Application` CR appears in the User's Org namespace, created via the openova MCP `create_application` tool (tier-admin-gated, Org-pinned).
