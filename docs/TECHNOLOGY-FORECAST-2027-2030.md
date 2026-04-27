# Technology Forecast 2027-2030

Component technology assessment and strategic forecast for the OpenOva platform.

**Status:** Accepted | **Updated:** 2026-02-26

---

## Overview

This document provides a forward-looking assessment of all 52 platform components, evaluating their relevance trajectory through 2027 and 2030 in the context of AI-driven development, regulatory evolution, and cloud-native ecosystem maturation.

---

## Scoring Methodology

Each component is scored on a 0-100 scale across three time horizons:
- **2026 (Current):** Today's relevance and essentiality
- **2027 (Near-term):** Expected relevance in 12 months
- **2030 (Long-term):** Expected relevance in 4 years

Factors considered: AI replacement risk, regulatory demand, ecosystem maturity, operational complexity, and community momentum.

---

## Mandatory Components (26)

> **Classification basis:** "Mandatory" = installed on every Sovereign — comprises the Catalyst control plane (per [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §2) plus per-host-cluster infrastructure (§3). "A La Carte" below = Application Blueprints (§4) that customers opt into per Environment.

| Component | 2026 | 2027 | 2030 | Trend | Notes |
|-----------|------|------|------|-------|-------|
| cert-manager | 95 | 95 | 95 | Stable | TLS automation is evergreen |
| cilium | 95 | 96 | 95 | Stable | eBPF dominance continues |
| external-secrets | 95 | 95 | 95 | Stable | Secrets management is foundational |
| openbao | 93 | 93 | 90 | Stable | May face cloud-native alternatives |
| flux | 92 | 92 | 88 | Stable | GitOps is established pattern |
| minio | 92 | 92 | 90 | Stable | S3-compatible storage remains essential |
| velero | 92 | 90 | 88 | Stable | Backup is non-negotiable |
| harbor | 90 | 92 | 92 | Rising | Supply chain security drives adoption |
| falco | 90 | 92 | 93 | Rising | Runtime security more critical with AI workloads |
| trivy | 90 | 90 | 88 | Stable | Complemented by Syft/Grype for SBOM |
| sigstore | 90 | 93 | 95 | Rising | Regulatory mandates drive adoption (EU CRA) |
| syft-grype | 90 | 92 | 94 | Rising | SBOM requirements accelerating |
| coraza | 88 | 88 | 85 | Stable | WAF remains necessary for web apps |
| external-dns | 90 | 88 | 85 | Stable | DNS synchronization is mechanical |
| grafana | 88 | 88 | 85 | Stable | AI may generate dashboards but metrics collection stays |
| kyverno | 88 | 90 | 90 | Rising | Policy-as-code increasingly mandated |
| crossplane | 78 | 80 | 82 | Rising | Cloud resource management via CRDs maturing |
| opentofu | 82 | 80 | 75 | Declining | Bootstrap-only; Crossplane takes over day-2 |
| gitea | 83 | 82 | 78 | Stable | Self-hosted Git remains important |
| k8gb | 80 | 80 | 78 | Stable | DNS-based GSLB is proven |
| keda | 80 | 82 | 85 | Rising | Event-driven autoscaling more relevant with AI |
| vpa | 78 | 78 | 75 | Stable | Right-sizing is ongoing need |
| reloader | 80 | 80 | 78 | Stable | Simple operator, high value |
| failover-controller | 82 | 82 | 80 | Stable | Multi-region failover always needed |
| keycloak | 85 | 85 | 85 | Stable | Catalyst control-plane identity — per-Org realms in SME, per-Sovereign realm in corporate |

### Note on OpenTelemetry

OpenTelemetry is mandatory but has no separate platform directory - it is deployed as part of the observability stack via Grafana Alloy configuration.

---

## A La Carte Components (26)

| Component | 2026 | 2027 | 2030 | Trend | Notes |
|-----------|------|------|------|-------|-------|
| vllm | 95 | 96 | 95 | Stable | LLM inference is essential for AI |
| kserve | 88 | 90 | 90 | Rising | Model serving standardizing |
| milvus | 88 | 90 | 90 | Rising | Vector search increasingly critical |
| cnpg | 90 | 90 | 90 | Stable | PostgreSQL operator is proven |
| opensearch | 50 | 55 | 60 | Rising | Application Blueprint — opt-in for SIEM (paired with ClickHouse + bp-specter) |
| valkey | 85 | 85 | 85 | Stable | Caching is evergreen |
| nemo-guardrails | 90 | 92 | 93 | Rising | AI safety regulations expanding |
| langfuse | 90 | 92 | 90 | Rising | LLM observability maturing |
| llm-gateway | 87 | 88 | 85 | Stable | Claude Code access proxy |
| anthropic-adapter | 85 | 85 | 80 | Stable | API translation layer |
| bge | 82 | 82 | 78 | Stable | Embeddings are commoditizing |
| knative | 75 | 75 | 72 | Stable | Serverless for model serving |
| librechat | 75 | 75 | 70 | Stable | Chat UI may be AI-generated |
| ferretdb | 75 | 78 | 80 | Rising | MongoDB compatibility without SSPL |
| strimzi | 72 | 72 | 70 | Stable | Event streaming is established |
| debezium | 70 | 70 | 68 | Stable | CDC tied to streaming adoption |
| temporal | 68 | 72 | 75 | Rising | Saga orchestration gaining relevance |
| flink | 60 | 65 | 70 | Rising | Stream processing for real-time analytics |
| clickhouse | 55 | 60 | 65 | Rising | OLAP analytics with SIEM workloads |
| iceberg | 50 | 55 | 65 | Rising | Open table format adoption accelerating |
| stalwart | 70 | 70 | 68 | Stable | Self-hosted email is niche but needed |
| stunner | 68 | 70 | 72 | Rising | WebRTC gaining enterprise adoption |
| livekit | 72 | 75 | 78 | Rising | Video/audio infrastructure growing |
| matrix | 70 | 72 | 75 | Rising | Self-hosted chat gaining traction |
| neo4j | 65 | 68 | 70 | Rising | Knowledge graphs for RAG |
| litmus | 72 | 75 | 78 | Rising | Chaos engineering for compliance proof |
| openmeter | 55 | 58 | 60 | Stable | Usage metering is niche |

---

## Product Impact Analysis

### OpenOva Cortex (AI Hub)

**Trend: Strong growth.** AI infrastructure components are all stable or rising. Addition of NeMo Guardrails and LangFuse addresses critical gaps in AI safety and observability. Removal of Airflow, SearXNG, and LangServe simplifies the stack without losing capability (AI generates custom code).

### OpenOva Fingate (Open Banking)

**Trend: Stable.** Core components (Keycloak, OpenMeter) remain solid. Removal of Lago (billing is customer-specific) simplifies the product. PSD2/DORA regulatory pressure continues to drive demand.

### OpenOva Fabric (Data & Integration)

**Trend: Rising.** Merging Titan + Fuse into Fabric creates a stronger product. Strimzi, Flink, Temporal, and ClickHouse are all stable-to-rising. Removal of declining components (Airflow, Trino, Superset, Camel, Dapr, RabbitMQ, ActiveMQ) makes the product leaner and more focused.

### OpenOva Relay (Communication)

**Trend: Rising.** Self-hosted communication (email, video, chat) is a growing enterprise need driven by data sovereignty and compliance. All four components (Stalwart, LiveKit, STUNner, Matrix) are stable-to-rising.

### OpenOva Specter (AIOps)

**Trend: Strong growth.** AI-powered operations become more capable as LLM quality improves. SIEM/SOAR integration (Falco + OpenSearch + ClickHouse) provides rich context for autonomous incident response.

---

## Strategic Recommendations

### Components to Watch (potential additions 2027+)

| Component | Score | Rationale |
|-----------|-------|-----------|
| Ray | 80 | Distributed AI compute (training, batch) |
| MLflow | 78 | Model registry + experiment tracking |
| OpenCost | 75 | FinOps for K8s/GPU cost visibility |
| Flagger | 72 | Progressive delivery (canary, blue-green) |

### Risks to Monitor

| Risk | Impact | Mitigation |
|------|--------|------------|
| eBPF kernel API changes | Cilium compatibility | Track kernel LTS versions |
| OpenSearch license change | SIEM backend | Fork-ready posture |
| GPU supply constraints | Cortex deployment | Multi-GPU vendor support |
| EU CRA enforcement timeline | Supply chain security | Sigstore + Syft/Grype already in stack |

---

## Removed Components (Rationale)

| Component | Score (2026) | Why Removed |
|-----------|-------------|-------------|
| Backstage | 45 | Replaced by Catalyst console (the platform's own developer-facing UI) |
| MongoDB | 72 | Replaced by FerretDB on CNPG (no SSPL, simpler DR) |
| Airflow | 33 | Replaced by Flink + OTel (AI generates workflows) |
| Superset | 40 | AI-generated visualizations replace dashboard tools |
| Trino | 38 | ClickHouse + CNPG direct queries sufficient |
| LangServe | 73 | Custom RAG behind KServe (AI generates integration code) |
| SearXNG | 40 | LLM Gateway tool registry replaces meta-search |
| Camel K | 20 | AI generates integration code directly |
| Dapr | 30 | Sidecar overhead unnecessary; Kafka + custom code |
| RabbitMQ | 25 | Kafka covers event streaming |
| ActiveMQ | 12 | JMS legacy, no modern use case |
| Vitess | 15 | MySQL sharding is niche |
| Lago | 58 | Billing is customer-specific, not platform concern |

---

*Part of [OpenOva](https://openova.io)*
