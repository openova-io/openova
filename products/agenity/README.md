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

## What the fresh-prov walk should see

The full live e2e walk runs in the orchestrator's wipe → prov → walk loop.
On a fresh prov, after a User installs bp-agenity:

1. `kubectl -n <org>-prod get statefulset,svc,httproute,externalsecret -l catalyst.openova.io/blueprint=bp-agenity` → all present; the StatefulSet pod becomes `Ready` (`/healthz` 200).
2. The Agenity pod env carries `CHEPHERD_DEFAULT_MODEL=claude-opus-4-7`, `ANTHROPIC_API_KEY` (from the Secret), and `CHEPHERD_EXTRA_MCP_JSON` with the `openova` server stanza.
3. The Agenity console loads at `agenity.<org>.<sovereign-fqdn>` (SSO).
4. Spawning the solo agent → its `.mcp.json` lists **both** the `chepherd` runtime MCP and the `openova` MCP servers, and the agent was launched with `--model claude-opus-4-7`.
5. Asking the agent to install an app → a new `Application` CR appears in the User's Org namespace, created via the openova MCP `create_application` tool (tier-admin-gated, Org-pinned).
