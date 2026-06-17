# UAT — hw158 live walkthrough (2026-06-17)

> **Env: `hw158.omani.works` · deployment `ab2135d4cf2d01e4` · 2026-06-17 · single physical
> kom4dc region (2 VPCs `me-east-215-a` / `-b`).** Fully converged: **61/64 HelmReleases Ready**,
> status=ready. The 3 not-Ready are Hetzner-only charts deliberately suspended on Huawei
> (`bp-cluster-autoscaler-hcloud`, `bp-hcloud-ccm`, `bp-velero`; `bp-velero-hcs` IS Ready) — the
> conformant TC-07 set, not failures.

**Walked live: 2026-06-17 on `hw158.omani.works`.** **Login: none** — a signed
`/auth/handover?token=<RS256-JWT>` minted from the mothership owner key (`/tmp/hw-priv.pem`, whose
public modulus byte-matches the on-cluster `handover-jwt-public/public.jwk`) lands directly in the
console as `emrah.baysal@openova.io` (role `sovereign-admin`); every app is then opened at its
**bare public URL** in the same browser session.

> **Wipe-and-rewalk contract:** every wipe empties this file; the rows below were walked **on
> hw158** 2026-06-17 from the live UI + `kubectl` on `/tmp/hw158-kc.yaml` (primary region A,
> server `212.72.24.26:6443`). Prior-env (hw144 / hw150 / hw130) results are void and were cleared.
> Screenshots: `docs/sessions/2026-06-17/evidence/hw158-*.png`. Per-surface walkthroughs:
> [`docs/ledger/uat-walkthrough/`](uat-walkthrough/).

---

## 🌟 The 4 North Stars (founder verbatim) — on hw158

| # | North Star (founder) | On hw158 | Evidence |
|---|---|---|---|
| 1 | **Every app runs IN a vCluster** (placement law §4) | ✅ PASS | `audit-placement-conformance.py live` → exit 0, "zero undeclared host workloads"; 31 pods carry the `-x-…-{mgmt,dmz,rtz}-vcluster` syncer suffix · `hw158-13` |
| 2 | **3 shared-PG instances → 3 cards; 6–7 apps many-to-many** | ✅ PASS | `/catalog/bp-postgres` Instances table = exactly 3 cards (shared-pg/-b/-c); `/app/shared-pg` Contexts = db/registry→harbor, db/gitea→gitea, db/keycloak→keycloak; -b/-c carry the same 3 DBs (9 bindings) · `hw158-07`/`hw158-08` |
| 3 | **NO login UI anywhere — URL → signed in as emrah.baysal as ADMIN** | ✅ PASS | console `/auth/handover`→`/dashboard` signed-in, avatar "Signed in as emrah.baysal@openova.io"; grafana/harbor/gitea/openbao all zero-click admin · `hw158-01`..`hw158-05` |
| 4 | **Agreed apps actually multi-region** | 🟡 PARTIAL | substrate ✅ — `/cloud` Region 2/2 + Cluster 2/2, both `me-east-215-a`/`-b` clusters; shared-pg WAL `streaming` ×2 replicas; cnpg-pair 3 instances. **Live region-kill BLOCKED** — cnpg-pair replica still 2/3 "Creating", continuum CR Degraded (LeaseHeld=False) → no armed cross-region standby to kill-promote yet · `hw158-06` |

---

## ROW 0 — MASTER PROOF (#3687, object model alive at runtime) — ✅ PASS

`kubectl --kubeconfig /tmp/hw158-kc.yaml get applications.apps.openova.io,organizations.orgs.openova.io -A`:

| Check | Result | Evidence |
|---|---|---|
| `applications.apps.openova.io` rows | **3 Ready** — `shared-pg`/`shared-pg-b`/`shared-pg-c`, blueprint `bp-postgres@0.2.2`, placement `active-hotstandby`, env `platform-bootstrap` | kubectl |
| `organizations.orgs.openova.io` rows | **0** — EXPECTED pre-signup (the parent-Sovereign Org is host-owned; TC-01 funnel mints the first tenant Org) | kubectl |
| 4 canonical CRDs present | ✅ `applications.apps` · `organizations.orgs` · `environments.catalyst` · `continuums.dr` | kubectl |
| controllers `1/1 Running` | ✅ all 7 in `catalyst-system` (api, application, catalog, environment, organization, ui, useraccess) + continuum-controller | kubectl |

> ✅ **PASS:** Application CRs return non-zero Ready rows; the canonical object model is alive at
> runtime (not a pod/HR projection). Organizations=0 is the expected pre-funnel state.

---

## The 14 UAT rows

| # | Row (ticket / NS) | Result | Evidence (hw158) |
|---|---|---|---|
| 0 | **Master proof** — object model alive (#3687) | ✅ PASS | 3 Application CRs Ready; 4 CRDs; 8 controllers Running |
| 1 | **SSO — console** zero-click → emrah.baysal admin (#3374 / NS3) | ✅ PASS | `/auth/handover`→`/dashboard`; avatar "Signed in as emrah.baysal@openova.io" · `hw158-01` |
| 2 | **SSO — grafana** (#3374 / NS3) | ✅ PASS | `grafana.…/?orgId=1` Home, no login form; `/api/user`→`login=emrah.baysal@openova.io isGrafanaAdmin=true` · `hw158-02` |
| 3 | **SSO — harbor** (#3374 / NS3) | ✅ PASS (minor) | `/harbor/projects` zero-click, Administration menu visible, user=emrah.baysal; `/api/v2.0/users/current`→`admin_role_in_auth:true` but `sysadmin_flag:false` on hw158 (login ✅; the OIDC admin-group→Harbor-sysadmin promote is a small follow-up) · `hw158-03` |
| 4 | **SSO — gitea** (#3374 / NS3) | ✅ PASS | title "emrah.baysal — Dashboard — Catalyst Gitea"; `/admin` → 200 (Site Administration reachable; non-admin would 403) · `hw158-04` |
| 5 | **SSO — openbao** (#3374 / NS3) | ✅ PASS | `/ui/vault/secrets` Secrets Engines (cubbyhole/, secret/) — **NO `/ui/vault/auth` token form** (founder-witnessed fail dead) · `hw158-05` |
| 6 | **SSO — bonus** (keycloak/guacamole/newapi/pdns-admin) | 🟡 2 PASS / 1 PARTIAL / 1 FAIL | keycloak-admin ✅ (Sovereign realm console, emrah.baysal) `hw158-12`; guacamole ✅ (OIDC auto-completes → Recent/All Connections, no Tomcat-404) `hw158-09`; newapi 🟡 route+OIDC fire but lands `/setup` init-wizard not `/console` (fresh-prov uninitialized) `hw158-10`; pdns-admin ❌ login wall → OIDC click → KC "Invalid parameter: redirect_uri" (client redirect-URI unregistered) `hw158-11` |
| 7 | **Placement** — every app in a vCluster (#3373 / NS1) | ✅ PASS | `audit-placement-conformance.py live` exit 0, "zero undeclared host workloads"; mgmt={loki,mimir,nats,tempo}, rtz={seaweedfs,valkey,vllm,sandbox}, dmz={coraza}; ratified route/secret apps on host conformant · `hw158-13` |
| 8 | **Contexts** — 3 shared-PG cards, many-to-many (#3370 / NS2) | ✅ PASS | `/catalog/bp-postgres` = 3 instance cards; `/app/shared-pg` Contexts tab = 3 isolated db/ children (harbor/gitea/keycloak); -b/-c each host gitea/keycloak/registry (9 bindings total) · `hw158-07`/`hw158-08` |
| 9 | **Topology Q1/Q2** — vocabulary + editable picker (#3375 / NS4) | ✅ PASS | `/app/shared-pg` Topology tab: Q1 declared "singleton — no cross-region failover" (honest); Q2 4-mode radio picker (single-region[checked]/active-active/active-hotstandby/active-passive) + both regions as checkboxes + Preview/Apply; Live-status honest "n/a — bootstrap component (HelmRelease, no Application CR)" · `hw158-15` |
| 10 | **Multi-region substrate** — 2/2 (#3375 / NS4) | ✅ PASS | `/cloud` Region 2/2 · Cluster 2/2 · vCluster 6/6 · Network 2/2; both `hw-me-east-215-a-rtz-prod` + `hw-me-east-215-b-rtz-prod` clusters in graph w/ own CP+3 workers · `hw158-06` |
| 11 | **Region-kill EXECUTION** — live promote (#3375 / NS4) | ❌ BLOCKED | continuum `cnpg-pair-…-continuum` phase=**Degraded**, LeaseHeld=False, Ready=False; cnpg-pair replica 2/3 "Creating a new replica" — cross-region standby not yet armed, so an honest kill→promote walk cannot run. shared-pg intra-instance WAL `streaming` ✅ but that is HA, not cross-region DR. NOT claimed green. |
| 12 | **Funnel (TC-01)** — voucher → app → Org non-zero (#3376 / NS1+NS3) | ⚠️ BLOCKED (substrate repaired) | Marketplace `marketplace.hw158.…/` "Build Your Tenant — OpenOva SME" serves (HTTP 200) · `hw158-14`; back-office `/back-office/` "Revenue — SME Admin" reachable. **Blocker found + fixed live:** SME `provisioning` was `Init:0/1` for 35 min — its `wait-for-gitea-token` init read an EMPTY `GITHUB_TOKEN` because `sme/provisioning-github-token` was never rendered (the chart mirrors it via a render-time `lookup` of `catalyst-system/catalyst-gitea-token`, whose 40-char PAT only became valid AFTER the initial render; helm-controller won't re-render on cluster-state-only change — a `reconcile.fluxcd.io/requestedAt` nudge did not recreate it). Mirrored the validated PAT (gitea `/api/v1/user`→200) into `provisioning-github-token`, deleted the stuck pod → **all 12 SME pods now 1/1 Running.** Full stranger redeem→checkout→Org-mint NOT completed (back-office voucher SPA renders empty; `console.<slug>.omani.homes` tenant-subdomain DNS pool is a further dependency); `kubectl get organizations` still **0** — **no Org fabricated.** |
| 13 | **Robustness** — install-record real, no crash-loop (#3380 / NS1) | ✅ PASS | 61/64 HR Ready; the 3 not-Ready are Hetzner-only suspended charts (autoscaler-hcloud, hcloud-ccm, velero; velero-hcs Ready); no crash-loop; `/dashboard` treemap renders 93 items · kubectl + `hw158-01` |

---

## SSO — type the URL → land signed in (zero clicks) — walked on hw158

| # | App | Bare URL | Now | Proof (hw158, 2026-06-17) |
|---|---|---|---|---|
| 1 | console | `console.hw158.omani.works/` | ✅ | handover→`/dashboard`; avatar "Signed in as emrah.baysal@openova.io" · `hw158-01` |
| 2 | grafana | `grafana.hw158.omani.works/` | ✅ | Home, `/api/user`→isGrafanaAdmin=true · `hw158-02` |
| 3 | harbor | `registry.hw158.omani.works/` | ✅ | `/harbor/projects`, Admin menu; sysadmin_flag minor follow-up · `hw158-03` |
| 4 | gitea | `gitea.hw158.omani.works/` | ✅ | "emrah.baysal — Dashboard"; `/admin`→200 · `hw158-04` |
| 5 | openbao | `bao.hw158.omani.works/ui/` | ✅ | `/ui/vault/secrets`, no token form · `hw158-05` |
| 6 | keycloak admin | `auth.hw158.omani.works/admin/sovereign/console/` | ✅ | Sovereign realm admin console (Clients/Users/Groups/Realm-settings nav); header "emrah.baysal@openova.io" · `hw158-12` |
| 7 | guacamole | `guacamole.hw158.omani.works/guacamole/` | ✅ | OIDC auto-completes from live KC session (`id_token` groups=sovereign-admins); "Recent Connections / All Connections" as emrah.baysal; NO Tomcat-404 · `hw158-09` |
| 8 | newapi | `newapi.hw158.omani.works/` | 🟡 | route + custom-OAuth `kc_idp_hint=catalyst-pin` flow fire and return, but the app lands on `/setup` ("System initialization" 4-step wizard) not a signed-in `/console` — fresh-prov uninitialized · `hw158-10` |
| 9 | pdns-admin | `pdns-admin.hw158.omani.works/` | ❌ | lands `/login` (username/password form + "Sign in using OpenID Connect" link — NOT zero-click); clicking OIDC → KC `realms/sovereign` returns **"Invalid parameter: redirect_uri"** (the `powerdns-admin` client has no `…/oidc/authorized` redirect-URI registered) · `hw158-11` |

**SSO core 5/5 ✅** (console, grafana, harbor, gitea, openbao) — all zero-click signed-in admin.
**SSO bonus 2 PASS (keycloak, guacamole) / 1 PARTIAL (newapi `/setup`) / 1 FAIL (pdns-admin redirect-uri).**

---

## What is NOT green on hw158 (honest open list)

1. **Region-kill EXECUTION (#3375 / NS4)** — ❌ BLOCKED. The cnpg-pair cross-region replica was
   still joining (2/3 "Creating") and the continuum CR was Degraded (LeaseHeld=False) at walk
   time, so there is no armed cross-region standby to hard-kill and promote. The multi-region
   *substrate* is proven (2/2 regions, cnpg-pair, shared-pg streaming) but the live promote was
   not walkable — and is NOT claimed.
2. **Funnel (TC-01, #3376)** — ⚠️ BLOCKED, substrate repaired. The SME `provisioning` Init-wedge
   (missing `sme/provisioning-github-token` → empty `GITHUB_TOKEN` → gitea 403 in `wait-for-gitea-token`)
   was root-caused and fixed live (validated PAT mirrored → all 12 SME pods now `1/1 Running`;
   marketplace landing + back-office serve). A full stranger voucher→checkout→Org-mint was NOT
   completed (back-office voucher SPA renders empty; tenant-subdomain DNS pool dependency).
   `kubectl get organizations` is still **0** — no Org was fabricated. This is the one terminal
   pillar gap on hw158.
3. **pdns-admin SSO (#3374 bonus)** — ❌ FAIL. Lands on a `/login` username/password form; the
   "Sign in using OpenID Connect" link → Keycloak returns **"Invalid parameter: redirect_uri"**.
   The `powerdns-admin` OIDC client in the `sovereign` realm is missing the
   `https://pdns-admin.hw158.omani.works/oidc/authorized` redirect-URI. Concrete client-config fix.
4. **newapi SSO (#3374 bonus)** — 🟡 PARTIAL. The route + custom-OAuth flow fire and return from
   Keycloak, but the app renders its `/setup` "System initialization" wizard instead of a signed-in
   `/console` (the instance is uninitialized on a fresh prov). Not zero-click-to-admin.
5. **harbor admin-group → sysadmin mapping (#3374, minor)** — login ✅ on hw158, `sysadmin_flag:false`;
   small OIDC-group-promote follow-up.
6. **Cutover (#3379 / NS5)** — handover-gated (`cutoverComplete=false` is the expected fresh-prov
   state; the operator drives "Achieve True Sovereignty" + the 600s deny-egress hold). Not walked
   this session.
