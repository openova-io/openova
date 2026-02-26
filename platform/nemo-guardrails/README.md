# NeMo Guardrails

AI safety firewall for LLM deployments.

**Category:** AI Safety | **Type:** A La Carte

---

## Overview

NeMo Guardrails provides programmable safety rails for LLM interactions, including prompt injection detection, PII filtering, hallucination detection, and topic control. Non-negotiable for regulated environments deploying AI.

## Key Features

- Prompt injection detection and blocking
- PII filtering (input and output)
- Hallucination detection via fact-checking rails
- Topic boundary enforcement
- Custom rail definitions (Colang)

## Integration

| Component | Integration |
|-----------|-------------|
| KServe | Deployed as pre/post-processing step |
| LLM Gateway | Inline filtering for all LLM requests |
| LangFuse | Traces guardrail activations |
| Grafana | Guardrail metrics and alerting |

## Used By

- **OpenOva Cortex** - AI safety for enterprise LLM deployments

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: nemo-guardrails
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/nemo-guardrails
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
