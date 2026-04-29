# Catalyst Implementation Status

**Status:** Authoritative. Living document. **Updated:** 2026-04-29 (Reconcile Pass 3 — admin landing surface, umbrella charts, helmwatch, deployments PVC, OpenTofu CLI bundling, Cilium-before-Flux cloud-init, infrastructure-config Kustomization split, blueprint-release CI hollow-chart guards).

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
| catalyst-ui | 🚧 | React SPA wizard scaffold at `products/catalyst/bootstrap/ui/`. Deployed on Catalyst-Zero (namespace `catalyst`). 7-step wizard, canonical order from `STEPS` in [`src/pages/wizard/WizardPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/wizard/WizardPage.tsx): **Org → Topology → Provider → Credentials → Components → Domain → Review** (the operator picks the platform first — sizing, provider, creds, components — and only then names the Sovereign in DNS). Per #176 Topology drives both the SKU pickers (via `PROVIDER_NODE_SIZES[provider]`) and worker count; per #d3346441/#b0ec0c43 Components is a single flat marketplace card grid with family chips + product detail / family portfolio routes. After clicking **Provision** the wizard redirects to the Sovereign Admin landing surface at `/sovereign/provision/$deploymentId` (route module [`AdminPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/sovereign/AdminPage.tsx); the legacy `ProvisionPage.tsx` is now a 22-line re-export shim). The DAG view (~1300 lines of SVG bubbles + edges + supernode mapping) was gutted at `4047ba1d` and replaced with an **application card grid** (every Application installed on this Sovereign — bootstrap-kit + user-selected — renders as a card with status pill + brand-coloured logo + family chip from first paint), with per-Application drill-down at `/sovereign/provision/$deploymentId/app/$componentId` ([`ApplicationPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/sovereign/ApplicationPage.tsx)) carrying Overview / Logs / Dependencies / Status tabs. Merges into `core/console/src/pages/sovereign/` per Phase 3. |
| catalyst-api | 🚧 | Go bootstrap API at `products/catalyst/bootstrap/api/`. Deployed on Catalyst-Zero. `internal/hetzner/` is read-only credential validation only — never used to mutate cloud state. `internal/provisioner/` execs the bundled `tofu` binary against the bundled `infra/hetzner/` module (both shipped inside the catalyst-api image — Tofu CLI v1.11.6 with SHA256-verified release at commits `9b6c297d` + `61c61226`). New: `internal/helmwatch/` (per-component HelmRelease watch on the new Sovereign cluster, emitting SSE events shaped `phase: "component", component: <id>, state: pending|installing|installed|failed|degraded` once `flux-bootstrap` lands — `5be6bcba`); `internal/store/` (per-deployment JSON file at `/var/lib/catalyst/deployments/<id>.json` with secrets redacted — `hetznerToken`, `dynadotKey`, `dynadotSecret`, `registrarToken` are stripped — backed by RWO PVC `catalyst-api-deployments` 1Gi `Recreate` strategy `fsGroup: 65534`, `418cead0`). New endpoints: `GET /api/v1/deployments/<id>/kubeconfig` (returns the temporary kubeconfig captured at `tofu-output`) and `GET /api/v1/deployments/<id>/events` (replays the persisted SSE history). New env vars: `CATALYST_PHASE1_WATCH_TIMEOUT` (default 60m) and `CATALYST_DEPLOYMENTS_DIR` (default `/var/lib/catalyst/deployments`). Migrates into `core/marketplace-api/provisioner/` per Phase 4. |
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
| PowerDNS | ✅ | bp-powerdns:1.1.0 deployed on contabo-mkt (#167; gpgsql-dnssec=yes; live HelmRelease confirms `bp-powerdns@1.1.0+ef3c785bfd24`) — authoritative DNS for every Sovereign zone (pool + BYO), CNPG-backed Postgres at `pdns-pg`, dnsdist front-end. Replaces the historical k8gb GSLB role via lua-records. See [`PLATFORM-POWERDNS.md`](PLATFORM-POWERDNS.md) and [`MULTI-REGION-DNS.md`](MULTI-REGION-DNS.md). |
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
| `catalyst-provisioner.openova.io` always-on service | 🚧 | Designed in [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §2. Catalyst-Zero (Contabo k3s, namespace `catalyst`) IS the catalyst-provisioner today. Real Go provisioning code lives at [`products/catalyst/bootstrap/api/internal/provisioner/`](../products/catalyst/bootstrap/api/internal/provisioner/) — a thin wrapper around `tofu` that writes `tofu.auto.tfvars.json` from wizard input, runs `tofu init && tofu plan && tofu apply` against the bundled [`infra/hetzner/`](../infra/hetzner/) module (the canonical Tofu sources are baked into the catalyst-api image at `/infra/hetzner/`, and the `tofu` v1.11.6 CLI is bundled too — both shipped at commits `61c61226` + `9b6c297d`, smoke-tested at build time, SHA256-verified against the upstream OpenTofu release artefacts), and streams events back to the wizard via SSE. Per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #3, no cloud APIs called from Go code; OpenTofu does Phase 0, Crossplane adopts day-2 management at Phase 1 hand-off. End-to-end DoD against a real Hetzner project pending Group M (#43 waterfall). |
| Hetzner OpenTofu modules | 🚧 | Canonical module at [`infra/hetzner/`](../infra/hetzner/) — `main.tf` provisions VPC + subnet + firewall + SSH key + control-plane and worker servers (variable count, ha_enabled toggle) + load balancer. **No DNS in this module** — the historical `null_resource.dns_pool` was removed at `330211d2` because pool-domain-manager (PDM) is the single owner of pool-domain Dynadot writes (PDM `/v1/commit` runs once the LB IP is known, after `tofu-output` resolves). `variables.tf` regex accepts every Hetzner SKU family (`cx*` shared Intel, `cpx*` shared AMD — the wizard's recommended **CPX32** (4 vCPU AMD / 8 GB / €0.0232/hr) lives here, `ccx*` dedicated Intel, `cax*` Ampere Arm — `c6cbfe68`); `worker_size = ""` is also valid for solo Sovereigns where `worker_count = 0` (the empty string short-circuits the validation regex per the same commit). `cloudinit-control-plane.tftpl` installs k3s with `--flannel-backend=none`, then **installs Cilium first via Helm** (with `k8sServiceHost=127.0.0.1` so the bootstrap doesn't deadlock on the LB IP — `54872009`), then installs Flux core (`e571ec7a`), then applies a single cloud-init manifest (`/var/lib/catalyst/flux-bootstrap.yaml`) that creates the GitRepository plus **two** Kustomizations — `bootstrap-kit` (HelmReleases) and `infrastructure-config` (ProviderConfig + Crossplane Compositions, `dependsOn: bootstrap-kit`, `wait: true`) — pointing at `clusters/<sovereign-fqdn>/` in this monorepo. The split (`34c8de84` + `2da4c43c`) keeps Crossplane CRDs from being applied before the controller is healthy. `cloudinit-worker.tftpl` joins workers via the project-derived k3s token. All values are runtime variables — no hardcoded region, sizes, or k3s flags per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #4. |
| Bootstrap kit (cilium → cert-manager → flux → crossplane → sealed-secrets → spire → nats-jetstream → openbao → keycloak → gitea → bp-catalyst-platform; bp-powerdns runs on Catalyst-Zero only) | 🚧 | **All 11 bp-* charts under `clusters/<sovereign-fqdn>/bootstrap-kit/` are now Helm umbrella charts at v1.1.0** — each `Chart.yaml` declares its upstream chart under `dependencies:` so `helm dependency build` pulls the upstream payload into the published OCI artifact (`43aff202` + `e42799fa`). Pinned upstream versions: cilium 1.16.5, cert-manager v1.16.2, flux2 2.13.0, crossplane 1.18.0, sealed-secrets 2.16.1, spire 0.21.0, nats 1.2.0 (jetstream), openbao 0.16.0, keycloak 24.7.1 (bitnami), gitea 10.5.0, bp-catalyst-platform 1.1.0 (umbrella over the 10 leaves + bp-external-dns). `bp-powerdns:1.1.0` (upstream `pschichtel/powerdns` 0.10.0) ships separately and is deployed on Catalyst-Zero only — it's the authoritative DNS for every other Sovereign's zone, not part of the franchised Sovereign install set. **The earlier v1.0.0 / v1.0.1 OCI artefacts are HOLLOW** (Catalyst overlays only, no upstream subchart bytes — see [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) §11.1 for the post-mortem); CI now enforces the umbrella shape via the four hollow-chart guards in [`.github/workflows/blueprint-release.yaml`](../.github/workflows/blueprint-release.yaml) (verified at `bdeb0f54`/`54418bd5`/`35dcb84d`). The cluster manifests (`clusters/_template/bootstrap-kit/01-cilium.yaml` … `11-bp-catalyst-platform.yaml`) reference the OCI artefacts at `oci://ghcr.io/openova-io` with `secretRef: ghcr-pull` (`efa41803`); the duplicate `kube-system` Namespace declarations on `01-cilium.yaml` + `05-sealed-secrets.yaml` were dropped at `2022e1af` (kubectl-built-in namespace, never re-declare). Flux on the new cluster reconciles `clusters/<sovereign-fqdn>/` to install them in the dependency order specified in [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) §3. Steady-state DoD pending real Hetzner provisioning (Group M). |

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
