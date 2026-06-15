# UAT — ground reality on `hw139.omani.works` (live 2-region prov, 2026-06-15)

**Last verified live: 2026-06-15 on `hw139.omani.works`** (deployment `c89aa7059556b342`, 2-region
kom4dc prov — zones `me-east-215-a` / `-b`, SHARED_PG=true, catalyst-api `sha=9c6f864` / console
`bp-catalyst-platform@1.4.6xx`). The env is **converged and STILL WALKABLE post-cutover** (console 200,
newapi 200, pdns-admin 302→`/dashboard/`, registry 200). **Login: none** — a signed `/auth/handover`
token lands directly in the console as `emrah.baysal@openova.io` (role `sovereign-admin`); every app is
then opened at its **bare public URL** in the same browser session.

> **Wipe-and-rewalk contract:** every wipe empties this file; the rows below were all walked **on hw139**
> 2026-06-15 from the live UI via headless Playwright (Chromium) from `/home/openova/repos/ping`. Prior-env
> (hw133 / hw130 / hw128) results are void and were cleared. Every row: click the URL yourself; a row is ✅
> only if it works **right now** with a screenshot. Per-surface detail + screenshots live in
> [`docs/ledger/uat-walkthrough/`](uat-walkthrough/) and `docs/sessions/2026-06-15/evidence/`.

---

## 🌟 The 4 North Stars (founder verbatim) — PROVEN on hw139

| # | North Star (founder) | Proven on hw139 | Evidence |
|---|---|---|---|
| 1 | **Every app runs IN a vCluster** (placement law §4) | **22/22** — 9/9 re-homed apps verified in their target vCluster (nats/loki/mimir/tempo→mgmt, sandbox/valkey/seaweedfs/vllm→rtz, coraza→dmz); 17/17 §4-exception apps correctly held on `host` BY DESIGN (loft.sh-Free CR-sync would need a permanent tether — incompatible with Pillar-5 cutover); dashboard treemap LAYER1=vCluster renders the 4 groups host/mgmt/dmz/rtz | [3373-placement.md](uat-walkthrough/3373-placement.md) |
| 2 | **3 shared-PG instances → 3 cards; 6–7 apps many-to-many** | **3 cards + 11 contexts** — the `/apps` Deployments grid renders `shared-pg` (⛓ 3 contexts) / `shared-pg-b` (⛓ 3) / `shared-pg-c` (⛓ 5) as dedicated INSTALLED instance cards (`GET /api/v1/sovereign/apps` → `instance:true=4`); consumption is genuinely many-to-many (`shared-pg`←harbor/gitea/keycloak, `-b`←grafana/powerdns/powerdns-admin, `-c`←newapi/openova-flow+3 sme_*) | [3370-contexts.md](uat-walkthrough/3370-contexts.md) |
| 3 | **NO login UI anywhere — URL → signed in as emrah.baysal as ADMIN** | **10–11 / 12 zero-click admin landings** — console, grafana, gitea, harbor, openbao, keycloak (sovereign realm), guacamole, pdns-admin, hubble, marketplace all land signed-in/authenticated zero-click; proof = surfing each app's admin panel (Server Admin / Site Admin / Administration / realm-admin) as `emrah.baysal@openova.io`. The 1 genuine fail = **newapi** (zero-click OAuth fires but dead-ends on an "already-bound" non-idempotent re-login defect); openova-flow N/A (no UI by design) | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| 4 | **Agreed apps actually multi-region** | **Topology declaration 17/17** — every §6/mgmt-tier app renders the matrix DR class (`active-hot-standby (multi-region · 2 regions)`), state backend (`cnpg-pair · sync`), switchover owner (`bp-continuum`), RTO/RPO, and per-cluster ACTIVE/PASSIVE; the cnpg-pair Switchover button is enabled. **Region-kill EXECUTION** (live cross-region promote) was last witnessed on **hw128** (RTO ~3s, 0-loss); on hw139 the execution row is **deferred** this pass (no live Continuum CR driven — READ-ONLY) | [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) |

---

## Per-surface fractions (every row = a UI action → screen → ☐, sourced from the walkthroughs)

| Surface (issue) | Live result on hw139 | Walkthrough |
|---|---|---|
| **Placement** (#3373) | ✅ **22/22** (9 re-homed + 13 host-by-design rows + treemap) | [3373-placement.md](uat-walkthrough/3373-placement.md) |
| **Contexts** (#3370) | ✅ **14/14** — 3 instance cards + catalog ⛓ shareable + 11 contexts across 3 instances | [3370-contexts.md](uat-walkthrough/3370-contexts.md) |
| **SSO** (#3374) | ✅ **10 / ❌ 1 / N/A 1** (12 apps) — newapi the lone fail | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| **Topology / DR** (#3375) | ✅ **17/17** declaration; region-kill execution deferred (witnessed hw128) | [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) |
| **Jobs** (#3367) | ✅ **3/3** — `/jobs` renders, empty-filter search no longer white-screens (`0` `toLowerCase` crashes) | [3367-jobs.md](uat-walkthrough/3367-jobs.md) |
| **Organizations** (#3378) | ✅ **11/11** — one Organizations menu, parent-first directory, Internal-kind defaults, badge fidelity, `0` pageerrors | [3378-organizations.md](uat-walkthrough/3378-organizations.md) |
| **Robustness** (#3380) | ✅ **8/8** (+N/A 1) — 49 INSTALLED, 0 FAILED badges; both regions 4/4 Ready; cnpg-pair primary 3/3 + replica 3/3 healthy | [3380-robustness.md](uat-walkthrough/3380-robustness.md) |
| **Funnel — front half** (#3376) | ✅ **7/7** — anonymous storefront + full wizard (Plans→Stack→Add-ons→Topology→Review→Checkout) + OTP send (`/api/auth/magic-link` 200) | [3376-funnel.md](uat-walkthrough/3376-funnel.md) |
| **Funnel — back half** (#3376) | ❌ **0/7** — voucher-credit → provisioned-tenant completion not green this pass | [3376-funnel.md](uat-walkthrough/3376-funnel.md) |
| **Cutover** (#3379) | ⚠️ **9/11 (code-complete, kom4dc-infra-gated)** — engine steps 1–9 `result=success` (gitea-mirror, harbor-projects/prewarm, registry-pivot, flux GitRepository→local Gitea, ghcr-pull keyed `registry.hw139…`, env-patch, gitea-token-mint); step-08 witnessed deny-egress hold + step-10 `registryMirror` residual fixed at source (`bp-self-sovereign-cutover` 0.1.62) but `cutoverComplete=true` + the 600s `cutover-egress-block` CCNP are **PENDING that roll + a re-fire** — not yet witnessed on hw139. NO half-pivot; tethers intact | [3379-cutover.md](uat-walkthrough/3379-cutover.md) · [3379-sovereignty.md](uat-walkthrough/3379-sovereignty.md) |

**Honesty line:** the two genuine open gaps on hw139 are **newapi SSO** (advanced past the old `/setup`
wizard — `setup=true`, provider seeded, zero-click OAuth fires — but dead-ends on an "already-bound"
non-idempotent re-login defect; DB-confirmed bound user `sovereign_2` role=100) and the **funnel back half**
(voucher→provisioned-tenant). The **cutover** is code-complete on the engine (9/11 steps `success`, fixes
landed in chart 0.1.62) but the witnessed deny-egress hold + `cutoverComplete=true` remain blocked behind a
kom4dc roll-and-re-fire — stated honestly, not claimed green.

---

## SSO — type the URL → land signed in (zero clicks) — walked on hw139

| # | App | Try it | Now | Proof (hw139, 2026-06-15) |
|---|---|---|---|---|
| 1 | console | [open](https://console.hw139.omani.works/) | ✅ | handover → `/dashboard` signed-in as emrah, admin avatar **E**, full sidebar |
| 2 | grafana | [open](https://grafana.hw139.omani.works/admin/users) | ✅ | bare URL → Home signed-in; `/admin/users` → Server Admin Users (admin-only) |
| 3 | gitea | [open](https://gitea.hw139.omani.works/admin) | ✅ | bare URL → "emrah.baysal - Dashboard - Catalyst Gitea"; `/admin` → site Administration |
| 4 | harbor | [open](https://registry.hw139.omani.works/harbor/users) | ✅ | bare URL → `/harbor/projects`; `/harbor/users` → Administration, `emrah.baysal@openova.io` |
| 5 | openbao | [open](https://bao.hw139.omani.works/ui/) | ✅ | `/ui/` → `/ui/vault/secrets` Secrets Engines, privileged token, no auth form |
| 6 | keycloak (sovereign) | [open](https://auth.hw139.omani.works/admin/sovereign/console/) | ✅ | sovereign-realm admin console signed-in as emrah, full realm-admin nav |
| 7 | guacamole | [open](https://guacamole.hw139.omani.works/) | ✅ | OIDC handshake completes (id_token `groups=[sovereign-admins,…]`) → Connections home |
| 8 | pdns-admin | [open](https://pdns-admin.hw139.omani.works/) | ✅ | **FIXED #3547** — bare URL → `/dashboard/` signed-in, admin sidebar; hw133 500 gone |
| 9 | hubble | [open](https://hubble.hw139.omani.works/) | ✅ | oauth2-proxy gate silent → Hubble "select a namespace", live namespace list |
| 10 | marketplace | [open](https://marketplace.hw139.omani.works/) | ✅ | anonymous storefront renders, no forced pre-checkout login (its own contract) |
| 11 | newapi | [open](https://newapi.hw139.omani.works/) | ❌ | zero-click OAuth fires (no login form) but callback dead-ends **"already bound"** → `/console`→`/login?expired=true`; DB-confirmed bound user `sovereign_2`. `setup=true`, provider seeded |
| 12 | openova-flow | [open](https://openova-flow.hw139.omani.works/) | N/A | backend HTTP+SSE router, JSON service-descriptor at `/`, no UI by design (oidc-gate 200) |

**SSO score on hw139: 10/12 zero-click signed-in admin** (+1 N/A). The lone fail is newapi —
materially advanced from the prior `/setup`-wizard state, now blocked only on a re-login idempotency
defect. Full per-row evidence + screenshots: [3374-sso.md](uat-walkthrough/3374-sso.md) ·
`docs/sessions/2026-06-15/evidence/3374-hw139-refresh/`.

## Contexts — shared-PG instances render as cards + many-to-many (North Star #2) — walked on hw139

| Check | Where | Now | Proof (hw139) |
|---|---|---|---|
| 3 shared-PG instance **cards** on the grid | `/apps` Deployments | ✅ | `shared-pg` ⛓3 / `shared-pg-b` ⛓3 / `shared-pg-c` ⛓5, all INSTALLED (#3537 / PR #3553 live, `instance:true=4`) |
| PostgreSQL **catalog** card | `/apps` Catalog → search postgres | ✅ | `PostgreSQL · ⛓ shareable · multi-instance · db`, 3-row Instances table, + New instance wizard |
| Each instance's **Contexts** tab | `/app/shared-pg{,-b,-c}` | ✅ | `Context · Occupied by · Credential · Status`; 9 `ready` across the 3 instances; many-to-many |
| Consumer-side binding | `/app/bp-gitea` | ✅ | "Depends on: shared-pg / db:gitea" chip renders |

Full evidence: [3370-contexts.md](uat-walkthrough/3370-contexts.md) ·
`docs/sessions/2026-06-15/evidence/3370-hw139-refresh/`.

## Topology / DR — agreed apps multi-region (North Star #4) — walked on hw139

| Check | Where | Now | Proof (hw139) |
|---|---|---|---|
| cnpg-pair DR declaration | `/app/bp-cnpg-pair` | ✅ | `active-hot-standby (multi-region · 2 regions)` · `cnpg-pair · sync` · switchover `bp-continuum` · RTO/RPO 10s/0s · rtz-A ACTIVE / rtz-B PASSIVE; **Switchover button enabled** |
| mgmt-tier HA (keycloak/harbor/grafana) | `/app/bp-keycloak` etc. | ✅ | each renders `active-hot-standby (multi-region · 2 regions)` · `cnpg-pair · sync` · RTO/RPO 30s/0s |
| 2-region substrate | robustness (#3380) | ✅ | both regions 4/4 Ready; cnpg-pair primary (region-a) 3/3 + replica (region-b) 3/3 "healthy state" |
| Region-kill EXECUTION (live promote) | continuum switchover | ❌ deferred | no live Continuum CR driven this READ-ONLY pass; last witnessed live on **hw128** (RTO ~3s, 0-loss) |

Full evidence: [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) ·
`docs/sessions/2026-06-15/evidence/3375-hw139/`.

---

## What is NOT yet green on hw139 (honest open list)

1. **newapi zero-click landing** (#3374) — OAuth flow fires but re-login dead-ends on "already bound"
   (the bound `sovereign_2` user blocks the bind code path; needs the SPA/seed to take the *login* path
   for an already-bound identity). Single SSO fail of 12.
2. **Funnel back half** (#3376) — voucher-credit load → Purchase → provisioned tenant; front half (storefront
   + wizard + OTP) is 7/7 green.
3. **Cutover completion** (#3379) — engine 9/11 `success`, source fixes shipped (chart 0.1.62), but the
   witnessed 600s deny-egress hold + `cutoverComplete=true` are pending a kom4dc roll + re-fire. No
   half-pivot; the Sovereign stays fully intact.
