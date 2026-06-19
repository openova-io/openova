# mothership-ops

**What it is.** A repo-tracked Kustomize base of housekeeping manifests that
run **on the OpenOva mothership** — the Contabo k3s control plane that hosts
`catalyst-api`, the marketplace, and the deployment provisioner. It is **not**
a catalog Blueprint: nothing here is installed into a Sovereign. There is no
`blueprint.yaml` on purpose — these manifests would never appear as an
installable card.

**Role in Catalyst.** Control-plane self-maintenance. The mothership runs the
provisioning goroutine in-process (`products/catalyst/bootstrap/api`), so the
mothership node's own disk/memory headroom is load-bearing for every fresh
prov. This base keeps that node healthy so a fresh prov never takes off into a
resource-starved node.

See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §8.9.7 for the
mothership-vs-Sovereign deployment split (mothership manifests are reconciled
from `openova-private`; reusable, versioned bases live here in the public repo).

## Contents

### `base/disk-prune-cronjob.yaml` — mothership disk auto-prune (#3889)

Hourly CronJob, pinned to the control-plane node, that **self-maintains the
mothership disk** so the hw168 cascade (node ~93% → DiskPressure → pod
evictions → dead pdm ghcr token → un-persisted deployment record → orphaned
Huawei infra) can never recur. Only when the node root filesystem is at/above a
high-water mark (default **75%**) it:

- **A.** Prunes UNUSED containerd images via `k3s crictl rmi --prune` — the
  safe form that never removes a layer referenced by a running container.
- **B.** Vacuums `/var/lib/catalyst/{tofu,executions}/<id>` workdirs for every
  `<id>` whose deployment record `deployments/<id>.json` is **absent** (wiped /
  never persisted), with a 6h mtime grace window so an in-flight prov whose
  record write is lagging is never touched. A live deployment's record,
  kubeconfig, and workdir are never removed.

It never touches the bastion (not a k3s node), never runs on a worker, and is a
cheap no-op on a healthy node.

## Configuration knobs

Set on the CronJob container `env` (or patched via an openova-private overlay):

| Env | Default | Meaning |
|---|---|---|
| `DISK_HIGH_WATER_PCT` | `75` | Only prune when node root fs usage ≥ this percent. |
| `VACUUM_GRACE_HOURS` | `6` | Don't vacuum a recordless workdir younger than this. |
| `CATALYST_DIR` | `/host-catalyst` | Mount path of the `catalyst-api-deployments` PVC. |
| `CONTAINERD_SOCK` | `/run/k3s/containerd/containerd.sock` | Host containerd socket for crictl. |

The CronJob `schedule` (default `17 * * * *`) is overridable the same way.

## How it is applied to the mothership

The mothership's GitOps tree lives in `openova-private`. Reference this base
from there so it stays versioned + repo-tracked (NOT a one-off `kubectl apply`):

```yaml
# openova-private/clusters/contabo-mkt/apps/mothership-ops/kustomization.yaml
resources:
  - github.com/openova-io/openova/platform/mothership-ops/base?ref=main
```

Flux on the mothership reconciles it into `catalyst-system`.

## Operational notes

- **Backups / scaling / multi-region:** none — this is a single-node mothership
  housekeeping job, not a workload. It runs only on the control-plane node.
- **Pairing with the pre-fire gate:** the durable #3889 fix is two-sided. This
  CronJob keeps the disk from filling (proactive). The **pre-fire resource
  preflight gate** in `products/catalyst/bootstrap/api` refuses a deployment
  create when the mothership node is already over budget (reactive) — see that
  package's `resource_preflight.go`.
