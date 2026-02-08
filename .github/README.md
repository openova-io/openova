# OpenOva

**Enterprise-grade support provider for open-source Kubernetes ecosystems.**

OpenOva provides a converged blueprint ecosystem with operational guarantees, enabling cloud-native transformation for enterprises.

---

## Documentation

| Document | Description |
|----------|-------------|
| [Platform Tech Stack](../docs/PLATFORM-TECH-STACK.md) | Technology stack overview |
| [SRE Handbook](../docs/SRE.md) | Site reliability practices |
| [Core Application](../core/README.md) | Bootstrap + Lifecycle Manager |

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

---

## Repository Structure

This is a monorepo containing all OpenOva platform components:

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager
├── platform/                # Individual component blueprints
│   ├── networking/          # Cilium, k8gb, ExternalDNS, STUNner
│   ├── security/            # cert-manager, ESO, Vault, Kyverno
│   ├── observability/       # Grafana Stack
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

## Core Application

The [core/](../core/) directory contains:

| Mode | Purpose |
|------|---------|
| **Bootstrap** | Initial provisioning via Terraform (runs outside cluster) |
| **Lifecycle Manager** | Day-2 operations via Crossplane (runs inside cluster) |

---

## Platform Components

### Mandatory

| Category | Components |
|----------|------------|
| Networking | [Cilium](../platform/networking/cilium/), [k8gb](../platform/networking/k8gb/), [ExternalDNS](../platform/networking/external-dns/) |
| Security | [cert-manager](../platform/security/cert-manager/), [External Secrets](../platform/security/external-secrets/), [Vault](../platform/security/vault/), [Kyverno](../platform/security/kyverno/) |
| Observability | [Grafana Stack](../platform/observability/grafana/) |
| Storage | [MinIO](../platform/storage/minio/), [Harbor](../platform/storage/harbor/), [Velero](../platform/storage/velero/) |
| Scaling | [KEDA](../platform/scaling/keda/), [VPA](../platform/scaling/vpa/) |
| Failover | [Failover Controller](../platform/failover/failover-controller/) |
| GitOps | [Flux](../platform/gitops/flux/), [Gitea](../platform/gitops/gitea/) |
| IDP | [Backstage](../platform/idp/backstage/) |
| IaC | [Terraform](../platform/iac/terraform/), [Crossplane](../platform/iac/crossplane/) |

### A La Carte

| Category | Components |
|----------|------------|
| Data | [CNPG](../platform/data/cnpg/), [MongoDB](../platform/data/mongodb/), [Valkey](../platform/data/valkey/), [Redpanda](../platform/data/redpanda/) |
| Communication | [Stalwart](../platform/communication/stalwart/), [STUNner](../platform/networking/stunner/) |
| Identity | [Keycloak](../platform/identity/keycloak/) |

---

## Meta-Platforms

### AI Hub

Enterprise AI platform with LLM serving, RAG, and intelligent agents.

See [meta-platforms/ai-hub/](../meta-platforms/ai-hub/)

### Open Banking

Fintech sandbox with PSD2/FAPI compliance.

See [meta-platforms/open-banking/](../meta-platforms/open-banking/)

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

## Hosted Products

- [TalentMesh](https://github.com/talentmesh-io) - AI-powered talent assessment platform

---

*Enterprise Kubernetes, delivered with GitOps*
