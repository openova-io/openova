# Catalyst Implementation Status

**Status:** Authoritative. Living document. **Updated:** 2026-04-27

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
| `core/` Catalyst control-plane Go application | 📐 | Directory tree exists with `.gitkeep` placeholders. No Go code yet. |
| `products/axon/` | ✅ | Real implementation (chart/, src/, scripts/). The only product with code. |
| `products/catalyst/` umbrella Blueprint (`bp-catalyst-platform`) | 📐 | Currently only a `bootstrap/ui/` Vite scaffold. No umbrella manifest. |
| `products/{cortex,fabric,fingate,relay}/` | 📐 | README only. No charts or manifests. |

---

## 2. Catalyst control plane components (per [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §2)

These run **per-Sovereign** on the management cluster:

### 2.1 User-facing surfaces and backend services

| Component | Status | Notes |
|---|---|---|
| console (Catalyst UI) | 📐 | Designed. No code. |
| marketplace (public Blueprint card grid) | 📐 | Designed. No code. |
| admin (sovereign-admin operations UI) | 📐 | Designed. No code. |
| catalog-svc | 📐 | Designed. No code. |
| projector (CQRS read-side, JetStream → KV → SSE) | 📐 | Designed. No code. |
| provisioning service | 📐 | Designed. No code. |
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
| k8gb | 🚧 | README only. |
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
| `openova` (the OpenOva-run Sovereign — formerly "Nova") | 🚧 | Current deployment runs on Contabo at `console.openova.io/nova` (legacy SME marketplace); does NOT run Catalyst control plane yet. Migration to a true Catalyst Sovereign is the implementation goal. |
| `omantel` | 📐 | Planned. Hetzner-hosted. Not yet provisioned. |
| `bankdhofar` | 📐 | Planned. Customer-hosted. Not yet provisioned. |

---

## 7. Catalyst provisioner

| Item | Status | Notes |
|---|---|---|
| `catalyst-provisioner.openova.io` always-on service | 📐 | Documented in [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md). Currently the legacy Contabo VPS runs the SME marketplace; provisioner role is target state. |
| Hetzner OpenTofu modules | 📐 | Skeleton may exist in `openova-private/infra/`; not yet aligned with the Catalyst bootstrap kit. |
| Bootstrap kit (cilium → flux → spire → jetstream → openbao → catalyst control plane) | 📐 | Designed; implementation tracked under issue #37 follow-ups. |

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
