# vLLM

High-performance LLM inference engine with PagedAttention.

**Status:** Accepted | **Updated:** 2026-02-07

---

## Overview

vLLM provides high-throughput LLM serving with efficient memory management via PagedAttention. Recommended runtime for LLM inference in OpenOva.

```mermaid
flowchart LR
    subgraph vLLM["vLLM Engine"]
        PagedAttn[PagedAttention]
        Scheduler[Continuous Batching]
        KVCache[KV Cache Management]
    end

    subgraph API["OpenAI-Compatible API"]
        Chat[/v1/chat/completions]
        Completions[/v1/completions]
        Models[/v1/models]
    end

    Request[Request] --> API
    API --> vLLM
    vLLM --> GPU[GPU]
```

---

## Why vLLM?

| Feature | Benefit |
|---------|---------|
| PagedAttention | 24x higher throughput than HuggingFace |
| Continuous batching | Efficient request handling |
| OpenAI-compatible API | Drop-in replacement |
| Tensor parallelism | Multi-GPU support |
| Quantization | AWQ, GPTQ, INT8 support |

---

## Supported Models

| Model Family | Examples |
|--------------|----------|
| Qwen | Qwen2.5, Qwen3 (recommended) |
| Llama | Llama 3.1, Llama 3.2 |
| Mistral | Mistral, Mixtral |
| DeepSeek | DeepSeek-R1, DeepSeek-V3 |
| Others | Phi, Gemma, Yi, etc. |

---

## Configuration

### Deployment via KServe

```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: qwen-32b
  namespace: ai-hub
spec:
  predictor:
    model:
      modelFormat:
        name: vllm
      runtime: vllm-runtime
      storageUri: pvc://model-cache/models/qwen3-32b-awq
    resources:
      requests:
        nvidia.com/gpu: "2"
      limits:
        nvidia.com/gpu: "2"
```

### Standalone Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm
  namespace: ai-hub
spec:
  replicas: 1
  selector:
    matchLabels:
      app: vllm
  template:
    spec:
      containers:
        - name: vllm
          image: vllm/vllm-openai:latest
          args:
            - --model=/models/qwen3-32b-awq
            - --tensor-parallel-size=2
            - --max-model-len=32768
            - --gpu-memory-utilization=0.9
            - --enable-prefix-caching
          ports:
            - containerPort: 8000
          resources:
            requests:
              nvidia.com/gpu: "2"
            limits:
              nvidia.com/gpu: "2"
          volumeMounts:
            - name: model-cache
              mountPath: /models
      volumes:
        - name: model-cache
          persistentVolumeClaim:
            claimName: model-cache
```

---

## Key Parameters

| Parameter | Purpose | Example |
|-----------|---------|---------|
| `--model` | Model path or HuggingFace ID | `/models/qwen3-32b` |
| `--tensor-parallel-size` | Number of GPUs | `2` |
| `--max-model-len` | Maximum context length | `32768` |
| `--gpu-memory-utilization` | GPU memory fraction | `0.9` |
| `--quantization` | Quantization method | `awq`, `gptq` |
| `--enable-prefix-caching` | Cache common prefixes | - |

---

## API Usage

### Chat Completions

```bash
curl http://vllm.ai-hub.svc:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "qwen3-32b",
    "messages": [
      {"role": "user", "content": "Hello!"}
    ],
    "stream": true
  }'
```

### With Thinking Mode (Qwen3)

```bash
curl http://vllm.ai-hub.svc:8000/v1/chat/completions \
  -d '{
    "model": "qwen3-32b",
    "messages": [
      {"role": "user", "content": "Solve this step by step: ..."}
    ],
    "extra_body": {
      "chat_template_kwargs": {"enable_thinking": true}
    }
  }'
```

---

## Multi-GPU Configuration

### Tensor Parallelism (Single Node)

```yaml
args:
  - --tensor-parallel-size=4  # Split model across 4 GPUs
```

### Pipeline Parallelism (Multi-Node)

```yaml
args:
  - --pipeline-parallel-size=2  # Split across 2 nodes
  - --tensor-parallel-size=4    # 4 GPUs per node
```

---

## Quantization

| Method | Memory Reduction | Quality |
|--------|------------------|---------|
| AWQ | ~4x | Excellent |
| GPTQ | ~4x | Good |
| INT8 | ~2x | Very Good |
| FP8 | ~2x | Excellent |

```yaml
args:
  - --quantization=awq
  - --dtype=half
```

---

## Monitoring

| Metric | Query |
|--------|-------|
| Request latency | `vllm:request_latency_seconds` |
| Tokens/second | `vllm:generation_tokens_total` |
| GPU memory | `vllm:gpu_cache_usage_perc` |
| Queue length | `vllm:num_requests_waiting` |

---

## Consequences

**Positive:**
- Industry-leading performance
- OpenAI-compatible API
- Excellent quantization support
- Multi-GPU scaling
- Active development

**Negative:**
- GPU required
- Memory-intensive for large models
- Some models not yet supported

---

*Part of [OpenOva](https://openova.io)*
