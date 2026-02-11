# OpenOva Project Memory

> Last Updated: 2026-02-08
> Purpose: Persistent context for Claude Code sessions about OpenOva platform strategy and architecture

---

## 0. Final Building Blocks Table (2026-01-17)

### Mandatory Components (Always Installed)

| Category | Component | Purpose |
|----------|-----------|---------|
| IaC | Terraform | Bootstrap provisioning |
| IaC | Crossplane | Day-2 cloud resources |
| CNI | Cilium | eBPF networking + Hubble |
| Mesh | Cilium Service Mesh | mTLS, L7 policies (replaces Istio) |
| WAF | Coraza | OWASP CRS with Envoy Gateway |
| GitOps | Flux | GitOps delivery (ArgoCD future option) |
| Git | Gitea | Internal Git server (bidirectional mirror) |
| TLS | cert-manager | Certificate automation |
| Secrets | External Secrets (ESO) | Secrets operator |
| Secrets | OpenBao | Secrets backend (MPL 2.0, drop-in Vault replacement) |
| Policy | Kyverno | Auto-generate PDBs, NetworkPolicies |
| Scaling | VPA | Vertical Pod Autoscaler |
| Scaling | KEDA | Event-driven + scale-to-zero |
| Observability | Grafana Stack | Alloy + Loki + Mimir + Tempo + Grafana |
| Observability | OpenTelemetry | Auto-instrumentation (independent of mesh) |
| Registry | Harbor | Container registry + scanning |
| Storage | MinIO | Fast S3 (tiered to archival) |
| Backup | Velero | Backup to archival S3 |
| DNS | ExternalDNS | Sync to DNS provider |
| GSLB | k8gb | Authoritative DNS + cross-region GSLB |
| Failover | Failover Controller | Generic failover orchestration |
| IDP | Backstage | Developer portal |

### User Choice Options

| Category | Options | Notes |
|----------|---------|-------|
| Cloud Provider | Hetzner (now), Huawei/OCI (coming) | Provider unlocks related services |
| Regions | 1 or 2 | 2 recommended for DR, 1 allowed |
| LoadBalancer | Cloud LB (~5-10/mo), k8gb DNS-based (free), Cilium L2 (free, single subnet) | Cloud LB recommended |
| DNS Provider | Cloudflare (always), Hetzner DNS, Route53/Cloud DNS/Azure DNS (if using that cloud) | Cloudflare recommended |
| Secrets Backend | OpenBao self-hosted, cloud secret managers | Self-hosted OpenBao recommended |
| Archival S3 | Cloudflare R2, AWS S3, GCP GCS, Azure Blob, OCI Object Storage, Huawei OBS | For backup + MinIO tiering |

### A La Carte Data Services

| Component | Purpose | DR Strategy |
|-----------|---------|-------------|
| CNPG | PostgreSQL operator | WAL streaming (async primary-replica) |
| MongoDB | Document database | CDC via Debezium → Strimzi (Kafka) |
| Strimzi | Apache Kafka streaming | MirrorMaker2 |
| Valkey | Redis-compatible cache (BSD-3 OSS) | REPLICAOF |

### A La Carte Communication

| Component | Purpose |
|-----------|---------|
| Stalwart | Email server (JMAP/IMAP/SMTP) |
| STUNner | WebRTC gateway |

---

## 1. Critical Architecture Decisions (2026-01-17)

### Service Mesh: Cilium (NOT Istio)

**Decision**: Cilium Service Mesh replaces Istio entirely.

**Rationale**:
- OpenTelemetry auto-instrumentation is independent of service mesh (via init container injection)
- SQL query visibility comes from OTel Java/Python/Node agents, NOT Envoy sidecars
- Cilium provides mTLS via eBPF with lower resource overhead
- Single CNI+Mesh solution reduces operational complexity

**Cilium Service Mesh Features**:
| Feature | How |
|---------|-----|
| mTLS | Cilium identity-based encryption |
| L7 Policies | Envoy proxy (CiliumEnvoyConfig) |
| Traffic Management | CiliumNetworkPolicy + HTTPRoute |
| Observability | Hubble + OTel (independent) |

### Git Provider: Gitea Only

**Decision**: Gitea is the sole internal Git provider. GitHub/GitLab options removed.

**Architecture**:
- Gitea deployed in each region
- Bidirectional mirroring between Gitea instances
- CNPG for metadata storage (async primary-replica, NOT multi-master)
- Each Gitea connects to LOCAL CNPG only
- Cross-region writes via primary region
- Gitea Actions for CI/CD and approval workflows

### DNS Architecture: k8gb Authoritative

**Decision**: k8gb acts as authoritative DNS server (NOT just a record manager).

**Architecture**:
- k8gb CoreDNS serves as authoritative DNS for GSLB zone
- Domain registrar NS records point to k8gb CoreDNS LoadBalancer IPs
- k8gb CoreDNS is SEPARATE from Kubernetes internal CoreDNS
- No Cloudflare hybrid option - k8gb handles entire GSLB zone

### Split-Brain Protection: Cloud Witness (Cloudflare)

**Decision**: Use Cloudflare Workers + KV as cloud witness for lease-based failover authority.

**Why Cloud Witness (not external DNS resolvers)**:
- External DNS resolvers can only verify if a region is reachable, not who should be active
- Lease-based approach provides true single-source-of-truth
- Prevents k8gb's DNS-based failover from causing split-brain during partitions

**Mechanism**:
- Active region holds lease in Cloudflare KV (renews every 10s, TTL 30s)
- Standby region cannot become active while lease is held
- Failover Controller gates all readiness based on lease ownership

### Failover Controller: Comprehensive Failover Orchestration

**Decision**: Build a Failover Controller that controls ALL failover (not just databases).

**Scope (Three Layers)**:
1. **External traffic** (Gateway API → k8gb): Controls HTTPRoute readiness
2. **Internal traffic** (Cilium Cluster Mesh): Controls Service endpoints
3. **Stateful services** (CNPG, MongoDB): Signals database promotion

**Key Insight**: k8gb alone cannot prevent split-brain during network partitions. The Failover Controller gates k8gb's view by controlling whether endpoints are visible.

**Architecture**:
- Cloudflare Worker + KV as witness (lease-based authority)
- Per-cluster Failover Controller with state machine (ACTIVE/STANDBY/FAILING_OVER)
- Actuators for Gateway, Service, and Database resources

**Modes**: automatic | semi-automatic | manual (for regulated environments)

### DDoS Protection: Cloud Provider Native

**Decision**: Rely on cloud provider native DDoS protection.

| Provider | Protection | Visibility |
|----------|------------|------------|
| Hetzner | Automatic, always-on | Low (black box) |
| OCI | Always-on, free | Medium |
| Huawei | Anti-DDoS Basic (free) | Low-Medium |

**No Cloudflare proxy required** - cloud providers handle volumetric attacks at edge.

**WAF (L7)**: Coraza handles application-layer protection separately.

### Multi-Region Strategy

- **Recommended 2 regions** (BCP/DR) but **1 region allowed**
- **Independent clusters** per region (NOT stretched clusters)
- Each cluster survives independently during network partition
- Async data replication between regions (eventual consistency)

### Cloud Providers

- **Primary**: Hetzner Cloud (first supported)
- **Coming Soon**: Huawei Cloud, Oracle Cloud (OCI)
- **Dropped**: Contabo (no Crossplane support), AWS/GCP/Azure (future consideration)

### LoadBalancer Strategy

- **Option 1**: Cloud provider LoadBalancers (Hetzner LB, OCI LB, etc.) - recommended
- **Option 2**: k8gb DNS-based LB (Gateway API hostNetwork + k8gb health routing) - free
- **Option 3**: Cilium L2 Mode (ARP-based, same subnet only) - free
- BGP is NOT available on target cloud providers (only bare-metal/dedicated)

### Secrets Management

- **SOPS eliminated completely** - not even for bootstrap
- **Interactive bootstrap**: Wizard generates credentials, operator saves them
- **Architecture**: Independent OpenBao per cluster + ESO PushSecrets for cross-cluster sync
- **Flow**: K8s Secret → ESO PushSecret → Both OpenBao instances simultaneously
- **ESO Generators**: Auto-create complex passwords/keys (no manual generation)
- All secrets managed via K8s CRDs (no manual OpenBao updates)

### Storage Architecture

- **MinIO**: Fast S3 (in-cluster) with tiered storage
- **Archival S3**: External cloud storage (R2, S3, GCS, Blob, OBS)
- **MinIO tiers to Archival S3** for cold data
- **Velero backs up to Archival S3** (not MinIO)
- **Harbor backs up to Archival S3**

### Cross-Region Networking

- **WireGuard mesh** for cross-region connectivity
- OR **native cloud peering** if same provider (Hetzner vSwitch, OCI FastConnect)
- Required for: OpenBao sync, k8gb coordination, data replication, Gitea mirroring

### Data Replication Patterns (All Community Edition)

| Service | Replication Method |
|---------|-------------------|
| CNPG (Postgres) | WAL streaming to standby cluster (async primary-replica) |
| Gitea | Bidirectional mirror + CNPG for metadata |
| MongoDB | CDC via Debezium → Kafka → Sink Connector |
| Strimzi (Kafka) | MirrorMaker2 (native) |
| Valkey | REPLICAOF command (async) |
| MinIO | Bucket replication |
| Harbor | Registry replication |

### MongoDB Replication (IMPORTANT)

- MongoDB Community Edition does NOT have native cross-cluster replication
- **Only option**: CDC via Debezium + Strimzi (Kafka)
- Truly independent clusters (not stretched replica set)
- Downsides: eventual consistency, conflict resolution needed, Debezium complexity

---

## 2. OpenOva Positioning & Value Proposition

### Core Identity

OpenOva.io is **NOT** another Kubernetes platform or IDP. It is:
- **Enterprise-grade support provider for open-source K8s ecosystems**
- **Transformation journey partner** for organizations adopting cloud-native
- **Converged blueprint ecosystem** with operational guarantees

### Value Proposition

"We provide enterprise-grade, end-to-end support for curated open-source ecosystems on Kubernetes. We don't just deploy technologies - we optimize, harden, upgrade, and stand behind them."

### Differentiator

- **Operational excellence** (Day-2 safety, upgrades, SLAs) - not tooling
- **Confidence as a service** - we own the pager, not the customer
- **Productized blueprints** - intellectual property is in the converged, optimized configurations

### Target Market

- Banks, telcos, petroleum (regulated industries)
- Organizations scared of OSS complexity but wanting to avoid vendor lock-in
- Teams burned by past platform attempts

---

## 3. Architecture Model

### Blueprint vs Instance Model

- **Public blueprints** (openova-io): Templates with `<tenant>` placeholders - the "class"
- **Private instances** (acme-private): Generated repos with choices made - the "instance"
- **Bootstrap wizard**: Generates instance repos from blueprints

### Three-Layer Architecture

```
+-------------------------------------------------------+
| OPENOVA BOOTSTRAP WIZARD (Managed UI)                 |
| - Hosted on OpenOva's infrastructure                  |
| - Collects credentials, runs Terraform                |
| - Export option for self-hosted bootstrap             |
| - Permanent sessions with SSO (Google/Azure)          |
| - Exits the picture after bootstrap complete          |
+-------------------------------------------------------+
                         |
                         v
+-------------------------------------------------------+
| CUSTOMER'S ENVIRONMENT (Post-Bootstrap)               |
| - Backstage (IDP - entry door for lifecycle)          |
| - Flux (GitOps delivery)                              |
| - Gitea (internal Git with bidirectional mirror)      |
| - Crossplane (selective - lifecycle abstraction)      |
| - Operators (CNPG, etc.)                              |
+-------------------------------------------------------+
                         |
                         v
+-------------------------------------------------------+
| OPENOVA BLUEPRINTS (Our IP - stays in picture)        |
| - Certified configurations                            |
| - Upgrade-safe versions                               |
| - Best practices (PDBs, VPAs, policies)               |
| - Published via Git, consumed by customer's Flux      |
+-------------------------------------------------------+
```

### Key Architectural Decisions

1. **Bootstrap wizard is SEPARATE** - independent repo/application, hosted on OpenOva
2. **Bootstrap wizard EXITS after provisioning** - must be safe to delete after day 1
3. **First cluster inherits bootstrapping capability** via Crossplane/CAPI for expansion
4. **Backstage becomes the entry point** for customer's lifecycle management
5. **OpenOva stays in picture via blueprints** - not runtime components
6. **Terraform is the unified bootstrap mechanism** (SaaS or Self-Hosted)

---

## 4. Bootstrap Modes

### Mode 1: Managed Bootstrap ("OpenOva Cloud Bootstrap")

- Customer uses OpenOva wizard (hosted UI)
- OpenOva's Terraform provisions customer's cloud infrastructure
- After bootstrap, customer's Crossplane takes over
- Customer provides cloud credentials to OpenOva (temporarily)
- Redirect to Backstage after completion

### Mode 2: Self-Hosted Bootstrap ("OpenOva Bring-Your-Own Bootstrap")

- Customer exports Terraform manifests from wizard
- Customer runs Terraform locally with their own credentials
- Credentials never leave customer environment
- Same end result: Backstage + platform stack ready

### Unified Approach

Both modes use the same Terraform manifests - only difference is WHERE terraform apply runs.

### Bootstrap Sequence

```
Terraform → K8s Cluster → Flux (bootstrap)
         → Gitea (internal Git)
         → Crossplane + Operators
         → Backstage + Grafana Stack
         → Platform ready
```

---

## 5. Git Repository: Gitea

**Fixed decision**: Gitea is the sole Git provider.

### Gitea Architecture

- Deployed in each region (active-active for reads)
- Bidirectional mirroring between instances
- CNPG for PostgreSQL metadata (async primary-replica)
- Each Gitea connects to LOCAL CNPG only
- Gitea Actions for CI/CD pipelines
- CODEOWNERS for security approval workflows

### Why Gitea (not GitHub/GitLab)

| Reason | Benefit |
|--------|---------|
| Self-hosted | Full control, no external dependency |
| Lightweight | Lower resource footprint than GitLab |
| GitOps-focused | Designed for Flux integration |
| Bidirectional mirror | Active-active reads across regions |
| Gitea Actions | GitHub Actions compatible CI/CD |

---

## 6. IDP vs Crossplane Decision

### With IDP (Backstage) in place:

- **IDP handles**: Catalog UX, form generation, YAML templating, PR creation
- **Crossplane needed only when**:
  - Multi-backend portability expected (CNPG today → managed DB tomorrow)
  - Complex compositions (one request → many resources)
  - Non-K8s resources in same catalog
  - Lifecycle coupling required

### Encapsulation Strategy: LIGHT

- Thin claims (5-10 fields max): tier, ha, backup, deletionProtection, networkProfile
- Everything else stays internal (operator defaults)
- Two-lane model: Standard (90%) + Advanced escape hatch (10%)

---

## 7. End-User Journeys

### Journey 1: Initial Bootstrap (Infra SPOC)

```
OpenOva Wizard UI → Select cloud/options → Generate Terraform
                  → Run Terraform (managed or self-hosted)
                  → Cluster + Platform ready
                  → Redirect to Backstage URL
```

### Journey 2: Day-2 Operations (App Teams via Backstage)

```
Backstage → Select blueprint (e.g., "Tier-1 Postgres")
         → Fill minimal form (tier, ha, backup)
         → PR generated to Gitea
         → Flux applies → Operator reconciles
         → Resource ready, secret injected
```

### Journey 3: Platform Extension (Infra SPOC via Backstage)

```
Backstage → Platform Admin section
         → "Add Cluster" or "Enable Capability Pack"
         → PR generated to Gitea
         → Flux + Crossplane/CAPI provision
```

### Journey 4: Blueprint Updates (OpenOva → Customer)

```
OpenOva publishes new blueprint version
         → Customer's Backstage shows notification
         → Customer reviews changelog
         → Customer clicks "Upgrade" (generates PR to Gitea)
         → Flux applies
```

---

## 8. Support Model

### Fully Supported

- Entire mandatory stack
- Selected a la carte components
- Blueprint configurations only

### Best Effort

- Customer customizations beyond blueprints
- Edge cases not in support matrix

### Unsupported

- Versions outside support matrix
- Non-blueprint configurations
- DIY operator installations

---

## 9. Decided Questions (2026-01-17)

| Question | Decision |
|----------|----------|
| Service mesh | Cilium Service Mesh (NOT Istio) |
| Git provider | Gitea only (GitHub/GitLab removed) |
| Cloud provider | Hetzner first, then Huawei/OCI. Contabo dropped. |
| Multi-region | Recommended 2 regions but 1 region allowed (independent clusters) |
| LoadBalancer | Cloud LB (default), k8gb DNS-based (free), Cilium L2 (single subnet) |
| DNS architecture | k8gb as authoritative DNS server for GSLB zone |
| Split-brain protection | Cloudflare Workers + KV (lease-based witness) |
| Failover orchestration | Failover Controller (controls external, internal, stateful) |
| DDoS protection | Cloud provider native (no Cloudflare proxy) |
| Secrets backend | Self-hosted OpenBao per cluster + ESO PushSecrets |
| SOPS | Eliminated completely |
| Harbor | Mandatory from day 1 |
| VPA | Mandatory |
| Crossplane | Mandatory for post-bootstrap cloud ops |
| MongoDB replication | CDC via Debezium + Strimzi (Kafka) |
| Redis-compatible cache | Valkey (BSD-3, Linux Foundation) |
| MinIO | Fast S3 with tiering (NOT backup target) |
| Archival S3 | R2/S3/GCS/Blob for backup + tiering |
| GitOps | Flux (ArgoCD as future option) |
| CI/CD | Gitea Actions |
| Observability | OTel auto-instrumentation (independent of mesh) + Grafana Stack |

## 10. Open Decisions / Questions

1. **Exact naming for bootstrap modes** - "Managed" vs "Self-Hosted"?
2. **First flagship blueprint** - PostgreSQL or Service Mesh?
3. **Wizard tech stack** - what to build it with?
4. **Failover Controller implementation** - research existing OSS or build new?
5. **Conflict resolution strategy** - for eventual consistency scenarios

---

## 15. RESOLVED - k8gb and Failover Architecture (2026-01-18)

### 15.1 k8gb Architecture Deep Dive

**Status:** RESOLVED

**Key Finding from Source Code Analysis:**

k8gb clusters operate **independently** with **DNS-based discovery only**:

| Aspect | k8gb Behavior |
|--------|---------------|
| Local health check | Direct service health check (Ingress/Gateway endpoints) |
| Cross-cluster "health" | DNS query to `localtargets-*` record |
| Communication | **DNS only** - no direct health checks between clusters |

**Critical Limitation:** k8gb cannot distinguish between:
- "Region is down" (failover needed)
- "Network partition" (failover NOT wanted)

Both produce the same symptom: DNS query fails or times out.

```
Cluster B queries: localtargets-app.example.com from Cluster A
├── Gets IPs → "Cluster A is healthy"
└── No IPs / timeout → "Cluster A is unavailable" (but WHY?)
```

**Scenarios Analyzed:**

| Scenario | k8gb Behavior | Problem? |
|----------|---------------|----------|
| Region truly down | Removes region from DNS | Correct |
| Network partition | Also removes region from DNS | **Incorrect failover** |
| Both healthy | Returns both regions | Correct |

**Conclusion:** k8gb is suitable for **stateless services** where brief dual-routing during partition is acceptable. For **stateful services** and strict active-passive, a Failover Controller with cloud witness is required.

### 15.2 Failover Controller Design

**Status:** RESOLVED

**Architecture Decision:** Cloudflare Workers + KV as cloud witness

| Component | Role |
|-----------|------|
| Cloudflare Worker | Lease management API |
| Cloudflare KV | Lease storage with TTL |
| Failover Controller | Per-cluster controller that manages readiness |

**Three Layers Controlled:**

1. **External** (Gateway API → k8gb): HTTPRoute readiness
2. **Internal** (Cilium Cluster Mesh): Service endpoint manipulation
3. **Stateful** (CNPG, MongoDB): Database promotion signaling

**Witness Pattern:**
- Active region holds lease (renews every 10s, TTL 30s)
- Standby region queries lease status
- If lease expires → standby acquires lease → becomes active
- Network partition: both regions reach witness → active keeps renewing → no split-brain

**Documentation:** See `failover-controller/docs/ADR-FAILOVER-CONTROLLER.md`

### 15.3 k8gb Scope Clarification

**Status:** RESOLVED

**k8gb is for EXTERNAL services only:**
- Routes traffic via DNS based on endpoint availability
- Does NOT coordinate internal services
- Does NOT handle database failover

**Internal services use Cilium Cluster Mesh:**
- Cross-region service discovery
- Failover Controller manipulates endpoints

**ExternalDNS Role:**
- Creates NS records delegating GSLB zone to k8gb
- Manages non-GSLB records in parent zone
- One-time setup for delegation, ongoing for other records

### 15.4 Gateway API Clarification

**Status:** RESOLVED

- Entry point: Kubernetes Gateway API backed by Cilium/Envoy
- Traefik (K3s default): Disabled in OpenOva deployments
- Kong: Not included (Cilium Gateway sufficient for routing)
- API Management: Future consideration if needed

### 15.5 Redis-Compatible Caching

**Status:** RESOLVED

- **Valkey** selected (Linux Foundation, BSD-3)
- Dragonfly dropped (BSL license)
- Redis OSS dropped (license concerns)

### 15.6 Harbor S3 Backend

**Status:** RESOLVED

- MinIO as S3 backend documented
- Tiered archiving to external S3 documented

### 15.7 SRE Repo

**Status:** FUTURE DISCUSSION

- VPA policies
- Topology spread
- PVC resizing
- KEDA configurations

---

## 11. New A La Carte Components (2026-01-18)

### Identity

| Component | Purpose | Use Cases |
|-----------|---------|-----------|
| Keycloak | OIDC/OAuth/FAPI Authorization Server | Any app needing auth, SSO, FAPI compliance |

### Monetization

| Component | Purpose | Use Cases |
|-----------|---------|-----------|
| OpenMeter | Usage metering | API monetization, usage tracking |
| Lago | Billing and invoicing | Subscription billing, usage-based pricing |

These are standalone a la carte components that can be used independently or bundled into meta blueprints.

---

## 12. Open Banking Meta Blueprint (2026-01-18)

### Overview

Meta blueprint that bundles a la carte components with custom services for PSD2/FAPI fintech sandboxes.

### Architecture Concept

**Meta Blueprint = A La Carte Components + Custom Services**

```
Open Banking Meta Blueprint
├── Keycloak (a la carte)      ─► FAPI Authorization
├── OpenMeter (a la carte)     ─► Usage metering
├── Lago (a la carte)          ─► Billing
└── Custom Services            ─► Open Banking specific
    ├── ext-authz
    ├── accounts-api
    ├── payments-api
    ├── consents-api
    ├── tpp-management
    └── sandbox-data
```

### Key Architectural Decision

**Envoy at the heart** - NOT Kong/Tyk. Leverages existing Cilium/Envoy investment with specialized services.

### Architecture Flow

```
TPP Request (eIDAS cert)
    |
    v
Cilium Ingress (Envoy)
    |
    +--> ext_authz Service
    |       |
    |       +--> Validate eIDAS cert
    |       +--> Check TPP registry
    |       +--> Verify consent
    |       +--> Check/decrement quota (Valkey)
    |
    v
Backend Services (Accounts/Payments/Consents)
    |
    v
Access Logs --> Kafka --> OpenMeter --> Lago
```

### Monetization Models

| Model | Flow |
|-------|------|
| Prepaid | Buy credits → Valkey balance → Atomic decrement → Block at zero |
| Post-paid | Use APIs → Meter usage → Invoice at period end |
| Subscription + Overage | Monthly base + per-call overage |

### Why Not Kong/Tyk

- Already have Cilium/Envoy for service mesh
- Open Banking logic doesn't fit plugin architecture
- Unified observability with existing Grafana stack
- Custom services give full control over PSD2 compliance

### Open Banking Standards

| Standard | Status |
|----------|--------|
| UK Open Banking 3.1 | Primary |
| Berlin Group NextGenPSD2 | Planned |
| STET (France) | Planned |

### Documentation

- ADR: `handbook/docs/adrs/ADR-OPEN-BANKING-BLUEPRINT.md`
- Spec: `handbook/docs/specs/SPEC-OPEN-BANKING-ARCHITECTURE.md`
- Blueprint: `handbook/docs/blueprints/BLUEPRINT-OPEN-BANKING.md`

---

## 13. Repository Structure

```
openova-io/                    # Public blueprints org
├── bootstrap/                 # Bootstrap wizard
├── terraform/                 # IaC modules
├── flux/                      # GitOps configs
├── handbook/                  # Documentation
├── <component>/               # Individual component blueprints
│   ├── cilium/                # CNI + Service Mesh
│   ├── gitea/                 # Git server
│   ├── failover-controller/   # Failover orchestration
│   ├── grafana/
│   ├── harbor/
│   ├── openbao/              # Secrets backend (MPL 2.0)
│   ├── k8gb/
│   ├── external-dns/
│   ├── keycloak/              # FAPI AuthZ (Open Banking)
│   ├── openmeter/             # Usage metering (Open Banking)
│   ├── lago/                  # Billing (Open Banking)
│   ├── open-banking/          # Open Banking services
│   └── ...

acme-private/                  # Example private instance
├── terraform/                 # Configured for acme
├── flux/                      # Configured for acme
├── <component>/               # Configured for acme
```

---

## 14. Key Quotes & Principles

> "Crossplane doesn't kill Terraform. It kills Terraform-as-a-control-plane."

> "The catalog is a contract, not a UI."

> "You are selling confidence, not Kubernetes. Insurance, not innovation."

> "If the bootstrap platform stays in the picture after day 1, it's doing too much."

> "IDP is the front desk. Your thin layer is the contract, the rules, and the insurance behind the desk."

> "Wrap CNPG/Strimzi only if you are intentionally offering 'databases' and 'streams' as platform products."

> "Public blueprints are the class, private instances are the objects."

> "OTel is completely independent of service mesh - that's why Cilium is a no-brainer."

---

## 15. Technical ADRs Referenced

- ADR-MULTI-REGION-STRATEGY: Independent clusters, recommended not enforced
- ADR-PLATFORM-ENGINEERING-TOOLS: Crossplane, Backstage, Flux (mandatory)
- ADR-IMAGE-REGISTRY: Harbor mandatory
- ADR-SECURITY-SCANNING: Trivy CI/CD + Harbor + Runtime
- ADR-CILIUM-SERVICE-MESH: Cilium replaces Istio
- ADR-GITEA: Gitea as sole Git provider
- ADR-FAILOVER-CONTROLLER: Generic failover orchestration
- ADR-K8GB-GSLB: k8gb as authoritative DNS
- ADR-AIRGAP-COMPLIANCE: Air-gap capable architecture

---

## 16. Competitive Landscape

### Not Competing With

- Red Hat OpenShift (distro)
- Cloud providers (AWS/GCP/Azure)
- Pure tooling vendors

### Competing For

- Regulated enterprises wanting OSS with support
- Organizations burned by OpenShift cost/complexity
- Teams needing "someone to call at 3am"

### Adjacent Players

- Upbound (Crossplane ecosystem)
- Humanitec (Platform orchestrator)
- Loft/vCluster (Multi-tenancy)

---

---

## 17. Monorepo Consolidation (2026-02-08)

### Decision

Consolidated 45+ separate GitHub repos into a single monorepo: `openova-io/openova`

### Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager (Go application)
├── platform/                # All 55 component blueprints (FLAT structure)
├── products/                # Bundled vertical solutions
│   ├── cortex/              # OpenOva Cortex - Enterprise AI Hub
│   ├── fingate/             # OpenOva Fingate - Open Banking (+ 6 services)
│   ├── titan/               # OpenOva Titan - Data Lakehouse
│   └── fuse/                # OpenOva Fuse - Microservices Integration
└── docs/                    # Platform documentation
```

### Key Decisions

| Decision | Rationale |
|----------|-----------|
| Flat platform/ structure | No hierarchical subfolders (networking/, security/, etc.) |
| Documentation shows groupings | README displays logical categories while folders stay flat |
| Products bundle platform components | Reference components from platform/, no duplication |
| Core is single Go app | Bootstrap + Lifecycle Manager with mode switch |
| 55 platform components | All flat under platform/ |
| 4 products | cortex, fingate (+ 6 services), titan, fuse |

### Component Count (61 total)

- **Platform components**: 55 (flat under platform/)
- **Fingate custom services**: 6 (accounts-api, consents-api, ext-authz, payments-api, sandbox-data, tpp-management)

### Documentation Groupings (in READMEs)

**Mandatory (Core Platform):**
| Category | Components |
|----------|------------|
| Infrastructure | opentofu, crossplane |
| GitOps & IDP | flux, gitea, backstage |
| Networking | cilium, external-dns, k8gb, stunner |
| Security | cert-manager, external-secrets, openbao, trivy, falco |
| Policy | kyverno |
| Observability | grafana, opensearch |
| Scaling | vpa, keda |
| Storage | minio, velero |
| Registry | harbor |
| Failover | failover-controller |

**A La Carte (Optional):**
| Category | Components |
|----------|------------|
| Data | cnpg, mongodb, valkey, strimzi, rabbitmq, activemq, vitess, clickhouse |
| CDC | debezium |
| Workflow | airflow, temporal |
| Integration | camel, dapr |
| Data Lakehouse | iceberg, trino, superset, flink |
| Identity | keycloak |
| Communication | stalwart |
| Monetization | openmeter, lago |
| AI/ML | knative, kserve, vllm, milvus, neo4j, langserve, librechat, airflow, searxng, bge, llm-gateway, anthropic-adapter |

### Sync to Customer Gitea

```
GitHub (monorepo)                    Customer Gitea (multi-repo)
─────────────────                    ──────────────────────────
openova/core/              ──sync──> openova-core/
openova/platform/cilium/   ──sync──> openova-cilium/
openova/platform/flux/     ──sync──> openova-flux/
```

---

## 18. Core Application Architecture (2026-02-08)

### Two Deployment Modes

| Mode | Location | Purpose | IaC Tool |
|------|----------|---------|----------|
| **Bootstrap** | Outside cluster | Initial provisioning | Terraform |
| **Lifecycle Manager** | Inside cluster | Day-2 operations | Crossplane |

### Zero External Dependencies

| Mode | State Storage | Rationale |
|------|---------------|-----------|
| Bootstrap | SQLite (embedded) | No CNPG needed for ephemeral wizard |
| Manager | Kubernetes CRDs | Native K8s, no external DB |

### Bootstrap Exits After Provisioning

The bootstrap wizard is designed to be **safely deletable** after initial provisioning. Crossplane owns all cloud resources going forward.

### No Overlap with Backstage

| Concern | Backstage | Lifecycle Manager |
|---------|-----------|-------------------|
| **Audience** | Developers (internal) | Platform operators |
| **Focus** | Service catalog, scaffolding | Platform health, upgrades |
| **Scope** | Application-level | Infrastructure-level |
| **UI** | Rich portal | Minimal admin dashboard |

---

## 19. OpenOva Cortex - AI Hub (2026-02-08)

### Overview

Enterprise AI platform with LLM serving, RAG, and intelligent agents.

### Components (12 from platform/)

knative, kserve, vllm, milvus, neo4j, langserve, librechat, airflow, searxng, bge, llm-gateway, anthropic-adapter

### Architecture

```
User Interfaces (LibreChat, Claude Code, Airflow)
         ↓
Gateway Layer (LLM Gateway, Anthropic Adapter)
         ↓
RAG Service (LangServe)
         ↓
Model Serving (KServe, vLLM)
         ↓
Knowledge Layer (Milvus vectors, Neo4j graph)
         ↓
Embeddings (BGE-M3, BGE-Reranker)
```

### Resource Requirements

| Component | CPU | Memory | GPU |
|-----------|-----|--------|-----|
| vLLM | 4 | 32Gi | 2x A10 |
| BGE-M3 | 2 | 4Gi | 1x A10 |
| BGE-Reranker | 1 | 2Gi | 1x A10 |
| Milvus (3 replicas) | 2 | 8Gi | - |
| **Total** | ~15 | ~55Gi | 4x A10 |

---

## 20. Deleted Repositories (2026-02-08)

All old separate repos deleted after monorepo consolidation:

openova-anthropic-adapter, openova-backstage, openova-bge, openova-cert-manager, openova-cilium, openova-cnpg, openova-crossplane, openova-external-dns, openova-external-secrets, openova-failover-controller, openova-flux, openova-gitea, openova-grafana, openova-harbor, openova-k8gb, openova-keda, openova-keycloak, openova-knative, openova-kserve, openova-kyverno, openova-lago, openova-langserve, openova-librechat, openova-llm-gateway, openova-milvus, openova-minio, openova-mongodb, openova-n8n, openova-neo4j, openova-openmeter, openova-redpanda, openova-searxng, openova-stalwart, openova-stunner, openova-terraform, openova-trivy, openova-valkey, openova-vault, openova-velero, openova-vllm, openova-vpa, openova-open-banking, openova-core, openova-handbook

---

*This document serves as persistent context for Claude Code sessions. Update as decisions are made.*
