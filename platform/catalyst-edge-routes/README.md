# bp-catalyst-edge-routes

Region-b (secondary control-plane) **edge** for a 2-region Catalyst Sovereign.

## Role in Catalyst

Control-plane plumbing — **not** a user-installable Application Blueprint. It is
installed only on the **secondary** CP of a 2-region Sovereign, by bootstrap-kit
slot `13b-catalyst-secondary-edge-routes.yaml`, and it renders **only** Cilium
ClusterMesh global-Service stubs + Gateway-API HTTPRoutes/ReferenceGrant — never
a Deployment, StatefulSet, PVC, or Job.

## Why it exists (issue #5289)

The Catalyst control plane (`catalyst-api` / `catalyst-ui` / `catalyst-catalog`
+ the Organization marketplace stack in `org-services`) is a **single-writer
singleton pinned to region-a**: G2 (#2574) suspends `bp-catalyst-platform` on
every secondary CP, and the api store is on a zone-scoped RWO EVS PVC — a second
`catalyst-api` would multi-attach and split-brain (#5267).

But the #5246 both-region **console ELB** deliberately targets *both* regions'
`cilium-envoy` pods on `:443` so the console EIP survives a region-kill. In
normal 2-region operation that means ~half of `api.` / `console.` /
`marketplace.<fqdn>` requests land on **region-b's** envoy — which, with the
umbrella HR suspended, serves **no** control-plane HTTPRoute and returns **404**.
That is the steady-state `200↔404` flap #5289 root-caused (~6/10 requests 404 on
hw280).

## How it fixes it

Region-b must **serve** those hosts by **proxying** to region-a's single pods
over Cilium ClusterMesh **global Services** — the same idiom `bp-cnpg-pair` /
`bp-openbao` use for cross-region delivery:

1. **region-a** (`bp-catalyst-platform` ≥ 1.4.1190, gated on
   `multiRegion.enabled`) annotates its real control-plane Services with
   `service.cilium.io/global: "true"` + `.../shared: "true"`.
2. **region-b** (this chart) renders same-name+namespace **stub** Services with
   the same annotations and **zero local backends**. Cilium merges the two into
   one global Service, so every region-b dial crosses the mesh to region-a's
   singleton.
3. **region-b** renders the `api.` / `console.` / `marketplace.` HTTPRoutes
   (byte mirrors of the region-a routes) parented onto the same
   `cilium-gateway-console` listener, backendRef'd at the stubs.

No second `catalyst-api`/`catalyst-ui` Deployment, no PVC → the region-a pods
stay the sole writers.

## Configuration knobs

All supplied per-Sovereign by bootstrap-kit slot 13b (defaults render nothing):

| Value | Meaning |
|---|---|
| `ingress.gateway.enabled` | Master gate — no routes/services without it. |
| `ingress.gateway.parentRef.{name,namespace,sectionName}` | The console Gateway (`cilium-gateway-console` in `kube-system`, or `cilium-gateway` on a single-EIP prov). |
| `ingress.hosts.{console,api,marketplace}.host` | `console./api./marketplace.<fqdn>`. |
| `ingress.marketplace.enabled` | Render the marketplace-host proxy (org-services stubs + route + grant). Mirrors slot 13's `MARKETPLACE_ENABLED`. |
| `services.catalog.enabled` | Render the `catalyst-catalog` stub + `api.<fqdn>/api/v1/catalog` route. Mirrors `bp-catalyst-platform`'s `services.catalog.enabled`. |

## Operational notes

- **Region selection**: the slot-13b HelmRelease is un-suspended only on the
  secondary CP via the `SECONDARY_EDGE_SUSPEND` cloud-init var (the symmetric
  inverse of `SECONDARY_HR_SUSPEND`). On the primary CP and on any single-region
  prov the HR is suspended and this chart is inert.
- **Failover semantics**: during a region-a kill the stubs have no healthy mesh
  backend, so region-b returns `503 no-healthy-upstream` for the control-plane
  hosts — but `catalyst-api` is region-a-pinned and down during the kill anyway
  (the data-plane gateway VIP still fails over per #5246). Steady-state (both
  regions up) is `200` from either envoy, which is the #5289 contract.
- **No NodePort** (§854): routing is Gateway-API + ClusterMesh only.

Canonical model: see [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §10.7
(control-plane DR posture) and [`docs/DOD.md`](../../docs/DOD.md) §6.
