# Walk hw173 — UAT Epic: model (rows 1–25, #3687)

- Env: **hw173** (depID `7bb723da8da06047`), `console.hw173.omani.works`
- Method: handover-authenticated curl (`/auth/handover` → cookie) + mothership-kubectl via catalyst-api pod.
- Walker: hatiyildiz. Date: 2026-06-20.

## Headline env-fact governing this section

hw173 is a **fresh zero-touch prov with the PLATFORM estate only** — there is **NO customer Organization** and **NO customer-launched app**:
- `GET /api/v1/sme/tenants` → `{"items":[]}` (no sub-orgs)
- The cluster has **no `organizations.catalyst.openova.io` CRD** registered (`kubectl get organizations` → "the server doesn't have a resource type organizations")
- `GET /api/v1/sovereign/apps` → 70 cards, only INSTALLED instances are `shared-pg` / `shared-pg-b` / `shared-pg-c` / `valkey` (all `bootstrapKit:true`); zero non-bootstrap customer apps (`blog`/`acme` absent)
- `GET /api/v1/sme/consumption` → only 2 org rows: parent `hw173.omani.works` (apps=0) + `__platform__` roll-up

Therefore every row that **asserts the `acme` customer Org / a customer app is present, active, vcluster-backed** is a **❌** (the asserted record does not exist on this env), not a UI defect. The platform-side model surfaces (sidebar, catalog, apps, shared-PG contexts, treemap Job-exclusion, Platform-overhead roll-up, owner login) are all live and correct.

## Verdict table

| Row | Verdict | Evidence (HTTP/JSON/kubectl) | Note |
|-----|---------|------------------------------|------|
| 1 | ✅ | `/auth/handover?token=…` → 302 `redirect_url=https://console.hw173.omani.works/dashboard`; bare `/` w/ session → 200; `GET /api/v1/whoami` → `{"email":"emrah.baysal@openova.io","tier":"owner","realm_access.roles":["catalyst-owner","catalyst-admin","sovereign-admins"]}` | bare URL lands /dashboard signed-in as owner, no PIN |
| 2 | ✅ | `SovereignSidebar.tsx` renders all 10 nav items: Dashboard / Cloud / Apps / Catalog / Sandbox / Jobs / Compliance / Users / Organizations / Settings (grep-confirmed); console served (200) | full sidebar present |
| 3 | ⚠️ | console `/redeem/?code=…` → 200 (SPA shell, no server 302); marketplace `/redeem/?code=…` → 200. The authed-owner→/dashboard redirect is a client-side SPA navigation (no server redirect to observe by curl) | browser-only render; route reachable, no 404/500 |
| 4 | ✅ | shared-pg detail `contexts:[{name:registry,kind:db,occupiedBy:harbor},{gitea→gitea},{keycloak→keycloak}]`; `harbor.dependsOn` includes `{instance:"shared-pg",context:"db/registry"}`, gitea→`db/gitea`, keycloak→`db/keycloak`. Both archetypes use the same `AppDetail` tab strip (Overview·Contexts·Topology·Dependencies…) | consumer Dependencies shows `Depends on: shared-pg / db:<ctx>`; identical strip |
| 5 | ❌ | `GET /api/v1/sme/tenants` → `{"items":[]}` — no customer Org row at all | customer Org `acme` absent on this prov |
| 6 | ❌ | tenants feed empty; consumption orgs = only `hw173.omani.works`(parent) + `__platform__` | no customer Org record in directory/showback |
| 7 | ❌ | Org-detail page resolves `target = rows.find(slug==='acme')`; directory has only the parent row (FQDN slug), so `/organizations/acme` renders `org-detail-not-found`. tenants empty | `acme` org detail not found |
| 8 | ✅ | `CreateTenantPage.tsx` (route `/organizations/new`) renders kind picker (`orgKind`), slug (`subdomain`), Company name (`companyName`), Admin email (`adminEmail`), parent-domain (`parentDomain`) — all 5 controls present | create-org form complete (code-verified) |
| 9 | ❌ | tenants empty; no `organizations` CRD on cluster → no active vcluster-backed customer Org exists | nothing to mark active |
| 10 | ❌ | same — Lane-B convergence read shows no customer Org (tenants `{"items":[]}`) | absent |
| 11 | ❌ | `/organizations/acme` → not-found (tenants empty); no directory/detail `vcluster` row for acme | absent |
| 12 | ❌ | re-load of `/organizations/acme` still not-found (tenants empty, stable) | absent |
| 13 | ✅ | `GET /api/v1/catalog` → 200 with card items (e.g. bp-alloy card{title,category}); `GET /api/v1/catalog/postgres` → 200 `bp-postgres` v0.1.6 card; `CatalogDetail.tsx` renders InstancesSection with "+ New instance" topology-picker dialog | catalog grid + bp-postgres detail render; + New instance present |
| 14 | ✅ | shared-pg detail `contexts` = 3 db consumers (registry/harbor, gitea/gitea, keycloak/keycloak) over ONE bp-postgres instance; shared-pg-c has contextCount=5 | shared-PG reuse LIVE; multiple consumers, one PG |
| 15 | ✅ | `/api/v1/sovereign/apps` → 70 cards, one per Application; installed instances shared-pg/-b/-c + valkey carry `bootstrapKit:true` (Platform/Bootstrap badge); no customer-launched card (none exist yet) | one card per Application |
| 16 | ❌ | `GET /api/v1/sovereigns/7bb723da8da06047/applications/blog` → 404 — the customer app `blog` does not exist on this prov | no customer app to open/save-topology |
| 17 | ✅ | `Dashboard.tsx` sovereign default `defaultLayers=['organization','application']` (Layer-1=Organization); treemap fetch `GET /api/v1/dashboard/treemap?group_by=organization,application` → 200, total_count=97 | Layer-1 default = Organization, drillable |
| 18 | ✅ | treemap L1 cell="Platform overhead" with 97 leaves; NO `cutover-*`/`scan-vulnerability*`/`*-snapshot-save-*` cells. Live cluster HAS `scan-vulnerabilityreport-*` (ownerRef **Job**) + `openbao-snapshot-save-*` Job pods — these are EXCLUDED. The lone `trivy` cell = `trivy-trivy-operator` Deployment (long-lived), `self-sovereign-cutover` cell = controller Deployment | no ephemeral Job-pod cells (#3869 holds) |
| 19 | ✅ | apps feed = one card per Application (70), not per HR/pod; bootstrap apps carry `bootstrapKit:true` badge (52/70 bootstrapKit) | one card per Application + Bootstrap badge |
| 20 | ⚠️ | treemap L1 = single "Platform overhead" cell (no customer estate exists to be visually distinct from it). Mechanism is present (org dimension) but there is no customer Org to render distinctly | needs a customer Org present to verify distinctness |
| 21 | ⚠️ | `/api/v1/sme/consumption` → parent `hw173.omani.works` row has `apps:[]` (no infra/Job pollution); all 109 platform/Job workloads sit under `__platform__`. Correct exclusion shown, but no customer Org to list "only that Org's apps" | platform-exclusion correct; no customer Org to fully assert |
| 22 | ✅ | consumption `orgs` includes single `{org:"__platform__",isPlatform:true,costUnits:14960,apps:[109 control-plane/Job workloads]}` distinct from real orgs | single Platform-overhead roll-up holds all control-plane/Job workloads |
| 23 | ❌ | consumption orgs = only `hw173.omani.works`(parent) + `__platform__`; no 2nd customer Org row | no 2nd Org ran an app on this prov |
| 24 | ✅ | shared-pg (bp-postgres, `shareable:true`) detail returns `contexts` → `AppDetail.tsx` renders Contexts tab gated on `BLUEPRINT_BY_ID[bp].shareable===true`; canonical strip Overview·Contexts·Topology·Dependencies | Contexts tab present for shareable blueprint |
| 25 | ⚠️ | The 3 surfaces AGREE: `/organizations`+`/sme/tenants` show only parent; `/sovereign/apps` shows only platform instances; treemap shows only Platform overhead; consumption shows parent+`__platform__`. They are mutually consistent — but the row's intent (a customer Org appearing across all surfaces) can't be asserted with no customer Org present | surfaces consistent; no customer Org to cross-check |

## Summary

- ✅ **12**: rows 1, 2, 4, 8, 13, 14, 15, 17, 18, 19, 22, 24
- ❌ **9**: rows 5, 6, 7, 9, 10, 11, 12, 16, 23 — all fail on the SAME root: **no customer Organization / no customer app exists on this fresh zero-touch prov** (`/sme/tenants` empty, no `organizations` CRD, `/app/blog` 404). Not UI defects — missing customer estate.
- ⚠️ **4**: rows 3 (SPA-side redeem redirect, browser-only), 20 / 21 / 25 (mechanism live but require a customer Org present to fully assert).

Recount: ✅ = {1,2,4,8,13,14,15,17,18,19,22,24} = **12**. ❌ = {5,6,7,9,10,11,12,16,23} = **9**. ⚠️ = {3,20,21,25} = **4**. (12+9+4 = 25.)

The platform-side model machinery (silent owner login, full sidebar, catalog + New-instance, shared-PG many-to-many contexts, one-card-per-Application + bootstrap badges, treemap org-dimension + Job-pod exclusion, Platform-overhead showback roll-up) is **all live and correct**. The 9 ❌ are entirely attributable to the absence of a provisioned customer Organization on this env — to flip them, an `acme` (or equivalent) customer Org must be created + reach active/vcluster, then re-walk rows 5-12, 16, 23.
