# LangFuse

LLM observability and analytics. **Application Blueprint** (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.7). Traces every LLM call in `bp-cortex` — latency, tokens, cost, eval scores. Catalyst's general-purpose observability stack (Grafana/OTel) covers infrastructure; LangFuse covers the AI-specific dimensions (prompt/response, model drift, eval).

**Category:** AI Observability | **Type:** Application Blueprint

---

## Overview

LangFuse provides tracing, evaluation, and analytics for LLM applications. It captures every LLM call with cost, latency, token usage, and evaluation scores. Complements Grafana (which handles infrastructure metrics) with AI-specific observability.

## Key Features

- LLM call tracing (input, output, cost, latency, tokens)
- Prompt management and versioning
- Evaluation scoring and datasets
- User analytics and session tracking
- Cost attribution per model/user/feature

## Integration

| Component | Integration |
|-----------|-------------|
| LLM Gateway | Automatic trace capture |
| Grafana | Infrastructure metrics complement |
| CNPG | PostgreSQL backend for traces |
| NeMo Guardrails | Traces guardrail activations |

## Used By

- **OpenOva Cortex** - LLM observability for enterprise AI

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: langfuse
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/langfuse
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
