# Catalyst-Zero Provisioning Plan

**Status:** Authoritative working plan — **execution underway**. **Updated:** 2026-04-29.
**Owner:** OpenOva engineering. **Parent issue:** [#43](https://github.com/openova-io/openova/issues/43). **Sub-tickets:** A–M groups, [#45–#175](https://github.com/openova-io/openova/issues?q=is%3Aopen+%5B+). Post-Group-M continuation tickets (#161, #162, #163, #167, #168, #169, #170, #171, #173, #174, #175) extend the plan with the per-Sovereign PowerDNS zone model, pool-domain-manager + registrar adapters, three-mode StepDomain (pool/byo-manual/byo-api), the wizard StepComponents redesign, and k8gb retirement.

---

## Execution status (live)

| Group | Tickets | Status | Commits |
|---|---|---|---|
| A — Code consolidation | 9 | ✅ Done | 3c2f7e4 |
| B — SME backend services | 10 | ✅ Source migrated; CI workflow live | 7646840 |
| C — Cutover Catalyst-Zero | 8 | ✅ Flux is now reconciling Catalyst-Zero from `github.com/openova-io/openova` (public repo) — confirmed via `kubectl get gitrepository -A` returning `openova-public` source serving the catalyst-platform Kustomization | 9d93912, dc56854, bd967a7, 61de3da, 9fdfe07, 8c40984 (Group C cutover merge) |
| D — Wizard | 10 | 🚧 Domain capture + Hetzner project ID added; AppsStep replacement pending | 854a063 |
| E — Provisioner backend | 13 | 🚧 Real Hetzner client + bootstrap installer + Dynadot DNS landed; SSH kubeconfig fetch is stub | 915c467, db4f21a, 07b4bcf |
| F — Bootstrap-kit Helm charts | 14 | ✅ All 12 G2 wrapper charts (original 11 + bp-powerdns #167) + blueprint-release CI live | 8c0f766, 0190c605 |
| G — DNS multi-domain | 6 | ✅ Superseded by PowerDNS authoritative (#167) + pool-domain-manager (#163) + registrar adapters (#170) — Dynadot is now one of five registrar adapters inside PDM, not the authoritative DNS surface | db4f21a, 0190c605 (#167), 2854d652 (#163), 567d7e1f (#170) |
| H — Franchise model | 7 | 🚧 docs/FRANCHISE-MODEL.md authored from existing admin impl; cross-Sovereign voucher deferred | this commit |
| I — Wizard UX | 6 | 📐 SSE event log pane + step indicator pending |  |
| J — Hetzner infra | 6 | 🚧 cloud-init in repo; firewall + k3s flags wired into provisioner | 07b4bcf |
| K — Documentation | 8 | 🚧 IMPLEMENTATION-STATUS + core/README + products/catalyst/README updated; component-count anchor refreshed 53 → 56 (spire + nats-jetstream + sealed-secrets factored in); reconcile-pass-1 (2026-04-29) refreshed canonical docs against PowerDNS/PDM/registrar-adapter ground truth | 3c2f7e4, 8c0f766, group-k-docs, reconcile-pass-1 |
| L — Testing | 8 | 📐 Playwright + integration tests pending |  |
| M — End-to-end DoD | 9 | 📐 Awaiting Hetzner credentials from operator + first OCI-artifact CI runs to complete |  |

---



This document captures the agreed plan for consolidating the existing nova/console/admin/marketplace code into the public OpenOva Catalyst monorepo, deploying it as **Catalyst-Zero** (the first Catalyst Sovereign — running on Contabo, the chicken in the chicken-and-egg problem), and then provisioning the first **franchised Sovereign** on Hetzner via the wizard at `console.openova.io/sovereign`.

This plan is the canonical reference. It supersedes any session-local conversation. Future Claude sessions or new contributors should read this first.

---

## 1. The chicken-and-egg problem and its resolution

Catalyst is a Kubernetes-native control plane that provisions other Sovereigns. Provisioning a Sovereign requires a **provisioner service** (`catalyst-provisioner.openova.io` per `SOVEREIGN-PROVISIONING.md` §2). That provisioner has to **run somewhere**. It cannot run inside the Sovereign it is provisioning (chicken-and-egg).

**Resolution:** the legacy nova/console/admin/marketplace stack currently running on **Contabo k3s** (in namespaces `catalyst`, `sme`, `marketplace`, `website`) is **Catalyst-Zero** — the first Sovereign. It exists today, has running pods today, and is the chicken from which the egg (the first Hetzner-hosted franchised Sovereign) gets provisioned.

The work in this plan **consolidates** that existing code into the public repo, **redeploys** it as a public-repo build (CI from `github.com/openova-io/openova`), and then **uses it** to provision the first franchised Sovereign. There is no greenfield "build Catalyst from scratch" — the Sovereign already exists; we are aligning it to the canonical Catalyst contract.

---

## 2. Current state inventory (verified against live cluster + repos, 2026-04-28)

### 2.1 Code locations (today)

| What | Where today | Where it must end up |
|---|---|---|
| Catalyst console UI (Astro+Svelte) | `openova-private/apps/console/` | `openova/core/console/` |
| Catalyst admin UI (Astro+Svelte) | `openova-private/apps/admin/` | `openova/core/admin/` |
| Catalyst marketplace UI (Astro+Svelte) | `openova-private/apps/marketplace/` | `openova/core/marketplace/` |
| marketplace-api (Go backend) | `openova-private/website/marketplace-api/` | `openova/core/marketplace-api/` |
| Catalyst-zero deployment chart | `openova-private/clusters/contabo-mkt/apps/catalyst/` | `openova/products/catalyst/chart/templates/` |
| Vite scaffold for sovereign-wizard | `openova/products/catalyst/bootstrap/ui/` | merges into `openova/core/console/src/pages/sovereign/` |
| CI workflows (6 of them) | `openova-private/.github/workflows/{catalyst-build,marketplace-api-build,sme-{admin,console,marketplace,services}-build}.yaml` | `openova/.github/workflows/` |
| Voucher / billing / tenants admin surface | `openova-private/apps/admin/src/{components/BillingPage.svelte, lib/api.ts, pages/{billing,catalog,orders,tenants}.astro}` | `openova/core/admin/...` (carry forward unchanged) |

### 2.2 Live deployment on Contabo (verified via `kubectl get all -A`)

| Namespace | Pods running | Notes |
|---|---|---|
| `catalyst` | catalyst-api + catalyst-ui | 39 days uptime |
| `sme` | console + admin + marketplace | 5–6 days uptime |
| `marketplace` | marketplace-api | 13 days uptime |
| `website` | openova-website | live |

These pods are Catalyst-Zero. They stay running through Phases 1–2; Phase 2 is a rolling-update cutover to public-repo image builds.

### 2.3 Existing 5-step wizard (the "Components (5)" page reference)

The "Components (5)" the user referenced is the 5-step marketplace flow at `openova-private/apps/marketplace/src/components/`:

```
PlanStep → AppsStep → AddonsStep → CheckoutStep → ReviewStep
```

`AppsStep` is what gets replaced with the unified marketplace card grid (driven by the same `bp-<x>` Blueprint surface every Catalyst Sovereign uses).

### 2.4 Voucher mechanism (already implemented)

Lives in `openova-private/apps/admin/`:
- `src/components/BillingPage.svelte` — voucher / billing UI
- `src/lib/api.ts` — voucher API client
- `src/pages/{billing,catalog,orders,tenants}.astro` — admin pages

This is the **canonical** voucher implementation. Do not redesign. Read what's there, propagate to franchised Sovereigns, document in `docs/FRANCHISE-MODEL.md`.

---

## 3. Architectural agreements (from the design conversation, durable)

These agreements survive any context compaction and apply to every phase of the work below.

1. **Catalyst-Zero is the existing Contabo deployment.** Not greenfield. The work is consolidate + cutover + extend, not rebuild.
2. **omani.works is the first Sovereign-provided subdomain pool** (registered to the OpenOva Dynadot account). User dynamically picks `omantel.omani.works` during provisioning. The wizard offers BYO domain (customer's own) or a Sovereign-pool subdomain (default). Multi-region setups are out of scope for the first run.
3. **Existing admin voucher implementation is the source of truth.** Do not propose new CRDs. Read the existing implementation, propagate it to franchised Sovereigns, document it.
4. **G2 quality only.** Catalyst-curated wrapper Helm charts at `platform/<x>/chart/` for every component in the bootstrap kit. No upstream-as-is shortcuts. No corner-cutting. The unified Blueprint contract from `BLUEPRINT-AUTHORING.md` §1 is the standard.
5. **No mocks. No iterations. No partial deliveries.** Waterfall — every phase produces real, deployed, working artifacts.
6. **All product code is public.** Per the build-minutes constraint, code moves to `openova/` (the public monorepo) before any further development. CI runs in the public repo from this point onward.
7. **The Vite scaffold at `products/catalyst/bootstrap/ui/`** merges into `core/console/src/pages/sovereign/`. It does not become its own deployable.
8. **Sovereign-provisioning wizard target URL: `console.openova.io/sovereign`.** Captured fields include domain (BYO or pool), Hetzner Cloud API token, Hetzner project ID, Hetzner region (runtime parameter, never hardcoded), plus the marketplace-style App selection.
9. **The Hetzner region is a runtime parameter chosen by the wizard user.** Never hardcoded anywhere in code.
10. **Dynadot is OpenOva's registrar of record for the pool domains.** The `dynadot-api-credentials` K8s secret in `openova-system` is account-scoped and covers `openova.io` plus `omani.works` (and any other domain in the same Dynadot account). Post-#167/#170 Dynadot is **not** authoritative DNS for any Sovereign zone — bp-powerdns is. Dynadot is one of five registrar adapters PDM uses to (a) keep the OpenOva pool domains' parent-zone NS records pointing at OpenOva PowerDNS and (b) honour `byo-api` Sovereigns whose customer happens to use Dynadot.

---

## 4. The 8-phase waterfall

Each phase produces one or more commits to `openova/`. Each commit is real working code, not scaffold. No phase is skipped, abbreviated, or deferred.

### Phase 1 — Code consolidation (openova-private → openova)

**What:** `git mv` the 4 apps (`console`, `admin`, `marketplace`, `marketplace-api`) from openova-private to `openova/core/`. Move 6 CI workflows to `openova/.github/workflows/`. Move Catalyst-Zero deployment manifests from `openova-private/clusters/contabo-mkt/apps/catalyst/` to `openova/products/catalyst/chart/templates/`.

**Outputs:**
- `openova/core/{console,admin,marketplace,marketplace-api}/` populated
- `openova/.github/workflows/{catalyst-build,marketplace-api-build,sme-*-build}.yaml`
- `openova/products/catalyst/chart/templates/{api-deployment,api-service,ui-deployment,ui-service,ingress}.yaml`
- `openova/products/catalyst/chart/Chart.yaml` (new)
- All import paths, image refs (`ghcr.io/openova-io/openova/{console,admin,marketplace,marketplace-api,catalyst-api,catalyst-ui}:<sha>`) updated
- VALIDATION-LOG entry: Pass 105

**Commit message:** `feat(consolidation): move Catalyst-Zero apps + CI from openova-private to public monorepo`

### Phase 2 — Cutover Catalyst-Zero to public-repo build

**What:** Trigger first public-repo CI run, get `:<sha>` images into GHCR, roll the existing Contabo deployment to the new images. Catalyst-Zero is now built from the public repo. Delete legacy paths from `openova-private` (preserved in git history).

**Outputs:**
- GHCR images at `ghcr.io/openova-io/openova/{console,admin,marketplace,marketplace-api,catalyst-api,catalyst-ui}:<sha>`
- Contabo k3s pods rolled to new image SHAs
- `openova-private` cleaned of legacy paths
- VALIDATION-LOG entry: Pass 106

**Commit message:** `infra(cutover): Catalyst-Zero now built from public repo`

**Acceptance:** `kubectl describe pod` on each rolled pod shows `image: ghcr.io/openova-io/openova/...`. Console at `console.openova.io` still loads. Brief rolling-update window (<60s).

### Phase 3 — Sovereign-provisioning wizard

**What:** Build the wizard at `core/console/src/pages/sovereign/` using the Vite scaffold. Replace the legacy 5-step marketplace flow's `AppsStep` with a unified marketplace card grid (driven by `bp-<x>` Blueprint surface). Add Sovereign-provisioning-specific fields:

- Domain: BYO (customer's own domain) **or** pool selection (default `omani.works` → user picks subdomain like `omantel`, `acme-bank`, etc.)
- Hetzner Cloud API token (capture, store via ESO into OpenBao, never log)
- Hetzner project ID
- Hetzner region (dropdown of valid Hetzner regions; runtime parameter)
- Sovereign owner email (becomes initial sovereign-admin)
- Initial App selection (the unified marketplace grid)

**Outputs:**
- `openova/core/console/src/pages/sovereign/index.astro` + sub-pages for each wizard step
- `openova/core/console/src/components/sovereign/{DomainStep,HetznerStep,AppsStep-unified,ReviewStep}.svelte`
- The legacy bootstrap Vite scaffold at `openova/products/catalyst/bootstrap/ui/` is merged in and the directory deleted (its content is now part of `core/console/`)
- VALIDATION-LOG entry: Pass 107

**Commit message:** `feat(console): sovereign-provisioning wizard at /sovereign with domain + Hetzner inputs + unified marketplace App selection`

### Phase 4 — Provisioner backend

**What:** Build the wizard's backend at [`products/catalyst/bootstrap/api/`](../products/catalyst/bootstrap/api/) (the Go service deployed as `catalyst-api` in the `catalyst` namespace on Catalyst-Zero). Real backend that takes wizard input → calls OpenTofu → returns Sovereign provisioning state via SSE. Per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #3, **no cloud APIs are called from Go directly** — OpenTofu owns Phase 0, Crossplane owns day-2, and Hetzner client code is reserved for read-only credential validation.

**Outputs:**
- [`products/catalyst/bootstrap/api/internal/provisioner/`](../products/catalyst/bootstrap/api/internal/provisioner/) — thin wrapper around `tofu` that writes `tofu.auto.tfvars.json` from validated wizard input, runs `tofu init && tofu plan && tofu apply -auto-approve`, streams stdout/stderr lines to the wizard via SSE
- [`products/catalyst/bootstrap/api/internal/hetzner/`](../products/catalyst/bootstrap/api/internal/hetzner/) — read-only Hetzner client for credential validation (`POST /api/v1/credentials/validate`); never used to mutate cloud state
- [`products/catalyst/bootstrap/api/internal/pdm/`](../products/catalyst/bootstrap/api/internal/pdm/) — PDM client (`/v1/reserve`, `/v1/commit`, `/v1/validate`) for pool-subdomain allocation and registrar-token validation
- [`products/catalyst/bootstrap/api/internal/dynadot/`](../products/catalyst/bootstrap/api/internal/dynadot/) — Dynadot client (used as one registrar adapter inside PDM's adapter set, not for direct DNS writes from this service)
- [`products/catalyst/bootstrap/api/internal/handler/`](../products/catalyst/bootstrap/api/internal/handler/) — REST handlers including `POST /api/v1/deployments`, `GET /api/v1/deployments/{id}/logs` (SSE), `POST /api/v1/deployments/{id}/phases/{phase}/retry`, `POST /api/v1/credentials/validate`, `POST /api/v1/subdomains/check`, `GET /api/v1/registrars`
- [`infra/hetzner/main.tf`](../infra/hetzner/main.tf) — OpenTofu module (network, firewall, ssh-key, control-plane + worker servers, load balancer)
- VALIDATION-LOG entry: Pass 108

**Commit message:** `feat(provisioner): real Hetzner Sovereign provisioning end-to-end`

### Phase 5 — Bootstrap kit Helm charts (G2 quality)

**What:** Real Catalyst-curated wrapper Helm charts at `platform/<x>/chart/` for every bootstrap-kit component. Each chart wraps upstream OSS with Catalyst-specific values, includes a `blueprint.yaml` per the unified Blueprint contract from `BLUEPRINT-AUTHORING.md` §1, publishes a `bp-<name>:<semver>` OCI artifact via CI fan-out.

**Components (in dependency order):**
1. `platform/cilium/chart/` (CNI must come first)
2. `platform/cert-manager/chart/`
3. `platform/flux/chart/` (host-level)
4. `platform/crossplane/chart/`
5. `platform/sealed-secrets/chart/` (transient bootstrap-only)
6. `platform/spire/chart/` (the `platform/spire/` folder may need to be added — workload identity)
7. `platform/nats-jetstream/chart/` (the `platform/nats-jetstream/` folder may need to be added)
8. `platform/openbao/chart/`
9. `platform/keycloak/chart/`
10. `platform/gitea/chart/`
11. `products/catalyst/chart/` — the umbrella `bp-catalyst-platform`

**Outputs:**
- 11 directories with `Chart.yaml`, `values.yaml`, `templates/`, `blueprint.yaml`, optional `compositions/`, `policies/`, `overlays/`
- 11 entries in `openova/.github/workflows/blueprint-release.yaml` (path-matrix CI fan-out)
- 11 OCI artifacts published at `ghcr.io/openova-io/bp-<name>:<semver>` after first CI run
- One commit per chart (11 commits) — incremental review possible
- VALIDATION-LOG entries: Pass 109 through Pass 119

**Commit messages:** `feat(bp-<name>): G2 Catalyst-curated chart for <name> per BLUEPRINT-AUTHORING contract`

### Phase 6 — DNS architecture: PowerDNS authoritative + PDM + registrar adapters

**What:** The DNS architecture has two layers. **Authoritative DNS** lives on bp-powerdns (#167) — every Sovereign zone (pool: `omantel.omani.works`, BYO: `acme.bank.com`) gets its own PowerDNS zone with DNSSEC + lua-records. **Allocation + registrar control** lives on the pool-domain-manager service (#163), which exposes registrar adapters (#170) for byo-api flow:

- **Pool subdomains** (e.g. `<sub>.omani.works`, `<sub>.openova.io`): PDM `/v1/reserve` checks availability, `/v1/commit` creates the per-Sovereign PowerDNS zone, writes the canonical 6-record set, and updates the parent zone's NS delegation via the OpenOva Dynadot registrar adapter.
- **BYO with manual NS-flip** (`byo-manual`): wizard surfaces the OpenOva NS list; customer pastes them into their own registrar UI; catalyst-api polls until propagation; PDM `/v1/commit` then writes the canonical record set into the new PowerDNS zone (no parent-zone change from OpenOva).
- **BYO with API NS-flip** (`byo-api`): customer picks their registrar from the supported list (Cloudflare, Namecheap, GoDaddy, OVH, Dynadot — #170), pastes a token; PDM `/v1/validate` confirms scope read-only; on commit, the matching registrar adapter flips the NS records to OpenOva's NS set.

**Outputs:**
- [`core/pool-domain-manager/`](../core/pool-domain-manager/) — Go service deployed at `pool-domain-manager` in `openova-system`, CNPG-backed `pdm-pg`. Modules: `internal/allocator`, `internal/pdns`, `internal/registrar`, `internal/dynadot`, `internal/reserved`, `internal/store`. CI: [`.github/workflows/pool-domain-manager-build.yaml`](../.github/workflows/pool-domain-manager-build.yaml).
- [`platform/crossplane/compositions/composition-pool-allocation.yaml`](../platform/crossplane/compositions/composition-pool-allocation.yaml) + matching XRD — declarative Crossplane wrapper around PDM `/v1/reserve` so Sovereign provisioning runs through the canonical IaC path.
- [`platform/powerdns/`](../platform/powerdns/) — bp-powerdns wrapper chart (Chart.yaml, values.yaml, blueprint.yaml, templates) with DNSSEC + lua-records on by default, dnsdist companion for rate-limiting.
- VALIDATION-LOG entry: Pass 120 (component-count refresh + PDM landing).

**Commit message:** `feat(dns): bp-powerdns + pool-domain-manager + registrar adapters for pool/byo flows`

### Phase 7 — Franchise model docs + voucher propagation

**What:** Read existing voucher implementation in admin app. Write `docs/FRANCHISE-MODEL.md` documenting it as canonical. Ensure the new Sovereign at `omantel.omani.works` has its own admin surface (the same admin app, deployed inside the Sovereign) where omantel-admin can issue vouchers to omantel's tenants. Update `GLOSSARY.md` with `Voucher` and `Franchisee` definitions if not already present.

**Outputs:**
- `openova/docs/FRANCHISE-MODEL.md` — canonical doc
- Updates to `GLOSSARY.md` if needed
- Updates to `BUSINESS-STRATEGY.md` revenue model if needed
- VALIDATION-LOG entry: Pass 121

**Commit message:** `docs(franchise): canonical franchise model + voucher propagation, sourced from existing admin impl`

### Phase 8 — End-to-end provisioning (live demo / DoD)

**What:** From browser at `console.openova.io/sovereign`:
1. User logs in (Keycloak SSO)
2. Picks "New Sovereign"
3. Pastes Hetzner Cloud API token + project ID, picks region (any — runtime parameter)
4. Picks domain: pool → `omani.works` → user types `omantel` (creates `omantel.omani.works`)
5. Picks initial Apps (unified marketplace selection)
6. Click Provision
7. Watches SSE-driven progress for ~10 minutes
8. Provisioning completes; new Sovereign at `omantel.omani.works` is reachable
9. omantel-admin (initial sovereign-admin) logs into `console.omantel.omani.works`
10. omantel-admin issues 1 voucher
11. A fictional customer redeems the voucher at `omantel.omani.works/redeem?code=...`
12. Customer's Organization + Environment + first App is created on omantel.omani.works
13. Customer reaches their App's URL

**Acceptance:** every step above works without intervention. No mocks, no manual steps beyond the browser clicks.

**Outputs:**
- VALIDATION-LOG entry: Pass 122 — DoD documented with screenshots / kubectl evidence
- Optional: `docs/DEMO-RUNBOOK.md` for repeatability

---

## 5. What this plan does NOT change

- The unified Application = Gitea Repo model (Pass 103) is preserved everywhere. The franchised Sovereign at omantel.omani.works will use the same model — one Gitea Org per Catalyst Organization, one Gitea Repo per Application.
- The 5 conventional Gitea Orgs convention (`catalog`, `catalog-sovereign`, `<org>` per Catalyst Organization, `system`) applies to the new Sovereign exactly as it does to Catalyst-Zero.
- The component-count anchor (Pass 104 set 53; Pass 105 raised it to 56 with spire + nats-jetstream + sealed-secrets) holds. SeaweedFS unified S3 encapsulation, Guacamole in bp-relay, OpenBao independent-Raft per region — all preserved.
- The audit procedure stays on-demand (no scheduled loops). The `audit-catalyst-docs` skill is the only validation entry point.

---

## 6. References

- `docs/ARCHITECTURE.md` — target architecture (the design Catalyst-Zero is being aligned to)
- `docs/SOVEREIGN-PROVISIONING.md` §3 Phase 0 — bootstrap kit dependency order (canonical reference for Phase 5 of this plan)
- `docs/BLUEPRINT-AUTHORING.md` §1 — unified Blueprint shape (the contract Phase 5 charts must satisfy)
- `docs/IMPLEMENTATION-STATUS.md` — gets updated incrementally as each phase lands (📐 → 🚧 → ✅)
- `docs/AUDIT-PROCEDURE.md` — how to validate after each phase
- `docs/VALIDATION-LOG.md` Pass 1–104 — historical record; Pass 105+ tracks this plan's execution

---

*Part of [OpenOva](https://openova.io)*
