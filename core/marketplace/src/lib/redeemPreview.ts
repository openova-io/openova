// #5634 (UAT row 92) — the /redeem landing's response-to-panel decision, lifted
// out of `src/pages/redeem.astro` so it can be tested without a browser.
//
// The page owns a set of sibling panels and reveals exactly one. Before this
// module existed the page handled 404 and 410 by hand and then funnelled EVERY
// other non-ok status — 429 included — into one `if (!res.ok)` branch that showed
// the "Voucher not valid" panel. A rate-limited customer was therefore told their
// code was bad. 429 now gets its own panel and carries the server's retry window.

import { rateLimitMessage, type HeaderBag } from './rateLimitNotice';

export const REDEEM_PANELS = [
  'redeem-loading',
  'redeem-missing',
  'redeem-not-valid',
  'redeem-ended',
  'redeem-throttled',
  'redeem-valid',
] as const;

export type RedeemPanelId = (typeof REDEEM_PANELS)[number];

/** Shown when the voucher service answers, but with something we cannot read. */
export const GENERIC_VALIDATE_DETAIL =
  'Could not validate the voucher right now. Please try again in a moment.';

/** The panel's own resting copy, restored on 404 so a previous failure's detail
 *  text cannot bleed into an unrelated "unknown code" answer. */
export const NOT_VALID_DEFAULT_DETAIL = 'This code does not exist or has been retired.';

export type RedeemOutcome =
  | { panel: 'redeem-valid' }
  | { panel: 'redeem-ended' }
  | { panel: 'redeem-not-valid'; detail: string }
  | { panel: 'redeem-throttled'; detail: string };

/**
 * Map a redeem-preview response onto the panel the customer should see.
 * `status` is the HTTP status; `body` is the parsed JSON body, or null when the
 * response carried none; `headers` is the response's own header bag, which is
 * where an intermediary (the Cilium/Envoy gateway, a CDN) puts its retry window
 * when it answers the 429 itself and sends no JSON at all.
 */
export function classifyRedeemPreview(status: number, body: unknown, headers?: HeaderBag): RedeemOutcome {
  if (status === 404) return { panel: 'redeem-not-valid', detail: NOT_VALID_DEFAULT_DETAIL };
  if (status === 410) return { panel: 'redeem-ended' };
  // #5634: 429 is answered by the gateway limiter, not by the voucher store —
  // it says nothing about whether the code is valid, so it must not reach the
  // "Voucher not valid" panel.
  if (status === 429) return { panel: 'redeem-throttled', detail: rateLimitMessage(body, 'redeem', headers) };
  // Everything else non-2xx keeps the previous generic treatment (`!res.ok`).
  if (status < 200 || status >= 300) {
    return { panel: 'redeem-not-valid', detail: GENERIC_VALIDATE_DETAIL };
  }
  return { panel: 'redeem-valid' };
}

/** Reveal exactly one panel, hiding the rest. */
export function showRedeemPanel(doc: Document, panel: RedeemPanelId): void {
  for (const id of REDEEM_PANELS) {
    const el = doc.getElementById(id);
    if (el) el.classList.toggle('hidden', id !== panel);
  }
}

/** Write the outcome's detail copy into its panel, then reveal that panel. */
export function applyRedeemOutcome(doc: Document, outcome: RedeemOutcome): void {
  if (outcome.panel === 'redeem-throttled') {
    const el = doc.getElementById('redeem-throttled-detail');
    if (el) el.textContent = outcome.detail;
  } else if (outcome.panel === 'redeem-not-valid') {
    const el = doc.getElementById('redeem-not-valid-detail');
    if (el) el.textContent = outcome.detail;
  }
  showRedeemPanel(doc, outcome.panel);
}
