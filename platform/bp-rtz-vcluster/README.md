# bp-rtz-vcluster

Bootstrap-kit Blueprint #59. Provisions the **RTZ vCluster** on every
**secondary** region — the regional tenant-workload vCluster.

## Why this exists — DoD A4

`docs/DOD.md` invariant A4:

> primary region = MGMT + DMZ vCluster; each secondary region = DMZ +
> RTZ vCluster. Cross-vCluster intra-region traffic stays inside host
> k3s via Cilium.

This Blueprint implements the RTZ half of the secondary-region pair.

| Region role | vClusters | This chart |
|---|---|---|
| Primary    | MGMT + DMZ                 | not rendered (gated off via SOVEREIGN_REGION_ROLE) |
| Secondary  | DMZ  + RTZ                 | rendered                                            |

## Resources rendered (full-ON)

- `Namespace rtz` (catalyst.openova.io/vcluster-role=rtz label)
- `NetworkPolicy default-deny + allowFrom dmz` (RTZ has NO direct
  MGMT access — primary's MGMT only reachable via DMZ WG cross-region)
- Upstream loft-sh/vcluster 0.20.0 subchart under `rtz` namespace with:
  - `nodeSelector: openova.io/region=<secondary-region-key>` so the
    StatefulSet lands on the region's own CP node
  - `local-path` storage class, 5Gi PVC
  - 200m CPU / 384Mi memory request
  - MIRROR-EVERYTHING image: `harbor.openova.io/proxy-ghcr/loft-sh/vcluster:0.20.0`

## Topology dependency

```text
Phase 0 (cloud-init Hetzner CP per region)
   ↓
bp-cilium             — CNI + Gateway API (slot 01)
   ↓
bp-cert-manager       — TLS for ClusterIssuers (slot 02)
   ↓
bp-mgmt-vcluster      — slot 58 (primary-only)
bp-dmz-vcluster       — slot 54 (every region)
bp-rtz-vcluster       — THIS chart (slot 59, secondary-only)
```

## See also

- `docs/DOD.md` — A4 contract
- `platform/bp-mgmt-vcluster/` — primary-region companion
- `platform/bp-dmz-vcluster/` — every-region companion
- `scripts/expected-bootstrap-deps.yaml` slot 59
