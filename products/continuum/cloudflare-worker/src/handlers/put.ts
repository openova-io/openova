// handlers/put.ts — PUT /lease/<slot> handler (acquire + renew).
//
// Per the K-Cont-3 contract this single endpoint serves two
// operations distinguished by the `op` field of the request body:
//
//   - acquire — claim an empty slot OR re-acquire the same slot you
//               already hold. The CAS guard is `If-Match` matching
//               the stored generation (or 0 for a never-touched slot).
//   - renew   — extend the TTL on a slot you already hold.
//               REQUIRES stored.holder === requested.holder AND
//               stored.expiresAt > now (per K-Cont-2 contract — Renew
//               is for the holder only).
//
// CAS semantics on PUT (per K-Cont-3 trap #1, "If-Match: 0 = first-
// acquire-on-empty-slot signal"):
//
//   stored generation | If-Match | accept?
//   ───────────────────┼──────────┼─────────
//   N/A (empty slot)   | 0        | YES (first acquire)
//   N/A (empty slot)   | non-0    | 412 (CAS conflict — caller has
//                                         stale view; client retries
//                                         after a fresh Read)
//   G                  | G        | YES if takeable (acquire-on-empty/
//                                                    expired/sameHolder)
//   G                  | != G     | 412 (CAS lost between caller's
//                                         Read and PUT; retry)
//
// Per K-Cont-3 trap #2 generation increments UNCONDITIONALLY on every
// successful write (including the case where the same holder
// re-acquires/renews — bump preserves "newest write wins" semantics
// for ANY observer, even one in our same region).
//
// Per K-Cont-3 trap #4 we stamp `expiresAt = now + ttlSeconds`
// server-side. Per trap #3 we include the current state body on 412
// so the client can use it for diagnostics.

import type { Env, LeaseState, LeaseWriteRequest } from "../types";
import { readLease, writeLease } from "../kv";
import {
  jsonResponse,
  preconditionFailedResponse,
  badRequestResponse,
} from "../index";

export async function handlePut(
  request: Request,
  env: Env,
  slot: string,
): Promise<Response> {
  // ─── Parse + validate body ─────────────────────────────────────────
  let body: LeaseWriteRequest;
  try {
    body = (await request.json()) as LeaseWriteRequest;
  } catch (err) {
    return badRequestResponse(
      "invalid_json",
      `body must be JSON: ${err instanceof Error ? err.message : String(err)}`,
    );
  }
  if (!body || typeof body !== "object") {
    return badRequestResponse("invalid_body", "body must be a JSON object");
  }
  if (typeof body.holder !== "string" || body.holder.length === 0) {
    return badRequestResponse("invalid_holder", "holder must be a non-empty string");
  }
  if (typeof body.ttlSeconds !== "number" || body.ttlSeconds <= 0 || !Number.isFinite(body.ttlSeconds)) {
    return badRequestResponse("invalid_ttl", "ttlSeconds must be a positive number");
  }
  if (body.op !== "acquire" && body.op !== "renew") {
    return badRequestResponse("invalid_op", `op must be "acquire" or "renew", got "${body.op}"`);
  }

  // ─── Parse If-Match ────────────────────────────────────────────────
  // Per the K-Cont-3 contract the header value is the integer
  // generation, NOT a quoted ETag. We accept both forms (some HTTP
  // clients quote by default) for forward compat.
  const ifMatchRaw = request.headers.get("If-Match");
  if (ifMatchRaw === null) {
    return badRequestResponse("missing_if_match", "If-Match header is required");
  }
  const ifMatch = parseIfMatch(ifMatchRaw);
  if (ifMatch === null) {
    return badRequestResponse(
      "invalid_if_match",
      `If-Match must be a non-negative integer, got "${ifMatchRaw}"`,
    );
  }

  const now = new Date();
  const nowRFC = now.toISOString();
  const cur = await readLease(env.OPENOVA_LEASES, slot);
  const curGen = cur?.generation ?? 0;

  // ─── CAS check ─────────────────────────────────────────────────────
  if (ifMatch !== curGen) {
    // CAS lost OR caller passed a stale generation.
    return preconditionFailedResponse(cur);
  }

  // ─── Decide whether the slot is takeable ───────────────────────────
  // Per K-Cont-3 trap #4, the Worker doesn't actively evict expired
  // records — but a PUT-acquire from a NEW holder needs to succeed
  // when the current holder's lease has expired. Mirror the Go
  // fakeWorker logic in client_test.go::serveLease (which is the
  // authoritative spec).
  //
  // "Released" record (post-DELETE) carries holder="" + bumped
  // generation. Treat it the same as an empty slot for takeability
  // — otherwise a re-acquire after Release would 412 forever
  // because the empty-holder shell isn't sameHolder OR expired.
  // (See `kv.ts::clearLeaseBumpGen` for the Release shape.)
  const released = cur !== null && cur.holder === "";
  const sameHolder = cur !== null && cur.holder !== "" && cur.holder === body.holder;
  const expired = cur !== null && cur.expiresAt !== "" && !isAfter(cur.expiresAt, now);
  const heldByOther = cur !== null && !released && !sameHolder && !expired;

  if (heldByOther) {
    // Slot is held by another, non-expired holder. Reject the acquire
    // (and any renew which would have already bounced on the renew
    // pre-check below). Include current state on 412 per trap #3.
    return preconditionFailedResponse(cur);
  }

  if (body.op === "renew") {
    // Renew requires sameHolder + non-expired. Reject otherwise per
    // K-Cont-2 contract — Renew is for the holder only.
    if (!sameHolder || expired) {
      return preconditionFailedResponse(cur);
    }
  }

  // ─── Compute new state ─────────────────────────────────────────────
  // Preserve `acquiredAt` when the same holder re-acquires/renews
  // a non-expired slot. Stamp a fresh `acquiredAt` when a NEW holder
  // takes over (the previous holder's lease expired, or this is a
  // first acquire on a never-held slot).
  let acquiredAt: string;
  if (sameHolder && !expired && cur && cur.acquiredAt !== "") {
    acquiredAt = cur.acquiredAt;
  } else {
    acquiredAt = nowRFC;
  }

  const ttlMs = Math.floor(body.ttlSeconds * 1000);
  const expiresAt = new Date(now.getTime() + ttlMs).toISOString();

  const next: LeaseState = {
    holder: body.holder,
    acquiredAt,
    expiresAt,
    generation: curGen + 1,
  };
  await writeLease(env.OPENOVA_LEASES, slot, next);

  // 200 always (not 201) — this matches the Go reference's
  // fakeWorker which returns 200 on both first-acquire and renew.
  // The CFKVClient accepts 200 and 201 but the contract is 200.
  return jsonResponse(200, next);
}

/**
 * Parse `If-Match` header. Accepts:
 *   - bare integer:    `0`, `42`, `123456789`
 *   - quoted ETag:     `"42"`, `W/"42"` (weak ETags accepted because
 *                       some HTTP libraries auto-add the W/ prefix)
 * Returns the parsed integer, or null when the value is malformed.
 */
function parseIfMatch(raw: string): number | null {
  const trimmed = raw.trim();
  // Strip optional W/ + surrounding quotes.
  const m = /^(?:W\/)?(?:"(.+)"|(.+))$/.exec(trimmed);
  if (!m) return null;
  const v = (m[1] ?? m[2] ?? "").trim();
  if (!/^\d+$/.test(v)) return null;
  const n = Number.parseInt(v, 10);
  if (!Number.isFinite(n) || n < 0) return null;
  return n;
}

/**
 * `isAfter(expiresAt, now)` — true when the RFC3339 `expiresAt` is
 * strictly after `now`. Mirrors the Go `time.Time.Before(t)` check
 * (`now.Before(t)` ↔ `t.After(now)`).
 */
function isAfter(rfc3339: string, now: Date): boolean {
  const t = new Date(rfc3339);
  if (Number.isNaN(t.getTime())) return false;
  return t.getTime() > now.getTime();
}
