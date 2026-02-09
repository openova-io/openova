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
| Core Platform | **OpenOva** | 41 components, turnkey K8s ecosystem |
| AI Hub | **OpenOva Cortex** | LLM serving, RAG, agents |
| LLM Gateway | **OpenOva Synapse** | SaaS inference gateway (neural link to Cortex) |
| Open Banking | **OpenOva Fingate** | PSD2/FAPI fintech sandbox |
| AIOps Agents | **OpenOva Specter** | AI-powered SOC/NOC, self-healing |

## Business Model

- Blueprints are **FREE and open source** (always)
- Revenue: support subscriptions, managed services, Specter (AIOps), expert network, consultancy
- Pricing: per-vCPU-core under management (ELA with true-up or PAYG)
- Target market: banks first (2 prospects), then regulated verticals, then broader

## Monorepo Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager application
├── platform/                # All 41 component blueprints (flat structure)
├── meta-platforms/          # Bundled vertical solutions
│   ├── ai-hub/              # Enterprise AI platform (README only)
│   └── open-banking/        # PSD2/FAPI fintech sandbox (+ 6 services)
└── docs/                    # Platform documentation
```

## Core Application

The `core/` directory contains a single Go application with two deployment modes:

| Mode | Location | Purpose | IaC Tool |
|------|----------|---------|----------|
| **Bootstrap** | Outside cluster | Initial provisioning | Terraform |
| **Manager** | Inside cluster | Day-2 operations | Crossplane |

See [core/README.md](core/README.md) for detailed architecture.

## Platform Components (41)

All components are flat under `platform/`:

anthropic-adapter, backstage, bge, cert-manager, cilium, cnpg, crossplane, external-dns, external-secrets, failover-controller, flux, gitea, grafana, harbor, k8gb, keda, keycloak, knative, kserve, kyverno, lago, langserve, librechat, llm-gateway, milvus, minio, mongodb, n8n, neo4j, openmeter, redpanda, searxng, stalwart, stunner, terraform, trivy, valkey, vault, velero, vllm, vpa

## Meta-Platforms

Meta-platforms reference components from `platform/`:

- **ai-hub**: Uses kserve, knative, vllm, milvus, neo4j, langserve, librechat, n8n, searxng, bge, llm-gateway, anthropic-adapter
- **open-banking**: Uses keycloak, openmeter, lago + 6 custom services (accounts-api, consents-api, ext-authz, payments-api, sandbox-data, tpp-management)

## Key Principles

- Bootstrap wizard EXITS after provisioning (must be safe to delete)
- Lifecycle Manager continues inside cluster for day-2 operations
- Backstage is for developers; Lifecycle Manager is for platform operators
- OpenOva stays in picture via blueprints, not runtime components
- Zero external dependencies for core (no CNPG, Valkey, Redpanda for itself)

## Documentation

- [Platform Tech Stack](docs/PLATFORM-TECH-STACK.md) - Technology stack
- [SRE Handbook](docs/SRE.md) - Site reliability practices
- [Core Application](core/README.md) - Bootstrap + Lifecycle Manager

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
