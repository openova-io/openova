# walk(hw173) — placement (rows 96–115) + orgs (rows 116–122)

Env: hw173 (depID `7bb723da8da06047`), region `hw-me-east-215-a-rtz-prod`, status `ready`.
Walker: hatiyildiz, Opus. Evidence = LIVE kubectl (admin kubeconfig via mothership catalyst-api
pod) + LIVE curl against `*.hw173.omani.works` + the shipped console bundle `/assets/index-CXT0kSay.js`.

## Method / shared evidence

**Owner session (row 96 + all console API calls):**
```
GET /auth/handover?token=<jwt>  -> 302 Location: /dashboard
GET /dashboard                   -> 200
GET /api/v1/whoami               -> 200
  {"email":"emrah.baysal@openova.io","tier":"owner",
   "realm_access":{"roles":["catalyst-owner","catalyst-admin","sovereign-admins"]}}
```

**vCluster topology (host cluster):** three vClusters Ready — `mgmt-vcluster 1/1`, `rtz-vcluster 1/1`,
`dmz-vcluster 1/1` (statefulset READY). All platform apps are synced INTO the `mgmt` namespace
from the **mgmt vCluster** — every workload pod carries the `…-x-<ns>-x-mgmt-vcluster` syncer suffix,
which is the definitive proof the workload lives in the mgmt vCluster, not on the host.

`kubectl get pods -n mgmt` (the mgmt-vcluster synced workloads), abridged to the 7 named apps + the
row-106 observability set:
```
grafana-6f94dbbd8f-…-x-grafana-x-mgmt-vcluster                3/3 Running
harbor-core-…-x-harbor-x-mgmt-vcluster                        0/1 CreateContainerConfigError   <-- app DOWN
harbor-jobservice-…-x-harbor-x-mgmt-vcluster                  0/1 CrashLoopBackOff (64)         <-- app DOWN
harbor-registry/portal/nginx/redis-…-x-harbor-x-mgmt-vcluster Running
keycloak-0-x-keycloak-x-mgmt-vcluster                         1/1 Running
gitea-5486569f5c-…-x-gitea-x-mgmt-vcluster                    1/1 Running
openbao-0-x-openbao-x-mgmt-vcluster                           1/1 Running
newapi-bp-newapi-…-x-newapi-x-mgmt-vcluster                   3/3 Running
guacamole-server-…-x-catalyst-system-…  guacd-…-x-mgmt-vcluster   Running
loki-0-x-loki-x-mgmt-vcluster / mimir-*-x-mimir-x-mgmt-vcluster / nats-jetstream-*-x-nats-system-x-mgmt-vcluster / tempo-0-x-tempo-x-mgmt-vcluster  Running
```

`kubectl get pods` in the HOST-side app namespaces (grafana/harbor/keycloak/gitea/openbao/newapi/guacamole):
```
grafana    -> No resources found            keycloak  -> No resources found
openbao    -> No resources found            guacamole -> No resources found
harbor     -> harbor-pg-1 (CNPG db only)    gitea     -> gitea-pg-1 (CNPG db only)
newapi     -> newapi-bp-newapi-newapi-pg-1/2 (CNPG db only)
```
=> No app WORKLOAD runs on host; only the CNPG postgres backing instances sit in the host app
namespaces (the #3642 host-bridge model). The 7 apps themselves are 100% in the mgmt vCluster.

## Placement rows 96–115

| Row | Verdict | Evidence (HTTP/JSON/kubectl) | Note |
|-----|---------|------------------------------|------|
| 96  | ✅ | `/auth/handover`→302→`/dashboard` 200; `/api/v1/whoami`→200 email=emrah.baysal@openova.io tier=owner roles=[catalyst-owner,catalyst-admin,sovereign-admins] | lands signed-in as owner, no login form |
| 97  | ⚠️ | treemap + LAYER1/LAYER2 comboboxes are a browser-only render; data backing it is live (dashboard 200) | browser-only render, not API-reachable headless |
| 98  | ⚠️ | LAYER1=vCluster regroup is browser-only; the vClusters exist live (`mgmt/rtz/dmz-vcluster 1/1 Ready`) | render not headless; data confirmed |
| 99  | ✅ | `grafana-…-x-grafana-x-mgmt-vcluster 3/3 Running`; host `grafana` ns = No resources | grafana tile is mgmt, not host (kubectl-proven placement) |
| 100 | ✅ | `harbor-*-…-x-harbor-x-mgmt-vcluster`; host `harbor` ns = only harbor-pg-1 (CNPG) | harbor workload is mgmt, not host (app health is row 111) |
| 101 | ✅ | `keycloak-0-x-keycloak-x-mgmt-vcluster 1/1 Running`; host `keycloak` ns = No resources | keycloak tile is mgmt, not host |
| 102 | ✅ | `gitea-5486569f5c-…-x-gitea-x-mgmt-vcluster 1/1 Running`; host `gitea` ns = only gitea-pg-1 (CNPG) | gitea tile is mgmt, not host |
| 103 | ✅ | `openbao-0-x-openbao-x-mgmt-vcluster 1/1 Running`; host `openbao` ns = No resources | openbao tile is mgmt, not host |
| 104 | ✅ | `newapi-bp-newapi-…-x-newapi-x-mgmt-vcluster 3/3 Running`; host `newapi` ns = only newapi-pg-1/2 (CNPG) | newapi tile is mgmt, not host (#3831) |
| 105 | ✅ | `guacd-…-x-mgmt-vcluster` + `guacamole-server-…-x-catalyst-system-…` synced from mgmt vCluster; host `guacamole` ns = No resources | guacamole tile is mgmt, not host |
| 106 | ✅ | mgmt-vcluster pods include grafana+harbor+keycloak+gitea+openbao+newapi+guacamole AND loki-0, mimir-*, nats-jetstream-*, tempo-0 — all `…-x-…-x-mgmt-vcluster` | all 7 named apps + loki/mimir/nats/tempo are in mgmt (drill-down content correct; tile drill is browser-only) |
| 107 | ✅ | host app namespaces: grafana/keycloak/openbao/guacamole = No resources; harbor/gitea/newapi = CNPG-db pod only, no app workload | none of the 7 named app WORKLOADS appear on host (only their bridged CNPG dbs) |
| 108 | ⚠️ | keycloak placement = mgmt is kubectl-true (row 101); the per-app CARD placement field render is browser-only | render not headless; underlying placement confirmed mgmt |
| 109 | ⚠️ | `GET auth.hw173/realms/sovereign/account`→302 (login redirect for the unauthenticated curl); `auth.hw173/`→200 lands `/admin/sovereign/console/`. Keycloak up. | account-console signed-in render needs a browser session; reachable + realm up |
| 110 | ✅ | `gitea.hw173/`→302→keycloak broker `catalyst-pin/login`, redirect_uri=`gitea.hw173/user/oauth2/openova-sso/callback`; gitea pod 1/1 | gitea up + SSO wired (idp=catalyst-pin); final landed-in render is browser-only |
| 111 | ❌ | `harbor.hw173/`→**404**; `harbor-core` pod `CreateContainerConfigError` (0/1), `harbor-jobservice` `CrashLoopBackOff` 64 restarts. Cause: env sources secret `harbor-database-secret-x-harbor-x-mgmt-vcluster` not present | **Harbor app is DOWN — projects list cannot render.** env-INDEPENDENT app defect |
| 112 | ✅ | `grafana.hw173/login/generic_oauth`→302→keycloak `…/auth?kc_idp_hint=catalyst-pin&client_id=grafana&redirect_uri=…grafana.hw173/login/generic_oauth`; grafana pod 3/3 | grafana up + SSO wired; final home-dashboard render is browser-only |
| 113 | ✅ | `bao.hw173/`→200 final `bao.hw173/sso/landing`; `openbao-0` pod 1/1 + openbao-sso-landing pod Running | OpenBao up, SSO landing serves (no raw unseal/token prompt blocking) |
| 114 | ✅ | `newapi.hw173/`→200 serves `<title>Signing you in…</title>` SSO auto-redirect page; `/api/status`→200; newapi pod 3/3 | newapi up, no upstream-connect-111 error (#3858 fix holds); login form replaced by SSO landing |
| 115 | ✅ | `guacamole.hw173/`→200 final `/guacamole/`; guacd + guacamole-server pods Running | guacamole up at app context; final connections-list render is browser-only |

## Orgs rows 116–122

**Directory data state (load-bearing):** there are **zero Organization CRs** on the cluster and the
directory API is empty:
```
GET /api/v1/organizations          -> 200 {"items":[]}
GET /api/v1/organizations/acme     -> 404
kubectl get organizations.orgs.openova.io -A  -> No resources found
  (CRD organizations.orgs.openova.io IS installed — created 2026-06-20T03:32:29Z — but has 0 CRs)
```
This is a fresh prov with no Organization onboarded yet (the funnel that creates `acme`/`walk-stranger`
runs in rows 80–95). So any row that needs a populated directory or the `acme`/`Acme Corp` detail page
cannot be satisfied: there is no org to render.

**Console route table + nav (shipped bundle `/assets/index-CXT0kSay.js`):**
```
nav item:   {id:`organizations`,label:`Organizations`,to:`/organizations`}
route alias: {path:`/bss`,to:`/organizations`}, {path:`/bss/tenants`,to:`/organizations`}, {path:`/bss/billing`,to:`/organizations`}
detail breadcrumb literal present: `← Organizations`
billing model literal present: billingMode:`showback` (internal→showback, external→real)
```

**Banned-term leaks found in the SAME bundle (adjacent BSS surfaces, not the org directory/detail):**
```
BSS Plans table column:  {label:`Tenants`,col:`tenants`}  (alongside Plan / MRR columns)
Domain-pool dropdown:    `sme-pool — offered to SME tenants`, `pool domains are offered to SME tenants`
Help text:               `Tenants get a real kubeconfig, namespaces…`
```

| Row | Verdict | Evidence (HTTP/JSON/kubectl) | Note |
|-----|---------|------------------------------|------|
| 116 | ⚠️ | `/organizations`→200 (SPA shell `<title>OpenOva Corporate</title>`); nav label `Organizations` in bundle; no host-side "Tenants" heading found for the directory | H1 text is client-rendered (browser-only); directory is empty (0 orgs). Heading-not-"Tenants" not headlessly confirmable |
| 117 | ❌ | Org directory column headers cannot be confirmed headless, BUT the shipped bundle ships a banned-term column `{label:`Tenants`,col:`tenants`}` on the BSS Plans screen + `SME tenants` pool strings | banned-term "Tenants"/"SME" still leak in the console bundle (Plans + domain-pool surfaces) — #3383 rename incomplete |
| 118 | ✅ | bundle nav item `{id:`organizations`,label:`Organizations`,to:`/organizations`}` | left-nav label reads "Organizations", not "Tenants"/"SME" |
| 119 | ⚠️ | create-organization form is browser-only; no org exists to compare against; create-flow strings not isolatable headless | form labels need a browser walk; cannot confirm/deny headless |
| 120 | ❌ | `GET /api/v1/organizations/acme`→**404**; 0 Organization CRs; directory `{"items":[]}` | **no `acme`/`Acme Corp` org exists — detail view (slug/kind/tier/billing/isolation) is unreachable** |
| 121 | ⚠️ | bundle ships `billingMode:`showback`` framing for internal orgs, but with 0 orgs the BSS/billing screen has no org to frame | "showback" model present in code; live screen needs an org (browser-only) |
| 122 | ⚠️ | `GET /bss/tenants`→200 (SPA shell, not 404, not login-redirect); bundle route table aliases `/bss/tenants`→`/organizations` | alias resolves (200, redirect wired in route table); the rendered H1 "Organizations" is client-side (browser-only) — directory would render empty |

## Summary

- Placement 96–115: **11 ✅ / 1 ❌ / 4 ⚠️**.
- Orgs 116–122: **1 ✅ / 2 ❌ / 4 ⚠️**.
- **Total: 12 ✅ / 3 ❌ / 8 ⚠️** (27 rows).

### FAILED rows
- **111** — Harbor app DOWN: `harbor-core` CreateContainerConfigError + `harbor-jobservice` CrashLoopBackOff(64); `harbor.hw173/`→404. Missing secret `harbor-database-secret-x-harbor-x-mgmt-vcluster`. env-independent app defect.
- **117** — banned-term leak: console bundle still ships a `Tenants` column (BSS Plans) + `SME tenants` domain-pool strings; #3383 rename incomplete on those surfaces.
- **120** — no `acme`/`Acme Corp` Organization exists (`/api/v1/organizations/acme`→404, 0 CRs); org-detail canonical fields unverifiable.

### ⚠️ rows — why
- 97/98/108 placement-treemap and per-app-card RENDER are browser-only; underlying placement (apps in mgmt vCluster) is kubectl-confirmed.
- 109 keycloak account-console + 116/119/121/122 orgs UI labels/heading/forms are client-rendered SPA — not readable from the static shell headless. 122's `/bss/tenants` alias resolves 200 (route wired) but the rendered H1 needs a browser. The orgs directory is also EMPTY (0 orgs) so these screens have nothing to populate.
