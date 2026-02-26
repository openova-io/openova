# Reloader

Auto-restart pods on ConfigMap and Secret changes.

**Category:** Operations | **Type:** Mandatory

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
