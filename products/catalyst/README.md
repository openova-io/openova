# OpenOva Catalyst (composite Blueprint)

The umbrella Blueprint `bp-catalyst-platform` — composes the Catalyst control plane.

**Status:** Deployed. **Updated:** 2026-04-28.

This product directory contains:

- `chart/` — the Helm chart that deploys Catalyst-Zero on a Kubernetes cluster (and every franchised Sovereign).
- `chart/templates/{ui,api}-deployment.yaml` + service + ingress — the catalyst-ui (React SPA wizard scaffold) and catalyst-api (Go bootstrap API) workloads.
- `chart/templates/sme-services/` — 11 manifests for the legacy SME backend services + the consolidated `console`, `admin`, `marketplace` UI workloads (sourced from `core/{console,admin,marketplace}/`).
- `chart/templates/marketplace-api/` — manifests for the Go marketplace-api backend (sourced from `core/marketplace-api/`).
- `bootstrap/{ui,api}/` — the source code for catalyst-ui and catalyst-api (deployed via the catalyst-build CI workflow).

For the unified architecture and the wizard's target shape, see [`docs/PROVISIONING-PLAN.md`](../../docs/PROVISIONING-PLAN.md), [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md), and [`docs/SOVEREIGN-PROVISIONING.md`](../../docs/SOVEREIGN-PROVISIONING.md).

---

## How Catalyst-Zero is deployed today

A Flux Kustomization on the Catalyst-Zero cluster (Contabo k3s) reconciles `products/catalyst/chart/templates/` from this public repo. CI workflows (`.github/workflows/{catalyst,console,admin,marketplace,marketplace-api}-build.yaml`) build and push images on every push to `main`, then the deploy step pins the image SHA into the corresponding manifest in this directory and commits back. Flux picks up the commit and rolls the deployment.

Image registry: `ghcr.io/openova-io/openova/{catalyst-ui,catalyst-api,console,admin,marketplace,marketplace-api}:<sha>`.

## Migration status (per `docs/PROVISIONING-PLAN.md`)

| Component | Source location | Image | Status |
|---|---|---|---|
| catalyst-ui | `products/catalyst/bootstrap/ui/` | `ghcr.io/openova-io/openova/catalyst-ui` | ✅ public repo |
| catalyst-api | `products/catalyst/bootstrap/api/` | `ghcr.io/openova-io/openova/catalyst-api` | ✅ public repo |
| console | `core/console/` | `ghcr.io/openova-io/openova/console` | ✅ public repo (Phase 1) |
| admin | `core/admin/` | `ghcr.io/openova-io/openova/admin` | ✅ public repo (Phase 1) |
| marketplace | `core/marketplace/` | `ghcr.io/openova-io/openova/marketplace` | ✅ public repo (Phase 1) |
| marketplace-api | `core/marketplace-api/` | `ghcr.io/openova-io/openova/marketplace-api` | ✅ public repo (Phase 1) |
| sme-{auth,billing,catalog,domain,gateway,notification,provisioning,tenant} | (still in openova-private/services/) | `ghcr.io/openova-io/openova-private/sme-*` | ⏳ follow-up phase — source not yet moved |

