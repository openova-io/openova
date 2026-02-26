# OpenOva

**Enterprise-grade support provider for open-source Kubernetes ecosystems.**

OpenOva provides a converged blueprint ecosystem with operational guarantees, enabling cloud-native transformation for enterprises.

---

## Documentation

| Document | Description |
|----------|-------------|
| [Platform Tech Stack](docs/PLATFORM-TECH-STACK.md) | Technology stack and architecture |
| [SRE Handbook](docs/SRE.md) | Site reliability practices |
| [Core Application](core/README.md) | Bootstrap + Lifecycle Manager |
| [Business Strategy](docs/BUSINESS-STRATEGY.md) | Product strategy and GTM |
| [Technology Forecast](docs/TECHNOLOGY-FORECAST-2027-2030.md) | Component forecast 2027-2030 |

---

## Repository Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager
├── platform/                # All 52 component blueprints (flat)
├── products/                # Bundled vertical solutions
│   ├── cortex/              # OpenOva Cortex - Enterprise AI Hub
│   ├── fingate/             # OpenOva Fingate - Open Banking (+ 6 services)
│   ├── fabric/              # OpenOva Fabric - Data & Integration
│   ├── relay/               # OpenOva Relay - Communication
│   └── axon/                # OpenOva Axon - SaaS LLM Gateway
└── docs/                    # Platform documentation
```

---

## What We Provide

| Offering | Description |
|----------|-------------|
| **Converged Blueprints** | Production-ready K8s component bundles |
| **Day-2 Operations** | Upgrades, security, SLA guarantees |
| **Transformation Journey** | Cloud-native adoption partnership |

---

## Platform Architecture

```
Bootstrap Wizard → Customer's K8s + Catalyst IDP + Flux + Gitea
                 → OpenOva Blueprints (stays in picture)
```

**Two-Phase Provisioning:**
- **Bootstrap (OpenTofu)**: Initial cluster + core components
- **Lifecycle Manager (Crossplane)**: Day-2 operations + a la carte components

---

## Platform Components (52)

All components under `platform/` (flat structure):

### Mandatory (Core Platform)

#### Infrastructure & Provisioning

| Component | Purpose |
|-----------|---------|
| [opentofu](platform/opentofu/) | Infrastructure as Code (bootstrap, MPL 2.0) |
| [crossplane](platform/crossplane/) | Day-2 cloud resource provisioning |

#### GitOps & Git

| Component | Purpose |
|-----------|---------|
| [flux](platform/flux/) | GitOps configuration |
| [gitea](platform/gitea/) | Self-hosted Git + CI/CD |

#### Networking

| Component | Purpose |
|-----------|---------|
| [cilium](platform/cilium/) | CNI + Service Mesh (eBPF, mTLS) |
| [external-dns](platform/external-dns/) | DNS synchronization |
| [k8gb](platform/k8gb/) | Global Server Load Balancing |

#### Security

| Component | Purpose |
|-----------|---------|
| [cert-manager](platform/cert-manager/) | TLS certificate automation |
| [external-secrets](platform/external-secrets/) | Secrets management (ESO) |
| [openbao](platform/openbao/) | Secrets backend (MPL 2.0) |
| [trivy](platform/trivy/) | Security scanning |
| [falco](platform/falco/) | Runtime security (eBPF) |

#### Supply Chain Security

| Component | Purpose |
|-----------|---------|
| [sigstore](platform/sigstore/) | Container image signing (Sigstore/Cosign) |
| [syft-grype](platform/syft-grype/) | SBOM generation + vulnerability matching |

#### WAF

| Component | Purpose |
|-----------|---------|
| [coraza](platform/coraza/) | Web Application Firewall (OWASP CRS) |

#### Policy

| Component | Purpose |
|-----------|---------|
| [kyverno](platform/kyverno/) | Policy engine (validation, mutation, generation) |

#### Observability

| Component | Purpose |
|-----------|---------|
| [grafana](platform/grafana/) | LGTM stack (Loki, Tempo, Mimir) |
| [opensearch](platform/opensearch/) | Hot SIEM backend (security analytics) |

#### Scaling

| Component | Purpose |
|-----------|---------|
| [vpa](platform/vpa/) | Vertical Pod Autoscaler |
| [keda](platform/keda/) | Event-driven autoscaling |

#### Operations

| Component | Purpose |
|-----------|---------|
| [reloader](platform/reloader/) | Auto-restart on ConfigMap/Secret changes |

#### Storage & Registry

| Component | Purpose |
|-----------|---------|
| [minio](platform/minio/) | S3-compatible object storage |
| [velero](platform/velero/) | Kubernetes backup |
| [harbor](platform/harbor/) | Container registry |

#### Failover

| Component | Purpose |
|-----------|---------|
| [failover-controller](platform/failover-controller/) | Multi-region failover orchestration |

### A La Carte (Optional)

#### Data

| Component | Purpose |
|-----------|---------|
| [cnpg](platform/cnpg/) | PostgreSQL operator |
| [ferretdb](platform/ferretdb/) | MongoDB wire protocol on PostgreSQL |
| [valkey](platform/valkey/) | Redis-compatible cache |
| [strimzi](platform/strimzi/) | Apache Kafka streaming |
| [clickhouse](platform/clickhouse/) | Column-oriented analytics database |

#### CDC

| Component | Purpose |
|-----------|---------|
| [debezium](platform/debezium/) | Change data capture |

#### Workflow & Processing

| Component | Purpose |
|-----------|---------|
| [temporal](platform/temporal/) | Saga orchestration + compensation |
| [flink](platform/flink/) | Stream + batch processing |

#### Data Lakehouse

| Component | Purpose |
|-----------|---------|
| [iceberg](platform/iceberg/) | Open table format |

#### Identity

| Component | Purpose |
|-----------|---------|
| [keycloak](platform/keycloak/) | FAPI Authorization Server |

#### Monetization

| Component | Purpose |
|-----------|---------|
| [openmeter](platform/openmeter/) | Usage metering |

#### Communication

| Component | Purpose |
|-----------|---------|
| [stalwart](platform/stalwart/) | Self-hosted email server |
| [stunner](platform/stunner/) | K8s-native TURN/STUN (WebRTC) |
| [livekit](platform/livekit/) | Video/audio/data (WebRTC SFU) |
| [matrix](platform/matrix/) | Team chat (Matrix/Synapse) |

#### AI/ML

| Component | Purpose |
|-----------|---------|
| [knative](platform/knative/) | Serverless platform |
| [kserve](platform/kserve/) | Model serving |
| [vllm](platform/vllm/) | LLM inference engine |
| [milvus](platform/milvus/) | Vector database |
| [neo4j](platform/neo4j/) | Graph database |
| [librechat](platform/librechat/) | Chat UI |
| [bge](platform/bge/) | Embeddings + reranking |
| [llm-gateway](platform/llm-gateway/) | Subscription proxy for Claude Code |
| [anthropic-adapter](platform/anthropic-adapter/) | OpenAI-to-Anthropic translation |

#### AI Safety & Observability

| Component | Purpose |
|-----------|---------|
| [nemo-guardrails](platform/nemo-guardrails/) | AI safety firewall |
| [langfuse](platform/langfuse/) | LLM observability |

#### Chaos Engineering

| Component | Purpose |
|-----------|---------|
| [litmus](platform/litmus/) | Chaos engineering experiments |

---

## Products

Bundled vertical solutions that reference components from `platform/`:

### OpenOva Cortex (AI Hub)

Enterprise AI platform with LLM serving, RAG, AI safety, and LLM observability.

**Uses:** kserve, knative, vllm, milvus, neo4j, librechat, bge, llm-gateway, anthropic-adapter, nemo-guardrails, langfuse

See [products/cortex/](products/cortex/)

### OpenOva Fingate (Open Banking)

Fintech sandbox with PSD2/FAPI compliance.

**Uses:** keycloak, openmeter + 6 custom services

See [products/fingate/](products/fingate/)

### OpenOva Fabric (Data & Integration)

Event-driven data integration and lakehouse analytics.

**Uses:** strimzi, flink, temporal, debezium, iceberg, clickhouse, minio

See [products/fabric/](products/fabric/)

### OpenOva Relay (Communication)

Enterprise communication platform with email, video, chat, and WebRTC.

**Uses:** stalwart, livekit, stunner, matrix

See [products/relay/](products/relay/)

### OpenOva Axon (SaaS LLM Gateway)

Hosted inference gateway connecting to OpenOva Cortex.

See [products/axon/](products/axon/)

---

## Cloud Providers

| Provider | Status |
|----------|--------|
| Hetzner Cloud | Available |
| Huawei Cloud | Coming Soon |
| Oracle Cloud (OCI) | Coming Soon |

---

## Getting Started

```bash
# Managed Bootstrap (recommended)
# Visit https://bootstrap.openova.io

# Self-Hosted Bootstrap
docker run -p 8080:8080 ghcr.io/openova-io/bootstrap:latest
```

---

## Sync to Customer Gitea

This monorepo syncs to customer's multi-repo Gitea:

```
GitHub (monorepo)                    Customer Gitea (multi-repo)
─────────────────                    ──────────────────────────
openova/core/              ──sync──> openova-core/
openova/platform/cilium/   ──sync──> openova-cilium/
openova/platform/flux/     ──sync──> openova-flux/
```

---

*Enterprise Kubernetes, delivered with GitOps*
