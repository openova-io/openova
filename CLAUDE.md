# OpenOva Platform

## Project Memory

**IMPORTANT**: Read `.github/.internal/project-memory.md` for full strategic context about OpenOva positioning, architecture decisions, and product strategy.

## Purpose

OpenOva is an **enterprise-grade support provider for open-source K8s ecosystems** - NOT just an IaC platform. We provide:
- Converged blueprint ecosystem with operational guarantees
- Transformation journey partnership for cloud-native adoption
- Day-2 operational excellence (upgrades, safety, SLAs)
- Both consultancy AND productized platform from day 1

## Product Family (Locked 2026-02-09)

| Product | Name | Description |
|---------|------|-------------|
| Core Platform | **OpenOva** | 55 components, turnkey K8s ecosystem |
| AI Hub | **OpenOva Cortex** | LLM serving, RAG, agents |
| LLM Gateway | **OpenOva Synapse** | SaaS inference gateway (neural link to Cortex) |
| Open Banking | **OpenOva Fingate** | PSD2/FAPI fintech sandbox |
| AIOps Agents | **OpenOva Specter** | AI-powered SOC/NOC, self-healing |
| Bootstrap + Lifecycle | **OpenOva Catalyst** | Bootstrap wizard + Day-2 lifecycle manager |
| Migration Program | **OpenOva Exodus** | Structured migration from proprietary to open source |
| Data Lakehouse | **OpenOva Titan** | Iceberg + Trino + Superset + Flink analytics |
| Microservices Integration | **OpenOva Fuse** | Temporal + Camel K + Dapr integration platform |

## Business Model

- Blueprints are **FREE and open source** (always)
- Revenue: per-vCPU-core platform support subscription (all software is free, only charge for support)
- Pricing: per-vCPU-core under management (ELA with true-up or PAYG)
- Target market: banks first (2 prospects), then regulated verticals, then broader

## Monorepo Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager application
├── platform/                # All 55 component blueprints (flat structure)
├── products/                # Bundled vertical solutions
│   ├── cortex/              # OpenOva Cortex - Enterprise AI Hub
│   ├── fingate/             # OpenOva Fingate - Open Banking (+ 6 services)
│   ├── titan/               # OpenOva Titan - Data Lakehouse
│   └── fuse/                # OpenOva Fuse - Microservices Integration
└── docs/                    # Platform documentation
```

## Core Application

The `core/` directory contains a single Go application with two deployment modes:

| Mode | Location | Purpose | IaC Tool |
|------|----------|---------|----------|
| **Bootstrap** | Outside cluster | Initial provisioning | OpenTofu |
| **Manager** | Inside cluster | Day-2 operations | Crossplane |

See [core/README.md](core/README.md) for detailed architecture.

## Platform Components (55)

All components are flat under `platform/`:

activemq, airflow, anthropic-adapter, backstage, bge, camel, cert-manager, cilium, clickhouse, cnpg, crossplane, dapr, debezium, external-dns, external-secrets, failover-controller, falco, flink, flux, gitea, grafana, harbor, iceberg, k8gb, keda, keycloak, knative, kserve, kyverno, lago, langserve, librechat, llm-gateway, milvus, minio, mongodb, neo4j, openbao, openmeter, opensearch, opentofu, rabbitmq, searxng, stalwart, strimzi, stunner, superset, temporal, trino, trivy, valkey, velero, vitess, vllm, vpa

## Products

Products bundle platform components with custom services for specific verticals:

- **cortex** (OpenOva Cortex - AI Hub): Uses kserve, knative, vllm, milvus, neo4j, langserve, librechat, airflow, searxng, bge, llm-gateway, anthropic-adapter
- **fingate** (OpenOva Fingate - Open Banking): Uses keycloak, openmeter, lago + 6 custom services (accounts-api, consents-api, ext-authz, payments-api, sandbox-data, tpp-management)
- **titan** (OpenOva Titan - Data Lakehouse): Uses iceberg, trino, superset, flink, airflow, clickhouse, debezium, strimzi, minio
- **fuse** (OpenOva Fuse - Microservices Integration): Uses temporal, camel, dapr, strimzi, rabbitmq, activemq

## Key Principles

- Bootstrap wizard EXITS after provisioning (must be safe to delete)
- Lifecycle Manager continues inside cluster for day-2 operations
- Backstage is for developers; Lifecycle Manager is for platform operators
- OpenOva stays in picture via blueprints, not runtime components
- Zero external dependencies for core (no CNPG, Valkey, Strimzi for itself)

## Documentation

- [Platform Tech Stack](docs/PLATFORM-TECH-STACK.md) - Technology stack
- [SRE Handbook](docs/SRE.md) - Site reliability practices
- [Core Application](core/README.md) - Bootstrap + Lifecycle Manager
- [Business Strategy](docs/BUSINESS-STRATEGY.md) - Product strategy and GTM

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
