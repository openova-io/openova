# bp-chargeback — chart design

**What this is**: the install path for the chargeback application (ADR-0014,
EPIC #6723) — a standalone application that meters cloud usage per customer,
rates it against a price book and produces statements. One binary (Go service +
embedded UI), its own CNPG database, its own API and screens. It is NOT a
control-plane component of Catalyst; it is a Blueprint like `bp-agenity` or
`bp-openova-mcp`, and its OpenOva integration is an adapter, not a dependency.

## Composable structure (how it plugs into the console)

OpenOva is composed of applications; the sovereign console's left menu binds to
them the same way `bp-agenity` does. `bp-chargeback` declares
`spec.consoleUI.{sidebarEntry,sidebarLabel,sidebarRoute,sidebarIcon}` in its
`blueprint.yaml`; the catalyst-api projects that into
`GET /api/v1/sovereigns/{id}/console-ui/sidebar-entries`, the sovereign console
renders it, and Settings → **Menu** lets the sovereign-admin re-map it
(PR #6724). No console code change is needed to add or move the entry.

| Concern | Where it is decided |
|---|---|
| Composability + left-menu binding | ADR-0014 D9/D9a + `Blueprint.spec.consoleUI` + Settings → Menu |
| Entity types per deployment mode | ADR-0014 D2a |
| Billing source-of-truth per customer type (incl. dual-registered) | ADR-0014 D2b |
| Customer intake / import without duplication | ADR-0014 D8a |
| Collection modes (event-driven vs pulled change log vs sampled) | ADR-0014 D3a |
| UI surface per decision, per profile | ADR-0014 D9a |
| Deployment profiles `sovereign` / `operator-central` | ADR-0014 D10 |

## Entity model (no new platform entity type)

Inside the app the billed party is a `Customer` (an app-internal object, like
posts in WordPress — NOT a Catalyst CRD). Per ADR-0014 D2/D2a:

| Deployment mode | What a `Customer` is |
|---|---|
| Sovereign BSS (adapter on) | synced 1:1 from an Organization slug; plus any external cloud project attached as a cost source |
| Standalone tenant (operator-central) | onboarded directly (create / bulk import / self-service invite, D8a); no Organization exists |
| The Sovereign's own footprint | the `platform` customer (case 3) |

Billing source-of-truth per customer type is D2b: an OpenOva-Sovereign-only
customer bills from its own instance; a National-Cloud-only customer bills from
the central instance; a dual-registered customer is two roles (seller on its own
Sovereign, buyer on the operator's central instance) that exchange statements,
never identity — no duplication across instances.

## What the chart renders

- **Deployment** (2 replicas, Guaranteed QoS, distroless numeric-nonroot uid,
  read-only rootfs, `/healthz`+`/readyz` probes) serving the API + embedded UI.
- **CNPG `Cluster`** (private, gated on the CRD) + placeholder-DSN Secret +
  post-install sync Job — the pattern-A database wiring from `bp-newapi`.
- **HTTPRoute** `chargeback.<sovereign-fqdn>` on the Cilium gateway (fail-closed:
  no resolvable host ⇒ no route), + the two CiliumNetworkPolicies with the
  mandatory DNS carve-out.
- **ServiceAccount** — zero RBAC by default. When `adapter.enabled=true`
  (ADR-0014 D5, the `sovereign` profile) a read-only **ClusterRole** is rendered
  (`orgs.openova.io` + `namespaces`/`pods`/`persistentvolumeclaims`,
  get/list/watch — the least-privilege set the collector needs), the SA token is
  mounted, and `ADAPTER_ENABLED` + `BILLING_HOOK_*` env are wired. The credential
  Secrets named by `costSources[].credentialRef` are granted by per-Organization
  Roles the org-gitops emitter writes on the named Secret (`resourceNames get`),
  never a cluster-wide secrets read.

## Deployment profiles

`config.profile` selects `sovereign` (adapter available; syncs Organizations,
runs the platform collector) or `operator-central` (the operator's own central
instance for all its customers; adapter off, customers onboarded directly).
Same chart, same image — the difference is configuration. The engine is
API-first; the UI is one client, so an operator may embed it behind its own
portal instead (ADR-0014 D10).

## Operational notes

- Migrations are embedded Go, applied at startup; the CNPG cluster is backed up
  by barman (Kyverno backup policy exempts `cnpg.io/cluster` PVCs).
- Secrets (per-customer AK/SK) are envelope-encrypted with `APP_ENCRYPTION_KEY`
  and never returned by the API or logged.
- Cutover-safe: image carries the `global.imageRegistry` pivot seam; kit slot
  **13f** ships the HelmRelease.

See [ADR-0014](../../../docs/adr/0014-chargeback-usage-ledger-and-split-deployment.md)
for the full decision record and [`../README.md`](../README.md) for the service
layout.
