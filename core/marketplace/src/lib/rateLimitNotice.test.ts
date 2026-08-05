// #5634 (UAT row 92) — the CHECKOUT half of the funnel's 429 handling, plus the
// two siblings in `api.ts` that swallow the same response in other ways.
//
// `redeemPreview.test.ts` covers the /redeem landing. Nothing covered the
// checkout submit path: `requestErrorMessage` was module-private and every
// assertion went through `rateLimitMessage` directly, so deleting the 429 branch
// in `api.ts` left the whole suite green. These tests drive the REAL exported
// call sites (`createCheckout`, `redeemVoucherPreview`) through a mocked fetch,
// so the wiring is what is under test, not a re-implementation of it.
//
// Every throttle assertion is paired with its negative. A guard that only proves
// "429 shows the notice" is equally satisfied by relabelling every failure as a
// rate limit, which would be a worse defect than the one being fixed.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  createCheckout,
  redeemVoucherPreview,
  requestErrorMessage,
  RateLimitError,
} from './api';
import {
  DEFAULT_RETRY_AFTER_SEC,
  retryAfterFromHeader,
  retryAfterSeconds,
} from './rateLimitNotice';

// The body the billing service sends once #5634's server half is in
// (`core/services/billing/handlers/handlers.go` — 5 checkouts per 60s per User).
const BILLING_CHECKOUT_429 = JSON.stringify({
  error: 'checkout rate-limit exceeded — please wait before retrying',
  retry_after: 60,
});

// The body the gateway's global limiter sends (`core/services/gateway/ratelimit.go`).
const GATEWAY_GLOBAL_429 = JSON.stringify({ error: 'rate limit exceeded', retry_after: 37 });

function res(status: number, body: string, headers: Record<string, string> = {}): Response {
  return new Response(body, {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });
}

/** Answer each successive fetch with the next queued response. */
function mockFetch(...responses: Response[]): ReturnType<typeof vi.fn> {
  const fn = vi.fn();
  for (const r of responses) fn.mockResolvedValueOnce(r);
  vi.stubGlobal('fetch', fn);
  return fn;
}

const CHECKOUT_BODY = { plan_id: 'plan-m', tenant_id: 'org-1', apps: [], subdomain: 'acme' } as any;

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('a throttled checkout submit tells the customer to wait', () => {
  it('turns the billing 429 into the wait-and-retry notice, not a raw status blob', async () => {
    mockFetch(res(429, BILLING_CHECKOUT_429, { 'Retry-After': '60' }));

    await expect(createCheckout(CHECKOUT_BODY)).rejects.toBeInstanceOf(RateLimitError);
  });

  it('renders what happened, how long to wait, and that nothing was charged', async () => {
    mockFetch(res(429, BILLING_CHECKOUT_429, { 'Retry-After': '60' }));

    const err = await createCheckout(CHECKOUT_BODY).catch(e => e as Error);
    const msg = err.message;

    expect(msg).toMatch(/submitted checkout several times in a few seconds/i);
    expect(msg).toContain('Wait 60 seconds');
    expect(msg).toMatch(/nothing has been charged/i);
    // Not the pre-fix shape: a raw status code joined to a raw JSON blob.
    expect(msg).not.toContain('429');
    expect(msg).not.toContain('retry_after');
    expect(msg).not.toContain('{');
    // Copy rules: no apology, no vagueness.
    expect(msg).not.toMatch(/sorry|apolog|something went wrong|oops/i);
    expect(msg).not.toMatch(/NaN|undefined|null/);
  });

  it('carries the machine-readable window so a caller can act on it', async () => {
    mockFetch(res(429, BILLING_CHECKOUT_429, { 'Retry-After': '60' }));
    const err = (await createCheckout(CHECKOUT_BODY).catch(e => e)) as RateLimitError;
    expect(err).toBeInstanceOf(RateLimitError);
    expect(err.status).toBe(429);
    expect(err.retryAfterSec).toBe(60);
  });

  it('honours a Retry-After header when the 429 carries NO body at all', async () => {
    // What an intermediary (the Cilium/Envoy gateway, a CDN) answers with.
    mockFetch(res(429, '', { 'Retry-After': '45' }));
    const err = (await createCheckout(CHECKOUT_BODY).catch(e => e)) as Error;
    expect(err.message).toContain('Wait 45 seconds');
  });

  it('never advises a shorter wait than the response asked for', async () => {
    // Header and body disagree — taking the smaller one guarantees the retry
    // re-trips the limiter, which is the harm this whole issue is about.
    mockFetch(res(429, GATEWAY_GLOBAL_429, { 'Retry-After': '60' }));
    const err = (await createCheckout(CHECKOUT_BODY).catch(e => e)) as Error;
    expect(err.message).toContain('Wait 60 seconds');
    expect(err.message).not.toContain('Wait 37 seconds');
  });

  it('still gives a usable wait when neither the header nor the body carries one', async () => {
    mockFetch(res(429, JSON.stringify({ error: 'rate limit exceeded' })));
    const err = (await createCheckout(CHECKOUT_BODY).catch(e => e)) as Error;
    expect(err.message).toContain(`Wait ${DEFAULT_RETRY_AFTER_SEC} seconds`);
    expect(err.message).not.toMatch(/NaN|undefined/);
  });
});

describe('a genuine failure is NOT relabelled as a rate limit', () => {
  it('keeps a 500 on the generic status shape', async () => {
    mockFetch(res(500, JSON.stringify({ error: 'failed to look up customer' })));
    const err = (await createCheckout(CHECKOUT_BODY).catch(e => e)) as Error;

    expect(err).not.toBeInstanceOf(RateLimitError);
    expect(err.message).toContain('500');
    expect(err.message).not.toMatch(/several times in a few seconds|Wait \d+ second/i);
  });

  it('keeps a 503 on the generic status shape', async () => {
    mockFetch(res(503, JSON.stringify({ error: 'payment processor not configured' })));
    const err = (await createCheckout(CHECKOUT_BODY).catch(e => e)) as Error;
    expect(err).not.toBeInstanceOf(RateLimitError);
    expect(err.message).toContain('503');
    expect(err.message).not.toMatch(/Wait \d+ second/i);
  });

  it('keeps the `409: ...` prefix CheckoutStep matches on for slug collisions', async () => {
    // `CheckoutStep.svelte` retries a taken slug via `msg.startsWith('409')`.
    // The 429 branch must not have disturbed that shape.
    mockFetch(res(409, JSON.stringify({ error: 'slug is already taken' })));
    const err = (await createCheckout(CHECKOUT_BODY).catch(e => e)) as Error;
    expect(err.message.startsWith('409')).toBe(true);
    expect(err.message).toContain('slug is already taken');
  });

  it('a healthy 200 throws nothing and shows no notice', async () => {
    mockFetch(res(200, JSON.stringify({ order_id: 'ord-1', paid_by_credit: true })));
    await expect(createCheckout(CHECKOUT_BODY)).resolves.toMatchObject({ order_id: 'ord-1' });
  });
});

describe('a throttled voucher preview does not masquerade as an invalid code', () => {
  it('rejects on 429 instead of silently contributing zero credit', async () => {
    mockFetch(res(429, BILLING_CHECKOUT_429, { 'Retry-After': '60' }));
    const err = (await redeemVoucherPreview('OMANI-WELCOME-25').catch(e => e)) as RateLimitError;
    expect(err).toBeInstanceOf(RateLimitError);
    expect(err.retryAfterSec).toBe(60);
  });

  it('still resolves to null on a genuinely unknown code', async () => {
    // 404/410 mean "this code does not apply" — unchanged, and deliberately NOT
    // an error, so the cart shows no alarm for a typo.
    mockFetch(res(404, JSON.stringify({ error: 'voucher not found' })));
    await expect(redeemVoucherPreview('NOPE')).resolves.toBeNull();
  });

  it('still resolves to null on a retired campaign', async () => {
    mockFetch(res(410, JSON.stringify({ error: 'campaign ended' })));
    await expect(redeemVoucherPreview('OLD')).resolves.toBeNull();
  });
});

describe('a throttled token refresh does not end the session', () => {
  const seedSession = () => {
    localStorage.setItem('org-token', 'tok-old');
    localStorage.setItem('org-refresh-token', 'refresh-old');
  };

  it('keeps the customer signed in when /auth/refresh is rate-limited', async () => {
    seedSession();
    // The original call 401s, so the funnel tries a refresh; the refresh itself
    // is throttled — exactly what UAT row 92's rapid-retry burst produces.
    mockFetch(res(401, JSON.stringify({ error: 'token expired' })), res(429, GATEWAY_GLOBAL_429));

    await createCheckout(CHECKOUT_BODY).catch(() => undefined);

    expect(localStorage.getItem('org-token')).toBe('tok-old');
    expect(localStorage.getItem('org-refresh-token')).toBe('refresh-old');
  });

  it('keeps the customer signed in when /auth/refresh is briefly unavailable', async () => {
    seedSession();
    mockFetch(res(401, '{}'), res(503, JSON.stringify({ error: 'upstream unavailable' })));

    await createCheckout(CHECKOUT_BODY).catch(() => undefined);

    expect(localStorage.getItem('org-token')).toBe('tok-old');
  });

  it('STILL ends the session when the refresh credentials are actually rejected', async () => {
    // The negative direction: this fix must not become "never sign anyone out".
    seedSession();
    mockFetch(res(401, '{}'), res(401, JSON.stringify({ error: 'invalid refresh token' })));

    await createCheckout(CHECKOUT_BODY).catch(() => undefined);

    expect(localStorage.getItem('org-token')).toBeNull();
    expect(localStorage.getItem('org-refresh-token')).toBeNull();
  });

  it('STILL ends the session on a 403 refresh rejection', async () => {
    seedSession();
    mockFetch(res(401, '{}'), res(403, JSON.stringify({ error: 'refresh token revoked' })));

    await createCheckout(CHECKOUT_BODY).catch(() => undefined);

    expect(localStorage.getItem('org-token')).toBeNull();
  });
});

describe('retryAfterFromHeader', () => {
  const bag = (v: string | null) => ({ get: () => v });

  it('reads delta-seconds', () => {
    expect(retryAfterFromHeader(bag('60'))).toBe(60);
    expect(retryAfterFromHeader(bag(' 45 '))).toBe(45);
  });

  it('reads an HTTP-date', () => {
    const now = Date.parse('2026-08-06T07:00:00Z');
    expect(retryAfterFromHeader(bag('Thu, 06 Aug 2026 07:00:30 GMT'), now)).toBe(30);
  });

  it('returns null when absent, empty, past, or nonsense', () => {
    const now = Date.parse('2026-08-06T07:00:00Z');
    expect(retryAfterFromHeader(undefined)).toBeNull();
    expect(retryAfterFromHeader(bag(null))).toBeNull();
    expect(retryAfterFromHeader(bag(''))).toBeNull();
    expect(retryAfterFromHeader(bag('0'))).toBeNull();
    expect(retryAfterFromHeader(bag('-5'))).toBeNull();
    expect(retryAfterFromHeader(bag('soon'))).toBeNull();
    // Already elapsed — no wait to advise.
    expect(retryAfterFromHeader(bag('Thu, 06 Aug 2026 06:59:00 GMT'), now)).toBeNull();
  });
});

describe('retryAfterSeconds combines both carriers', () => {
  const bag = (v: string | null) => ({ get: () => v });

  it('uses the header when the body has none', () => {
    expect(retryAfterSeconds({}, bag('30'))).toBe(30);
  });

  it('uses the body when there is no header', () => {
    expect(retryAfterSeconds({ retry_after: 30 })).toBe(30);
    expect(retryAfterSeconds({ retry_after: 30 }, bag(null))).toBe(30);
  });

  it('takes the larger when both are present', () => {
    expect(retryAfterSeconds({ retry_after: 10 }, bag('60'))).toBe(60);
    expect(retryAfterSeconds({ retry_after: 60 }, bag('10'))).toBe(60);
  });

  it('falls back only when neither carrier is usable', () => {
    expect(retryAfterSeconds(null, bag('nope'))).toBe(DEFAULT_RETRY_AFTER_SEC);
    expect(retryAfterSeconds({ retry_after: 'soon' })).toBe(DEFAULT_RETRY_AFTER_SEC);
  });
});

describe('requestErrorMessage', () => {
  it('maps 429 to the notice and everything else to the status shape', () => {
    expect(requestErrorMessage(429, BILLING_CHECKOUT_429)).toContain('Wait 60 seconds');
    expect(requestErrorMessage(500, 'boom')).toBe('500: boom');
    expect(requestErrorMessage(409, 'slug is already taken')).toBe('409: slug is already taken');
  });
});
