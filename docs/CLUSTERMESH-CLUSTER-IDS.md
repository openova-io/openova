# Cilium ClusterMesh — Cluster ID Registry

## Why this file exists

Every Cilium ClusterMesh peer needs a unique `cluster.name` + `cluster.id`
pair. The ID is a uint8 (1–255 range; 0 is reserved) and identity collisions
are silent — peer A and peer B both thinking they are id=1 produces a
peering failure with no helpful log line. Per
`docs/PRINCIPLES.md` rule #4 (never hardcode), the chart cannot
ship a default cluster.id, and per the same rule the IDs MUST be tracked
somewhere durable so future Sovereigns don't accidentally re-use them.

This file is that ledger. It is the source of truth for ClusterMesh peer
identity across the OpenOva fleet. Every per-Sovereign overlay at
`clusters/<sovereign>/bootstrap-kit/01-cilium.yaml` MUST set
`spec.values.cilium.cluster.name` + `spec.values.cilium.cluster.id` to a
pair listed in the table below; new Sovereigns MUST claim a fresh row in
the same PR that introduces the overlay.

## The chart shape

The `bp-cilium` umbrella chart at `platform/cilium/chart/` ships the
ClusterMesh values overlay separately at `values-clustermesh.yaml` (see
that file's header for rationale). The default `values.yaml` has no
ClusterMesh block — it is opt-in per Sovereign so single-region
Sovereigns don't pay the LoadBalancer / NodePort cost.

When a Sovereign joins a mesh, its overlay merges these values on top of
the chart defaults:

```yaml
values:
  cilium:
    cluster:
      name: <one of the names in §"Registry" below>
      id:   <matching id>
    clustermesh:
      useAPIServer: true
      apiserver:
        service:
          # NodePort for Hetzner / on-prem (avoids per-peer LB quota
          # on Hetzner projects); LoadBalancer for AWS/GCP if the
          # provider has free LB quota.
          type: NodePort
          nodePort: 32379
```

NodePort `32379` is the canonical port across the OpenOva fleet (matches
upstream Cilium's documented default for self-hosted peering). The
Hetzner firewall rules `catalyst-omantel-biz-fw` open 32379/tcp from
peer Sovereigns' private CIDRs; analogous rules MUST be added for every
new peer.

## Registry

| cluster.id | cluster.name | Sovereign FQDN | Region | Mesh | Notes |
|------------|--------------|----------------|--------|------|-------|
| 1 | `omantel-fsn` | `omantel.omani.works` | Hetzner fsn1 | mesh-omantel | Original (Phase-1). |
| 2 | `omantel-hel` | `omantel-hel.omani.works` | Hetzner hel1 | mesh-omantel | Phase-2 peer (qa-loop iter-6). |
| 3..255 | reserved for future omantel-* and other Sovereign meshes | | | | Allocate sequentially. |

> **Rule.** When a new Sovereign joins a mesh, claim the next free ID in
> this table in the same PR that introduces the per-Sovereign overlay.
> Before merging, run:
>
> ```bash
> # No two rows in the table may share an id within the same Mesh column.
> grep -E "^\| [0-9]" docs/CLUSTERMESH-CLUSTER-IDS.md \
>   | awk -F'|' '{ print $5 ":" $2 }' | sort | uniq -d
> ```
>
> A non-empty result fails the PR.

## Convention: cluster.name format

`<sovereign-stem>-<region-code>` where:

- `<sovereign-stem>` — first label of the Sovereign FQDN (e.g. `omantel`,
  `otech`, `acme`). Lower-case, ASCII, hyphens allowed.
- `<region-code>` — Hetzner region short code (`fsn`, `hel`, `nbg`, `ash`,
  `hil`) or AWS/GCP region key (`use1`, `usw2`, `euw1`, ...).

Examples that ARE valid: `omantel-fsn`, `omantel-hel`, `acme-nbg`,
`bigtenant-use1`. Examples that are NOT: `omantel.omani.works`
(uses the full FQDN — too long, dots not allowed), `prod-fsn` (no
sovereign tied), `OMANTEL-FSN` (case-sensitive on cluster identity).

## Mesh boundaries

A mesh is the closed graph of peers connected via `cilium clustermesh
connect`. Within a mesh every cluster.id MUST be unique; ACROSS meshes
the same id is fine. Today every Sovereign's data-plane regions form a
mesh per Sovereign (single tenant boundary); that is the only mesh
topology the chart supports.

When Continuum (#1101) goes live, the cross-Sovereign management plane
mesh (`hz-nbg-mgt-prod` ↔ data-plane peers) will form a SECOND mesh
graph. Allocate cluster.ids in the same table but mark the mesh column
distinctly so the duplicate-id check above keeps working.

## Operator runbook (per-peer bringup)

See `platform/cilium/chart/values-clustermesh.yaml` header — the steps
are the source of truth. In short:

1. Apply the overlay (HelmRelease reconcile).
2. `cilium clustermesh enable --service-type NodePort --context <local>`
3. `cilium clustermesh enable --service-type NodePort --context <remote>`
4. `cilium clustermesh connect --context <local> --destination-context <remote>`
5. `cilium clustermesh status --context <local>` — assert all peers
   `connected`.
6. Spot-check a global service: deploy bp-cnpg-pair on the mesh and
   assert the replica region's `pg_stat_wal_receiver` shows the primary
   region's WAL stream.

## Live mesh as of 2026-05-09 (qa-loop iter-6)

Mesh `mesh-omantel`:

```
omantel-fsn (id=1, NodePort 32379)
   ↕ cilium clustermesh connect
omantel-hel (id=2, NodePort 32379)
```

Both peers report 2-of-2 connected via `cilium clustermesh status`.
bp-cnpg-pair@0.1.1 deployed on top exercises a global Service for WAL
streaming.
