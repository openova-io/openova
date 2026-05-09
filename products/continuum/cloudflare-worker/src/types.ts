// types.ts — wire shapes for the OpenOva Continuum lease-witness
// Worker (K-Cont-4 of EPIC-6 #1101).
//
// The State + WriteRequest shapes mirror the Go `kvState` +
// `kvWriteRequest` types in
// core/controllers/continuum/internal/witness/cloudflarekv/client.go
// — they ARE the contract. Any field-name change on either side
// breaks the witness and must land in BOTH repos in the same PR.

/**
 * State stored under `lease:<slot-url-encoded>` in KV. Returned from
 * GET 200 / PUT 200 / on PUT-412 conflict (per K-Cont-3 trap #3, "412
 * SHOULD include current state body").
 *
 * - `holder` — the per-region identity holding the lease (e.g.
 *   "fsn1-primary"). Empty after Release.
 * - `acquiredAt` — RFC3339 timestamp the holder first acquired this
 *   slot. Preserved on renew + re-acquire by the same holder; reset
 *   to `now()` when a new holder takes over.
 * - `expiresAt` — RFC3339 timestamp the lease expires
 *   (`acquiredAt + ttlSeconds` for first acquire, `now() + ttlSeconds`
 *   for renew). Per K-Cont-3 trap #4 the Worker is server-authoritative
 *   for this stamp; the controller's `IsHeldBy(now)` decides eviction
 *   client-side.
 * - `generation` — monotonically increasing int64. Bumped on EVERY
 *   successful PUT and DELETE (per K-Cont-3 trap #2 — including
 *   Release, so the next Acquire's `If-Match` has a defined non-zero
 *   baseline rather than rolling back to 0).
 */
export interface LeaseState {
  holder: string;
  acquiredAt: string; // RFC3339
  expiresAt: string;  // RFC3339
  generation: number;
}

/**
 * PUT body. Both acquire and renew share this shape; `op` is the
 * discriminator.
 *
 * - `holder` — the requesting region's identity. Compared against the
 *   stored `holder` for renew (must match) and acquire (any value
 *   allowed when slot is takeable).
 * - `ttlSeconds` — lease TTL in whole seconds. Worker sets
 *   `expiresAt = now + ttlSeconds`.
 * - `op` — `"acquire"` or `"renew"`. `renew` REQUIRES that
 *   `stored.holder === requested.holder` AND `stored.expiresAt > now`
 *   (per the K-Cont-2 contract Renew is for the holder only). Worker
 *   rejects renew with 412 otherwise.
 */
export interface LeaseWriteRequest {
  holder: string;
  ttlSeconds: number;
  op: "acquire" | "renew";
}

/**
 * Env shape — Cloudflare Worker bindings. Matches `wrangler.toml`'s
 * `[vars]` and `[[kv_namespaces]]` declarations. Per INVIOLABLE-PRINCIPLES
 * #4 every value here is bound at deploy time, never hardcoded in
 * source.
 *
 * - `OPENOVA_LEASES` — KV namespace handle. Key shape:
 *   `lease:<slot-url-encoded>` (see kv.ts).
 * - `BEARER_TOKENS_CSV` — comma-separated allow-list of valid bearer
 *   tokens. NEVER logged. The token itself is supplied via
 *   `wrangler secret put` and exposed to the Worker as this var.
 * - `LOG_LEVEL` — "error" | "info" | "debug". Defaults to "info".
 */
export interface Env {
  OPENOVA_LEASES: KVNamespace;
  BEARER_TOKENS_CSV: string;
  LOG_LEVEL?: "error" | "info" | "debug";
}

/**
 * The Worker's "everything-is-an-error-shape" envelope when the
 * response body is NOT a LeaseState (i.e. 401/404/400/405/500). Lets
 * the CFKVClient log a meaningful error message via `readSnippet` (see
 * client.go).
 */
export interface ErrorBody {
  error: string;
  reason?: string;
}
