# OpenOva Catalyst

**A self-sufficient Kubernetes-native platform. Published as signed OCI Blueprints. Deployable as your own Sovereign.**

Catalyst is the open-source platform built by [OpenOva](https://openova.io). It turns any Kubernetes cluster into a **Sovereign**: a self-contained control plane that hosts Organizations, Environments, and Applications via GitOps + Crossplane, with a unified UI/Git/API for users.

---

## Documentation

| Document | What it covers |
|---|---|
| [`docs/GLOSSARY.md`](docs/GLOSSARY.md) | Canonical terminology — read first |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Catalyst architecture overview |
| [`docs/IMPLEMENTATION-STATUS.md`](docs/IMPLEMENTATION-STATUS.md) | **What's built today vs what's design-only** — read second |
| [`docs/NAMING-CONVENTION.md`](docs/NAMING-CONVENTION.md) | Naming patterns for every resource type |
| [`docs/PERSONAS-AND-JOURNEYS.md`](docs/PERSONAS-AND-JOURNEYS.md) | Personas × journeys matrix; surfaces |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Identity (SPIFFE + Keycloak), secrets (OpenBao + ESO), rotation, multi-region semantics |
| [`docs/SOVEREIGN-PROVISIONING.md`](docs/SOVEREIGN-PROVISIONING.md) | How to bring a Sovereign online |
| [`docs/BLUEPRINT-AUTHORING.md`](docs/BLUEPRINT-AUTHORING.md) | Writing Blueprints (incl. Crossplane Compositions) |
| [`docs/PLATFORM-TECH-STACK.md`](docs/PLATFORM-TECH-STACK.md) | Every component's role in Catalyst |
| [`docs/SRE.md`](docs/SRE.md) | Operating a Sovereign |
| [`docs/BUSINESS-STRATEGY.md`](docs/BUSINESS-STRATEGY.md) | Product strategy and GTM |
| [`docs/TECHNOLOGY-FORECAST-2027-2030.md`](docs/TECHNOLOGY-FORECAST-2027-2030.md) | Component forecast 2027–2030 |
| [`docs/VALIDATION-LOG.md`](docs/VALIDATION-LOG.md) | Trail of doc-integrity validation passes (audit log) |

> **Heads-up before reading further**: the architecture docs in this repo describe Catalyst's **target** state. Significant portions are not yet implemented — see [`docs/IMPLEMENTATION-STATUS.md`](docs/IMPLEMENTATION-STATUS.md) for what exists today vs what is design.

---

## The model in 60 seconds

```
OpenOva (the company) publishes Catalyst (the platform).
A deployed Catalyst is called a Sovereign.

A Sovereign has:
  - Organizations (multi-tenancy unit)
  - Environments (org-scoped, env-typed: prod/stg/uat/dev/poc)
  - Applications (installed Blueprints)
  - Blueprints (the App Store catalog — public + Org-private)

Users install Applications from Blueprints into Environments.
Blueprints can depend on Blueprints (arbitrary depth).
Each Environment is one Gitea repo + one or more vclusters.
Every state change is a Git commit.
Every UI surface reads from a single CQRS projection.

Same code runs in every Sovereign:
  - openova         (run by us; SaaS Organizations)
  - omantel         (run by Omantel; SME Organizations across Oman)
  - bankdhofar      (run by the bank; internal Organizations)
  - your-company    (run by you, on infrastructure you choose)
```

See [`docs/GLOSSARY.md`](docs/GLOSSARY.md) for every term, [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) for the full picture.

---

## What's in this repo

```
openova/
├── core/              # Catalyst control-plane application (Go) — design-stage; mostly placeholders today
├── platform/          # Component Blueprint folders (one folder per upstream OSS project)
├── products/          # Composite Blueprint folders OpenOva publishes
│   ├── catalyst/      # The Catalyst control plane itself, target umbrella Blueprint
│   ├── cortex/        # AI Hub (LLM serving, RAG, AI safety)
│   ├── axon/          # SaaS LLM Gateway (default upstream for Cortex)
│   ├── fingate/       # Open Banking (PSD2/FAPI sandbox)
│   ├── fabric/        # Data & Integration (event-driven + lakehouse)
│   └── relay/         # Communication (email, video, chat, WebRTC)
│                      # (specter and exodus are deliverable services, not Blueprints in this layout)
└── docs/              # Platform documentation
```

Each folder under `platform/` and `products/` is the source of one **Blueprint**, published from CI as a signed OCI artifact at `ghcr.io/openova-io/bp-<name>:<semver>` (the `bp-` prefix is added to the OCI artifact name; folder names stay short). Per-folder isolation is provided at the OCI artifact layer, not the Git repo layer — this is a **monorepo with per-Blueprint fan-out**, not a meta-repo of separate Git repositories. See [`docs/BLUEPRINT-AUTHORING.md`](docs/BLUEPRINT-AUTHORING.md) §2 for the folder layout contract.

> **Today**, every folder under `platform/` and `products/` (except `products/axon/`) contains only a `README.md`. The Blueprint manifests, charts, Compositions, and CI fan-out are all **design-stage** — see [`docs/IMPLEMENTATION-STATUS.md`](docs/IMPLEMENTATION-STATUS.md).

---

## Stack at a glance

| Layer | Technology |
|---|---|
| **Container runtime** | k3s (k8s-conformant), containerd |
| **CNI / Service Mesh** | Cilium (eBPF mTLS, L7 policies, Gateway API) |
| **GitOps** | Flux (per-vcluster, lightweight) |
| **Git** | Gitea (per-Sovereign, hosts Blueprint mirror + per-Environment repos) |
| **IaC for non-K8s** | Crossplane (the only IaC; not user-facing) |
| **Bootstrap IaC** | OpenTofu (one-shot, archived after Phase 0) |
| **Multi-tenancy** | vcluster (one per Organization per host cluster) |
| **Identity (workloads)** | SPIFFE/SPIRE (5-min rotating SVIDs, mTLS everywhere) |
| **Identity (users)** | Keycloak (per-Org for SME, per-Sovereign for corporate) |
| **Secrets** | OpenBao (Apache 2.0; independent Raft per region, no stretched cluster) + External Secrets Operator |
| **Event spine** | NATS JetStream (Apache 2.0; pub/sub + KV; per-Org accounts) |
| **TLS** | cert-manager + Let's Encrypt or corporate CA |
| **Policy** | Kyverno |
| **Supply chain** | cosign (Sigstore), Syft + Grype SBOM, Trivy scans |
| **Runtime security** | Falco (eBPF) |
| **Observability** | OpenTelemetry → Grafana stack (Alloy + Loki + Mimir + Tempo) |
| **WAF** | Coraza (OWASP CRS) |
| **GSLB** | k8gb (authoritative DNS) |
| **Backup** | Velero (to SeaweedFS, which routes the cold tier to cloud archival S3) |
| **Container registry** | Harbor |

For the full component list and trends see [`docs/PLATFORM-TECH-STACK.md`](docs/PLATFORM-TECH-STACK.md) and [`docs/TECHNOLOGY-FORECAST-2027-2030.md`](docs/TECHNOLOGY-FORECAST-2027-2030.md).

---

## Cloud providers

| Provider | Status |
|---|---|
| Hetzner Cloud | Available (most-tested path) |
| AWS / GCP / Azure | Crossplane providers available; full path coming |
| Oracle Cloud (OCI) | Crossplane provider available; full path coming |
| Huawei Cloud | Crossplane provider available; full path coming |

All providers reach Catalyst via the same Crossplane abstraction; Sovereign provisioning details per provider are in [`docs/SOVEREIGN-PROVISIONING.md`](docs/SOVEREIGN-PROVISIONING.md).

---

## Getting started

### Try it (managed)

Visit `marketplace.openova.io` to install Applications on the openova Sovereign without any infrastructure setup. SaaS journey for SMEs and evaluations.

### Run your own Sovereign

```
1. Provision via catalyst-provisioner.openova.io (managed bootstrap), OR
2. Self-host bp-catalyst-provisioner in your own infrastructure (air-gap path).

Then follow the procedure in docs/SOVEREIGN-PROVISIONING.md.
```

### Build a Blueprint

See [`docs/BLUEPRINT-AUTHORING.md`](docs/BLUEPRINT-AUTHORING.md). A Blueprint is a folder under `platform/<name>/` (or `products/<name>/`) in this monorepo containing `blueprint.yaml` + manifests (Helm chart or Kustomize base) + (optional) Crossplane Compositions. CI signs each folder's contents and publishes to OCI as `ghcr.io/openova-io/bp-<name>:<semver>`. Catalyst's `blueprint-controller` picks it up automatically. Org-private Blueprints follow the same shape inside per-Sovereign Gitea repos.

---

## License

All Blueprints and the Catalyst control plane are open source. Each component carries its own upstream license (typically Apache 2.0, MPL 2.0, or BSD-3); see each component's `LICENSE` file.

OpenOva charges for support, managed operations, and expert services — never for access to code. See [`docs/BUSINESS-STRATEGY.md`](docs/BUSINESS-STRATEGY.md) §10.

---

## Contributing

PRs welcome. The contribution path for Blueprints (including Crossplane Compositions) is documented in [`docs/BLUEPRINT-AUTHORING.md`](docs/BLUEPRINT-AUTHORING.md) §13. Issues and discussions on GitHub.

---

*Cloud-native is the foundation. Catalyst is how you operate it.*
