// handlers/get.ts — GET /lease/<slot> handler.
//
// Per the K-Cont-3 contract:
//   - 200 + LeaseState body when the slot exists (whether or not it's
//     expired — TTL eviction is controller-side per trap #4).
//   - 404 when the slot is empty (KV returns null).
//   - 401 when bearer-token validation fails (handled in index.ts;
//     this handler runs only on authorized requests).
//
// The slot is the URL path tail after `/lease/`. The Worker's URL
// parser already URL-decodes `%2F` back to `/`, so the slot we pass
// downstream is the canonical "<namespace>/<name>" form the Go client
// sent before encoding.

import type { Env } from "../types";
import { readLease } from "../kv";
import { jsonResponse, notFoundResponse } from "../index";

export async function handleGet(
  _request: Request,
  env: Env,
  slot: string,
): Promise<Response> {
  const state = await readLease(env.OPENOVA_LEASES, slot);
  if (!state) {
    // Empty slot — the CFKVClient maps 404 to a zero-State with
    // Generation=0, then PUTs with `If-Match: 0` to take the slot.
    // (See client.go::Read switch on http.StatusNotFound.)
    return notFoundResponse("lease not found");
  }
  return jsonResponse(200, state);
}
