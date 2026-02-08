# OpenOva

**Enterprise-grade support provider for open-source Kubernetes ecosystems.**

OpenOva provides a converged blueprint ecosystem with operational guarantees, enabling cloud-native transformation for enterprises.

---

## Documentation

| Document | Description |
|----------|-------------|
| [Platform Tech Stack](docs/PLATFORM-TECH-STACK.md) | Technology stack and architecture |
| [SRE Handbook](docs/SRE.md) | Site reliability practices |

---

## Repository Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager application
├── platform/                # Individual component blueprints
│   ├── networking/          # Cilium, k8gb, ExternalDNS, STUNner
│   ├── security/            # cert-manager, ESO, Vault, Kyverno, Trivy
│   ├── observability/       # Grafana Stack (Loki, Mimir, Tempo)
│   ├── storage/             # MinIO, Harbor, Velero
│   ├── scaling/             # KEDA, VPA
│   ├── failover/            # Failover Controller
│   ├── gitops/              # Flux, Gitea
│   ├── idp/                 # Backstage
│   ├── data/                # CNPG, MongoDB, Valkey, Redpanda
│   ├── communication/       # Stalwart
│   ├── iac/                 # Terraform, Crossplane
│   └── identity/            # Keycloak
├── meta-platforms/          # Bundled vertical solutions
│   ├── ai-hub/              # Enterprise AI platform
│   └── open-banking/        # PSD2/FAPI fintech sandbox
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

See [core/README.md](core/README.md) for Bootstrap and Lifecycle Manager architecture.

---

## Core Application

The [core/](core/) directory contains the Bootstrap wizard and Lifecycle Manager:

| Mode | Location | Purpose |
|------|----------|---------|
| **Bootstrap** | Outside cluster | Initial provisioning via Terraform |
| **Manager** | Inside cluster | Day-2 operations via Crossplane |

---

## Platform Components

### Mandatory (Always Installed)

| Category | Components |
|----------|------------|
| **Networking** | [Cilium](platform/networking/cilium/), [k8gb](platform/networking/k8gb/), [ExternalDNS](platform/networking/external-dns/) |
| **Security** | [cert-manager](platform/security/cert-manager/), [External Secrets](platform/security/external-secrets/), [Vault](platform/security/vault/), [Kyverno](platform/security/kyverno/) |
| **Observability** | [Grafana Stack](platform/observability/grafana/) (Alloy, Loki, Mimir, Tempo) |
| **Storage** | [MinIO](platform/storage/minio/), [Harbor](platform/storage/harbor/), [Velero](platform/storage/velero/) |
| **Scaling** | [KEDA](platform/scaling/keda/), [VPA](platform/scaling/vpa/) |
| **Failover** | [Failover Controller](platform/failover/failover-controller/) |
| **GitOps** | [Flux](platform/gitops/flux/), [Gitea](platform/gitops/gitea/) |
| **IDP** | [Backstage](platform/idp/backstage/) |
| **IaC** | [Terraform](platform/iac/terraform/) (bootstrap), [Crossplane](platform/iac/crossplane/) (day-2) |

### A La Carte (Optional)

| Category | Components |
|----------|------------|
| **Data** | [CNPG](platform/data/cnpg/), [MongoDB](platform/data/mongodb/), [Valkey](platform/data/valkey/), [Redpanda](platform/data/redpanda/) |
| **Communication** | [Stalwart](platform/communication/stalwart/), [STUNner](platform/networking/stunner/) |
| **Identity** | [Keycloak](platform/identity/keycloak/) |

---

## Meta-Platforms

Bundled vertical solutions that combine platform components with custom services:

### AI Hub

Enterprise AI platform with LLM serving, RAG, and intelligent agents.

| Component | Purpose |
|-----------|---------|
| [KServe](meta-platforms/ai-hub/components/kserve/) | Model serving |
| [vLLM](meta-platforms/ai-hub/components/vllm/) | LLM inference |
| [Milvus](meta-platforms/ai-hub/components/milvus/) | Vector database |
| [LangServe](meta-platforms/ai-hub/components/langserve/) | RAG service |
| [LibreChat](meta-platforms/ai-hub/components/librechat/) | Chat UI |

See [meta-platforms/ai-hub/README.md](meta-platforms/ai-hub/README.md)

### Open Banking

Fintech sandbox with PSD2/FAPI compliance.

| Component | Purpose |
|-----------|---------|
| Keycloak | FAPI Authorization Server |
| [OpenMeter](meta-platforms/open-banking/components/openmeter/) | Usage metering |
| [Lago](meta-platforms/open-banking/components/lago/) | Billing |

See [meta-platforms/open-banking/README.md](meta-platforms/open-banking/README.md)

---

## Cloud Providers

| Provider | Status |
|----------|--------|
| Hetzner Cloud | Available |
| Huawei Cloud | Coming Soon |
| Oracle Cloud (OCI) | Coming Soon |

---

## Getting Started

### Option 1: Managed Bootstrap (Recommended)

Visit [bootstrap.openova.io](https://bootstrap.openova.io) to provision your platform using the wizard UI.

### Option 2: Self-Hosted Bootstrap

```bash
# Run bootstrap locally
docker run -p 8080:8080 ghcr.io/openova-io/bootstrap:latest

# Access wizard at http://localhost:8080
```

See [core/README.md](core/README.md) for details.

---

## Sync to Customer Gitea

This monorepo syncs to customer's multi-repo Gitea:

```
GitHub (monorepo)                    Customer Gitea (multi-repo)
─────────────────                    ──────────────────────────
openova/core/              ──sync──> openova-core/
openova/platform/cilium/   ──sync──> openova-cilium/
openova/platform/flux/     ──sync──> openova-flux/
openova/meta-platforms/    ──sync──> openova-ai-hub/
```

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

---

*Enterprise Kubernetes, delivered with GitOps*
