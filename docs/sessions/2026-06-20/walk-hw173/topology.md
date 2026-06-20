# Walk hw173 — Epic: topology (UAT rows 46–71)

Env: **hw173** (depID `7bb723da8da06047`), `console.hw173.omani.works` HTTP 200, deployment `status=ready`, `operationInProgress=false` (quiescent — health-gate OK).
Method: mothership-kubectl against the hw173 admin kubeconfig (`/var/lib/catalyst/kubeconfigs/7bb723da8da06047.yaml` via the catalyst-api pod) + handover-authed console API. Walker on Opus, git identity hatiyildiz.

## 🛑 Decisive env fact governing this epic

**hw173 is a SINGLE-region prov.** All 6 nodes carry `topology.kubernetes.io/region=me-east-215-a`; there is **NO region-b** node, cluster, or vCluster. The Continuum CR exists but is **Degraded** (no lease holder) because its declared `hotStandbyRegions:[hw-me-east-215-b-rtz-prod]` never materialised. And **zero `Application` CRs** (`apps.openova.io/v1`) exist cluster-wide. Every row asserting a live 2-region pair / region-a-active-region-b-standby / armed Switchover / Cluster 2/2 / "both regions" / region-kill is therefore unmet on the live data layer.

Shared evidence blocks (cited by `[En]` below):

- **[E1]** `kubectl get nodes -L topology.kubernetes.io/region` → 6 nodes, distinct region values = **`me-east-215-a` only** (no `-b`).
- **[E2]** `continuums.dr.openova.io -n cnpg cnpg-pair-bp-cnpg-pair-continuum`: `spec.primaryRegion=hw-me-east-215-a-rtz-prod`, `spec.hotStandbyRegions=[hw-me-east-215-b-rtz-prod]`, `spec.autoFailover=false`; **`status.phase=Degraded`**, `LeaseHeld=False`, `Ready=False`, `replicationLagSeconds=0`, `switchoverInProgress=false`, no `leaseHolder`.
- **[E3]** `cluster.postgresql.cnpg.io -n cnpg cnpg-pair-bp-cnpg-pair-primary`: 3/3 ready, healthy, primary `…-primary-1`; **all 3 pods on region-a worker nodes** (wc5b620/wed93d5/wfce2ae) — single-region 3-replica, not a 2-region pair.
- **[E4]** `clusters.postgresql.cnpg.io -A` → **9 CNPG clusters, all "Cluster in healthy state"** (guacamole-pg, openova-flow-pg, cnpg-pair-primary, gitea-pg, harbor-pg, newapi-pg, sme-pg, pda-pg, pdns-pg).
- **[E5]** `kubectl get pods -n mgmt` → grafana/harbor/keycloak/gitea/newapi/guacamole/loki/mimir/nats all suffixed `-x-mgmt-vcluster` (every app pod sits in a vCluster ns; mgmt/rtz/dmz vClusters all Active).
- **[E6]** `applications.apps.openova.io -A` → **0 items**; `shared-data` ns empty (no host shared-pg materialised).
- **[E7]** `GET /api/v1/deployments/7bb723da8da06047` → `region=hw-me-east-215-a-rtz-prod`, `status=ready`, `operationInProgress=false`.
- **[E8]** Source `platform/postgres/blueprint.yaml`: `placementSchema.modes=[singleton, active-active, active-hot-standby, active-passive]` (the picker vocabulary, served verbatim on the catalog-item wire); `configSchema.topology.mode.enum=[singleton, active-hot-standby]`.
- **[E9]** app pods (region-a only): grafana 3/3, keycloak-0 1/1, pda 1/1 + powerdns-admin 1/1, guacamole-server 1/1 — all Running.
- **[E10]** `kubectl get hr -A` → 62 Ready=True, 3 blank (bp-cluster-autoscaler-hcloud / bp-hcloud-ccm / bp-velero — peripheral).

The operator-console catalog-detail / Topology-tab UI (testids `cif-*`, `catalog-detail-ed…`, `readTopologies`, declared-vs-effective, Switchover button) is NOT in this public repo's `core/console`; it lives in openova-private and renders client-side. Pages return HTTP 200 (SPA shell) but the JS-rendered assertions are not API-reachable from a headless walker, so pure-render rows are ⚠️ unless the live data layer directly contradicts/supports them.

## Verdicts

| Row | Verdict | Evidence (HTTP/JSON/kubectl) | Note |
|-----|---------|------------------------------|------|
| 46 | ⚠️ | `GET console.hw173/catalog/bp-postgres → 200` (SPA shell); create-dialog `<select>` is JS-render, not API-reachable | catalog UI in openova-private; can't confirm dialog opens headless |
| 47 | ⚠️ | [E8] source `placementSchema.modes=[singleton,active-active,active-hot-standby,active-passive]` matches the asserted 4-mode vocab | Source matches; LIVE `<select>` render is browser-only (openova-private UI) |
| 48 | ⚠️ | [E8] `active-passive` ∈ placementSchema.modes (source) | Selectable per source; live render not headless-verifiable |
| 49 | ⚠️ | [E8] `singleton` ∈ placementSchema.modes, distinct from multi-region modes (source) | Source supports; live render browser-only |
| 50 | ❌ | [E6] 0 `Application` CRs cluster-wide; a "Provision active-hot-standby" never produced an Application/instance | No live create succeeded; nothing materialised on hw173 |
| 51 | ❌ | [E1] no region-b; [E2] continuum Degraded; [E6] no shared-pg Application | "region-a active + region-b standby as ONE placement" — region-b absent |
| 52 | ❌ | [E1]/[E2] no region-b standby exists | "standby region-b scaled-down" — there is no region-b copy |
| 53 | ⚠️ | per-app Topology tab is JS-render (openova-private); [E6] no Application CRs back it | Placement view not headless-reachable; no app CR to drive it |
| 54 | ⚠️ | [E8] one vocabulary in source (placementSchema canonical); header/picker dialect-match is a render assertion | Source canonical; live strip render browser-only |
| 55 | ❌ | [E2] effective topology = Degraded/no-lease; declared active-hot-standby ≠ effective | declared-vs-effective: effective is unhealthy single-region, not a live pair |
| 56 | ❌ | [E1]/[E2] region-b replica absent; `replicationLagSeconds=0` is the Degraded default, not a live cross-region lag | "region-a primary, region-b replica + live lag" — no region-b replica |
| 57 | ❌ | [E2] `LeaseHeld=False`, `phase=Degraded`, no live 2-region cnpg-pair ([E1]) | Switchover cannot be honestly "armed" — no live pair backs it |
| 58 | ⚠️ | singleton-app (cilium) Topology tab DR-hidden is a render assertion (openova-private UI) | Cannot confirm headless; cilium has no Application CR [E6] |
| 59 | ❌ | [E6] no `Application` CRs; no singleton instance was provisioned via Catalog New-instance | Create→placement-render never happened on hw173 |
| 60 | ❌ | [E1] no region-b; [E6] no Application CR; an active-hot-standby provision can't show a 2-region pair | Same single-region root; no 2-region pair to render |
| 61 | ❌ | [E6] 0 Application CRs → no newly-provisioned postgres instance cards exist | Apps grid has no created-instance cards w/ topology badges |
| 62 | ❌ | [E2] Continuum `phase=Degraded`, `LeaseHeld=False`, no lease holder, no standby — not a live Ready DR status | "live Continuum status Ready / lease holder / standby" — Continuum is Degraded |
| 63 | ⚠️ | grafana has no live DR backing ([E2] only continuum is the cnpg-pair, Degraded); honest "no live DR / Switchover unavailable" is the EXPECTED state but render is browser-only | Data supports the honest-disabled state; UI render not headless-verifiable |
| 64 | ❌ | [E2] `replicationLagSeconds=0` is the Degraded/no-replica default; [E1] no region-b replica | Field shows 0 because there is no replica, not a live cross-region lag value |
| 65 | ❌ | [E1] only region me-east-215-a exists; [E7] `region=hw-me-east-215-a-rtz-prod` | True region count = 1/1, NOT "Cluster 2/2"; the row asserts a healthy 2-region prov |
| 66 | ❌ | [E1] single region; no `me-east-215-b` cluster/nodes | "2/2 HEALTHY clusters, one per region" — region-b cluster absent |
| 67 | ⚠️ | [E9] grafana 3/3 Running in region-a; no crashloop | Healthy in the ONE region, but "in BOTH regions" is unmet ([E1] no region-b) |
| 68 | ⚠️ | [E9] powerdns-admin 1/1 + pda-pg 1/1 Running, no "could not translate host" | App healthy (region-a); "both regions" clause unmet ([E1]) |
| 69 | ⚠️ | [E9] keycloak-0 1/1 Running (mgmt vcluster), no UnknownHostException | Healthy in region-a; "both regions" unmet ([E1]) |
| 70 | ⚠️ | [E9] guacamole-server 1/1 + guacd 1/1 Running, no missing-recordings-PVC error | Healthy in region-a; "both regions" unmet ([E1]) |
| 71 | ❌ | [E2] Continuum `phase=Degraded`, no lease held, no region-b standby ([E1]), lag=0 default | Region-kill baseline requires live Continuum Ready + lease region-a + region-b standby — all absent |

## Tally

- ✅ 0
- ❌ 14 — rows 50, 51, 52, 55, 56, 57, 59, 60, 61, 62, 64, 65, 66, 71
- ⚠️ 12 — rows 46, 47, 48, 49, 53, 54, 58, 63, 67, 68, 69, 70

## Root cause (one line)

hw173 provisioned **single-region** (region-a only); the active-hot-standby Continuum is **Degraded** with no region-b, and **zero Application CRs** exist — so every live 2-region / region-pair / armed-Switchover / "both regions" / region-kill topology assertion fails on the data layer, and the remaining rows are browser-only renders of an openova-private UI not reachable from a headless walker.
