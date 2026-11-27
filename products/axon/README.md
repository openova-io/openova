# Axon

The simulation back end of the model plane: until a Sovereign runs its own
models, Axon answers the model contract by running the agent kit of one
subscription login. Blueprint `bp-axon` (chart `products/axon/chart`).

## Role in Catalyst

Control-plane plumbing, never a user surface and never installed from the
catalogue (visibility `unlisted`). Kit slot 82
(`clusters/_template/bootstrap-kit/82-bp-axon.yaml`) installs it in every
region of a Sovereign: one replica in the primary region, zero in every other,
where the Service stays and reaches the primary region's Axon through the mesh.
There is one Axon per login, because Axon is the only party that refreshes
that login's credential.

Its one caller is the meter of the model gateway; the gateway is the model
plane's front door (decision record
[`docs/adr/0030-model-plane.md`](../../docs/adr/0030-model-plane.md), M5).

## Contract

Axon serves the HTTP interface of
[`docs/contracts/model-contract.md`](../../docs/contracts/model-contract.md)
section 1 on port 3000, for the upstream model names of `axon.modelMap`
(`sim-default`, `sim-fast`). A name outside the map gets 404
`model_not_found`. A caller's tools come back as tool calls and never run in
Axon; tool results are accepted in later turns.

Port 3001 serves the admin routes:

| Route | Answer |
|---|---|
| `GET /healthz` | 200 while the event loop runs (liveness) |
| `GET /health` | 200 once both listeners are up, whatever the credential state (readiness) |
| `GET /stats` | the credential state (`absent`, `valid`, `expired`, `rejected` or `conflict`), the runs active, parked and queued, and the counters |

Axon has no caller keys. The chart's `CiliumNetworkPolicy` is the fence:
port 3000 admits only pods `app.kubernetes.io/name=model-meter` of namespace
`model-gateway`, port 3001 only `catalyst-api` of namespace `catalyst-system`.
Egress is cluster DNS, OpenBao on 8200, and the two hosts of `egress.hosts`
on 443. The Service is ClusterIP; there is no route and no node port.

## The credential

The credential document lives in OpenBao only, at
`mounted/data/axon/sa/axon/model-credential/e1`, key `document`. The Secrets
Store CSI driver mounts it as `/var/run/secrets/openova/model-credential`
(`SecretProviderClass` `axon`, role `workload`); the chart renders no Secret
object. Axon reads it once at start, refreshes it before it expires, and
writes each refreshed document back with check-and-set, logging in with its
projected token of audience `openbao` as role `axon-credential`. A child
process receives the access token alone.

On a fresh install the path holds an empty placeholder that kit slot 82 asks
the birth Job of `platform/secrets-lib` for, so the mount succeeds, the Pod
becomes Ready, and `/stats` reports `absent` until the real document is
stored. Every step then answers 503 `simulation_credential_invalid`.

## Configuration

| Value | Default | Meaning |
|---|---|---|
| `secondary` | `false` | `true` in every region but the primary: zero replicas. Kit slot 82 sets it from `SECONDARY_HR_SUSPEND`. A boolean |
| `image.repository`, `image.digest` | the published image | the image by digest; `axon-build.yaml` writes the digest of each image it pushes from `main` |
| `imagePullSecrets` | `ghcr-pull` | the login the Pod pulls its image with; after the cutover the Sovereign's private project `openova-io` needs it |
| `axon.maxActive`, `axon.maxParked` | `4`, `8` | runs generating at once, and runs waiting for tool results |
| `axon.modelMap` | `sim-default`, `sim-fast` | upstream model name to the agent kit's model |
| `axon.rssActiveMi`, `axon.rssParkedMi` | `223`, `243.1` | resident memory of one child process (spike AIP-020); the memory limit is 512Mi plus each times its count, rounded up (3349Mi at the defaults) |
| `openbao.address` | `http://openbao.openbao.svc.cluster.local:8200` | OpenBao, for the mount and the write-back |
| `egress.hosts` | the provider's API host and token host | the only hosts outside the cluster that Axon reaches |
| `generatedSecrets` | `{}` | values for the birth Job of `secrets-lib`; kit slot 82 lists the placeholder |

## Operations

- **Storing the credential**: write the document to key `document` of its
  OpenBao path as an identity that may update it there (bp-openbao grants that
  to its producer `catalyst-api` and to Axon's own role). Axon reads the file
  at start, so restart the Deployment `axon/axon` after the first store. The
  strategy is `Recreate`: two Axons would be two refreshers of one login.
- **State `conflict`**: another writer changed the document; Axon stopped
  refreshing. Store a fresh document and restart Axon.
- **Multi-region**: each region's birth Job creates its own placeholder; only
  the primary region runs Axon. A region with zero replicas keeps the Service.
- **Backups**: none. Axon keeps nothing but parked runs in memory; the
  credential is in OpenBao.

## Limits of the simulation

These are limits of the back end, not defects:

1. **Sampling.** `temperature`, `top_p`, `seed`, penalties, `logit_bias` and
   `logprobs` are accepted and ignored; `n` above 1 is refused.
2. **Output ceiling.** `max_tokens` is not enforced by the model; a caller
   cannot bound the cost of one answer.
3. **Forced tool choice.** `tool_choice: required` or a named function becomes
   an instruction; `strict` argument schemas are not guaranteed.
4. **Token counts.** They are the upstream model's real counts, including the
   agent kit's own overhead; prices and budgets calibrated here do not carry
   over to the Sovereign's own models. An abandoned stream has no usage block.
5. **Model behaviour.** The alias hides another model family than the one that
   will be self-hosted; agent quality measured here is an upper bound.
6. **Throughput.** Each run in flight or parked holds one child process, and
   the login has rate limits that are not published: concurrency is a small
   fixed number.
7. **History after a restart.** When a parked run is lost, earlier tool calls
   reach the model as text.
8. **Context window, reasoning text, stop sequences.** The window is the
   upstream model's; reasoning text is not returned; stop sequences are
   applied by cutting the output.

## Development

```bash
cd products/axon
npm ci && npm run build && npm test
bash chart/tests/render.sh chart      # needs helm and yq
```
