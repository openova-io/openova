# OpenOva Platform

## Project Memory

**IMPORTANT**: Read `.github/.internal/project-memory.md` for full strategic context about OpenOva positioning, architecture decisions, and product strategy.

## Purpose

OpenOva is an **enterprise-grade support provider for open-source K8s ecosystems** - NOT just an IaC platform. We provide:
- Converged blueprint ecosystem with operational guarantees
- Transformation journey partnership for cloud-native adoption
- Day-2 operational excellence (upgrades, safety, SLAs)

## Monorepo Structure

```
openova/
├── core/                    # Bootstrap + Lifecycle Manager application
├── platform/                # Individual component blueprints
│   ├── networking/          # Cilium, k8gb, ExternalDNS, STUNner
│   ├── security/            # cert-manager, ESO, Vault, Trivy
│   ├── policy/              # Kyverno
│   ├── observability/       # Grafana Stack (Alloy, Loki, Mimir, Tempo)
│   ├── registry/            # Harbor
│   ├── storage/             # MinIO, Velero
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

## Core Application

The `core/` directory contains a single Go application with two deployment modes:

| Mode | Location | Purpose | IaC Tool |
|------|----------|---------|----------|
| **Bootstrap** | Outside cluster | Initial provisioning | Terraform |
| **Manager** | Inside cluster | Day-2 operations | Crossplane |

See [core/README.md](core/README.md) for detailed architecture.

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
