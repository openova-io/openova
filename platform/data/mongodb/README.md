# MongoDB

Document database for OpenOva platform.

**Status:** Accepted | **Updated:** 2026-01-17

---

## Overview

MongoDB provides document database capabilities with:
- CDC (Change Data Capture) via Debezium for DR
- Replication through Redpanda
- Schema-flexible document storage
- Aggregation pipeline for analytics

---

## Architecture

### Single Region

```mermaid
flowchart TB
    subgraph MongoDB["MongoDB Replica Set"]
        Primary[Primary]
        Secondary1[Secondary 1]
        Secondary2[Secondary 2]
    end

    App[Applications] --> Primary
    Primary -->|"Replicate"| Secondary1
    Primary -->|"Replicate"| Secondary2
```

### Multi-Region DR (CDC)

```mermaid
flowchart TB
    subgraph Region1["Region 1"]
        MG1[MongoDB Primary]
        Debezium[Debezium]
        RP1[Redpanda]
    end

    subgraph Region2["Region 2"]
        RP2[Redpanda]
        Sink[Sink Connector]
        MG2[MongoDB DR]
    end

    MG1 -->|"CDC"| Debezium
    Debezium --> RP1
    RP1 -->|"MirrorMaker2"| RP2
    RP2 --> Sink
    Sink --> MG2
```

---

## Why CDC Instead of Native Replication

| Aspect | Native Replication | CDC via Debezium |
|--------|-------------------|------------------|
| Cross-region | Complex, latency sensitive | Works over async message queue |
| Network tolerance | Requires stable connection | Tolerates partitions |
| Flexibility | MongoDB to MongoDB only | Can transform/filter |
| Ordering | Strict global | Per-document ordering |

**Decision:** Use CDC for multi-region DR to leverage existing Redpanda infrastructure and improve fault tolerance.

---

## Configuration

### MongoDB Deployment

```yaml
apiVersion: mongodbcommunity.mongodb.com/v1
kind: MongoDBCommunity
metadata:
  name: mongodb
  namespace: databases
spec:
  members: 3
  type: ReplicaSet
  version: "7.0.5"
  security:
    authentication:
      modes: ["SCRAM"]
  users:
    - name: admin
      db: admin
      passwordSecretRef:
        name: mongodb-admin-password
      roles:
        - name: root
          db: admin
    - name: debezium
      db: admin
      passwordSecretRef:
        name: mongodb-debezium-password
      roles:
        - name: read
          db: local
        - name: readWrite
          db: <tenant>
```

### Debezium Connector

```yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: mongodb-source
  namespace: databases
spec:
  class: io.debezium.connector.mongodb.MongoDbConnector
  tasksMax: 1
  config:
    mongodb.connection.string: mongodb://debezium:${password}@mongodb.databases.svc:27017
    mongodb.name: openova
    collection.include.list: "<tenant>.*"
    capture.mode: change_streams_update_full
    topic.prefix: cdc.mongodb
```

### Sink Connector (DR Region)

```yaml
apiVersion: kafka.strimzi.io/v1beta2
kind: KafkaConnector
metadata:
  name: mongodb-sink
  namespace: databases
spec:
  class: com.mongodb.kafka.connect.MongoSinkConnector
  tasksMax: 1
  config:
    connection.uri: mongodb://admin:${password}@mongodb.databases.svc:27017
    database: <tenant>
    topics.regex: cdc.mongodb.<tenant>.*
    change.data.capture.handler: com.mongodb.kafka.connect.sink.cdc.debezium.DebeziumCdcHandler
```

---

## Failover

### Automatic (Within Region)

MongoDB replica set handles automatic primary election.

### Manual (Cross-Region)

1. Stop CDC pipeline
2. Verify DR data is current
3. Promote DR MongoDB to primary
4. Update application connection strings
5. (Optional) Reverse CDC direction

---

## Monitoring

| Metric | Description |
|--------|-------------|
| `mongodb_connections` | Active connections |
| `mongodb_op_latencies_latency` | Operation latency |
| `mongodb_repl_lag` | Replication lag |
| `debezium_streaming_lag` | CDC lag |

---

*Part of [OpenOva](https://openova.io)*
