# OpenMeter

Usage metering. **Application Blueprint** (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.8 — Identity & metering). Used by `bp-fingate` (Open Banking) to meter API calls per TPP for monetization; available to any Organization that wants per-API-call metering.

**Status:** Accepted | **Updated:** 2026-04-27

---

## Overview

OpenMeter provides real-time usage metering:
- CloudEvents-based ingestion
- ClickHouse backend for analytics
- Integration with customer billing systems
- Real-time usage dashboards

---

## Architecture

```mermaid
flowchart TB
    subgraph Sources["Event Sources"]
        API[Open Banking API]
        Gateway[API Gateway]
    end

    subgraph OpenMeter["OpenMeter"]
        Ingest[Ingest API]
        Process[Event Processor]
        Query[Query API]
    end

    subgraph Storage["Storage"]
        Kafka[Kafka]
        CH[ClickHouse]
    end

    subgraph Consumers["Consumers"]
        Grafana[Grafana]
        Billing[Customer Billing]
    end

    API --> Ingest
    Gateway --> Ingest
    Ingest --> Kafka
    Kafka --> Process
    Process --> CH
    Query --> CH
    Billing --> Query
    Grafana --> Query
```

---

## Event Format (CloudEvents)

```json
{
  "specversion": "1.0",
  "type": "api.call",
  "source": "open-banking-api",
  "id": "uuid-here",
  "time": "2024-01-15T10:30:00Z",
  "subject": "tpp-12345",
  "data": {
    "endpoint": "/accounts",
    "method": "GET",
    "status_code": 200,
    "response_time_ms": 45
  }
}
```

---

## Configuration

### OpenMeter Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: openmeter
  namespace: open-banking
spec:
  template:
    spec:
      containers:
        - name: openmeter
          image: openmeter/openmeter:v1.0.0
          env:
            - name: OPENMETER_KAFKA_BROKER
              value: kafka-kafka-bootstrap.databases.svc:9092
            - name: OPENMETER_CLICKHOUSE_ADDRESS
              value: clickhouse.databases.svc:9000
            - name: OPENMETER_POSTGRES_URL
              valueFrom:
                secretKeyRef:
                  name: openmeter-db-credentials
                  key: url
```

### Meter Definition

```yaml
meters:
  - slug: api_calls
    description: API call count per TPP
    eventType: api.call
    aggregation: COUNT
    groupBy:
      subject: true
      endpoint: $.data.endpoint
      method: $.data.method

  - slug: api_latency
    description: API latency percentiles
    eventType: api.call
    valueProperty: $.data.response_time_ms
    aggregation: SUM
    groupBy:
      subject: true
      endpoint: $.data.endpoint
```

---

## Billing Integration

OpenMeter exposes usage data via its Query API. Customer billing systems consume aggregated usage for invoicing. Billing integration is customer-specific and not bundled into the platform.

---

## Quota Checking (Real-Time)

For prepaid credits, Valkey provides real-time quota checks:

```mermaid
flowchart LR
    subgraph RealTime["Real-Time Path"]
        API[API Gateway]
        Valkey[Valkey]
    end

    subgraph Metering["Metering Path"]
        OM[OpenMeter]
        Billing[Customer Billing]
    end

    API -->|"Check quota"| Valkey
    API -->|"Record event"| OM
    OM -->|"Sync usage"| Billing
    Billing -->|"Update credits"| Valkey
```

---

## Monitoring

| Metric | Description |
|--------|-------------|
| `openmeter_events_ingested_total` | Total events ingested |
| `openmeter_events_processed_total` | Events processed |
| `openmeter_query_latency_seconds` | Query latency |

---

*Part of [OpenOva](https://openova.io)*
