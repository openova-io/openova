# Litmus Chaos

Chaos engineering for Kubernetes.

**Category:** Chaos Engineering | **Type:** A La Carte

---

## Overview

Litmus provides chaos engineering experiments for Kubernetes workloads. Banks and regulated environments need proof of resilience — Litmus enables automated chaos testing as part of CI/CD pipelines and compliance validation.

## Key Features

- Pre-built chaos experiments (pod-kill, network-latency, disk-fill)
- ChaosHub for experiment catalog
- GameDay orchestration
- Resilience scoring
- CI/CD integration via Gitea Actions

## Integration

| Component | Integration |
|-----------|-------------|
| Grafana | Chaos experiment observability |
| Kyverno | Policy-based chaos boundaries |
| Gitea Actions | Automated chaos in CI/CD |
| Failover Controller | Validate failover behavior |

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: litmus
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/litmus
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
