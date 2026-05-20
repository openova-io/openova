# OpenOva Catalyst

**A self-sufficient Kubernetes-native platform. Published as signed OCI Blueprints. Deployable as your own Sovereign.**

Catalyst is the open-source platform built by [OpenOva](https://openova.io). It turns any Kubernetes cluster into a **Sovereign**: a self-contained control plane that hosts Organizations, Environments, and Applications via GitOps + Crossplane, with a unified UI/Git/API for users.

---

## Documentation

The canonical doc set is 10 top-level files plus subdirectories for ADRs, archive, ledger, lessons-learned, proposals, sub-runbooks, and session artifacts. Each top-level file has a single topic; no orphan satellite docs.

| Document | What it covers |
|---|---|
| [`docs/GLOSSARY.md`](docs/GLOSSARY.md) | Canonical terminology + banned terms — read first |
| [`docs/STATUS.md`](docs/STATUS.md) | What's built today vs design-only — read second |
| [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) | Catalyst architecture, naming, component inventory, PowerDNS deployment, multi-region DNS (lua-records), ClusterMesh ID registry |
| [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) | The 15 inviolable engineering principles + anti-pattern receipts |
| [`docs/DOD.md`](docs/DOD.md) | Definition of Done — 5 pillars + Phase 0/1/2 deterministic test + canonical FQDN patterns |
| [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) | Operator how-tos: Sovereign provisioning, Blueprint authoring, chart conventions, demo walks, failover recovery, troubleshooting matrix, doc-integrity audit cadence |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Identity (SPIFFE + Keycloak), secrets (OpenBao + ESO), secret-rotation procedures, multi-region OpenBao posture, threat model |
| [`docs/SRE.md`](docs/SRE.md) | Operating a Sovereign — SLOs, incident response, progressive delivery, observability, alertmanager |
| [`docs/BUSINESS-STRATEGY.md`](docs/BUSINESS-STRATEGY.md) | Product strategy + GTM + franchise model + voucher mechanism + product families map |
| [`docs/TECHNOLOGY-FORECAST-2027-2030.md`](docs/TECHNOLOGY-FORECAST-2027-2030.md) | Component forecast 2027–2030 |

**Subdirectories:**

| Directory | What it contains |
|---|---|
| [`docs/adr/`](docs/adr/) | Architecture Decision Records (immutable; one file per decision) |
| [`docs/archive/`](docs/archive/) | Superseded / historical / one-off docs (incl. validation-log, Catalyst-Zero provisioning plan, component-logos asset manifest, UI-regression-guards catalog) |
| [`docs/ledger/`](docs/ledger/) | Live verification ledger — TRUST.md + TRACKER.md, cron-refreshed |
| [`docs/lessons-learned/`](docs/lessons-learned/) | Per-incident retrospectives |
| [`docs/proposals/`](docs/proposals/) | Active doc proposals not yet ratified into an ADR |
| [`docs/runbooks/`](docs/runbooks/) | Sub-runbooks (incident playbooks split out by surface) |
| [`docs/sessions/`](docs/sessions/) | Date-stamped session artifacts (walks, retros, audit reports) |

> **Heads-up before reading further**: the architecture docs in this repo describe Catalyst's **target** state. Significant portions are not yet implemented — see [`docs/STATUS.md`](docs/STATUS.md) for what exists today vs what is design.

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

Each folder under `platform/` and `products/` is the source of one **Blueprint**, published from CI as a signed OCI artifact at `ghcr.io/openova-io/bp-<name>:<semver>` (the `bp-` prefix is added to the OCI artifact name; folder names stay short). Per-folder isolation is provided at the OCI artifact layer, not the Git repo layer — this is a **monorepo with per-Blueprint fan-out**, not a meta-repo of separate Git repositories. See [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) §2 for the folder layout contract.

> **Today**, the 12-component bootstrap kit (cilium, cert-manager, flux, crossplane, sealed-secrets, spire, nats-jetstream, openbao, keycloak, gitea, powerdns + the bp-catalyst-platform umbrella under `products/catalyst/`) ships with full `chart/` + `blueprint.yaml` per [`docs/STATUS.md`](docs/STATUS.md) §7, plus `products/axon/` and the `external-dns` leaf chart. The remaining 45 platform components and the `cortex / fabric / fingate / relay` product folders are **design-stage** — README only — until each lands its Blueprint manifest, chart, Compositions, and CI fan-out.

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
| **DNS** | PowerDNS authoritative per Sovereign zone + DNSSEC + lua-records (`ifurlup`, `pickclosest`); pool-domain-manager allocates pool subdomains and flips parent-zone NS via registrar adapters (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot) — see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §13 (PowerDNS deployment) + §14 (multi-region DNS) |
| **Backup** | Velero (to SeaweedFS, which routes the cold tier to cloud archival S3) |
| **Container registry** | Harbor |

For the full component list and trends see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) and [`docs/TECHNOLOGY-FORECAST-2027-2030.md`](docs/TECHNOLOGY-FORECAST-2027-2030.md).

---

## Cloud providers

| Provider | Status |
|---|---|
| Hetzner Cloud | Available (most-tested path) |
| AWS / GCP / Azure | Crossplane providers available; full path coming |
| Oracle Cloud (OCI) | Crossplane provider available; full path coming |
| Huawei Cloud | Crossplane provider available; full path coming |

All providers reach Catalyst via the same Crossplane abstraction; Sovereign provisioning details per provider are in [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) §8 (Bring up a Sovereign).

---

## Getting started

### Try it (managed)

Visit `marketplace.openova.io` to install Applications on the openova Sovereign without any infrastructure setup. SaaS journey for SMEs and evaluations.

### Run your own Sovereign

```
1. Provision via catalyst-provisioner.openova.io (managed bootstrap), OR
2. Self-host bp-catalyst-provisioner in your own infrastructure (air-gap path).

Then follow the procedure in docs/RUNBOOKS.md §8 (Bring up a Sovereign).
```

### Build a Blueprint

See [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md). A Blueprint is a folder under `platform/<name>/` (or `products/<name>/`) in this monorepo containing `blueprint.yaml` + manifests (Helm chart or Kustomize base) + (optional) Crossplane Compositions. CI signs each folder's contents and publishes to OCI as `ghcr.io/openova-io/bp-<name>:<semver>`. Catalyst's `blueprint-controller` picks it up automatically. Org-private Blueprints follow the same shape inside per-Sovereign Gitea repos.

---

## License

All Blueprints and the Catalyst control plane are open source. Each component carries its own upstream license (typically Apache 2.0, MPL 2.0, or BSD-3); see each component's `LICENSE` file.

OpenOva charges for support, managed operations, and expert services — never for access to code. See [`docs/BUSINESS-STRATEGY.md`](docs/BUSINESS-STRATEGY.md) §10.

---

## Contributing

PRs welcome. The contribution path for Blueprints (including Crossplane Compositions) is documented in [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) §13. Issues and discussions on GitHub.

---

*Cloud-native is the foundation. Catalyst is how you operate it.*
