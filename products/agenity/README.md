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
  with the Org's Keycloak SSO (silent login). The agent's openova MCP calls
  catalyst-api with an **Org-scoped service bearer** projected into the pod as
  `OPENOVA_MCP_BEARER` (see the next section, #4276). The MCP verifies its own
  bearer against the seeded RS256 pubkey (`OPENOVA_MCP_RS256_PUBKEY_PEM`); a
  future enhancement may additionally forward the live User's per-call
  `_auth.token` from the chat UI, but the auto-seeded service bearer is what
  makes the agentic `create_application` journey work zero-touch today.

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

**Supplying the platform credential (the one founder action).** Set it **once on
the mothership** — not once per Sovereign:

- **Mothership (preferred, #4277).** Populate `CATALYST_ANTHROPIC_API_KEY` and
  `CATALYST_ANTHROPIC_CREDENTIALS_JSON` on the mothership catalyst-api (they
  arrive via its own `catalyst-openova-kc-credentials` Secret, which
  `catalyst-system/sovereign-anthropic-credentials` on the MOTHERSHIP
  source-wins into). Every Sovereign provisioned afterwards is seeded with no
  human step: the cloud-init kubeconfig postback runs
  `seedSovereignAnthropicCredentials`
  (`products/catalyst/bootstrap/api/internal/handler/sovereign_anthropic_seed_mothership.go`,
  called from `kubeconfig.go`'s `PutKubeconfig`), which creates
  `catalyst-system/sovereign-anthropic-credentials` on the new cluster — the
  same rail the #883 SMTP-relay credential rides. It refuses to ship a
  credential that is absent, key-only, malformed or expired, so a Sovereign is
  never handed a Secret that inspects as populated and fails at first use.
- **Per-Sovereign (the escape hatch, and the rotation seam).** Create or edit
  `catalyst-system/sovereign-anthropic-credentials` (keys `apiKey` /
  `credentialsJson`) on that Sovereign directly. The mothership seed **never
  overwrites an existing Secret**, so operator-supplied bytes always win, and
  this stays the way to re-seed the EXPIRING OAuth blob without a chart
  upgrade (#6163 reads it live — no catalyst-api roll needed).
- **Chart floor.** A per-Sovereign overlay setting
  `sovereign.anthropic.{apiKey,credentialsJson}` on bp-catalyst-platform is the
  lowest-precedence source; both seams above win over it.

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
Since #6163 FREEZE 3 the check is **no longer diagnostic-only**: an expired
credential takes the same verdict as an absent one under
`anthropic.onExpiredCredential` (`fail`|`continue`, default `fail`). It still
never mutates the blob.

### ✅ The platform now REFRESHES that credential automatically (#6317)

The manual re-seed described above was, until chart `0.5.31` /
`bp-catalyst-platform 1.4.1537`, a **standing per-Sovereign chore on a ~5h
timer**. It is no longer. `catalyst-api`'s seed reconciler renews the
credential itself, and three links were closed to make the renewal actually
reach a running agent:

| Link | What changed |
|---|---|
| **Renew** | `refreshAnthropicCredential` (`sovereign_anthropic_refresh.go`) exchanges the `refreshToken` at `console.anthropic.com/v1/oauth/token` when **less than 2h** of the accessToken's life remains — and also when it has already expired, since the refresh token outlives it by weeks. It runs on every 10m seed-reconcile pass, **before** the seed leg, so the same pass propagates the renewed value. |
| **Store** | The renewed pair is written to the **root Secret first** (`catalyst-system/sovereign-anthropic-credentials`) and then to OpenBao. Order matters: the exchange **rotates** the refresh token, so a value that reached OpenBao but not the root would leave the root holding spent material the reconciler could later re-apply. |
| **Deliver** | The ExternalSecret `refreshInterval` is **15m** (was `1h`), and a `creds-resync` sidecar copies a **newer** credential from `/creds` into the running Pod's `~/.claude/.credentials.json` every 2m — so a rotation reaches live agent spawns **with no pod restart** and no killed sessions. It copies only when the Secret's `expiresAt` is strictly greater, so a credential `claude-code` rotated for itself is never clobbered. |

Budget: a ~5h token, renewed with 2h to spare, against ~18m of worst-case
propagation (15m ESO + ~1m kubelet re-projection + 2m re-sync) — and twelve
renewal attempts inside the window at the 10m cadence.

`apiKey` is rewritten **only when it is byte-identical to the access token**
(which is how the seeded pair is issued). An independent long-lived
`sk-ant-api03-…` key an operator supplied on purpose is left alone.

**Failures are loud, never silent.** A refresh that cannot proceed logs at
ERROR with its remediation — `anthropic refresh IMPOSSIBLE` (no `refreshToken`
in the blob), `anthropic refresh FAILED` (the exchange), or
`anthropic refresh FAILED TO PERSIST` (the write, which also names that the old
refresh token has already been spent). A silent no-op leaving an expired token
in place is the one outcome the design forbids.

**Manual re-seed is still the remedy for the cases refresh cannot cover:** a
blob with no `refreshToken`, a malformed document, or a refresh token that has
itself expired or been revoked. Mint a **fresh** `credentialsJson` (a current
`claude` login on a workstation writes `~/.claude/.credentials.json`; copy that
whole blob) and rotate `catalyst-system/sovereign-anthropic-credentials` — the
#6163 live read picks it up on the next pass with **no catalyst-api roll**.

### The Sovereign-side verdict: absent, unusable and valid are three states (#6250)

Everything above is the per-Org (workspace) half. The producer half —
`seedAnthropicToken`, which writes this OpenBao path at Org-create and every ten
minutes thereafter — used to answer only **two** of the three questions an
operator can be in, and the missing one is the one a stale Sovereign is
usually in:

| Sovereign state | catalyst-api verdict | what the operator sees |
|---|---|---|
| **absent** — no credential configured | `skipped-no-env`, loud ERROR naming the Secret to create | correct, and correct since #4277 |
| **valid** — a `claudeAiOauth` blob that can authenticate | `seeded` | correct |
| **unusable** — configured and cannot authenticate | ~~`seeded`~~ → `unusable-credential-seeded` | **was reported as success** |

`unusable` covers three real operator states, each with its own remediation, so
the log line names which one it is:

- **key-only** — `apiKey` is set and `credentialsJson` is empty. `claude-code`
  authenticates from the OAuth blob, so this cannot spawn an agent at all.
- **malformed** — `credentialsJson` is not a `claudeAiOauth` document. The
  commonest shape is a bare `sk-ant-oat01-…` token pasted into that field.
- **expired** — the right document, an `accessToken` past its `expiresAt`.
  Given the hours-long lifetime described above, this is the **steady state**
  of a credential nobody rotated, not a rare edge. Remediation is **rotation**,
  not creation — which is why it must not share a message with `absent`.

Two consequences worth knowing before reading a log:

- The unusable value **is still written** to the OpenBao path, deliberately, so
  the per-Org ExternalSecret syncs and the workspace's own init container can
  diagnose from the real bytes rather than reporting a generic "credential never
  arrived". What changed is that catalyst-api no longer calls it seeded.
- The write is **withheld** when the stored path already holds a credential that
  works (outcome `unusable-credential-withheld`) — a fat-fingered rotation must
  not demote a Sovereign whose workspaces authenticate today.

The ten-minute self-heal loop asks the same question of what is **already
stored**: its health check is `credentialsJson` being present *and usable*, not
`apiKey` **or** `credentialsJson` being non-empty. Under the old check a path
holding an apiKey and an empty `credentialsJson` read healthy and the loop went
silent for the life of the cluster, while every Agenity workspace on the
Sovereign refused to start. A stored credential that cannot work now re-seeds if
a usable source exists, and otherwise keeps reporting
`[SEED-RECONCILE] 🛑 self-heal did NOT take` with the rotation remediation
attached.

> **Why not chart-seed it?** A Helm-seeded placeholder would pin an **empty**
> value forever under the reflector/ESO empty-seed trap (the bp-wordpress-tenant
> empty-password lesson) — the agent would then hold a permanently-blank
> credential. Absent-and-unseeded (the ExternalSecret renders, the key is
> simply missing) is the correct pre-seed state; the operator's one `bao kv put`
> is the activation.

## The openova-MCP Catalyst bearer (#4276 hop 7 — the create_application credential)

Seeding the Anthropic credential (above) only lets the spawned `claude-code`
talk to **Anthropic**. It does **not** give the agent a **Catalyst** identity to
call `create_application`. The openova MCP forwards a **session bearer** to
catalyst-api on every tool call, resolved from either a per-call `_auth.token`
argument **or** the `OPENOVA_MCP_BEARER` env. On a fresh funnel Org neither was
supplied, so every `create_application` returned **`-32001 unauthenticated`** —
even with the Anthropic key seeded. #4276 closes that gap (hop 7) plus the
coupled verify-pubkey gap (hop 7b).

**Auto-seeded at Org-create (#4276).** The catalyst-api producer `seedMCPBearer`
(called from `runOrganizationPipeline`, right beside `seedAnthropicToken`):

1. **Mints an Org-scoped session JWT** via the handover signer
   (`SignCustomClaims`) — the byte-for-byte shape `HandlePinVerify` /
   `auth_org_handover` mint for an Org customer session: `tier=org-admin`,
   `role=openova-user`, `typ=session`, `org`+`org_id=<slug>`,
   `realm_access.roles=[org-admin]`, `iss=<CATALYST_PIN_ISSUER>`, RS256-signed.
   This resolves in the MCP to `(context=organization, tier=admin, org_id=<org>)`
   → `create_application` (`MinTier=Admin`) is allowed AND routes to the own-org
   `POST /api/v1/org/applications` path (#4116); cross-Org create stays
   forbidden. A sovereign-admin bearer would be **wrong** here (it 403s under the
   #4110 host-scope guard).
2. **Emits the matching RS256 verify pubkey** as PKIX **PEM** — the public half
   of the same signer, the exact format `OPENOVA_MCP_RS256_PUBKEY_PEM` parses
   (`x509.ParsePKIXPublicKey`). Bearer + verify-key travel together (gap 7b),
   sidestepping the JWK-vs-PEM cross-namespace mismatch the host
   `catalyst-handover-jwt-public` mirror (a JWK) would otherwise impose (#4228).
3. **Writes both** to the **per-Org** OpenBao path
   `secret/catalyst/agenity/<slug>/mcp-bearer` (properties `bearer` + `pubkeyPem`)
   — per-Org because the bearer carries the Org slug, unlike the cluster-shared
   Anthropic path. Same `catalyst/`-prefix write-policy reasoning as above.

The chart's `templates/externalsecret-mcp-bearer.yaml` pulls both into the
per-Org Secret `agenity-mcp-bearer`; the org-gitops emitter points
`openovaMCP.bearerSecret` + `openovaMCP.rs256PubkeySecret` at it so the
StatefulSet projects `OPENOVA_MCP_BEARER` + `OPENOVA_MCP_RS256_PUBKEY_PEM`. No
per-Org hand action — zero-touch by construction.

**TTL + re-mint.** A session JWT expires. The StatefulSet is long-lived, so the
bearer is minted with a **1-year** service-identity TTL and **re-seeded
idempotently on every Org reconcile** (`PutKVv2` overwrites — each reconcile
pushes the expiry window forward). This mirrors the #4303 Anthropic-blob
re-seed and the OAuth-expiry pre-flight class #4111 documents.

> **Why not chart-seed the bearer/pubkey?** Same reflector/ESO empty-seed trap:
> a Helm placeholder would pin an empty bearer forever. The values default
> `mcpBearer.externalSecret.enabled=false` (no producer outside a Sovereign
> Org-pipeline); the emitter enables it with the per-Org `remoteKey`.

## What the fresh-prov walk should see

The full live e2e walk runs in the orchestrator's wipe → prov → walk loop.
On a fresh prov, after a User installs bp-agenity:

1. `kubectl -n <org>-prod get statefulset,svc,httproute,externalsecret -l catalyst.openova.io/blueprint=bp-agenity` → all present; the StatefulSet pod becomes `Ready` (`/healthz` 200).
2. The Agenity pod env carries `CHEPHERD_DEFAULT_MODEL=claude-opus-4-7` and `CHEPHERD_EXTRA_MCP_JSON` with the `openova` server stanza. In OAuth mode (`anthropic.credentialsKey` set, the default) the spawned agent authenticates from the init-seeded `~/.claude/.credentials.json` blob — `ANTHROPIC_API_KEY` is deliberately **omitted** (an `sk-ant-oat01-…` OAuth token in that env is rejected by `claude-code`, and it would shadow the valid `credentials.json`); only key-only mode (no `credentialsKey`) sets `ANTHROPIC_API_KEY` from the Secret. The `seed-claude-creds` init log states whether the seeded OAuth token is valid or expired.
3. The Agenity console loads at `agenity.<org>.<sovereign-fqdn>` (SSO).
4. Spawning the solo agent → its `.mcp.json` lists **both** the `chepherd` runtime MCP and the `openova` MCP servers, and the agent was launched with `--model claude-opus-4-7`.
5. Asking the agent to install an app → a new `Application` CR appears in the User's Org namespace, created via the openova MCP `create_application` tool (tier-admin-gated, Org-pinned).
