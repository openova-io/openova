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

- **Agenity integration** — the `bp-openova-mcp dependsOn bp-agenity`
  Blueprint chart, the `openovaMCP.*` repoint, the `.mcp.json` injection.
- **HTTP/SSE transport** — this slice uses stdio; the long-lived
  per-Org/per-Sovereign Service needs streamable-HTTP/SSE + stable DNS.
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

## Packaging — `bp-openova-mcp` (DRAFT, #899)

**What it is.** A Blueprint packaging of this server so its API surface (the
`list_applications` / `get_application` / `create_application` tools that UAT
rows 211/212/213/221/222/223 exercise) can be shipped as an OCI image + Helm
chart rather than only as source. **Role in Catalyst:** a supporting AI-runtime
facade (`pts-7-org-tenant`) consumed by the agentic runtime — not a control-plane
service. Canonical placement: [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md)
(bootstrap-kit slots) + [`docs/DOD.md`](../../docs/DOD.md) Pillar 4.

Files:

- `Dockerfile` — multi-stage Go build (distroless `static-debian12:nonroot`),
  repo-root build context so the `replace github.com/openova-io/openova/core/services/shared`
  directive resolves. Mirrors `products/sandbox/mcp-server/Dockerfile`. Builds
  the identical binary agenity bakes at `/usr/local/bin/openova-mcp`.
- `chart/` — `bp-openova-mcp` Helm chart: ServiceAccount + ConfigMap always;
  Deployment + Service (ClusterIP) + HTTPRoute + K8s NetworkPolicy + Cilium
  ingress/egress CNPs gated behind `httpTransport.enabled`.
- `blueprint.yaml` — the `bp-openova-mcp` catalog declaration (`configSchema`,
  singleton topology, `visibility: unlisted`).

### 🛑 Transport prerequisite — why the serving resources are gated OFF

The first slice speaks **JSON-RPC over stdio only** — it binds **no port** and
has **no HTTP listener**. Run in a Deployment with no stdin it hits EOF and
exits, so a Pod would crash-loop. The streamable-HTTP/SSE transport a
long-lived HTTPRoute-fronted Service needs is a #3988 §5 follow-up. Until it
lands, `httpTransport.enabled` defaults to **false** and the chart renders only
the ServiceAccount + ConfigMap. This packaging is therefore *design-ready
reconnaissance*: the target topology is authored and reviewable, and flips live
the moment the transport ships. It is intentionally **not** wired into the
bootstrap-kit or catalog-seed here.

### Config knobs (chart values ↔ `OPENOVA_MCP_*`)

| values path | env | purpose |
|---|---|---|
| `httpTransport.enabled` | — | gate the serving resources (default false — see above) |
| `httpTransport.port` | — | design-target HTTP listen port (8080) |
| `config.catalystApiUrl` | `OPENOVA_MCP_CATALYST_API_URL` | catalyst-api / gateway base URL the bearer is forwarded to |
| `config.context` | `OPENOVA_MCP_CONTEXT` | `organization` \| `sovereign` context pin |
| `config.verify` | `OPENOVA_MCP_VERIFY` | `rs256` (default) \| `hs256` \| `insecure` |
| `config.tenantHost` | `OPENOVA_MCP_TENANT_HOST` | X-Tenant-Host for the org-scoped install path (#4116) |
| `config.rs256PubkeySecret` | `OPENOVA_MCP_RS256_PUBKEY_PEM` | Secret projecting the handover-jwt public key |

### Operational notes

- **Auth model:** openova-mcp validates the caller's **bearer in-process**
  (`internal/identity`, RS256/HS256) — it is a machine-to-machine MCP endpoint,
  not a browser SPA, so it does **not** sit behind `bp-oidc-gate`. The HTTPRoute
  exposes it directly and the server is the auth authority per `tools/call`.
- **The "Org-scoped RBAC" the UAT rows describe is application-tier**, not
  Kubernetes RBAC: it is JWT-claim-driven, enforced in `internal/identity` +
  `internal/tools` (the two-layer visible/Call gate) + the catalyst-api handler
  tier check. The facade makes **no** kube-apiserver calls, so the chart ships a
  bare ServiceAccount and `rbac.create` defaults to **false** (no Role/RoleBinding).
- **NodePorts are forbidden:** the Service is ClusterIP-only; the front door is
  the Cilium Gateway HTTPRoute.
- **Scaling / multi-region:** stateless region-pinned singleton (holds no
  state, nothing to replicate).
