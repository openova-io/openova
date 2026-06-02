# Per-blueprint multi-region topology audit — 2026-06-02

> **Scope**: every `platform/*/blueprint.yaml` in this repo, classified by canonical tier and intended multi-region topology, with the actual hw86 deploy state at audit time. Surfaced two gaps that need follow-up issues: G115 (Grafana empty datasources) and G116 (formalize `placementSchema` across all platform blueprints).

## TL;DR

- **Zero of 56** platform blueprints declare `spec.placementSchema` today. With nothing declared, the application-controller treats every blueprint as **install-once on the primary host cluster** (mgmt). That's the de-facto answer for every app — Grafana, Keycloak, OpenBao, NATS, all of them.
- The "intended topology" column below is sourced from [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md) §3 + §8 and [`docs/DOD.md`](../DOD.md) Pillars 2-3 — **not from blueprint code**. That is the gap to formalize → **G116**.
- DoD Pillar 3 ("Two independent CNPG clusters + region-kill failover") relies on `bp-cnpg-pair`, which **no Org has activated on hw86**. Multi-Region capability is UNVERIFIED on the verification ledger.
- Grafana renders empty because no datasource ConfigMaps + no default dashboards are auto-provisioned by `bp-grafana` → **G115**.

## The 4-row example (the rows asked for first)

| App | Tier (per ARCHITECTURE.md §3) | Intended multi-region topology | hw86 actual today |
|---|---|---|---|
| **bp-catalyst-platform** | Catalyst control-plane | Per-Sovereign-cluster singleton on mgmt; cross-region = LB picks healthy Sovereign-cluster; DR via `bp-self-sovereign-cutover` reinstall | mgmt region-a only (no region-b clone) |
| **bp-openbao** | Catalyst control-plane | Raft StatefulSet 3–5 replicas per Sovereign cluster; **cross-region = INDEPENDENT vault per region**, unseal keys re-distributed (NOT a single global cluster — Raft consensus across regions = too much latency) | 1 region-a StatefulSet, recovery-seed lookup via [chart-credential-persistence pattern](../../platform/openbao/chart/templates/recovery-seed-bootstrap.yaml). region-b: not installed |
| **bp-grafana** | Catalyst control-plane (Observability) | Per-Sovereign singleton (1–2 replicas) backed by Loki/Mimir/Tempo S3; cross-region = independent install per region, dashboards NOT replicated (operator-rebuild) | 1 region-a Pod (Bitnami chart), backed by SeaweedFS S3. **Empty because no datasource ConfigMaps + no default dashboards auto-provisioned → G115.** region-b: not installed |
| **bp-opensearch** | **Application Blueprint (per-Org vCluster)** — NOT control-plane | Per-Org vCluster multi-node cluster (3-master + 3-data typical); cross-region = opt-in `bp-opensearch-cross-cluster-replication` sub-blueprint | Not installed on hw86 (no Org has activated it yet) |

## Catalyst control-plane tier (mgmt cluster only, primary Sovereign-cluster)

| Blueprint | placementSchema declared? | Intended topology | hw86 region-a | hw86 region-b |
|---|---|---|---|---|
| bp-catalyst-platform | ❌ no | Singleton on mgmt | installed | — |
| bp-keycloak | ❌ no | Singleton (1 KC + 1 PG) per Sovereign | installed | post-upgrade hook fail (blocks region-b cascade) |
| bp-openbao | ❌ no | Raft 3-5 / Sovereign, **independent per region** | installed | — |
| bp-gitea | ❌ no | Singleton (1 replica + 1 PG) per Sovereign | installed | — |
| bp-harbor | ❌ no | Singleton (registry + 1 PG + Redis) per Sovereign; OBS-backed | installed (357MB blob OBS-stall risk) | — |
| bp-grafana | ❌ no | Singleton per Sovereign; S3-backed datasources | installed (empty — G115) | — |
| bp-loki | ❌ no | Multi-replica cluster per Sovereign; OBS-backed | installed | — |
| bp-mimir | ❌ no | Multi-replica cluster per Sovereign; OBS-backed | installed | — |
| bp-tempo | ❌ no | Multi-replica per Sovereign; OBS-backed | installed | — |
| bp-alloy | ❌ no | DaemonSet (every node every cluster) | DS on region-a nodes | DS on region-b nodes |
| bp-nats-jetstream | ❌ no | 3-node JetStream per Sovereign cluster (NOT cross-region replicated) | installed | — |
| bp-guacamole | ❌ no | Singleton per Sovereign | installed | — |
| bp-k8s-ws-proxy | ❌ no | Singleton per Sovereign | installed | — |
| bp-newapi | ❌ no | Singleton per Sovereign | installed | — |
| bp-sso-bridge | ❌ no | Singleton per Sovereign (G91/G112/G113 chain) | installed | — |
| bp-self-sovereign-cutover | ❌ no | One-shot Jobs at handover (dormant otherwise) | dormant | dormant |

## Per-host-cluster infrastructure tier (installed in EVERY host cluster: mgmt + rtz + dmz)

| Blueprint | placementSchema declared? | Intended topology | hw86 region-a | hw86 region-b |
|---|---|---|---|---|
| bp-cilium | ❌ no | DaemonSet per node | DS | DS |
| bp-cilium-policies | ❌ no | CRDs per host cluster | applied | applied |
| bp-flux | ❌ no | 3-controller HA per host cluster | installed | installed |
| bp-gateway-api | ❌ no | Per host cluster | installed | installed |
| bp-vcluster-helmrepo | ❌ no | Per host cluster | installed | installed |
| bp-mgmt-vcluster | ❌ no | One per host cluster (mgmt placement) | installed | n/a (no mgmt vCluster in region-b) |
| bp-rtz-vcluster | ❌ no | One per host cluster (regulated/tenant) | installed | installed |
| bp-dmz-vcluster | ❌ no | One per host cluster (DMZ tenant) | installed | installed |
| bp-cnpg | ❌ no | Operator per host cluster | installed | installed |
| bp-cnpg-pair | ❌ no | **Active-hot-standby PAIR via Cilium ClusterMesh + sync replication (DoD Pillar 3)** | **NOT shipped end-to-end — Pillar 3 UNVERIFIED** | — |
| bp-crossplane | ❌ no | Per host cluster | installed | installed |
| bp-crossplane-claims | ❌ no | Per host cluster | applied | applied |
| bp-cert-manager | ❌ no | Per host cluster | installed | installed |
| bp-cert-manager-powerdns-webhook | ❌ no | Per host cluster | installed | installed |
| bp-cert-manager-dynadot-webhook | ❌ no | Per host cluster | installed | installed |
| bp-external-secrets | ❌ no | Per host cluster | installed | installed |
| bp-external-secrets-stores | ❌ no | Per host cluster | applied | applied |
| bp-external-dns | ❌ no | Per host cluster | installed | installed |
| bp-powerdns | ❌ no | Per host cluster HA pair | installed | installed |
| bp-powerdns-admin | ❌ no | Per host cluster | installed | installed |
| bp-sealed-secrets | ❌ no | Per host cluster controller | installed | installed |
| bp-coraza | ❌ no | Per host cluster (Gateway WAF) | installed | installed |
| bp-kyverno | ❌ no | Per host cluster | installed | installed |
| bp-kyverno-policies | ❌ no | Per host cluster (policy CRs) | applied | applied |
| bp-trivy | ❌ no | Per host cluster operator | installed | installed |
| bp-falco | ❌ no | DaemonSet per node | DS | DS |
| bp-sigstore | ❌ no | Per host cluster | installed | installed |
| bp-syft-grype | ❌ no | Per host cluster | installed | installed |
| bp-vpa | ❌ no | Per host cluster | installed | installed |
| bp-reloader | ❌ no | Per host cluster | installed | installed |
| bp-reflector | ❌ no | Per host cluster | installed | installed |
| bp-seaweedfs | ❌ no | Per host cluster multi-replica (S3 backend) | installed | installed |
| bp-velero | ❌ no | Per host cluster (backup) | installed | installed |
| bp-hcloud-ccm | ❌ no | DaemonSet per node (Hetzner only) | n/a on Huawei | n/a |
| bp-hcloud-csi | ❌ no | DaemonSet per node (Hetzner only) | n/a on Huawei | n/a |
| bp-cluster-autoscaler-hcloud | ❌ no | Per host cluster (Hetzner only) | n/a on Huawei | n/a |
| bp-opentelemetry | ❌ no | Per host cluster collector | installed | installed |
| bp-opentelemetry-operator | ❌ no | Per host cluster | installed | installed |
| bp-network-policies | ❌ no | Hollow chart; per host cluster | dormant | dormant |
| bp-netbird | ❌ no | Mesh-VPN (Catalyst-CP candidate) | not installed | — |
| bp-spire | ❌ no | Workload identity (Catalyst-CP candidate) | not installed | — |
| bp-openclaw | ❌ no | TBD scaffold | dormant | dormant |

## Application Blueprints (installed per-Org inside vClusters, NOT at platform install)

| Blueprint | placementSchema declared? | Intended topology | hw86 today |
|---|---|---|---|
| bp-ferretdb | ❌ no | Per-Org vCluster (1+ replica) | none installed (no Org activated) |
| bp-valkey | ❌ no | Per-Org vCluster cluster | none |
| bp-strimzi | ❌ no | Per-Org vCluster Kafka | none |
| bp-clickhouse | ❌ no | Per-Org vCluster cluster | none |
| bp-opensearch | ❌ no | Per-Org vCluster cluster | none |
| bp-stalwart-tenant | ❌ no | Per-Org vCluster mail | none |
| bp-stalwart-sovereign | ❌ no | Per Sovereign (mothership mail) | external (mail.openova.io) |
| bp-livekit | ❌ no | Per-Org vCluster | none |
| bp-matrix | ❌ no | Per-Org vCluster | none |
| bp-stunner | ❌ no | Per-Org vCluster TURN | none |
| bp-milvus | ❌ no | Per-Org vCluster | none |
| bp-neo4j | ❌ no | Per-Org vCluster | none |
| bp-vllm | ❌ no | Per-Org vCluster GPU | none |
| bp-kserve | ❌ no | Per-Org vCluster GPU | none |
| bp-knative | ❌ no | Per-Org vCluster | none |
| bp-librechat | ❌ no | Per-Org vCluster | none |
| bp-bge | ❌ no | Per-Org vCluster | none |
| bp-llm-gateway | ❌ no | Per-Org vCluster | none |
| bp-anthropic-adapter | ❌ no | Per-Org vCluster | none |
| bp-langfuse | ❌ no | Per-Org vCluster | none |
| bp-nemo-guardrails | ❌ no | Per-Org vCluster | none |
| bp-temporal | ❌ no | Per-Org vCluster | none |
| bp-flink | ❌ no | Per-Org vCluster | none |
| bp-debezium | ❌ no | Per-Org vCluster | none |
| bp-iceberg | ❌ no | Per-Org vCluster | none |
| bp-openmeter | ❌ no | Per-Org vCluster | none |
| bp-litmus | ❌ no | Per-Org vCluster chaos | none |
| bp-wordpress-tenant | ❌ no | Per-Org vCluster | none |
| bp-qa-app | ❌ no | Test scaffold | none |

## Two findings that fall out of this table

### G115 — bp-grafana auto-provision datasources + default dashboards

Grafana renders empty on hw86 because the bp-grafana chart does NOT ship pre-baked `datasource` ConfigMaps for the colocated Loki/Mimir/Tempo, nor any default dashboards. An operator landing on a freshly-deployed Grafana has nothing to look at — defeating the "click and observe" Pillar 1 intent. Fix scope:

- Add `templates/datasource-configmaps.yaml` with `prometheus`, `loki`, `tempo`, `mimir` datasources pointing at the in-cluster Service DNS (`http://mimir-nginx.observability.svc:80`, etc.).
- Add `templates/dashboard-configmaps.yaml` with a starter pack (cluster-overview, node-exporter, kube-state, OpenBao audit, Flux reconcile health).
- Use Grafana's sidecar-discovery pattern (`grafana.sidecar.datasources.enabled=true` + `grafana.sidecar.dashboards.enabled=true`) so additions are hot-loadable.

### G116 — formalize `placementSchema` across all platform Blueprints

The `Blueprint` CRD already defines `spec.placementSchema` (per [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md) §3 row 457) with shape `{vcluster: "mgmt|rtz|dmz|''", bcpTopology: "active-active|active-passive|active-hot-standby|singleton", regions: [...]}`. Zero blueprints populate it. The application-controller therefore has no machine-readable topology declaration to switch on — every install ends up as a chart-default singleton on the primary cluster.

Fix scope:

- Author a `placementSchema` block for each `platform/*/blueprint.yaml` matching the "Intended topology" column above. Catalyst-CP rows → `{vcluster: mgmt, bcpTopology: singleton}`. Per-host-cluster rows → `{vcluster: '', bcpTopology: per-cluster}`. Application rows → `{vcluster: rtz, bcpTopology: <as required>}`.
- CI gate: blueprint admission webhook rejects new blueprints without `placementSchema`.
- application-controller honors `placementSchema.bcpTopology` when fanning HRs across host clusters (G92 EPIC already wired the kubeConfig pivot — the gap is the data shape, not the controller).

## How this table was generated

```bash
python3 << 'PYEOF'
import os, glob, yaml
for bp in sorted(glob.glob("platform/*/blueprint.yaml")):
    name = os.path.basename(os.path.dirname(bp))
    d = yaml.safe_load(open(bp)) or {}
    ps = d.get("spec", {}).get("placementSchema", {})
    label = d.get("metadata", {}).get("labels", {}).get("catalyst.openova.io/section", "")
    print(f"{name:30} placementSchema={bool(ps)!s:5} section={label}")
PYEOF
```

Pair the output with the canonical 3-tier table in [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md) §3 + the DoD Pillar 2/3 invariants in [`docs/DOD.md`](../DOD.md) to populate the intended-topology column.
