# bp-openmeter

Real-time CloudEvents usage metering. Catalyst Application Blueprint —
slot 45 of the omantel-1 bootstrap-kit. See
[`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.8
(Identity & metering).

**Status:** Accepted | **Updated:** 2026-04-30

---

## Overview

OpenMeter ingests CloudEvents, deterministically aggregates per-subject
usage, and exposes an OpenAPI Query endpoint for downstream billing.
Used by `bp-fingate` (Open Banking) to meter API calls per TPP for
monetization, and available to any Application that needs per-event
usage tracking.

## Catalyst profile (omantel-1) — ClickHouse-less

Per [`docs/BOOTSTRAP-KIT-EXPANSION-PLAN.md`](../../docs/BOOTSTRAP-KIT-EXPANSION-PLAN.md)
§6.4, omantel-1 ships OpenMeter without the bundled ClickHouse cluster:

- **Aggregation backend:** `bp-cnpg` (PostgreSQL materialized views)
- **Event bus:** `bp-nats-jetstream` (raw event subject)
- **Cache:** `bp-valkey` (Redis-compatible) — operator overlay supplies
  the connection string

The chart-level toggle that records the active profile is
`catalystBlueprint.backend.kind` in `chart/values.yaml` (default `cnpg`).
On a host cluster that later adds `bp-clickhouse`, the operator re-rolls
the Application with `backend.kind: clickhouse` plus a per-Sovereign
overlay supplying `openmeter.config.aggregation.clickhouse.address`.

## Chart shape

```
platform/openmeter/
├── blueprint.yaml                     # Catalyst Blueprint CRD
├── chart/
│   ├── Chart.yaml                     # umbrella; deps: openmeter (OCI)
│   ├── values.yaml                    # ClickHouse-less profile defaults
│   └── templates/
│       ├── _helpers.tpl
│       ├── networkpolicy.yaml         # default OFF
│       ├── servicemonitor.yaml        # default OFF (CRD-gated)
│       └── hpa.yaml                   # default OFF
├── chart/tests/observability-toggle.sh
└── README.md
```

## Dependencies

| Blueprint | Purpose |
|-----------|---------|
| `bp-cnpg` | aggregation backend (CNPG materialized views) |
| `bp-nats-jetstream` | raw event subject |
| `bp-cert-manager` | ingress TLS via ClusterIssuer |

## Observability toggles (all default OFF)

Per [`docs/BLUEPRINT-AUTHORING.md`](../../docs/BLUEPRINT-AUTHORING.md)
§11.2, every observability surface defaults `false`. Operator opts in
via per-cluster overlay once `bp-kube-prometheus-stack` reconciles.

| Toggle | Default | Why |
|--------|---------|-----|
| `serviceMonitor.enabled` | `false` | `monitoring.coreos.com/v1` CRD ships with kube-prometheus-stack |
| `networkPolicy.enabled` | `false` | Operator supplies consumer-namespace selectors per-Sovereign |
| `hpa.enabled` | `false` | Solo-Sovereign baseline is a single API replica |

## Verification

```bash
helm dependency update platform/openmeter/chart
helm template platform/openmeter/chart | grep -E "^kind:" | sort -u
helm lint platform/openmeter/chart
bash platform/openmeter/chart/tests/observability-toggle.sh
```

---

*Part of [OpenOva](https://openova.io). Closes #272.*
