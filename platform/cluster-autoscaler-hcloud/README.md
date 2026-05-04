# bp-cluster-autoscaler-hcloud

Catalyst Blueprint umbrella chart for the Kubernetes
[cluster-autoscaler](https://github.com/kubernetes/autoscaler) configured
with the Hetzner Cloud cloud-provider. Adds and removes Hetzner workers
in response to `FailedScheduling` events on a Sovereign's k3s cluster.

## Why

Per issue #767, a freshly-provisioned Sovereign reaches `FailedScheduling`
the moment the bootstrap-kit's RAM aggregate exceeds the static worker
pool the operator picked in the wizard. Live evidence (otech92): two
`cpx32` workers couldn't fit the `external-secrets-webhook` Pod because
the bootstrap-kit consumed the full 16 GB. The fix is two-pronged:

1. **Pre-launch**: the wizard's StepReview surfaces an *estimated*
   footprint so the operator picks a worker pool that fits.
2. **Runtime**: this blueprint adds `cluster-autoscaler` so the
   Sovereign scales workers up/down on demand, bounded by the
   `min`/`max` operator chose at launch.

## How it wires

- **Helm subchart**: upstream
  [`kubernetes/autoscaler/cluster-autoscaler`](https://github.com/kubernetes/autoscaler/tree/master/charts/cluster-autoscaler)
  vendor-neutral, multi-cloud cluster-autoscaler. The Hetzner cloud
  provider ships in the same upstream container image.
- **Hetzner token**: read at HelmRelease apply time from
  `flux-system/cloud-credentials.hcloud-token` (the canonical Secret
  cloud-init writes per ADR-0001 §11.3 — same Secret consumed by
  Crossplane provider-hcloud + provider-config-hcloud).
- **Node group**: a single canonical pool keyed off the Sovereign's
  worker SKU + region + cloud-init template. The pool's `min` is the
  operator's chosen worker count; `max` defaults to 10 (overridable
  per-Sovereign).
- **Scale-down**: 10 minutes idle (cost-saving default).

## What this blueprint does NOT do

- **It does not pre-create extra nodes.** Phase 0 (`tofu apply`) only
  provisions the `min` worker count; cluster-autoscaler creates
  additional workers on-demand against the same Hetzner project.
- **It does not provision the OpenTofu node-pool template.** That
  restructuring is tracked separately (see follow-up issue) — the
  MVP shipped in this PR pins the node-group config in chart values
  and assumes the existing single-pool topology.
- **It does not autoscale workloads.** `KEDA` (event-driven workload
  autoscaling) and the kubernetes-builtin `HPA` (horizontal pod
  autoscaler) are layered on top; cluster-autoscaler handles the
  *node* dimension only.

## Upstream pinning

| Knob | Value | Notes |
|------|-------|-------|
| Chart | `cluster-autoscaler` (kubernetes/autoscaler) | `9.46.6` — current stable on 2026-05-04 |
| App | `cluster-autoscaler` | `1.32.0` (matches k3s 1.31.x — within +/-1 minor of the Sovereign apiserver) |
| Cloud provider | `hetzner` | Built into upstream image |

Per `docs/INVIOLABLE-PRINCIPLES.md` #4 (never hardcode), every value is
runtime-configurable; cluster overlays in `clusters/<sovereign>/` MAY
override any of them without rebuilding the OCI artifact.
