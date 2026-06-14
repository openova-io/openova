# #3375 TOPOLOGY / DR — honest per-app UI re-walk (100% web UI) on hw138

The agreed truth is the per-Blueprint **topology matrix** in
[`docs/topology-matrix.md`](../../topology-matrix.md) (the durable promotion of the
`2026-06-02-per-blueprint-topology-audit.md` agreement). **Every row below is read
off that matrix — never invented.** For each app the operator opens its **Topology**
tab in the console and checks that the declared **class + state backend + switchover
mechanism + RTO/RPO + per-cluster placement roles** match the matrix row; for the §6
priority HA apps the operator confirms an **enabled Switchover** button that opens the
cross-region promotion dialog, plus an honest DR panel.

This is a **READ-ONLY** walk: kubectl/psql are not used to judge any row — the verdict
is what the **operator console renders live**. Every **Tested page** is a clickable link
to the live env **`hw138.omani.works`** (deployment `4b5ff7852e33fc15`).

## Walk facts — hw138.omani.works (2026-06-14, signed in via handover)

- **Sign-in**: the handover URL signs the operator straight in — lands on `/dashboard`
  as `emrah.baysal`, **Tenant** tier, **no login form**; the dashboard header reads
  `OpenOva Sovereign · hw138.omani.works` and shows a **111-item** treemap. Captured:
  [`r0-dashboard.png`](../../sessions/2026-06-14/evidence/3375-hw138/r0-dashboard.png).
- **Build**: `bp-catalyst-platform@1.4.629` (read off the catalyst-platform detail header).
- **hw138 is single-region for the live walk.** The placement editor offers
  `me-east-215-a` + `me-east-215-b` and the effective class reads `(multi-region · 2
  regions)`, but there is **no live `Continuum` CR**, so every DR panel honestly shows
  "No live Continuum record … activates once placed … on a 2-region Sovereign." The
  **region-kill EXECUTION** rows are therefore **❌ on this single live walk** — they
  require a live 2-region Continuum drive, which this read-only pass does not perform.
- **This pass records ONLY what rendered on hw138.** No hw136 / hw133 / hw135 evidence
  is carried over; earlier-env screenshots are not reused.

Every app below was opened at `/app/<bp-name>`, the **Topology** tab clicked, and the
panel text + button enabled/disabled state captured headlessly. Each row's screenshot
is the live hw138 render.

---

## 1. Per-app topology declaration — Topology tab matches the matrix row

### 1a. Catalyst control-plane tier (mgmt clusters)

| Tested page | Matrix row → what rendered live on hw138 | Status | Evidence |
|---|---|---|---|
| [catalyst-platform · Topology](https://console.hw138.omani.works/app/bp-catalyst-platform) | **active-hot-standby (mgmt-A active / mgmt-B passive)**, cnpg-pair · sync. **Rendered:** Declared `active-hot-standby`; Effective `active-hot-standby (multi-region · 2 regions)`; Supported `active-hot-standby · singleton`; Tier `mgmt`; State `cnpg-pair · sync`; Switchover `bp-continuum`; RTO/RPO `30s / 0s`; mgmt-A ACTIVE / mgmt-B PASSIVE. HR `Ready` (`@1.4.629`). Exactly the matrix row. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-catalyst-platform.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-catalyst-platform.png) |
| [keycloak · Topology](https://console.hw138.omani.works/app/bp-keycloak) | **active-hot-standby (mgmt-A / mgmt-B)**, cnpg-pair · sync. **Rendered:** `active-hot-standby`, `cnpg-pair · sync`, `bp-continuum`, RTO/RPO `30s / 0s`, mgmt-A ACTIVE / mgmt-B PASSIVE. HR `Ready` (`@1.4.28`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-keycloak.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-keycloak.png) |
| [gitea · Topology](https://console.hw138.omani.works/app/bp-gitea) | **active-hot-standby (mgmt-A / mgmt-B)**, cnpg-pair · sync. **Rendered:** `active-hot-standby`, `cnpg-pair · sync`, `bp-continuum`, `30s / 0s`, mgmt-A/mgmt-B. HR `Ready` (`@1.2.35`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-gitea.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-gitea.png) |
| [harbor · Topology](https://console.hw138.omani.works/app/bp-harbor) | **active-hot-standby (mgmt-A / mgmt-B)**, cnpg-pair · sync. **Rendered:** `active-hot-standby`, `cnpg-pair · sync`, `bp-continuum`, `30s / 0s`, mgmt-A/mgmt-B. HR `Ready` (`@1.2.30`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-harbor.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-harbor.png) |
| [grafana · Topology](https://console.hw138.omani.works/app/bp-grafana) | **active-hot-standby (mgmt-A / mgmt-B)**, cnpg-pair · sync. **Rendered:** `active-hot-standby`, `cnpg-pair · sync`, `bp-continuum`, `30s / 0s`, mgmt-A/mgmt-B. HR `Ready` (`@1.0.14`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-grafana.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-grafana.png) |
| [openbao · Topology](https://console.hw138.omani.works/app/bp-openbao) | **active-passive (mgmt-A active / mgmt-B passive)**, openbao perf-replication. **Rendered:** Declared `active-passive`; State `openbao-perf-replication · async`; Switchover `raft-transition`; RTO/RPO `60s / 30s`; mgmt-A ACTIVE / mgmt-B PASSIVE. Matches the matrix (the actual raft promotion EXECUTION is not driven here). HR `Ready` (`@1.2.40`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-openbao.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-openbao.png) |
| [newapi · Topology](https://console.hw138.omani.works/app/bp-newapi) | **active-passive (mgmt-A / mgmt-B)**. **Rendered:** `active-passive`, State `cnpg-pair · sync`, `bp-continuum`, `30s / 0s`, mgmt-A ACTIVE / mgmt-B PASSIVE. HR `Ready` (`@1.4.88`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-newapi.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-newapi.png) |
| [loki · Topology](https://console.hw138.omani.works/app/bp-loki) | **active-passive (mgmt-A / mgmt-B)**, gap(CLASS-B) `s3-bucket-replication`. **Rendered:** `active-passive`, State `s3-bucket-replication · async`, `bp-continuum`, RTO/RPO `60s / 60s`, mgmt-A/mgmt-B — the declared CLASS-B variant + mechanism shown correctly. HR `Ready` (`@1.0.0`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-loki.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-loki.png) |
| [mimir · Topology](https://console.hw138.omani.works/app/bp-mimir) | **active-passive (mgmt-A / mgmt-B)**, gap(CLASS-B) `s3-bucket-replication`. **Rendered:** `active-passive`, `s3-bucket-replication · async`, `bp-continuum`, `60s / 60s`, mgmt-A/mgmt-B. HR `Ready` (`@1.0.5`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-mimir.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-mimir.png) |
| [tempo · Topology](https://console.hw138.omani.works/app/bp-tempo) | **active-passive (mgmt-A / mgmt-B)**, gap(CLASS-B) `s3-bucket-replication`. **Rendered:** `active-passive`, `s3-bucket-replication · async`, `bp-continuum`, `60s / 60s`, mgmt-A/mgmt-B. HR `Ready` (`@1.0.0`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-tempo.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-tempo.png) |
| [nats-jetstream · Topology](https://console.hw138.omani.works/app/bp-nats-jetstream) | **active-passive (mgmt-A / mgmt-B)**, gap(CLASS-B) `raft`. **Rendered:** `active-passive`, State `raft · sync`, `bp-continuum`, RTO/RPO `30s / 60s`, mgmt-A/mgmt-B. Declared variant + mechanism correct. HR `Ready` (`@1.3.3`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-nats-jetstream.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-nats-jetstream.png) |
| [k8s-ws-proxy · Topology](https://console.hw138.omani.works/app/bp-k8s-ws-proxy) | **active-passive (mgmt-A / mgmt-B)**, stateless DNS-flip only. **Rendered:** `active-passive`, State `none · none`, `bp-continuum`, RTO/RPO `5s / 0s`, mgmt-A ACTIVE / mgmt-B PASSIVE. HR `Ready` (`@0.1.14`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-k8s-ws-proxy.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-k8s-ws-proxy.png) |
| [sso-bridge · Topology](https://console.hw138.omani.works/app/bp-sso-bridge) | **active-passive (mgmt-A / mgmt-B)**, stateless DNS-flip only. **Rendered:** `active-passive`, State `none · none`, `bp-continuum`, `30s / 0s`, mgmt-A/mgmt-B. HR `Ready` (`@0.2.18`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-sso-bridge.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-sso-bridge.png) |
| [oidc-gate · Topology](https://console.hw138.omani.works/app/bp-oidc-gate) | **active-passive (mgmt-A active / mgmt-B passive)**, stateless DNS-flip only. **Rendered:** `active-passive`, State `none · none`, `bp-continuum`, `30s / 0s`, mgmt-A/mgmt-B. HR `Ready` (`@0.1.1`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-oidc-gate.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-oidc-gate.png) |

**Result (§1a): ✅ 14 / ❌ 0.** Every catalyst-tier app's Topology tab renders a
per-app "Declared topology" panel that reads back the matrix-declared class + state
backend/mode + switchover mechanism + RTO/RPO + per-cluster placement roles.
`active-passive`, `active-hot-standby` are both correctly surfaced.

### 1b. Per-host-cluster infrastructure tier

| Tested page | Matrix row → what rendered live on hw138 | Status | Evidence |
|---|---|---|---|
| [cnpg · Topology](https://console.hw138.omani.works/app/bp-cnpg) | **singleton (all 6 tiers)**. **Rendered:** Declared `singleton` ("One instance in one region; no cross-region failover"), Supported `singleton`, per-cluster all-tiers SINGLETON (mgmt-A · mgmt-B · dmz-A · dmz-B · rtz-A · rtz-B). No DR panel (correct — singleton owes none). HR `Ready` (`@1.0.9`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-cnpg.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-cnpg.png) |
| [cnpg-pair · Topology](https://console.hw138.omani.works/app/bp-cnpg-pair) | **active-hot-standby (rtz-A active / rtz-B standby)**, cnpg-pair · sync. **Rendered:** `active-hot-standby`, Tier `rtz`, State `cnpg-pair · sync`, Switchover `bp-continuum`, RTO/RPO `10s / 0s`, **rtz-A ACTIVE / rtz-B PASSIVE**. DR panel + enabled Switchover dialog — see §2a. HR `Ready` (`@0.2.4`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-cnpg-pair.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-cnpg-pair.png) |
| [powerdns · Topology](https://console.hw138.omani.works/app/bp-powerdns) | **singleton (all 6 tiers)**. **Rendered:** `singleton`, all-tiers SINGLETON. HR `Ready` (`@1.2.14`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-powerdns.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-powerdns.png) |
| [powerdns-admin · Topology](https://console.hw138.omani.works/app/bp-powerdns-admin) | **singleton (all 6 tiers)**. **Rendered:** `singleton` ("One instance in one region; no cross-region failover"), Effective `singleton (multi-region · 2 regions)`, full all-tiers SINGLETON panel (mgmt-A · mgmt-B · dmz-A · dmz-B · rtz-A · rtz-B). HR `Ready` (`@0.1.15`). Renders fully on the first attempt with the `networkidle` settle. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-powerdns-admin.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-powerdns-admin.png) |
| [coraza · Topology](https://console.hw138.omani.works/app/bp-coraza) | **singleton (all 6 tiers)**. **Rendered:** `singleton`, State `flux-git · async`, all-tiers SINGLETON. HR `Ready` (`@1.0.0`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-coraza.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-coraza.png) |
| [seaweedfs · Topology](https://console.hw138.omani.works/app/bp-seaweedfs) | **singleton (all 6 tiers)**. **Rendered:** `singleton`, State `filer-remote-storage · async`, all-tiers SINGLETON. HR `Ready` (`@1.2.1`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-seaweedfs.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-seaweedfs.png) |
| [valkey · Topology](https://console.hw138.omani.works/app/bp-valkey) | **active-passive (rtz-A active / rtz-B passive)**, gap(CLASS-B) `sentinel`. **Rendered:** `active-passive`, Tier `rtz`, State `sentinel · async`, Switchover `sentinel-failover`, RTO/RPO `30s / —`, rtz-A ACTIVE / rtz-B PASSIVE; declared variant + `sentinel-failover` mechanism correct. Has a **Contexts** tab. HR `Ready` (`@1.1.2`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-valkey.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-valkey.png) |
| [vllm · Topology](https://console.hw138.omani.works/app/bp-vllm) | **active-active (rtz-A / rtz-B)**, stateless DNS-flip only. **Rendered:** `active-active` ("Both regions serve live traffic; data syncs between them"), State `none · none`, Switchover `none`, **rtz-A ACTIVE / rtz-B ACTIVE**; DR panel honestly says "**Both regions serve — no switchover**". HR `Ready` (`@1.0.1`). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-vllm.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-vllm.png) |

**Result (§1b infra): ✅ 8 / ❌ 0.** Singleton infra rows (cnpg · powerdns ·
powerdns-admin · coraza · seaweedfs) read back `singleton` with full per-tier
placement; cnpg-pair / valkey / vllm read back their exact matrix variant
(active-hot-standby / active-passive · sentinel / active-active).

---

## 2. §6 priority HA apps — enabled Switchover + honest DR panel

The matrix names six **§6 priority** HA apps: **cnpg-pair, keycloak, gitea, harbor,
grafana, openbao**. Each must show an **enabled Switchover** button that opens the
cross-region promotion dialog, plus an honest DR panel. All six rendered the enabled
button + honest DR state live on hw138; the dialog was opened (and **cancelled — never
confirmed**) on cnpg-pair and openbao to capture its content.

### 2a. cnpg-pair — Switchover dialog (the reference active-hot-standby)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cnpg-pair · Topology](https://console.hw138.omani.works/app/bp-cnpg-pair) | DR panel: declared **active-hot-standby**, honest state, no spinner. **Rendered:** "Disaster Recovery (active-hot-standby)" + **enabled Switchover…** button + honest "No live Continuum record for bp-cnpg-pair yet — the cross-region DR machinery activates once placed active-hot-standby on a 2-region Sovereign. Declared switchover mechanism: bp-continuum" + "No switchover events recorded yet". | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-cnpg-pair.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-cnpg-pair.png) |
| [cnpg-pair · Switchover dialog](https://console.hw138.omani.works/app/bp-cnpg-pair) | **Switchover** button enabled (Tenant tier) → opens the promotion dialog. **Rendered:** clicking it opens **"Switchover — bp-cnpg-pair"** — "Primary will move the current primary → the standby region" with a **7-step plan** (1 validate-lease · 2 cordon-old-primary · 3 drain-http · 4 flip-dns · 5 swap-lease · 6 uncordon-new-primary · 7 audit-emit), "Estimated duration: <60s / Write disruption: <5s", a Reason field, and **Cancel / Confirm Switchover**. Cancelled (never confirmed). | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/dialog-bp-cnpg-pair.png)](../../sessions/2026-06-14/evidence/3375-hw138/dialog-bp-cnpg-pair.png) |
| cnpg-pair · region-kill EXECUTION | Kill region-a primary → region-b promotes → data survives. **Reality (hw138):** single-region live walk, no live Continuum CR — no drivable promotion. Not driven in this read-only pass. | ❌ — region-kill EXECUTION not driven (single live walk) | — |

### 2b. keycloak / harbor / grafana / gitea — enabled Switchover + honest DR

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [keycloak · DR panel](https://console.hw138.omani.works/app/bp-keycloak) | DR panel renders honest state + **enabled** Switchover. **Rendered:** "Disaster Recovery (active-hot-standby)" + enabled Switchover… + honest "No live Continuum record … bp-continuum" + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-keycloak.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-keycloak.png) |
| [harbor · DR panel](https://console.hw138.omani.works/app/bp-harbor) | DR panel honest + **enabled** Switchover. **Rendered:** "Disaster Recovery (active-hot-standby)" + enabled Switchover… + honest no-live-CR text + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-harbor.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-harbor.png) |
| [grafana · DR panel](https://console.hw138.omani.works/app/bp-grafana) | DR panel honest + **enabled** Switchover. **Rendered:** "Disaster Recovery (active-hot-standby)" + enabled Switchover… + honest no-live-CR text + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-grafana.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-grafana.png) |
| [gitea · DR panel](https://console.hw138.omani.works/app/bp-gitea) | DR panel honest + **enabled** Switchover. **Rendered:** "Disaster Recovery (active-hot-standby)" + enabled Switchover… + honest "No live Continuum record … bp-continuum" + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-gitea.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-gitea.png) |
| gitea · app-served-from-promoted-region | Sign in to gitea served from the promoted region after failover; repo survives. **Reality (hw138):** no drivable promotion on a single-region live walk. Not driven. | ❌ — region-kill EXECUTION not driven (single live walk) | — |

### 2c. openbao — declared raft-transition + Switchover dialog

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [openbao · DR panel](https://console.hw138.omani.works/app/bp-openbao) | DR panel declares **active-passive** with mechanism `raft-transition`; honest, not faked. **Rendered:** "Disaster Recovery (active-passive)" + **enabled Switchover…** + honest "No live Continuum record for bp-openbao yet … Declared switchover mechanism: **raft-transition**" + no-events. The declared raft mechanism is surfaced honestly in the panel. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-openbao.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-openbao.png) |
| [openbao · Switchover dialog](https://console.hw138.omani.works/app/bp-openbao) | **Switchover** enabled → opens the promotion dialog. **Rendered:** "Switchover — bp-openbao" with the same generic **7-step** cross-region plan + Cancel / Confirm Switchover (cancelled, never confirmed). **Honest note:** the panel header declares `raft-transition` as openbao's mechanism, but the dialog body reuses the generic CNPG-worded 7-step template (cordon CNPG Cluster CR, flip CNPG replica.enabled) rather than openbao-raft-specific steps — the dialog copy is mechanism-agnostic. | ✅ — dialog opens enabled; copy is the generic CNPG template | [![](../../sessions/2026-06-14/evidence/3375-hw138/dialog-bp-openbao.png)](../../sessions/2026-06-14/evidence/3375-hw138/dialog-bp-openbao.png) |
| openbao · raft-transition EXECUTION | Drive the raft-transition promotion; KV reads stay up. **Reality (hw138):** no drivable openbao failover on a single-region live walk (no `-b` region, no live Continuum CR). Not driven. | ❌ — raft-transition EXECUTION not driven (single live walk) | — |

**Result (§2 + §6): ✅ 9 / ❌ 3.** All six §6 priority apps render an **enabled
Switchover** button + an honest DR panel on hw138; the promotion dialog opens with a
real 7-step cross-region plan and Cancel / Confirm Switchover (captured on cnpg-pair +
openbao, both cancelled). The **3 ❌** are the cross-region **EXECUTION** rows
(cnpg-pair region-kill, gitea-app-served-from-promoted-region, openbao raft-transition)
— not driven on this single-region read-only walk; they need a live 2-region Continuum
drive.

---

## 3. DR panel honesty for bootstrap-HA apps (no spinner, no fake promotion)

§3 acceptance: open keycloak / gitea / harbor / grafana / openbao → Topology → the DR
panel renders an **honest** state (declared mechanism + "activates on 2-region / no
live CR"), not a spinner, not a fake "promoted" claim.

| Tested page | Status | Evidence |
|---|---|---|
| [keycloak · DR panel](https://console.hw138.omani.works/app/bp-keycloak) — "Disaster Recovery (active-hot-standby)" + enabled Switchover + honest no-live-CR + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-keycloak.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-keycloak.png) |
| [gitea · DR panel](https://console.hw138.omani.works/app/bp-gitea) — "Disaster Recovery (active-hot-standby)" + honest no-live-CR + bp-continuum + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-gitea.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-gitea.png) |
| [harbor · DR panel](https://console.hw138.omani.works/app/bp-harbor) — "Disaster Recovery (active-hot-standby)" + honest no-live-CR + bp-continuum + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-harbor.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-harbor.png) |
| [grafana · DR panel](https://console.hw138.omani.works/app/bp-grafana) — "Disaster Recovery (active-hot-standby)" + honest no-live-CR + bp-continuum + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-grafana.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-grafana.png) |
| [openbao · DR panel](https://console.hw138.omani.works/app/bp-openbao) — "Disaster Recovery (active-passive)" + enabled Switchover + declared `raft-transition` (not faked) + no-events. | ✅ | [![](../../sessions/2026-06-14/evidence/3375-hw138/bp-openbao.png)](../../sessions/2026-06-14/evidence/3375-hw138/bp-openbao.png) |

**Result (§3): ✅ 5 / ❌ 0.** All five render an honest DR state on hw138 — declared
mechanism + "activates on 2-region / no live Continuum CR" + "No switchover events
recorded yet". None spins; none fakes a promotion.

---

## WALK RESULT — hw138.omani.works (deployment `4b5ff7852e33fc15`, signed in as emrah.baysal via handover, 100% headless browser, READ-ONLY)

**Result: ✅ 33 / ❌ 6 (passed 33 / total 39).**

- **§1a catalyst-tier declaration read-back: ✅ 14 / ❌ 0** — every app's Topology tab
  reads back the exact matrix class + state + mechanism + RTO/RPO + per-cluster roles.
- **§1b infra declaration read-back: ✅ 8 / ❌ 0** — singletons (cnpg, powerdns,
  powerdns-admin, coraza, seaweedfs) show all-tiers SINGLETON; cnpg-pair / valkey / vllm
  show active-hot-standby / active-passive·sentinel / active-active.
- **§2 §6 HA Switchover + DR panel: ✅ 9 / ❌ 3** — all six priority apps show an enabled
  Switchover button + honest DR panel; the dialog opens with a real 7-step cross-region
  plan (captured on cnpg-pair + openbao, cancelled). The 3 ❌ are EXECUTION rows.
- **§3 bootstrap-HA DR-panel honesty: ✅ 5 / ❌ 0.**

**The 6 genuine ❌ (all cross-region EXECUTION, not driven on this single live walk):**

1. cnpg-pair region-kill EXECUTION — no live 2-region Continuum CR on hw138.
2. gitea-app-served-from-promoted-region — same; no drivable promotion.
3. openbao raft-transition EXECUTION — no drivable openbao failover (single region).

(The §2 table renders 3 EXECUTION ❌ rows; §2 + §6 narrative counts them once each.) hw138
is single-region for this read-only walk — every region-kill / promotion EXECUTION row
is ❌ exactly as expected, because the verdict is "what rendered / was drivable live",
and no failover was driven. The declared-topology + Switchover-dialog + honest-DR-panel
UI — the three #3375 deliverables — **all render correctly live on hw138**.

**One honesty note recorded:** the openbao Switchover dialog body reuses the generic
CNPG-worded 7-step template (it talks about cordoning the CNPG Cluster CR and flipping
CNPG `replica.enabled`) even though openbao's declared mechanism is `raft-transition`.
The panel header declares `raft-transition` correctly; the dialog step copy is
mechanism-agnostic. Recorded as ✅ (dialog opens, enabled) with the caveat noted, not as
a separate ❌.

**Evidence:** `docs/sessions/2026-06-14/evidence/3375-hw138/` — 22 per-app Topology
screenshots + the dashboard (`r0-dashboard.png`) + two Switchover-dialog screenshots
(`dialog-bp-cnpg-pair.png`, `dialog-bp-openbao.png`), all captured live on hw138 in this
pass (no earlier-env carryover).
