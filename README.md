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
├── platform/                # All 41 component blueprints (flat)
├── meta-platforms/          # Bundled vertical solutions
│   ├── ai-hub/              # Enterprise AI platform
│   └── open-banking/        # PSD2/FAPI fintech sandbox (+ 6 services)
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
- **Bootstrap (Terraform)**: Initial cluster + core components
- **Lifecycle Manager (Crossplane)**: Day-2 operations + a la carte components

---

## Platform Components (41)

All components under `platform/` (flat structure):

### Mandatory (Core Platform)

#### Infrastructure & Provisioning

| Component | Purpose |
|-----------|---------|
| [terraform](platform/terraform/) | Infrastructure as Code (bootstrap) |
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
| [vault](platform/vault/) | Secrets backend |
| [trivy](platform/trivy/) | Security scanning |

#### Policy

| Component | Purpose |
|-----------|---------|
| [kyverno](platform/kyverno/) | Policy engine (validation, mutation, generation) |

#### Observability

| Component | Purpose |
|-----------|---------|
| [grafana](platform/grafana/) | LGTM stack (Loki, Tempo, Mimir) |

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
| [redpanda](platform/redpanda/) | Kafka-compatible streaming |

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
| [n8n](platform/n8n/) | Workflow automation |
| [searxng](platform/searxng/) | Privacy-respecting web search |
| [bge](platform/bge/) | Embeddings + reranking |
| [llm-gateway](platform/llm-gateway/) | Subscription proxy for Claude Code |
| [anthropic-adapter](platform/anthropic-adapter/) | OpenAI ↔ Anthropic translation |

---

## Meta-Platforms

Bundled vertical solutions that reference components from `platform/`:

### AI Hub

Enterprise AI platform with LLM serving, RAG, and intelligent agents.

**Uses:** kserve, knative, vllm, milvus, neo4j, langserve, librechat, n8n, searxng, bge, llm-gateway, anthropic-adapter

See [meta-platforms/ai-hub/](meta-platforms/ai-hub/)

### Open Banking

Fintech sandbox with PSD2/FAPI compliance.

**Uses:** keycloak, openmeter, lago + 6 custom services

See [meta-platforms/open-banking/](meta-platforms/open-banking/)

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
