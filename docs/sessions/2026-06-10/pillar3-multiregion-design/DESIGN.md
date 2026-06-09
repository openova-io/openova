# Pillar-3 Multi-Region (cnpg-pair + Continuum) — design + live gap analysis (#3189 Deliverable C)

Date: 2026-06-10 · Sovereign: hw124 (`catalyst-hw124.omani.works`) · Author: hatiyildiz

## 0. TL;DR

hw124 was assumed single-region, but live state shows it is provisioned as **two
regions of compute** (`me-east-215-a` cluster-id 168, `me-east-215-b` cluster-id
169) — each running a **complete, fully-independent** Catalyst stack (own Flux,
own 56-ish HRs, own standalone CNPG clusters). What it is **not** is a Pillar-3
*active-hot-standby* topology: there is **no cross-region replication link**.

The cross-region DR layer the 9 active-hot-standby Blueprints declare in their
`topology.perTopology.active-hot-standby` block (`replication.backend: cnpg-pair`,
`switchover.mechanism: bp-continuum`) is entirely **absent** at runtime. This doc
specifies the target path and enumerates the concrete wiring gaps observed live.

## 1. Target cross-region path (per the 9 active-hot-standby Blueprints)

Each of the 9 cnpg-pair-backed apps (bp-catalyst-platform, bp-gitea, bp-grafana,
bp-harbor, bp-keycloak, bp-guacamole, bp-netbird, bp-spire, + the bp-cnpg-pair
primitive itself) replicates Postgres state region A → region B:

```
        Region A (primary)                         Region B (replica)
   ┌──────────────────────────┐             ┌──────────────────────────┐
   │ CNPG Cluster <app>-pg     │  WAL        │ CNPG ReplicaCluster       │
   │   primary + 2 HA replicas │  stream     │   bootstrap.pg_basebackup │
   │                           │ ──────────▶ │   externalClusters[]:     │
   │ Service <app>-pg-primary  │  over       │     <app>-pg-primary-mesh │
   │   -mesh                   │  Cilium     │   replica.enabled: true   │
   │   annotation:             │  ClusterMesh│                           │
   │   service.cilium.io/      │             │ promotable when WAL-lag   │
   │   global: "true"          │             │   < walStreaming threshold│
   └──────────────────────────┘             └──────────────────────────┘
                │                                         ▲
                │  Continuum CR (dr.openova.io/v1)        │
                ▼                                         │
   ┌──────────────────────────────────────────────────────────────────┐
   │ continuum-controller — 7-step switchover on region-A-kill:        │
   │  1 detect primary loss (lease witness: cloudflare-kv / dns-quorum) │
   │  2 verify replica WAL-lag < threshold (RPO=0 gate)                 │
   │  3 CNPG promote ReplicaCluster → primary (pg_ctl promote)          │
   │  4 PDM lua-record DNS flip (A-record → region-B ingress)           │
   │  5 drain region-A HTTPRoutes                                       │
   │  6 post-switchover health probe (multi-vantage DNS resolve)        │
   │  7 NATS audit publish (catalyst.audit)                             │
   │  Target: RTO 30s · RPO 0 (sync WAL).                              │
   └──────────────────────────────────────────────────────────────────┘
```

The mechanics already exist in code:
- `platform/cnpg-pair/chart` renders primary Cluster + replica ReplicaCluster +
  the `*-primary-mesh` global Service + the `*-audit-config` ConfigMap
  (label `catalyst.openova.io/audit-config: "true"`).
- `core/controllers/continuum` ships the full 7-step Sequencer, lease witnesses
  (cloudflare-kv / dns-quorum), PDM lua-record client, and health probe.
- `bp-continuum`'s `prerequisite-check` probes for the cnpg-pair audit ConfigMap
  to decide active-vs-idle.

## 2. Live gap analysis on hw124 (what's missing for Pillar-3)

| # | Gap (observed live) | Evidence |
|---|---------------------|----------|
| G1 | **No `bp-cnpg-pair` installed.** Only `bp-cnpg` (the operator) is present. No primary/replica cluster-pair, no ReplicaCluster, no `*-primary-mesh` Service. | `kubectl get hr -A` → `bp-cnpg` only; `kubectl get cm -A -l catalyst.openova.io/audit-config=true` → none. |
| G2 | **`bp-cnpg-pair` is not in the bootstrap-kit.** `clusters/_template/bootstrap-kit/` has `62-bp-continuum.yaml` but no cnpg-pair slot, so a fresh prov never installs it. The chart also defaults `cnpgPair.enabled: false`. | `ls clusters/_template/bootstrap-kit/ \| grep cnpg-pair` → empty. |
| G3 | **Cilium ClusterMesh between A and B is not connected.** Region names/IDs differ (precondition met) but the `cilium-clustermesh` Secret is absent in region A, so cross-region pod/service routing — the transport cnpg-pair WAL streaming rides on — is down. | `kubectl -n kube-system get secret cilium-clustermesh` → NotFound; `cilium-config` cluster-name A=`hw124-mesh`/168, B=`hw124-me-east--b`/169. |
| G4 | **No global services.** No Service carries `service.cilium.io/global: "true"`, so even with mesh up there is nothing for region B to consume. | Service annotation scan → 0 hits. |
| G5 | **All CNPG clusters are standalone HA-within-region.** Both regions independently run `gitea-pg`, `harbor-pg`, etc. as 2-instance clusters — no `ReplicaCluster` kind anywhere. | `kubectl get clusters.postgresql.cnpg.io -A` in both regions → all standalone primaries. |
| G6 | **No `Continuum` CR seeded.** continuum-controller has 0 CRs to reconcile (correctly idle once the crashloop guard is fixed — see #3189 A2). | `kubectl get continuum -A` → none. |

## 3. Why a fresh 2-region prov was NOT fired

Per the #3189 brief, autonomy was granted to provision a fresh 2-region Sovereign.
A fresh prov was **deliberately not fired** because it would land in the *exact same
state* observed above: the bootstrap-kit ships `bp-continuum` but neither
`bp-cnpg-pair` (G2) nor a ClusterMesh-peering step (G3), so the cross-region DR
layer would again be absent. Provisioning would burn HCS EIP/VPC quota
(`feedback_hcs_quota_structural_blocker_pattern`) and reproduce, not close, the gap.

The gap is in the **bootstrap-kit wiring + ClusterMesh peering**, not in any
per-Blueprint topology declaration (those are clean — see #3189 Deliverable B).
The single-region-safe code fixes (#3189 A1–A3) are the shippable, verified work
this session; the Pillar-3 enablement below is the follow-up backlog.

## 4. Follow-up backlog to enable Pillar-3 (ordered)

1. **ClusterMesh peering as a bootstrap-kit step** (G3): after both regions' k3s
   are up, exchange CA + apiserver endpoints and write the `cilium-clustermesh`
   Secret on both sides (or `cilium clustermesh connect`). This is the transport
   prerequisite for everything else.
2. **Add `bp-cnpg-pair` to the bootstrap-kit** (G1/G2) gated on
   `SOVEREIGN_ENABLE_HOT_STANDBY=true` + a 2nd region present, with
   `cnpgPair.enabled: true`, `primary.region`/`replica.region` stamped from the
   deployment's region list. One cnpg-pair per stateful app (or a shared
   convention) so each `*-pg` primary in A gets a ReplicaCluster in B.
3. **Seed one `Continuum` CR per active-hot-standby app** referencing its
   cnpg-pair + lease witness, so continuum-controller leaves idle and starts
   reconciling (the prerequisite-check then logs PASS, not the single-region
   idle path from #3189 A2).
4. **Region-kill failover walk**: drop region A, assert continuum promotes the B
   ReplicaCluster + flips DNS within RTO 30s with 0 tx lost (RPO 0), capture
   evidence per `docs/ledger/UAT.md`.

## 5. Relationship to #3189 single-region fixes

The #3189 A2 fix (continuum idle-no-op on single-region) is the **correct default**
until items 1–4 land: with no cnpg-pair, continuum-controller MUST sit idle-Ready,
not crashloop. Once the backlog wires cnpg-pair + a Continuum CR, the same probe
logs PASS and the controller activates — no further code change to bp-continuum
needed. The two are complementary: A2 makes the absence graceful; §4 makes the
presence functional.
