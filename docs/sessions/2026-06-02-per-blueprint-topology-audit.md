# Per-blueprint multi-region topology — 2026-06-02

> **Scope**: every `platform/*/blueprint.yaml` mapped to a 6-cluster reference Sovereign (2 mgmt + 2 dmz + 2 rtz, across 2 regions), with per-cluster placement, state preservation, and switchover mechanism.
>
> **Today's state**: most rows below describe the **intended** topology per `docs/ARCHITECTURE.md` §8 + `docs/DOD.md` Pillars 2-3. Zero blueprints currently declare `spec.placementSchema`, so the application-controller treats every install as a singleton on the primary cluster — gap tracked as **G116**. The "intended" topology in this doc is the target state to formalize.

## Legend

| Code | Meaning |
|---|---|
| 🟢 **A** | **Active** — serves live traffic |
| 🟡 **P** | **Passive** — warm/hot standby, ready to take over |
| 🔵 **S** | **Singleton independent** — its own state, no cross-cluster sync |
| ⬜ blank | **Not deployed** in this cluster |

Cluster columns are the 6 host-clusters of the reference Sovereign:

- `mgmt-A` / `mgmt-B` — Catalyst control-plane clusters (1 per region; Pillar-3 multi-region CP)
- `dmz-A` / `dmz-B` — DMZ tenant workload clusters
- `rtz-A` / `rtz-B` — Regulated tenant workload clusters

> **Note on mgmt × 2**: today only `mgmt-A` exists (per `ARCHITECTURE.md` §8 — "Management host cluster (one per Sovereign)"). The `mgmt-B` column is the Pillar-3 multi-region CP target state; rows that show 🟢 A / 🟡 P across `mgmt-A` / `mgmt-B` describe the intended HA shape, NOT current deploy.

## Catalyst control-plane tier

> Runs in mgmt clusters only. Cross-region HA = active in `mgmt-A`, hot/warm standby in `mgmt-B`. State preserved via `bp-cnpg-pair` (Postgres) + SeaweedFS S3 (blobs) + OpenBao perf-replication (secrets). Switchover orchestrated by `bp-continuum`.

| Blueprint | mgmt-A | mgmt-B | dmz-A | dmz-B | rtz-A | rtz-B | Stateful? | State preservation | Switchover | Default external endpoint |
|---|:---:|:---:|:---:|:---:|:---:|:---:|---|---|---|---|
| bp-catalyst-platform | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (Catalyst CRDs in etcd + PG state) | CRDs replicated via `bp-cnpg-pair` (etcd-backed via Catalyst-api PG); deployments are reconcilable from Git | `bp-continuum` flips PowerDNS for `console.<sov>` + `api.<sov>`; standby promotes once CNPG primary flips | `console.<sov>` + `api.<sov>` |
| bp-keycloak | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (KC realm config + sessions in PG) | PG via `bp-cnpg-pair` sync `remote_apply`; user sessions are JWT (stateless on KC) | `bp-continuum` PG promote → KC pod on standby is already running → DNS flip `auth.<sov>` (~30s TTL) | `auth.<sov>` |
| bp-openbao | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (Raft store on PVC; 3-5 replicas/cluster) | Within cluster: Raft consensus. Cross-cluster: async perf-replication (OSS uses snapshot+restore; Enterprise has native primary→replica) | `bp-continuum` runs `vault operator raft transition-to-primary` on the standby; KV reads continue uninterrupted on replica throughout | `vault.<sov>` (or internal-only) |
| bp-gitea | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (PG + Git repo blobs on PVC) | Postgres via `bp-cnpg-pair` sync; Git blobs land on SeaweedFS S3 mirrored cross-region (async, ~seconds lag) | `bp-continuum` PG demote/promote + PowerDNS flip for `gitea.<sov>` (30s TTL); Git push paused ~5s | `gitea.<sov>` |
| bp-harbor | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (PG + Redis + image blobs on object storage) | PG via `bp-cnpg-pair`; image blobs on Huawei OBS / Hetzner S3 (cross-region replication = provider-native bucket replication, async) | `bp-continuum` PG flip + PowerDNS flip for `registry.<sov>` + `harbor.<sov>`; blob pulls keep working through replica bucket on standby region | `harbor.<sov>` + `registry.<sov>` |
| bp-grafana | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (Grafana DB + dashboards) | Grafana DB on `bp-cnpg-pair` sync; dashboards rendered from Loki/Mimir/Tempo which themselves write to SeaweedFS S3 replicated cross-region | `bp-continuum` DNS flip for `grafana.<sov>`; downstream Grafana on standby reads same shared S3 backend — ~30s convergence on TTL | `grafana.<sov>` |
| bp-loki | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (chunks on S3, index on object storage) | All persistent state on object storage (SeaweedFS or provider S3); cross-region = bucket replication async | `bp-continuum` DNS flip for `loki.<sov>`; standby Loki reads same chunks (eventually-consistent) — Active-Active feasible if read-only queries split | internal-only (queried via Grafana) |
| bp-mimir | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (TSDB blocks on object storage) | All persistent state on object storage; cross-region bucket replication | `bp-continuum` DNS flip; standby Mimir queriers read same blocks | internal-only |
| bp-tempo | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (traces on object storage) | All persistent state on object storage; cross-region bucket replication | `bp-continuum` DNS flip; standby Tempo queriers read same blocks | internal-only |
| bp-alloy | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (telemetry shipper, DaemonSet) | n/a — stateless; each node's Alloy ships to local Loki/Mimir/Tempo endpoint | n/a — DS on every node; node failure means kubelet replaces the Pod | none |
| bp-nats-jetstream | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (JetStream Raft cluster within mgmt) | Within mgmt: 3-node JetStream cluster (Raft). Cross-cluster: NATS leaf-node tunnel only — streams NOT replicated by default | `bp-continuum` leaf-node retarget + DNS flip; in-flight messages on lost region drop unless `stream.mirror` enabled | internal-only (`nats://nats:4222`) |
| bp-guacamole | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (PG with session + connection config) | PG via `bp-cnpg-pair` sync | `bp-continuum` PG flip + DNS flip for `guac.<sov>` (~30s); in-progress remote-desktop sessions drop | `guac.<sov>` |
| bp-k8s-ws-proxy | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | no (stateless WebSocket proxy) | n/a — Pods are stateless; routing comes from Kubernetes API which Catalyst-api supplies | `bp-continuum` DNS flip; new connections route to standby Pod immediately, in-flight WS sessions drop | internal (proxied by catalyst-api) |
| bp-newapi | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | no (stateless API) | n/a — config from CRDs + values | DNS flip via `bp-continuum` | internal |
| bp-sso-bridge | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | no (stateless auth bridge — G91/G112/G113 chain) | n/a — secrets via External-Secrets / openbao | DNS flip via `bp-continuum` | internal |
| bp-self-sovereign-cutover | 🟢 A | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | no (one-shot Jobs at handover) | n/a — Jobs run once then terminate (dormant on standby until activated) | n/a — single-shot pivot operation; if mgmt-A is lost mid-cutover, the cutover must be re-run from standby once promoted | none (one-shot) |

## Per-host-cluster infrastructure tier

> Runs in **every** host cluster; each cluster owns its own copy. Cross-cluster sync **is not part of these blueprints** — they're cluster-local infrastructure. Failover is N/A: a cluster failure removes one cluster's instance; the others keep running independently.

| Blueprint | mgmt-A | mgmt-B | dmz-A | dmz-B | rtz-A | rtz-B | Stateful? | State preservation | Switchover | Default external endpoint |
|---|:---:|:---:|:---:|:---:|:---:|:---:|---|---|---|---|
| bp-cilium | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (identity allocator CRDs) | NOT replicated — each cluster owns identity space. ClusterMesh shares **endpoint reachability only**, not control state | n/a — independent per cluster | optional `hubble.<sov>` |
| bp-cilium-policies | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (CiliumNetworkPolicy CRs per cluster) | Replicated via Flux from this repo (Git is the source of truth) | n/a — declarative; Flux reconciles per cluster | none (CRDs) |
| bp-flux | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (Helm release state + Kustomization status) | Each cluster's Flux state is local. Source-of-truth is the upstream Git repo replicated to Gitea (which IS cross-region replicated) | n/a — each cluster pulls from Gitea after cutover | none |
| bp-gateway-api | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (Gateway/HTTPRoute CRs) | Source-of-truth is Git/Flux | n/a | none (CRDs) |
| bp-vcluster-helmrepo | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (HelmRepository reference) | n/a — pointer to OCI registry | n/a | none |
| bp-mgmt-vcluster | 🔵 S | 🔵 S | ⬜ | ⬜ | ⬜ | ⬜ | yes (vCluster's own k8s state in PVC) | vCluster state on local PVC; not cross-cluster replicated | Pillar-3 plan: mgmt-A vCluster state replicates to mgmt-B vCluster via Velero scheduled backups + restore-on-failover | internal vCluster API |
| bp-rtz-vcluster | ⬜ | ⬜ | ⬜ | ⬜ | 🔵 S | 🔵 S | yes (vCluster state in PVC) | Per-cluster; cross-region failover = Velero restore + re-bind tenant Apps | Per-Org tenant decision via `bp-continuum` Org policy | internal vCluster API |
| bp-dmz-vcluster | ⬜ | ⬜ | 🔵 S | 🔵 S | ⬜ | ⬜ | yes (vCluster state in PVC) | Per-cluster | Same as bp-rtz-vcluster | internal vCluster API |
| bp-cnpg | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (operator; the Clusters it manages are stateful) | Operator code is stateless; Cluster CRs it manages are replicated by `bp-cnpg-pair` | n/a — operator | none (operator) |
| bp-cnpg-pair | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (PG WAL + data PVC) | **Sync streaming replication** (`remote_apply`) over Cilium ClusterMesh — zero-tx-loss; pair lives within matching tier (rtz↔rtz, dmz↔dmz, mgmt↔mgmt) | `bp-continuum` lease + CNPG `cnpg promote`; PowerDNS lua-record flip for `db.<org>.<sov>` (~5s write disruption, bank-tier RTO) | Postgres protocol `db.<org>.<sov>` |
| bp-crossplane | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (provider state in CRDs) | Provider state local; source-of-truth in Git via Composition CRs | n/a | none |
| bp-crossplane-claims | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (Claim CRs) | Replicated via Flux from Git | n/a | none (CRDs) |
| bp-cert-manager | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (Certificate + Order CRs + Secrets) | Each cluster owns its certs; OpenBao perf-replication carries the wildcard root if used | Per-cluster — cluster failure means re-issue on the surviving cluster's cert-manager | none |
| bp-cert-manager-powerdns-webhook | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (webhook) | n/a | n/a | none |
| bp-cert-manager-dynadot-webhook | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (webhook) | n/a | n/a | none |
| bp-external-secrets | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (ExternalSecret CRs + cached Secrets) | Secrets pulled fresh from OpenBao on each refresh; cache rebuilds from source | n/a — pulls from cluster-local OpenBao replica | none |
| bp-external-secrets-stores | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (ClusterSecretStore CRs) | Replicated via Flux | n/a | none |
| bp-external-dns | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (controller; PowerDNS is the state) | n/a — writes to PowerDNS REST API | n/a | none |
| bp-powerdns | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (PG-backed authoritative records) | Each cluster runs its own PowerDNS auth + recursor pair. Lua-record health-checked failover across clusters is the cross-cluster mechanism | n/a within cluster; cross-cluster failover via lua-record `ifurlup` (auto, ~30s TTL) | `ns1.<sov>`, `ns2.<sov>` (DNS protocol) |
| bp-powerdns-admin | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (admin UI state in PG) | Per-cluster PG | n/a — admin UI; if a cluster's instance dies, edit via another cluster | `pdns-admin.<sov>` |
| bp-sealed-secrets | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (sealing keypair in cluster) | Sealing private key cluster-local; decrypts only what was sealed against its public half | n/a — each cluster owns its own sealing identity; SealedSecret CRs are tagged with cluster scope | none |
| bp-coraza | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (WAF rules; rule corpus from Git) | n/a — rules from CoreRuleSet via Git | n/a | none (WAF; rides Gateway) |
| bp-kyverno | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (policy CRs + report CRs) | Policies replicated via Flux; reports are local (current-state) | n/a | none |
| bp-kyverno-policies | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (ClusterPolicy CRs) | Replicated via Flux | n/a | none (CRDs) |
| bp-trivy | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (vuln scan reports as CRs) | Reports local; rebuilds on re-scan | n/a | none |
| bp-falco | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (DaemonSet, events ship to Loki) | n/a — events shipped out | n/a | none |
| bp-sigstore | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (cosign keypair) | Per-cluster | n/a | optional `cosign.<sov>` |
| bp-syft-grype | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (SBOM + vuln pipeline) | n/a | n/a | none |
| bp-vpa | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (VPA recommendation CRs) | Per-cluster; rebuilds from observed Pod history | n/a | none |
| bp-reloader | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (controller) | n/a | n/a | none |
| bp-reflector | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (controller) | n/a | n/a | none |
| bp-seaweedfs | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (S3 object store + filer DB) | Within cluster: replicas + erasure coding. Cross-cluster: SeaweedFS Filer Remote Storage / S3 bucket replication (configurable) | n/a within cluster; cross-cluster replication is async pull | internal S3 (`s3.<sov>` optional) |
| bp-velero | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (backup catalog in PVC + blobs on S3) | Backup blobs land on per-Sovereign S3 (replicated cross-region by provider) | n/a — restore is operator-initiated | none (API only) |
| bp-hcloud-ccm | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | n/a (Hetzner-only; not used on Huawei reference Sovereign) | — | — | n/a (Hetzner) |
| bp-hcloud-csi | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | n/a (Hetzner-only) | — | — | n/a (Hetzner) |
| bp-cluster-autoscaler-hcloud | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | n/a (Hetzner-only) | — | — | n/a (Hetzner) |
| bp-opentelemetry | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (collector) | n/a | n/a | internal collector |
| bp-opentelemetry-operator | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | no (operator) | n/a | n/a | none |
| bp-network-policies | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | 🔵 S | yes (NetworkPolicy CRs; hollow chart placeholder) | Replicated via Flux | n/a | none (CRDs) |
| bp-netbird | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (management state in PG) | PG via `bp-cnpg-pair` (candidate; not currently installed) | Candidate Catalyst-CP — `bp-continuum` flip when wired | `netbird.<sov>` |
| bp-spire | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (SPIRE Server datastore in PG/sqlite) | PG via `bp-cnpg-pair` (candidate; not currently installed) | Candidate Catalyst-CP | internal |
| bp-openclaw | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | yes (scaffold; TBD) | TBD | TBD | TBD |

## Application Blueprints (per-Org, inside vClusters)

> Installed by tenants per-Org. The vCluster they live in sits in `rtz` or `dmz` tier. Cross-region HA inside a single Org's vCluster is the **vCluster-itself**'s job (state in PVC + Velero); the App Blueprint's own state replication is per-App-Blueprint and listed below.

| Blueprint | mgmt-A | mgmt-B | dmz-A | dmz-B | rtz-A | rtz-B | Stateful? | State preservation | Switchover | Default external endpoint |
|---|:---:|:---:|:---:|:---:|:---:|:---:|---|---|---|---|
| bp-ferretdb | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (MongoDB-API over PG backend) | PG via `bp-cnpg-pair` sync; ferretdb itself is stateless | `bp-continuum` PG flip → ferretdb on standby is already running | Mongo wire `db.<org>.<sov>` |
| bp-valkey | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (in-memory + AOF persistence) | Redis-style replication (master + replicas); cross-region = sentinel-pair across clusters via ClusterMesh | `bp-continuum` runs Sentinel failover; clients reconnect (~5s) | Redis wire `valkey.<org>.<sov>` |
| bp-strimzi | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | yes (Kafka log on PVC) | Per-cluster Kafka cluster; cross-region = Strimzi MirrorMaker2 (async replication of topics) | Active-Active topics replicate both ways; consumer offset translation via MM2 | Kafka wire `kafka.<org>.<sov>` |
| bp-clickhouse | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | yes (sharded + replicated MergeTree) | ClickHouse native replication via ZooKeeper/Keeper; cross-region = each region runs its own replica of the same shard | DNS flip; replicas keep ingesting on the surviving region | `clickhouse.<org>.<sov>` |
| bp-opensearch | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | yes (Lucene indices on PVC) | OpenSearch cross-cluster replication (CCR) — opt-in `bp-opensearch-cross-cluster-replication` sub-blueprint | DNS flip; CCR follower keeps catching up from leader writes | `opensearch.<org>.<sov>` + `dashboards.<org>.<sov>` |
| bp-stalwart-tenant | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (mail blobs on S3; metadata in PG) | PG via `bp-cnpg-pair`; mail blobs on tenant S3 bucket replicated cross-region | `bp-continuum` PG flip + DNS flip for `mail.<org>.<sov>` | `mail.<org>.<sov>` + IMAP/SMTP |
| bp-stalwart-sovereign | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | ⬜ | external — runs on OpenOva mothership (mail.openova.io), not on this Sovereign | external | external | external (mothership) |
| bp-livekit | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless SFU; rooms ephemeral) | n/a — rooms exist while at least one participant is connected | DNS flip; new rooms route to surviving region | `livekit.<org>.<sov>` (WebRTC) |
| bp-matrix | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (homeserver DB in PG + media on S3) | PG via `bp-cnpg-pair`; media on S3 cross-region replicated | `bp-continuum` PG flip + DNS flip for `matrix.<org>.<sov>` | `matrix.<org>.<sov>` |
| bp-stunner | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless TURN/STUN relay) | n/a | DNS flip; new ICE sessions route to surviving region | TURN/STUN (no HTTP) |
| bp-milvus | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (vector index on S3 + metadata in etcd) | etcd + S3 cross-region replication | `bp-continuum` DNS flip; standby Milvus queriers read same S3 | `milvus.<org>.<sov>` (gRPC) |
| bp-neo4j | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (graph store on PVC) | Neo4j Causal Clustering (Enterprise) or core+read-replica (Community); cross-region = backup-restore on PVC | `bp-continuum` promote read-replica to core or restore from Velero | `neo4j.<org>.<sov>` (Bolt + HTTP) |
| bp-vllm | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless inference; GPU-bound) | n/a — model weights on shared PVC or pre-baked in image | DNS flip; new requests route to surviving region | `vllm.<org>.<sov>` |
| bp-kserve | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless model serving) | n/a — model artifacts on S3 | DNS flip | `kserve.<org>.<sov>` |
| bp-knative | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (controller; serverless Pods are ephemeral) | n/a | DNS flip; scale-to-zero unaffected | none (event-driven) |
| bp-librechat | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | yes (chat history in Mongo/PG) | PG via `bp-cnpg-pair`; UI itself stateless | `bp-continuum` PG flip + DNS flip | `chat.<org>.<sov>` |
| bp-bge | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless embedding service) | n/a — model in image | DNS flip | `bge.<org>.<sov>` |
| bp-llm-gateway | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless proxy) | n/a — secrets via External-Secrets | DNS flip | `llm.<org>.<sov>` |
| bp-anthropic-adapter | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless adapter) | n/a | DNS flip | `anthropic.<org>.<sov>` |
| bp-langfuse | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (traces in PG + ClickHouse) | PG via `bp-cnpg-pair`; ClickHouse replicated within Org | `bp-continuum` PG flip + DNS flip | `langfuse.<org>.<sov>` |
| bp-nemo-guardrails | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | no (stateless policy enforcement) | n/a — policies from Git | DNS flip | internal |
| bp-temporal | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (history in PG + visibility in ES/OpenSearch) | PG via `bp-cnpg-pair`; visibility store via bp-opensearch CCR | `bp-continuum` PG flip + DNS flip; workflows resume from history | `temporal.<org>.<sov>` |
| bp-flink | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (jobmanager state + checkpoints on S3) | Checkpoints on S3 cross-region replicated; jobmanager HA via ZooKeeper/k8s leader-election | `bp-continuum` jobmanager re-elect on standby; flink resumes from last checkpoint | `flink.<org>.<sov>` |
| bp-debezium | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (offsets in Kafka) | Offsets in bp-strimzi (replicated via MM2) | `bp-continuum` restarts connector on standby reading from latest Kafka offset | internal |
| bp-iceberg | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟢 A | yes (table metadata + data files on S3) | All state on S3; catalog (Hive/REST) is the only stateful service | DNS flip on catalog; data plane keeps reading same S3 | `iceberg.<org>.<sov>` (catalog REST) |
| bp-openmeter | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (events in ClickHouse + state in PG) | PG via `bp-cnpg-pair`; ClickHouse replicated within Org | `bp-continuum` PG flip + DNS flip | `openmeter.<org>.<sov>` |
| bp-litmus | ⬜ | ⬜ | ⬜ | ⬜ | 🔵 S | 🔵 S | yes (experiment runs in MongoDB) | Per-cluster — chaos experiments are intentionally cluster-scoped | n/a | `litmus.<org>.<sov>` |
| bp-wordpress-tenant | ⬜ | ⬜ | ⬜ | ⬜ | 🟢 A | 🟡 P | yes (PG + media on S3) | PG via `bp-cnpg-pair`; media on tenant S3 bucket cross-region replicated | `bp-continuum` PG flip + DNS flip for `<site>.<org>.<sov>` | `<site>.<org>.<sov>` |
| bp-qa-app | ⬜ | ⬜ | ⬜ | ⬜ | 🔵 S | 🔵 S | no (test scaffold) | n/a | n/a | TBD (scaffold) |

## Topology patterns this captures

| Pattern | Example | Cells signature |
|---|---|---|
| **CP-tier HA** (mgmt active/standby) | bp-gitea, bp-grafana, bp-openbao, bp-keycloak | 🟢 A + 🟡 P in mgmt cols, rest blank |
| **Independent per cluster** (no inter-cluster sync) | bp-cilium, bp-flux, bp-cert-manager | 🔵 S in every cluster the chart is installed in |
| **Cross-region sync pair** (App-tier or CP-tier DB) | bp-cnpg-pair | 🟢 A + 🟡 P in matching cluster across regions |
| **DaemonSet** | bp-alloy, bp-falco | 🔵 S × 6 (every cluster, every node) |
| **Active-Active load-balanced** | bp-strimzi, bp-clickhouse, bp-opensearch, stateless APIs | 🟢 A + 🟢 A in same tier across regions |
| **External / mothership-hosted** | bp-stalwart-sovereign | all cells blank, note "external" |
| **One-shot Job** | bp-self-sovereign-cutover | 🟢 A in single cluster, dormant elsewhere |

## Gaps surfaced by this audit

### G115 — bp-grafana auto-provision datasources + default dashboards

Grafana renders empty on hw86 because the bp-grafana chart does NOT ship pre-baked `datasource` ConfigMaps for the colocated Loki/Mimir/Tempo, nor any default dashboards. Fix scope:

- Add `templates/datasource-configmaps.yaml` with `prometheus`, `loki`, `tempo`, `mimir` datasources pointing at in-cluster Service DNS.
- Add `templates/dashboard-configmaps.yaml` with a starter pack (cluster overview, node-exporter, kube-state, OpenBao audit, Flux reconcile health).
- Use Grafana sidecar-discovery (`grafana.sidecar.datasources.enabled=true` + `grafana.sidecar.dashboards.enabled=true`) so additions are hot-loadable.

### G116 — formalize `placementSchema` across all platform Blueprints

The `Blueprint` CRD already defines `spec.placementSchema` (per `ARCHITECTURE.md` §3) with shape `{vcluster: "mgmt|rtz|dmz|''", bcpTopology: "active-active|active-passive|active-hot-standby|singleton", regions: [...]}`. **Zero blueprints populate it.** Application-controller therefore has no machine-readable topology declaration to switch on — every install ends up as a chart-default singleton on the primary cluster.

Fix scope:

- Author a `placementSchema` block for each `platform/*/blueprint.yaml` matching the per-cluster placement column above.
- CI gate: blueprint admission webhook rejects new blueprints without `placementSchema`.
- application-controller honors `placementSchema.bcpTopology` when fanning HRs across host clusters (G92 EPIC already wired the kubeConfig pivot — the gap is the data shape, not the controller).

### Pillar 3 (`bp-cnpg-pair` cross-region failover) is UNVERIFIED

No Org on hw86 has activated `bp-cnpg-pair`. The active-hot-standby PAIR + Cilium ClusterMesh + sync replication + Continuum lease+DNS-flip chain has not been walked end-to-end. Verification ledger row stays UNVERIFIED until a tenant-flow walk + region-kill drill is shot.

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

Per-row intended topology cross-referenced against `docs/ARCHITECTURE.md` §3 + §8 and `docs/DOD.md` Pillars 2-3.
