# openova-flow-adapter-flux

Kubernetes informer sidecar that watches Flux HelmRelease + HelmChart
CRs in the local cluster, maps each state change into a FlowMessage
envelope, and POSTs to a configured `openova-flow-server`.

This is Agent #2's region-aware emitter — runs as a DaemonSet on every
cluster (mother + every Sovereign + every secondary). The receiving
server is on the primary cluster only; cross-cluster reachability comes
from Cilium Gateway over public HTTPS (no NetBird required for v1).

## Status mapping

Flux's `HelmRelease.status.conditions[type=Ready]` folds into the
`FlowNode.status` palette:

| Ready.status | Ready.reason             | FlowNode.status |
|--------------|--------------------------|-----------------|
| True         | *any*                    | `succeeded`     |
| False        | InstallFailed            | `failed`        |
| False        | UpgradeFailed            | `failed`        |
| False        | RetriesExhausted         | `failed`        |
| False        | Progressing              | `running`       |
| False        | *(other)*                | `running`       |
| Unknown      | *any*                    | `running`       |
| *(no Ready)* | —                        | `pending`       |

## Identity & topology

- **FlowNode.id** = `{REGION_KEY}/{hr.metadata.name}` — region-aware so
  multi-region renders correctly when N adapter sidecars (one per
  cluster) all post to the same flowId.
- **FlowNode.family** — reads `metadata.labels[catalyst.openova.io/family]`
  when present; otherwise heuristic `<name>` with `bp-` prefix stripped
  (so `bp-cert-manager` → `cert-manager`).
- **FlowNode.region** = `REGION_KEY` env.
- **Synthetic region node** — on startup the adapter emits a
  `FlowNode` whose ID == `REGION_KEY` (label "fsn1", "hel1", etc.) so
  the canvas has a stable container parent. Each HR then emits a
  `contains` relationship FROM the region node TO itself.
- **DependsOn relationships** — one per entry in `hr.spec.dependsOn[]`,
  type `finish-to-start`, condition `on-success`.

## Env

| Name                | Default        | Required | Purpose |
|---------------------|----------------|----------|---------|
| `FLOW_SERVER_URL`   | —              | yes      | Base URL of openova-flow-server. |
| `FLOW_ID`           | —              | yes      | Runtime FlowInstance id this adapter binds to. |
| `REGION_KEY`        | —              | yes      | Region id ("fsn1", "hel1", etc.). |
| `NAMESPACE_FILTER`  | `flux-system`  | no       | Informer namespace scope. |
| `EMIT_INTERVAL`     | `200ms`        | no       | Min delay between (id,status) duplicates. |
| `POST_TIMEOUT`      | `10s`          | no       | Per-POST wall clock cap. |
| `HEALTH_LISTEN_ADDR`| `:8081`        | no       | Liveness/readiness listener. |

Per `docs/PRINCIPLES.md` #4 every operational knob is
env-driven; per #4a the container image is built by GitHub Actions
and pulled through harbor.

## Build

```bash
cd products/openova-flow/adapter-flux
go build ./...
go test ./...
```

CI image: `harbor.openova.io/proxy-ghcr/openova-io/openova/openova-flow-adapter-flux:<sha>`.

## Canonical patterns this code follows

- **Informer factory** — `products/catalyst/bootstrap/api/internal/k8scache/factory.go`
  is the seam for `dynamicinformer.NewDynamicSharedInformerFactory` +
  per-resource event handler dispatch.
- **Chart layout** — `platform/qa-app/chart/` is the seam for a simple
  in-house Deployment+Service+ServiceAccount chart (the mirror chart
  for openova-flow-server). The DaemonSet+ClusterRole+ClusterRoleBinding
  shape (mirror chart for this adapter) follows
  `platform/k8s-ws-proxy/chart/`.

## Tests

- `test/mapper_test.go` — every Ready condition state + every dependsOn
  fan-out + every family-label / heuristic path is asserted.
