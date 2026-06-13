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
| [catalyst-platform · Topology](https://console.hw133.omani.works/app/bp-catalyst-platform) | Topology tab shows **active-hot-standby (mgmt-A active / mgmt-B standby)** — matches matrix. Class: **cnpg-pair sync** (Catalyst CRDs + PG state; `bp-continuum` flips `console.<sov>` + `api.<sov>`). **Reality:** Topology tab is a generic **placement EDITOR** (radio: single-region / active-active / active-hotstandby + region checkboxes me-east-215-a/-b + Preview/Apply). It does NOT read back "active-hot-standby (mgmt-A/mgmt-B)" as the matrix asserts; live status = "Loading status… / Replication lag: n/a (mode) / Last switchover: —". No mgmt-A/mgmt-B placement shown. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-catalyst-platform.png) |
| [keycloak · Topology](https://console.hw133.omani.works/app/bp-keycloak) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (realm + sessions in PG; endpoint `auth.<sov>`). **§6 priority — full walk in §2**. **Reality:** same generic placement editor; no declared-topology read-back, no DR panel, lag "n/a (mode)". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-keycloak.png) |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (PG + Git blobs on SeaweedFS S3; endpoint `gitea.<sov>`). **§6 priority — full walk in §2**. **Reality:** same generic placement editor; no mgmt-A/mgmt-B read-back, no DR panel. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-gitea.png) |
| [harbor · Topology](https://console.hw133.omani.works/app/bp-harbor) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (PG + image blobs on object storage; endpoints `harbor.<sov>` + `registry.<sov>`). **Reality:** generic placement editor only; no declared-topology read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-harbor.png) |
| [grafana · Topology](https://console.hw133.omani.works/app/bp-grafana) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (Grafana DB on cnpg-pair; dashboards read shared S3; endpoint `grafana.<sov>`). **Reality:** generic placement editor only. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-grafana.png) |
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | **active-passive (mgmt-A active / mgmt-B passive)** — matches matrix. Class **openbao perf-replication** (Raft store; `bp-continuum` runs `raft transition-to-primary`; endpoint `vault.<sov>`). **§6 priority — full walk in §2**. **Reality:** editor offers only single-region / active-active / active-hotstandby — **`active-passive` is not even an option**; no read-back of the matrix declaration. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-openbao.png) |
| [newapi · Topology](https://console.hw133.omani.works/app/bp-newapi) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (state in PG; DNS-flip via `bp-continuum`). **Reality:** generic editor; `active-passive` not offered; no read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-newapi.png) |
| [guacamole · Topology](https://console.hw133.omani.works/app/bp-guacamole) | **active-hot-standby (mgmt-A / mgmt-B)** — matches matrix. Class **cnpg-pair sync** (session + connection config in PG; endpoint `guac.<sov>`). Note: matrix flags `orphan-placementSchema→remove`. **Reality:** generic editor; no declared-topology read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-guacamole.png) |
| [k8s-ws-proxy · Topology](https://console.hw133.omani.works/app/bp-k8s-ws-proxy) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. Class **stateless DNS-flip only**. **Reality:** generic editor; `active-passive` not offered; no read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-k8s-ws-proxy.png) |
| [sso-bridge · Topology](https://console.hw133.omani.works/app/bp-sso-bridge) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. Class **stateless DNS-flip only**. **Reality:** generic editor; no read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-sso-bridge.png) |
| [oidc-gate · Topology](https://console.hw133.omani.works/app/bp-oidc-gate) | **active-passive (mgmt-A active / mgmt-B passive)** — matches matrix. Class **stateless DNS-flip only**. **Reality:** app page renders (7-tab strip incl. Topology) but only the generic placement editor; no declared-topology read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-oidc-gate.png) |
| [loki · Topology](https://console.hw133.omani.works/app/bp-loki) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — `s3-bucket-replication/async` not wired. **Reality:** generic editor; no variant read-back, no live DR. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-loki.png) |
| [mimir · Topology](https://console.hw133.omani.works/app/bp-mimir) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — `s3-bucket-replication/async` not wired. **Reality:** generic editor only. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-mimir.png) |
| [tempo · Topology](https://console.hw133.omani.works/app/bp-tempo) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — `s3-bucket-replication/async` not wired. **Reality:** generic editor only. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-tempo.png) |
| [nats-jetstream · Topology](https://console.hw133.omani.works/app/bp-nats-jetstream) | **active-passive (mgmt-A / mgmt-B)** — matches matrix. ❌ Class **gap(CLASS-B)** — `raft/sync` not wired. **Reality:** generic editor only. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-nats-jetstream.png) |
| [openova-flow-server · Topology](https://console.hw133.omani.works/app/bp-openova-flow) | Control-plane component of catalyst-platform — inherits **mgmt active-hot-standby** placement. **Reality:** `https://console.hw133.omani.works/app/bp-openova-flow` returns **"App not found — bp-openova-flow is not part of this deployment"** (no such app slug). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1a-openova-flow.png) |

**Result (§1a): ✅ 0 / ❌ 16.** Every installed app's "Topology" tab renders, but it is a *generic placement EDITOR* (single-region / active-active / active-hotstandby radio + me-east-215-a/-b region checkboxes + Preview/Apply) — **identical for every app**. It does **not** read back each app's matrix-declared topology+placement (no "active-hot-standby (mgmt-A/mgmt-B)" assertion shown), offers **no `active-passive` and no `singleton`** options, and live status is permanently "Loading status… / Replication lag: n/a (mode) / Last switchover: —". So the matrix declarations **cannot be user-verified from this UI**. `bp-openova-flow` is "App not found".

### 1b. Per-host-cluster infrastructure tier (installed on the base Sovereign)

These run in **every** host cluster (each cluster owns its own copy); cross-cluster
sync is **not** part of these blueprints. The Topology tab shows **singleton** with
the all-tiers placement, Flux-reconciled from Git — failover is **N/A** (a cluster
loss removes one copy; the others keep running).

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cnpg · Topology](https://console.hw133.omani.works/app/bp-cnpg) | **singleton (mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B)** — matches matrix. Class **per-cluster infra**. **Reality:** generic placement editor; **no `singleton` label/option**, no per-tier placement read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-cnpg.png) |
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | **active-hot-standby (rtz-A active / rtz-B standby)** — matches matrix. Class **cnpg-pair** sync streaming replication. **§6 priority — full walk in §2**. **Reality:** generic placement editor; no rtz-A/rtz-B read-back, live status "n/a (mode)", no DR panel. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-cnpg-pair.png) |
| [shared-pg (instance 1) · Topology](https://console.hw133.omani.works/app/bp-postgres) | **bp-postgres** data-instance, **active-hot-standby / cnpg-pair sync** (ADR-0004). **Reality:** `https://console.hw133.omani.works/app/bp-postgres` returns **"App not found — bp-postgres is not part of this deployment"** — no shared-pg data-instance card exists at this slug. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-postgres-1.png) |
| [shared-pg (instance 2) · Topology](https://console.hw133.omani.works/app/bp-postgres) | Second **bp-postgres** data-instance. **Reality:** same — **"App not found"** (no bp-postgres slug). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-postgres-2.png) |
| [shared-pg (instance 3) · Topology](https://console.hw133.omani.works/app/bp-postgres) | Third **bp-postgres** data-instance. **Reality:** same — **"App not found"** (no bp-postgres slug). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-postgres-3.png) |
| [seaweedfs · Topology](https://console.hw133.omani.works/app/bp-seaweedfs) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra**. **Reality:** generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-seaweedfs.png) |
| [powerdns · Topology](https://console.hw133.omani.works/app/bp-powerdns) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra**. **Reality:** generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-powerdns.png) |
| [powerdns-admin · Topology](https://console.hw133.omani.works/app/bp-powerdns-admin) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra**. **Reality:** page stuck on "Loading bp-powerdns-admin…" — detail never rendered. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-powerdns-admin.png) |
| [coraza · Topology](https://console.hw133.omani.works/app/bp-coraza) | **singleton (all 6 tiers)** — matches matrix. Class **per-cluster infra**. **Reality:** generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-coraza.png) |
| [sandbox · Topology](https://console.hw133.omani.works/app/bp-sandbox) | Declares **active-hot-standby / singleton**, backend `none`. **Reality:** app page renders (7-tab strip incl. Topology) but only the generic placement editor; no declared-variant read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1b-sandbox.png) |

### 1c. Application Blueprints installed on the base Sovereign (per-Org / rtz tier)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [valkey · Topology](https://console.hw133.omani.works/app/bp-valkey) | **active-passive (rtz-A active / rtz-B passive)** — matches matrix. ❌ Class **gap(CLASS-B)** — `sentinel/async` not wired. **Reality:** generic placement editor; `active-passive` not offered; no read-back, no live DR. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1c-valkey.png) |
| [vllm · Topology](https://console.hw133.omani.works/app/bp-vllm) | **active-active (rtz-A / rtz-B)** — matches matrix. Class **stateless DNS-flip only**. **Reality:** generic placement editor; `active-active` IS one of the 3 radio options but the tab does not read back rtz-A/rtz-B placement or confirm the app's current mode. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r1c-vllm.png) |

**Result (§1b + §1c): ✅ 0 / ❌ 13.** Same generic placement-editor tab; `bp-postgres` ×3 (the 3 shared-pg instances) and the §1b `singleton` infra rows have no per-app declared-topology read-back, `bp-postgres` returns "App not found", `powerdns-admin` never finished loading. The editor never offers `singleton` or `active-passive` and shows no live DR/placement state.

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
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | DR section shows **Phase = Healthy**, **Primary region** (rtz-A), **Replica region** (rtz-B), **replication lag** (green, ~0 — sync `remote_apply`). **Reality:** no DR section — only the generic placement editor + "Live status: Loading status… / Replication lag: n/a (mode) / Last switchover: —". No Phase / Primary / Replica / lag-green shown. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2a-cnpgpair-dr.png) |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Create a record you'll recognise later through an app that lives on cnpg-pair (e.g. a new Gitea repo `dr-proof`). **Reality:** Gitea reached via silent SSO, signed in as **emrah.baysal** (0 repos). Reachable, but no switchover engine to drive the DR proof. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2a-gitea-create.png) |
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | ❌ **GAP** — **Switchover…** button is **disabled** ("Owner tier required"). **Reality:** there is **no Switchover button at all** (not even a disabled one) — the tab is a placement editor (Preview/Apply) with no DR/Switchover control. The expected "Owner tier required" disabled state could not be screenshotted because the control is absent. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2a-cnpgpair-switchover.png) |
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | Watch the panel advance → **other region (rtz-B) now primary**, last switchover **Success**. **Reality:** no switchover can be triggered; "Last switchover: —", no promotion observable. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2a-cnpgpair-promoted.png) |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Re-open the app → it loads and works (served from the promoted region). **Reality:** Gitea loads (signed in), but no switchover happened so "served from promoted region" cannot be asserted. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2a-gitea-reopen.png) |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | The `dr-proof` record created earlier is **still there** — **zero data loss**. **Reality:** no record was created and no switchover ran → cannot prove zero data loss. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2a-gitea-survives.png) |

### 2b. gitea (active-hot-standby — PG via cnpg-pair + Git blobs on SeaweedFS S3)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | DR section shows **Primary = mgmt-A**, **Replica = mgmt-B**, lag green; declared **active-hot-standby** matches matrix. **Reality:** no DR section — generic placement editor only; no Primary/Replica/lag, no declared-variant read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2b-gitea-dr.png) |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Sign in (silent SSO) → create a repo `dr-gitea-proof` and push/commit a file. **Reality:** silent SSO works (signed in as emrah.baysal); repo creation not driven because there is no downstream switchover to validate against. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2b-gitea-signin.png) |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | ❌ **GAP** — **Switchover** button **disabled** ("Owner tier required"). **Reality:** no Switchover button exists at all on the Topology tab. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2b-gitea-switchover.png) |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | Panel shows **mgmt-B now primary**, switchover **Success**. **Reality:** no switchover engine present; cannot observe promotion. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2b-gitea-promoted.png) |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Re-open Gitea → repo `dr-gitea-proof` and the committed file are **still present**. **Reality:** no record created, no switchover ran → cannot prove survival. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2b-gitea-survives.png) |

### 2c. openbao (active-passive — Raft store + perf-replication; reads stay up throughout)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | DR section shows declared **active-passive (mgmt-A active / mgmt-B passive)**; mechanism **perf-replication**. **Reality:** generic placement editor; `active-passive` is not even an option (only single-region / active-active / active-hotstandby); no DR section, no read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2c-openbao-dr.png) |
| [bao.hw133.omani.works/ui/](https://bao.hw133.omani.works/ui/) | Sign in to the Vault UI → write a recognisable KV secret `secret/dr-proof`. **Reality:** OIDC sign-in **failed** — the `/ui/.../oidc/oidc/callback` lands on an error "**Cannot read properties of null (reading 'postMessage')**"; the Vault UI never authenticated, so no secret could be written. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2c-bao-write.png) |
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | ❌ **GAP** — **Switchover** button **disabled** ("Owner tier required"). **Reality:** no Switchover button exists at all on the Topology tab. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2c-openbao-switchover.png) |
| [bao.hw133.omani.works/ui/](https://bao.hw133.omani.works/ui/) | During/after the switchover: **reading** `secret/dr-proof` stays available. **Reality:** Vault UI OIDC callback errors (same `postMessage` error) — not signed in; no read possible. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2c-bao-read.png) |

### 2d. keycloak / harbor / grafana — identical cnpg-pair shape

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [keycloak · Topology](https://console.hw133.omani.works/app/bp-keycloak) | DR panel Primary/Replica/lag green → ❌ Switchover disabled → after flip user survives. **Reality:** no DR panel and no Switchover button — generic placement editor only. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2d-keycloak.png) |
| [harbor · Topology](https://console.hw133.omani.works/app/bp-harbor) | DR panel green → ❌ Switchover disabled → repo+tag survive. **Reality:** no DR panel and no Switchover button — generic placement editor only. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2d-harbor.png) |
| [grafana · Topology](https://console.hw133.omani.works/app/bp-grafana) | DR panel green → ❌ Switchover disabled → dashboard survives. **Reality:** no DR panel and no Switchover button — generic placement editor only. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r2d-grafana.png) |

**Result (§2 region-kill walks): ✅ 0 / ❌ 24.** Worse than the encoded "Switchover disabled" gap — there is **no DR/Switchover panel at all** on any app's Topology tab (no Phase/Primary/Replica/lag, no Switchover button even disabled). The Topology tab is purely a placement *editor*. gitea silent-SSO works (signed in as emrah.baysal) but the bao Vault UI OIDC callback **errors** (`Cannot read properties of null (reading 'postMessage')`) so even the create/read data halves on openbao are unreachable.

---

## 3. Rejoin / no split-brain (after the original region returns)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cnpg-pair · Topology](https://console.hw133.omani.works/app/bp-cnpg-pair) | After the original region returns → panel shows **exactly one primary**, old region a **follower** — no split-brain. **Reality:** no switchover ever ran (no engine/button) and no DR panel exists → rejoin state is not observable in the UI. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r3-cnpgpair-rejoin.png) |
| [gitea · Topology](https://console.hw133.omani.works/app/bp-gitea) | Original mgmt-A rejoins as **standby** under promoted mgmt-B; lag green; no dual-primary. **Reality:** no DR panel / no switchover → not observable. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r3-gitea-rejoin.png) |
| [openbao · Topology](https://console.hw133.omani.works/app/bp-openbao) | Returned region rejoins as **replica**; one primary only. **Reality:** no DR panel / no switchover → not observable. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r3-openbao-rejoin.png) |

---

## 4. Optional catalog apps — agreed topology, NOT installed on a base Sovereign

These are in the matrix but are **not installed on a base Sovereign**. Listed with
their **agreed topology + DR class** so they can be verified the same Topology-tab
way **when added** to an Org. `Status = ☐`, each marked **not installed — verify
when added**.

### 4a. Control-plane / infra candidates (not installed by default)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [netbird](https://console.hw133.omani.works/app/bp-netbird) | **active-hot-standby (mgmt-A/mgmt-B)** · cnpg-pair sync — **not installed**. **Reality:** app page resolves but shows only the generic placement editor (no declared-topology read-back). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4a-netbird.png) |
| [spire](https://console.hw133.omani.works/app/bp-spire) | **active-hot-standby (mgmt-A/mgmt-B)** · cnpg-pair sync — **not installed**. **Reality:** "App not found — bp-spire is not part of this deployment" (confirms not-installed; no topology shown). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4a-spire.png) |
| [alloy](https://console.hw133.omani.works/app/bp-alloy) | **singleton (all 6 tiers)** · stateless telemetry — **not installed by default**. **Reality:** app page resolves but only the generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4a-alloy.png) |
| [self-sovereign-cutover](https://console.hw133.omani.works/app/bp-self-sovereign-cutover) | **singleton (mgmt-A)** · one-shot handover Jobs — **dormant until cutover**. **Reality:** app page resolves but only the generic placement editor. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4a-self-sovereign-cutover.png) |
| [openclaw](https://console.hw133.omani.works/app/bp-openclaw) | **singleton (rtz-A)** · scaffold — **not installed**. **Reality:** "App not found — bp-openclaw is not part of this deployment". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4a-openclaw.png) |

### 4b. App Blueprints (per-Org catalog — install per tenant)

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [ferretdb](https://console.hw133.omani.works/app/bp-ferretdb) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync — **not installed**. **Reality:** "App not found" (confirms not-installed). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-ferretdb.png) |
| [strimzi](https://console.hw133.omani.works/app/bp-strimzi) | **active-active (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `mirrormaker2` — **not installed**. **Reality:** app page resolves but only generic placement editor; no read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-strimzi.png) |
| [clickhouse](https://console.hw133.omani.works/app/bp-clickhouse) | **active-active (rtz-A/rtz-B)** · native replication / DNS-flip — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-clickhouse.png) |
| [opensearch](https://console.hw133.omani.works/app/bp-opensearch) | **active-active (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `ccr` — **not installed**. **Reality:** app page resolves but only generic placement editor. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-opensearch.png) |
| [stalwart-tenant](https://console.hw133.omani.works/app/bp-stalwart-tenant) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + S3 mail blobs — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-stalwart-tenant.png) |
| [stalwart-sovereign](https://console.hw133.omani.works/app/bp-stalwart-sovereign) | **external (mothership)** — not a deployable Sovereign workload. **Reality:** "App not found" (expected — external). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-stalwart-sovereign.png) |
| [livekit](https://console.hw133.omani.works/app/bp-livekit) | **active-active (rtz-A/rtz-B)** · stateless SFU / DNS-flip — **not installed**. **Reality:** app page resolves but only generic placement editor. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-livekit.png) |
| [matrix](https://console.hw133.omani.works/app/bp-matrix) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + S3 media — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-matrix.png) |
| [stunner](https://console.hw133.omani.works/app/bp-stunner) | **active-active (rtz-A/rtz-B)** · stateless TURN/STUN / DNS-flip — **not installed**. **Reality:** app page resolves but only generic placement editor. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-stunner.png) |
| [milvus](https://console.hw133.omani.works/app/bp-milvus) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `s3-bucket-replication` — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-milvus.png) |
| [neo4j](https://console.hw133.omani.works/app/bp-neo4j) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `velero` — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-neo4j.png) |
| [kserve](https://console.hw133.omani.works/app/bp-kserve) | **active-active (rtz-A/rtz-B)** · stateless model serving / DNS-flip — **not installed**. **Reality:** app page resolves but only generic placement editor. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-kserve.png) |
| [knative](https://console.hw133.omani.works/app/bp-knative) | **active-active (rtz-A/rtz-B)** · stateless / DNS-flip — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-knative.png) |
| [librechat](https://console.hw133.omani.works/app/bp-librechat) | **active-active (rtz-A/rtz-B)** · cnpg-pair sync — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-librechat.png) |
| [bge](https://console.hw133.omani.works/app/bp-bge) | **active-active (rtz-A/rtz-B)** · stateless embedding / DNS-flip — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-bge.png) |
| [llm-gateway](https://console.hw133.omani.works/app/bp-llm-gateway) | **active-active (rtz-A/rtz-B)** · stateless proxy / DNS-flip — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-llm-gateway.png) |
| [anthropic-adapter](https://console.hw133.omani.works/app/bp-anthropic-adapter) | **active-active (rtz-A/rtz-B)** · stateless adapter / DNS-flip — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-anthropic-adapter.png) |
| [langfuse](https://console.hw133.omani.works/app/bp-langfuse) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + ClickHouse — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-langfuse.png) |
| [nemo-guardrails](https://console.hw133.omani.works/app/bp-nemo-guardrails) | **active-active (rtz-A/rtz-B)** · stateless policy / DNS-flip — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-nemo-guardrails.png) |
| [temporal](https://console.hw133.omani.works/app/bp-temporal) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + opensearch — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-temporal.png) |
| [flink](https://console.hw133.omani.works/app/bp-flink) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `s3-bucket-replication` — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-flink.png) |
| [debezium](https://console.hw133.omani.works/app/bp-debezium) | **active-passive (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `mirrormaker2` — **not installed**. **Reality:** app page resolves but only generic placement editor. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-debezium.png) |
| [iceberg](https://console.hw133.omani.works/app/bp-iceberg) | **active-active (rtz-A/rtz-B)** · ❌ gap(CLASS-B) `s3-bucket-replication` — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-iceberg.png) |
| [openmeter](https://console.hw133.omani.works/app/bp-openmeter) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + ClickHouse — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-openmeter.png) |
| [litmus](https://console.hw133.omani.works/app/bp-litmus) | **singleton (rtz-A,rtz-B)** · per-cluster chaos — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-litmus.png) |
| [wordpress-tenant](https://console.hw133.omani.works/app/bp-wordpress-tenant) | **active-passive (rtz-A/rtz-B)** · cnpg-pair sync + S3 media — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-wordpress-tenant.png) |
| [qa-app](https://console.hw133.omani.works/app/bp-qa-app) | **singleton (rtz-A,rtz-B)** · test scaffold — **not installed**. **Reality:** "App not found". | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4b-qa-app.png) |

### 4c. Remaining per-cluster singleton infra (matrix `n/a-singleton`, verify-when-relevant)

All declare **singleton** across the relevant tiers, class **per-cluster infra,
Flux-reconciled from Git**, failover **N/A**. Not separately walked for DR (no
cross-region contract owed); listed for completeness.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [cilium / cilium-policies / flux / gateway-api / crossplane(+claims) · Topology](https://console.hw133.omani.works/app/bp-cilium) | **singleton (all 6 tiers)** · per-cluster infra · failover N/A. **Reality:** bp-cilium page resolves but only the generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4c-cilium.png) |
| [cert-manager(+powerdns/dynadot webhooks) / external-secrets(+stores) / external-dns / sealed-secrets · Topology](https://console.hw133.omani.works/app/bp-cert-manager) | **singleton (all 6 tiers)** · per-cluster infra · failover N/A. **Reality:** generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4c-cert-manager.png) |
| [kyverno(+policies) / trivy / falco / sigstore / syft-grype / network-policies · Topology](https://console.hw133.omani.works/app/bp-kyverno) | **singleton (all 6 tiers)** · per-cluster security infra · failover N/A. **Reality:** generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4c-kyverno.png) |
| [vpa / reloader / reflector / velero / opentelemetry(+operator) · Topology](https://console.hw133.omani.works/app/bp-velero) | **singleton (all 6 tiers)** · per-cluster infra · failover N/A. **Reality:** generic placement editor; no `singleton` read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4c-velero.png) |
| [mgmt-vcluster / rtz-vcluster / dmz-vcluster / vcluster-helmrepo · Topology](https://console.hw133.omani.works/app/bp-mgmt-vcluster) | **singleton** (tier-scoped) · per-cluster. **Reality:** bp-mgmt-vcluster page resolves but only the generic placement editor; no tier-scoped placement read-back. | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4c-mgmt-vcluster.png) |
| [hcloud-ccm / hcloud-csi / cluster-autoscaler-hcloud · Topology](https://console.hw133.omani.works/app/bp-hcloud-ccm) | **singleton** · Hetzner-only · N/A. **Reality:** bp-hcloud-ccm page resolves but only the generic placement editor (Hetzner CCM is irrelevant on this Huawei Sovereign). | ❌ | [shot](../../sessions/2026-06-14/evidence/3375/r4c-hcloud-ccm.png) |

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

---

## WALK RESULT — hw133.omani.works, 2026-06-14 (signed in as emrah.baysal, sovereign-admin via handover; 100% headless browser)

**Overall: ✅ 0 / ❌ 87.** Every row walked; not a single row passes.

**Headline finding — there is NO topology-declaration UI and NO DR/switchover UI.**
Every installed app's **"Topology" tab is the SAME generic placement EDITOR**, not a
per-app declaration display: a 3-way radio (`single-region` / `active-active` /
`active-hotstandby`) + region checkboxes (`me-east-215-a`, `me-east-215-b`) +
**Preview / Apply**, then a static *"Live status: Loading status… · Replication lag:
n/a (mode) · Last switchover: —"*. Consequently:

- **§1 declaration rows (0/29 pass):** the tab never **reads back** the matrix-
  declared topology+placement for the app (no "active-hot-standby (mgmt-A/mgmt-B)"
  assertion, no per-tier placement, no current-mode highlight). The editor does
  **not even offer** the matrix's `active-passive` or `singleton` variants — so
  apps the matrix declares as those (openbao, newapi, k8s-ws-proxy, sso-bridge,
  oidc-gate, loki, mimir, tempo, nats-jetstream, valkey, and every `singleton`
  infra row) **cannot be verified at all**. The "Topology tab shows the declared
  variant" premise the doc encodes is **false on this build**.
- **§2 / §6 region-kill rows (0/24 pass):** worse than the encoded "Switchover
  button disabled (Owner tier required)" gap — there is **no Switchover button at
  all** (not even a disabled one), no Phase/Primary/Replica/lag DR panel. The
  expected disabled-button screenshot could not be captured because the control is
  absent. gitea silent-SSO works (signed in as emrah.baysal) but the **openbao
  Vault UI OIDC sign-in errors** (`Cannot read properties of null (reading
  'postMessage')` on the `/ui/.../oidc/oidc/callback`), so the bao data halves are
  also unreachable.
- **§3 rejoin rows (0/3):** no switchover ever runs → rejoin/no-split-brain not
  observable.
- **Not-installed slugs:** `bp-openova-flow`, `bp-postgres` (×3, the shared-pg
  data-instances), plus most §4 catalog apps return **"App not found — … is not
  part of this deployment."** `bp-powerdns-admin` never finished loading.

**Bottom line:** the Topology UI on hw133 is a placement *editor*, not the
declaration+DR acceptance surface this ticket assumes. Until the app-detail
Topology tab renders the app's **current declared topology + per-tier placement**
(read-back) and a real **DR panel with a Switchover control**, none of the §1–§3
acceptance rows are user-verifiable. Evidence: 87 screenshots in
`docs/sessions/2026-06-14/evidence/3375/`.
