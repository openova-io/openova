# Litmus Chaos

Chaos engineering experiments for Kubernetes. **Application Blueprint** (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.9 — Chaos engineering). Used to validate Catalyst's resilience guarantees (failover-controller behavior under network partition, OpenBao DR promotion, PowerDNS lua-record `ifurlup` endpoint removal) — see [`docs/SRE.md`](../../docs/SRE.md) for the resilience model. Required by some compliance regimes (DORA, NIS2) as evidence of resilience testing.

**Category:** Chaos Engineering | **Type:** Application Blueprint

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
