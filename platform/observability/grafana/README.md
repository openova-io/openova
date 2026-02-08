# Grafana Stack

LGTM observability stack for OpenOva platform.

**Status:** Accepted | **Updated:** 2026-01-17

---

## Overview

The Grafana Stack provides unified observability with:
- **Loki** - Log aggregation
- **Grafana** - Visualization
- **Tempo** - Distributed tracing
- **Mimir** - Metrics storage
- **Alloy** - Telemetry collection

---

## Architecture

```mermaid
flowchart TB
    subgraph Apps["Applications"]
        App1[App 1]
        App2[App 2]
        OTel[OTel SDK]
    end

    subgraph Alloy["Grafana Alloy"]
        Collector[Telemetry Collector]
    end

    subgraph Storage["Storage Layer"]
        Loki[Loki<br/>Logs]
        Tempo[Tempo<br/>Traces]
        Mimir[Mimir<br/>Metrics]
    end

    subgraph Tier["Tiered Storage"]
        Hot[Hot: Local]
        Cold[Cold: MinIO]
        Archive[Archive: R2]
    end

    subgraph UI["Visualization"]
        Grafana[Grafana]
    end

    App1 --> Collector
    App2 --> Collector
    OTel --> Collector
    Collector --> Loki
    Collector --> Tempo
    Collector --> Mimir
    Loki --> Hot
    Hot --> Cold
    Cold --> Archive
    Grafana --> Loki
    Grafana --> Tempo
    Grafana --> Mimir
```

---

## Components

| Component | Purpose | Memory |
|-----------|---------|--------|
| Grafana Alloy | Telemetry collection (OTLP, Prometheus) | 256MB |
| Loki | Log aggregation | 512MB |
| Tempo | Distributed tracing | 256MB |
| Mimir | Metrics storage | 512MB |
| Grafana | Visualization | 256MB |

---

## Tiered Storage

```mermaid
flowchart LR
    subgraph Hot["Hot (7 days)"]
        Local[Local PV]
    end

    subgraph Warm["Warm (30 days)"]
        MinIO[MinIO]
    end

    subgraph Cold["Cold (1 year)"]
        R2[Cloudflare R2]
    end

    Local -->|"After 7d"| MinIO
    MinIO -->|"After 30d"| R2
```

| Tier | Duration | Storage |
|------|----------|---------|
| Hot | 0-7 days | Local PV |
| Warm | 7-30 days | MinIO |
| Cold | 30d-1 year | Cloudflare R2 |

---

## Configuration

### Alloy Collector

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: alloy-config
  namespace: monitoring
data:
  config.alloy: |
    otelcol.receiver.otlp "default" {
      grpc { endpoint = "0.0.0.0:4317" }
      http { endpoint = "0.0.0.0:4318" }
    }

    otelcol.exporter.loki "default" {
      forward_to = [loki.write.default.receiver]
    }

    otelcol.exporter.otlp "tempo" {
      client { endpoint = "tempo.monitoring.svc:4317" }
    }

    prometheus.scrape "pods" {
      targets = discovery.kubernetes.pods.targets
      forward_to = [prometheus.remote_write.mimir.receiver]
    }
```

### Loki with S3 Backend

```yaml
loki:
  schemaConfig:
    configs:
      - from: 2024-01-01
        store: tsdb
        object_store: s3
        schema: v13

  storage:
    type: s3
    s3:
      endpoint: minio.storage.svc:9000
      bucketnames: loki-data
      access_key_id: ${MINIO_ACCESS_KEY}
      secret_access_key: ${MINIO_SECRET_KEY}
```

---

## OpenTelemetry Integration

Applications send telemetry via OTLP:

```yaml
# OTel auto-instrumentation
apiVersion: opentelemetry.io/v1alpha1
kind: Instrumentation
metadata:
  name: default
  namespace: <tenant>
spec:
  exporter:
    endpoint: http://alloy.monitoring.svc:4317
  propagators:
    - tracecontext
    - baggage
```

---

## Dashboards

| Dashboard | Purpose |
|-----------|---------|
| Platform Overview | Request rates, latencies, errors |
| Cilium Network | Traffic flows, policy drops |
| Flux GitOps | Reconciliation status |
| CNPG Postgres | Database performance |
| AI Hub Overview | LLM inference metrics |
| GPU Metrics | Utilization, memory, temperature |

---

## Alerting

Alerts flow through Alertmanager to Gitea Actions:

```mermaid
flowchart LR
    Mimir[Mimir] -->|"Alert Rules"| AM[Alertmanager]
    AM -->|"Webhook"| GA[Gitea Actions]
    GA -->|"Auto-Remediation"| K8s[Kubernetes]
```

---

*Part of [OpenOva](https://openova.io)*
