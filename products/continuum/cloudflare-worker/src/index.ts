// index.ts — Cloudflare Worker entrypoint for the OpenOva Continuum
// lease-witness Worker (K-Cont-4 of EPIC-6 #1101).
//
// Routes:
//   GET    /lease/<slot-url-encoded>   → handlers/get.ts
//   PUT    /lease/<slot-url-encoded>   → handlers/put.ts (acquire+renew)
//   DELETE /lease/<slot-url-encoded>   → handlers/delete.ts
//   *      anything-else               → 405 / 404
//
// Per the K-Cont-3 Cloudflare KV Worker contract entry in
// .claude/architect-briefs/epic-0/01-canonical-seams.md THE FOUR
// behaviors below are non-negotiable:
//
//   1. Slot is the URL path tail after `/lease/`. URL-decoded by
//      the Worker's URL parser (so `%2F` becomes `/` again).
//   2. Bearer-token validation against env-bound allow-list applies
//      to ALL methods uniformly (no per-method auth differences).
//   3. Server-authoritative timestamp + generation: every PUT/DELETE
//      stamps `expiresAt = now + ttlSeconds` and bumps `generation`.
//   4. 412 SHOULD include current state body so the client can use
//      it for diagnostics.
//
// Per CLAUDE.md "10. CREDENTIAL HYGIENE" the bearer token is NEVER
// logged. The X-Lease-Slot header (optional, per trap #7) is logged
// for KV log granularity.

import { handleGet } from "./handlers/get";
import { handlePut } from "./handlers/put";
import { handleDelete } from "./handlers/delete";
import { isAuthorized } from "./auth";
import type { Env, ErrorBody, LeaseState } from "./types";

export default {
  async fetch(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
    void ctx; // ctx unused — no waitUntil/passThroughOnException needed here

    const url = new URL(request.url);
    const slot = extractSlot(url.pathname);

    // ─── Routing layer ─────────────────────────────────────────────
    if (slot === null) {
      // Non-/lease/ path — 404. We deliberately don't expose a /healthz
      // endpoint because the Worker has no warm-up state to report on
      // and CF Workers are pinged by the platform itself; an unauthed
      // /healthz would be a probe surface for nothing.
      return notFoundResponse("not found");
    }

    // ─── Auth layer ────────────────────────────────────────────────
    if (!isAuthorized(request, env)) {
      log(env, "info", `auth_rejected method=${request.method} slot=${slot}`);
      return unauthorizedResponse();
    }

    // ─── Method dispatch ───────────────────────────────────────────
    log(
      env,
      "debug",
      `method=${request.method} slot=${slot} if-match=${request.headers.get("If-Match") ?? "-"} x-holder=${request.headers.get("X-Holder") ?? "-"}`,
    );

    try {
      switch (request.method) {
        case "GET":
          return await handleGet(request, env, slot);
        case "PUT":
          return await handlePut(request, env, slot);
        case "DELETE":
          return await handleDelete(request, env, slot);
        case "OPTIONS":
          // CORS pre-flight — controller is server-side Go, not a
          // browser, but a future operator console could call this
          // Worker directly. Allow the methods we serve.
          return new Response(null, {
            status: 204,
            headers: {
              "Allow": "GET, PUT, DELETE, OPTIONS",
              "Access-Control-Allow-Methods": "GET, PUT, DELETE",
              "Access-Control-Allow-Headers":
                "Authorization, If-Match, X-Holder, X-Lease-Slot, Content-Type",
            },
          });
        default:
          return methodNotAllowedResponse();
      }
    } catch (err) {
      // Defensive — the handlers should not throw under happy-path
      // inputs, but a KV transient failure or a malformed JSON parse
      // could surface here. 500 with an error message body so the
      // CFKVClient's `readSnippet` logs something meaningful.
      const msg = err instanceof Error ? err.message : String(err);
      log(env, "error", `internal_error slot=${slot} method=${request.method} err=${msg}`);
      return jsonResponse<ErrorBody>(500, { error: "internal_error", reason: msg });
    }
  },
};

// ─── Routing helpers ───────────────────────────────────────────────────

/**
 * Extract the slot from a `/lease/<slot>` URL path. Returns null when
 * the path doesn't match `/lease/...`. The slot may contain `/` after
 * URL decoding (e.g. `ns/cr-main`); we keep it intact.
 */
export function extractSlot(pathname: string): string | null {
  // Strip trailing slash so `/lease/` (no slot) returns null cleanly.
  // (Not strictly needed but defensive against client weirdness.)
  if (!pathname.startsWith("/lease/")) return null;
  const tail = pathname.slice("/lease/".length);
  if (tail.length === 0) return null;
  return tail;
}

// ─── Response helpers ─────────────────────────────────────────────────
//
// Centralised so handlers don't reinvent Response shapes. Each helper
// sets `Content-Type` correctly and stamps the K-Cont-3 contract
// status codes.

export function jsonResponse<T = LeaseState | ErrorBody>(
  status: number,
  body: T,
): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export function emptyResponse(status: number): Response {
  return new Response(null, { status });
}

export function notFoundResponse(reason: string): Response {
  return jsonResponse<ErrorBody>(404, { error: "not_found", reason });
}

export function unauthorizedResponse(): Response {
  // Per K-Cont-3 contract 401 → CFKVClient surfaces "auth rejected"
  // error and the controller flags `LeaseHeld=False` Reason=AuthFailed.
  return jsonResponse<ErrorBody>(401, { error: "unauthorized" });
}

export function methodNotAllowedResponse(): Response {
  return jsonResponse<ErrorBody>(405, { error: "method_not_allowed" });
}

export function badRequestResponse(error: string, reason: string): Response {
  return jsonResponse<ErrorBody>(400, { error, reason });
}

/**
 * 412 Precondition Failed — CAS lost OR (DELETE) holder mismatch.
 * Per K-Cont-3 trap #3 the body SHOULD include the current state
 * when known so the client can diagnose. We always include the
 * current state when non-null; on empty slots we send an empty body.
 */
export function preconditionFailedResponse(cur: LeaseState | null): Response {
  if (cur === null) {
    return new Response(null, { status: 412 });
  }
  return jsonResponse<LeaseState>(412, cur);
}

// ─── Logging helper ────────────────────────────────────────────────────
//
// Filters by LOG_LEVEL (default "info"). NEVER logs the Authorization
// header value — only the boolean outcome of auth (logged as
// "auth_rejected" with no token contents).

const LEVELS: Record<string, number> = { error: 0, info: 1, debug: 2 };

function log(env: Env, level: "error" | "info" | "debug", msg: string): void {
  const want = LEVELS[env.LOG_LEVEL ?? "info"] ?? LEVELS.info;
  const got = LEVELS[level];
  if (got > want) return;
  // Workers Logs picks up console.log / console.error via the
  // observability binding declared in wrangler.toml.
  if (level === "error") {
    console.error(msg);
  } else {
    console.log(msg);
  }
}
