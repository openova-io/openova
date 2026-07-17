# hw266 — NORTH-STAR: openova-mcp LIVE + rs256 ACTIVE (#5167 / #3988) — 2026-07-17

Env: hw266.omani.works, dep b85cb3b3a565893a, 2-region, bp-openova-mcp@0.1.3.

## PROVEN live (definitive)
1. **MCP deployed as a networked Blueprint** — Service `openova-mcp-bp-openova-mcp`
   (ClusterIP) + HTTPRoute `mcp.hw266.omani.works`; `GET /healthz` over public HTTPS → `ok`.
   Resolves the prior ❌ blocker on rows 211-213/221 ("openova-mcp is stdio-only, no
   HTTP transport, not deployed" — #3988 §5). It NOW serves streamable-HTTP
   (`POST /mcp, GET /healthz, GET /readyz`).
2. **#5167 rs256 verify ACTIVE** — 0 DEGRADED in the log (was DEGRADED-401 on every
   prior env); env wired to `catalyst-handover-jwt-public/public.jwk` (the fix). A
   mothership-minted owner RS256 token → `whoami` resolved to
   `{context:sovereign, email:emrah.baysal@openova.io, sovereign_admin:true, tier:sovereign-admin, fqdn:hw266.omani.works}`
   → the JWK-loaded key VERIFIED the RS256 signature + extracted claims. The #5167 fix
   works end-to-end.
3. **Full RBAC-scoped tool surface** served: `create_application, get_application,
   list_applications, list_environments, list_organizations, whoami` — the north-star
   `create_application` ("Install an Application in the caller's Organization … RBAC-scoped:
   an Org-scoped caller may create ONLY in their own Organization; a sovereign-admin may
   create in any Organization").
4. **Fail-closed** — no-bearer `tools/call` → `-32001 unauthenticated`.

## Remaining for the full data round-trip (rows 211-213 → ✅)
The MCP is a THIN facade that forwards the caller's bearer to the live catalyst-api.
`whoami` resolves MCP-locally (proven), but `list_applications` forwards upstream and the
mothership HANDOVER token is not a catalyst-api SESSION bearer (upstream 401 — correct).
The real Agenity flow uses the user's PIN-login session token (minted by the Sovereign's
own catalyst-api). Deferred to the post-cutover PIN-login owner walk, which mints the
correct session token → list_applications returns apps → rows 211-213 flip ✅. This is a
test-token-type detail, NOT an MCP or #5167 defect.

Evidence: tools-list.json, list-applications.json (this dir).
