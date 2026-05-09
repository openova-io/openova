# Continuum Lease-Witness Worker — Design

**Slice K-Cont-4 of EPIC-6 (#1101).**
**Predecessor**: K-Cont-3 (`core/controllers/continuum/internal/witness/cloudflarekv/` — the Go client).

This Worker is the **server side** of the Cloudflare KV witness pattern (per `docs/SRE.md` §2.4). The Continuum controller's `CFKVClient` (K-Cont-3) calls it for `Acquire` / `Renew` / `Release` / `Read` operations against a per-Continuum-CR slot stored in Cloudflare Workers KV.

## Why a Worker (vs raw KV REST)?

Cloudflare KV's REST API has no native CAS primitive — concurrent writers can clobber each other. A Worker fronts KV with read-then-CAS-write semantics enforced via the `If-Match` header pattern. This single endpoint also:

- Centralises the bearer-token allow-list (one secret rotated in one place, not per-CR)
- Stamps `acquiredAt`/`expiresAt` server-side (clock-skew immune across the controller fleet)
- Bumps `generation` monotonically across the lifecycle (including Release) so a re-Acquire after Release sees `If-Match: <gen+1>` instead of rolling back to 0
- Logs per-slot access for audit (visible in Workers Logs / CF dashboard)

## HTTP contract (non-negotiable per K-Cont-3 #1158)

| Method | Path | Required headers | Body | Success | Conflict | Auth fail |
|---|---|---|---|---|---|---|
| GET | `/lease/<slot-url-encoded>` | `Authorization: Bearer <token>` | — | 200 + LeaseState | 404 | 401 |
| PUT | `/lease/<slot>` | `Authorization`, `If-Match: <gen-or-0>`, `Content-Type: application/json` | `{holder, ttlSeconds, op∈{acquire,renew}}` | 200 + LeaseState | 412 + LeaseState | 401 |
| DELETE | `/lease/<slot>` | `Authorization`, `If-Match: <gen>`, `X-Holder: <region>` | — | 204 | 412 | 401 |

`LeaseState` JSON shape: `{holder: string, acquiredAt: RFC3339, expiresAt: RFC3339, generation: int64}`.

### 7 critical behaviors (per K-Cont-3 traps item h — all covered)

1. **`If-Match: 0` = first-acquire-on-empty-slot signal.** Worker accepts on empty slot; rejects 412 if held. (`put.ts` CAS branch.)
2. **Generation increments unconditionally on PUT/DELETE including Release.** (`put.ts` `next.generation = curGen + 1`; `kv.ts::clearLeaseBumpGen`.)
3. **412 SHOULD include current state body** for diagnostics. (`index.ts::preconditionFailedResponse(cur)`.)
4. **TTL eviction is server-authoritative in stamping, NOT in storage.** Worker computes `expiresAt = now + ttlSeconds`; reads return stored state regardless of expiry. The controller's `IsHeldBy(now)` decides eviction. (`put.ts`; verified by the "Read returns stored state regardless of expiry" test.)
5. **`X-Holder` on DELETE = holder-identity check.** Stale region can't evict the new primary even with stale-but-matching `If-Match`. (`delete.ts` second 412 branch.)
6. **Bearer token validation against env-bound allow-list** — no per-account path scoping. (`auth.ts`.)
7. **Optional `X-Lease-Slot` header** for KV log granularity. (Logged in `index.ts::log` at debug level; never affects routing.)

## Module layout

```
products/continuum/cloudflare-worker/
├── package.json              # @cloudflare/workers-types, vitest, wrangler
├── tsconfig.json             # ES2022 + strict + workers-types
├── wrangler.toml             # name, KV binding (id REPLACE_ME via tofu),
│                             # vars (BEARER_TOKENS_CSV, LOG_LEVEL),
│                             # observability, cpu_ms=100
├── vitest.config.ts          # @cloudflare/vitest-pool-workers config
├── .eslintrc.cjs             # @typescript-eslint/recommended
├── src/
│   ├── index.ts              # entrypoint: routing + auth + dispatch + helpers
│   ├── auth.ts               # bearer-token validation (no logging of token)
│   ├── kv.ts                 # KV access wrapper; key shape `lease:<slot>`
│   ├── types.ts              # LeaseState + LeaseWriteRequest + Env + ErrorBody
│   └── handlers/
│       ├── get.ts            # GET /lease/<slot>
│       ├── put.ts            # PUT /lease/<slot> (acquire + renew)
│       └── delete.ts         # DELETE /lease/<slot>
├── test/
│   ├── handlers.test.ts      # branch-coverage tests (auth, routing, all
│   │                         # methods, every status code, edge cases)
│   └── contract.test.ts      # exact-shape verification against K-Cont-3
│                             # CFKVClient request bodies + header sets
└── DESIGN.md                 # this file
```

## KV key/value shape

- **Key**: `lease:<slot>` where `<slot>` is the URL-decoded path tail (e.g. `lease:ns/cr-main`).
- **Value**: JSON `LeaseState` blob (see `types.ts`).
- **TTL on KV record**: NONE. Records persist until DELETE replaces them with a `{holder: "", generation: gen+1}` shell. Per trap #4 the Worker does NOT delete records on TTL expiry — that's the controller's responsibility.

## Auth model

- Bearer tokens live in the `BEARER_TOKENS_CSV` env var, comma-separated. Per Inviolable Principle #5 the actual values are passed at deploy time via `wrangler secret put` (or the CF dashboard), NEVER inlined in `wrangler.toml`.
- The OpenTofu module at `infra/cloudflare-worker-leases/` consumes a SealedSecret (per the Continuum CR's `spec.leaseClient.config.tokenSecretRef`) and writes the comma-separated list to the Worker's `vars` at apply time.
- `Authorization: Bearer <token>` is checked on EVERY request (no per-method auth differences). Missing header / wrong scheme / unknown token → 401.
- The Worker NEVER logs the token value, only the boolean outcome.

## Operator runbook — deploy a new Sovereign

To deploy the lease witness Worker for a new Sovereign:

1. **Provision the SealedSecret** holding the bearer token allow-list. Use `kubeseal` to encrypt a Secret of shape `{token: "<32-char-random>"}`. The Continuum CR's `spec.leaseClient.config.tokenSecretRef` references this Secret. Generate the token with `openssl rand -hex 32` (Inviolable Principle #5: 24+ chars, fully random, no dictionary words). Commit the SealedSecret to the Sovereign's overlay directory under `clusters/<sovereign-fqdn>/`.
2. **Run the tofu module** at `infra/cloudflare-worker-leases/`. Inputs: `cf_account_id`, `cf_zone_id` (optional), `kv_namespace_name` (defaults to `OPENOVA_LEASES`), and `bearer_tokens_csv` (read from the SealedSecret via `data.kubernetes_secret`). The module creates the KV namespace, deploys the Worker, and binds the namespace + env vars. Outputs: `worker_url` (e.g. `https://openova-continuum-lease-witness.<account>.workers.dev`).
3. **Wire the Continuum CR**: set `spec.leaseClient.kind: cloudflare-kv` and `spec.leaseClient.config.baseURL: <tofu-output-worker-url>`. The catalyst-controllers reconciler picks up the change on the next reconcile (~30s) and the CFKVClient starts hitting the new Worker. Verify via `kubectl logs -n catalyst-controllers continuum-controller-* | grep cloudflarekv` — the first Acquire should succeed within one renew cycle.

For per-Sovereign isolation, deploy ONE Worker per Sovereign (each with its own KV namespace + token); the slot-per-CR partitioning inside one Worker is sufficient for multiple Continuum CRs within a single Sovereign but does NOT isolate Sovereigns from each other if they share a Worker.

## Local dev

```sh
cd products/continuum/cloudflare-worker
npm install
npm test                      # vitest contract + handler suites
npm run lint                  # ESLint
npm run typecheck             # tsc --noEmit
npm run build:dryrun          # wrangler deploy --dry-run (verifies build)
```

Real local `wrangler dev` requires a CF account; the contract tests run against an in-process workerd via `@cloudflare/vitest-pool-workers` so no account needed for CI.

## Out of scope

- DO NOT couple this Worker to a Sovereign's other K8s state. It MUST work against any controller that speaks the contract.
- DO NOT add a `/healthz` endpoint. Cloudflare's platform pings the Worker; an unauthed `/healthz` would be probe surface for nothing.
- DO NOT add per-account path scoping. Auth is binary (allow-list); per-CR isolation is by `slot`.

— Slice K-Cont-4
