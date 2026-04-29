# Catalyst Implementation Status

**Status:** Authoritative. Living document. **Updated:** 2026-04-29

This document is the **bridge** between the target architecture (described in [`ARCHITECTURE.md`](ARCHITECTURE.md), [`SECURITY.md`](SECURITY.md), [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md), etc.) and the current state of the code in this repository.

The other architecture docs describe the **target**: where Catalyst is going. This document records **what exists today** and **what is design-only**. When in doubt, read this file before making any claim about Catalyst's capabilities.

> If you find a claim elsewhere in this repo that contradicts this file, this file wins until either (a) the code catches up to the claim or (b) the claim is corrected.

---

## Status legend

| Status | Meaning |
|---|---|
| ✅ **Implemented** | Code exists, tested, deployable. |
| 🚧 **Partial** | Some code exists; significant gaps; not production-ready. |
| 📐 **Design** | Documented in canonical docs; no code yet. The doc is the contract for the future implementation. |
| ⏸ **Deferred** | Mentioned in docs but explicitly out of scope until later. |

---

## 1. Repository structure

| Item | Status | Notes |
|---|---|---|
| Public repo at `github.com/openova-io/openova` (this repo) | ✅ | Monorepo. Source of truth for documentation and (eventually) for every Blueprint's manifests. |
| Per-folder Blueprint convention (`platform/<name>/` and `products/<name>/`) | 🚧 | Folders exist with READMEs only. None yet contain a `blueprint.yaml`, `chart/`, or CI pipeline. |
| `bp-<name>:<semver>` OCI artifacts in `ghcr.io/openova-io/` | 📐 | Target: every Blueprint folder fans out to a signed OCI artifact via CI. Not yet wired. |
| `core/{console,admin,marketplace,marketplace-api}/` | 🚧 | **Consolidated 2026-04-28 (Pass 105)** from `openova-private/apps/{console,admin,marketplace}/` and `openova-private/website/marketplace-api/`. Astro+Svelte UIs (console, admin, marketplace) plus Go backend (marketplace-api). All deployed today on Catalyst-Zero (Contabo k3s, namespaces `sme` + `marketplace`). |
| `products/axon/` | ✅ | Real implementation (chart/, src/, scripts/). |
| `products/catalyst/` umbrella Blueprint (`bp-catalyst-platform`) | 🚧 | **Has `bootstrap/{ui,api}/` source code** (React SPA wizard + Go bootstrap API, deployed on Catalyst-Zero in `catalyst` namespace). **Has `chart/` with Chart.yaml + Helm templates for the full Catalyst-Zero deployment** (catalyst-ui, catalyst-api, console, admin, marketplace, marketplace-api, plus the legacy `sme-services/` backend services). Per `docs/PROVISIONING-PLAN.md`, this is the canonical Helm chart for Catalyst-Zero and every franchised Sovereign. |
| `products/{cortex,fabric,fingate,relay,specter}/` | 📐 | README only. No charts or manifests. |

---

## 2. Catalyst control plane components (per [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §2)

These run **per-Sovereign** on the management cluster:

### 2.1 User-facing surfaces and backend services

| Component | Status | Notes |
|---|---|---|
| console (Catalyst UI) | 🚧 | Astro + Svelte UI at `core/console/`. Deployed on Catalyst-Zero (Contabo, namespace `sme`). Sovereign-provisioning wizard at `/sovereign` not yet built (Phase 3 of `docs/PROVISIONING-PLAN.md`). |
| marketplace (public Blueprint card grid) | 🚧 | Astro + Svelte UI at `core/marketplace/`. Deployed on Catalyst-Zero. 5-step `Plan→Apps→Addons→Checkout→Review` flow exists; `AppsStep` to be replaced with unified `bp-<x>` marketplace card grid (Phase 3). |
| admin (sovereign-admin operations UI) | 🚧 | Astro + Svelte UI at `core/admin/`. Deployed on Catalyst-Zero. Includes existing voucher / billing / catalog / orders / tenants admin surface (the canonical voucher implementation per `docs/PROVISIONING-PLAN.md`). |
| catalyst-ui | 🚧 | React SPA wizard scaffold at `products/catalyst/bootstrap/ui/`. Deployed on Catalyst-Zero (namespace `catalyst`). 7-step wizard, canonical order from `STEPS` in [`src/pages/wizard/WizardPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/wizard/WizardPage.tsx): **Org → Topology → Provider → Credentials → Components → Domain → Review** (the operator picks the platform first — sizing, provider, creds, components — and only then names the Sovereign in DNS). Per #176 Topology drives both the SKU pickers (via `PROVIDER_NODE_SIZES[provider]`) and worker count; per #d3346441/#b0ec0c43 Components is a single flat marketplace card grid with family chips + product detail / family portfolio routes. Merges into `core/console/src/pages/sovereign/` per Phase 3. |
| catalyst-api | 🚧 | Go bootstrap API at `products/catalyst/bootstrap/api/`. Deployed on Catalyst-Zero. `internal/hetzner/` already has Hetzner Cloud API client groundwork. Migrates into `core/marketplace-api/provisioner/` per Phase 4. |
| marketplace-api | 🚧 | Go backend at `core/marketplace-api/`. Deployed on Catalyst-Zero (namespace `marketplace`). Has `provisioner/` and `store/` modules — extends to full Hetzner Sovereign provisioning per Phase 4. |
| catalog-svc | 📐 | Designed. No code. |
| projector (CQRS read-side, JetStream → KV → SSE) | 📐 | Designed. No code. |
| provisioning service | 🚧 | Provisioning logic exists in `core/marketplace-api/provisioner/` (consolidated 2026-04-28). Extends per Phase 4. |
| environment-controller | 📐 | Designed. No code. |
| blueprint-controller | 📐 | Designed. No code. |
| billing | 📐 | Designed. No code. |

### 2.2 Per-Sovereign supporting services

| Component | Status | Notes |
|---|---|---|
| Gitea (per Sovereign) | 🚧 | Component README exists; no Catalyst-specific deployment manifest. |
| NATS JetStream (per Sovereign) | 📐 | Selected as event spine; no Catalyst-specific deployment manifest. |
| OpenBao (per region, independent Raft) | 🚧 | Component README exists with the agreed multi-region semantics; deployment manifests not in this repo. |
| Keycloak (per-Org SME / per-Sovereign corporate) | 🚧 | Component README exists; topology choice is a Catalyst-level concern not yet wired. |
| SPIRE server + agent | 📐 | Selected for workload identity; no integration code. |
| Catalyst observability (Grafana stack) | 🚧 | Per-component READMEs exist; not yet wired as a Catalyst-level umbrella. |

## 3. Per-host-cluster infrastructure (per [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §3)

These run on **every host cluster** (mgt, rtz, dmz). Status is per-component README only — none yet ship as deployable Blueprints.

| Component | Status | Notes |
|---|---|---|
| Cilium | 🚧 | README only. |
| External-DNS | 🚧 | README only. |
| PowerDNS | ✅ | bp-powerdns:1.0.6 deployed on contabo-mkt (#167; gpgsql-dnssec=yes) — authoritative DNS for every Sovereign zone (pool + BYO), CNPG-backed Postgres at `pdns-pg`, dnsdist front-end. Replaces the historical k8gb GSLB role via lua-records. See [`PLATFORM-POWERDNS.md`](PLATFORM-POWERDNS.md) and [`MULTI-REGION-DNS.md`](MULTI-REGION-DNS.md). |
| pool-domain-manager (PDM) | ✅ | Deployed at `pool-domain-manager` in `openova-system` (#163, #168, #170). CNPG-backed `pdm-pg`. Allocates pool subdomains under `omani.works`/`openova.io`, owns the per-Sovereign PowerDNS zone lifecycle, and exposes registrar adapters (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot) for BYO Flow B (registrar-API NS-flip). REST API: `/v1/reserve`, `/v1/commit`, `/v1/validate`, `/v1/registrars`. Source: [`core/pool-domain-manager/`](../core/pool-domain-manager/). |
| Coraza | 🚧 | README only. |
| Flux | 🚧 | README only. Per-vcluster Flux is a Catalyst-managed convention not yet implemented. |
| Crossplane | 🚧 | README only. |
| OpenTofu (bootstrap IaC) | 🚧 | README only. |
| cert-manager | 🚧 | README only. |
| External Secrets Operator | 🚧 | README only. |
| Kyverno | 🚧 | README only. |
| Trivy | 🚧 | README only. |
| Falco | 🚧 | README only. |
| Sigstore | 🚧 | README only. |
| Syft + Grype | 🚧 | README only. |
| VPA, KEDA, Reloader | 🚧 | READMEs only. |
| SeaweedFS, Velero, Harbor | 🚧 | READMEs only. |
| failover-controller | 🚧 | README only. |

---

## 4. CRDs

[`core/README.md`](../core/README.md) and [`ARCHITECTURE.md`](ARCHITECTURE.md) reference these CRDs:

| CRD | Status | Notes |
|---|---|---|
| `Sovereign` | 📐 | Top-level deployment object. No Go type yet. |
| `Organization` | 📐 | Multi-tenancy unit. No Go type yet. |
| `Environment` | 📐 | `{org}-{env_type}` scope. No Go type yet. |
| `Application` | 📐 | An installed Blueprint. No Go type yet. |
| `Blueprint` | 📐 | The unified Blueprint CRD spec is in [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) §3 — that is the design contract for the Go type. |
| `EnvironmentPolicy` | 📐 | Promotion gating. No Go type yet. |
| `SecretPolicy` | 📐 | Rotation policy. No Go type yet. |
| `Runbook` | 📐 | Auto-remediation. No Go type yet. |

`core/pkg/apis/v1alpha1/` is currently a `.gitkeep` directory. The Go types will be added when the control-plane services are scaffolded.

---

## 5. Surfaces

| Surface | Status | Notes |
|---|---|---|
| **UI** (Catalyst console) | 📐 | Astro + Svelte target stack chosen; no code yet. |
| **Git** (direct push to Application Gitea repo, branch per env_type) | 📐 | Pattern documented; depends on provisioning-service + environment-controller being implemented. |
| **API** (REST + GraphQL) | 📐 | OpenAPI / GraphQL schema not yet defined. |
| **kubectl** (debug-only inside own vcluster) | 📐 | Standard K8s; works as soon as a Sovereign exists. |

---

## 6. Sovereigns running today

| Sovereign | Status | Notes |
|---|---|---|
| `openova` Catalyst-Zero (the chicken in the chicken-and-egg) | 🚧 | **Running on Contabo k3s today** in namespaces `catalyst`, `sme`, `marketplace`, `website`. Pods include catalyst-{ui,api}, console, admin, marketplace, marketplace-api. Catalyst-Zero IS the catalyst-provisioner that provisions every other Sovereign — see `docs/PROVISIONING-PLAN.md`. As of 2026-04-28 (Pass 105), all UI source code is consolidated into `core/` and `products/catalyst/` in this public repo; cutover to public-repo CI builds happens in Phase 2 of the plan. |
| `omantel` (first franchised Sovereign, target: `omantel.omani.works` on Hetzner) | 📐 | Provisioned by Catalyst-Zero per Phase 8 of `docs/PROVISIONING-PLAN.md`. Not yet provisioned. |
| `bankdhofar` | 📐 | Planned. Customer-hosted. Not yet provisioned. |

---

## 7. Catalyst provisioner

| Item | Status | Notes |
|---|---|---|
| `catalyst-provisioner.openova.io` always-on service | 🚧 | Designed in [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §2. Catalyst-Zero (Contabo k3s, namespace `catalyst`) IS the catalyst-provisioner today. Real Go provisioning code lives at [`products/catalyst/bootstrap/api/internal/provisioner/`](../products/catalyst/bootstrap/api/internal/provisioner/) — a thin wrapper around `tofu` that writes `tofu.auto.tfvars.json` from wizard input, runs `tofu init && tofu plan && tofu apply` against [`infra/hetzner/`](../infra/hetzner/), and streams events back to the wizard via SSE. Per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #3, no cloud APIs called from Go code; OpenTofu does Phase 0, Crossplane adopts day-2 management at Phase 1 hand-off. End-to-end DoD against a real Hetzner project pending Group M (#43 waterfall). |
| Hetzner OpenTofu modules | 🚧 | Canonical module at [`infra/hetzner/`](../infra/hetzner/) — `main.tf` provisions VPC + subnet + firewall + SSH key + control-plane and worker servers (variable count, ha_enabled toggle) + load balancer + DNS via the catalyst-dns helper for managed pool domains. `cloudinit-control-plane.tftpl` installs k3s and bootstraps Flux pointing at `clusters/<sovereign-fqdn>/` in this monorepo. `cloudinit-worker.tftpl` joins workers via the project-derived k3s token. All values are runtime variables — no hardcoded region, sizes, or k3s flags per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #4. |
| Bootstrap kit (cilium → cert-manager → flux → crossplane → sealed-secrets → spire → nats-jetstream → openbao → keycloak → gitea → powerdns → bp-catalyst-platform) | 🚧 | **All 12 G2 wrapper Helm charts exist** under `platform/<x>/chart/` (Pass 105, commit 8c0f766; bp-powerdns added at commit 0190c605 for #167) including the new platform/spire/, platform/nats-jetstream/, platform/sealed-secrets/, platform/powerdns/. Each carries a `blueprint.yaml`, `values.yaml`, `Chart.yaml`, and is published as `bp-<name>:<semver>` OCI artifact via `.github/workflows/blueprint-release.yaml`. Flux on the new cluster reconciles `clusters/<sovereign-fqdn>/` to install them in the dependency order specified in [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §3. Steady-state DoD pending real Hetzner provisioning (Group M). |

---

## 8. What this means for newcomers

If you're reading the Catalyst architecture for the first time:

- The **architectural model** in [`ARCHITECTURE.md`](ARCHITECTURE.md) is the agreed direction. The model is settled.
- The **code in this repo** is mostly a scaffold today. Significant implementation lies ahead.
- The **canonical docs** ([`GLOSSARY.md`](GLOSSARY.md), [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md), [`SECURITY.md`](SECURITY.md), [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md), [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md), [`PERSONAS-AND-JOURNEYS.md`](PERSONAS-AND-JOURNEYS.md), [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md), [`SRE.md`](SRE.md)) describe the **target** the implementation is converging toward.
- Component-level READMEs under `platform/<name>/` describe the upstream technology and Catalyst's intended use of it. Most do not yet contain a deployable Blueprint.

If a doc says "Catalyst does X" without a `📐` or `🚧` marker, treat it as a target. Use this `IMPLEMENTATION-STATUS.md` to confirm whether X is built today.

---

## 9. How to update this file

This file is updated whenever a status changes:

- A controller is implemented → flip the row from 📐 to ✅.
- A component is partially shipped → 🚧 with notes on what's missing.
- A target is deferred → ⏸ with a forward-pointing reference.

Keeping this honest is the only way to prevent the kind of doc/code drift that makes the architecture text unreliable.
