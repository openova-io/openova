# bp-mgmt-vcluster

Bootstrap-kit Blueprint #58. Provisions the **MGMT vCluster** that hosts every
Sovereign's mgmt-tier control plane (catalyst-api, catalyst-ui,
openova-flow-server) on the **primary region** of a multi-region
Sovereign.

## Why this exists — DoD A4

`docs/DOD.md` ratified 2026-05-15 declares
invariant **A4**:

> **vCluster topology**: primary region = MGMT + DMZ vCluster; each
> secondary region = DMZ + RTZ vCluster. Cross-vCluster intra-region
> traffic stays inside host k3s via Cilium.

This Blueprint implements the MGMT half of that contract.

| Region role | vClusters this Blueprint renders | Companion charts |
|---|---|---|
| Primary    | MGMT                              | bp-dmz-vcluster (slot 54) |
| Secondary  | (skipped — gated off)             | bp-dmz-vcluster + bp-rtz-vcluster (slot 59) |

The bootstrap-kit Kustomization gates render via a `SOVEREIGN_REGION_ROLE`
substitute. The primary CP's cloud-init template sets it to `primary`;
secondary CPs set it to `secondary`. The slot 58 manifest's
`mgmtVcluster.enabled` flips on only when role=primary.

## Resources rendered (full-ON)

- `Namespace mgmt` (catalyst.openova.io/vcluster-role=mgmt label so the
  OpenovaFlow canvas adapter counts it for the dashboard vCluster X/Y
  tile)
- `NetworkPolicy default-deny + allowFrom dmz` for cross-vCluster
  intra-region traffic from the public-fronted DMZ vCluster
- Upstream loft-sh/vcluster 0.20.0 subchart resources (StatefulSet,
  Service, RBAC, etc.) under the `mgmt` namespace with:
  - `nodeSelector: openova.io/region=<primary-region-key>` so the
    StatefulSet pod always lands on the primary CP node
  - `local-path` storage class, 5Gi PVC for embedded sqlite backing store
  - 200m CPU / 384Mi memory request (limits 2 CPU / 1Gi memory)
  - MIRROR-EVERYTHING image: `harbor.openova.io/proxy-ghcr/loft-sh/vcluster:0.20.0`

## Topology dependency

```text
Phase 0 (cloud-init Hetzner CP)
   ↓
bp-cilium             — CNI + Gateway API (slot 01)
   ↓
bp-cert-manager       — TLS for ClusterIssuers (slot 02)
   ↓
bp-mgmt-vcluster      — THIS chart (slot 58, primary-only)
bp-dmz-vcluster       — slot 54 (every region)
bp-rtz-vcluster       — slot 59 (secondary-only)
```

## Testing

`tests/render.sh` exercises three contracts via `helm template`:

1. Default-OFF renders zero umbrella resources
2. Enabled-with-empty-image-tag fails fast (#4a SHA-pin guard)
3. Full-ON renders Namespace + NetworkPolicy + subchart StatefulSet + Service

## See also

- `docs/DOD.md` — A4 contract
- `infra/hetzner/README.md` lines 50-100 — topology diagram
- `platform/bp-dmz-vcluster/` — companion (every region)
- `platform/bp-rtz-vcluster/` — companion (secondary regions)
- `scripts/expected-bootstrap-deps.yaml` slot 58 — dependency-graph audit declaration
