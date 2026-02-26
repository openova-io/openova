# Matrix/Synapse

Decentralized chat and messaging platform.

**Category:** Communication | **Type:** A La Carte

---

## Overview

Matrix (via the Synapse server) provides self-hosted, federated chat and messaging with end-to-end encryption. Supports team collaboration, incident communication, and integration with external Matrix networks.

## Key Features

- End-to-end encrypted messaging
- Federation with external Matrix servers
- Room-based team collaboration
- Bridge support (Slack, IRC, Discord)
- Webhook integrations for alerting

## Integration

| Component | Integration |
|-----------|-------------|
| Keycloak | SSO via OIDC |
| CNPG | PostgreSQL backend |
| Grafana | Alert notifications via Matrix |
| Stalwart | Email notifications |

## Used By

- **OpenOva Relay** - Team messaging component

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: matrix
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/matrix
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
