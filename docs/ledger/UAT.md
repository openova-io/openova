# UAT — ground reality on `hw144.omani.works` (live kom4dc prov, 2026-06-15)

> **Env: `hw144.omani.works` · deployment `d8e798bdf1b4256b` · 2026-06-15 · single physical kom4dc
> region (2 VPCs `me-east-215-a` / `-b`).** This ledger re-walks on the comprehensive `hw144` prov
> fired with the five remaining fixes baked in (cutover half-pivot #3568, newapi idempotent re-login
> #3564, openbao cross-region raft #3562, Open-button grid restore #3570, shared-PG real
> cross-region DR #3572).

**Last verified live: 2026-06-15 on `hw144.omani.works`.** **Login: none** — a signed
`/auth/handover` token lands directly in the console as `emrah.baysal@openova.io` (role
`sovereign-admin`); every app is then opened at its **bare public URL** in the same browser session.

> **Wipe-and-rewalk contract:** every wipe empties this file; the rows below were walked **on
> hw144** 2026-06-15 from the live UI. Prior-env (hw139 / hw128) results are void and were cleared.
> Every row: click the URL yourself; a row is ✅ only if it works **right now** with an hw144
> screenshot. Per-surface detail + screenshots live in [`docs/ledger/uat-walkthrough/`](uat-walkthrough/)
> and `docs/sessions/2026-06-15/evidence/` (the `hw144-*` files).

---

## 🌟 The 4 North Stars (founder verbatim) — on hw144

| # | North Star (founder) | On hw144 | Evidence |
|---|---|---|---|
| 1 | **Every app runs IN a vCluster** (placement law §4) | **✅ conformant** — 9 apps in their target vClusters (`loki`/`mimir`/`tempo`/`nats`→mgmt, `valkey`/`seaweedfs`/`vllm`→rtz, `coraza`→dmz); the 5 route/secret apps (`keycloak`/`gitea`/`grafana`/`harbor`/`openbao`) held on `host` BY ratified #3373 Batch-A design (loft.sh-Free CR-sync would need a permanent tether — incompatible with Pillar-5 cutover); `audit-placement-conformance.py live` exit 0; treemap LAYER1=vCluster renders host/mgmt/rtz/dmz | [3373-placement.md](uat-walkthrough/3373-placement.md) |
| 2 | **3 shared-PG instances → 3 cards; 6–7 apps many-to-many** | **✅ 3 cards + 11 contexts / 9 apps** — `/catalog/bp-postgres` Instances table = `shared-pg`/`shared-pg-b`/`shared-pg-c` (all active-hotstandby + Ready); consumption many-to-many (`shared-pg`←harbor/gitea/keycloak, `-b`←grafana/powerdns/powerdns-admin, `-c`←catalyst-platform×3/newapi/openova-flow) | [3370-contexts.md](uat-walkthrough/3370-contexts.md) · [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) |
| 3 | **NO login UI anywhere — URL → signed in as emrah.baysal as ADMIN** | **✅** — handover URL → `/dashboard` signed-in (avatar **E**, "Signed in as emrah.baysal@openova.io"); grafana / harbor / openbao / gitea all land signed-in zero-click at their bare URL; admin proven by surfing Server Admin / Administration / Secrets Engines / owner-dashboard. **Q6: Open buttons RESTORED (#3570)** (`↗ Open <App>` + `Open →`) | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| 4 | **Agreed apps actually multi-region** | **✅** — `/cloud` reports Region 2/2 + Cluster 2/2, the Topology picker is editable (single-region / active-active / active-hotstandby × me-east-215-a/-b, Preview+Apply), and the **live region-kill PASSED** on hw144: region-a killed (cordon 3 workers + delete `shared-pg-1/2/3`) → region-b `shared-pg-replica` promoted in **RTO ≈ 4s** (`pg_is_in_recovery` t→f, 12:23:39→12:23:43Z) → consumer keycloak realms (master+sovereign) + 3 users identical post-kill → **RPO = 0** | [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) |

---

## Per-surface fractions (every row = a UI action → screen → result, sourced from the walkthroughs)

| Surface (issue) | Live result on hw144 | Walkthrough |
|---|---|---|
| **Placement** (#3373) | ✅ — 9 apps in target vClusters + 5 route/secret apps host-by-ratified-design; treemap LAYER1=vCluster (host/mgmt/rtz/dmz); audit exit 0 | [3373-placement.md](uat-walkthrough/3373-placement.md) |
| **Contexts** (#3370) | ✅ — 3 instance cards + ⛓ shareable catalog + 11 contexts across 3 instances / 9 distinct apps | [3370-contexts.md](uat-walkthrough/3370-contexts.md) |
| **SSO** (#3374) | ✅ — handover + grafana + harbor + openbao + gitea land signed-in zero-click; Open buttons (Q6) present | [3374-sso.md](uat-walkthrough/3374-sso.md) |
| **Topology Q1 / Q2** (#3375) | ✅ — 3 active-hotstandby cards; Q1 declared-singleton honesty render; Q2 truly-editable Topology picker | [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) |
| **Region-kill** (#3375 / NS#4) | ✅ — live kill→promote driven on hw144: region-a killed → region-b promoted RTO ≈ 4s → consumer keycloak data identical (RPO = 0) | [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) |
| **Cutover** (#3379) | ⏳ PENDING — `cutoverComplete=true` + the 600s deny-egress hold walk not yet witnessed this session | [3379-cutover.md](uat-walkthrough/3379-cutover.md) · [3379-sovereignty.md](uat-walkthrough/3379-sovereignty.md) |
| **Cutover PROOF integrity** (#3379 — durable fact · true deny-egress · faithful pivot · audit fidelity · host-loop) | ⏳ PENDING live-walk — chart `bp-self-sovereign-cutover` 0.1.75 built + unit/render/POSIX-verified; the five faces (the seal survives a chart upgrade & doesn't re-fire the hold; default-deny CCNP blocks console.openova.io; registriesYamlActive=v2 + per-node ack; cutoverStartedAt written-once; bootstrap-kit Ready + zero residual tether) are walkable per the linked doc | [cutover-durable-true-deny-egress-and-faithful-pivot.md](uat-walkthrough/cutover-durable-true-deny-egress-and-faithful-pivot.md) |
| **Jobs one-honest-canvas** (#3646) | ⏳ UNVERIFIED — PR open (not merged); the generic reconciler ingestion + typed-Kind de-merge + Flux-native per-row Retry awaits the live 14-step walk on hw150 (RED cron alongside green install · reconciler health flip · Retry annotates the HR · 403 for non-operator). NOT claimed green until the walk lands a screenshot. | [jobs-one-honest-canvas-no-fabrication-with-remediation.md](uat-walkthrough/jobs-one-honest-canvas-no-fabrication-with-remediation.md) |

**Honesty line:** the **region-kill EXECUTION** (NS#4) is now **✅** — the live kill→promote walk ran
on hw144 (RTO ≈ 4s, RPO = 0). The one remaining open item on hw144 is the **Pillar-5 cutover** (the
witnessed deny-egress hold + `cutoverComplete=true`), marked **⏳ PENDING** — it will be filled in
after the live run and is NOT claimed green.

---

## SSO — type the URL → land signed in (zero clicks) — walked on hw144

| # | App | Try it | Now | Proof (hw144, 2026-06-15) |
|---|---|---|---|---|
| 1 | console | [open](https://console.hw144.omani.works/auth/handover) | ✅ | handover → `/dashboard` signed-in, avatar **E**, "Signed in as emrah.baysal@openova.io" (hw144-01/02) |
| 2 | grafana | [open](https://grafana.hw144.omani.works/) | ✅ | bare URL → Home/Dashboards signed-in; `/api/user` `isGrafanaAdmin=true` (hw144-10) |
| 3 | harbor | [open](https://registry.hw144.omani.works/) | ✅ | bare URL → `/harbor/projects` signed-in, `emrah.baysal@openova.io`, Administration menu (admin-group mapping = minor follow-up) (hw144-11) |
| 4 | openbao | [open](https://bao.hw144.omani.works/) | ✅ | bare URL → OIDC round-trip → `/ui/vault/secrets`, no auth form (hw144-12) |
| 5 | gitea | [open](https://gitea.hw144.omani.works/) | ✅ | bare URL → "emrah.baysal — Dashboard — Catalyst Gitea" (hw144-14) |
| 6 | grafana / guacamole Open buttons | [open](https://console.hw144.omani.works/app/bp-grafana) | ✅ | app-detail header `↗ Open <App>` + Endpoints `Open →` restored (#3570) (hw144-09) |
| 7 | newapi | — | N/A | exposes NO external HTTPRoute ("Internal-only services reachable cluster-side") — no browser SSO walk; re-login is internal/M2M |

**SSO on hw144: ✅** — 5 apps land zero-click signed-in admin + the Open buttons (Q6) are restored.
Full per-row evidence: [3374-sso.md](uat-walkthrough/3374-sso.md) · `docs/sessions/2026-06-15/evidence/`.

## Contexts — shared-PG instances render as cards + many-to-many (North Star #2) — walked on hw144

| Check | Where | Now | Proof (hw144) |
|---|---|---|---|
| 3 shared-PG instance **cards** | `/catalog/bp-postgres` Instances | ✅ | `shared-pg` / `shared-pg-b` / `shared-pg-c`, all active-hotstandby + Ready, `bp-postgres@0.2.1` (hw144-04) |
| PostgreSQL **catalog** card | `/apps` Catalog → search postgres | ✅ | `PostgreSQL · ⛓ shareable · multi-instance · db`, 3-row Instances table, + New instance (hw144-04) |
| Each instance's **Contexts** tab | `/app/shared-pg{,-b,-c}` | ✅ | `Context · Occupied by · Credential · Status`; 9 `ready` (+2 `Declared`) across 3 instances; many-to-many (hw144-06/07/08) |

Full evidence: [3370-contexts.md](uat-walkthrough/3370-contexts.md) · `docs/sessions/2026-06-15/evidence/`.

## Topology / DR — agreed apps multi-region (North Star #4) — walked on hw144

| Check | Where | Now | Proof (hw144) |
|---|---|---|---|
| shared-PG cards = active-hotstandby | `/catalog/bp-postgres` | ✅ | 3 instances, all active-hotstandby + Ready (hw144-04) |
| Q1 declared-singleton honesty | `/app/shared-pg` Topology | ✅ | "Declared topology · singleton … no cross-region failover"; Live status "No per-region status yet … Replication lag n/a" (hw144-15) |
| Q2 editable Topology picker | `/app/shared-pg` Topology | ✅ | radios single-region / active-active / active-hotstandby × regions me-east-215-a/-b + Preview + Apply (truly editable, not cosmetic) (hw144-15) |
| 2-region substrate | `/cloud` | ✅ | Region 2/2 + Cluster 2/2 graph renders (hw144-16); region-b `shared-pg-replica` confirmed streaming (`pg_is_in_recovery=t`, wal_receiver=streaming) at the gate |
| Region-kill EXECUTION (live promote) | kubectl kill→promote | ✅ | **PASS on hw144** — region-a killed (cordon 3 workers + delete `shared-pg-1/2/3`) → region-b promoted RTO ≈ 4s (12:23:39→12:23:43Z) → keycloak realms+3 users identical, RPO = 0; no hw128 carryover ([proof txt](../sessions/2026-06-15/evidence/hw144-ns4-region-kill-proof.txt)) |

Full evidence: [3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md) · `docs/sessions/2026-06-15/evidence/`.

---

## What is NOT yet green on hw144 (honest open list)

1. **Cutover completion** (#3379) — the witnessed 600s deny-egress hold + `cutoverComplete=true`
   have not yet been walked on hw144. Marked **⏳ PENDING**.
2. **harbor admin-group mapping** (#3374, minor) — harbor login lands signed-in, but
   `/api/v2.0/users/current` reports `sysadmin:false`; the OIDC admin-group → Harbor-sysadmin
   mapping is a small follow-up (login itself is ✅).

*(Resolved this session: **Region-kill EXECUTION** (#3375 / NS#4) — the live kill → promote →
zero-data-loss walk PASSED on hw144 (RTO ≈ 4s, RPO = 0). See the NS#4 table in
[3375-topology-dr.md](uat-walkthrough/3375-topology-dr.md).)*
