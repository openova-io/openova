// handlers/delete.ts — DELETE /lease/<slot> handler (release).
//
// Per the K-Cont-3 contract:
//
//   DELETE /lease/<slot>
//     Authorization: Bearer <token>
//     If-Match: <generation>
//     X-Holder: <region>
//
//   → 204 No Content        success
//   → 412 Precondition Failed
//                            CAS lost (stale generation) OR
//                            X-Holder doesn't match stored holder
//                            (per trap #5: "stale region can't evict
//                             the new primary")
//   → 401 Unauthorized      bearer token missing or invalid
//
// Per K-Cont-3 trap #2 generation increments on Release too. The Go
// fakeWorker writes `{Generation: cur.Generation + 1}` (empty holder)
// on Release; we mirror that exact behavior so a subsequent Acquire
// presents `If-Match: <gen+1>` rather than rolling back to 0.
//
// Empty-slot DELETE is treated as 204 success (idempotent — the
// Release is the desired end state). The CFKVClient also has a
// client-side idempotency guard (`if cur.Holder == "" || cur.Holder
// != holder { return nil }`), so a stray DELETE on an empty slot
// shouldn't reach us in production — but we accept it gracefully.

import type { Env } from "../types";
import { readLease, clearLeaseBumpGen } from "../kv";
import {
  emptyResponse,
  preconditionFailedResponse,
  badRequestResponse,
} from "../index";

export async function handleDelete(
  request: Request,
  env: Env,
  slot: string,
): Promise<Response> {
  // ─── Parse If-Match ─────────────────────────────────────────────
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

  // ─── Parse X-Holder ─────────────────────────────────────────────
  const holder = request.headers.get("X-Holder");
  if (holder === null || holder.trim().length === 0) {
    return badRequestResponse("missing_x_holder", "X-Holder header is required");
  }

  const cur = await readLease(env.OPENOVA_LEASES, slot);
  if (cur === null) {
    // Idempotent — empty slot is the desired end state. 204.
    return emptyResponse(204);
  }

  // ─── CAS + holder-identity check (trap #5) ──────────────────────
  if (cur.generation !== ifMatch) {
    return preconditionFailedResponse(cur);
  }
  if (cur.holder !== holder.trim()) {
    // Stale region can't evict the new primary even with stale-but-
    // matching `If-Match`. (This is theoretically impossible if
    // generation matches because new-holder always bumps gen — but
    // belt-and-suspenders against any future code path that forgets
    // to bump.)
    return preconditionFailedResponse(cur);
  }

  // ─── Apply Release: bump generation, clear holder ───────────────
  await clearLeaseBumpGen(env.OPENOVA_LEASES, slot, cur.generation);
  return emptyResponse(204);
}

/**
 * Parse `If-Match` header. See put.ts for the spec.
 *
 * Duplicated rather than shared to keep handler files self-contained
 * — extracting to a shared module would require a runtime barrel
 * import that adds a few KB to the Worker bundle for no observable
 * benefit. If a third caller appears, lift to `src/headers.ts`.
 */
function parseIfMatch(raw: string): number | null {
  const trimmed = raw.trim();
  const m = /^(?:W\/)?(?:"(.+)"|(.+))$/.exec(trimmed);
  if (!m) return null;
  const v = (m[1] ?? m[2] ?? "").trim();
  if (!/^\d+$/.test(v)) return null;
  const n = Number.parseInt(v, 10);
  if (!Number.isFinite(n) || n < 0) return null;
  return n;
}
