# Coraza

Web Application Firewall with OWASP Core Rule Set. Per-host-cluster infrastructure (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §3.1) — runs at the DMZ edge of every host cluster Catalyst manages.

**Category:** WAF | **Type:** Mandatory per host cluster (DMZ block)

---

## Overview

Coraza is a high-performance WAF that integrates with Cilium/Envoy to provide application-layer protection using the OWASP Core Rule Set (CRS). Protects against SQL injection, XSS, and other OWASP Top 10 threats.

## Key Features

- OWASP Core Rule Set (CRS) compliance
- Envoy external processing filter integration
- Request/response inspection
- Custom rule support
- Low-latency inline processing

## Integration

| Component | Integration |
|-----------|-------------|
| Cilium/Envoy | Inline WAF via ext_proc filter |
| Grafana | WAF metrics and blocked request dashboards |
| Falco | Correlate WAF blocks with runtime events |
| OpenSearch | WAF log analysis in SIEM |

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: coraza
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/coraza
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
