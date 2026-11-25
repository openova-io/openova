# bp-speech

Transcription on CPU: one speaches server (github.com/speaches-ai/speaches, MIT, `0.8.3-cpu` by digest) serves one Whisper model in CTranslate2 form with int8 weights.

## Role in Catalyst

An AI-service Blueprint (`class: ai-service`) of the platform's model plane, not an Application a User installs. The platform's model gateway is its one caller: the Service is `ClusterIP`, and the chart renders no `HTTPRoute`, `Ingress`, `Gateway` or `GRPCRoute`. The kit's target namespace is `model-serving`, which `bp-bge`'s release creates and owns; this chart renders no Namespace, because Helm refuses an object another release owns. See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) for where model services sit.

Route: `POST /v1/audio/transcriptions` (multipart: `file`, `model`, `language`, `response_format`), and `GET /health` for the probes. The caller sends audio in segments of at most 30 seconds: on one long file the measured defaults drop words or repeat themselves (`docs/ledger/plan/spikes/PRD-080.md`). The chart has no language routing; a language-specific model is a second release behind a second alias.

## Configuration

The Blueprint's `configSchema` (`blueprint.yaml`) is the chart's parameters; the chart refuses at render what it refuses.

| Value | Meaning |
|---|---|
| `home`, `enabled` | Set by the kit slot (`enabled` from `MODEL_PLANE_TRANSCRIPTION`). Both true: the chart renders its workloads. Otherwise it renders nothing. |
| `model` | `source`, `revision`, `servedName`. Default: `hf://deepdml/faster-whisper-large-v3-turbo-ct2` at `4df90f75…`, served as `whisper-large-v3-turbo`. |
| `cache` | Optional weights claim: `sizeGi` (at least 10), `storageClass`. Without it, an emptyDir of 10Gi. |
| `startup` | `loadSeconds` (at least 30, default 30): the start-up probe allows 1.5 times it, rounded up to whole periods of 10 seconds. |

A `source` is never a floating reference: `oci://<host>/<path>@sha256:<64 hex>`, or `hf://<owner>/<name>` with `revision`, the 40-character commit. `servedName` is the name a request gives as its `model`, the `upstreamModel` of the gateway's alias; the release's alias file (`model_aliases.json`, mounted over the image's own) maps it to the model.

Outside the schema: `image`, `port` (8000), `resources` (2 to 4 CPUs, 3 to 5Gi of memory; the server's threads follow `limits.cpu`), `hubToken.secretProviderClass`, `networkPolicy`, `functionTest`.

**Weights.** The server reads a Hugging Face cache on the weights volume, read-only, with the hub switched off (`HF_HUB_OFFLINE`); `refs/main` names the one pinned snapshot, so the model never floats. Init containers write it.

**hf:// models.** An init container of the server image fetches the model's files at the pinned revision with the image's hub client. That needs `networkPolicy.hubEgress: true` (default false; with the policy on and it false, an `hf://` source is refused at render). A gated model's token is one file: `hubToken.secretProviderClass` names a SecretProviderClass of the release namespace whose object `hf-token` the Secrets Store CSI driver mounts as `/var/run/secrets/openova/hf-token` (`HF_TOKEN_PATH`). No credential is a value, and the chart reads no Secret.

**Model image.** An `oci://` model is an image of the Sovereign's registry with one layer over `scratch`: the files of one revision of a CTranslate2 Whisper repository (`model.bin`, `config.json`, `tokenizer.json`, `preprocessor_config.json`, `vocabulary.*` and `README.md`, whose model card names the library `ctranslate2` and the task `automatic-speech-recognition`), readable by every user, in `/model`, and a statically linked `/bin/cp` (for example busybox's). An init container of the server image lays out the repository `oci/model`; then the model image's `/bin/cp -R /model/.` copies the files in as its snapshot, named by the first 40 characters of the image digest. The kubelet pulls the image, so the Pod needs no egress for it.

## Operations

- One replica. The Pod runs as user 1000 (the image's user `ubuntu`, by number) with a read-only root file system, all capabilities dropped and no service-account token; the hub client's files are on a `/tmp` emptyDir.
- The server answered in about 3 seconds when measured. The model loads on the first request (6.5 to 13.9 seconds measured for the default model) and then stays loaded (`WHISPER__TTL=-1`). The measured speed of the default model, segment by segment, was 1.28 to 1.76 seconds of audio per second on CPUs with AVX-512 and 0.82 on one without; measure on the target CPU before a speed is promised.
- The network policy (on by default) admits the serving port only from namespace `model-gateway` and from the release's function test, and lets the Pods send to cluster DNS, to the server's port and, with `hubEgress`, to TCP 443 of public addresses (10.0.0.0/8, 100.64.0.0/10, 169.254.0.0/16, 172.16.0.0/12 and 192.168.0.0/16 excepted).

## Function test

`helm test` sends the bundled sample `chart/files/function-test-sample.ogg` to `POST /v1/audio/transcriptions` and passes when the text is not empty and holds the word "americans". The sample is the first five seconds of `samples/jfk.wav` of github.com/ggml-org/whisper.cpp at commit `d1be6fde11ac6e0407606b4e42fe72d34add8037` (a 1961 address of the President of the United States, a work of the United States government in the public domain), resampled to 16 kHz mono and encoded as Opus at 32 kbit/s in Ogg (19,582 bytes), made with the server image's ffmpeg:

```sh
ffmpeg -i jfk.wav -t 5 -ac 1 -ar 16000 -c:a libopus -b:a 32k -map_metadata -1 -fflags +bitexact function-test-sample.ogg
```

Tests: `chart/tests/render.sh` (the rules at render), `chart/tests/function-test-render.sh` (the test hook), `chart/tests/model-load.sh` (a real run of the image: fetch from the hub, copy from a model image pulled by digest from a registry, and from each a transcription of the sample and a pass of the rendered function test's script).

Single region (`topology: singleton`): the model plane runs in the region whose `MODEL_PLANE_HOME` is true.
