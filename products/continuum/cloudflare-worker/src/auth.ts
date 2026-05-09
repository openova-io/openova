// auth.ts — bearer-token validation for the lease-witness Worker.
//
// Per K-Cont-3 trap #6, "Bearer token validation against env-bound
// allow-list (no per-account path scoping)". One Worker can serve
// multiple Continuum CRs across multiple regions/holders; access
// control is binary: any token in the allow-list is allowed to call
// any path. Per-CR isolation is by `slot` (URL path), not by token
// scoping.
//
// Per CLAUDE.md credential hygiene: NEVER log the token value, never
// echo it in error responses, never persist it. Only the boolean
// outcome of validation may be logged.

import type { Env } from "./types";

/**
 * Parse the bearer-tokens allow-list from the env var. Tokens are
 * comma-separated; whitespace around each token is trimmed. Empty
 * tokens are filtered out.
 *
 * Defensively cached per-request: the parser is cheap and called once
 * per request. If callers want to share the parsed set across requests
 * they should pass it through; we explicitly do NOT cache at module
 * scope to avoid the V8-isolate-shared-state surprise (one Worker
 * isolate may serve many tenants if account allow-lists ever differ).
 *
 * @returns Set<string> of valid tokens. Empty when env var is unset.
 */
export function parseAllowList(env: Env): Set<string> {
  const raw = env.BEARER_TOKENS_CSV ?? "";
  const tokens = raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
  return new Set(tokens);
}

/**
 * Extract the bearer token from an Authorization header and check it
 * against the allow-list. Returns the boolean outcome ONLY — never
 * the token itself, never the header value. Constant-time string
 * compare is NOT used here because:
 *
 *   1. The allow-list lives in env (not user-controllable per request)
 *      so timing-side-channel discovery would only leak which token
 *      the operator chose, not the secret value.
 *   2. Set.has() in V8 is O(1) hash; effective compare time depends on
 *      hash distribution, not token length.
 *
 * If we later move to per-tenant tokens with attacker-controllable
 * inputs, swap to a constant-time compare via crypto.subtle.timingSafeEqual.
 *
 * @param request — the incoming Request (we read its `Authorization` header)
 * @param env     — Worker env containing BEARER_TOKENS_CSV
 * @returns true when the request carries a valid bearer token; false
 *          when the header is missing, malformed, or the token is not
 *          in the allow-list.
 */
export function isAuthorized(request: Request, env: Env): boolean {
  const header = request.headers.get("Authorization");
  if (!header) return false;

  // Match `Bearer <token>` exactly. The `Bearer` keyword is
  // case-insensitive per RFC 6750 §2.1; we accept any casing.
  const match = /^Bearer\s+(.+)$/i.exec(header);
  if (!match) return false;

  const token = match[1].trim();
  if (!token) return false;

  const allow = parseAllowList(env);
  if (allow.size === 0) {
    // Fail-closed when the operator forgot to set the allow-list.
    // Better to refuse every request (loud Worker 401s in the
    // controller's logs) than to default-allow.
    return false;
  }
  return allow.has(token);
}
