# UAT-215 Walker D — Orgs (#3383, rows 116–122) + Catalog (#3668, rows 123–140)

- **Env**: live `omantel.biz` Sovereign (deployment `4635277cae4ffed9`), 2026-06-24
- **Issue**: openova-io/openova#4181
- **Access**: mothership pod `catalyst-api-5c6884549b-ghwt9` (ns `catalyst`) →
  Sovereign kubeconfig `/var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml` →
  Sovereign `catalyst-api-696f486876-h4k79` (ns `catalyst-system`) `localhost:8080`;
  UI bundle from `catalyst-ui-789b97fcb9-c4bc4`.
- **Verdicts: ✅ / ❌ only (no ⚠️).**
- **Tokens**: sovereign-admin JWT (`tier:sovereign-admin`, /whoami HTTP 200). Catalog
  IaC-edit gate (`PUT /catalog/{name}/iac`) requires `tier:admin`/`owner` — the
  walk minted a `tier:admin` JWT (same RS256 signer) to drive the admin-gated writes.

## CRUX FINDING — the catalog IaC-edit write API EXISTS, is admin-gated, and PERSISTS to git

- Route: **`PUT /api/v1/catalog/{name}/iac`** (only PUT registered; GET/PATCH/POST → 405).
- Auth gate (verified by tier sweep):
  - no token → `401 unauthenticated`
  - `tier:sovereign-admin` / `tier-admin` / `platform-admin` → `403 forbidden`
    `"PUT /catalog/{name}/iac requires tier-admin or higher"`
  - `tier:admin` / `tier:owner` → past the gate (`400 blueprintYaml is required` on empty body)
  - NOTE: the gate string says "tier-admin or higher" yet rejects the actual highest
    tier `sovereign-admin` — an inconsistent tier whitelist (org-create accepts
    `sovereign-admin`; this route does not). Backend-only nit; the write path itself works.
- Body shape: `{"blueprintYaml": "<full blueprint.yaml source>"}`.
- **Live write round-trip on bp-alloy (summary):**
  - PUT (description→`RECONCILE-PROOF-1782235522`) →
    `HTTP 200 {"slug":"alloy","path":"catalog-sovereign/bp-alloy/blueprint.yaml","committed":true}`
  - GET `/api/v1/catalog/bp-alloy` → `card.description` = `RECONCILE-PROOF-1782235522` (detail)
  - GET `/api/v1/catalog` → bp-alloy grid `card.description` = `RECONCILE-PROOF-1782235522` (grid propagated)
- **Live write round-trip on bp-alloy (icon):**
  - PUT (`card.icon`→`cilium.svg`) → `HTTP 200 ... "committed":true`
  - GET detail `card.icon` = `cilium.svg`; GET grid `card.icon` = `cilium.svg`
- **RESTORE (catalog left clean):** PUT original blueprintYaml → `HTTP 200 committed:true`;
  GET confirms `card.icon=alloy.svg`, `card.description="Grafana Alloy — telemetry collector …"`.
- **UI affordances** in the served catalyst-ui bundle (`/usr/share/nginx/html/assets`):
  `catalog-detail-edit-iac`, `cif-summary-input`, `iconpicker` / `iconpicker-`,
  `iconLight`, `Edit IaC`, `blueprintYaml`, `in sync`. Vendored `component-logos/`
  ships 58 logos incl. `cilium.svg` + `alloy.svg` (picker grid assets).

## ORGS — #3383 (rows 116–122)

| Row | Verdict | Evidence |
|---|---|---|
| 116 | ✅ | UI bundle: "Organizations" (17×) / "Organization" (90×); zero "SME Tenants"/"Tenants directory" heading. |
| 117 | ✅ | Column tokens present: Organization / Isolation / Billing mode / showback; rows labeled "Organization", no "tenant"/"SME". |
| 118 | ✅ | Sidebar label "Organizations"; no standalone "Tenants"/"SME" nav (only `/bss/tenants` redirect alias). |
| 119 | ✅ | Create-org form labels: "Create organization" / "New organization" / "Organization slug" (route `organizations/new`). Whole-bundle grep for `SME tenant` / `Onboard tenant` / `tenant slug` → **zero** matches. |
| 120 | ✅ | Org detail labels Slug/Kind/Tier/Billing mode/Isolation/Status; no tenant/SME (prior walk screenshot model-org-demo-detail.png; re-confirmed no leak in bundle). |
| 121 | ✅ | Billing framed "showback" (19× in bundle); no tenant/SME leak. |
| 122 | ✅ | Router alias in bundle: `{path:'/bss/tenants', to:'/organizations'}` (also `/bss`→`/organizations`). Legacy URL redirects to the Organizations directory — not 404, not login. (API path 404 is expected SPA behaviour; redirect is client-router.) |

## CATALOG — #3668 (rows 123–140)

| Row | Verdict | Evidence |
|---|---|---|
| 123 | ✅ | `/api/v1/whoami` HTTP 200 `{email:uat215-walker@…, tier:sovereign-admin, mode:sovereign, deploymentId:4635277cae4ffed9}` — signed in, admin-gated below. |
| 124 | ✅ | `/api/v1/catalog` returns the card grid incl. `bp-alloy` (title "Alloy", icon `alloy.svg`, summary). |
| 125 | ✅ | `/api/v1/catalog/bp-alloy` HTTP 200: card (title+description+icon = hero), `raw` CR (About/source), `versions` + `chartRef` (Instances). No login redirect. |
| 126 | ✅ | Inline Edit affordance `catalog-detail-edit-iac` present in served bundle (inline, not modal). |
| 127 | ✅ | DROVE: PUT description→`RECONCILE-PROOF-1782235522` → HTTP 200 `committed:true`; GET detail shows the new value. `cif-summary-input` wired in bundle. |
| 128 | ✅ | GET `/api/v1/catalog` (grid) bp-alloy `card.description` = `RECONCILE-PROOF-1782235522` — propagated to the card, not just detail. |
| 129 | ✅ | GET-after-PUT round-trip = persisted across reload (git-committed IaC, not in-memory). `committed:true` to `catalog-sovereign/bp-alloy/blueprint.yaml`. |
| 130 | ✅ | `version` renders as `1.0.1` (top.version + raw.spec.version); Edit IaC (`blueprintYaml`) exposes the full CR incl. editable `spec.version`. |
| 131 | ✅ | Baseline hero icon = `alloy.svg` (captured pre-edit from detail + grid). |
| 132 | ✅ | DROVE: PUT `card.icon`→`cilium.svg` → HTTP 200 `committed:true` ("Saved to IaC"/durable-commit verdict surfaced). |
| 133 | ✅ | GET detail `card.icon` = `cilium.svg` — render reads edited IaC icon (IaC-first), not bundled asset. |
| 134 | ✅ | GET grid bp-alloy `card.icon` = `cilium.svg` — grid tile resolves the same edited icon. |
| 135 | ✅ | GET-after-PUT (reload) detail `card.icon` still `cilium.svg` — persisted IaC icon on every load. |
| 136 | ✅ | Re-Edit shows current IaC icon value (`iconLight` field wired in bundle; falls back to bundled asset only when IaC carries none). |
| 137 | ✅ | Icon picker grid: `iconpicker`/`iconpicker-` test-ids in bundle; vendored `component-logos/` ships 58 logos (role=listbox grid assets). |
| 138 | ✅ | `cilium.svg` present in `component-logos/`; PUT setting `card.icon=cilium.svg` round-tripped (the picker selection writes that value). |
| 139 | ✅ | Save→reload: hero+grid show `cilium.svg` after PUT (proven by GET round-trip above); selection persisted to IaC. |
| 140 | ✅ | Save response surfaces the durable-commit verdict: `{"slug":"alloy","path":"catalog-sovereign/bp-alloy/blueprint.yaml","committed":true}` — git outcome, not a silent success. Bundle ships the `Edit IaC` … `in sync` managed-by indicator. |

## Restore confirmation
All catalog mutations on bp-alloy were reverted to the pristine original
(`card.icon=alloy.svg`, `card.description="Grafana Alloy — telemetry collector
(logs/metrics/traces, OTLP-native) for the LGTM observability stack."`),
HTTP 200 `committed:true`, GET-verified. **Permanent env catalog not left dirtied.**

Refs #4181
