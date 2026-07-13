# Catalyst Implementation Status

**Status:** Authoritative. Living document. **Updated:** 2026-07-14 (re-stamped to hw24x/hw25x truth: Huawei kom4dc substrate, 11-step cutover chain, Pillar-4 = Agenity + `bp-openova-mcp`). **Staleness rule:** if this stamp is more than 14 days old, treat this file as drifted — re-derive live state before trusting any row (see §11 Re-stamp cadence).

This document is the **bridge** between the target architecture (described in [`ARCHITECTURE.md`](ARCHITECTURE.md), [`SECURITY.md`](SECURITY.md), [`RUNBOOKS.md`](RUNBOOKS.md) §3 Blueprint authoring, etc.) and the current state of the code in this repository.

The other architecture docs describe the **target**: where Catalyst is going. This document records **what exists today** and **what is design-only**. When in doubt, read this file before making any claim about Catalyst's capabilities.

> If you find a claim elsewhere in this repo that contradicts this file, this file wins until either (a) the code catches up to the claim or (b) the claim is corrected — **unless (c) the `Updated:` stamp above is more than 14 days old**, in which case this file's precedence is VOID until it is re-stamped against live state. (This file once sat frozen for ~8 weeks while still claiming to "win" — the staleness clause exists so that can never happen again.)

---

## Status legend

| Status | Meaning |
|---|---|
| ✅ **Implemented** | Code exists, tested, deployable; AND operator walked the surface on a fresh prov with screenshot evidence. |
| 🟦 **CODE-COMPLETE** | All controllers + CRDs + tests landed; awaiting fresh-prov walk to flip to ✅ per 5-pillar DoD. |
| 🚧 **Partial** | Some code exists; significant gaps; not production-ready. |
| 📐 **Design** | Documented in canonical docs; no code yet. The doc is the contract for the future implementation. |
| ⏸ **Deferred** | Mentioned in docs but explicitly queued for a future chain. |

Per [`DOD.md`](DOD.md), 🟦 CODE-COMPLETE does NOT mean shipped. A pillar is **shipped** when an operator walks a **fresh prov** through the pillar-relevant steps and produces a screenshot + non-empty wire-capture + working downstream artifact. PR merge ≠ pillar shipped.

---

## 1. Repository structure

*(Section re-stamped 2026-07-14.)*

| Item | Status | Notes |
|---|---|---|
| Public repo at `github.com/openova-io/openova` | ✅ | Monorepo. Source of truth for documentation and (eventually) for every Blueprint's manifests. |
| Per-folder Blueprint convention (`platform/<name>/` and `products/<name>/`) | 🚧 | Many folders carry chart/ + blueprint.yaml + CI; many still README-only. |
| `bp-<name>:<semver>` OCI artifacts in `ghcr.io/openova-io/` | ✅ | `.github/workflows/blueprint-release.yaml` fans out per-Blueprint to signed OCI artifacts with SBOM + cosign signature. |
| `core/{console,admin,marketplace,marketplace-api}/` | 🚧 | Consolidated 2026-04-28 from `openova-private`. Astro+Svelte UIs + Go backend. Deployed on Catalyst-Zero. |
| `products/axon/` | ✅ | Real implementation (chart/, src/, scripts/). |
| `products/agenity/` + `products/openova-mcp/` | 🟦 | The **Pillar 4 pair** (re-scoped — see §2.2 and §9): `bp-agenity` (chart 0.5.20 — per-Org multi-agent runtime + dashboard) + `openova-mcp` (Go MCP server: RBAC-scoped thin facade over live catalyst-api, JSON-RPC over stdio). |
| `products/catalyst/` umbrella (`bp-catalyst-platform`) | 🟦 | Has `bootstrap/{ui,api}/` (React SPA wizard + Go bootstrap API) + `chart/` with Chart.yaml + Helm templates for the full Catalyst-Zero deployment + `crds/` with the 13 CRDs enumerated in §4. Canonical Helm chart for Catalyst-Zero and every franchised Sovereign. |
| `products/{cortex,fabric,fingate,relay}/` | 📐 | README only. No charts or manifests. (`products/specter/` does not exist — Specter is a deliverable service, not a Blueprint in this layout, per `README.md` §"What's in this repo".) |

---

## 2. Catalyst control plane components (per [`ARCHITECTURE.md`](ARCHITECTURE.md) §2)

*(Section re-stamped 2026-07-14.)*

These run **per-Sovereign** on the management cluster.

### 2.1 User-facing surfaces and backend services

| Component | Status | Notes |
|---|---|---|
| console (Catalyst UI) | 🚧 | Astro + Svelte UI at `core/console/`. Deployed on Catalyst-Zero. BSS menu (vouchers, billing, catalog, orders, Organizations) now lives inside the operator console at `/bss` — replaces the legacy `admin.<sovereign-fqdn>` URL. |
| marketplace (public Blueprint card grid) | 🟦 | Astro + Svelte UI at `core/marketplace/`. **CODE-COMPLETE** for Pillar 1 step 2: configSchema fields now render on AppDetail (PR [#2038](https://github.com/openova-io/openova/pull/2038), 2026-05-19, TBD-V18); form values thread into the install POST (PR [#2043](https://github.com/openova-io/openova/pull/2043), TBD-V18-D). |
| admin (sovereign-admin operations UI) | 🚧 | Astro + Svelte UI at `core/admin/`. Voucher / billing / catalog / orders / Organizations admin surface — being folded into console `/bss` per BSS migration. |
| catalyst-ui (provisioning wizard) | 🟦 | React SPA wizard scaffold at `products/catalyst/bootstrap/ui/`. **CODE-COMPLETE** Pillar 2 (multi-region BCP topology choice at signup): wizard surfaces region picker + BCP mode, threads into Topology step output (PR [#2029](https://github.com/openova-io/openova/pull/2029), TBD-V20). Canonical order: Org → Topology → Provider → Credentials → Components → Domain → Review. |
| catalyst-api | 🟦 | Go bootstrap API at `products/catalyst/bootstrap/api/`. **CODE-COMPLETE** Pillar 3 install path: provisioning now generalises `bp-cnpg-pair` install beyond WP-only (PR [#2073](https://github.com/openova-io/openova/pull/2073)); threads `Tenant.AppConfigs` (Go struct in `internal/store/tenant_registry.go`) into rendered manifests (PR [#2053](https://github.com/openova-io/openova/pull/2053), TBD-V27); emits a `Continuum` CR for each multi-region Organization Application (PR [#2074](https://github.com/openova-io/openova/pull/2074)). |
| marketplace-api | 🚧 | Go backend at `core/marketplace-api/`. Has `provisioner/` and `store/` modules. |
| catalog-svc | 📐 | Designed. No code. |
| projector (CQRS read-side, JetStream → KV → SSE) | 📐 | Designed. No code. |
| provisioning service | 🟦 | Pillar 3 logic at `core/marketplace-api/provisioner/`. Multi-region CNPG pair + Continuum emission CODE-COMPLETE post PR #2073 / #2074. |
| environment-controller | 🚧 | Controller scaffolded with Reconcile at `core/controllers/environment/internal/controller/environment_controller.go` (post-2026-05-20). Not yet walked on a fresh prov. |
| blueprint-controller | 🚧 | Controller scaffolded with Reconcile at `core/controllers/blueprint/internal/controller/blueprint_controller.go` (post-2026-05-20). Not yet walked on a fresh prov. |
| billing | 📐 | Designed. No code. |

### 2.2 Per-Sovereign supporting services

| Component | Status | Notes |
|---|---|---|
| Gitea (per Sovereign) | 🚧 | Chart + blueprint.yaml shipped. Per-Sovereign deployment manifests landing per ADR-0002 cutover. |
| NATS JetStream (per Sovereign) | 🚧 | Chart + blueprint.yaml shipped. Per-Org Account isolation pending. |
| OpenBao (per region, independent Raft) | 🚧 | Chart + blueprint.yaml shipped. Multi-region replication semantics agreed; not yet wired in topology. |
| Keycloak (per-Org SME / per-Sovereign corporate) | 🚧 | Chart + blueprint.yaml shipped. Topology choice is a Catalyst-level concern not yet wired. |
| Agenity workspace + `bp-openova-mcp` (Pillar 4 — re-scoped) | 🟦 | **Pillar 4 is Agenity + `bp-openova-mcp`** per [`DOD.md`](DOD.md) §Pillar-4 (founder re-scope; the Sandbox surface is REMOVED from Pillar 4). `bp-agenity` ([`products/agenity/`](../products/agenity/), chart 0.5.20): per-Org multi-agent runtime + dashboard whose solo agent drives the RBAC-scoped [`products/openova-mcp/`](../products/openova-mcp/) facade over live catalyst-api to provision Applications in the User's own Organization. Proven live twice (dep 91dc0591, omantel.biz, 2026-06-28; zero-touch hw220, 2026-07-03). Outstanding: durable per-Sovereign Anthropic credential + credential-propagation design call (founder-gated — #4111/#4277) and `bp-agenity` auto-install on clean provs. The legacy Sandbox controller + `openova-sandbox-mcp` auto-mount chain (PRs #1615/#1618/#1621/#1622/#1626/#1631/#1632) is retained as **internal machinery only**, exempt from pillar DoD (see §4). |
| SPIRE server + agent | ⏸ | **DEFERRED — opt-in only.** PR [#665](https://github.com/openova-io/openova/pull/665) (2026-05-03) dropped bp-spire from the canonical bootstrap-kit. `clusters/_template/bootstrap-kit/06-spire.yaml` deleted; bp-spire `dependsOn` removed from `07-nats-jetstream.yaml` and `08-openbao.yaml`. Doc-alignment sweeps: PRs [#2056](https://github.com/openova-io/openova/pull/2056), [#2061](https://github.com/openova-io/openova/pull/2061) (Refs [#2055](https://github.com/openova-io/openova/issues/2055), TBD-V29). The `platform/spire/` chart is retained as opt-in. Workload identity today: Cilium WireGuard (kernel-layer east-west encryption, 100% mesh coverage) + K8s ServiceAccount TokenReview (audience-scoped 1h projected bound-tokens via the OpenBao `kubernetes` auth method). Re-enable triggers per [`SECURITY.md`](SECURITY.md) §2: (a) cross-Sovereign workload federation, (b) compliance audit requiring sub-hour cryptographic workload attestation (SOC2/PCI-DSS/FedRAMP), (c) per-workload-fingerprint authorization beyond `(namespace, ServiceAccount)`. Re-introduction roadmap sketched in TBD-V29. |
| Continuum (failover orchestrator, Pillar 3) | 🟦 | **CODE-COMPLETE.** `bp-continuum` wired into bootstrap-kit by PR [#2072](https://github.com/openova-io/openova/pull/2072). Continuum CRD ships in `products/catalyst/chart/crds/continuum.yaml`. Used by Pillar 3 region-kill recovery walk. |
| cnpg-pair (Pillar 3) | 🟦 | **CODE-COMPLETE.** `bp-cnpg-pair` ships synchronous replication (`remote_apply`) for zero-tx-loss (PR [#2071](https://github.com/openova-io/openova/pull/2071)). CRD ships in `products/catalyst/chart/crds/cnpgpair.yaml`. D31 acceptance test harness landed: PR [#2075](https://github.com/openova-io/openova/pull/2075). |
| bp-self-sovereign-cutover (Pillar 5) | 🚧 | Dormant slot 06a in bootstrap-kit. **11-step post-handover chain** ([`platform/self-sovereign-cutover/chart`](../platform/self-sovereign-cutover/chart/), currently **0.1.126**) pivots every mothership tether — step Jobs 01–11: gitea-mirror, harbor-projects, harbor-prewarm (+ offline-mirror-resolver), containerd registry-pivot DaemonSet, flux-gitrepository-patch, helmrepository-patches, catalyst-api-env-patch, egress-block-test, cutover-status + gitea-token-mint, vcluster-registry-pivot + auto-trigger, crossplane-provider-pivot + mirror-resync CronJob. The sovereignty proof is the **10-minute deny-egress hold** against `github.com`, `ghcr.io`, `harbor.openova.io`; `cutoverComplete=true` only if the cluster reconciles green during the hold. **Keystone — zero-touch `cutoverComplete=true` on a fresh 2-region kom4dc prov — is PENDING**: furthest ever hw246 (green through step-7, died at the step-8 pre-hold ref-host lint); the redirect-aware lint ruling merged as #5038 (2026-07-12), not yet proven live. ADR-0002's original "eight sequential Jobs" spec is superseded by this shipped 11-step chain. |
| Catalyst observability (Grafana stack) | 🚧 | Per-component READMEs exist; not yet wired as a Catalyst-level umbrella. |

---

## 3. Per-host-cluster infrastructure (per [`ARCHITECTURE.md`](ARCHITECTURE.md) §3)

*(Section re-stamped 2026-07-14.)*

These run on **every host cluster** (mgt, rtz, dmz).

| Component | Status | Notes |
|---|---|---|
| Cilium | 🚧 | Chart + blueprint.yaml shipped at `platform/cilium/`. WireGuard east-west mesh enabled by default per PR #665. |
| External-DNS | 🚧 | README only. |
| PowerDNS | ✅ | bp-powerdns:1.0.6 deployed (#167; gpgsql-dnssec=yes) — authoritative DNS for every Sovereign zone (pool + BYO), CNPG-backed Postgres at `pdns-pg`, dnsdist front-end. See [`ARCHITECTURE.md`](ARCHITECTURE.md) §8.9 (deployment shape) and §8.8 (multi-region lua-records). |
| pool-domain-manager (PDM) | ✅ | Deployed at `pool-domain-manager` in `openova-system` (#163, #168, #170). CNPG-backed `pdm-pg`. Allocates pool subdomains under `omani.works`/`omani.homes`/`omani.rest`/`omani.trade`/`omantel.biz`, owns the per-Sovereign PowerDNS zone lifecycle, and exposes registrar adapters (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot) for BYO Flow B (registrar-API NS-flip). REST API: `/v1/reserve`, `/v1/commit`, `/v1/validate`, `/v1/registrars`. Source: [`core/pool-domain-manager/`](../core/pool-domain-manager/). |
| Coraza | 🚧 | README only. |
| Flux | 🚧 | Chart + blueprint.yaml shipped. Per-vcluster Flux convention is Catalyst-managed; not yet implemented. |
| Crossplane | 🚧 | Chart + blueprint.yaml shipped. Compositions live in product folders. |
| OpenTofu (bootstrap IaC) | ✅ | Provider-split modules at `infra/providers/{huawei,hetzner,_shared}` — **Huawei kom4dc is the delivery target** (HCS region `me-east-215`; 2-region topology via fake-regions `me-east-215-a`/`-b` with per-region VPC isolation). Runs at Phase 0 from the catalyst-api wrapper. Pre-fire quota gates (VPC 5-total/2-per-prov, EVS 400-volume cap, ~15-min wipe-release lag): [`docs/runbooks/preflight-sovereign-provision.md`](runbooks/preflight-sovereign-provision.md). |
| cert-manager | 🚧 | Chart + blueprint.yaml shipped. ClusterIssuer rendered by Catalyst overlay. |
| External Secrets Operator | 🚧 | README only. |
| Kyverno | 🚧 | README only. |
| Trivy | 🚧 | README only. |
| Falco | 🚧 | README only. |
| Sigstore | 🚧 | README only. |
| Syft + Grype | 🚧 | README only. |
| VPA, KEDA, Reloader | 🚧 | READMEs only. |
| SeaweedFS, Velero, Harbor | 🚧 | READMEs only. |
| failover-controller | 🚧 | README only. Replaced by `Continuum` controller pattern (EPIC-6 #1101). |

---

## 4. CRDs

*(Section re-stamped 2026-07-14.)*

All canonical CRD schemas now ship in `products/catalyst/chart/crds/`. Go types + reconciliation controllers (Group C of #1095) land per pillar work.

| CRD | Status | Notes |
|---|---|---|
| `Sovereign` | 📐 | Top-level deployment object. No Go type yet. |
| `Organization` | 🚧 | Schema in `organization.yaml` (slice B1, PR #1106). organization-controller (slice C1) scaffolded with Reconcile at `core/controllers/organization/internal/controller/` (post-2026-05-20) — see §8 ADR-0009 iac-bootstrap row. |
| `Environment` | 🚧 | Schema in `environment.yaml` (slice B2, PR #1107). environment-controller (slice C2) scaffolded with Reconcile at `core/controllers/environment/internal/controller/` (post-2026-05-20). |
| `Application` | 🚧 | Schema in `application.yaml` (slice B3, PR #1105). application-controller (slice C4) scaffolded with Reconcile at `core/controllers/application/internal/controller/` (post-2026-05-20). **G117.6 (Wave-2/C1)** — application-controller now resolves typed `Blueprint.spec.topology` (W1.B1 contract), fans HelmReleases per `perTopology[<choice>].placement.clusters[]` with `catalyst.openova.io/{role,topology,app,cluster}` labels, and writes `Application.status.perCluster[]` for operator-console drill-down + catalyst-api `GET /apps/{id}.perCluster[]`. Operator-override via `Application.spec.topology`; default keyed on Sovereign-region-count (locked decision #7). Reasons: `InvalidTopology` on mismatch with `Blueprint.spec.topology.supported[]`. Refs #2674 + #2745. **G117.2 W2.C2 (multi-instance Applications)** — Application CRD now carries `spec.instanceId` (immutable per CRD `x-kubernetes-validations`), `spec.isolationLevel` (enum `namespace` (default) / `vcluster`), and `spec.namingTemplate` (Go-template; default depends on isolationLevel). New admission package at `core/controllers/application/admission/` enforces multi-instance + name-collision + maxPerOrg gates per the OpenAPI 409 contract; catalyst-api `POST /apps/instances` delegates through `instances.MapDecision`. Migration tool at `tools/migrate-applications-instanceId.py` (idempotent) wired into `products/catalyst/chart/templates/migrate-applications-instanceId-job.yaml` as a post-install / post-upgrade Helm hook Job. Refs G117.2 #2741. |
| `Blueprint` | 🚧 | Schema in `blueprint.yaml` (slice B4, PR #1112). Serves `v1alpha1` (legacy) + `v1` (canonical). All 59 existing platform/products blueprint.yamls validate. blueprint-controller (slice C3) scaffolded with Reconcile at `core/controllers/blueprint/internal/controller/` (post-2026-05-20). **G117.1 (Wave-1/B3)** — every Application-tier `platform/<bp>/blueprint.yaml` (29 Blueprints) now declares typed `spec.topology` + `spec.endpoints` + `spec.sso` + `spec.multiInstance` per `platform/_schemas/blueprint-topology.json`. 9 scaffold-only Blueprints (ferretdb / strimzi / clickhouse / opensearch / milvus / neo4j / flink / debezium / iceberg) got a new minimal `blueprint.yaml` carrying the 4 blocks ahead of chart authoring. Helper at `tools/g117_w1b3_apply.py` is idempotent for re-runs. |
| `EnvironmentPolicy` | 🚧 | Schema in `environmentpolicy.yaml` (slice B5, PR #1108). Promotion gating + per-policy compliance weights + permissive/enforcing modes. Consumer (compliance-aggregator) lives in EPIC-1 #1096. |
| `SecretPolicy` | 🚧 | Schema in `secretpolicy.yaml` (slices B6+B7, PR #1111). Skeleton — populated by SRE Lead post-Phase-0; rotation engine is a future controller. |
| `Runbook` | 🚧 | Schema in `runbook.yaml` (slices B6+B7, PR #1111). Skeleton — auto-remediation hooks for prometheus-alert / cr-condition / nats-event / schedule triggers. Executor is a future controller. |
| `Continuum` | 🟦 | Schema in `continuum.yaml` (slice B8, PR #1110). Group `dr.openova.io/v1`. Switchover orchestration with Cloudflare-KV or DNS-quorum lease witness. **bp-continuum wired into bootstrap-kit** by PR [#2072](https://github.com/openova-io/openova/pull/2072). continuum-controller in EPIC-6 #1101. |
| `ProvisioningState` | 🚧 | Schema in `provisioningstate.yaml` (slice H3, PR #1104). Writer is `internal/store/crd_store.go`. |
| `CNPGPair` *(Pillar 3, new 2026-05-20)* | 🟦 | Schema in `cnpgpair.yaml`. Group `dr.openova.io/v1`. Defines a paired CNPG cluster across two regions over Cilium ClusterMesh with synchronous replication (`remote_apply`, `ReplicaCluster`). Used by `bp-cnpg-pair` (PR [#2071](https://github.com/openova-io/openova/pull/2071)) for zero-tx-loss Pillar 3 walk. D31 acceptance test PR [#2075](https://github.com/openova-io/openova/pull/2075). |
| `PDM` *(Pool Domain Manager allocations, new 2026-05-20)* | 🚧 | Schema in `pdm.yaml`. Tracks per-Sovereign pool subdomain allocation, parent-zone NS delegation state, and registrar-adapter flow (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot). Consumed by PDM service at `core/pool-domain-manager/`. |
| `Sandbox` *(internal — REMOVED from Pillar 4 scope)* | 🟦 | Schema in `sandbox.yaml`. Controller + auto-mounted `openova-sandbox-mcp` chain landed (PRs #1615 scaffold, #1618, #1621, #1622, #1626, #1631, #1632) — but per the founder Pillar-4 re-scope the Sandbox surface is **no longer a pillar surface**: Pillar 4 = Agenity + `bp-openova-mcp` (§2.2, §9, [`DOD.md`](DOD.md) §Pillar-4). The CRD + controller are retained as internal machinery, exempt from pillar DoD. |

Go types live at `core/pkg/apis/<group>/v1alpha1/` (e.g. `application/v1alpha1/application_types.go`, `blueprint/v1alpha1/topology_types.go`) and at `core/controllers/pkg/apis/`; further types are added as control-plane services are scaffolded (slice C1..C5 of #1095).

---

## 5. Surfaces

*(Section dated 2026-05-20.)*

Per [`CLAUDE.md`](../CLAUDE.md) §"What's user-facing": **UI / Git / API only**. There is no Terraform provider, no Pulumi SDK, no `catalystctl install` for production changes. Crossplane is platform plumbing, never a user surface.

| Surface | Status | Notes |
|---|---|---|
| **UI** (Catalyst console) | 🚧 | Astro + Svelte target stack. Multiple surfaces deployed (console, admin, marketplace); full integration pending. |
| **Git** (direct push to Application Gitea repo, branch per env_type) | 📐 | Pattern documented; depends on provisioning-service + environment-controller. |
| **API** (REST + GraphQL) | 🚧 | catalyst-api REST surfaces shipped for provisioning, BSS, voucher, sandbox; GraphQL not yet. |
| **kubectl** (debug-only inside own vcluster) | 📐 | Standard K8s; works as soon as a Sovereign exists. |

---

## 6. CI / supply-chain guards (pre-merge)

*(Section dated 2026-05-20.)*

Every guard listed here is a pre-merge check that fails the PR if violated. This is the structural defence against the anti-patterns in [`PRINCIPLES.md`](PRINCIPLES.md).

| Guard | Status | PR | Rule | Catches |
|---|---|---|---|---|
| Hollow-chart guard | ✅ SHIPPED | [#2087](https://github.com/openova-io/openova/pull/2087) (elevated from post-merge to pre-merge, Refs #2080) | Every changed `chart/Chart.yaml` MUST EITHER declare non-empty `dependencies:` OR carry annotation `catalyst.openova.io/no-upstream: "true"` | Hollow wrapper charts (overlay templates only, no upstream payload) — three real recurrences pre-2026-05-20 each dead-reserved a chart version |
| Smoke-render guard | ✅ SHIPPED | [#2093](https://github.com/openova-io/openova/pull/2093) (elevated from post-merge to pre-merge) | `helm template` with default values must produce ≥5 lines OR chart must carry `catalyst.openova.io/smoke-render-mode: "default-off"` | Dual-annotation gap — bp-network-policies:1.0.1 dead-reserve incident (2026-05-20) had `no-upstream:true` but missing `smoke-render-mode:default-off` |
| No-auto-close-keyword guard | ✅ SHIPPED | [#2082](https://github.com/openova-io/openova/pull/2082) | Reject PR bodies containing `Closes #N` / `Fixes #N` / `Resolves #N` unless the PR carries label `ci-gate-exception` | Anti-theater: `Refs #N` is the default; auto-close on PR merge is the enemy. Issue closes only after operator-walk-with-screenshot. |
| Observability-toggle test | ✅ SHIPPED | (per-chart `tests/observability-toggle.sh`) | Default render produces zero `monitoring.coreos.com/v1` references; opt-in render succeeds AND produces a ServiceMonitor; explicit-off succeeds AND produces zero references | Regressions re-introducing hardcoded `enabled: true` — verified failure mode on omantel.omani.works 2026-04-29 (issue #182) |
| Subchart guards (4-step) | ✅ SHIPPED | (`.github/workflows/blueprint-release.yaml`) | After `helm dependency build` / `helm package` / `helm push` / `helm pull`: every declared subchart must be physically present at each layer | Per-layer dropouts: dependency-build skip, packaging strip, registry path mangling, OCI manifest rewrite |
| Flux version-pin replay | ✅ SHIPPED | (`platform/flux/chart/tests/version-pin-replay.sh`) | cloud-init Flux URL pin + bp-flux umbrella `flux2` subchart `appVersion` must match | Catastrophic bp-flux double-install (2026-04-29 omantel incident — Flux controllers deleted by Helm rollback) |

---

## 7. Sovereigns running today

*(Section re-stamped 2026-07-14. Env names below are a snapshot — re-derive the live inventory from the owner-scoped deployments API + the highest `uat-hw<NNN>` branch before acting on any row.)*

| Sovereign | Status | Notes |
|---|---|---|
| `openova` Catalyst-Zero (mothership) | 🚧 | Serves `console.openova.io` (namespaces `catalyst`, `sme`, `marketplace`, `website`; pods include catalyst-{ui,api}, console, admin, marketplace, marketplace-api). The mothership IS the catalyst-provisioner that provisions every other Sovereign — canonical lifecycle API: owner-scoped `POST /sovereign/api/v1/deployments` (create) / `POST .../deployments/{id}/wipe` (destroy). The 2026-05 "Contabo k3s" substrate claim is superseded; re-derive the mothership's live substrate from the deployments API + cluster before relying on it. |
| Test/walk Sovereigns (`hw<NNN>` series) | 🚧 | Convention: `hw<NNN>.omani.works` ↔ `hw<NNN>.omantel.biz` (TLD rotation when LE-rate-limited), provisioned on **Huawei kom4dc** — the delivery substrate: 2-region `me-east-215-a`/`-b`, one VPC per region (quota 5 total), EVS 400-volume cap, ~15-min wipe-release lag ([`docs/runbooks/preflight-sovereign-provision.md`](runbooks/preflight-sovereign-provision.md)). Fired per release-train, never for one passenger. Latest walked env = the highest `uat-hw<NNN>` branch (hw250 as of 2026-07-14; furthest cutover ever = hw246). Forbidden test domains unchanged: `openova.io`, `omantel.openova.io`, `Nova Cloud`, `eventforge.io`. |
| `omantel` production Sovereign (`omantel.biz`) | ✅ | **PERMANENT production Sovereign, live on Huawei kom4dc** — hw240 as of this stamp (re-verify against the live deployments API before relying on the name; production was destroyed twice by in-band quota reclaim — #4614, #4675 dep 2c3f7c34). On the never-touch protect-list: never a wipe target, never a cutover re-fire target, never an in-band reclaim victim. (The only other founder-protected shared resource is the bastion node `bastion-openova`, EIP 212.72.24.20.) |
| Customer-hosted Sovereigns | 📐 | Customers run their own Sovereigns under their own private agreements. Partner identities are intentionally not surfaced in this public catalog. |

---

## 8. Catalyst provisioner

*(Section re-stamped 2026-07-14.)*

| Item | Status | Notes |
|---|---|---|
| `catalyst-provisioner.<mothership-fqdn>` always-on service | ✅ | Live at `console.openova.io` — the mothership IS the catalyst-provisioner. Real Go provisioning code lives at [`products/catalyst/bootstrap/api/internal/provisioner/`](../products/catalyst/bootstrap/api/internal/provisioner/) — a thin wrapper around `tofu` that writes `tofu.auto.tfvars.json` from wizard input, runs `tofu init && tofu plan && tofu apply` against [`infra/providers/`](../infra/providers/)`<provider>/` (**Huawei kom4dc = delivery target**; Hetzner retained), and streams events back to the wizard via SSE. Per Inviolable Principle #3, no cloud APIs called from Go code; OpenTofu does Phase 0, Crossplane adopts day-2 at Phase 1 hand-off. End-to-end zero-touch provisioning **through handover is proven repeatedly** on the hw2xx series (e.g. hw235/hw236 conclusion provs, 2026-07-10); the outstanding keystone is zero-touch `cutoverComplete=true` (§9 Pillar 5). |
| Provider OpenTofu modules | ✅ | Provider-split layout at [`infra/providers/`](../infra/providers/): `huawei/` (**delivery target** — kom4dc HCS, region `me-east-215`, fake-regions `me-east-215-a`/`-b` via per-region VPC isolation), `hetzner/` (retained — VPC + subnet + firewall + servers + LB + DNS), `_shared/` (`cloudinit-control-plane.tftpl` installs k3s and bootstraps Flux pointing at `clusters/<sovereign-fqdn>/`). Provider contract: [`infra/providers/PROVIDER-INTERFACE.md`](../infra/providers/PROVIDER-INTERFACE.md). All values are runtime variables — no hardcoded region, sizes, or k3s flags. |
| Bootstrap kit (cilium → cert-manager → flux → crossplane → sealed-secrets → nats-jetstream → openbao → keycloak → gitea → powerdns → bp-catalyst-platform) | 🚧 | G2 wrapper Helm charts exist under `platform/<x>/chart/`. Each carries blueprint.yaml, values.yaml, Chart.yaml, published as `bp-<name>:<semver>` OCI artifact. `platform/spire/` retained as opt-in but NOT in the bootstrap chain (PR #665, 2026-05-03). Steady-state DoD pending real Hetzner provisioning (Group M). |
| `bp-continuum` (Pillar 3 failover orchestrator) | 🟦 | Wired into bootstrap-kit by PR #2072. Used by Continuum CR to coordinate multi-region failover with lease witness. |
| `bp-self-sovereign-cutover` (Pillar 5 sovereignty cutover) | 🚧 | Dormant slot 06a. **11-step** post-handover chain at chart **0.1.126** — full step map + keystone state in the §2.2 row (furthest ever hw246, died at the step-8 pre-hold ref-host lint; #5038 redirect-aware ruling merged 2026-07-12, unproven live). ADR-0002's "eight sequential Jobs" wording is superseded by the shipped chain. |
| Per-Org Keycloak realm + 2-hop SSO federation to sovereign realm (G117.5 W2.C4 [#2744](https://github.com/openova-io/openova/issues/2744)) | 🟦 CODE-COMPLETE | bp-keycloak 1.4.12 ships `templates/configmap-per-org-realms.yaml` rendering one ConfigMap per `.Values.tenantRealms[]` entry, each carrying a full per-Org realm-import JSON with `sovereign-broker` keycloak-oidc IdP federating into the sovereign realm + IDR `defaultProvider=sovereign-broker` binding (no KC login form) + `first broker login auto link` flow (G113 Bug-6 carryover, bypasses SMTP) + `groups` clientScope (G91 manual recovery mandate). New `bp-keycloak.tenantBrokerClientSecret` helper derives per-Org broker secrets deterministically. bp-sso-bridge 0.2.2 adds `provision_org_realm` + `reconcile_per_org_realms` shell functions in the reconcile loop: discovers ConfigMaps with `catalyst.openova.io/per-org-realm=true`, POSTs `/admin/realms` (idempotent on 409), POST/PUTs matching `<slug>-broker` Client in sovereign realm with same secret, persists in OpenBao `kv/org/<slug>/keycloak/sovereign-broker-secret`. Cross-Org isolation invariant: each realm issues id_tokens with distinct `iss=https://auth.<sov>/realms/<slug>` so Org-A tokens are rejected at OIDC discovery by Org-B applications. 11 chart-test cases + 12 reconciler-shape test cases all green. Playwright `tests/e2e/playwright/tests/g117-2hop-sso-cross-org-isolation.spec.ts` (5 tests, SOV_FQDN+ORG_A+ORG_B-gated) awaits fresh prov with ≥2 Orgs for ✅. |
| Tier-1 silent SSO across grafana / gitea / harbor / openbao (G117.5 [#2744](https://github.com/openova-io/openova/issues/2744)) | 🟦 CODE-COMPLETE | bp-keycloak 1.4.10 codifies all 4 OIDC clients in the sovereign realm-import with stable derived secrets via `bp-keycloak.tier1ClientSecret` helper. bp-grafana 1.0.5 / bp-gitea 1.2.13 / bp-harbor 1.2.23 wire `kc_idp_hint=catalyst-pin` defense-in-depth at the per-app layer (Grafana env-override, Gitea `--use-custom-url-mapping`, Harbor `oidc_extra_redirect_parms`). bp-openbao 1.2.23 omits the hint (architectural limitation: hashicorp/cap library has no auth_url query-param knob); silent SSO for openbao is delivered EXCLUSIVELY by the realm-config IDR `defaultProvider` binding shipped in 1.4.9. Awaits fresh-prov 4-hop curl walk (`tests/e2e/playwright/tests/g117-5-silent-sso-tier1.spec.ts` gated on SOV_FQDN) for ✅. |
| Grafana fresh-install data population (G115 [#2744](https://github.com/openova-io/openova/issues/2744)) | 🟦 CODE-COMPLETE | bp-grafana 1.0.5 ships `templates/datasource-configmaps.yaml` (Prometheus/Loki/Tempo via canonical in-cluster Service DNS) + `templates/dashboard-configmaps.yaml` (5-dashboard starter pack: cluster-overview, node-exporter, kube-state, openbao-audit, flux-reconcile-health). `grafana.sidecar.{datasources,dashboards}.enabled: true` so the upstream kiwigrid/k8s-sidecar auto-discovers cluster-wide. Per-Sovereign overlay may opt-out individual datasources/dashboards via `observabilityStack.*` knobs. Awaits fresh-prov walk (Playwright spec asserts `/api/datasources` returns ≥3 entries) for ✅. |
| Per-Org IaC repo bootstrap on Org create (G117.3 / W2.C3 [#2742](https://github.com/openova-io/openova/issues/2742) + G117.3b [#2765](https://github.com/openova-io/openova/issues/2765)) | 🟦 CODE-COMPLETE | ADR-0009 codified end-to-end in `core/controllers/organization/internal/iacbootstrap/` (Bootstrap + Teardown + RotateRobotToken + OpenBaoStore). organization-controller's Reconcile loop now adds the `orgs.openova.io/iac-bootstrap` finalizer on first observation, runs the 6-step bootstrap (Org → repo → tree → robot user → collaborator → branch protection on main with locked status checks), persists the freshly-minted plaintext token to OpenBao at `kv/org/<slug>/iac-bot-token` (G117.3b), surfaces `status.iacBootstrap{state, repoURL, robotUsername, lastError}` + emits an `IacRepoBootstrapped` Condition. Finalizer reverses provisioning in order (branch-protection → collaborator → repo → token → user → OpenBao path). Gitea client extended with `CreateAdminUser` / `CreateUserAccessToken` / `AddCollaborator` / `EnsureBranchProtection` + deletes. 50+ unit tests across `core/controllers/pkg/gitea` + `core/controllers/organization/internal/iacbootstrap` + `core/controllers/organization/internal/controller`. OpenAPI doc extended with `/orgs/{org}/iac-bootstrap` (informational — describes the controller's behavior). Awaits fresh-prov walk for ✅. |

---

## 9. Pillar status (5-pillar DoD — per [`DOD.md`](DOD.md))

*(Section re-stamped 2026-07-14.)*

| Pillar | Status | Anchoring evidence |
|---|---|---|
| **Pillar 1** — Marketplace + voucher onboarding | 🟦 CODE-COMPLETE | configSchema fields render on AppDetail (PR [#2038](https://github.com/openova-io/openova/pull/2038), TBD-V18); form values thread into install POST (PR [#2043](https://github.com/openova-io/openova/pull/2043), TBD-V18-D). Awaits fresh-prov walk for ✅. |
| **Pillar 2** — Multi-region BCP topology choice at signup | 🟦 CODE-COMPLETE | Wizard surfaces region picker + BCP mode (PR [#2029](https://github.com/openova-io/openova/pull/2029), TBD-V20). Awaits fresh-prov walk for ✅. |
| **Pillar 3** — Two independent CNPG clusters + region-kill failover (zero-tx-loss) | 🟦 CODE-COMPLETE | bp-cnpg-pair synchronous replication via `remote_apply` (PR [#2071](https://github.com/openova-io/openova/pull/2071)); bp-continuum wired (PR [#2072](https://github.com/openova-io/openova/pull/2072)); provisioning generalised beyond WP-only (PR [#2073](https://github.com/openova-io/openova/pull/2073)); the `SMETenantGitOpsWriter` (Go code path) emits a Continuum CR per multi-region Organization Application (PR [#2074](https://github.com/openova-io/openova/pull/2074)); D31 acceptance test harness (PR [#2075](https://github.com/openova-io/openova/pull/2075)); AppConfigs thread into rendered manifests (PR [#2053](https://github.com/openova-io/openova/pull/2053), TBD-V27). Awaits fresh-prov region-kill walk for ✅. |
| **Pillar 4** — Agenity + `bp-openova-mcp` (re-scoped; Sandbox surface REMOVED from pillar scope) | 🟦 CODE-COMPLETE | `bp-agenity` (chart 0.5.20) + `openova-mcp` RBAC-scoped MCP facade over live catalyst-api; the pillar walk is an application provisioned end-to-end THROUGH Agenity. Proven live twice (dep 91dc0591, 2026-06-28; zero-touch hw220, 2026-07-03). Blocked-on-founder: durable per-Sovereign Anthropic credential + credential-propagation design (#4111/#4277). The walk fires on ANY converged env — it is never keystone-gated. Legacy Sandbox CRD/controller: internal machinery, exempt (§4). |
| **Pillar 5** — Sovereign independence post-`bp-self-sovereign-cutover` | 🚧 | **11-step chain** at chart **0.1.126** + 10-min deny-egress hold against `github.com`/`ghcr.io`/`harbor.openova.io`. **Keystone — zero-touch `cutoverComplete=true` on a fresh 2-region Huawei kom4dc prov — is PENDING**: furthest ever hw246 (green through step-7, died at the step-8 pre-hold ref-host lint); the redirect-aware ruling merged as #5038 (2026-07-12) and awaits (1) a converged-env cutover re-fire proving it live, then (2) the fresh-prov zero-touch keystone walk for ✅. |

The 5 pillars are inseparable — DoD claim requires all 5 walked on the same fresh prov with screenshot + non-empty wire-capture + working downstream artifact.

Live per-row walk state lives in [`docs/ledger/UAT.md`](ledger/UAT.md) (~281 rows, all row-ID formats counted; best-ever 67% on hw241; latest conclusion walks hw235/hw236 on 2026-07-10, reset pending hw250) and [`docs/ledger/TRUST.md`](ledger/TRUST.md). The "Anchoring evidence" column above records the code-complete genealogy — the ledgers, not this table, carry the current walk stamps.

---

## 10. What this means for newcomers

If you're reading the Catalyst architecture for the first time:

- The **architectural model** in [`ARCHITECTURE.md`](ARCHITECTURE.md) is the agreed direction. The model is settled.
- The **code in this repo** is mostly scaffold + 4 CODE-COMPLETE pillars. Significant ✅-flipping (operator walks on fresh prov) lies ahead.
- The **7 canonical docs** ([`GLOSSARY.md`](GLOSSARY.md), `STATUS.md` (this file), [`ARCHITECTURE.md`](ARCHITECTURE.md), [`DOD.md`](DOD.md), [`PRINCIPLES.md`](PRINCIPLES.md), [`RUNBOOKS.md`](RUNBOOKS.md), [`SECURITY.md`](SECURITY.md)) describe the **target**.
- Component-level READMEs under `platform/<name>/` describe the upstream technology and Catalyst's intended use of it.

If a doc says "Catalyst does X" without a 📐 / 🚧 / 🟦 marker, treat it as a target. Use this `STATUS.md` to confirm whether X is built today.

---

## 11. How to update this file

This file is updated whenever a status changes:

- A controller is implemented + tested → flip from 📐 to 🟦 (CODE-COMPLETE)
- A pillar walks GREEN on a fresh prov with screenshot evidence → flip from 🟦 to ✅
- A component is partially shipped → 🚧 with notes on what's missing
- A target is deferred → ⏸ with a forward-pointing reference

Per [`DOD.md`](DOD.md): 🟦 means "all controllers + CRDs + tests landed". It is the **maximum** state achievable from code review alone. ✅ requires the operator walk.

Keeping this honest is the only way to prevent the kind of doc/code drift that makes the architecture text unreliable.

### Re-stamp cadence (added 2026-07-14)

This file self-declares that it "wins" over every other doc — yet it once sat frozen at *Updated: 2026-05-20* for ~8 weeks while the platform moved to the Huawei kom4dc substrate, the 11-step cutover chain, and the Agenity Pillar-4 re-scope. A stale winner is worse than no winner. Standing rules:

1. **Weekly re-stamp check**: [`docs/ledger/TRACKER.md`](ledger/TRACKER.md) must carry a weekly checklist row asserting `STATUS.md re-stamped ≤7 days` — the tracker refresh script greps this file's `**Updated:**` date and flips the row red past 7 days (wire this into the DoD-dashboard renderer when it moves into `scripts/`; until then the row is maintained by hand in the weekly sweep).
2. **Same-commit date bump**: any PR that changes a status in this file updates the header `**Updated:**` date in the same commit.
3. **>14 days = precedence void**: per the header rule, a stamp older than 14 days suspends this file's "wins over other docs" clause. A session that finds it stale must re-derive live state first (current `uat-hw<NNN>` branch, `gh issue list`/`gh pr list`, the owner-scoped deployments API, `docs/ledger/` heads) and re-stamp this file in the same session.
