# OpenOva Catalyst

**A self-sufficient Kubernetes-native platform. Published as signed OCI Blueprints. Deployable as your own Sovereign.**

Catalyst is the open-source platform built by [OpenOva](https://openova.io). It turns any Kubernetes cluster into a **Sovereign**: a self-contained control plane that hosts Organizations, Environments, and Applications via GitOps + Crossplane, with a unified UI/Git/API for users.

---

## Documentation

The `docs/` tree, in reading order. Every file under `docs/` appears exactly once below.

### Core (read in this order)

- [`docs/GLOSSARY.md`](docs/GLOSSARY.md) — canonical terminology + banned terms; wins over every other doc
- [`docs/STATUS.md`](docs/STATUS.md) — what's built today vs what's design-only
- [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — target architecture, tech stack, naming, repo layout
- [`docs/PRINCIPLES.md`](docs/PRINCIPLES.md) — inviolable engineering rules + anti-pattern catalog
- [`docs/DOD.md`](docs/DOD.md) — 5-pillar Definition of Done + D0-D35 gates + domains canon

### Build and operate

- [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) — provisioning, chart bumps, Blueprint authoring, failover recovery, doc-integrity audit cadence, Sovereign bring-up, UI regression catalog, Catalyst-Zero waterfall
- [`docs/SRE.md`](docs/SRE.md) — operate a Sovereign in production (SLOs, incident response, GPU ops)
- [`docs/SECURITY.md`](docs/SECURITY.md) — identity (Cilium WG + Keycloak), secrets (OpenBao + ESO), threat model
- [`docs/SECRET-ROTATION.md`](docs/SECRET-ROTATION.md) — credential inventory, rotation schedule, rollback path

### Strategy

- [`docs/BUSINESS-STRATEGY.md`](docs/BUSINESS-STRATEGY.md) — positioning, revenue model, competitive landscape, GTM (incl. §5.5 product families map and §10.8 franchise model)
- [`docs/ROADMAP.md`](docs/ROADMAP.md) — technology roadmap 2027–2030: 56-component relevance forecast, components to watch, risks

### Brand kit ([`brand/`](brand/))

- [`brand/README.md`](brand/README.md) — public CC-BY-SA-4.0 brand assets: OO mark + horizontal lockup + stacked lockup. Drop-in SVGs for third-party "powered by OpenOva" placements (chepherd v0.5 + future partner Blueprints).

### Deep-dive (component / surface level)

- The Catalyst-Zero phase-by-phase execution plan, UI regression catalog, Sovereign provisioning walkthrough, and doc-integrity audit cadence now live in [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) §8–§11.
- Multi-region DNS (PowerDNS lua-records), PowerDNS deployment shape, ClusterMesh cluster.id registry, and the component-logo manifest now live in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §8.7–§8.9 and §6.7.

### Decision records ([`docs/adr/`](docs/adr/))

- [`docs/adr/README.md`](docs/adr/README.md) — ADR index + Accepted / Superseded states
- [`docs/adr/0001-catalyst-control-plane-architecture.md`](docs/adr/0001-catalyst-control-plane-architecture.md) — Catalyst control-plane architecture (Accepted)
- [`docs/adr/0002-post-handover-sovereignty-cutover.md`](docs/adr/0002-post-handover-sovereignty-cutover.md) — post-handover Sovereign cutover (Accepted)
- [`docs/adr/0003-rbac-newapi-user-create-hook.md`](docs/adr/0003-rbac-newapi-user-create-hook.md) — RBAC ↔ NewAPI user-create hook contract (Accepted)
- [`docs/adr/0004-cnpg-sync-replication.md`](docs/adr/0004-cnpg-sync-replication.md) — CNPG Pillar-3 synchronous replication (Accepted)

### Live state ([`docs/ledger/`](docs/ledger/) — cron-refreshed)

- [`docs/ledger/TRACKER.md`](docs/ledger/TRACKER.md) — open-issue + DoD-gate progress board (15-min refresh)
- [`docs/ledger/TRUST.md`](docs/ledger/TRUST.md) — verification ledger (UNVERIFIED / VERIFIED-PASS / FAIL / PARTIAL)

### Lessons learned ([`docs/lessons-learned/`](docs/lessons-learned/))

- [`docs/lessons-learned/README.md`](docs/lessons-learned/README.md) — index + contribution rules
- [`docs/lessons-learned/catalyst-bootstrap-api.md`](docs/lessons-learned/catalyst-bootstrap-api.md) — Phase-0 `tofu destroy` + token-hygiene behavior
- [`docs/lessons-learned/chi-router-quirks.md`](docs/lessons-learned/chi-router-quirks.md) — go-chi percent-encoding + route-match traps
- [`docs/lessons-learned/helm-controller-logs.md`](docs/lessons-learned/helm-controller-logs.md) — Flux v2.4 nested-JSON log shape for HelmRelease
- [`docs/lessons-learned/helm-controller-rbac.md`](docs/lessons-learned/helm-controller-rbac.md) — helm-controller SA needs cluster-admin in `flux-system`
- [`docs/lessons-learned/helm-hooks-and-crd-ordering.md`](docs/lessons-learned/helm-hooks-and-crd-ordering.md) — `before-hook-creation` deadlocks on subchart-registered CRDs

### Operational runbooks ([`docs/runbooks/`](docs/runbooks/))

- [`docs/runbooks/openova-flow-multi-region-verify.md`](docs/runbooks/openova-flow-multi-region-verify.md) — OpenovaFlow multi-region rendering verification

### Proposals ([`docs/proposals/`](docs/proposals/) — in-flight)

- [`docs/proposals/jobs-dependencies-viz.md`](docs/proposals/jobs-dependencies-viz.md) — Jobs Dependencies tab SVG-DAG visualization

### Session archives ([`docs/sessions/`](docs/sessions/))

- [`docs/sessions/2026-05-17-convergence.md`](docs/sessions/2026-05-17-convergence.md) — convergence wave + Sandbox scaffold session report
- [`docs/sessions/2026-05-19-20-trust-recovery.md`](docs/sessions/2026-05-19-20-trust-recovery.md) — trust-recovery cycle whole-day retrospective
- [`docs/sessions/2026-05-20-trust-audit.md`](docs/sessions/2026-05-20-trust-audit.md) — random-sample evidence audit of closed issues
- [`docs/sessions/2026-05-20-walk-runbook.md`](docs/sessions/2026-05-20-walk-runbook.md) — fresh-prov walk runbook for 42 unverified PRs

### Archive ([`docs/archive/`](docs/archive/) — superseded, kept for audit trail)

- [`docs/archive/omantel-handover-wbs.md`](docs/archive/omantel-handover-wbs.md) — Omantel handover work-breakdown structure
- [`docs/archive/orchestrator-state.md`](docs/archive/orchestrator-state.md) — Catalyst-Zero multi-agent orchestrator hand-off state
- [`docs/archive/validation-log.md`](docs/archive/validation-log.md) — trail of past documentation-integrity validation passes

> **Heads-up before reading further**: the architecture docs in this repo describe Catalyst's **target** state. Significant portions are not yet implemented — `docs/STATUS.md` (listed above) records what exists today vs what is design.

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
| **DNS** | PowerDNS authoritative per Sovereign zone + DNSSEC + lua-records (`ifurlup`, `pickclosest`); pool-domain-manager allocates pool subdomains and flips parent-zone NS via registrar adapters (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot) — see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) §8.8 (lua-records) + §8.9 (PowerDNS deployment shape) |
| **Backup** | Velero (to SeaweedFS, which routes the cold tier to cloud archival S3) |
| **Container registry** | Harbor |

For the full component list and trends see [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) and [`docs/ROADMAP.md`](docs/ROADMAP.md).

---

## Cloud providers

| Provider | Status |
|---|---|
| Hetzner Cloud | Available (most-tested path) |
| AWS / GCP / Azure | Crossplane providers available; full path coming |
| Oracle Cloud (OCI) | Crossplane provider available; full path coming |
| Huawei Cloud | Crossplane provider available; full path coming |

All providers reach Catalyst via the same Crossplane abstraction; Sovereign provisioning details per provider are in [`docs/RUNBOOKS.md`](docs/RUNBOOKS.md) §9 (Bring up a Sovereign).

---

## Getting started

### Try it (managed)

Visit `marketplace.openova.io` to install Applications on the openova Sovereign without any infrastructure setup. SaaS journey for SMEs and evaluations.

### Run your own Sovereign

```
1. Provision via catalyst-provisioner.openova.io (managed bootstrap), OR
2. Self-host bp-catalyst-provisioner in your own infrastructure (air-gap path).

Then follow the procedure in docs/RUNBOOKS.md §9 (Bring up a Sovereign).
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
