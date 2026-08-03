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

/** Fallback wait when the server sends no usable `retry_after` — matches the
 *  gateway's own `redeemWindowSec` default. */
export const DEFAULT_RETRY_AFTER_SEC = 10;

/** The subject of the throttled submit, so the notice can name the right thing. */
export type RateLimitSubject = 'redeem' | 'checkout';

type RateLimitBody = { error?: unknown; retry_after?: unknown };

/**
 * Read the server's retry window. Tolerates the field being absent, a string, or
 * nonsense — a customer-facing wait must never render as `NaN` or `undefined`.
 * Fractional windows round up so we never advise a wait shorter than the server's.
 */
export function retryAfterSeconds(body: unknown): number {
  const raw = (body as RateLimitBody | null | undefined)?.retry_after;
  const seconds = typeof raw === 'number' ? raw : Number(raw);
  if (!Number.isFinite(seconds) || seconds <= 0) return DEFAULT_RETRY_AFTER_SEC;
  return Math.ceil(seconds);
}

/**
 * The customer-facing notice. Deliberately states the cause (too many submits in
 * a row) and the remedy (wait this long, then retry) rather than echoing a status
 * code. For the redeem path it also says the code itself is still good, because
 * the defect this fixes told throttled customers their voucher was invalid.
 */
export function rateLimitMessage(body: unknown, subject: RateLimitSubject): string {
  const seconds = retryAfterSeconds(body);
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
