# bp-dmz-vcluster-tenant

> **Note (TBD-A6c rename, 2026-05-18):** This chart was previously named
> `bp-dmz-vcluster` — same as `platform/bp-dmz-vcluster/chart/`, which
> prevented `scripts/check-bootstrap-kit-pin-sync.sh` from
> disambiguating the two charts. The per-tenant marketplace chart is
> now named `bp-dmz-vcluster-tenant`; the platform/ bootstrap-topology
> chart keeps its canonical `bp-dmz-vcluster` name (slot 54, DoD A4).
> See issue #1719 for the rename rationale.

Catalyst-authored Blueprint chart for a DMZ vCluster — an isolated
customer-internet-facing virtual Kubernetes cluster running inside the
management cluster. Per `docs/ARCHITECTURE.md` §8.5 the
DMZ vCluster gives customer workloads that need direct internet
exposure (public APIs, webhooks, customer-facing dashboards) a hard
isolation boundary from the management plane.

## What ships

| Resource | Purpose |
|---|---|
| `HelmRelease/<rel>` | Wraps the upstream `loft-sh/vcluster` chart; SHA-pinned via `image.tag`; resource budget + sync config + storage class |
| `Service/<rel>-apiserver` | ClusterIP for operator `vcluster connect` access |
| `HTTPRoute/<rel>` (optional) | Cilium Gateway exposure for tenant Services synced into the host namespace |
| `NetworkPolicy/<rel>-default-deny` | Empty ingress + egress = deny-all baseline |
| `NetworkPolicy/<rel>-allow-essentials` | DNS + designated egress-gateway + intra-namespace |

## Default-OFF gate

`dmz.enabled: false` in `values.yaml`. `helm template` renders **zero**
resources by default. Operator opts in via per-Sovereign overlay at
`clusters/<sovereign>/products/dmz-vcluster/release.yaml` once Cilium
ClusterMesh + the egress gateway are ready.

## Isolation model

- **Own openova-system namespace** inside the host cluster
  (`dmz` by default; multi-DMZ overlays use per-tenant names like
  `dmz-acme`).
- **Own Cilium identity** — by virtue of the dedicated namespace,
  Cilium assigns a distinct identity to every DMZ-vCluster Pod. Default
  Cluster-wide CCNPs (H8 default-deny) treat them as an isolated
  endpoint cohort.
- **NetworkPolicy default-deny** on the host namespace: every
  flow into or out of the namespace is denied unless explicitly
  allowed. Allow rules cover DNS + the designated egress gateway +
  intra-namespace coordination only.
- **Egress to the public internet only via the designated egress
  gateway** — the egress gateway SNATs to a reserved public IP so
  audit + threat-intel can attribute outbound flows to the tenant.
- **No privileged caps; no host-network access; read-only root FS**
  where the upstream vcluster image permits.

## SHA-pinned vcluster image

Per `docs/PRINCIPLES.md` #4a, `dmz.vcluster.image.tag` is
empty in `values.yaml` and the helm-template render fails-fast when an
overlay leaves it empty (see `_helpers.tpl::bp-dmz-vcluster-tenant.image`). CI
populates the SHA tag via `yq eval -i .image.tag = "<sha>"` when
promoting a build into `clusters/<sovereign>/`.

## Upstream HelmRelease wrapper, NOT a vendored subchart

The DMZ vCluster install happens via a `HelmRelease` pointing at the
upstream `loft-sh/vcluster` chart. The Catalyst layer ships:

1. The HelmRelease CR with the operator-pinned upstream chart version.
2. A `values:` block exposing only the safe subset of upstream values
   (resources, storage, sync, security context).
3. The isolation NetworkPolicy + Service + HTTPRoute that ride
   alongside the upstream install.

Per-Sovereign overlays flip individual values (resource limits,
pod-security, auth) without forking the upstream chart. The
`HelmRepository` for `https://charts.loft.sh` is part of the
Sovereign's bootstrap-kit.

## Tests

`bash tests/render.sh` exercises three contracts:

1. **Default-OFF**: zero K8s resources rendered (CC3 default-OFF gate).
2. **Fail-fast on empty image tag**: render aborts with the exact
   `bp-dmz-vcluster-tenant: ... image.tag is empty` message when
   `enabled: true` without a SHA stamp.
3. **Full-ON canonical bundle**: HelmRelease + 2 NetworkPolicies +
   Service (+ HTTPRoute when hostname is set).

`helm lint` clean.

## See also

- `DESIGN.md` — design rationale, isolation boundary, ADR-0001 alignment.
- `blueprint.yaml` — Blueprint manifest (catalyst.openova.io/v1alpha1).
- `platform/network-policies/chart/templates/default-deny.yaml` —
  cluster-wide default-deny CCNP that complements the host-namespace
  NetworkPolicy here.
