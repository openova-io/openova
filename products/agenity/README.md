# bp-agenity — Agenity as an OpenOva Application Blueprint (#4010)

**What it is.** **Agenity** is a multi-agent runtime + dashboard, built on the
upstream [agenity](https://github.com/agenity-org/agenity) (chepherd) daemon. **bp-agenity**
packages it as an installable OpenOva **Application Blueprint**: a User installs
it into their own **Organization** from the catalog, signs in to Claude inside
the workspace with the **Organization's own plan**, then chats with its
built-in **solo agent** (Claude Opus 4.7). The agent reaches the **OpenOva MCP**
— with a short-lived tool identity of that Organization — to **create more
Applications in the Organization** on the User's behalf.

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
   --headless` as the account `agenity`, a `Service`, the sign-in gate at
   `agenity.<org>.<pool>`, CSI-backed PVCs.
3. The User opens the Agenity console and **signs in to Claude** with the
   Organization's own plan (see [Signing in](#signing-in-the-organizations-own-plan)).
   The sign-in stays on the workspace's persistent volume.
4. The User **chats** with the solo agent — Claude **Opus 4.7** — and asks it
   to "install WordPress in my org". The agent calls the
   **`create_application`** tool on the **openova MCP**, which forwards the
   workspace's tool identity to catalyst-api `POST /api/v1/org/applications`.
   The tool needs tier admin, and the identity has it only while it acts for
   a person who holds admin or owner in the Organization (see
   [The tool identity](#the-tool-identity-an-agent-token-of-at-most-one-hour)).
   The target Org is **pinned to the Organization's own Org**.
5. A new Application appears in the Org, created by the agent.

## How the solo agent is wired

| Concern | Wiring |
|---|---|
| **Model = Claude Opus 4.7** | `agent.model: claude-opus-4-7` → `CHEPHERD_DEFAULT_MODEL` env → the runtime passes `--model claude-opus-4-7` to the spawned `claude-code` (chepherd #4010 default-model fallback). |
| **Model credential** | None in the chart. A person signs in inside the workspace; Claude Code keeps the sign-in in `~/.claude/.credentials.json` on the persistent volume and refreshes it there itself. No Secret, no ExternalSecret, no `ANTHROPIC_API_KEY`, nothing copied from the Sovereign. |
| **Daemon lookup** | `CHEPHERD_CLAUDE_CREDS_PATH` points the daemon at a placeholder with no refresh token, so the daemon never refreshes the sign-in itself (see below). |
| **Solo (no supervisor)** | `agent.solo: true` → `chepherd run --no-shepherd` — a single worker agent the User chats 1:1 with. |
| **openova MCP** | `CHEPHERD_EXTRA_MCP_JSON` (set by the chart) merges an `openova` MCP-server stanza into **every** spawned agent's `.mcp.json` (chepherd #4010 merge seam). A small launcher hands the tool server catalyst-api's signer key set and starts the bundled `/usr/local/bin/openova-mcp`. |
| **Tool identity** | The tool server reads its bearer from `/var/run/agenity-tools/bearer` (`OPENOVA_MCP_BEARER_FILE`) at every call; the `tool-identity` sidecar renews that file. |
| **Tool search** | `agent.toolSearch: false` sets `ENABLE_TOOL_SEARCH=false`, so the model is offered the openova tools up front, not only a search tool. |

## openova MCP — RBAC-scoped app creation

The agent's app-creation tool is the **`create_application`** tool of
[`products/openova-mcp`](../openova-mcp/README.md) (#4010 — the first
write/mutating MCP tool):

- **Tier-gated** (`MinTier=admin`). An agent token has tier developer, so a
  token without an acting person never even *sees* the tool. A token that
  acts for a person of tier admin has tier admin at the tool server, and
  catalyst-api lets it make this one mutation, `POST /api/v1/org/applications`,
  and no other (AIP-154).
- **Org-pinned** — in Organization context the `organizationRef` is forced to
  the caller's own Org; an attempt to point the environment at another Org is
  denied (`ErrForbidden`).
- **Parity** — the MCP surfaces the upstream catalyst-api status verbatim
  (incl. 403), so catalyst-api's own checks are the final word.

## Configuration knobs (`configSchema` highlights)

| Knob | Default | Notes |
|---|---|---|
| `agent.model` | `claude-opus-4-7` | The Claude model the solo agent runs on. |
| `agent.solo` | `true` | Single solo agent (no shepherd). |
| `agent.toolSearch` | `false` | `false` offers the openova tools up front (`ENABLE_TOOL_SEARCH=false`). |
| `openovaMCP.enabled` | `true` | Inject the openova MCP into spawned agents, with its tool identity. |
| `openovaMCP.context` | `organization` | Pins the MCP to the User's Org (defense in depth). |
| `toolIdentity.serviceAccount` | `agenity` | The account the workspace runs as; catalyst-api admits it for its own Organization. |
| `toolIdentity.ttlSeconds` | `3600` | The bearer's life, 60 to 3600 s; renewed every third of it. |
| `toolIdentity.actingPerson` | `""` | The person the agent acts for; empty = the Organization's owner. |
| `persistence.state.size` | `5Gi` | Runtime state and the workspace home (CSI default class — **local-path forbidden**, #3971). |

## Image

`ghcr.io/openova-io/agenity` is the **upstream Agenity (chepherd) daemon**
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
  StorageClass** (leave `storageClass` empty). Never local-path (#3971). The
  state claim also holds the workspace home (folder `claude-home`).
- **Backups**: the state PVC holds session history and the Organization's
  sign-in; back it up with the Org's standard PVC backup policy, and treat the
  backup as holding a credential.
- **Multi-region**: single-region `singleton` only — Agenity is a
  per-Org control surface, not a replicated data plane.
- **Auth**: the Agenity console is reached through its sign-in gate with the
  Sovereign's Keycloak SSO (silent login). The agent's openova MCP calls
  catalyst-api with the tool identity below — never a long-lived bearer.
- **Egress**: the Pod reaches `api.anthropic.com` (the model, and the profile
  read at every start), `platform.claude.com` (the refresh of the sign-in) and
  the public console host (tool calls and the token route), all on 443. The
  person's browser alone reaches `claude.com`, to sign in.

## Signing in: the Organization's own plan

The workspace carries no model credential of the Sovereign. A person signs in
once, inside the workspace, with the Organization's own Claude plan:

1. Open the Agenity console and start the sign-in from the daemon's sign-in
   screen; it shows a `claude.com` address.
2. Open that address in the browser, sign in with the Organization's plan, and
   paste the code back into the screen.
3. Claude Code writes the sign-in to `~/.claude/.credentials.json` (mode 600)
   on the persistent volume. That file is the only copy. Claude Code
   refreshes it at `platform.claude.com` under its own lock and writes the new
   pair back, so it survives a restart and needs no refresher of its own.

**The sign-in counts when the agent answers.** In this in-pod mode the
daemon's sign-in screen may report a failure (`504 credentials never
appeared`) although the sign-in succeeded: the screen looks for the file in a
place the in-pod agent does not use. Send the agent a message; if it answers,
the sign-in worked. The fix of that screen belongs to the daemon, outside this
repository.

**Why the daemon gets a placeholder.** Before each spawn the daemon checks for a
credential, and it refreshes one that expires within 60 s itself without
writing the new pair back. A refresh token works once, so that spent the
Organization's sign-in. The chart therefore points `CHEPHERD_CLAUDE_CREDS_PATH`
at a placeholder that expires in 2100 and has no refresh token: the daemon
finds a credential, never refreshes it, and the agent signs in from the home
file. Remove it once the daemon stops refreshing credentials it does not write
back.

The onboarding file `~/.claude.json` (first-run gates and the `openova` tool
server for a `claude` started by hand) is written only when it is absent:
once it exists, Claude Code owns it.

## The tool identity: an agent token of at most one hour

The openova MCP forwards an **agent token** to catalyst-api: one
Organization, the agent `agenity`, tier developer, a life of at most one hour
(`docs/contracts/model-contract.md` section 2).

- The workspace runs as the ServiceAccount `agenity`. Its token is not
  mounted into the workspace container, where the agent runs; a projected
  token is mounted only into the `tool-identity` containers.
- The `tool-identity` sidecar calls `POST /api/v1/internal/agent-tokens` with
  that projected token. catalyst-api's TokenReview names the account, and the
  default entry of `agentTokens.callers` (bp-catalyst-platform) admits the
  account `agenity` of every Organization namespace for that Organization
  only. The sidecar asks again every third of the token's life (30 s after a
  failure) and replaces `/var/run/agenity-tools/bearer` by rename; the tool
  server reads the file at every call, so a renewed bearer needs no restart.
  A failed renewal keeps the bearer it has.
- Each pass also reads catalyst-api's public signer key set,
  `GET /api/v1/auth/signers.jwks`, which the tool server verifies the bearer
  with. No Secret carries it.
- **The acting person.** The request names the Organization's owner (from
  `spec.owners` of the Organization object, read when the chart is installed,
  or `toolIdentity.actingPerson`). catalyst-api writes the claim
  `acting_person` only while that person holds admin or owner in the
  Organization, read at every issue; only such a token may call
  `create_application`. Without a person the token stays at developer and the
  agent can read, not create.
- A first pass runs as the init container `tool-identity-init`, so the first
  tool server already finds the key set and the bearer. It never blocks the
  Pod: when catalyst-api cannot be reached the sidecar keeps asking.

## What the fresh-prov walk should see

The full live e2e walk runs in the orchestrator's wipe → prov → walk loop.
On a fresh prov, after a User installs bp-agenity:

1. `kubectl -n <org> get statefulset,svc,serviceaccount -l catalyst.openova.io/blueprint=bp-agenity` → all present, and **no** `ExternalSecret` or `Secret` of the workspace; the Pod becomes `Ready` (`/healthz` 200).
2. The `tool-identity` log reads `renewed the tool bearer, valid until …`; `/var/run/agenity-tools/bearer` exists in the Pod.
3. The Agenity console loads at `agenity.<org>.<pool>` (SSO); a person signs in to Claude with the Organization's plan, and the agent answers.
4. Spawning the solo agent → its `.mcp.json` lists **both** the `chepherd` runtime MCP and the `openova` MCP servers, and the agent was launched with `--model claude-opus-4-7`.
5. Asking the agent to install an app → a new `Application` CR appears in the Org namespace, created via the openova MCP `create_application` tool, and catalyst-api's audit event `agent-token.issue` names the acting person.
