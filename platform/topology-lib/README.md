# platform/topology-lib

Helm **library chart** holding the one topology values contract of ADR-0015 D2
and the helpers every multi-region chart uses to render **only its own region's
half**. Platform plumbing — not a Blueprint, never installed on its own, no
`blueprint.yaml`.

Canonical decision: [`docs/adr/0015-region-symmetric-gitops-and-native-topology-transitions.md`](../../docs/adr/0015-region-symmetric-gitops-and-native-topology-transitions.md) (D2), sync replication per
[`docs/adr/0004-cnpg-sync-replication.md`](../../docs/adr/0004-cnpg-sync-replication.md).

## Contract

```yaml
openova:
  topology:
    mode: singleton | active-hot-standby | active-passive | active-active
    self: <region-id>       # the region THIS HelmRelease is applied in
    primary: <region-id>    # the region holding the Primary
    peers: [<region-id>]    # every other region of the placement
```

One HelmRelease per region; every field identical across regions except
`self`. Region ids use the cluster region id spelling (`hw-me-east-215-a-rtz-prod`,
ADR-0015 D3). Absent / empty ⇒ legacy single-region render.

## Consuming it

```yaml
# Chart.yaml
dependencies:
  - name: topology-lib
    version: "0.1.0"
    repository: "file://../../topology-lib/chart"
```

`helm dependency build` vendors the library into the consumer's package, so a
published Blueprint carries the exact helper code it was tested with and never
waits on a separate registry publish. `blueprint-release.yaml`,
`check-chart-annotations.sh` and `check-no-nodeports.sh` skip `type: library`
charts (nothing installable to render).

## Helpers

| Helper | Returns |
|---|---|
| `openova.topology.get` | JSON `{present, mode, multiRegion, self, primary, peers, isPrimary}`; **fails** on: unknown mode; multi-region without `self` / `primary` / `peers`; `self` in `peers`; `primary` neither self nor a peer; duplicate peers; non-DNS-label ids; singleton with peers |
| `openova.topology.assertMode` `(dict "ctx" . "supported" (list …))` | fails if the chart does not support the mode |
| `openova.topology.isPrimary` / `multiRegion` / `role` | `"true"` / `"true"` / `primary\|replica` |
| `openova.topology.regionHash` `<region-id>` | first 8 hex of sha256 — the per-region suffix |
| `openova.topology.nameFor` `(dict "base" B "region" R "overrides" O)` | `<B[:41]>-<hash(R)>` (≤ 50 chars, the CNPG name ceiling) or `O[R]` |
| `openova.topology.memberNames` `(dict "ctx" . "base" B "overrides" O)` | JSON `{self, primary, peers[], source}` |
| `openova.topology.cnpg.replica` | the CNPG distributed-topology `replica:` body (`self`, `primary`, `source`, `promotionToken` only on the member named primary) |
| `openova.topology.cnpg.externalClusters` | one entry per member over `<member>-mesh`, presenting `<self>-replication` (no sslRootCert) |
| `openova.topology.cnpg.syncStandbyNames` | JSON list of peer Cluster names on the primary, `[]` elsewhere (ADR-0004: names only DECLARED peers) |
| `openova.topology.cnpg.managedMeshService` | this member's `<self>-mesh` for `managed.services.additional` (`selectorType: rw`, never `r` — #3740) |
| `openova.topology.cnpg.peerMeshStubs` | zero-backend `<peer>-mesh` global Services so peer aliases resolve locally |
| `openova.topology.meshAnnotations` `<affinity>` | Cilium global/shared/affinity annotations |
| `openova.topology.labels` | `openova.io/region: <self>` + `openova.io/topology-mode` |

## Consumers

- `platform/wordpress-tenant` (0.5.0) — first consumer.
- TODO (each still speaks its own role vocabulary): `platform/cnpg-pair`
  (`cnpgPair.side`), `platform/postgres` (`topology.side/mode`),
  `platform/gitea`, `platform/openbao`, `platform/harbor`, `platform/newapi`,
  `platform/guacamole`, `platform/sandbox`.

CI: [`scripts/check-chart-renders-self-only.sh`](../../scripts/check-chart-renders-self-only.sh)
renders every registered multi-region chart once per region and fails on any
resource pinned to a region other than `self`.
