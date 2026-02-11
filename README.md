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

---

## Repository Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager
├── platform/                # All 55 component blueprints (flat)
├── products/                # Bundled vertical solutions
│   ├── cortex/              # OpenOva Cortex - Enterprise AI Hub
│   ├── fingate/             # OpenOva Fingate - Open Banking (+ 6 services)
│   ├── titan/               # OpenOva Titan - Data Lakehouse
│   └── fuse/                # OpenOva Fuse - Microservices Integration
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
Bootstrap Wizard → Customer's K8s + Backstage + Flux + Gitea
                 → OpenOva Blueprints (stays in picture)
```

**Two-Phase Provisioning:**
- **Bootstrap (OpenTofu)**: Initial cluster + core components
- **Lifecycle Manager (Crossplane)**: Day-2 operations + a la carte components

---

## Platform Components (55)

All components under `platform/` (flat structure):

### Mandatory (Core Platform)

#### Infrastructure & Provisioning

| Component | Purpose |
|-----------|---------|
| [opentofu](platform/opentofu/) | Infrastructure as Code (bootstrap, MPL 2.0) |
| [crossplane](platform/crossplane/) | Day-2 cloud resource provisioning |

#### GitOps & IDP

| Component | Purpose |
|-----------|---------|
| [flux](platform/flux/) | GitOps configuration |
| [gitea](platform/gitea/) | Self-hosted Git + CI/CD |
| [backstage](platform/backstage/) | Internal Developer Platform |

#### Networking

| Component | Purpose |
|-----------|---------|
| [cilium](platform/cilium/) | CNI + Service Mesh (eBPF, mTLS) |
| [external-dns](platform/external-dns/) | DNS synchronization |
| [k8gb](platform/k8gb/) | Global Server Load Balancing |
| [stunner](platform/stunner/) | K8s-native TURN server |

#### Security

| Component | Purpose |
|-----------|---------|
| [cert-manager](platform/cert-manager/) | TLS certificate automation |
| [external-secrets](platform/external-secrets/) | Secrets management (ESO) |
| [openbao](platform/openbao/) | Secrets backend (MPL 2.0) |
| [trivy](platform/trivy/) | Security scanning |
| [falco](platform/falco/) | Runtime security (eBPF) |

#### Policy

| Component | Purpose |
|-----------|---------|
| [kyverno](platform/kyverno/) | Policy engine (validation, mutation, generation) |

#### Observability

| Component | Purpose |
|-----------|---------|
| [grafana](platform/grafana/) | LGTM stack (Loki, Tempo, Mimir) |
| [opensearch](platform/opensearch/) | Search and SIEM analytics |

#### Scaling

| Component | Purpose |
|-----------|---------|
| [vpa](platform/vpa/) | Vertical Pod Autoscaler |
| [keda](platform/keda/) | Event-driven autoscaling |

#### Storage

| Component | Purpose |
|-----------|---------|
| [minio](platform/minio/) | S3-compatible object storage |
| [velero](platform/velero/) | Kubernetes backup |

#### Registry

| Component | Purpose |
|-----------|---------|
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
| [mongodb](platform/mongodb/) | Document database |
| [valkey](platform/valkey/) | Redis-compatible cache |
| [strimzi](platform/strimzi/) | Apache Kafka streaming |
| [rabbitmq](platform/rabbitmq/) | Message broker (AMQP) |
| [activemq](platform/activemq/) | Message broker (JMS/AMQP) |
| [vitess](platform/vitess/) | MySQL-compatible horizontal scaling |
| [clickhouse](platform/clickhouse/) | Column-oriented analytics database |

#### CDC

| Component | Purpose |
|-----------|---------|
| [debezium](platform/debezium/) | Change data capture |

#### Workflow

| Component | Purpose |
|-----------|---------|
| [airflow](platform/airflow/) | Workflow orchestration (Apache 2.0) |
| [temporal](platform/temporal/) | Durable workflow execution |

#### Integration

| Component | Purpose |
|-----------|---------|
| [camel](platform/camel/) | Integration framework (Apache Camel K) |
| [dapr](platform/dapr/) | Distributed application runtime |

#### Data Lakehouse

| Component | Purpose |
|-----------|---------|
| [iceberg](platform/iceberg/) | Open table format |
| [trino](platform/trino/) | Distributed SQL query engine |
| [superset](platform/superset/) | Data visualization and BI |
| [flink](platform/flink/) | Stream processing |

#### Identity

| Component | Purpose |
|-----------|---------|
| [keycloak](platform/keycloak/) | FAPI Authorization Server |

#### Communication

| Component | Purpose |
|-----------|---------|
| [stalwart](platform/stalwart/) | Self-hosted email server |

#### Monetization

| Component | Purpose |
|-----------|---------|
| [openmeter](platform/openmeter/) | Usage metering |
| [lago](platform/lago/) | Billing and invoicing |

#### AI/ML

| Component | Purpose |
|-----------|---------|
| [knative](platform/knative/) | Serverless platform |
| [kserve](platform/kserve/) | Model serving |
| [vllm](platform/vllm/) | LLM inference engine |
| [milvus](platform/milvus/) | Vector database |
| [neo4j](platform/neo4j/) | Graph database |
| [langserve](platform/langserve/) | LangChain RAG service |
| [librechat](platform/librechat/) | Chat UI |
| [airflow](platform/airflow/) | Workflow orchestration |
| [searxng](platform/searxng/) | Privacy-respecting web search |
| [bge](platform/bge/) | Embeddings + reranking |
| [llm-gateway](platform/llm-gateway/) | Subscription proxy for Claude Code |
| [anthropic-adapter](platform/anthropic-adapter/) | OpenAI ↔ Anthropic translation |

---

## Products

Bundled vertical solutions that reference components from `platform/`:

### OpenOva Cortex (AI Hub)

Enterprise AI platform with LLM serving, RAG, and intelligent agents.

**Uses:** kserve, knative, vllm, milvus, neo4j, langserve, librechat, airflow, searxng, bge, llm-gateway, anthropic-adapter

See [products/cortex/](products/cortex/)

### OpenOva Fingate (Open Banking)

Fintech sandbox with PSD2/FAPI compliance.

**Uses:** keycloak, openmeter, lago + 6 custom services

See [products/fingate/](products/fingate/)

### OpenOva Titan (Data Lakehouse)

Analytics platform with open table formats and distributed SQL.

**Uses:** iceberg, trino, superset, flink, airflow, clickhouse, debezium, strimzi, minio

See [products/titan/](products/titan/)

### OpenOva Fuse (Microservices Integration)

Enterprise integration platform for microservices orchestration.

**Uses:** temporal, camel, dapr, strimzi, rabbitmq, activemq

See [products/fuse/](products/fuse/)

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
