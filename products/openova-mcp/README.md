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

1. **The MCP server core** (`cmd/openova-mcp`) — JSON-RPC 2.0 over stdio,
   the same proven transport the sandbox MCP uses.
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

## Env contract (`cmd/openova-mcp`)

| Env var | Purpose |
|---|---|
| `OPENOVA_MCP_CATALYST_API_URL` | base URL of the catalyst-api / gateway (required for data tools; `whoami` works without it) |
| `OPENOVA_MCP_CONTEXT` | `organization` \| `sovereign` — pins the instance context per the topology table (empty = derive per-token) |
| `OPENOVA_MCP_VERIFY` | `rs256` (default) \| `hs256` \| `insecure` — bearer verification mode |
| `OPENOVA_MCP_RS256_PUBKEY_PEM` | PEM of the RS256 public key (Sovereign handover-jwt pubkey) when verify=rs256 |
| `OPENOVA_MCP_HS256_SECRET` | HS256 shared secret when verify=hs256 |
| `OPENOVA_MCP_BEARER` | fallback bearer when a `tools/call` omits the `_auth.token` argument |
| `OPENOVA_MCP_EXPECTED_ISSUER` | optional exact `iss` claim pin — the instance-level trusted-realm boundary (#3988 §4.3) |
| `OPENOVA_MCP_ORG_SCOPE` | optional Org slug pin for a per-Org instance — a token minted for a different Org is rejected outright |
| `OPENOVA_MCP_LISTEN` | optional listen address (e.g. `:8080`) — serves the **streamable-HTTP transport** (`POST /mcp` with `Authorization: Bearer`, `GET /healthz`, `GET /readyz`) instead of stdio (#5114) |

## Install path (bp-openova-mcp)

`chart/` + `blueprint.yaml` (#5114, Refs #3988) package the standalone
Service instance: Deployment (HTTP transport) + ClusterIP Service +
Gateway-API HTTPRoute (`mcp.<sovereign-fqdn>`; never a Traefik Ingress,
never a NodePort) + a zero-RBAC ServiceAccount (the facade reads nothing
from the k8s API). The sovereign-mode instance installs on every fresh
prov via bootstrap-kit slot 13d; `mode=organization` values pin a per-Org
instance. See `chart/DESIGN.md` for the topology table + recorded
deviations from the #3988 design. The standalone image is
`ghcr.io/openova-io/openova-mcp` (`Containerfile`,
`.github/workflows/build-openova-mcp.yaml`); the agenity image continues
to bundle the same binary as its stdio child.

## Live-acceptance (hw173)

Proven on `hw173.omani.works` (dep `7bb723da8da06047`) by running this
server **inside hw173's catalyst-api pod** pointed at the real
`localhost:8080`, with bearers signed by hw173's own handover key:

- A sovereign-admin `list_applications` returns the **real** Application CRs
  unfiltered.
- An Org token (`org_id=acme`) sees **only** that Org's Applications.
- An out-of-scope `get_application` for another Org returns **forbidden**.

See the walk evidence on issue #3988.

## Deferred to follow-ups (NOT built in this slice)

Per #3988 §5, the following are explicitly out of this slice and tracked as
follow-ups:

- ~~**HTTP transport**~~ — SHIPPED (#5114): `OPENOVA_MCP_LISTEN` serves
  streamable-HTTP (`POST /mcp` + health endpoints); server-initiated SSE
  streams (`GET /mcp`) remain a follow-up (design doc O4).
- ~~**The install path**~~ — SHIPPED (#5114): `chart/` + `blueprint.yaml`
  + bootstrap-kit slot 13d (sovereign mode). Still follow-ups: per-Org
  auto-install via the org pipeline; the chepherd attach stays a
  values-gated seam until a bp-chepherd Blueprint exists (#3988 §4.4
  deviation).
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
