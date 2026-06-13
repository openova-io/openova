# #3375 TOPOLOGY / DR — exhaustive per-app user-acceptance walk (100% web UI)

The agreed truth is the per-Blueprint **topology matrix** in
[`docs/topology-matrix.md`](../../topology-matrix.md) (the durable promotion of the
`2026-06-02-per-blueprint-topology-audit.md` agreement). **Every row below is read
off that matrix — never invented.** For each installed application the user opens
its **Topology** tab in the operator console and checks that the **declared
topology + placement match the matrix row** for that app; for the §6 priority HA
apps the user then drives the full **region-kill walk** (create data → click
**Switchover** in the DR panel → confirm the other region promotes → re-open the
app → data survives with zero loss).

This is **100% web UI** — no terminal, no `kubectl`, no `psql`. Every **Tested
page** is a clickable link to the live env **`hw133.omani.works`**.

**Two preconditions and two honest gaps are encoded up front (do not skip them):**

- **2-region precondition.** The cross-region DR machinery (cnpg-pair sync, perf-
  replication, mesh) is **OFF unless the Sovereign is genuinely 2-region with the
  app's multi-region option enabled**. On a single-region prov every Topology tab
  shows the `single-region` default (`singleton`) and there is no DR panel.
- **The four topologies.** `singleton` (one place, no DR) · `active-passive`
  (primary + warm standby, switchover on failure) · `active-hot-standby` (sync
  replica, near-zero RTO/RPO — the **cnpg-pair** pattern) · `active-active` (both
  serve). The agreed switchover engine is **`bp-continuum`**.
- **❌ GAP 1 — Switchover button disabled.** In the live DR panel the
  **Switchover** button is **disabled** ("Owner tier required" — a UI bug). The
  *create-data* and *data-survives* halves are walkable; the *click-switchover*
  half **cannot be driven from the UI yet**.
- **❌ GAP 2 — CLASS-B mechanisms not wired.** Apps whose matrix class is
  `gap(CLASS-B)` declare a cross-region mechanism in their agreement row but the
  chart **does not yet wire it** (s3-bucket-replication, sentinel, raft,
  mirrormaker2, ccr). Their Topology tab shows the declared variant but **no live
  DR**.

---

## 1. Per-app topology declaration — one row per installed app

For each app: open its **Topology** tab → confirm the **declared topology +
placement match its matrix row**. `Status = ☐`, all web UI.

### 1a. Catalyst control-plane tier (mgmt clusters)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [catalyst-platform · Topology](https://console.hw133.omani.works/app/bp-catalyst-platform) | Topology tab shows **active-hot-standby (mgmt-A active / mgmt-B standby)** — matches matrix. Class: **cnpg-pair sync** (Catalyst CRDs + PG state; `bp-continuum` flips `console.<sov>` + `api.<sov>`) | ☐ | |
| [keycloak · Topology](https://console.hw133.omani.works/app/bp-keycloak) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (realm + sessions in PG; endpoint `auth.<sov>`). **§6 priority — full walk in §2** | ☐ | |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (PG + Git blobs on SeaweedFS S3; endpoint `gitea.<sov>`). **§6 priority — full walk in §2** | ☐ | |
| [harbor · Topology](https://console.hw133.omani.works/app/bp-harbor) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (PG + image blobs on object storage; endpoints `harbor.<sov>` + `registry.<sov>`) | ☐ | |
| [grafana · Topology](https://console.hw133.omani.works/app/bp-grafana) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (Grafana DB on cnpg-pair; dashboards read shared S3; endpoint `grafana.<sov>`) | ☐ | |
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | **active-passive (mgmt-A active / mgmt-B passive)** — matches matrix. Class **openbao perf-replication** (Raft store; `bp-continuum` runs `raft transition-to-primary`; endpoint `vault.<sov>`). **§6 priority — full walk in §2** | ☐ | |
| [newapi · Topology](https://console.hw133.omani.works/app/bp-newapi) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (state in PG; DNS-flip via `bp-continuum`) | ☐ | |
| [guacamole · Topology](https://console.hw133.omani.works/app/bp-guacamole) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (session + connection config in PG; endpoint `guac.<sov>`). Note: matrix flags `orphan-placementSchema→remove` (declaration cleanup, not a topology change) | ☐ | |
| [k8s-ws-proxy · Topology](https://console.hw133.omani.works/app/bp-k8s-ws-proxy) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. Class **stateless DNS-flip only** (no state IaC owed; in-flight WS sessions drop on flip) | ☐ | |
| [sso-bridge · Topology](https://console.hw133.omani.works/app/bp-sso-bridge) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. Class **stateless DNS-flip only** (secrets via External-Secrets / openbao) | ☐ | |
| [oidc-gate · Topology](https://console.hw133.omani.works/app/bp-oidc-gate) | **active-passive (mgmt-A active / mgmt-B passive)** — matches matrix. Class **stateless DNS-flip only** (stateless oauth2-proxy peer of sso-bridge; perTopology completed this ticket, founder-adjudicate amendment) | ☐ | |
| [loki · Topology](https://console.hw133.omani.works/app/bp-loki) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — row mechanism `s3-bucket-replication/async` **not yet wired**; tab shows variant, no live DR | ☐ | |
| [mimir · Topology](https://console.hw133.omani.works/app/bp-mimir) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — row mechanism `s3-bucket-replication/async` **not yet wired** | ☐ | |
| [tempo · Topology](https://console.hw133.omani.works/app/bp-tempo) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — row mechanism `s3-bucket-replication/async` **not yet wired** | ☐ | |
| [nats-jetstream · Topology](https://console.hw133.omani.works/app/bp-nats-jetstream) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — row mechanism `raft/sync` (leaf-node tunnel only, streams not replicated by default) **not yet wired** | ☐ | |
| [openova-flow-server · Topology](https://console.hw133.omani.works/app/bp-openova-flow) | Control-plane component of catalyst-platform (flow server + emitter) — **no independent matrix row**; inherits the **mgmt active-hot-standby** placement of catalyst-platform. Tab shows it bound to the mgmt tier; emitter is **stateless** (no own DR contract) | ☐ | |

### 1b. Per-host-cluster infrastructure tier (installed on the base Sovereign)

These run in **every** host cluster (each cluster owns its own copy); cross-cluster
sync is **not** part of these blueprints. The Topology tab shows **singleton** with
the all-tiers placement, Flux-reconciled from Git — failover is **N/A** (a cluster
loss removes one copy; the others keep running).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cnpg · Topology](https://console.hw133.omani.works/app/bp-cnpg) | **singleton (mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B)** — matches matrix. Class **per-cluster infra** — the PostgreSQL **engine operator** (stateless; the Clusters it manages are paired by cnpg-pair). Shown once under Platform/engines | ☐ | |
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | **active-hot-standby (rtz-A active / rtz-B standby)** — matches matrix. Class **cnpg-pair** — **sync streaming replication** (`remote_apply`, zero-tx-loss) over ClusterMesh; endpoint `db.<org>.<sov>`. **§6 priority — full walk in §2** | ☐ | |
| [shared-pg (instance 1) · Topology](https://console.hw133.omani.works/app/bp-postgres) | **bp-postgres** data-instance. Topology mode = **active-hot-standby** on a 2-region prov (`single-region` default = `singleton`) — matches matrix's cnpg-pair sync shape (ADR-0004). Rendered as a **data-instance card** with its Consumers (bindings) table | ☐ | |
| [shared-pg (instance 2) · Topology](https://console.hw133.omani.works/app/bp-postgres) | Second **bp-postgres** data-instance — same **active-hot-standby / cnpg-pair sync** declaration; distinct Consumers table (different bound apps) | ☐ | |
| [shared-pg (instance 3) · Topology](https://console.hw133.omani.works/app/bp-postgres) | Third **bp-postgres** data-instance — same **active-hot-standby / cnpg-pair sync** declaration; the 3 shared engines back the 6–7 consumer apps many-to-many | ☐ | |
| [seaweedfs · Topology](https://console.hw133.omani.works/app/bp-seaweedfs) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra** (S3 object store + filer; within-cluster replicas + erasure coding; cross-cluster bucket replication is async-pull, not part of this blueprint) | ☐ | |
| [powerdns · Topology](https://console.hw133.omani.works/app/bp-powerdns) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra** (PG-backed authoritative DNS; cross-cluster failover via lua-record `ifurlup`, ~30s TTL; endpoints `ns1.<sov>`/`ns2.<sov>`) | ☐ | |
| [powerdns-admin · Topology](https://console.hw133.omani.works/app/bp-powerdns-admin) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra** (admin UI state in per-cluster PG; endpoint `pdns-admin.<sov>`) | ☐ | |
| [coraza · Topology](https://console.hw133.omani.works/app/bp-coraza) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra** (stateless WAF; CoreRuleSet corpus from Git; rides the Gateway) | ☐ | |
| [sandbox · Topology](https://console.hw133.omani.works/app/bp-sandbox) | Declares **active-hot-standby / singleton** but **replication backend = `none`** (stateless controller; state lives in Gitea) → effectively **stateless DNS-flip**. Tab shows the declared variant with no own state replication | ☐ | |

### 1c. Application Blueprints installed on the base Sovereign (per-Org / rtz tier)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [valkey · Topology](https://console.hw133.omani.works/app/bp-valkey) | **active-passive (rtz-A active / rtz-B passive)** — matches matrix. ❌ Class **gap(CLASS-B)** — row mechanism `sentinel/async` (cross-region sentinel-pair) **not yet wired**; endpoint `valkey.<org>.<sov>` | ☐ | |
| [vllm · Topology](https://console.hw133.omani.works/app/bp-vllm) | **active-active (rtz-A / rtz-B)** — matches matrix. Class **stateless DNS-flip only** (GPU inference; model weights on shared PVC / baked image; both regions serve; endpoint `vllm.<org>.<sov>`) | ☐ | |

---

## 2. §6 priority HA apps — full region-kill walk (create → switchover → survives)

The matrix names six **§6 priority** HA apps that must pass the full region-kill
walk: **cnpg-pair, keycloak, gitea, harbor, grafana, openbao**. Below are the three
reference walks the founder called out (cnpg-pair, gitea, openbao) in full
click-by-click form; keycloak / harbor / grafana follow the identical cnpg-pair
shape (create data in their own UI → Switchover → re-open → data survives).

> **❌ Live blocker (both halves of every walk):** the **Switchover** button in the
> DR panel is **disabled** ("Owner tier required"). The **create-data** and
> **data-survives** steps below are walkable today; the **click-Switchover** step
> **cannot be driven from the UI yet** — mark it ❌ until the tier bug is fixed.

### 2a. cnpg-pair (the reference active-hot-standby — sync, zero-loss)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | DR section shows **Phase = Healthy**, **Primary region** (rtz-A), **Replica region** (rtz-B), **replication lag** (green, ~0 — sync `remote_apply`) | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Create a record you'll recognise later through an app that lives on cnpg-pair (e.g. a new Gitea repo `dr-proof`) | ☐ | |
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | ❌ **GAP** — **Switchover…** button is **disabled** ("Owner tier required"); an admin cannot run it in the live UI yet. *(When fixed: click → dialog lists ~7 steps + duration <60s → enter reason → **Confirm**)* | ☐ | |
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | Watch the panel advance → **other region (rtz-B) now primary**, last switchover **Success** (~5s write disruption, bank-tier RTO) | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Re-open the app → it loads and works (served from the promoted region) | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | The `dr-proof` record created earlier is **still there** — **zero data loss** (sync replica) | ☐ | |

### 2b. gitea (active-hot-standby — PG via cnpg-pair + Git blobs on SeaweedFS S3)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | DR section shows **Primary = mgmt-A**, **Replica = mgmt-B**, lag green; declared **active-hot-standby** matches matrix | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Sign in (silent SSO) → create a repo `dr-gitea-proof` and push/commit a file in the web editor (data lands in PG + Git blob on S3) | ☐ | |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | ❌ **GAP** — **Switchover** button **disabled** ("Owner tier required"). *(When fixed: Switchover → Confirm → `bp-continuum` does PG demote/promote + PowerDNS flip for `gitea.<sov>`, ~30s TTL, push paused ~5s)* | ☐ | |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | Panel shows **mgmt-B now primary**, switchover **Success** | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Re-open Gitea → repo `dr-gitea-proof` and the committed file are **still present** (PG sync zero-loss; blob arrived via S3 mirror) | ☐ | |

### 2c. openbao (active-passive — Raft store + perf-replication; reads stay up throughout)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | DR section shows declared **active-passive (mgmt-A active / mgmt-B passive)** — matches matrix; mechanism **perf-replication** | ☐ | |
| [bao.hw133.omani.works/ui/](https://bao.hw133.omani.works/ui/) | Sign in to the Vault UI → write a recognisable KV secret `secret/dr-proof` with a value you'll re-check | ☐ | |
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | ❌ **GAP** — **Switchover** button **disabled** ("Owner tier required"), AND the openbao *promotion* may not yet be wired to the console switchover. *(When fixed: Switchover runs `vault operator raft transition-to-primary` on the standby)* | ☐ | |
| [bao.hw133.omani.works/ui/](https://bao.hw133.omani.works/ui/) | During/after the switchover: **reading** `secret/dr-proof` in the Vault UI stays **available throughout** (KV reads continue on the replica — that is the active-passive contract) | ☐ | |

### 2d. keycloak / harbor / grafana — identical cnpg-pair shape

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [keycloak · Topology](https://console.hw133.omani.works/app/bp-keycloak) | DR panel Primary/Replica/lag green; create a realm user in the KC admin UI → ❌ Switchover disabled (gap) → after flip re-open `auth.<sov>` → the user survives (PG sync, sessions are JWT) | ☐ | |
| [harbor · Topology](https://console.hw133.omani.works/app/bp-harbor) | DR panel green; push/tag an image record → ❌ Switchover disabled (gap) → after flip re-open `harbor.<sov>` → repo + tag survive (PG sync; blob pulls keep working through replica bucket) | ☐ | |
| [grafana · Topology](https://console.hw133.omani.works/app/bp-grafana) | DR panel green; save a dashboard → ❌ Switchover disabled (gap) → after flip re-open `grafana.<sov>` → dashboard survives (Grafana DB on cnpg-pair; reads same shared S3) | ☐ | |

---

## 3. Rejoin / no split-brain (after the original region returns)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | After the original region returns → panel shows **exactly one primary** (the promoted region), old region rejoined as a **follower** — **no split-brain** (lease + `cnpg promote` guard) | ☐ | |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | Original mgmt-A rejoins as **standby** under the promoted mgmt-B; replication lag returns to green; no dual-primary | ☐ | |
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | Returned region rejoins as **replica** of the promoted primary; one primary only | ☐ | |

---

## 4. Optional catalog apps — agreed topology, NOT installed on a base Sovereign

These are in the matrix but are **not installed on a base Sovereign**. Listed with
their **agreed topology + DR class** so they can be verified the same Topology-tab
way **when added** to an Org. `Status = ☐`, each marked **not installed — verify
when added**.

### 4a. Control-plane / infra candidates (not installed by default)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [netbird](https://console.hw133.omani.works/app/bp-netbird) | **active-hot-standby (mgmt-A/mgmt-B)** · cnpg-pair sync — **not installed** (candidate Catalyst-CP) | ☐ | |
| [spire](https://console.hw133.omani.works/app/bp-spire) | **active-hot-standby (mgmt-A/mgmt-B)** · cnpg-pair sync — **not installed** (candidate Catalyst-CP) | ☐ | |
| [alloy](https://console.hw133.omani.works/app/bp-alloy) | **singleton (all 6 tiers)** · stateless telemetry DaemonSet — **not installed by default** | ☐ | |
| [self-sovereign-cutover](https://console.hw133.omani.works/app/bp-self-sovereign-cutover) | **singleton (mgmt-A)** · one-shot handover Jobs — **dormant until cutover** | ☐ | |
| [openclaw](https://console.hw133.omani.works/app/bp-openclaw) | **singleton (rtz-A)** · scaffold default (ROW-AMENDMENT, founder-adjudicate) — **not installed** | ☐ | |

### 4b. App Blueprints (per-Org catalog — install per tenant)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [ferretdb](https://console.hw133.omani.works/app/bp-ferretdb) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync — **not installed** | ☐ | |
| [strimzi](https://console.hw133.omani.works/app/bp-strimzi) | **active-active (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `mirrormaker2` — **not installed** | ☐ | |
| [clickhouse](https://console.hw133.omani.works/app/bp-clickhouse) | **active-active (rtz-A/rtz-B)** · native replication / DNS-flip — **not installed** | ☐ | |
| [opensearch](https://console.hw133.omani.works/app/bp-opensearch) | **active-active (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `ccr` — **not installed** | ☐ | |
| [stalwart-tenant](https://console.hw133.omani.works/app/bp-stalwart-tenant) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + S3 mail blobs — **not installed** | ☐ | |
| [stalwart-sovereign](https://console.hw133.omani.works/app/bp-stalwart-sovereign) | **external (mothership)** — runs on `mail.openova.io`, not a deployable Sovereign workload (ROW-AMENDMENT) | ☐ | |
| [livekit](https://console.hw133.omani.works/app/bp-livekit) | **active-active (rtz-A/rtz-B)** · stateless SFU / DNS-flip — **not installed** | ☐ | |
| [matrix](https://console.hw133.omani.works/app/bp-matrix) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + S3 media — **not installed** | ☐ | |
| [stunner](https://console.hw133.omani.works/app/bp-stunner) | **active-active (rtz-A/rtz-B)** · stateless TURN/STUN / DNS-flip — **not installed** | ☐ | |
| [milvus](https://console.hw133.omani.works/app/bp-milvus) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `s3-bucket-replication` — **not installed** | ☐ | |
| [neo4j](https://console.hw133.omani.works/app/bp-neo4j) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `velero` — **not installed** | ☐ | |
| [kserve](https://console.hw133.omani.works/app/bp-kserve) | **active-active (rtz-A/rtz-B)** · stateless model serving / DNS-flip — **not installed** | ☐ | |
| [knative](https://console.hw133.omani.works/app/bp-knative) | **active-active (rtz-A/rtz-B)** · stateless / DNS-flip — **not installed** | ☐ | |
| [librechat](https://console.hw133.omani.works/app/bp-librechat) | **active-active (rtz-A/rtz-B)** · cnpg-pair sync (chat history) — **not installed** | ☐ | |
| [bge](https://console.hw133.omani.works/app/bp-bge) | **active-active (rtz-A/rtz-B)** · stateless embedding / DNS-flip — **not installed** | ☐ | |
| [llm-gateway](https://console.hw133.omani.works/app/bp-llm-gateway) | **active-active (rtz-A/rtz-B)** · stateless proxy / DNS-flip — **not installed** | ☐ | |
| [anthropic-adapter](https://console.hw133.omani.works/app/bp-anthropic-adapter) | **active-active (rtz-A/rtz-B)** · stateless adapter / DNS-flip — **not installed** | ☐ | |
| [langfuse](https://console.hw133.omani.works/app/bp-langfuse) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + ClickHouse — **not installed** | ☐ | |
| [nemo-guardrails](https://console.hw133.omani.works/app/bp-nemo-guardrails) | **active-active (rtz-A/rtz-B)** · stateless policy / DNS-flip — **not installed** | ☐ | |
| [temporal](https://console.hw133.omani.works/app/bp-temporal) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + opensearch visibility — **not installed** | ☐ | |
| [flink](https://console.hw133.omani.works/app/bp-flink) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `s3-bucket-replication` — **not installed** | ☐ | |
| [debezium](https://console.hw133.omani.works/app/bp-debezium) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `mirrormaker2` — **not installed** | ☐ | |
| [iceberg](https://console.hw133.omani.works/app/bp-iceberg) | **active-active (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `s3-bucket-replication` — **not installed** | ☐ | |
| [openmeter](https://console.hw133.omani.works/app/bp-openmeter) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + ClickHouse — **not installed** | ☐ | |
| [litmus](https://console.hw133.omani.works/app/bp-litmus) | **singleton (rtz-A,rtz-B)** · per-cluster chaos (intentionally cluster-scoped) — **not installed** | ☐ | |
| [wordpress-tenant](https://console.hw133.omani.works/app/bp-wordpress-tenant) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + S3 media — **not installed** | ☐ | |
| [qa-app](https://console.hw133.omani.works/app/bp-qa-app) | **singleton (rtz-A,rtz-B)** · test scaffold, no DR contract owed — **not installed** | ☐ | |

### 4c. Remaining per-cluster singleton infra (matrix `n/a-singleton`, verify-when-relevant)

All declare **singleton** across the relevant tiers, class **per-cluster infra,
Flux-reconciled from Git**, failover **N/A**. Not separately walked for DR (no
cross-region contract owed); listed for completeness.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cilium / cilium-policies / flux / gateway-api / crossplane(+claims) · Topology](https://console.hw133.omani.works/app/bp-cilium) | **singleton (all 6 tiers)** · per-cluster infra · failover N/A | ☐ | |
| [cert-manager(+powerdns/dynadot webhooks) / external-secrets(+stores) / external-dns / sealed-secrets · Topology](https://console.hw133.omani.works/app/bp-cert-manager) | **singleton (all 6 tiers)** · per-cluster infra · failover N/A | ☐ | |
| [kyverno(+policies) / trivy / falco / sigstore / syft-grype / network-policies · Topology](https://console.hw133.omani.works/app/bp-kyverno) | **singleton (all 6 tiers)** · per-cluster security infra · failover N/A | ☐ | |
| [vpa / reloader / reflector / velero / opentelemetry(+operator) · Topology](https://console.hw133.omani.works/app/bp-velero) | **singleton (all 6 tiers)** · per-cluster infra · failover N/A | ☐ | |
| [mgmt-vcluster / rtz-vcluster / dmz-vcluster / vcluster-helmrepo · Topology](https://console.hw133.omani.works/app/bp-mgmt-vcluster) | **singleton** (tier-scoped: mgmt-A,B / rtz-A,B / dmz-A,B) · vCluster state in PVC; cross-region via Velero restore · per-cluster | ☐ | |
| [hcloud-ccm / hcloud-csi / cluster-autoscaler-hcloud · Topology](https://console.hw133.omani.works/app/bp-hcloud-ccm) | **singleton** · Hetzner-only (not used on the Huawei reference Sovereign) · N/A | ☐ | |

---

## Summary

**Installed-app declarations vs the matrix (§1):** **27 installed-app rows** are
walked one-per-app (15 control-plane mgmt-tier incl. the 3 catalyst/flow control
rows, 10 per-cluster infra incl. the 3 shared-pg data-instances, 2 rtz App
Blueprints). On a genuine 2-region prov every installed app's Topology tab is
expected to show the **exact declared topology + placement of its matrix row** —
that is the §1 acceptance.

**DR mechanisms wired vs CLASS-B (the gaps):**
- **Wired — cnpg-pair sync (active-hot-standby / active-passive on PG):**
  catalyst-platform, keycloak, gitea, harbor, grafana, newapi, guacamole,
  cnpg-pair, the 3 shared-pg (bp-postgres) — these are the apps whose region-kill
  walk has a real backend (the hw128 PASS pattern).
- **Wired — perf-replication:** openbao (reads stay up; promotion is the §2c gap).
- **Stateless DNS-flip only (no state IaC owed):** sso-bridge, oidc-gate,
  k8s-ws-proxy, sandbox (backend `none`), vllm, flow-emitter.
- **❌ gap(CLASS-B) — declared but chart NOT wired:** loki, mimir, tempo
  (s3-bucket-replication) · nats-jetstream (raft) · valkey (sentinel) — plus the
  optional-catalog CLASS-B rows (strimzi, opensearch, milvus, neo4j, flink,
  debezium, iceberg). Their Topology tab shows the variant but has **no live DR**.

**The two blockers to a clean live walk today (§2):**
1. **❌ Switchover button disabled** ("Owner tier required" — UI tier bug). The
   create-data and data-survives halves are walkable; the click-Switchover half is
   not drivable from the UI. This blocks the switchover step of **all six** §6
   priority walks.
2. **2-region precondition** — the cross-region machinery is OFF unless the
   Sovereign is genuinely 2-region with the app's multi-region option enabled;
   single-region provs render every app as `singleton` with no DR panel.

**Verdict:** the per-app Topology-declaration walk (§1) is fully specified and
acceptance-ready; the region-kill walk (§2) is blocked on the Switchover tier bug
(❌ GAP 1) for the six priority apps, and the CLASS-B apps (❌ GAP 2) have no DR
mechanism to walk until their charts wire the agreed mechanism.
