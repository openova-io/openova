# NeMo Guardrails

AI safety firewall for LLM deployments. **Application Blueprint** (see [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §4.7 — AI safety). Sits between user input and LLM in `bp-cortex` to block prompt injection, PII leakage, off-topic content, and hallucinated citations.

**Status:** Accepted | **Updated:** 2026-08-26 | **Category:** AI Safety | **Type:** Application Blueprint

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

## Configuration

NeMo Guardrails is configured through a mounted config directory (`config.yml` +
Colang `*.co` flow files):

| Knob | Purpose |
|------|---------|
| `models:` (in `config.yml`) | LLM(s) the rails call for self-check / fact-check |
| `rails.input.flows` | Input rails — prompt-injection + jailbreak checks |
| `rails.output.flows` | Output rails — PII masking + hallucination / fact-check |
| `rails.dialog` | Topic-boundary (on-topic) enforcement |
| `sensitive_data_detection` | PII detection toggles (input and output) |
| Colang `*.co` files | Custom rail flows and canonical forms |

Each rail flow is independently toggled in `config.yml`; disabling a flow removes
that check without rebuilding the image.

## Operational Notes

- **Scaling**: the guardrails server is a stateless HTTP service — scale
  horizontally with replicas / HPA behind the LLM gateway; it holds no durable
  state.
- **Multi-region**: runs active-active per region, co-located in the `bp-cortex`
  Environment alongside the LLM gateway it fronts; each region's instance is
  independent, so a region kill removes only that region's rail hop — there is no
  cross-region state to replicate.
- **Backups**: none required — rail configuration lives in Git (GitOps) and the
  running Pods carry no persistent data.

---

*Part of [OpenOva](https://openova.io)*
