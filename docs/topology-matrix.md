# Topology / DR convergence matrix — durable registry

> **Status**: the durable promotion of the transient session audit
> [`2026-06-02-per-blueprint-topology-audit.md`](sessions/2026-06-02-per-blueprint-topology-audit.md)
> into the registry **#3375** converges reality to. The agreement table below is
> **verbatim** from the 2026-06-02 audit (the source of truth, per #3375 §2.1);
> the **convergence columns** (declaration / IaC / class / walk) are added per
> #3375 §5 and refreshed as each blueprint converges to its row.
>
> **The agreement doc is the law.** Every blueprint starts from its row here —
> never from `blueprint.yaml` (a drifted implementation artifact), never from
> agent judgement. Where a row is genuinely infeasible or superseded by lived
> evidence, the change is a **ROW-AMENDMENT proposal** (see §ROW-AMENDMENTS) the
> founder adjudicates — the row stands until then.

## Canonical declaration shape (#3375 DoD-1)

ONE canonical shape carries every column of the agreement: **`spec.topology`**
(the G117 shape — `supported[] · defaults{multi-region,single-region} ·
perTopology{<bcp>:{replication,switchover,placement{tier,clusters[],roles{}}}}`).
It is the shape wired to the per-cluster HelmRelease fan-out
(`core/controllers/application/internal/render/topology.go` `ResolveTopology` →
`fanout.go` `FanoutHRs`) and CRD-CEL-validated
(`products/catalyst/chart/crds/blueprint.yaml`). It expresses the agreement's
**mechanism** (`replication.backend/mode`), **switchover** (`switchover.mechanism`
+ RTO/RPO), and **per-cluster roles** columns directly.

The legacy **`spec.placementSchema`** shape (`modes[]/default/vcluster`, G92/G93)
cannot express replication backend, switchover mechanism, or per-cluster roles —
it is the **orphan**, slated for removal once the live consumers
(`core/controllers/internal/placement/placement.go`) are migrated to the topology
path. Blueprints still carrying it are flagged `orphan-placementSchema->remove` in
the declaration column below.

## Contract per declared variant (#3375 §5)

Each non-singleton `perTopology` variant is the blueprint's **DR contract**, all
fields sourced from the agreement row + chart citations (never invented):

| Contract field | Maps to | Agreement-row source |
|---|---|---|
| **state** | `replication.backend` + `replication.mode` | "State preservation" column |
| **traffic** | `endpoints[].hostnameTemplate` + the row's external endpoint | "Default external endpoint" column |
| **writes-during-failover** | `replication.mode` (sync = 0 loss; async = lag-window) | mechanism column |
| **promotion** | `switchover.mechanism` + `switchover.rtoSeconds` | "Switchover" column |
| **rejoin** | per-row rejoin procedure (split-brain guard) | "Switchover" column |
| **degraded** | role downgrade on partial loss | placement `roles{}` |

---

## Convergence columns (#3375 §5)

`declaration` in {matches-row, fixed-this-ticket, amendment-proposed} ·
`IaC` in {implements-row(cited), gap(CLASS-B), n/a-singleton} ·
`walk` in {evidence-link, pending}

| Blueprint | Agreement row (placement) | declaration | IaC | class / note | walk |
|---|---|---|:---:|---|:---:|
| bp-catalyst-platform | active-hot-standby: mgmt-A,mgmt-B | matches-row | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-keycloak | active-hot-standby: mgmt-A,mgmt-B | matches-row | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-openbao | active-passive: mgmt-A,mgmt-B | matches-row | implements-row(this-ticket) | stretched-raft cross-region (mgmt-B = retry_join non-voter, holds mgmt-A's live KV) + bp-continuum raft-transition promotion via OSS peers.json recovery (engine, #3492) both wired; snapshot-replication = RPO floor | pending |
| bp-gitea | active-hot-standby: mgmt-A,mgmt-B | matches-row | implements-row(this-ticket) | PRIORITY §6 — cnpg-pair | pending |
| bp-harbor | active-hot-standby: mgmt-A,mgmt-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-grafana | active-hot-standby: mgmt-A,mgmt-B | matches-row | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-loki | active-passive: mgmt-A,mgmt-B | matches-row | gap(CLASS-B) | row mechanism: s3-bucket-replication — not yet wired | pending |
| bp-mimir | active-passive: mgmt-A,mgmt-B | matches-row | gap(CLASS-B) | row mechanism: s3-bucket-replication — not yet wired | pending |
| bp-tempo | active-passive: mgmt-A,mgmt-B | matches-row | gap(CLASS-B) | row mechanism: s3-bucket-replication — not yet wired | pending |
| bp-alloy | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-nats-jetstream | active-passive: mgmt-A,mgmt-B | matches-row | gap(CLASS-B) | row mechanism: raft — not yet wired | pending |
| bp-guacamole | active-hot-standby: mgmt-A,mgmt-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-k8s-ws-proxy | active-passive: mgmt-A,mgmt-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-newapi | active-passive: mgmt-A,mgmt-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-sso-bridge | active-passive: mgmt-A,mgmt-B | matches-row | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-oidc-gate | active-passive: mgmt-A,mgmt-B | fixed-this-ticket (perTopology completed; amendment-proposed — post-2026-06-02 #3374 addition) | n/a-singleton | stateless oauth2-proxy; DNS-flip only, no state IaC owed (peer of bp-sso-bridge) | pending |
| bp-self-sovereign-cutover | singleton: mgmt-A | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cilium | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cilium-policies | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-flux | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-gateway-api | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-vcluster-helmrepo | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-mgmt-vcluster | singleton: mgmt-A,mgmt-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-rtz-vcluster | singleton: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-dmz-vcluster | singleton: dmz-A,dmz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cnpg | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cnpg-pair | active-hot-standby: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | implements-row(this-ticket) | PRIORITY §6 — cnpg-pair | pending |
| bp-crossplane | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-crossplane-claims | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cert-manager | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cert-manager-powerdns-webhook | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cert-manager-dynadot-webhook | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-external-secrets | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-external-secrets-stores | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-external-dns | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-powerdns | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-powerdns-admin | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-sealed-secrets | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-coraza | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-kyverno | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-kyverno-policies | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-trivy | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-falco | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-sigstore | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-syft-grype | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-vpa | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-reloader | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-reflector | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-seaweedfs | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-velero | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-hcloud-ccm | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-hcloud-csi | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-cluster-autoscaler-hcloud | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-opentelemetry | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-opentelemetry-operator | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-network-policies | singleton: mgmt-A,mgmt-B,dmz-A,dmz-B,rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-netbird | active-hot-standby: mgmt-A,mgmt-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-spire | active-hot-standby: mgmt-A,mgmt-B | matches-row | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-openclaw | singleton: rtz-A | fixed-this-ticket (amendment applied; founder-adjudicate) | n/a-singleton | scaffold default per ROW-AMENDMENT (was empty supported[]) | pending |
| bp-ferretdb | active-passive: rtz-A,rtz-B | matches-row | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-valkey | active-passive: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | gap(CLASS-B) | row mechanism: sentinel — not yet wired | pending |
| bp-strimzi | active-active: rtz-A,rtz-B | matches-row | gap(CLASS-B) | row mechanism: mirrormaker2 — not yet wired | pending |
| bp-clickhouse | active-active: rtz-A,rtz-B | matches-row | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-opensearch | active-active: rtz-A,rtz-B | matches-row | gap(CLASS-B) | row mechanism: ccr — not yet wired | pending |
| bp-stalwart-tenant | active-passive: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-stalwart-sovereign | singleton: — | amendment-proposed + orphan-placementSchema→remove | n/a-singleton | external/mothership — row=external | pending |
| bp-livekit | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-matrix | active-passive: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-stunner | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-milvus | active-passive: rtz-A,rtz-B | matches-row | gap(CLASS-B) | row mechanism: s3-bucket-replication — not yet wired | pending |
| bp-neo4j | active-passive: rtz-A,rtz-B | matches-row | gap(CLASS-B) | row mechanism: velero — not yet wired | pending |
| bp-vllm | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-kserve | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-knative | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-librechat | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-bge | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-llm-gateway | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-anthropic-adapter | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; DNS-flip only, no state IaC owed | pending |
| bp-langfuse | active-passive: rtz-A,rtz-B | matches-row | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-nemo-guardrails | active-active: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | n/a-singleton | stateless; policies from Git, DNS-flip only | pending |
| bp-temporal | active-passive: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-flink | active-passive: rtz-A,rtz-B | matches-row | gap(CLASS-B) | row mechanism: s3-bucket-replication — not yet wired | pending |
| bp-debezium | active-passive: rtz-A,rtz-B | matches-row | gap(CLASS-B) | row mechanism: mirrormaker2 — not yet wired | pending |
| bp-iceberg | active-active: rtz-A,rtz-B | matches-row | gap(CLASS-B) | row mechanism: s3-bucket-replication — not yet wired | pending |
| bp-openmeter | active-passive: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-litmus | singleton: rtz-A,rtz-B | matches-row | n/a-singleton | per-cluster infra; Flux-reconciled from Git | pending |
| bp-wordpress-tenant | active-passive: rtz-A,rtz-B | matches-row + orphan-placementSchema→remove | implements-row(cited:cnpg-pair) | cnpg-pair sync (hw128 PASS pattern) | pending |
| bp-qa-app | singleton: rtz-A,rtz-B | amendment-proposed | n/a-singleton | test scaffold — row=TBD | pending |
---

## CLASS-B checklist — row mechanism declared, chart IaC not yet wired (#3375 §4)

These rows declare a cross-region mechanism in their agreement row + `spec.topology`
but the chart does not yet wire it. Per #3375 §4 + §9 they are **CLASS-B checklist
rows** (the variant is hidden from the console until implemented) — executed on
founder priority **after** the §6 reference pattern (cnpg-pair / gitea-blob-mirror /
openbao-snapshot) proves the shape. Each row quotes its agreement mechanism as the
requirement:

| Blueprint | Variant | Row mechanism (the requirement) | Switchover |
|---|---|---|---|
| bp-debezium | active-passive | `mirrormaker2/async` | `bp-continuum` |
| bp-flink | active-passive | `s3-bucket-replication/async` | `bp-continuum` |
| bp-iceberg | active-active | `s3-bucket-replication/async` | `none` |
| bp-loki | active-passive | `s3-bucket-replication/async` | `bp-continuum` |
| bp-milvus | active-passive | `s3-bucket-replication/async` | `bp-continuum` |
| bp-mimir | active-passive | `s3-bucket-replication/async` | `bp-continuum` |
| bp-nats-jetstream | active-passive | `raft/sync` | `bp-continuum` |
| bp-neo4j | active-passive | `velero/async` | `bp-velero-restore` |
| bp-opensearch | active-active | `ccr/async` | `ccr-promote` |
| bp-opensearch | active-passive | `ccr/async` | `ccr-promote` |
| bp-strimzi | active-active | `mirrormaker2/async` | `mm2-symmetric` |
| bp-strimzi | active-passive | `mirrormaker2/async` | `mm2-symmetric` |
| bp-tempo | active-passive | `s3-bucket-replication/async` | `bp-continuum` |
| bp-valkey | active-passive | `sentinel/async` | `sentinel-failover` |

---

## ROW-AMENDMENTS — flagged for founder adjudication (#3375 §2.2, DoD-2)

The agreement row is TBD / external / superseded; the row stands until the founder
adjudicates. No silent deviation.

| Blueprint | Agreement row (old) | Proposed row | Evidence |
|---|---|---|---|
| bp-openclaw | TBD (scaffold; all cells TBD) | `supported: [singleton]`, `singleton.placement: {tier: rtz, clusters: [rtz-A]}` (scaffold default until the app defines state) | declares no real workload yet; `supported[]` currently empty -> fails the canonical-enum gate. Singleton-rtz is the safe scaffold default matching bp-qa-app. |
| bp-qa-app | rtz-A / rtz-B `singleton` (test scaffold) | keep `singleton`; mark **non-pillar test scaffold** (no DR contract owed) | "no (test scaffold)" in the row — intentionally cluster-scoped; no cross-region mechanism. Row stands; flagged so the lint exempts it from the HA-contract requirement. |
| bp-stalwart-sovereign | external — runs on OpenOva mothership (mail.openova.io), not on this Sovereign | keep external; declare `supported: [singleton]` with **empty clusters** + an `external: true` marker so the console shows it as mothership-hosted, not as a deployable Sovereign workload | row says "external (mothership)"; the empty-clusters declaration currently trips the EMPTY-CLUSTERS gate. The amendment makes "external" a first-class declared state. |
| bp-oidc-gate | no agreement row (#3374 addition, post-dates the 2026-06-02 audit) | `active-passive: {mgmt-A active, mgmt-B passive}, replication none, switchover bp-continuum` + `singleton: mgmt-A` — the stateless-oauth2-proxy peer of bp-sso-bridge (the ONE generic OIDC gate, slot 13c mandatory infra) | the blueprint declared `supported: [active-passive, singleton]` but shipped NO `perTopology` contract — an HA-supported topology with no replication/switchover/placement. Completed this ticket to match bp-sso-bridge (stateless, mgmt-tier, DNS-flip). |

---

## Machinery convergence (#3375 §3 — below the declarations)

| Gap | Status | Owner |
|---|---|---|
| (a) canonical cluster IDs (`mgmt-A`...`rtz-B`) resolve to per-region kubeconfig Secrets — `KubeConfigSecretFor` (fanout.go) populated | **this ticket DoD-4** — cluster-ID registry + seam wiring | #3375 |
| (b) the two regions run as independent full stacks | tracked; ClusterMesh + cnpg-pair pair the stateful tiers (hw128 PASS) | #3241/#3307/#3322 |
| (c) 0 Application CRs | **#3370** (creating them now) | #3370 |
| (d) G115 — grafana ships no datasource/dashboard provisioning | **this ticket DoD-5** | #3375 |

---

_Refreshed by #3375. The agreement doc (2026-06-02) + `docs/ARCHITECTURE.md` §3/§8
+ `docs/DOD.md` Pillars 2-3 are the canon this matrix converges to._

---

# Appendix A — the 2026-06-02 agreement table, VERBATIM

> Reproduced byte-for-byte from
> [`2026-06-02-per-blueprint-topology-audit.md`](sessions/2026-06-02-per-blueprint-topology-audit.md)
> (the source of truth). Do not edit here — edit the source + re-promote.

## Catalyst control-plane tier

> Runs in mgmt clusters only. Cross-region HA = active in `mgmt-A`, hot/warm standby in `mgmt-B`. State preserved via `bp-cnpg-pair` (Postgres) + SeaweedFS S3 (blobs) + OpenBao perf-replication (secrets). Switchover orchestrated by `bp-continuum`.

| Blueprint | mgmt-A | mgmt-B | dmz-A | dmz-B | rtz-A | rtz-B | Stateful? | State preservation | Switchover | Default external endpoint |
|---|:---:|:---:|:---:|:---:|:---:|:---:|---|---|---|---|
| bp-catalyst-platform | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (Catalyst CRDs in etcd + PG state) | CRDs replicated via `bp-cnpg-pair` (etcd-backed via Catalyst-api PG); deployments are reconcilable from Git | `bp-continuum` flips PowerDNS for `console.<sov>` + `api.<sov>`; standby promotes once CNPG primary flips | `console.<sov>` + `api.<sov>` |
| bp-keycloak | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (KC realm config + sessions in PG) | PG via `bp-cnpg-pair` sync `remote_apply`; user sessions are JWT (stateless on KC) | `bp-continuum` PG promote → KC pod on standby is already running → DNS flip `auth.<sov>` (~30s TTL) | `auth.<sov>` |
| bp-openbao | 🟢 A | 🟡 P | ⬜ | ⬜ | ⬜ | ⬜ | yes (Raft store on PVC; region-local single voter today) | Within cluster: Raft consensus. Cross-cluster: STRETCHED RAFT — mgmt-B joins mgmt-A as a `retry_join_as_non_voter` node, receiving the live data-replication stream (OSS has NO perf-replication; openbao discussion #1842). RPO≈0; periodic snapshots = RPO floor | `bp-continuum` raft-transition: OSS peers.json recovery (single-voter peers.json write + Pod restart) on the standby — `transition-to-primary` is Enterprise-only, absent in OSS (openbao PR #996). KV reads uninterrupted until the single restart | `bao.<sov>` (or internal-only) |
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
