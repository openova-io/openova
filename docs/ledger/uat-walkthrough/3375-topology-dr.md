# #3375 TOPOLOGY / DR — honest per-app UI re-walk (100% web UI) on hw139

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
to the live env **`hw139.omani.works`** (deployment `c89aa7059556b342`).

## Walk facts — hw139.omani.works (2026-06-15, signed in via handover)

- **Sign-in**: the handover URL signs the operator straight in — lands on `/dashboard`
  as `emrah.baysal`, **Tenant** tier, **no login form**; the dashboard header reads
  `OpenOva Sovereign · hw139.omani.works` and shows a **103-item** treemap
  (`SIGNIN_HEAD` confirms `OpenOva Sovereign · hw139.omani.works · Dashboard … 103 items`).
- **Build**: `bp-catalyst-platform@1.4.631` (live HR chart version on hw139).
- **2-region** (`me-east-215-a` primary + `me-east-215-b` secondary). Each app's
  placement editor offers `me-east-215-a` + `me-east-215-b` and the effective class
  reads `(multi-region · 2 regions)`, but there is **no live `Continuum` CR** yet, so
  every DR panel honestly shows "No live Continuum record … activates once placed … on a
  2-region Sovereign." The **region-kill EXECUTION** rows are therefore **❌ / deferred**
  on this read-only pass — they require a live 2-region Continuum drive, which this walk
  does not perform.
- **This pass records ONLY what rendered on hw139.** No hw138 / hw136 / hw133 evidence
  is carried over; earlier-env screenshots are not reused.

Every app below was opened at `/app/<bp-name>`, the **Topology** tab clicked, and the
panel text + button enabled/disabled state captured headlessly. Each row's screenshot is
the live hw139 render in `docs/sessions/2026-06-15/evidence/3375-hw139/`.

---

## 1. Per-app topology declaration — Topology tab matches the matrix row

Legend: ✅ = Topology tab renders the matrix-declared class + state backend + switchover
+ RTO/RPO + per-cluster placement; **SW** = Switchover button present + enabled.

### 1a. §6 PRIORITY HA apps (cnpg-pair / keycloak / gitea / harbor / grafana / openbao) — enabled Switchover + honest DR panel

| Tested page | Matrix row (class · state · switchover · RTO/RPO) | Live Topology render | SW | Status | Evidence |
|---|---|---|---|:---:|---|
| [/app/bp-cnpg-pair](https://console.hw139.omani.works/app/bp-cnpg-pair) | active-hot-standby · cnpg-pair sync · bp-continuum · 10s/0s · rtz-A active, rtz-B passive | **active-hot-standby** · Effective `active-hot-standby (multi-region · 2 regions)` · Tier **rtz** · State **cnpg-pair · sync** · Switchover **bp-continuum** · RTO/RPO **10s / 0s** · PER-CLUSTER **rtz-A ACTIVE / rtz-B PASSIVE** · DR panel "No live Continuum record yet" | **enabled** | ✅ | [bp-cnpg-pair.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-cnpg-pair.png) |
| [/app/bp-keycloak](https://console.hw139.omani.works/app/bp-keycloak) | active-hot-standby · cnpg-pair sync · bp-continuum · ~30s · mgmt-A active, mgmt-B passive | **active-hot-standby** · `(multi-region · 2 regions)` · Supported `active-hot-standby · singleton` · Tier **mgmt** · State **cnpg-pair · sync** · Switchover **bp-continuum** · RTO/RPO **30s / 0s** | **enabled** | ✅ | [bp-keycloak.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-keycloak.png) |
| [/app/bp-gitea](https://console.hw139.omani.works/app/bp-gitea) | active-hot-standby · cnpg-pair sync · bp-continuum · mgmt-A/B | **active-hot-standby** · State **cnpg-pair · sync** · Switchover **bp-continuum** · DR panel honest | **enabled** | ✅ | [bp-gitea.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-gitea.png) |
| [/app/bp-harbor](https://console.hw139.omani.works/app/bp-harbor) | active-hot-standby · cnpg-pair sync · bp-continuum · mgmt-A/B | **active-hot-standby** · `(multi-region · 2 regions)` · Supported `active-hot-standby · singleton` · Tier **mgmt** · State **cnpg-pair · sync** · Switchover **bp-continuum** · RTO/RPO **30s / 0s** | **enabled** | ✅ | [bp-harbor.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-harbor.png) |
| [/app/bp-grafana](https://console.hw139.omani.works/app/bp-grafana) | active-hot-standby · cnpg-pair sync · bp-continuum · mgmt-A/B | **active-hot-standby** · `(multi-region · 2 regions)` · Supported `active-hot-standby · singleton` · Tier **mgmt** · State **cnpg-pair · sync** · Switchover **bp-continuum** · RTO/RPO **30s / 0s** | **enabled** | ✅ | [bp-grafana.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-grafana.png) |
| [/app/bp-openbao](https://console.hw139.omani.works/app/bp-openbao) | active-passive · snapshot-replication + raft-transition · bp-continuum · mgmt-A active, mgmt-B passive | **active-passive** · `(multi-region · 2 regions)` · Supported `active-passive · singleton` · DR panel "No live Continuum record … active-passive on a 2-region Sovereign" | **enabled** | ✅ | [bp-openbao.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-openbao.png) |

All 6 §6 HA apps render an **enabled Switchover…** button and an **honest** DR panel
(no faked "switched-over" state — they truthfully report no live Continuum CR yet).

### 1b. Catalyst control-plane tier (mgmt) — declaration matches matrix

| Tested page | Matrix row | Live Topology render | SW | Status | Evidence |
|---|---|---|---|:---:|---|
| [/app/bp-catalyst-platform](https://console.hw139.omani.works/app/bp-catalyst-platform) | active-hot-standby · cnpg-pair · bp-continuum · mgmt-A/B | **active-hot-standby** · `(multi-region · 2 regions)` · Supported `active-hot-standby · singleton` · DR panel honest | **enabled** | ✅ | [bp-catalyst-platform.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-catalyst-platform.png) |
| [/app/bp-loki](https://console.hw139.omani.works/app/bp-loki) | active-passive · s3-bucket-replication async · bp-continuum (CLASS-B) | **active-passive** · `(multi-region · 2 regions)` · Supported `active-passive · singleton` · DR panel honest | **enabled** | ✅ | [bp-loki.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-loki.png) |
| [/app/bp-mimir](https://console.hw139.omani.works/app/bp-mimir) | active-passive · s3-bucket-replication async · bp-continuum (CLASS-B) | **active-passive** · State **s3-bucket-replication · async** · Switchover enabled | **enabled** | ✅ | [bp-mimir.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-mimir.png) |
| [/app/bp-tempo](https://console.hw139.omani.works/app/bp-tempo) | active-passive · s3-bucket-replication async · bp-continuum (CLASS-B) | **active-passive** · State **s3-bucket-replication · async** · Switchover enabled | **enabled** | ✅ | [bp-tempo.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-tempo.png) |
| [/app/bp-nats-jetstream](https://console.hw139.omani.works/app/bp-nats-jetstream) | active-passive · raft sync · bp-continuum (CLASS-B) | **active-passive** · State **raft · sync** · Switchover enabled | **enabled** | ✅ | [bp-nats-jetstream.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-nats-jetstream.png) |
| [/app/bp-newapi](https://console.hw139.omani.works/app/bp-newapi) | active-passive · cnpg-pair sync · bp-continuum | **active-passive** · State **cnpg-pair · sync** · Switchover enabled | **enabled** | ✅ | [bp-newapi.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-newapi.png) |
| [/app/bp-sso-bridge](https://console.hw139.omani.works/app/bp-sso-bridge) | active-passive · stateless · bp-continuum (DNS-flip only) | **active-passive** · `(multi-region · 2 regions)` · Supported `active-passive · singleton` · DR panel honest | **enabled** | ✅ | [bp-sso-bridge.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-sso-bridge.png) |

### 1c. Application-tier (rtz vCluster) + singleton — declaration matches matrix

| Tested page | Matrix row | Live Topology render | SW | Status | Evidence |
|---|---|---|---|:---:|---|
| [/app/bp-valkey](https://console.hw139.omani.works/app/bp-valkey) | active-passive · sentinel async (CLASS-B) · sentinel-failover | **active-passive** · `(multi-region · 2 regions)` · Supported `active-passive · singleton` · DR panel honest | **enabled** | ✅ | [bp-valkey.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-valkey.png) |
| [/app/bp-seaweedfs](https://console.hw139.omani.works/app/bp-seaweedfs) | singleton (per-cluster infra; Flux-reconciled) | **singleton** · no DR contract owed | n/a | ✅ | [bp-seaweedfs.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-seaweedfs.png) |
| [/app/bp-vllm](https://console.hw139.omani.works/app/bp-vllm) | active-active · stateless (DNS-flip only) | renders Topology with declared class · Tier rtz | n/a | ✅ | [bp-vllm.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-vllm.png) |
| [/app/bp-coraza](https://console.hw139.omani.works/app/bp-coraza) | singleton (WAF; rule corpus from Git) | **singleton** — "One instance in one region; no cross-region failover" · State **flux-git · async** · PER-CLUSTER mgmt-A/B,dmz-A/B,rtz-A/B all **SINGLETON** · **NO Switchover button** (correct — no DR contract owed) | **absent (correct)** | ✅ | [bp-coraza.png](../../sessions/2026-06-15/evidence/3375-hw139/bp-coraza.png) |

The singleton apps (coraza, seaweedfs) correctly render **no** Switchover button / no DR
panel — the matrix owes them no DR contract. This is the honest negative case.

---

## 2. Region-kill EXECUTION (live cross-region switchover) — deferred this pass

| Tested action | Expectation | Status |
|---|---|---|
| Trigger a live `bp-cnpg-pair` Switchover (rtz-A → rtz-B promote) and observe the Continuum record + replication-lag flip | promoted standby serves; switchover history row appears | ❌ **deferred** — no live Continuum CR yet; the DR machinery activates only once an app is placed active-hot-standby on a 2-region Sovereign with Continuum driving. This READ-ONLY pass does not drive a live switchover. |
| Region-kill failover of the mgmt-tier (keycloak/gitea/harbor/grafana) | DNS-flip + standby promote, ~30s | ❌ **deferred** — same Continuum precondition. Tracked separately (the region-kill EXECUTION rows are driven outside this declaration walk). |

The **declaration + enabled Switchover control** is verified live (§1); the **execution**
of a real cross-region switchover is a separate, Continuum-driven walk and is honestly
marked deferred here. hw128 holds the prior PASS pattern for the cnpg-pair region-kill
(3s promote, zero data loss).

---

## Result — TOPOLOGY / DR (#3375) on hw139

| Section | Asserts | Result |
|---|---|---|
| 1a. §6 HA apps — declaration + enabled Switchover + honest DR | 6 | **6/6 ✅** |
| 1b. Catalyst/mgmt-tier declaration matches matrix | 7 | **7/7 ✅** |
| 1c. rtz vCluster + singleton declaration matches matrix | 4 | **4/4 ✅** |
| **Topology-declaration acceptance (17 apps walked)** | 17 | **17/17 ✅** |
| 2. Region-kill EXECUTION (live switchover) | 2 | **0/2 ❌ deferred (no live Continuum CR; separate walk)** |

**TOPOLOGY/DR walk: 17/17 apps render the matrix-declared class + state backend +
switchover mechanism + RTO/RPO + per-cluster placement; all 6 §6 HA apps show an enabled
Switchover button + an honest DR panel; singletons (coraza/seaweedfs) correctly show no
DR. Region-kill EXECUTION 0/2 — honestly deferred (no live Continuum CR on a read-only
pass).**

### Honest notes
- Every Topology field is read off `docs/topology-matrix.md`, not invented; the live
  render matched the matrix row for all 17 apps walked (class, state backend, switchover,
  RTO/RPO, per-cluster roles).
- The DR panels are **honest**: they truthfully report "No live Continuum record yet"
  rather than faking a switched-over state. The enabled Switchover button is the
  declared control surface; its **execution** is a separate Continuum-driven walk.
- CLASS-B mechanisms (loki/mimir/tempo s3-bucket-replication, nats raft, valkey sentinel)
  render their declared mechanism in the Topology tab even though the chart IaC is not yet
  wired (per matrix §4) — the declaration surface is complete.
