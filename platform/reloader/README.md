# Reloader

Auto-restart Pods when ConfigMap/Secret hashes change. Per-host-cluster infrastructure (see [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §3.4) — runs on every host cluster Catalyst manages. Critical for Catalyst's secret-rotation flow: when ESO updates a K8s Secret from OpenBao, Reloader triggers a rolling deploy of consuming Pods (see [`docs/SECURITY.md`](../../docs/SECURITY.md) §3).

**Category:** Operations | **Type:** Mandatory per host cluster

---

## Overview

Reloader watches for changes to ConfigMaps and Secrets, then triggers rolling restarts of associated Deployments, StatefulSets, and DaemonSets. Eliminates the operational gap where configuration changes require manual pod restarts.

## Key Features

- Automatic rolling restart on ConfigMap/Secret changes
- Annotation-based opt-in per workload
- SHA-based change detection (no unnecessary restarts)
- Minimal resource footprint

## Integration

| Component | Integration |
|-----------|-------------|
| External Secrets (ESO) | Restart pods when secrets rotate |
| OpenBao | Secret rotation triggers pod refresh |
| cert-manager | Certificate renewal triggers restart |
| Flux | GitOps config changes auto-propagate |

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: reloader
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/reloader
  prune: true
```

---

## Configuration knobs

Opt-in is annotation-based (`reloader.stakater.com` namespace):

- `reloader.stakater.com/auto: "true"` — watch every ConfigMap/Secret the workload mounts and roll it on change.
- `configmap.reloader.stakater.com/reload: "<name>[,<name>…]"` — roll only on the named ConfigMaps.
- `secret.reloader.stakater.com/reload: "<name>[,<name>…]"` — roll only on the named Secrets.
- `reloader.stakater.com/match: "true"` — restrict `auto` to resources carrying the matching label.

## Operational notes

- Runs as a single, stateless, leader-elected Deployment **per host cluster** — no cross-region coordination and no per-Organization state, so multi-region topology is simply one Reloader per cluster, each watching only its own cluster's ConfigMaps/Secrets.
- Negligible footprint; horizontal scaling is not required (one replica watches the whole cluster) and backups are N/A (it holds no persistent state).

---

*Part of [OpenOva](https://openova.io)*
