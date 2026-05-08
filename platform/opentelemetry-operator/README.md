# bp-opentelemetry-operator

**Status**: Phase-0 scaffold (#1095 slice H5). Activated by EPIC-5 (#1100).
**Updated**: 2026-05-08

The OpenTelemetry Operator. Provides the `Instrumentation` CRD that
auto-injects OTel SDK sidecars into Pods based on annotations:

```yaml
metadata:
  annotations:
    instrumentation.opentelemetry.io/inject-java: "true"
    # or inject-dotnet / inject-nodejs / inject-python
```

When the annotation is set, the operator's mutating admission webhook
adds an init container that copies the OTel SDK into a shared volume and
edits the main container's env vars (`OTEL_EXPORTER_OTLP_ENDPOINT`,
`OTEL_RESOURCE_ATTRIBUTES`, `OTEL_TRACES_EXPORTER`, etc.) to point at the
collector deployed by `bp-opentelemetry`.

This Blueprint is **separate from** `bp-opentelemetry`. The latter is the
collector (DaemonSet/Deployment scraping + forwarding to Tempo/Loki/Mimir);
this one is the operator that injects per-Pod instrumentation. Two
distinct upgrade cycles, two distinct opt-ins.

## What it ships

| Template | Effect |
|---|---|
| Upstream `opentelemetry-operator` Helm subchart | The operator Pod + Instrumentation CRD. |
| `instrumentation-default.yaml` | A default `Instrumentation` CR named `default` in each Org namespace. Operator + per-Org overlays opt in to Java/.NET/Node/Python auto-injection. |

## Activation contract

```yaml
# values.yaml override (or per-Sovereign overlay)
enabled: true
defaultInstrumentation:
  enabled: true
  # Where the auto-injected SDK ships traces/logs/metrics. The collector
  # Service is created by bp-opentelemetry; this references it.
  exporter:
    endpoint: http://opentelemetry-collector.monitoring.svc:4317
  java: { enabled: true, image: "ghcr.io/open-telemetry/opentelemetry-operator/autoinstrumentation-java:latest" }
  nodejs: { enabled: true }
  python: { enabled: true }
  dotnet: { enabled: false }
```

When `enabled: false` (the default), no resources render — installing
this chart is a no-op until the operator opts in.

## Why default-OFF

1. The Operator's mutating admission webhook intercepts every Pod creation
   in the cluster. A misconfigured CR can break workloads cluster-wide.
2. The Instrumentation CR ties traces to a collector endpoint —
   `bp-opentelemetry` (collector) must be reconciled FIRST and reachable
   on the configured Service URL.
3. EPIC-5 (#1100) sequences both: collector first, exporters wired
   (Tempo/Loki/Mimir), then operator + Instrumentation CR.

## References

- docs/EPICS-1-6-unified-design.md §3.9 row 5 + §8.4 (EPIC-5)
- platform/opentelemetry/README.md — the collector
- Upstream: https://github.com/open-telemetry/opentelemetry-operator
