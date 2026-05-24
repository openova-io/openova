# OpenOva-MCP integration spec — for chepherd-style third-party Blueprints

**Status**: Draft 1.0 · **Created**: 2026-05-24 · **Refs**: #2316 (chepherd lift), Waves 0.3.1-0.3.6 (sandbox-controller lessons)

## What this doc covers

Third-party agent-runtime Blueprints (chepherd is the first) need the openova-MCP server to give agents `gitea.* / sandbox.db.* / k8s.* / marketplace.* / sandbox.deploy.* / sandbox.stripe.*` tool access. This doc locks the contract between openova-MCP (owned by openova) and any consuming Blueprint.

## §1 — Sidecar bundle pattern (not separate Deployment)

**Rule**: openova-MCP runs as a SIDECAR container INSIDE the agent-runtime Pod, NOT as a separate Deployment.

**Why**: Wave 0.3.4 lesson (2026-05-20) — separate MCP Deployment crash-looped with EOF on startup because the binary reads `os.Stdin` and a Pod has no stdin pipe. The fix bundles the `openova-sandbox-mcp` binary INSIDE the runtime's container image at `/usr/local/bin/openova-sandbox-mcp` via multi-stage Dockerfile. The agent CLI spawns it as a subprocess via mcp.json config.

**Mount layout**:
```
runtime-pod/
├── runtime-container/
│   └── /usr/local/bin/openova-sandbox-mcp  (bundled at image build time)
└── (no sidecar container — MCP IS the runtime's subprocess)
```

For chepherd: lift the bundling pattern from `products/sandbox/pty-server/Dockerfile` (multi-stage: COPY --from=openova/sandbox-mcp /openova-sandbox-mcp /usr/local/bin/openova-sandbox-mcp).

## §2 — Environment contract

Every consuming Blueprint MUST emit these env vars on the runtime container:

| Env var | Source | Required? | Purpose |
|---|---|---|---|
| `SANDBOX_TOKEN` | `newapi-bp-newapi-token-signing-key` Secret, key `LLM_GATEWAY_TOKEN` | yes | Per-Sandbox-minted bearer the MCP presents on every marketplace.* call |
| `SANDBOX_JWT_SECRET` | `newapi-bp-newapi-token-signing-key` Secret, key `SIGNING_KEY` | yes | HS256 signing key for in-cluster MCP→catalyst-api auth |
| `SANDBOX_REPOS` | CSV of `spec.repos[].giteaRepo` slugs | yes | Auth scope for gitea.repos.* tools |
| `OPENOVA_SANDBOX_AGENTS_PATH` | overlay (default `/etc/openova/agents.yaml`) | no | Agent catalog override path |
| `SANDBOX_DEFAULT_AGENT` | overlay (chepherd renders per Sandbox.spec.agentCatalogue[0]) | no | Lazy-spawn target |
| `SANDBOX_RING_BUFFER_BYTES` | overlay (default 1 MiB, clamped 16 MiB) | no | PTY replay buffer size |

**Reflector mirror**: openova-side bp-newapi chart at Wave 1.4.31 set `sandboxTokenSigningKey.reflectorNamespaces: "catalyst-system,sandbox,sandbox-.*"` so emberstack/reflector mirrors the source Secret into every per-Sandbox namespace via regex. For bp-chepherd, extend the regex to also include `chepherd-.*` OR add `chepherd-system` explicitly. Spec:

```yaml
# In bp-chepherd's chart, request reflector annotation on source Secret
metadata:
  annotations:
    reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
    reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "chepherd-system,chepherd-.*"
    reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"
    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: "chepherd-system,chepherd-.*"
```

## §3 — mcp.json injection (agent-CLI discovery)

The runtime's pty-server StatefulSet mounts a ConfigMap at canonical agent config paths via subPath projections so each agent CLI auto-discovers the openova-MCP subprocess:

| Agent | Config path | Format |
|---|---|---|
| claude-code | `~/.claude.json` | `{"mcpServers": {...}}` |
| qwen-code | `~/.qwen/settings.json` | `{"mcpServers": {...}}` |
| cursor-agent | `~/.cursor/mcp.json` | `{"mcpServers": {...}}` |
| generic | `./.mcp.json` (cwd, picked up by most CLIs) | `{"mcpServers": {...}}` |

Canonical config body:

```json
{
  "mcpServers": {
    "openova": {
      "command": "/usr/local/bin/openova-sandbox-mcp",
      "args": [],
      "env": {
        "SANDBOX_TOKEN": "${SANDBOX_TOKEN}",
        "SANDBOX_JWT_SECRET": "${SANDBOX_JWT_SECRET}",
        "SANDBOX_REPOS": "${SANDBOX_REPOS}"
      }
    }
  }
}
```

Wave 0.3.3 (PR #2049) shipped this in openova; chepherd lifts the ConfigMap template + subPath projections from `products/sandbox/pty-server/internal/server/routes.go` and the chart at `platform/sandbox/chart/templates/configmap-mcp-config.yaml`.

## §4 — Tool namespace reservation

| Namespace | Owner | Notes |
|---|---|---|
| `gitea.*` | openova-MCP | Repos / PRs / branches / file content |
| `sandbox.db.*` | openova-MCP | Per-Sandbox SQLite KV |
| `k8s.read.*` | openova-MCP | Read-only kubectl-style queries via catalyst-api |
| `marketplace.*` | openova-MCP | NewAPI marketplace queries |
| `sandbox.deploy.*` | openova-MCP | gitops PR-to-Flux flow |
| `sandbox.stripe.*` | openova-MCP | Per-Org billing read/write |
| `chepherd.*` | chepherd MCP | spawn / assign / grant_channel / list / read_pane |

**Collision rule**: if chepherd ever needs a tool that overlaps an openova-MCP namespace, the chepherd-side WRAPS the openova call (delegates) — never re-implements. This keeps openova-MCP as the single source of truth for openova-internal state.

## §5 — Auth chain handshake

When bp-chepherd runs in a Sovereign, the operator's existing catalyst-api session cookie flows through to chepherd's WebSocket:

```
User browser
  ↓ __Host-catalyst_sess=<base64> cookie (HttpOnly, Secure, SameSite=Strict)
Cilium Gateway HTTPRoute (chepherd.<sov-fqdn>)
  ↓ auth-policy filter validates cookie via /api/v1/internal/whoami
  ↓ injects headers downstream:
  ↓   X-Catalyst-User: <email>
  ↓   X-Catalyst-Tier: owner|admin|developer|user
  ↓   X-Catalyst-Org: <org-slug>
chepherd pty-server WebSocket upgrade handler
  → reads X-Catalyst-* headers, NOT the cookie
  → enforces per-tier RBAC on spawn/grant/list
```

**Per-Sandbox JWT mint**: chepherd's session-create flow POSTs to the openova sandbox-bridge endpoint at `http://newapi-bp-newapi.newapi.svc.cluster.local:8080/admin/tokens/sandbox` (Wave 5.57 sidecar) with body `{org_id, user_id, sandbox_id, allowed_channels: ["qwen"]}` + Authorization `Bearer ${NEWAPI_ADMIN_SECRET}`. Response: `{token, expires_at}`. chepherd mounts `token` as the per-Sandbox `SANDBOX_TOKEN` env.

**Alternate path** (chepherd-bring-its-own JWT-mint): if chepherd v0.5 ships its own JWT-mint internal to the runtime, openova-side can deprecate the sandbox-bridge sidecar entirely. Founder decision pending.

## §6 — PVC contract

bp-chepherd Blueprint MUST render the per-Sandbox StatefulSet with 3 PVCs (preserves the "close laptop, open phone" Scene 6 semantics):

| Mount | PVC | Size default | Purpose |
|---|---|---|---|
| `/repo` | `<sandbox-id>-repo` | 5 Gi | Operator repo content (git clone target) |
| `/.claude-memory` | `<sandbox-id>-claude-memory` | 1 Gi | Agent memory dir (Wave 0.3.2 §SANDBOX_REPOS) |
| `/.cache` | `<sandbox-id>-cache` | 2 Gi | Build cache (npm/cargo/go/pip) |

StatefulSet (not Deployment) so PVC binding survives Pod restarts. Wave 0.3.4 lesson: keep the StatefulSet pod-template stable; the pty-server's in-memory session state is the ONLY ephemeral piece.

## §7 — NetworkPolicy ingress

Existing `bp-newapi` NetworkPolicy (post-Wave-5.57a) allows ingress from `traefik` + `catalyst-system` + listed via `allowedFromNamespaces` array. To add chepherd-namespace reach, operators extend per-Sovereign overlay:

```yaml
# Per-Sovereign overlay for bp-newapi
networkPolicy:
  ingress:
    allowedFromNamespaces:
      - catalyst-system
      - chepherd-system      # added by bp-chepherd Blueprint install
```

## §8 — Open questions for the chepherd team

1. Will chepherd v0.5 ship its own JWT-mint (deprecates openova sandbox-bridge)? **Founder pending.**
2. Will chepherd add `chepherd-system` namespace to bp-newapi's NetPol via a per-Sovereign overlay PR OR via the bp-chepherd Helm hook? **Need confirmation.**
3. Catalog submission tree — `thirdparty/chepherd/` in openova-io/openova monorepo OR separate openova-io/chepherd repo? **Founder pending.**

## §9 — Versioning

This spec versions in lockstep with openova-MCP. Breaking changes to env vars, mcp.json shape, or tool namespaces bump the major version + chepherd-side must update bp-chepherd pin. Additive changes bump minor.

| Version | Changes |
|---|---|
| 1.0 (this doc) | Initial spec — Waves 0.3.1-0.3.6 + Wave 5.57 sidecar codified |
