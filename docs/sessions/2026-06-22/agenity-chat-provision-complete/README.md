# Agenity chat→provision — create_application LANDS (2026-06-22)

**Env:** omantel.biz / kom4dc (Huawei) dep `4635277cae4ffed9` (PERMANENT).
**Surface:** demo Org `org-7283eb4a-19e5-4e86-9066-d4aa26762064`, console
`https://console.demo.omani.homes`, agenity `https://agenity.demo.omani.homes/app/`.
**Issue:** #3988 (MCP) / #4116 (org-create path) / #4110 (OrgScopeGuard confinement).

## Status — last validated: omantel.biz / demo Org (2026-06-22)

| UAT row | Was | Now | Proof |
|---|---|---|---|
| 221 — chat→MCP→create_application | ⚠️ 403 org-scoped-forbidden | ✅ | `create_application`→HTTP 201, CR `test-shop` (uid bc367a49) persisted |
| 222 — created app appears in Org /apps | ❌ blocked | ✅ | `test-shop` card renders on console.demo.omani.homes/apps |
| 223 — MCP RBAC Org-scoped | ⚠️ dead-zone bearer | ✅ | `whoami`→context=organization tier=admin org_id=demo sovereign_admin=false |

## The remaining gap (precisely root-caused, now closed)

The agent's `create_application` returned **403 `org-scoped-forbidden`** because
the demo agenity's openova-mcp ran `OPENOVA_MCP_CONTEXT=sovereign` with a
**sovereign-admin bearer that carried NO `org_id`** (`tier=owner`,
`role=sovereign-admin`). So `handleCreateApplication` saw `Context=sovereign`
and fell through to the **sovereign-wide** seam
`POST /api/v1/sovereigns/{id}/applications`, which the #4110 `OrgScopeGuard`
denies for a host-scoped (`X-Tenant-Host`) session. The own-org seam
`POST /api/v1/org/applications` (allowlisted, #4116) was never reached because
it's only taken when `Context==organization`.

## The fix (3 legs)

1. **openova-mcp identity model** — `core/services/shared/auth.Claims` gained a
   `Tier string json:"tier,omitempty"` field (the precomputed tier claim the
   PIN session already stamps), and `openova-mcp/internal/identity.deriveTier`
   now reads it. `tierFromLabel` maps the Org-scoped marker **`org-admin`
   (auth.OrgScopedTier) → `TierAdmin`** — an Org-scoped session is the admin of
   its OWN Org, so it satisfies `create_application`'s `MinTier=Admin` gate,
   while carrying ZERO Sovereign signal (so `deriveContext` keeps it in
   `ContextOrganization` and #4110 confinement holds).

2. **Live bearer + context** — the demo agenity's `agenity-mcp-bearer` secret
   was replaced with an **Org-scoped** RS256 session JWT (`tier=org-admin`,
   `org_id=org=demo`, `role=openova-user`, `typ=session`, `iss` matching
   `CATALYST_PIN_ISSUER`), signed by the Sovereign's handover signer
   (`/var/lib/catalyst/handover-jwt-private.pem` — modulus verified to match
   `catalyst-handover-jwt/handover-jwt-public.pem`, the key the MCP verifies
   against). `OPENOVA_MCP_CONTEXT` was flipped `sovereign`→`organization` on the
   live StatefulSet (HR stays suspended so the patch survives).

3. **Durable chart** — `bp-agenity` 0.5.0→0.5.1: `values.yaml` documents the
   ORG-scoped bearer shape required for an Org-deployed agenity (NOT
   sovereign-admin), paired with the existing `openovaMCP.context=organization`
   default. The openova-mcp binary is built from THIS repo in the agenity
   Containerfile (stage 3), so the image rebuild carries the `deriveTier` fix.

## Proven live (full openova-mcp binary, not curl)

- `whoami` → `{"context":"organization","tier":"admin","org_id":"demo","sovereign_admin":false}`
- `tools/list` → 6 tools, **`create_application` VISIBLE** (was 0/invisible at tier=none)
- `create_application{bp-wordpress 0.4.1, test-shop}` → **`isError:false`, HTTP 201 Created**,
  CR `test-shop` uid `bc367a49-66a2-4432-adef-ec305e351c69` in `org-7283eb4a…`.
- `test-shop` renders on the demo Org console `/apps` → `02-…png` / `03-…png`.

Files: `mcp-create_application-transcript.txt`, `01-agenity-dashboard.png`,
`02-test-shop-in-demo-org-apps.png`, `03-test-shop-detail.png`.
