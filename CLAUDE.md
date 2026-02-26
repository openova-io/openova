# OpenOva Platform

## Project Memory

**IMPORTANT**: Read `.claude/project-memory.md` for full strategic context about OpenOva positioning, architecture decisions, and product strategy.

## Purpose

OpenOva is an **enterprise-grade support provider for open-source K8s ecosystems** - NOT just an IaC platform. We provide:
- Converged blueprint ecosystem with operational guarantees
- Transformation journey partnership for cloud-native adoption
- Day-2 operational excellence (upgrades, safety, SLAs)
- Both consultancy AND productized platform from day 1

## Product Family (Locked 2026-02-26)

| Product | Name | Description |
|---------|------|-------------|
| Core Platform | **OpenOva** | 52 component turnkey K8s ecosystem |
| Bootstrap+Lifecycle+IDP | **OpenOva Catalyst** | Bootstrap wizard, Day-2 manager, IDP, Workflow Explorer |
| AI Hub | **OpenOva Cortex** | LLM serving, RAG, AI safety, LLM observability |
| SaaS LLM Gateway | **OpenOva Axon** | Hosted inference gateway (neural link to Cortex) |
| Open Banking | **OpenOva Fingate** | PSD2/FAPI fintech sandbox |
| AIOps SOC/NOC | **OpenOva Specter** | AI-powered SOAR, self-healing |
| Data & Integration | **OpenOva Fabric** | Event-driven integration + data lakehouse |
| Communication | **OpenOva Relay** | Email, video, chat, WebRTC |
| Migration | **OpenOva Exodus** | Structured migration from proprietary to open source |

## Business Model

- Blueprints are **FREE and open source** (always)
- Revenue: per-vCPU-core platform support subscription (all software is free, only charge for support)
- Pricing: per-vCPU-core under management (ELA with true-up or PAYG)
- Target market: banks first (2 prospects), then regulated verticals, then broader

## Monorepo Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager application
├── platform/                # All 52 component blueprints (flat structure)
├── products/                # Bundled vertical solutions
│   ├── cortex/              # OpenOva Cortex - Enterprise AI Hub
│   ├── fingate/             # OpenOva Fingate - Open Banking (+ 6 services)
│   ├── fabric/              # OpenOva Fabric - Data & Integration
│   ├── relay/               # OpenOva Relay - Communication
│   └── axon/                # OpenOva Axon - SaaS LLM Gateway
└── docs/                    # Platform documentation
```

## Core Application

The `core/` directory contains a single Go application with two deployment modes:

| Mode | Location | Purpose | IaC Tool |
|------|----------|---------|----------|
| **Bootstrap** | Outside cluster | Initial provisioning | OpenTofu |
| **Manager** | Inside cluster | Day-2 operations | Crossplane |

See [core/README.md](core/README.md) for detailed architecture.

## Platform Components (52)

All components are flat under `platform/`:

anthropic-adapter, bge, cert-manager, cilium, clickhouse, cnpg, coraza, crossplane, debezium, external-dns, external-secrets, failover-controller, falco, ferretdb, flink, flux, gitea, grafana, harbor, iceberg, k8gb, keda, keycloak, knative, kserve, kyverno, langfuse, librechat, litmus, livekit, llm-gateway, matrix, milvus, minio, nemo-guardrails, neo4j, openbao, openmeter, opensearch, opentofu, reloader, sigstore, stalwart, strimzi, stunner, syft-grype, temporal, trivy, valkey, velero, vllm, vpa

## Products

Products bundle platform components with custom services for specific verticals:

- **cortex** (OpenOva Cortex - AI Hub): Uses kserve, knative, vllm, milvus, neo4j, librechat, bge, llm-gateway, anthropic-adapter, nemo-guardrails, langfuse
- **fingate** (OpenOva Fingate - Open Banking): Uses keycloak, openmeter + 6 custom services (accounts-api, consents-api, ext-authz, payments-api, sandbox-data, tpp-management)
- **fabric** (OpenOva Fabric - Data & Integration): Uses strimzi, flink, temporal, debezium, iceberg, clickhouse, minio
- **relay** (OpenOva Relay - Communication): Uses stalwart, livekit, stunner, matrix
- **axon** (OpenOva Axon - SaaS LLM Gateway): SaaS service, references Cortex infrastructure

## Key Principles

- Bootstrap wizard EXITS after provisioning (must be safe to delete)
- Lifecycle Manager continues inside cluster for day-2 operations
- Catalyst IDP is for developers; Lifecycle Manager is for platform operators
- OpenOva stays in picture via blueprints, not runtime components
- Zero external dependencies for core (no CNPG, Valkey, Strimzi for itself)

## Documentation

- [Platform Tech Stack](docs/PLATFORM-TECH-STACK.md) - Technology stack
- [SRE Handbook](docs/SRE.md) - Site reliability practices
- [Core Application](core/README.md) - Bootstrap + Lifecycle Manager
- [Business Strategy](docs/BUSINESS-STRATEGY.md) - Product strategy and GTM
- [Technology Forecast](docs/TECHNOLOGY-FORECAST-2027-2030.md) - Component forecast

## Conventions

- All manifests are Kustomize-based
- Secrets via External-Secrets Operator (never commit plaintext)
- Git commits: conventional commits (feat:, fix:, docs:, infra:)

## Customer Sync

This monorepo syncs to customer's multi-repo Gitea:

```
GitHub (monorepo)                    Customer Gitea (multi-repo)
─────────────────                    ──────────────────────────
openova/core/              ──sync──> openova-core/
openova/platform/cilium/   ──sync──> openova-cilium/
openova/platform/flux/     ──sync──> openova-flux/
```
