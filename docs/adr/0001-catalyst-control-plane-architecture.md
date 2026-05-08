# ADR-0001: Catalyst Control-Plane Architecture

| | |
|---|---|
| **Status** | Accepted (2026-05-08) |
| **Authors** | hatiyildiz, Claude (Opus 4.7) |
| **Date** | 2026-05-01 (Proposed); 2026-05-08 (Accepted with §2.3 amendment) |
| **Supersedes** | — |
| **Superseded by** | — |
| **Related** | #309, #320, #321, #322, #324, #325, #326, #347, #68; ratified under #1094 / #1095 (Phase-0 Foundation) — see [`docs/EPICS-1-6-unified-design.md`](../EPICS-1-6-unified-design.md). |

> **2026-05-08 amendment (rule 3 clarification)**: Reconciling RoleBindings, Kustomizations, ConfigMaps, and other K8s-to-K8s objects is the responsibility of Flux Kustomizations or thin in-cluster controllers — not Crossplane Compositions. The `useraccess-controller` is the canonical example: it watches `UserAccess` CRs and reconciles RoleBindings/ClusterRoleBindings via the kubernetes Go clientset. The earlier `XUserAccess` Composition that used `provider-kubernetes` is retired in EPIC-0 (#1095).

---

## 1. Why this exists

Three control planes had drifted: **Catalyst-Zero** (the wizard + provisioning API), **Catalyst-DNS** (PowerDNS + PDM in `openova-system`), and **SME** (the customer-facing marketplace stack of 13 microservices). Each picked its own auth, its own catalog, its own provisioning state-machine, its own messaging broker. State of record for "who owns which tenant" lived in two places. A customer ask for a tier-1 bank Sovereign — active/active across regions, near-zero RTO/RPO — had no coherent answer.

This ADR captures the unified architecture every Catalyst component must follow from now on. **It is the canonical reference; future work that contradicts this document is wrong by definition until this document is amended via a follow-up ADR.**

## 2. Foundational principles (the inviolable rules)

These extend `docs/INVIOLABLE-PRINCIPLES.md` — they are not in conflict with it; they make its abstract rules concrete for Catalyst.

1. **GitOps is the only deployment path.** Flux is the only reconciler on every Sovereign cluster. No `kubectl apply`, no `helm install`, no bespoke Go cloud-API calls, no Catalyst component runs `exec.Command("helm", …)` or equivalent.
2. **Crossplane is the only Day-2 cloud provisioning seam.** Cloud resources (Hetzner Servers, Volumes, LoadBalancers, Networks, etc.) are created and mutated by writing/patching Crossplane Composite Resource Claims (XRCs). Catalyst-api never calls a cloud-provider API directly.
3. **Crossplane does NOT do K8s-to-K8s composition.** Reconciling RoleBindings, Kustomizations, ConfigMaps from a higher-level intent CR is a job for Flux Kustomizations or a thin in-cluster controller — not a Crossplane Composition. Crossplane stays in its lane: cloud-provider APIs.
4. **Helm via Flux's HelmController is the only K8s manifest packaging unit.** Blueprints (`bp-<name>:<semver>` OCI artifacts) are the install primitive.
5. **K8s itself is the database for cluster state.** No shadow store mirrors pods/deployments/services into a separate database. Catalyst-api holds an in-process informer cache that is rebuilt from the kube-apiserver on cold start. There is no Mongo / FerretDB / Valkey / Postgres copy of cluster objects.
6. **Event-driven, never polling.** State is observed via K8s watch streams (client-go `SharedInformerFactory`). UI updates flow over Server-Sent Events. No `time.Tick` poll loops, no `setInterval` HTTP polls anywhere in the read path.
7. **Tenancy is K8s-native.** A Tenant is a namespace + a vCluster + a Keycloak group, with attributes carried as labels/annotations on those K8s objects (or on a thin `Tenant` CRD if a richer schema is required). There is no Postgres `tenants` table. The retired SME `auth.tenants` model is dead and stays dead.
8. **Identity is Keycloak.** Per-Sovereign realm. OIDC tokens issued by Keycloak flow end-to-end — the Sovereign's K8s api-server validates them via `--oidc-*` flags. No proxy ServiceAccounts, no custom token rotation in Catalyst code.
9. **Browser access to cluster resources is via Guacamole.** Guacamole is the access protocol for bastion sessions, pod consoles, and (where supported) RDP/VNC into VM workloads. One protocol, one audit log, one session-recording path.
10. **Catalyst's own events flow on NATS JetStream.** Audit log entries, user-action events, billing events, cross-replica fan-out, cross-region mirroring. Not Redpanda (license), not Kafka (overkill at Catalyst's volume).
11. **Five backing stores. Period.** Every Catalyst component picks from this list; nothing else qualifies as a sanctioned dependency.

## 3. The five backing stores

| Store | Tech | What goes in it | Replication mode |
|---|---|---|---|
| **SQL** | CNPG (CloudNativePG) | Keycloak's identity DB, PowerDNS records, billing/invoices, anything genuinely transactional | Streaming replication (sync to nearest region, async to far) |
| **Document (Mongo API)** | FerretDB on top of CNPG | Marketplace catalog item specs, anything with rich nested document shape | Inherits CNPG replication |
| **KV / cache** | Valkey | Sessions, rate-limit counters, idempotency keys, ephemeral pubsub | Sentinel HA in-region; not cross-region (regenerable) |
| **Messaging** | NATS JetStream | Audit log streams, billing events, user-action events, cross-replica fan-out, cross-region Mirror streams | R=3 in-region quorum; Mirror to DR region |
| **Object** | SeaweedFS | Bastion/pod session recordings (asciinema casts), large blobs | DC-aware replication |

Notable absences (deliberate): no MongoDB (FerretDB substitutes), no MySQL/MariaDB, no Redis (Valkey substitutes), no Redpanda, no Kafka in Catalyst itself, no MinIO (SeaweedFS substitutes), no separate etcd cluster (etcd is plumbing under the K8s api-server, not a tier we operate at).

## 4. Component layout

Catalyst is three deployable layers, currently in three namespaces. The **target** state collapses SME's backend services into the Sovereign API; the SME frontends remain.

### 4.1 Catalyst-Zero (`catalyst` namespace)

| Component | Image | Purpose | Persistence |
|---|---|---|---|
| `catalyst-ui` | `openova/catalyst-ui:<sha>` | React SPA — operator console at `console.<sov>` | none (stateless) |
| `catalyst-api` | `openova/catalyst-api:<sha>` | Sovereign API — wizard endpoints, provisioning lifecycle, helmwatch, infrastructure topology, RBAC editor, bastion provisioner, pod console proxy | `ProvisioningState` CRD (etcd) + flat-file event log on PVC |

`catalyst-api` is **stateless** with respect to live cluster state (the informer cache is rebuilt on restart). Its persistent state — the per-deployment event log — is recoverable from the PVC; the CRD projection on the home cluster is the long-term contract.

### 4.2 Catalyst-DNS (`openova-system` namespace)

| Component | Purpose | Persistence |
|---|---|---|
| `powerdns` | Authoritative DNS for the Sovereign's customer domains | CNPG (`pdns-pg`) |
| `pool-domain-manager` | Domain allocation API in front of PowerDNS | CNPG (`pdm-pg`) |
| `dnsdist` | DNS L7 proxy / load balancer in front of PowerDNS | none (config only) |

This subsystem already conforms to the architecture — kept as-is.

### 4.3 SME (`sme` namespace) — in transition

Today: 13 deployments (admin, auth, billing, catalog, console, domain, ferretdb, gateway, marketplace, notification, provisioning, tenant, valkey).

Target: **3 frontend deployments** + **2 thin services** + **shared backing stores**.

| Component | Today | Target |
|---|---|---|
| `marketplace` (FE) | exists | stays — calls Sovereign API |
| `console` (FE) | exists | stays — calls Sovereign API |
| `admin` (FE) | exists | stays — calls Sovereign API |
| `billing` | exists | stays — owns billing logic + CNPG + NATS subscriber |
| `notification` | exists | stays — NATS subscriber, fans out emails/webhooks |
| `auth` | exists | **retired** — Keycloak replaces it |
| `tenant` | exists | **retired** — catalyst-api absorbs the surface; tenants are K8s-native |
| `catalog` | exists | **retired** — catalyst-api owns the unified catalog |
| `provisioning` | exists | **retired** — catalyst-api is the unified provisioner |
| `domain` | exists | **retired** — catalyst-api wraps PDM |
| `gateway` | exists | **retired** — Ingress + Keycloak proxy suffices |
| `ferretdb` | exists | stays — backs `catalog` document specs |
| `valkey` | exists | stays |

Net SME deployment count: 13 → 7. Six services retire; their surfaces consolidate into catalyst-api or Keycloak.

## 5. Read path — how Catalyst sees cluster state

```
Hetzner / cloud provider API
        ▲
        │ provider-hcloud reconciles every ~30s (Crossplane's job)
        │
[Crossplane managed-resource CRD]   ←── cloud state lands as a K8s object
        │
[K8s api-server (etcd + watch cache)]
        │ watch stream — long-running HTTP/2, kube-apiserver pushes events
        ▼ (sub-second latency, event-driven, no polling)
[catalyst-api in-process Indexer (client-go SharedInformerFactory)]
        │ event handler fires on every ADDED/MODIFIED/DELETED
        ▼
[SSE endpoint /api/v1/sovereigns/{id}/k8s/stream]
        │ HTTP/2 Server-Sent Events
        ▼
[Browser EventSource → graph adapter applies addElement / removeElement]
```

**Cloud + K8s data is pre-married before it reaches Catalyst.** Crossplane projects cloud resources into K8s as managed-resource CRDs (`Server.hcloud.crossplane.io`, `LoadBalancer.hcloud.crossplane.io`, etc.). The Architecture graph adapter reads cluster-native objects (Pods, Nodes, vClusters, PVCs) and Crossplane-projected cloud objects from the same Indexer in one event stream. **One source per cluster, one cache, one stream.**

### 5.1 Cache details

| Question | Answer |
|---|---|
| Where is the cache? | In-process memory of the catalyst-api pod (the client-go `Indexer` inside each `SharedIndexInformer`) |
| Is it in Valkey? | **No.** Valkey is for ephemeral high-frequency K/V (sessions, rate-limit counters). Cluster state is in the in-process Indexer. |
| Cache size | Per-Sovereign sum of watched objects' JSON. Small Sovereign (omantel-scale): 5–10 MB. Tier-1 bank Sovereign with thousands of objects: 100–500 MB realistic, 2 GB worst-case. catalyst-api pod sized accordingly. |
| Cache update | Pure event-driven via watch streams. kube-apiserver pushes events ~50–200 ms after etcd commit. No poll loops. |
| Cold start | Per-cluster LIST (served from apiserver's in-memory cache, milliseconds) → open watch stream. Total: 1–30 s depending on Sovereign size. Disk-snapshot mitigation (#321) brings that to <1 s. |
| Per-event UI latency | Cluster state change → SSE on the wire → UI repaint: typically 200–500 ms end-to-end. |

## 6. Event path — Catalyst's own events

Two distinct streams travel to the browser over the same SSE endpoint:

```
A. K8s state events:
   kube-apiserver  ──watch──▶  catalyst-api Indexer  ──event handler──▶  SSE  ──▶  browser
   (no NATS in this path; sub-second; fully event-driven)

B. Catalyst's own events:
   catalyst-api  ──publish──▶  NATS JetStream  ──subscribe──▶  all catalyst-api replicas  ──▶  SSE  ──▶  browser
   (durable, replayable, cross-region mirrored)
```

What goes on NATS:

| Event class | Why NATS |
|---|---|
| **Audit log** (every operator action, every RBAC mutation, every Sovereign creation) | Durable, replayable, compliance-friendly. R=3 stream + retention. |
| **Cross-replica fan-out** | If user A's action lands on replica-1 and the SSE consumer is connected to replica-2, replica-2 must see it. NATS is the shared bus. |
| **Cross-region mirror** | NATS Mirror streams replicate the audit log to the DR region — supports tier-1 RTO/RPO. |
| **Billing / subscription events** | Durable + replayable; consumed by `billing` service. |
| **User-action notifications** ("Alice was granted admin") | Fans out to all connected operator browsers. |

What does **not** go on NATS:

- K8s state events. Those flow direct from the apiserver's watch stream into catalyst-api's in-process handlers and out the SSE endpoint. Adding NATS in this path would impose an unnecessary broker hop and serialise/deserialise overhead on a stream that's already real-time and reliable.

## 7. Multi-region / multi-cluster topology

### 7.1 Two scenarios, same model

- **Scenario A — One Sovereign across multiple regions** (tier-1 bank HA): 2–3 K8s clusters, each is its own kube-apiserver source. catalyst-api in each region opens informers against **all** member clusters.
- **Scenario B — Many Sovereigns, each in its own cluster, viewed from one Catalyst-Zero**: catalyst-api holds N kubeconfigs and runs N informer factories.

In both scenarios: **the source of truth is each cluster's kube-apiserver. catalyst-api is a stateless consolidator.**

### 7.2 Diagram (Scenario A — 2-region HA Sovereign)

```
┌─────────── Region A ────────────┐         ┌─────────── Region B ────────────┐
│                                  │         │                                  │
│  K8s cluster A                   │         │  K8s cluster B                   │
│  ├── kube-apiserver ──┐          │         │  ├── kube-apiserver ──┐          │
│  ├── Crossplane MRs   │          │         │  ├── Crossplane MRs   │          │
│  └── workloads        │          │         │  └── workloads        │          │
│                       │          │         │                       │          │
│   ┌───────────────────▼────────┐ │         │ ┌───────────────────▼────────┐  │
│   │ catalyst-api pod (region A)│ │         │ │ catalyst-api pod (region B)│  │
│   │  ┌─ Informer(cluster A) ─┐ │ │         │ │  ┌─ Informer(cluster A) ─┐ │  │
│   │  ┌─ Informer(cluster B) ─┐ │ │         │ │  ┌─ Informer(cluster B) ─┐ │  │
│   │  │ in-process Indexer    │ │ │         │ │  │ in-process Indexer    │ │  │
│   │  │ (both clusters)       │ │ │         │ │  │ (both clusters)       │ │  │
│   │  └───────────────────────┘ │ │         │ │  └───────────────────────┘ │  │
│   │  SSE endpoint → browsers   │ │         │ │  SSE endpoint → browsers   │  │
│   └─────┬──────────────────────┘ │         │ └─────┬──────────────────────┘  │
│         │                        │         │       │                         │
│   ┌─────▼─── NATS JetStream ─────▼─────────▼───────▼─── NATS JetStream ──┐  │
│   │  Stream: catalyst.events  (R=3, spans regions, sync-quorum writes)   │  │
│   │  Mirror: catalyst.events.region-a → region B and vice-versa          │  │
│   │  (carries: audit log, user-action events, billing, control RPCs)     │  │
│   └──────────────────────────────────────────────────────────────────────┘  │
│                                  │         │                                  │
└──────────────────────────────────┘         └──────────────────────────────────┘

         Global LB / DNS → routes browsers to nearest catalyst-api replica
```

Key properties:

- **No federated control plane.** No Karmada, no Liqo. K8s clusters are independent. Replication is at the **data layer**: CNPG streaming, NATS Mirror streams, Keycloak DB replica, SeaweedFS DC-aware topology.
- **HA is automatic.** If catalyst-api in Region A dies, Region B's catalyst-api already has all clusters' state in its Indexer (was watching them all along). UI in Region B is uninterrupted. Once Region A recovers, its catalyst-api re-LISTs and rehydrates — no data loss because it never *was* the source of truth.
- **Browsers connect to the nearest replica** via global LB / DNS. Locality of read.

### 7.3 RTO/RPO tier table

Sovereigns are provisioned at one of four tiers. The tier determines the redundancy depth. **Catalyst itself follows the tier of the customer it serves — this matters for tier-1 bank deployments where Catalyst's own audit log must meet the customer's compliance bar.**

| Tier | Customer | RPO | RTO | Topology |
|---|---|---|---|---|
| **T0 — single region** | dev / SMB | 1 hour | 30 min | 1 region, daily backups → SeaweedFS |
| **T1 — active/passive** | mid-market | 1 minute | 5 min | 2 regions, async streaming replication, Region A active + B warm standby |
| **T2 — active/passive sync** | enterprise | < 1 sec | 1–2 min | 2 regions, synchronous replication on critical paths, automatic failover |
| **T3 — active/active** | tier-1 bank | 0 | < 30 s | 3 regions, sync quorum writes, NATS Mirror across regions, multi-master CNPG via pgEdge or Citus where required |

### 7.4 NATS sufficiency for Catalyst near-zero RTO/RPO

NATS JetStream meets near-zero RTO/RPO at Catalyst's volume:

- **RPO**: With `Replicas: 3` and synchronous quorum writes (publisher receives ack only after 2 of 3 replicas have persisted to disk), RPO = 0 in steady state, ≤ 1 message under simultaneous double-replica failure.
- **RTO**: NATS clients (Go, JS, etc.) have automatic server-failover. On primary failure, clients reconnect to a surviving replica in single-digit seconds. Streams continue serving without operator action.
- **Cross-region**: Mirror streams replicate to a DR region asynchronously (typical lag < 1 s). On region failover, the DR region is promoted in ~10 s. RPO ≤ 1 s, RTO < 30 s.

NATS is the right answer for Catalyst's volume (hundreds of msg/sec at peak control-plane activity). Kafka is not chosen because:

- Per-Sovereign resource cost is far higher (~3 broker pods × ~700 MiB each vs NATS's ~64 MiB single binary).
- Operational complexity (Strimzi operator + KafkaCluster + KafkaTopic + KafkaUser CRDs + ACLs + Schema Registry + MirrorMaker 2 deployment) is unjustified.
- Catalyst's volume sits in a band where NATS is a better fit; Kafka's strengths (huge throughput, deep ecosystem) only matter at scales we don't reach.

Customer apps installed onto a Sovereign (e.g., a bank's own trading platform that requires Kafka) ship Kafka inside their own tenant — that's the customer's choice, not Catalyst's.

## 8. RBAC reconciliation (correcting the earlier #322 draft)

`UserAccess` is the intent CR, scoped per Sovereign. It declares "user X has role Y on application Z." **Reconciliation does not use Crossplane.** Two acceptable mechanisms:

**Preferred — Flux Kustomization with substitution.** `UserAccess` CRs in Git → a Flux Kustomization with `postBuild.substituteFrom` references renders one RoleBinding per (namespace × role). Reconciles every minute (Flux's normal interval). Drift heals automatically. Cost: zero new components.

**Acceptable — thin in-cluster controller.** A controller-runtime reconciler watches `UserAccess` CRs; on change, it creates/updates/deletes RoleBindings via the dynamic client. Sub-second reaction. Cost: one small Go binary (or built into catalyst-api).

Crossplane stays out of K8s-to-K8s composition.

## 9. Change vs unchanged map

### 9.1 Unchanged

| Area | Item |
|---|---|
| Deployed UI | catalyst-ui (PR #309 — accordion, force-graph, list pages) |
| catalyst-api persistence | `ProvisioningState` CRD + flat-file event log per deployment |
| catalyst-api read path | client-go dynamic client + helmwatch |
| GitOps reconciler | Flux |
| Cloud provisioning seam | Crossplane + provider-hcloud (cloud APIs only) |
| Catalyst-DNS | PowerDNS + dnsdist + PDM in `openova-system`, CNPG-backed |
| Helm install path | Flux HelmController consumes `bp-<name>:<semver>` OCI blueprints |
| SQL operator | CNPG |
| Document store | FerretDB on top of CNPG |
| Cache | Valkey |
| Tenant model | K8s namespace + vCluster + Keycloak group |
| Browser access | Guacamole (per founder direction) |

### 9.2 Changed

| # | Item | Today | Becomes | Reason |
|---|---|---|---|---|
| **B1** | UserAccess reconciliation (#322) | drafted as Crossplane Composition | Flux Kustomization OR thin controller | Crossplane is for cloud APIs; K8s-to-K8s composition is Flux's territory |
| **B2** | Bastion + pod console (#324, #325) access protocol | xterm.js + WebSocket exec; Guacamole as future addition | Guacamole as the access protocol for both | Founder explicit; one access primitive, one audit log |
| **B3** | CRUD Update path (#349) | implicit "direct API write" | XRC for cloud resources; CR write or Flux commit for K8s-native | Inviolable Principle #3 |
| **B4** | "Full relation cache" (#347 #348 item 2) | implied separate adapter logic | Source = #321 informer cache | Aligns v2 graph polish with the data-plane foundation |
| **B5** | SME messaging | Redpanda (BSL, cross-NS from talentmesh) | NATS JetStream (R=3 in-region, mirror to DR) | License + ops simplicity (issue #68) |
| **B6** | SME `auth` service | own deployment + `sme_auth` users table | Retired; Keycloak replaces | Single IAM system |
| **B7** | SME `tenant` service + `sme_auth.tenants` table | own deployment + Postgres rows | Retired; tenants are K8s-native | Single source of truth |
| **B8** | SME `catalog` service | own deployment + duplicate FerretDB catalog | Retired; catalyst-api owns unified catalog | Single catalog |
| **B9** | SME `provisioning` service | own state machine | Retired; catalyst-api is unified provisioner | Single provisioner |
| **B10** | SME `domain` service | own deployment | Retired; catalyst-api wraps PDM | Single DNS authority |
| **B11** | SME `gateway` service | own API gateway / fan-out | Retired; Ingress + Keycloak suffices | One reverse-proxy seam |

### 9.3 New (filed under follow-up tickets)

| # | Title | Scope |
|---|---|---|
| **C1** | This ADR | Approved + merged |
| **C2** | Epic — SME consolidation into Catalyst | 6 sub-issues, one per retired service (B6–B11), each "absorb into catalyst-api / Keycloak / PDM" + cutover plan |
| **C3** | Existing #68 — SME Redpanda → NATS JetStream | Update spec to match B5 |
| **C4** | Multi-region tier scaffolding | T1/T2/T3 recipes (CNPG streaming, NATS Mirror, Keycloak DB replica, DNS/LB failover) |
| **C5** | Update #322 brief | Strip Crossplane Composition; replace with Flux Kustomization or thin controller |
| **C6** | Update #324 + #325 briefs | Make Guacamole the canonical access protocol |
| **C7** | Update #349 brief | Add XRC vs K8s-native CR write rule |

### 9.4 Demo protection — the legacy SME stays running until cutover

**Hard rule for every agent and operator: until the founder explicitly authorises cutover, the entire `sme/` namespace runs untouched.** The legacy SME at the URLs below is the working customer demo and must not regress while the new surface is being built:

| URL | Backend (must keep working) |
|---|---|
| `https://console.openova.io/nova/*` | `sme/console` |
| `https://marketplace.openova.io` | `sme/marketplace` |
| `https://admin.openova.io` | `sme/admin` |

The `/sovereign/*` path on `console.openova.io` is the **new** surface (catalyst-ui in `catalyst/` namespace) and is where every architecture-aligned change lands. The two paths share a hostname but are completely independent ingresses (`console-sovereign` in `catalyst/` vs `console-nova` in `sme/`).

The B6–B11 service retirements in section 9.2 are **target-state** descriptions. The C2 epic in section 9.3 sequences the actual cutover with feature flags so:

- The new Sovereign-API surface lights up alongside the legacy SME services.
- Customer demos at the legacy URLs continue uninterrupted.
- Legacy SME services tear down only after the founder confirms parity and authorises the cutover.

Until that signal: **no SME service is stopped, no SME PVC is deleted, no SME DB is migrated, no SME ingress is repointed, no SME image is downgraded.** Anything that would break a live demo is out of bounds.

## 10. Sequencing

1. **This ADR merges first.** Gates everything else.
2. **C5 + C6 + C7 brief updates** on the open in-flight tickets (small textual changes).
3. **Dispatch #348, #349, #350** in parallel (the v2 graph polish + CRUD breadth + IA restructure). UI work that doesn't depend on the SME consolidation.
4. **Start #321** (informer + SSE data plane) as the foundational sub-issue of #320. Unblocks the long-term data path for #309 and #347.
5. **Open the C2 epic** for SME consolidation. Six sub-issues, sequenced over the following weeks; each lands behind a feature flag with cutover-and-rollback gates.
6. **C3 (Redpanda → NATS)** in parallel with C2.
7. **C4 (multi-region)** when the first tier-1 customer commits. Customer-driven; not speculative.

## 11. Glossary

| Term | Meaning in this document |
|---|---|
| **Sovereign** | A customer-dedicated cluster (or cluster set, for HA) — the deployment unit Catalyst provisions |
| **Catalyst-Zero** | The bootstrap layer: catalyst-api + catalyst-ui — operator-facing |
| **Catalyst-DNS** | The DNS subsystem: PowerDNS + PDM + dnsdist |
| **SME** | Sovereign Marketplace Engine — customer-facing marketplace stack, currently 13 services, target 7 |
| **Tenant** | A K8s namespace + vCluster + Keycloak group |
| **Blueprint** | An OCI artifact `bp-<name>:<semver>` — the install unit per Inviolable Principle #3 |
| **XRC** | Crossplane Composite Resource Claim — a CR that triggers cloud-resource provisioning |
| **MR** | Crossplane Managed Resource — the K8s-projection of a cloud-side object (e.g., `Server.hcloud.crossplane.io`) |
| **Indexer** | client-go's in-memory indexed store underneath a `SharedIndexInformer` |

## 12. Open questions

These are deliberately not answered in this ADR; each gets its own follow-up ADR.

- **Multi-master Postgres for T3.** pgEdge (BSL with commercial-friendly licence), Citus (Apache 2.0, sharded), or PostgreSQL with logical replication ring. Decision deferred until first T3 customer commits.
- **Anycast for DNS / API ingress at T3.** Provider-specific (BGP peering at edge); decision deferred.
- **Where do SME service source repos live?** Only deployment YAML is in this repo (`products/catalyst/chart/templates/sme-services/`); the `services-*` images come from elsewhere. The SME consolidation epic (C2) will surface this as it cuts over.
- **Catalyst's own audit log retention period.** 7-year is the default tier-1 bank ask; deferred to billing/compliance discussion.
