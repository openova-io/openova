// #5634 (UAT row 92) — the funnel must surface the gateway's 429, and must NOT
// surface it on a healthy submit.
//
// Both directions are asserted on purpose. A test that only proves "429 shows the
// notice" passes just as happily against a page that shows the notice
// unconditionally, which would be a worse bug than the one being fixed.

import { beforeEach, describe, expect, it } from 'vitest';

import {
  applyRedeemOutcome,
  classifyRedeemPreview,
  GENERIC_VALIDATE_DETAIL,
  NOT_VALID_DEFAULT_DETAIL,
  REDEEM_PANELS,
  showRedeemPanel,
  type RedeemPanelId,
} from './redeemPreview';
import { DEFAULT_RETRY_AFTER_SEC, rateLimitMessage, retryAfterSeconds } from './rateLimitNotice';

// The exact body `core/services/gateway/ratelimit.go` writes when the
// voucher-redeem burst limiter trips (PR #4028).
const GATEWAY_REDEEM_429 = {
  error: 'too many redeem attempts — please wait a few seconds and try again',
  retry_after: 10,
};

// The body the global per-minute limiter writes in the same middleware.
const GATEWAY_GLOBAL_429 = { error: 'rate limit exceeded', retry_after: 37 };

const VALID_PREVIEW = {
  code: 'OMANI-WELCOME-25',
  credit_omr: 25,
  description: 'Welcome credit',
  active: true,
  accepting_redemptions: true,
};

/** Rebuild the panel skeleton that `src/pages/redeem.astro` renders. */
function mountPanels(): void {
  document.body.innerHTML = REDEEM_PANELS.map(id => {
    const detail =
      id === 'redeem-not-valid'
        ? `<p id="redeem-not-valid-detail">${NOT_VALID_DEFAULT_DETAIL}</p>`
        : id === 'redeem-throttled'
          ? '<p id="redeem-throttled-detail"></p>'
          : '';
    return `<div id="${id}" class="hidden">${detail}</div>`;
  }).join('');
}

function visiblePanels(): string[] {
  return REDEEM_PANELS.filter(id => !document.getElementById(id)!.classList.contains('hidden'));
}

function throttleText(): string {
  return document.getElementById('redeem-throttled-detail')!.textContent ?? '';
}

function notValidText(): string {
  return document.getElementById('redeem-not-valid-detail')!.textContent ?? '';
}

/** Drive the page's decision the way `validate()` does: classify, then apply. */
function submit(status: number, body: unknown, headers?: Headers): RedeemPanelId {
  const outcome = classifyRedeemPreview(status, body, headers);
  applyRedeemOutcome(document, outcome);
  return outcome.panel;
}

beforeEach(() => {
  mountPanels();
});

describe('a rate-limited redeem submit IS surfaced', () => {
  it('routes the gateway 429 to its own panel, not to "Voucher not valid"', () => {
    expect(submit(429, GATEWAY_REDEEM_429)).toBe('redeem-throttled');
    expect(visiblePanels()).toEqual(['redeem-throttled']);
    // The regression this fixes: 429 reaching the not-valid panel.
    expect(visiblePanels()).not.toContain('redeem-not-valid');
  });

  it('tells the customer what happened and how long to wait', () => {
    submit(429, GATEWAY_REDEEM_429);
    const text = throttleText();
    expect(text).not.toBe('');
    // What happened.
    expect(text).toMatch(/several times in a few seconds/i);
    // What to do — the server's own retry window, in seconds.
    expect(text).toContain('Wait 10 seconds');
    // Not a raw status code, and not an apology.
    expect(text).not.toContain('429');
    expect(text).not.toMatch(/sorry|apolog/i);
    // Not the generic string that used to swallow this response.
    expect(text).not.toBe(GENERIC_VALIDATE_DETAIL);
  });

  it('does not tell a throttled customer their voucher is invalid', () => {
    submit(429, GATEWAY_REDEEM_429);
    expect(throttleText()).toMatch(/still fine/i);
    expect(throttleText()).not.toMatch(/not valid|does not exist|retired/i);
  });

  it('honours the global limiter body shape too', () => {
    expect(submit(429, GATEWAY_GLOBAL_429)).toBe('redeem-throttled');
    expect(throttleText()).toContain('Wait 37 seconds');
  });

  it('still renders a usable wait when the server sends no retry_after', () => {
    expect(submit(429, { error: 'rate limit exceeded' })).toBe('redeem-throttled');
    expect(throttleText()).toContain(`Wait ${DEFAULT_RETRY_AFTER_SEC} seconds`);
    expect(throttleText()).not.toMatch(/NaN|undefined|null/);
  });

  it('still renders a usable wait when the 429 carries no body at all', () => {
    expect(submit(429, null)).toBe('redeem-throttled');
    expect(throttleText()).toContain(`Wait ${DEFAULT_RETRY_AFTER_SEC} seconds`);
  });

  // #5634 second pass: the gateway/CDN answers some 429s itself, with the
  // standard `Retry-After` header and no JSON body. Reading only the body meant
  // those fell back to the 10s default however long the real window was.
  it('takes the wait from the Retry-After header when the body has none', () => {
    expect(submit(429, null, new Headers({ 'Retry-After': '45' }))).toBe('redeem-throttled');
    expect(throttleText()).toContain('Wait 45 seconds');
  });

  it('never advises a shorter wait than the response asked for', () => {
    submit(429, GATEWAY_REDEEM_429, new Headers({ 'Retry-After': '60' }));
    expect(throttleText()).toContain('Wait 60 seconds');
    expect(throttleText()).not.toContain('Wait 10 seconds');
  });

  it('ignores an absent or unusable Retry-After and keeps the body window', () => {
    submit(429, GATEWAY_REDEEM_429, new Headers());
    expect(throttleText()).toContain('Wait 10 seconds');
    submit(429, GATEWAY_REDEEM_429, new Headers({ 'Retry-After': 'soon' }));
    expect(throttleText()).toContain('Wait 10 seconds');
  });
});

describe('a normal submit does NOT surface the rate-limit notice', () => {
  it('shows the valid panel on 200 and leaves the throttle copy empty', () => {
    expect(submit(200, VALID_PREVIEW)).toBe('redeem-valid');
    expect(visiblePanels()).not.toContain('redeem-throttled');
    expect(throttleText()).toBe('');
  });

  it('keeps 404 on the not-valid panel with its own copy', () => {
    expect(submit(404, null)).toBe('redeem-not-valid');
    expect(visiblePanels()).toEqual(['redeem-not-valid']);
    expect(notValidText()).toBe(NOT_VALID_DEFAULT_DETAIL);
    expect(throttleText()).toBe('');
  });

  it('keeps 410 on the campaign-ended panel', () => {
    expect(submit(410, { code: 'OLD', credit_omr: 5 })).toBe('redeem-ended');
    expect(visiblePanels()).toEqual(['redeem-ended']);
    expect(throttleText()).toBe('');
  });

  it('keeps other server errors on the generic not-valid copy', () => {
    expect(submit(500, null)).toBe('redeem-not-valid');
    expect(notValidText()).toBe(GENERIC_VALIDATE_DETAIL);
    expect(throttleText()).toBe('');
  });

  it('does not leave the notice on screen after a throttled submit succeeds on retry', () => {
    submit(429, GATEWAY_REDEEM_429);
    expect(visiblePanels()).toEqual(['redeem-throttled']);
    // The retry the customer makes after waiting out the window.
    expect(submit(200, VALID_PREVIEW)).toBe('redeem-valid');
    expect(visiblePanels()).toEqual(['redeem-valid']);
    expect(document.getElementById('redeem-throttled')!.classList.contains('hidden')).toBe(true);
  });
});

describe('showRedeemPanel', () => {
  it('reveals exactly one panel', () => {
    showRedeemPanel(document, 'redeem-loading');
    expect(visiblePanels()).toEqual(['redeem-loading']);
    showRedeemPanel(document, 'redeem-missing');
    expect(visiblePanels()).toEqual(['redeem-missing']);
  });
});

describe('retryAfterSeconds', () => {
  it('uses the server value when usable', () => {
    expect(retryAfterSeconds({ retry_after: 10 })).toBe(10);
    expect(retryAfterSeconds({ retry_after: '25' })).toBe(25);
    expect(retryAfterSeconds({ retry_after: 1.2 })).toBe(2); // never advise a shorter wait
  });

  it('falls back on missing or nonsense values', () => {
    for (const body of [null, undefined, {}, { retry_after: 0 }, { retry_after: -5 }, { retry_after: 'soon' }]) {
      expect(retryAfterSeconds(body)).toBe(DEFAULT_RETRY_AFTER_SEC);
    }
  });
});

describe('rateLimitMessage', () => {
  it('names checkout on the checkout path and says nothing was charged', () => {
    const text = rateLimitMessage(GATEWAY_GLOBAL_429, 'checkout');
    expect(text).toMatch(/submitted checkout several times/i);
    expect(text).toContain('Wait 37 seconds');
    expect(text).toMatch(/nothing has been charged/i);
    expect(text).not.toContain('429');
  });

  it('uses the singular for a one-second window', () => {
    expect(rateLimitMessage({ retry_after: 1 }, 'redeem')).toContain('Wait 1 second,');
  });
});
