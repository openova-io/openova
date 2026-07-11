# openova-mcp — OpenOva MCP server (#3988, first vertical slice)

The **OpenOva MCP server** exposes the OpenOva product surface as MCP tools
whose surface is **RBAC-scoped per user**, so the tool set an agent sees is
**identical to what that user can do in the Catalyst console UI**. It is a
**thin facade**: it reimplements nothing — every data tool forwards the
caller's bearer to the **live catalyst-api**, so the endpoint's own authz
is the final word. Identity is the **single source-of-truth claim
contract** (`core/services/shared/auth.Claims`) — NOT a parallel auth
system.

This module is the **first concrete, live-testable slice** of the EPIC. It
ships:

1. **The MCP server core** (`cmd/openova-mcp`) — JSON-RPC 2.0 over one of two
   transports that share the SAME dispatch + auth core: **stdio** (default,
   the same proven transport the sandbox MCP uses) and an opt-in **HTTP/SSE**
   (Streamable-HTTP) transport (see [Transports](#transports) below).
2. **Per-relevant-Keycloak identity resolution** (`internal/identity`) —
   parses the bearer into `auth.Claims`, derives the realm **context**
   (Organization vs Sovereign), the **tier** (viewer<developer<operator<
   admin<owner<sovereign-admin), and the pinned **Org scope**.
3. **Two-layer RBAC** (`internal/tools`):
   - **Layer 1** — `tools/list` is filtered by `(context, tier)` so the
     agent's surface == the user's UI surface.
   - **Layer 2** — `tools/call` re-authorizes against the **same** identity,
     so a hand-crafted call to a filtered-out tool is still denied.
4. **Real, read-only, Org-scoped tools** backed by the live catalyst-api
   (`internal/catalystapi`):
   - `whoami` — the resolved identity the server sees (mirrors `/auth/me`).
   - `list_organizations` — `GET /api/v1/organizations`, Org-scoped.
   - `list_applications` — `GET /api/v1/sovereigns/{id}/applications`,
     Org-scoped (a user with rights only to Org X sees only Org X's apps).
   - `get_application` — `GET /api/v1/sovereigns/{id}/applications/{name}`,
     with a cross-Org defense-in-depth scope check.
   - `list_environments` — derived from the caller's Applications (Catalyst
     has no standalone list-environments REST endpoint; the console derives
     env partitions the same way, so the facade does too).
5. **The first WRITE tool** — `create_application` (UAT rows 221-223):
   - `create_application` — installs (creates) an Application in the caller's
     Org from a Blueprint by forwarding the canonical install-request body to
     `POST /api/v1/sovereigns/{id}/applications`
     (`HandleApplicationInstall`) — the **same** create seam the console
     Install button posts to, so the Org namespace is ensured and the
     Application CR is written by the catalyst-api **exactly once** (no
     duplicated namespace/CR logic).
   - **RBAC parity with the read side**: an Org-scoped caller may create
     **only** in their own Org (a cross-Org `organization` is denied with
     `ErrForbidden` → MCP 403, never reaching the backend); a sovereign-admin
     may create in any Org but **must** name the target Org explicitly. The
     tool is **admin-tier-gated** in `tools/list` (a viewer never sees it),
     mirroring the console Install gate, and a hand-crafted viewer call is
     re-denied at layer-2.

## How the thin-facade + RBAC parity holds

- The server holds **no privileged token**. Every backing call forwards the
  caller's compact JWT verbatim as `Authorization: Bearer …` (and as the
  `catalyst_session` cookie), so the catalyst-api `RequireSession` +
  per-handler tier check is the final word. When the endpoint returns 403,
  the MCP surfaces the **same** upstream status (parity).
- Org scoping is enforced twice: the endpoint scopes by the caller's
  session, and the facade additionally filters Application items to the
  caller's Org namespace (`<org>` / `<org>-<env_type>`), so a cross-Org
  leak cannot pass through even if the endpoint widened.

## Transports

The server speaks JSON-RPC 2.0 over one of two transports selected at startup.
Both run the **identical** resolve → two-layer-RBAC → thin-facade flow
(`core.handle` in `cmd/openova-mcp`) — the transport only owns the wire, never
the auth or dispatch logic.

- **stdio (default)** — NDJSON- or Content-Length-framed on stdin/stdout. This
  is the path `agenity` bakes and forks per session. The bearer is supplied per
  `tools/call`/`tools/list` via the `_auth.token` argument or `OPENOVA_MCP_BEARER`.
  Runs whenever no HTTP address is configured — **byte-for-byte unaffected** by
  the HTTP addition.
- **HTTP/SSE (opt-in, #3988 §5 / #899)** — the MCP **Streamable-HTTP** transport,
  enabled by setting `OPENOVA_MCP_HTTP_ADDR` (e.g. `:8080`) or passing
  `--http :8080`. Endpoints:

  | Method + path | Purpose |
  |---|---|
  | `POST /mcp` | a JSON-RPC request → JSON-RPC response (`application/json`); a notification → `202` |
  | `GET /mcp` | server→client SSE stream (`text/event-stream`); keep-alive today (no server-initiated messages in this slice) |
  | `GET /healthz`, `GET /readyz` | liveness/readiness for the chart probes |
  | `GET /` | JSON descriptor of the surface |

  **Auth:** every `/mcp` request MUST carry `Authorization: Bearer <jwt>`,
  validated through the SAME resolver the stdio path uses. An absent/invalid
  bearer is rejected at the transport with **HTTP 401** (+ `WWW-Authenticate`)
  before any dispatch. Application-tier RBAC is unchanged: a `tools/list` scope
  filter and a `tools/call` Org-scope re-auth (cross-Org `create`/`get` →
  **MCP 403**, the JSON-RPC error `-32003` with `data.status: 403`, inside a
  `200`) hold exactly as on stdio. The listener is a plain in-Pod address; the
  front door is the Cilium Gateway HTTPRoute — **never a NodePort**.

  This is the server the `bp-openova-mcp` chart's `httpTransport.enabled` path
  (PR #4981) is built to serve: container port `8080`, probes on `/healthz`.

## Env contract (`cmd/openova-mcp`)

| Env var | Purpose |
|---|---|
| `OPENOVA_MCP_CATALYST_API_URL` | base URL of the catalyst-api / gateway (required for data tools; `whoami` works without it) |
| `OPENOVA_MCP_CONTEXT` | `organization` \| `sovereign` — pins the instance context per the topology table (empty = derive per-token) |
| `OPENOVA_MCP_VERIFY` | `rs256` (default) \| `hs256` \| `insecure` — bearer verification mode |
| `OPENOVA_MCP_RS256_PUBKEY_PEM` | PEM of the RS256 public key (Sovereign handover-jwt pubkey) when verify=rs256 |
| `OPENOVA_MCP_HS256_SECRET` | HS256 shared secret when verify=hs256 |
| `OPENOVA_MCP_BEARER` | fallback bearer when a `tools/call` omits the `_auth.token` argument (stdio); on HTTP, a server-token fallback when a request omits the `Authorization` header |
| `OPENOVA_MCP_HTTP_ADDR` | when set (e.g. `:8080`), serve the HTTP/SSE transport on this address instead of stdio (equivalent to `--http <addr>`) |

## Live-acceptance (hw173)

Proven on `hw173.omani.works` (dep `7bb723da8da06047`) by running this
server **inside hw173's catalyst-api pod** pointed at the real
`localhost:8080`, with bearers signed by hw173's own handover key:

- A sovereign-admin `list_applications` returns the **real** Application CRs
  unfiltered.
- An Org token (`org_id=acme`) sees **only** that Org's Applications.
- A cross-Org `get_application` for another Org returns **forbidden**.

See the walk evidence on issue #3988.

## Deferred to follow-ups (NOT built in this slice)

Per #3988 §5, the following are explicitly out of this slice and tracked as
follow-ups:

- **Agenity integration** — the `bp-openova-mcp dependsOn bp-agenity`
  Blueprint chart, the `openovaMCP.*` repoint, the `.mcp.json` injection.
- **Bootstrap-kit / catalog-seed wiring** — the HTTP/SSE transport itself now
  exists (see [Transports](#transports)), but flipping `httpTransport.enabled`
  on a real Sovereign (the ConfigMap that sets `OPENOVA_MCP_HTTP_ADDR`, the
  stable per-Org/per-Sovereign DNS + HTTPRoute host) is a post-SME follow-up.
- **Per-realm JWKS validation** — this slice verifies against a single
  RS256/HS256 key; the full per-realm JWKS cache (Org realm vs Sovereign
  realm) is the production identity path.
- **Remaining write / mutating tools** — `deployments.*`, `vouchers.*`,
  `cutover.*`, `placement.*`, `k8s.write.*`, etc. (the first write tool,
  `create_application`, is now LIVE — see above.)
- **Sovereign-scoped admin tools** + the full ~26 (Org) / ~70 (Sovereign)
  tool catalog.
- **The registry-coverage parity test** — generate the registry from the
  route table + a CI test that fails when a mutating endpoint has neither a
  tool nor an explicit `// not-exposed:` annotation.
