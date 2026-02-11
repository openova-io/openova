# Apache Superset

Business intelligence and data visualization platform.

**Status:** Accepted | **Updated:** 2026-02-09

---

## Overview

Apache Superset is an enterprise-grade business intelligence platform that replaces proprietary tools like Tableau and Power BI for many use cases. It provides rich interactive dashboards, a powerful SQL exploration interface (SQL Lab), and a no-code chart builder that makes data accessible to both analysts and business users. Superset connects to any database that speaks SQL, making it the ideal visualization layer for the Trino query engine.

Within OpenOva, Superset serves as the user-facing analytics interface for the **Titan** data lakehouse product. Analysts connect Superset to Trino, which in turn federates queries across Iceberg tables on MinIO, PostgreSQL, MongoDB, and other sources. This architecture gives users a single pane of glass for all their data, with the full power of Trino's distributed query engine behind every chart and dashboard.

Superset is fully self-hosted, ensuring data sovereignty. It supports role-based access control, row-level security, and integration with Keycloak for SSO, making it suitable for multi-tenant enterprise deployments.

---

## Architecture

```mermaid
flowchart TB
    subgraph Users["Users"]
        Analyst[Data Analysts]
        Executive[Business Users]
        Engineer[Data Engineers]
    end

    subgraph Superset["Apache Superset"]
        UI[Dashboard UI]
        SQLLab[SQL Lab]
        Charts[Chart Builder]
        Cache[Result Cache]
    end

    subgraph Backend["Backend"]
        Metadata[Metadata DB / CNPG]
        Valkey[Valkey Cache]
        Celery[Celery Workers]
    end

    subgraph DataSources["Data Sources via Trino"]
        Trino[Trino Coordinator]
        Iceberg[Iceberg Tables]
        PG[PostgreSQL]
    end

    Users --> UI
    Users --> SQLLab
    UI --> Charts
    Charts --> Cache
    Cache --> Valkey
    Charts --> Trino
    SQLLab --> Trino
    Celery --> Trino
    Superset --> Metadata
    Trino --> Iceberg
    Trino --> PG
```

---

## Key Features

| Feature | Description |
|---------|-------------|
| Interactive Dashboards | Drag-and-drop dashboard builder with cross-filtering |
| SQL Lab | Full SQL IDE with autocomplete, query history, and saved queries |
| 40+ Chart Types | Bar, line, pie, geographic maps, heatmaps, pivot tables, and more |
| No-Code Exploration | Explore datasets visually without writing SQL |
| Row-Level Security | Restrict data access per user or role at the row level |
| Alerts and Reports | Scheduled dashboard snapshots delivered via email or Slack |
| Embedded Analytics | Embed charts and dashboards into external applications |
| Caching | Query result caching via Valkey for responsive dashboards |

---

## Configuration

### Helm Values

```yaml
superset:
  replicas: 2

  image:
    repository: apache/superset
    tag: "4.1.0"

  config:
    SQLALCHEMY_DATABASE_URI: "postgresql://superset:${PG_PASSWORD}@titan-postgres.databases.svc:5432/superset"
    SECRET_KEY: "${SUPERSET_SECRET_KEY}"  # From ESO

    # Valkey cache
    CACHE_CONFIG:
      CACHE_TYPE: RedisCache
      CACHE_REDIS_URL: "redis://valkey.cache.svc:6379/0"

    # Celery for async queries and scheduled reports
    CELERY_CONFIG:
      broker_url: "redis://valkey.cache.svc:6379/1"
      result_backend: "redis://valkey.cache.svc:6379/2"

    # Result backend for SQL Lab
    RESULTS_BACKEND:
      CACHE_TYPE: RedisCache
      CACHE_REDIS_URL: "redis://valkey.cache.svc:6379/3"

  resources:
    requests:
      cpu: 1
      memory: 2Gi
    limits:
      cpu: 2
      memory: 4Gi

  worker:
    replicas: 2
    resources:
      requests:
        cpu: 500m
        memory: 1Gi
```

### Trino Database Connection

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: superset-datasources
  namespace: data-lakehouse
data:
  datasources.yaml: |
    databases:
      - database_name: Titan Lakehouse
        sqlalchemy_uri: trino://trino-coordinator.data-lakehouse.svc:8080/iceberg
        expose_in_sqllab: true
        allow_ctas: false
        allow_cvas: false
        extra: |
          {
            "engine_params": {
              "connect_args": {
                "http_scheme": "http"
              }
            }
          }
```

---

## SSO with Keycloak

```python
# superset_config.py
from flask_appbuilder.security.manager import AUTH_OAUTH

AUTH_TYPE = AUTH_OAUTH
OAUTH_PROVIDERS = [
    {
        "name": "keycloak",
        "icon": "fa-key",
        "token_key": "access_token",
        "remote_app": {
            "client_id": "superset",
            "client_secret": "${KEYCLOAK_CLIENT_SECRET}",
            "api_base_url": "https://keycloak.<domain>/realms/titan/protocol/openid-connect",
            "access_token_url": "https://keycloak.<domain>/realms/titan/protocol/openid-connect/token",
            "authorize_url": "https://keycloak.<domain>/realms/titan/protocol/openid-connect/auth",
            "server_metadata_url": "https://keycloak.<domain>/realms/titan/.well-known/openid-configuration",
            "client_kwargs": {
                "scope": "openid email profile"
            },
        },
    }
]

# Map Keycloak roles to Superset roles
AUTH_ROLE_ADMIN = "Admin"
AUTH_ROLE_PUBLIC = "Public"
AUTH_USER_REGISTRATION = True
AUTH_USER_REGISTRATION_ROLE = "Gamma"
```

---

## Row-Level Security

Restrict data visibility per user or role.

```sql
-- Example: Users only see their own region's data
-- Configure in Superset Admin > Row Level Security
-- Clause: region = '{{ current_user().region }}'
```

| Role | Filter | Effect |
|------|--------|--------|
| EMEA Analyst | `region = 'EMEA'` | Only sees European data |
| APAC Analyst | `region = 'APAC'` | Only sees Asia-Pacific data |
| Global Admin | (none) | Sees all data |

---

## Alerts and Scheduled Reports

```yaml
# CronJob for scheduled dashboard snapshots
apiVersion: batch/v1
kind: CronJob
metadata:
  name: superset-reports
  namespace: data-lakehouse
spec:
  schedule: "0 8 * * 1-5"  # Weekdays at 8 AM
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: superset-reports
              image: apache/superset:4.1.0
              command:
                - superset
                - alerts-and-reports
                - trigger
```

---

## Monitoring

| Metric | Description |
|--------|-------------|
| `superset_query_count` | Total queries executed |
| `superset_query_duration_seconds` | Query execution time |
| `superset_cache_hit_ratio` | Percentage of cache hits |
| `superset_active_users` | Concurrent active users |
| `superset_dashboard_views` | Dashboard view count |
| `superset_celery_task_duration` | Async task duration |

---

## Consequences

**Positive:**
- Replaces expensive proprietary BI tools (Tableau, Power BI) with open-source
- SQL Lab provides a powerful exploration environment for data engineers
- Rich visualization library covers the majority of enterprise reporting needs
- Row-level security and SSO integration meet enterprise compliance requirements
- Caching via Valkey ensures responsive dashboards even with large datasets
- Embedded analytics enables building data products on top of Superset

**Negative:**
- Requires metadata database (PostgreSQL) and cache (Valkey) as dependencies
- Complex dashboard designs can lead to slow load times without proper caching
- Custom visualisation plugins require React/JavaScript development
- Alert and reporting features require Celery workers and additional resources
- Self-hosted model requires operational investment for upgrades and maintenance

---

*Part of [OpenOva Titan](https://openova.io) - Data Lakehouse*
