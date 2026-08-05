// #5634 (UAT row 92) — turn the gateway's 429 into something a customer can act on.
//
// `core/services/gateway/ratelimit.go` guards the funnel's submit paths with two
// limiters, both of which answer HTTP 429 with a JSON body carrying a
// `retry_after` field measured in seconds:
//
//   * the path-scoped voucher burst limiter (PR #4028), on the
//     `/api/billing/vouchers/redeem` prefix —
//     {"error":"too many redeem attempts — please wait a few seconds and try again","retry_after":10}
//   * the global per-minute limiter —
//     {"error":"rate limit exceeded","retry_after":<60-now.Second()>}
//
// The funnel used to discard both shapes, so a throttled customer was told their
// voucher was invalid. These helpers keep the server's own retry window and turn
// it into a notice that names what happened and how long to wait.

/** Fallback wait when the response carries no usable window at all — matches the
 *  gateway's own `redeemWindowSec` default. Only ever used when BOTH the
 *  `Retry-After` header and the body's `retry_after` are absent or unusable. */
export const DEFAULT_RETRY_AFTER_SEC = 10;

/** The subject of the throttled submit, so the notice can name the right thing. */
export type RateLimitSubject = 'redeem' | 'checkout';

type RateLimitBody = { error?: unknown; retry_after?: unknown };

/** Just enough of `Headers` to read one field — so callers can hand us a real
 *  `Response.headers`, a plain `new Headers({...})`, or nothing at all. */
export type HeaderBag = { get(name: string): string | null } | null | undefined;

/** Parse one candidate window. Returns null when it is absent or unusable. */
function usableSeconds(raw: unknown): number | null {
  const seconds = typeof raw === 'number' ? raw : Number(raw);
  if (!Number.isFinite(seconds) || seconds <= 0) return null;
  return Math.ceil(seconds);
}

/**
 * Read the standard `Retry-After` response header (RFC 9110 §10.2.3).
 *
 * Our own services put the window in the JSON body, but a 429 does not always
 * come from our own services — the Cilium/Envoy gateway in front of them, or any
 * CDN, answers with the header and frequently with no JSON body at all. Reading
 * only the body meant those responses fell back to the 10s default no matter how
 * long the real window was.
 *
 * Both wire forms are accepted: delta-seconds (`Retry-After: 60`) and an
 * HTTP-date (`Retry-After: Wed, 21 Oct 2026 07:28:00 GMT`).
 */
export function retryAfterFromHeader(headers: HeaderBag, now: number = Date.now()): number | null {
  const raw = headers?.get('Retry-After');
  if (raw == null) return null;
  const trimmed = String(raw).trim();
  if (trimmed === '') return null;
  const delta = usableSeconds(trimmed);
  if (delta !== null) return delta;
  const at = Date.parse(trimmed);
  if (!Number.isFinite(at)) return null;
  return usableSeconds((at - now) / 1000);
}

/**
 * Read the server's retry window from the response.
 *
 * When the header and the body both carry a window we take the LARGER of the
 * two. Advising a shorter wait than the server actually enforces is the specific
 * harm here: the customer retries too early, re-trips the limiter, and the
 * throttling gets worse. Overshooting costs a few seconds and nothing else.
 *
 * Tolerates every field being absent, a string, or nonsense — a customer-facing
 * wait must never render as `NaN` or `undefined`. Fractional windows round up.
 */
export function retryAfterSeconds(body: unknown, headers?: HeaderBag): number {
  const fromBody = usableSeconds((body as RateLimitBody | null | undefined)?.retry_after);
  const fromHeader = retryAfterFromHeader(headers);
  const candidates = [fromBody, fromHeader].filter((n): n is number => n !== null);
  if (candidates.length === 0) return DEFAULT_RETRY_AFTER_SEC;
  return Math.max(...candidates);
}

/**
 * The customer-facing notice. Deliberately states the cause (too many submits in
 * a row) and the remedy (wait this long, then retry) rather than echoing a status
 * code. For the redeem path it also says the code itself is still good, because
 * the defect this fixes told throttled customers their voucher was invalid.
 */
export function rateLimitMessage(body: unknown, subject: RateLimitSubject, headers?: HeaderBag): string {
  const seconds = retryAfterSeconds(body, headers);
  const wait = seconds === 1 ? '1 second' : `${seconds} seconds`;
  if (subject === 'redeem') {
    return `You have submitted this voucher several times in a few seconds. `
      + `Wait ${wait}, then try again — the code itself is still fine.`;
  }
  return `You have submitted checkout several times in a few seconds. `
    + `Wait ${wait}, then try again — nothing has been charged.`;
}

/** Best-effort JSON parse of a response body that may be empty or not JSON. */
export function parseRateLimitBody(raw: string): unknown {
  try {
    return JSON.parse(raw);
  } catch {
    return null;
  }
}
