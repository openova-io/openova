# bp-dmz-vcluster

Bootstrap-kit Blueprint #54. Provisions the **DMZ vCluster** on every
region (primary AND each secondary) — the **public-fronted vCluster**
and the **inter-region WireGuard hop** per DoD A2.

## Why this exists — DoD A4 + A2

`docs/SOVEREIGN-MULTI-REGION-DOD.md` invariants:

- **A4 (vCluster topology)**: primary = MGMT+DMZ, secondary = DMZ+RTZ.
  The DMZ vCluster is the only vCluster that appears on every region.
- **A2 (inter-region link)**: DMZ WireGuard over PUBLIC IPs. The WG
  endpoint binds the region's public IP from inside the DMZ vCluster.
- **A3 (ClusterMesh on LoadBalancer)**: clustermesh-apiserver Service
  type=LoadBalancer (public IP through the DMZ WG). The DMZ vCluster
  is where the LB endpoint lives.

This Blueprint creates the DMZ vCluster shell. The Cilium Gateway,
WG endpoint, ClusterMesh apiserver Service, and HTTPRoutes are
installed AS WORKLOADS into this vCluster via subsequent slots /
per-Sovereign overlay paths.

## Relationship to `products/dmz-vcluster`

This `platform/bp-dmz-vcluster` is the **bootstrap topology** piece —
it lands on every Sovereign region by design (slot 54).

`products/dmz-vcluster` is a **per-tenant marketplace** piece — a
customer-facing capability operators install via the Sovereign Console
when a customer requests a DMZ for their workloads. The two charts
serve different layers and may coexist; this Blueprint takes priority
for the bootstrap topology.

## Resources rendered (full-ON)

- `Namespace dmz` (catalyst.openova.io/vcluster-role=dmz label)
- `NetworkPolicy default-deny + allowFrom (mgmt, rtz, kube-system, gateway-system) + world-ingress`
- Upstream loft-sh/vcluster 0.20.0 subchart resources under the `dmz`
  namespace with:
  - `nodeSelector: openova.io/region=<region-key>` so the StatefulSet
    pod lands on the region's CP node (so WG endpoint binds the
    correct region's public IP)
  - `local-path` storage class, 5Gi PVC
  - 200m CPU / 384Mi memory request
  - MIRROR-EVERYTHING image: `harbor.openova.io/proxy-ghcr/loft-sh/vcluster:0.20.0`

## See also

- `docs/SOVEREIGN-MULTI-REGION-DOD.md` — A4 + A2 contract
- `infra/hetzner/README.md` lines 50-100 — topology diagram
- `platform/bp-mgmt-vcluster/` — companion (primary-only)
- `platform/bp-rtz-vcluster/` — companion (secondary-only)
- `products/dmz-vcluster/` — separate per-tenant marketplace artifact
- `scripts/expected-bootstrap-deps.yaml` slot 54
