# FerretDB

MongoDB wire protocol on PostgreSQL. **Application Blueprint** (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.1) — installed by Organizations that want MongoDB API compatibility. Replication piggybacks on the underlying CNPG cluster (WAL streaming) — no separate replication mechanism needed.

**Category:** Database | **Type:** A La Carte (Application Blueprint)

---

## Overview

FerretDB provides MongoDB wire protocol compatibility backed by PostgreSQL (via CNPG). Applications using MongoDB drivers connect unchanged, while data is stored in PostgreSQL with full ACID guarantees, WAL-based replication, and no SSPL license concerns.

## Key Features

- MongoDB wire protocol compatibility
- PostgreSQL backend (CNPG managed)
- No SSPL license (Apache 2.0)
- WAL-based replication via CNPG (no Debezium/Kafka required)
- Full ACID transactions

## Integration

| Component | Integration |
|-----------|-------------|
| CNPG | PostgreSQL backend (required dependency) |
| External Secrets (ESO) | Credential management |
| Velero | Backup via CNPG WAL archiving |

## Why FerretDB (Not MongoDB)

| Aspect | MongoDB Community | FerretDB |
|--------|-------------------|----------|
| License | SSPL | Apache 2.0 |
| Replication | Requires Debezium + Kafka CDC | CNPG WAL streaming (native) |
| Operational overhead | Separate operator | Uses existing CNPG |
| ACID | Limited | Full PostgreSQL ACID |

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ferretdb
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/ferretdb
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
