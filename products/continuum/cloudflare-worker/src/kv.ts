// kv.ts — KV access wrapper for the lease-witness Worker.
//
// Centralises the key shape (`lease:<slot>`) and JSON marshaling so the
// handlers stay focused on HTTP semantics. Per K-Cont-3 the value at
// each key is a `LeaseState` JSON blob; per K-Cont-3 trap #4 the
// Worker is server-authoritative for `acquiredAt`/`expiresAt` stamping
// and `generation` monotonicity.

import type { LeaseState } from "./types";

/**
 * Build the KV key for a slot. The slot has already been URL-decoded
 * by the Worker's URL parser (`new URL(req.url)` decodes `%2F` back to
 * `/`), so the canonical KV key is `lease:<slot>` — slashes preserved.
 *
 * KV doesn't care about characters in keys (per
 * https://developers.cloudflare.com/kv/learning/key-naming/) up to the
 * 512-byte limit. Slot length is bounded by Continuum CR
 * `<namespace>/<name>` — both ≤ 253 chars per K8s naming rules.
 */
export function leaseKey(slot: string): string {
  return `lease:${slot}`;
}

/**
 * Read the current LeaseState for a slot. Returns `null` when the slot
 * is empty (KV `get` returns `null` for missing keys — we surface that
 * directly so callers can distinguish "no record" from "stored empty
 * state with generation > 0" — the latter happens after Release.
 */
export async function readLease(
  ns: KVNamespace,
  slot: string,
): Promise<LeaseState | null> {
  // `type: "json"` instructs the runtime to JSON.parse the value
  // before returning. Saves one `await` + a manual parse.
  const v = await ns.get<LeaseState>(leaseKey(slot), { type: "json" });
  return v ?? null;
}

/**
 * Write a LeaseState. We do NOT pass `expirationTtl` to KV: per
 * K-Cont-3 trap #4 TTL eviction is server-authoritative IN THE
 * RESPONSE BODY (controller-side `IsHeldBy(now)` decides), not via KV
 * record deletion. Letting KV expire records would cause a subsequent
 * Acquire to see Generation=0 and re-take a slot the controller hasn't
 * yet released — the K-Cont-3 contract says generation must be
 * monotonic across the lifecycle, so the Worker keeps the record
 * forever (or until DELETE).
 */
export async function writeLease(
  ns: KVNamespace,
  slot: string,
  state: LeaseState,
): Promise<void> {
  await ns.put(leaseKey(slot), JSON.stringify(state));
}

/**
 * Delete + bump-generation. Per K-Cont-3 trap #2, "Generation
 * increments unconditionally on PUT/DELETE including Release". On
 * Release we don't actually erase the record — we replace it with an
 * empty-holder shell carrying `generation+1`. This way a subsequent
 * Acquire sees `generation=N+1` and presents `If-Match: N+1`, NOT
 * `If-Match: 0`. The CFKVClient + the K-Cont-3 reference impl both
 * verify this behavior in their TestCFKV_GenerationBumpedOnRelease
 * test.
 */
export async function clearLeaseBumpGen(
  ns: KVNamespace,
  slot: string,
  prevGeneration: number,
): Promise<LeaseState> {
  const next: LeaseState = {
    holder: "",
    acquiredAt: "",
    expiresAt: "",
    generation: prevGeneration + 1,
  };
  await writeLease(ns, slot, next);
  return next;
}
