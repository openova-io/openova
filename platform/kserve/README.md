# KServe

Kubernetes-native model serving. **Application Blueprint** (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.6). Used by `bp-cortex` to serve LLMs via vLLM, embedding models via BGE, and any custom inference workload.

**Status:** Accepted | **Updated:** 2026-04-30

---

## Blueprint chart

This folder ships an umbrella Helm chart at `chart/` that wraps the upstream `kserve/kserve` chart (v0.16.0 — latest version published on the official OCI registry as of 2026-04-30) under `dependencies:`. Catalyst-curated overlay templates render alongside:

- `chart/templates/networkpolicy.yaml` — locks the controller-manager namespace down (DEFAULT FALSE).
- `chart/templates/servicemonitor.yaml` — controller-manager metrics scrape (DEFAULT FALSE per [`docs/BLUEPRINT-AUTHORING.md`](../../docs/BLUEPRINT-AUTHORING.md) §11.2; Capabilities-gated).
- `chart/templates/hpa.yaml` — controller-manager Deployment HPA (DEFAULT FALSE; controller is leader-elected).

**Catalyst defaults**:
- `kserve.controller.deploymentMode: RawDeployment` — KServe writes plain Deployment+Service+HPA per InferenceService (no Knative hop on the hot path).
- `kserve.controller.gateway.ingressGateway.enableGatewayApi: true` + `className: cilium` — Catalyst's istio-less Cilium native Gateway-API path.
- `kserve.controller.gateway.disableIstioVirtualHost: true` — Knative-Istio is NOT installed.
- `bp-knative` is still installed (declared as a hard dependency in `blueprint.yaml`) so per-InferenceService annotation `serving.kserve.io/deploymentMode: Serverless` opts in to scale-to-zero on a per-tenant basis without infra changes.

---

## Overview

KServe provides standardized model serving on Kubernetes with support for multiple ML frameworks, autoscaling, and inference graphs.

```mermaid
flowchart TB
    subgraph KServe["KServe"]
        Controller[KServe Controller]
        Predictor[Predictor]
        Transformer[Transformer]
        Explainer[Explainer]
    end

    subgraph Runtimes["Serving Runtimes"]
        vLLM[vLLM]
        TorchServe[TorchServe]
        Triton[Triton]
        SKLearn[SKLearn]
    end

    subgraph Knative["Knative Serving"]
        Autoscale[Autoscaling]
        Revisions[Revisions]
    end

    Controller --> Predictor
    Controller --> Transformer
    Controller --> Explainer
    Predictor --> Runtimes
    Runtimes --> Knative
```

---

## Why KServe?

| Feature | Benefit |
|---------|---------|
| Multi-framework | TensorFlow, PyTorch, ONNX, vLLM, etc. |
| Autoscaling | Scale-to-zero via Knative |
| InferenceService | Standardized deployment pattern |
| Inference Graph | Multi-model pipelines |
| Model explainability | Integrated explainers |

---

## Components

| Component | Purpose |
|-----------|---------|
| **InferenceService** | Model deployment abstraction |
| **ServingRuntime** | Framework-specific runtime |
| **InferenceGraph** | Multi-model orchestration |
| **ClusterStorageContainer** | Model storage configuration |

---

## Serving Runtimes

| Runtime | Use Case |
|---------|----------|
| **vLLM** | LLM inference (recommended) |
| **TorchServe** | PyTorch models |
| **Triton** | Multi-framework, high performance |
| **SKLearn** | Scikit-learn models |
| **XGBoost** | Gradient boosting models |
| **ONNX** | ONNX format models |

---

## Configuration

### InferenceService Example

```yaml
apiVersion: serving.kserve.io/v1beta1
kind: InferenceService
metadata:
  name: llm-service
  namespace: ai-hub
spec:
  predictor:
    model:
      modelFormat:
        name: vllm
      runtime: vllm-runtime
      storageUri: pvc://model-cache/models/qwen-32b
      resources:
        requests:
          cpu: "4"
          memory: 32Gi
          nvidia.com/gpu: "2"
        limits:
          cpu: "8"
          memory: 64Gi
          nvidia.com/gpu: "2"
```

### ServingRuntime for vLLM

```yaml
apiVersion: serving.kserve.io/v1alpha1
kind: ServingRuntime
metadata:
  name: vllm-runtime
spec:
  supportedModelFormats:
    - name: vllm
      autoSelect: true
  containers:
    - name: kserve-container
      image: vllm/vllm-openai:latest
      args:
        - --model=$(MODEL_ID)
        - --tensor-parallel-size=2
        - --max-model-len=32768
      resources:
        requests:
          nvidia.com/gpu: "2"
```

---

## Inference Graph

Multi-model pipeline for complex inference:

```yaml
apiVersion: serving.kserve.io/v1alpha1
kind: InferenceGraph
metadata:
  name: rag-pipeline
spec:
  nodes:
    root:
      routerType: Sequence
      steps:
        - serviceName: embedder
        - serviceName: retriever
        - serviceName: llm
    embedder:
      serviceName: bge-embedder
    retriever:
      serviceName: vector-search
    llm:
      serviceName: qwen-llm
```

---

## GPU Scheduling

```yaml
# Node selector for GPU nodes
spec:
  predictor:
    nodeSelector:
      nvidia.com/gpu.product: NVIDIA-A10
    tolerations:
      - key: nvidia.com/gpu
        operator: Exists
        effect: NoSchedule
```

---

## Model Storage

### PVC-based Storage

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: model-cache
  namespace: ai-hub
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 100Gi
  storageClassName: oci-bv
```

### S3-based Storage (SeaweedFS)

```yaml
apiVersion: serving.kserve.io/v1alpha1
kind: ClusterStorageContainer
metadata:
  name: seaweedfs-storage
spec:
  container:
    name: storage-initializer
    image: kserve/storage-initializer:latest
    env:
      - name: AWS_ACCESS_KEY_ID
        valueFrom:
          secretKeyRef:
            name: seaweedfs-credentials
            key: accesskey
      - name: AWS_SECRET_ACCESS_KEY
        valueFrom:
          secretKeyRef:
            name: seaweedfs-credentials
            key: secretkey
      - name: S3_ENDPOINT
        value: http://seaweedfs.storage.svc:8333
```

---

## Monitoring

| Metric | Query |
|--------|-------|
| Inference latency | `kserve_inference_duration_seconds` |
| Request count | `kserve_inference_count` |
| GPU utilization | `DCGM_FI_DEV_GPU_UTIL` |
| Model load time | `kserve_model_load_duration_seconds` |

---

## Consequences

**Positive:**
- Standardized model deployment
- Multi-framework support
- Autoscaling via Knative
- Inference graphs for pipelines
- GPU scheduling support

**Negative:**
- Complexity for simple deployments
- Requires Knative
- Learning curve for KServe concepts

---

*Part of [OpenOva](https://openova.io)*
