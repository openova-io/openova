# OpenOva Cortex

Enterprise AI platform with LLM serving, RAG, and intelligent agents.

**Status:** Accepted | **Updated:** 2026-02-09

---

## Overview

OpenOva Cortex is an enterprise AI product that bundles AI/ML infrastructure components with custom services for enterprise AI deployments.

```mermaid
flowchart TB
    subgraph UI["User Interfaces"]
        LibreChat[LibreChat<br/>Chat UI]
        ClaudeCode[Claude Code]
        Airflow[Airflow<br/>Workflows]
    end

    subgraph Gateway["Gateway Layer"]
        LLMGateway[LLM Gateway]
        Adapter[Anthropic Adapter]
    end

    subgraph RAG["RAG Layer"]
        LangServe[LangServe<br/>RAG Service]
        Retrieval[Hybrid Retrieval]
    end

    subgraph Serving["Model Serving"]
        KServe[KServe]
        vLLM[vLLM]
    end

    subgraph Knowledge["Knowledge Layer"]
        Milvus[Milvus<br/>Vectors]
        Neo4j[Neo4j<br/>Graph]
    end

    subgraph Embeddings["Embeddings"]
        BGE[BGE-M3]
        Reranker[BGE-Reranker]
    end

    subgraph Search["Web Search"]
        SearXNG[SearXNG]
    end

    UI --> Gateway
    Gateway --> RAG
    RAG --> Serving
    RAG --> Knowledge
    RAG --> Embeddings
    RAG --> Search
    Serving --> vLLM
```

---

## Components

All components are in `platform/` (flat structure):

| Component | Purpose | Location |
|-----------|---------|----------|
| [llm-gateway](../../platform/llm-gateway/) | Subscription-based LLM access | platform/llm-gateway |
| [anthropic-adapter](../../platform/anthropic-adapter/) | Claude API translation | platform/anthropic-adapter |
| [knative](../../platform/knative/) | Serverless platform | platform/knative |
| [kserve](../../platform/kserve/) | Model serving | platform/kserve |
| [vllm](../../platform/vllm/) | LLM inference | platform/vllm |
| [milvus](../../platform/milvus/) | Vector database | platform/milvus |
| [neo4j](../../platform/neo4j/) | Graph database | platform/neo4j |
| [langserve](../../platform/langserve/) | RAG service | platform/langserve |
| [librechat](../../platform/librechat/) | Chat UI | platform/librechat |
| [airflow](../../platform/airflow/) | Workflow orchestration | platform/airflow |
| [searxng](../../platform/searxng/) | Web search | platform/searxng |
| [bge](../../platform/bge/) | Embeddings + reranking | platform/bge |

---

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                     User Interfaces                         │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────┐    │
│  │LibreChat │  │Claude    │  │ Airflow  │  │  Custom  │    │
│  │  (Chat)  │  │  Code    │  │(Workflow)│  │   Apps   │    │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────┬─────┘    │
└───────┼─────────────┼─────────────┼─────────────┼──────────┘
        │             │             │             │
        ▼             ▼             ▼             ▼
┌─────────────────────────────────────────────────────────────┐
│                    Gateway Layer                            │
│  ┌─────────────────────┐  ┌─────────────────────┐          │
│  │    LLM Gateway      │  │  Anthropic Adapter  │          │
│  │ (Subscription Proxy)│  │  (API Translation)  │          │
│  └──────────┬──────────┘  └──────────┬──────────┘          │
└─────────────┼────────────────────────┼─────────────────────┘
              │                        │
              ▼                        ▼
┌─────────────────────────────────────────────────────────────┐
│                     RAG Service (LangServe)                 │
│  ┌───────────┐  ┌───────────┐  ┌───────────┐  ┌─────────┐  │
│  │  Model    │  │  Hybrid   │  │  Session  │  │Document │  │
│  │  Router   │  │ Retrieval │  │  Cache    │  │ Ingest  │  │
│  └─────┬─────┘  └─────┬─────┘  └───────────┘  └─────────┘  │
└────────┼──────────────┼────────────────────────────────────┘
         │              │
         ▼              ▼
┌─────────────────────────────────────────────────────────────┐
│                    Model Serving                            │
│  ┌─────────────────────┐  ┌─────────────────────┐          │
│  │       KServe        │  │        vLLM         │          │
│  │   (Orchestration)   │  │     (Inference)     │          │
│  └─────────────────────┘  └─────────────────────┘          │
└─────────────────────────────────────────────────────────────┘
         │              │
         ▼              ▼
┌─────────────────────────────────────────────────────────────┐
│                   Knowledge Layer                           │
│  ┌─────────────────────┐  ┌─────────────────────┐          │
│  │       Milvus        │  │       Neo4j         │          │
│  │   (Vector Store)    │  │   (Graph Store)     │          │
│  └─────────────────────┘  └─────────────────────┘          │
└─────────────────────────────────────────────────────────────┘
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│                   Embedding Layer                           │
│  ┌─────────────────────┐  ┌─────────────────────┐          │
│  │       BGE-M3        │  │    BGE-Reranker     │          │
│  │    (Embeddings)     │  │  (Cross-Encoder)    │          │
│  └─────────────────────┘  └─────────────────────┘          │
└─────────────────────────────────────────────────────────────┘
```

---

## Agent Presets

| Agent | Purpose | Retrieval |
|-------|---------|-----------|
| **Deep Thinker** | Complex reasoning with CoT | None |
| **Quick Thinker** | Fast responses | None |
| **Compliance Advisor** | Regulatory knowledge | Vector + Graph |
| **AIOps Advisor** | Infrastructure docs | Vector |
| **Dev Advisor** | Development standards | Vector |
| **Internet Search** | Web research | SearXNG |
| **CAD Advisor** | Document comparison | Ephemeral Vector |

---

## Deployment

### Enable Cortex Product

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: ai-hub
  namespace: flux-system
spec:
  interval: 10m
  path: ./ai-hub/deploy
  prune: true
  sourceRef:
    kind: GitRepository
    name: openova-blueprints
  postBuild:
    substitute:
      TENANT: ${TENANT}
      DOMAIN: ${DOMAIN}
      GPU_NODE_POOL: ${GPU_NODE_POOL}
```

---

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `TENANT` | Tenant identifier | Required |
| `DOMAIN` | Base domain | Required |
| `GPU_NODE_POOL` | GPU node label | Required |
| `LLM_MODEL` | Default LLM | `qwen3-32b` |
| `EMBEDDING_MODEL` | Embedding model | `bge-m3` |
| `VECTOR_DIM` | Vector dimensions | `1024` |

---

## Resource Requirements

| Component | Replicas | CPU | Memory | GPU |
|-----------|----------|-----|--------|-----|
| vLLM | 1 | 4 | 32Gi | 2x A10 |
| BGE-M3 | 1 | 2 | 4Gi | 1x A10 |
| BGE-Reranker | 1 | 1 | 2Gi | 1x A10 |
| Milvus | 3 | 2 | 8Gi | - |
| Neo4j | 1 | 2 | 4Gi | - |
| LangServe | 2 | 1 | 2Gi | - |
| LibreChat | 2 | 0.5 | 1Gi | - |
| LLM Gateway | 2 | 0.25 | 512Mi | - |
| **Total** | - | ~15 | ~55Gi | 4x A10 |

---

## GPU Requirements

| GPU Type | Minimum | Recommended |
|----------|---------|-------------|
| NVIDIA A10 | 2 | 4 |
| NVIDIA A100 | 1 | 2 |
| NVIDIA H100 | 1 | 1 |

---

## Use Cases

### Claude Code with Internal Models

```bash
# Configure Claude Code
export ANTHROPIC_BASE_URL="https://llm-gateway.ai-hub.<domain>/v1"
export ANTHROPIC_API_KEY="your-subscription-token"

# Use Claude Code normally
claude "Explain this code..."
```

### RAG-Powered Chat

```bash
# Access LibreChat
https://chat.ai-hub.<domain>

# Select agent preset (e.g., Compliance Advisor)
# Upload documents for context
# Ask questions with citations
```

### Workflow Automation

```bash
# Access Airflow
https://airflow.cortex.<domain>

# Create workflow:
# Webhook → Document Parse → LLM Analysis → Store in Neo4j
```

---

## Monitoring

### Key Metrics

| Metric | Query |
|--------|-------|
| LLM latency | `vllm_request_duration_seconds` |
| Token throughput | `vllm_generation_tokens_total` |
| Retrieval latency | `langserve_retrieval_duration_seconds` |
| GPU utilization | `DCGM_FI_DEV_GPU_UTIL` |

### Grafana Dashboards

| Dashboard | Purpose |
|-----------|---------|
| AI Hub Overview | Request rates, latencies |
| GPU Metrics | Utilization, memory |
| RAG Analytics | Retrieval quality, citations |
| User Analytics | Usage by agent, user |

---

## Operations

### Health Checks

```bash
# Check all components
kubectl get pods -n ai-hub

# Check vLLM
curl http://vllm.ai-hub.svc:8000/health

# Check LangServe
curl http://langserve.ai-hub.svc:8000/health

# Check Milvus
kubectl exec -it milvus-proxy-0 -n ai-hub -- curl localhost:9091/healthz
```

### Document Ingestion

```bash
# Ingest documents via API
curl -X POST https://langserve.ai-hub.<domain>/ingest/file \
  -F "file=@document.pdf" \
  -F "source=compliance"
```

---

## Troubleshooting

| Issue | Cause | Resolution |
|-------|-------|------------|
| OOM on vLLM | Model too large | Increase GPU memory or use quantization |
| Slow retrieval | Index not optimized | Rebuild Milvus index |
| Empty responses | No relevant chunks | Check embedding quality |
| GPU not detected | Driver issue | Verify NVIDIA device plugin |

---

## Consequences

**Positive:**
- Enterprise-ready AI platform
- Claude Code with internal models
- Hybrid RAG (vector + graph)
- Multiple agent presets
- GPU-accelerated inference

**Negative:**
- GPU infrastructure required
- Complex deployment
- Resource-intensive
- Requires ML expertise

---

*Part of [OpenOva](https://openova.io)*
