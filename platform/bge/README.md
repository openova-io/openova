# bp-bge

Embeddings and reranking on CPU: one Infinity server (github.com/michaelfeil/infinity, MIT, `0.0.78-cpu` by digest) serves one embedding model and at most one reranker.

## Role in Catalyst

An AI-service Blueprint (`class: ai-service`) of the platform's model plane, not an Application a User installs. The platform's model gateway is its one caller: the Service is `ClusterIP`, and the chart renders no `HTTPRoute`, `Ingress`, `Gateway` or `GRPCRoute`. The kit's target namespace is `model-serving`. See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) for where model services sit.

Routes: `POST /v1/embeddings` (`{model, input}`), `POST /v1/rerank` (`{model, query, documents, top_n}` to `{results: [{index, relevance_score}]}`), and `GET /health` for the probes. `usage.prompt_tokens` counts tokens (`--lengths-via-tokenize`), which the gateway's meter reads.

## Configuration

The Blueprint's `configSchema` (`blueprint.yaml`) is the chart's parameters; the chart refuses at render what it refuses.

| Value | Meaning |
|---|---|
| `home`, `enabled` | Set by the kit slot. Both true: the chart renders its workloads. Otherwise it renders the Namespace only. |
| `embedding` | `source`, `revision`, `servedName`, `dimensions`. Default: `hf://ibm-granite/granite-embedding-107m-multilingual` at `d6cffd33…`, 384 dimensions. |
| `reranker` | Optional. `source`, `revision`, `servedName`. Default: `hf://cross-encoder/mmarco-mMiniLMv2-L12-H384-v1` at `1427fd65…`. |
| `cache` | Optional weights claim: `sizeGi` (at least 10), `storageClass`. Without it, an emptyDir of 10Gi. |

A `source` is never a floating reference: `oci://<host>/<path>@sha256:<64 hex>`, or `hf://<owner>/<name>` with `revision`, the 40-character commit. Both models come from the same kind of source.

Outside the schema: `image`, `port` (7997), `resources` (2 to 4 CPUs, 5 to 8Gi of memory), `hubToken.secretProviderClass`, `networkPolicy`, `functionTest`.

**Model image.** An `oci://` model is an image of the Sovereign's registry with one layer over `scratch`: the model's files, readable by every user, in `/model`, and a statically linked `/bin/cp` (for example busybox's). An init container runs `/bin/cp -R /model/. /models/<embedding|reranker>`; the kubelet pulls the image, so the Pod needs no egress for it, and the server reads the weights read-only with the hub switched off.

**hf:// models.** The server fetches the pinned revision itself into the weights volume. That needs `networkPolicy.hubEgress: true` (default false; with the policy on and it false, an `hf://` source is refused at render). A gated model's token is one file: `hubToken.secretProviderClass` names a SecretProviderClass of the release namespace whose object `hf-token` the Secrets Store CSI driver mounts as `/var/run/secrets/openova/hf-token` (`HF_TOKEN_PATH`). No credential is a value, and the chart reads no Secret.

## Operations

- One replica. The Pod runs as user 10001 with a read-only root file system, all capabilities dropped and no service-account token; its caches are on a `/tmp` emptyDir.
- The start-up probe allows 300 seconds before its first failure counts. Measured cold start with both default models: about 50 seconds.
- The network policy (on by default) admits the serving port only from namespace `model-gateway` and from the release's function test, and lets the Pods send to cluster DNS, to the server's port and, with `hubEgress`, to TCP 443 of public addresses (10.0.0.0/8, 100.64.0.0/10, 169.254.0.0/16, 172.16.0.0/12 and 192.168.0.0/16 excepted).
- Function test: `helm test` sends one text to `POST /v1/embeddings` and passes when the vector has `embedding.dimensions` numbers.
- Tests: `chart/tests/render.sh` (the rules at render), `chart/tests/function-test-render.sh` (the test hook), `chart/tests/model-load.sh` (a real run of the image: fetch from the hub, copy from model images pulled by digest from a registry, and an embedding of the declared length from each).
- Single region (`topology: singleton`): the model plane runs in the region whose `MODEL_PLANE_HOME` is true.
