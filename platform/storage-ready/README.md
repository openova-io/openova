# bp-storage-ready

The neutral storage gate of the bootstrap kit: a release that becomes Ready
only when the Sovereign's durable default StorageClass exists.

## Role in Catalyst

Control-plane plumbing, installed on every region by bootstrap-kit slot 15b
(`clusters/_template/bootstrap-kit/15b-storage-ready.yaml`) as HelmRelease
`storage-ready`. Never a catalog item (`visibility: unlisted`). See
[`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) for the bootstrap kit.
Slots that bind volumes depend on `storage-ready` and on no cloud-specific
disk-driver release; the first is `bp-cnpg` (slot 16). A PVC created before the
default class exists keeps an empty class and stays Pending; the gate prevents
that.

## What it ships and how to configure it

One pre-install and pre-upgrade hook Job, with a ServiceAccount, a ClusterRole
(`get` and `list` on StorageClasses) and its binding. The Job exits 0 when the
StorageClass named by `expectedClass` carries the annotation
`storageclass.kubernetes.io/is-default-class: "true"`. It checks every 5 seconds
for at most 10 minutes, then exits 1; Flux retries the release.

The one value, `expectedClass`, has no default and is required: an unset or
empty value fails the Job at once. Slot 15b sets it from the kit key
`SOVEREIGN_CNPG_STORAGE_CLASS`.

## Operational notes

- No data, no backup. Each region runs its own gate on its own cluster.
- A failed gate shows in the conditions of the HelmRelease `storage-ready` and
  in the log of the Job `storage-ready-gate` in `flux-system`.
- `bash platform/storage-ready/chart/tests/storage-ready-render.sh` renders the
  chart and runs the Job's script against a stub `kubectl`.
