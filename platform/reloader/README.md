# Reloader

Auto-restart Pods when ConfigMap/Secret hashes change. Per-host-cluster infrastructure (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §3.4) — runs on every host cluster Catalyst manages. Critical for Catalyst's secret-rotation flow: when ESO updates a K8s Secret from OpenBao, Reloader triggers a rolling deploy of consuming Pods (see [`docs/SECURITY.md`](../../docs/SECURITY.md) §3).

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

*Part of [OpenOva](https://openova.io)*
