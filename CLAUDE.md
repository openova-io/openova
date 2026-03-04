# OpenOva Platform (Public Repo)

This is the **public, open-source** repo. No proprietary content (website, deployment configs, infra secrets).
Website and deployment live in `openova-private`.

## Project Memory

**IMPORTANT**: Read `.claude/project-memory.md` for full strategic context about OpenOva positioning, architecture decisions, and product strategy.

## Purpose

OpenOva is an **AI-native infrastructure platform**. 52 open-source components on Kubernetes — every one designed to be AI-manageable. Cloud-native is the foundation. AI-native is the differentiator.

- **AI-powered operations built in** — Specter has pre-built semantic knowledge of every CRD, integration dependency, and failure mode across all 52 components
- **Converged blueprint ecosystem** with operational guarantees — turnkey, production-grade, instant
- **Comprehensive migration** — full legacy assessment, AI modernization roadmap, and structured migration (Exodus)
- Both consultancy AND productized platform from day 1

## Product Family (Locked 2026-02-26)

| Product | Name | Description |
|---------|------|-------------|
| Core Platform | **OpenOva** | 52 component AI-native K8s ecosystem — every component AI-manageable by Specter |
| Bootstrap+Lifecycle+IDP | **OpenOva Catalyst** | Bootstrap wizard, Day-2 manager, IDP, Workflow Explorer |
| AI Hub | **OpenOva Cortex** | LLM serving, RAG, AI safety, LLM observability |
| SaaS LLM Gateway | **OpenOva Axon** | Hosted inference gateway (neural link to Cortex) |
| Open Banking | **OpenOva Fingate** | PSD2/FAPI fintech sandbox |
| AIOps SOC/NOC | **OpenOva Specter** | AI brain with pre-built semantic knowledge of all 52 components. Token-efficient AI-powered SOAR and self-healing. |
| Data & Integration | **OpenOva Fabric** | Event-driven integration + data lakehouse |
| Communication | **OpenOva Relay** | Email, video, chat, WebRTC |
| Migration | **OpenOva Exodus** | Comprehensive legacy assessment + AI modernization roadmap + structured migration |

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
- **Every component is AI-manageable** — structured CRDs, unified OTel telemetry, standardized health endpoints, declarative GitOps
- **Specter is built-in, not bolted on** — pre-built semantic knowledge of the ecosystem is an architectural advantage, not a feature add-on

## AI-Native Architecture

What makes OpenOva AI-native (not AI-cosmetic):

| Property | What It Means | Why It Matters for AI |
|----------|--------------|----------------------|
| Structured CRDs | Every component exposes typed Kubernetes Custom Resource Definitions | Specter reads CRDs, not config files — machine-parseable by design |
| Unified OTel telemetry | All 52 components emit metrics, logs, traces via OpenTelemetry | Specter correlates signals across the entire stack in one query |
| Standardized health | Consistent liveness, readiness, startup probes | Specter knows the health vocabulary of every component |
| Kyverno policy-as-code | Security and operational policies are declarative and machine-readable | Specter reasons about compliance programmatically |
| Declarative GitOps | All state in Git via Flux | Specter diffs actual vs desired, detects drift, proposes reconciliation |
| Pre-built semantic models | Specter has knowledge of every CRD schema, integration graph, failure mode, upgrade path, compliance mapping | Surgical context = 10x fewer tokens, 10x faster, 10x more accurate than dumping raw logs |

**Token efficiency is the economic moat.** Competitors bolt AI onto unstructured platforms and dump massive context into LLM prompts. Specter sends surgical, structured context. This is an architectural advantage that cannot be retrofitted.

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
