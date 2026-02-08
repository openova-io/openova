# Velero

Kubernetes backup to Archival S3 for disaster recovery.

**Status:** Accepted | **Updated:** 2026-02-07

---

## Overview

Velero provides Kubernetes-native backup with flexible storage backend options. Backups are stored in **Archival S3** (external storage), not in-cluster MinIO.

```mermaid
flowchart TB
    subgraph K8s["Kubernetes Cluster"]
        Velero[Velero]
        Apps[Applications]
        PVs[Persistent Volumes]
    end

    subgraph Archival["Archival S3 Options"]
        R2[Cloudflare R2]
        S3[AWS S3]
        GCS[GCP GCS]
        Hetzner[Hetzner Object Storage]
        OCI[OCI Object Storage]
    end

    Apps --> Velero
    PVs --> Velero
    Velero -->|"Backup"| Archival
```

---

## Why Archival S3?

| Storage | Purpose | Use for Backup? |
|---------|---------|-----------------|
| MinIO | Fast in-cluster S3 | **No** |
| Archival S3 | External cold storage | **Yes** |

**Velero backs up to Archival S3, not MinIO.**

Reason: Backups must survive cluster failure. MinIO is inside the cluster.

---

## Storage Backend Options

| Provider | Availability | Egress Fees | Notes |
|----------|--------------|-------------|-------|
| **Cloud Provider Storage** | Default | Varies | Hetzner, OCI, Huawei OBS |
| Cloudflare R2 | Always available | **Free** | Zero egress, multi-cloud friendly |
| AWS S3 | Available | $0.09/GB | Full featured |
| GCP GCS | Available | $0.12/GB | Full featured |

**Default:** Cloud provider's object storage (Hetzner Object Storage, OCI Object Storage, etc.)

**Alternative:** Cloudflare R2 for zero egress fees, useful for multi-cloud or egress-heavy scenarios.

---

## Configuration

### Cloudflare R2 (Zero Egress)

```yaml
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: r2-backup
  namespace: velero
spec:
  provider: aws
  bucket: <tenant>-backups
  config:
    region: auto
    s3ForcePathStyle: "true"
    s3Url: https://<account-id>.r2.cloudflarestorage.com
  credential:
    name: r2-credentials
    key: cloud
```

### AWS S3

```yaml
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: s3-backup
  namespace: velero
spec:
  provider: aws
  bucket: <tenant>-backups
  config:
    region: us-east-1
  credential:
    name: aws-credentials
    key: cloud
```

### GCP GCS

```yaml
apiVersion: velero.io/v1
kind: BackupStorageLocation
metadata:
  name: gcs-backup
  namespace: velero
spec:
  provider: gcp
  bucket: <tenant>-backups
  credential:
    name: gcp-credentials
    key: cloud
```

---

## Backup Schedule

```yaml
apiVersion: velero.io/v1
kind: Schedule
metadata:
  name: daily-backup
  namespace: velero
spec:
  schedule: "0 2 * * *"  # Daily at 2 AM
  template:
    includedNamespaces:
      - "*"
    excludedNamespaces:
      - velero
      - kube-system
    includedResources:
      - "*"
    excludedResources:
      - events
      - events.events.k8s.io
    storageLocation: r2-backup
    ttl: 720h  # 30 days
```

### Backup Strategy

| Resource | Schedule | Retention |
|----------|----------|-----------|
| All namespaces | Daily 2 AM | 30 days |
| Databases (labels) | Hourly | 7 days |
| Secrets | Daily | 90 days |
| PVs (snapshots) | Daily | 14 days |

---

## Multi-Region Backup

```mermaid
flowchart TB
    subgraph Region1["Region 1"]
        V1[Velero]
        K1[Kubernetes]
    end

    subgraph Region2["Region 2"]
        V2[Velero]
        K2[Kubernetes]
    end

    subgraph Archival["Archival S3"]
        Bucket[Shared Bucket<br/>or Cross-Region Replication]
    end

    V1 -->|"Backup"| Bucket
    V2 -->|"Backup"| Bucket
    Bucket -->|"Restore"| V1
    Bucket -->|"Restore"| V2
```

Both regions can:
- Backup to same bucket (different prefixes)
- Restore from either region's backups
- Use for cross-region disaster recovery

---

## Restore Procedure

```mermaid
sequenceDiagram
    participant Op as Operator
    participant Velero as Velero
    participant S3 as Archival S3
    participant K8s as Kubernetes

    Op->>Velero: velero restore create
    Velero->>S3: Fetch backup
    S3->>Velero: Return backup data
    Velero->>K8s: Restore resources
    Velero->>K8s: Restore PV data
    K8s->>Op: Restoration complete
```

### Commands

```bash
# List available backups
velero backup get

# Restore entire backup
velero restore create --from-backup daily-backup-20260116

# Restore specific namespace
velero restore create --from-backup daily-backup-20260116 \
  --include-namespaces databases

# Restore to different namespace
velero restore create --from-backup daily-backup-20260116 \
  --include-namespaces databases \
  --namespace-mappings databases:databases-restored
```

---

## Operations

### Check Backup Status

```bash
# List backups
velero backup get

# Describe specific backup
velero backup describe daily-backup-20260116

# Check backup logs
velero backup logs daily-backup-20260116
```

### Verify Backup Location

```bash
# Check backup storage locations
velero backup-location get

# Verify connection
velero backup-location check r2-backup
```

### Manual Backup

```bash
# Create manual backup
velero backup create manual-backup-$(date +%Y%m%d)

# Backup specific namespace
velero backup create db-backup-$(date +%Y%m%d) \
  --include-namespaces databases
```

---

## Consequences

**Positive:**
- K8s-native backup
- Flexible storage backends
- Zero egress with Cloudflare R2
- Cross-region restore capability
- Incremental backups

**Negative:**
- Requires external S3 (by design)
- PV backup requires CSI snapshots
- Large restores take time

---

*Part of [OpenOva](https://openova.io)*
