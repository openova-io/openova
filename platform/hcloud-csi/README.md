# bp-hcloud-csi

**Status**: Phase-0 scaffold (#1095 slice H6). Activated by EPIC-6 (#1101).
**Updated**: 2026-05-08

Upstream Hetzner Cloud CSI driver (`hetznercloud/csi-driver`) wrapped as a
Catalyst Blueprint. Provides the `hcloud-volumes` StorageClass that backs
multi-node stateful workloads on Hetzner Sovereigns — required for CNPG
primary/replica pairs across nodes (and across regions, once Cilium
ClusterMesh + Continuum land in #1101).

This Blueprint is **not** in the bootstrap-kit. The default StorageClass on
existing Sovereigns is `local-path` (single-node-bound) — installing this
chart with `defaultStorageClass: true` flips the default to `hcloud-volumes`,
which would migrate every new PVC. Operators activate deliberately per
Sovereign once the upgrade path for existing PVCs is sized.

## What it ships

| Template | Effect |
|---|---|
| `storageclass.yaml` | StorageClass `hcloud-volumes` with `WaitForFirstConsumer` binding (Pod scheduling pins the volume to the right node) and `allowVolumeExpansion: true`. Optional `volumeBindingMode: Immediate` override for use cases that need pre-provisioning. |
| `storageclass-default.yaml` | Annotates `hcloud-volumes` as the cluster default when `.Values.defaultStorageClass: true`. |
| `volumesnapshotclass.yaml` | VolumeSnapshotClass for backup workflows. Default off. |

Upstream `hcloud-csi-driver` itself is pulled in as a Helm subchart from
`hetznercloud/csi-driver` (referenced in `Chart.yaml`). The catalyst overlay
templates above sit alongside it.

## Activation contract

```yaml
# values.yaml override (or per-Sovereign overlay)
enabled: true
defaultStorageClass: true              # flip the cluster default
hcloudCsi:
  controller:
    replicas: 2                        # HA for multi-node Sovereigns
  storageClasses:
    - name: hcloud-volumes
      reclaimPolicy: Delete
      volumeBindingMode: WaitForFirstConsumer
      allowVolumeExpansion: true
```

When `enabled: false` (the default), no resources render — installing the
chart is a no-op until the operator opts in.

## Why a separate Blueprint, not a values toggle on bp-cilium

CSI drivers are independent of CNI. Mixing them risks coupling the network
plane upgrade cycle to the storage plane upgrade cycle. Separate Blueprint
keeps the surfaces independent.

## Why default-OFF on `defaultStorageClass`

Flipping the cluster's default StorageClass is a destructive change for
Pods relying on the previous default's volume-binding semantics. Existing
Sovereigns ship with `local-path` as default; an in-place migration plan
(drain, repvc, copy-data) is its own slice. Keeping `defaultStorageClass:
false` here ensures installing the chart is reversible.

## References

- docs/EPICS-1-6-unified-design.md §3.9 row 6
- docs/SRE.md §2.5 (CNPG storage requirements)
- platform/cnpg/README.md §38-58
- Upstream: https://github.com/hetznercloud/csi-driver
