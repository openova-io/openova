# Catalyst Control Plane (`core/`)

The user-facing Catalyst control plane modules. **Status:** Consolidated and deployed on Catalyst-Zero (Contabo k3s) as of Pass 105 (2026-04-28).

> **Read first:** [`docs/PROVISIONING-PLAN.md`](../docs/PROVISIONING-PLAN.md), [`docs/GLOSSARY.md`](../docs/GLOSSARY.md), [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md), [`docs/IMPLEMENTATION-STATUS.md`](../docs/IMPLEMENTATION-STATUS.md).

---

## What this is

The four modules that constitute the Catalyst control plane's user-facing surface, plus the Go backend they share. Each is its own Containerfile-built workload, deployed on every Catalyst Sovereign (starting with Catalyst-Zero on Contabo, and on every franchised Sovereign provisioned thereafter).

| Module | Stack | Purpose | Deployed image |
|---|---|---|---|
| [`console/`](./console/) | Astro + Svelte | Primary user-facing UI. Form / Advanced / IaC editor depths. The Sovereign-provisioning wizard at `/sovereign` (Phase 3) lives here. | `ghcr.io/openova-io/openova/console:<sha>` |
| [`admin/`](./admin/) | Astro + Svelte | Sovereign-admin operations UI. **Includes the canonical voucher / billing / catalog / orders / tenants admin surface** that sovereign-admin uses to issue vouchers to franchised tenants. | `ghcr.io/openova-io/openova/admin:<sha>` |
| [`marketplace/`](./marketplace/) | Astro + Svelte | Public-facing Blueprint card grid (the "App Store"). 5-step `Plan → Apps → Addons → Checkout → Review` flow. | `ghcr.io/openova-io/openova/marketplace:<sha>` |
| [`marketplace-api/`](./marketplace-api/) | Go | Backend API for `marketplace` and `console`. Handlers (`handlers/`), provisioner (`provisioner/`), store (`store/`). Phase 4 extends this with full Hetzner provisioning. | `ghcr.io/openova-io/openova/marketplace-api:<sha>` |

The Helm chart that deploys all four (plus `catalyst-ui`, `catalyst-api`, and the legacy SME backend services) lives at [`products/catalyst/chart/`](../products/catalyst/chart/).

---

## CI / Build

Each module has a corresponding GitHub Actions workflow:

- [`.github/workflows/console-build.yaml`](../.github/workflows/console-build.yaml)
- [`.github/workflows/admin-build.yaml`](../.github/workflows/admin-build.yaml)
- [`.github/workflows/marketplace-build.yaml`](../.github/workflows/marketplace-build.yaml)
- [`.github/workflows/marketplace-api-build.yaml`](../.github/workflows/marketplace-api-build.yaml)
- [`.github/workflows/catalyst-build.yaml`](../.github/workflows/catalyst-build.yaml) — covers `products/catalyst/bootstrap/{ui,api}/` (the React SPA + Go bootstrap API)

Each workflow watches its module path, builds the Containerfile, pushes to GHCR with a SHA tag, and pins the SHA into the corresponding manifest in `products/catalyst/chart/templates/` (so Flux on Catalyst-Zero picks up the new image on the next reconciliation).

---

## Migration history

- **Pass 105 (2026-04-28)**: `console/`, `admin/`, `marketplace/` consolidated from `openova-private/apps/{console,admin,marketplace}/` into this directory. `marketplace-api/` consolidated from `openova-private/website/marketplace-api/`. Six CI workflows migrated to `.github/workflows/` of the public repo. Catalyst-Zero K8s manifests migrated from `openova-private/clusters/contabo-mkt/apps/{catalyst,sme/services,marketplace-api}/` into `products/catalyst/chart/templates/`. Image references updated from `ghcr.io/openova-io/openova-private/sme-{admin,console,marketplace}` to `ghcr.io/openova-io/openova/{admin,console,marketplace}`. The 8 legacy SME backend services (`auth`, `billing`, `catalog`, `domain`, `gateway`, `notification`, `provisioning`, `tenant`) keep their `openova-private/sme-*` image refs until their source code migrates in a follow-up phase.

---

*Part of [OpenOva](https://openova.io)*
