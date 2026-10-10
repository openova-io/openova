// quote.test.ts — #6971: the /review and /checkout totals come from the server.
//
// Three groups:
//   1. the quote body is the checkout body's pricing fields — same plan_id,
//      same merged addons, same package_sku, same topology;
//   2. getQuote POSTs to /api/billing/quote and hands back the server's
//      figures untouched — including a total that is NOT the sum of its
//      lines, which proves nothing is re-added in the browser;
//   3. a markup contract (the redeemMarkupContract.test.ts shape): the two
//      components on disk really quote through getQuote, bind their total to
//      the server's amount_baisa, render lines from quote.lines under the e2e
//      testids, and no longer sum money client-side.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { lineAmountLabel, quoteRequestFor, quoteable, topologyFor, QUOTE_STRINGS } from './quote';
import { getQuote, type QuoteResponse } from './api';
import { readCart, setPackage, setPlan, toggleAddon, toggleApp, writeCart } from './cart';

const ROOT = join(__dirname, '..', '..');
const REVIEW = readFileSync(join(ROOT, 'src', 'components', 'ReviewStep.svelte'), 'utf8');
const CHECKOUT = readFileSync(join(ROOT, 'src', 'components', 'CheckoutStep.svelte'), 'utf8');

const BACKUP = { sku: 'addon.backup', feature: 'backup', name: 'Backup', price_month: '1.500', currency: 'OMR' };

const SERVER_QUOTE: QuoteResponse = {
  currency: 'OMR',
  price_source: 'bss:OpenOva plans@2026-09-11',
  package_sku: 'plan.m',
  plan_id: 'm',
  plan_amount_baisa: 9000,
  topology: 'single-region',
  topology_amount_baisa: 0,
  lines: [{ sku: 'addon.backup', name: 'Backup', amount_baisa: 1500 }],
  // Deliberately NOT 9000 + 1500: a client that re-adds the lines would show
  // 10500 and fail the assertion below. The server's figure is the figure.
  amount_baisa: 10750,
  amount_omr: 11,
};

/** A Response-like for `request()` without the network. */
function response(status: number, body: unknown) {
  const text = JSON.stringify(body);
  return {
    ok: status >= 200 && status < 300,
    status,
    headers: new Headers(),
    json: async () => body,
    text: async () => text,
  };
}

// ---------------------------------------------------------------------------
// 1. The quote body is the checkout body.
// ---------------------------------------------------------------------------

describe('quoteRequestFor mirrors the pricing fields of the checkout POST', () => {
  beforeEach(() => localStorage.clear());

  it('carries plan_id, the merged addons, package_sku and the topology', () => {
    setPackage({ planId: 'm', planName: 'M', packageSku: 'plan.m', addons: [BACKUP] });
    toggleAddon('waf');
    toggleApp('1');
    const cart = readCart();
    cart.appConfigs = { postgres: { active_hot_standby: true, primary_region: 'me-east-215-a', replica_region: 'me-east-215-b' } };
    writeCart(cart);

    const req = quoteRequestFor(readCart());
    expect(req).toEqual({
      plan_id: 'm',
      apps: ['1'],
      addons: ['waf', 'addon.backup'],
      package_sku: 'plan.m',
      topology: 'active-hot-standby',
    });
    expect(quoteable(req)).toBe(true);
  });

  it('the legacy deck quotes without a package_sku and single-region by default', () => {
    setPlan('plan-pro', 'Pro');
    const req = quoteRequestFor(readCart());
    expect(req.plan_id).toBe('plan-pro');
    expect(req.package_sku).toBeUndefined();
    expect(req.topology).toBe('single-region');
    expect(topologyFor(readCart())).toBe('single-region');
  });

  it('a cart with neither plan nor package has nothing to price', () => {
    expect(quoteable(quoteRequestFor(readCart()))).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// 2. getQuote — the server's figures, untouched.
// ---------------------------------------------------------------------------

describe('getQuote', () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => vi.unstubAllGlobals());

  it('POSTs the body to /api/billing/quote and returns the server figures as-is', async () => {
    const fetchMock = vi.fn(async () => response(200, SERVER_QUOTE));
    vi.stubGlobal('fetch', fetchMock);

    const body = { plan_id: 'm', addons: ['addon.backup'], package_sku: 'plan.m', topology: 'single-region' };
    const q = await getQuote(body);

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/billing/quote');
    expect(init.method).toBe('POST');
    expect(JSON.parse(String(init.body))).toEqual(body);
    expect(q).toEqual(SERVER_QUOTE);
    // The total is the server's — not re-derived from the lines.
    expect(q.amount_baisa).toBe(10750);
    expect(q.amount_baisa).not.toBe(q.plan_amount_baisa + q.lines[0].amount_baisa);
  });

  it('a 422 (add-on not offered on the package) rejects with the server message', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response(422, {
      error: 'add-on "addon.dedicated-ip" (Dedicated IP address) is not offered on package "plan.s" (S)',
      addon_sku: 'addon.dedicated-ip', package_sku: 'plan.s',
    })));
    await expect(getQuote({ plan_id: 's', addons: ['addon.dedicated-ip'], package_sku: 'plan.s' }))
      .rejects.toThrow(/422:.*addon\.dedicated-ip.*plan\.s/);
  });

  it('a 503 (price book unreadable) rejects, never resolving a number', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => response(503, { error: 'prices unavailable — the price book could not be read; please retry' })));
    await expect(getQuote({ plan_id: 'm', addons: [], package_sku: 'plan.m' })).rejects.toThrow(/503:.*prices unavailable/);
  });
});

// ---------------------------------------------------------------------------
// 3. Markup contract — the components really use the quote.
// ---------------------------------------------------------------------------

describe('ReviewStep and CheckoutStep render their totals from the quote (#6971)', () => {
  const components: Array<[string, string]> = [['ReviewStep', REVIEW], ['CheckoutStep', CHECKOUT]];

  it('both quote through getQuote with the shared request and bind the total to amount_baisa', () => {
    for (const [name, src] of components) {
      expect(src, `${name} imports getQuote`).toMatch(/\bgetQuote\b/);
      expect(src, `${name} builds the body with quoteRequestFor(cart)`).toMatch(/quoteRequestFor\(cart\)/);
      expect(src, `${name} binds the total to the server's amount_baisa`)
        .toMatch(/const totalCost = \$derived\(quote\?\.amount_baisa \?\? 0\);/);
    }
  });

  it('neither sums money client-side any more', () => {
    for (const [name, src] of components) {
      expect(src, `${name}: plan + add-on sum`).not.toMatch(/planCost \+ addonCost/);
      expect(src, `${name}: cart-snapshot package prices`).not.toMatch(/packageAddonsBaisa/);
      expect(src, `${name}: minorUnits over the cart snapshot`).not.toMatch(/minorUnits\(a\.price_month\)/);
      expect(src, `${name}: hardcoded topology surcharge`).not.toMatch(/HOT_STANDBY_MONTHLY_BAISA|hotStandby \? 5000/);
      expect(src, `${name}: catalog monthly_price in a total`).not.toMatch(/reduce\(\(sum, a\) => sum \+ a\.monthly_price/);
    }
  });

  it('lines render from quote.lines under the e2e testids', () => {
    expect(REVIEW).toMatch(/\{#each quote\.lines as line \(line\.sku\)\}/);
    expect(REVIEW).toMatch(/data-testid="review-total-package-addon-\{line\.sku\}"/);
    expect(CHECKOUT).toMatch(/\{#each quote\.lines as line \(line\.sku\)\}/);
    expect(CHECKOUT).toMatch(/data-testid="checkout-package-addon-\{line\.sku\}"/);
  });

  it('the checkout cannot be submitted before the quote answers', () => {
    expect(CHECKOUT).toMatch(/disabled=\{checkoutLoading \|\| !cart\.plan \|\| cart\.apps\.length === 0 \|\| !quote\}/);
  });

  it('the checkout POST and the quote share the topology derivation', () => {
    expect(CHECKOUT).toMatch(/topology: topologyFor\(cart\),/);
  });

  it('lineAmountLabel: a redundant line reads Included, a priced one carries the amount', () => {
    const fmt = (b: number) => `OMR ${(b / 1000).toFixed(3)}`;
    expect(lineAmountLabel({ sku: 'addon.backup', name: 'Backup', amount_baisa: 1500 }, fmt)).toBe('+OMR 1.500');
    expect(lineAmountLabel({ sku: 'addon.backup', name: 'Backup', amount_baisa: 0, redundant: true }, fmt)).toBe(QUOTE_STRINGS.included);
  });
});
