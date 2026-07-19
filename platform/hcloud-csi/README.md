# bp-hcloud-csi

**Status**: Bootstrap-kit slot 55a (#3971) — installed on every fresh Hetzner
Sovereign as the cluster-default StorageClass. (Was Phase-0 scaffold #1095
slice H6 / EPIC-6 #1101.)
**Updated**: 2026-06-20

Upstream Hetzner Cloud CSI driver (`hetznercloud/csi-driver`) wrapped as a
Catalyst Blueprint. Provides the `hcloud-volumes` StorageClass that backs
ALL stateful workloads on Hetzner Sovereigns (CNPG primary/replica pairs,
openbao, keycloak, gitea, harbor, loki/mimir/tempo) — durable cloud block
volumes instead of ephemeral node-local disk.

**In the bootstrap-kit (#3971).** The HelmRelease at
`clusters/_template/bootstrap-kit/55a-bp-hcloud-csi.yaml` installs this chart
on every fresh **Hetzner** Sovereign with `enabled: true` +
`defaultStorageClass: true` (suspended on Huawei via `HCLOUD_HR_SUSPEND`). A
post-install hook promotes `hcloud-volumes` to cluster-default and DEMOTES the
k3s `local-path` default — so production PVCs never land on ephemeral disk.
The Phase-1 DoD gate in catalyst-api (`markPhase1Done`) FAILS a prov whose
final default StorageClass is still `local-path`, so this can never silently
regress. local-path stays installed (non-default) for genuinely-ephemeral
caches.

> **The P0 blocker it closes (#3971):** before this slot, k3s `local-path` was
> the default and ONLY StorageClass on every Sovereign — a directory on the
> node's local disk with no cloud volume behind it. A node replacement
> (autoscaler, failed node, re-prov, disk loss) destroyed that PV's data,
> including prod Postgres (3×100Gi), openbao secrets, and the customer-Org DB.

## What it ships

| Template | Effect |
|---|---|
| `storageclass.yaml` | StorageClass `hcloud-volumes` — `reclaimPolicy: Retain` (production-data durability: the backing Hetzner Volume survives PVC/PV deletion), `WaitForFirstConsumer` binding (pins the volume to the Pod's node), `allowVolumeExpansion: true`. |
| `default-class-hook.yaml` + `default-class-hook-rbac.yaml` | Post-install hook Job (+ scoped ClusterRole) that promotes `hcloud-volumes` to cluster-default and demotes the k3s `local-path` default so there is exactly ONE durable default class. |
| `volumesnapshotclass.yaml` | VolumeSnapshotClass `hcloud-volumes-snapshots` for storage-level DR / backup workflows. Default ON (#3971). |
| `hcloud-token-secret.yaml` | Renders the `hcloud-csi-token` Secret from the Hetzner API token (Flux `valuesFrom` cloud-credentials) so the controller authenticates. |

Upstream `hcloud-csi-driver` itself is pulled in as a Helm subchart from
`hetznercloud/csi-driver` (referenced in `Chart.yaml`). The catalyst overlay
templates above sit alongside it.

## Activation contract

The bootstrap-kit slot (55a) already sets these on every Hetzner Sovereign;
this is the values shape if you hand-override per Sovereign:

```yaml
# values.yaml override (or per-Sovereign overlay)
enabled: true
defaultStorageClass: true              # flip the cluster default
hetznerToken: "<from cloud-credentials via Flux valuesFrom>"
catalystStorageClasses:
  - name: hcloud-volumes
    reclaimPolicy: Retain                # production-data durability (#3971)
    volumeBindingMode: WaitForFirstConsumer
    allowVolumeExpansion: true
volumeSnapshotClass:
  enabled: true                         # storage-level DR (#3971)
```

When `enabled: false` (the chart default), no resources render — the chart is
a no-op. The bootstrap-kit slot is what flips `enabled: true` on Hetzner.

## Why a separate Blueprint, not a values toggle on bp-cilium

CSI drivers are independent of CNI. Mixing them risks coupling the network
plane upgrade cycle to the storage plane upgrade cycle. Separate Blueprint
keeps the surfaces independent.

## Default-class flip + local-path demotion (#3971)

The **chart's** `defaultStorageClass` value defaults to `false` (so a bare
`helm install` is reversible), but the **bootstrap-kit slot 55a** sets it
`true` on every Hetzner Sovereign. When true:

- The `hcloud-volumes` StorageClass renders with the
  `storageclass.kubernetes.io/is-default-class: "true"` annotation.
- A post-install hook Job (`default-class-hook.yaml`) waits for the SC to
  exist, then promotes it to default and **demotes** any OTHER StorageClass
  still carrying the default annotation so there is exactly ONE default (on
  a fresh prov this is a no-op — `local-path` does not exist at all).
- `local-path` is FORBIDDEN outright (#3971): k3s runs
  `--disable=local-storage` so the provisioner is NEVER installed, and the
  bp-kyverno-policies K23 ENFORCE policy DENIES any `local-path` PVC /
  StorageClass / `rancher.io/local-path` provisioner. There is no opt-in —
  genuinely-ephemeral caches use `emptyDir`, never a local-path PVC.

On a FRESH prov this is non-destructive: the stateful slots (openbao,
keycloak, gitea, cnpg) install LATE (after their `dependsOn` chains on
bp-mgmt-vcluster / bp-cert-manager), by which time the CSI default-class flip
has already run, so their PVCs bind to the durable `hcloud-volumes` class.
Migrating an EXISTING Sovereign's local-path data is out of scope (destructive,
separate) — the durable fix is the bootstrap so every fresh prov is correct.

## References

- docs/ARCHITECTURE.md §3.9 row 6
- docs/SRE.md §2.5 (CNPG storage requirements)
- platform/cnpg/README.md §38-58
- Upstream: https://github.com/hetznercloud/csi-driver
