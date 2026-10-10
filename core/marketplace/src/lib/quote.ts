// quote.ts — the server-priced cart (#6971).
//
// Money has ONE source. The S/M/L/XL table on /plans renders from the BSS
// public packages document; `POST /billing/checkout` prices the order from
// that same document (core/services/billing/handlers/handlers.go priceOrder).
// Before this module the /review and /checkout totals were summed in the
// browser from the catalog plan price, the catalog add-on prices, the cart's
// snapshot of the package add-on prices and a hardcoded topology surcharge —
// four lists, none of them the one the order was billed from.
//
// Now both pages ask `POST /billing/quote` (public, creates nothing) with the
// SAME body the checkout POST will carry, and render every figure from the
// answer: the plan amount, one line per add-on (BSS SKU or catalog id, with
// "Included" for a redundant one), the topology surcharge and the total.
// This module shapes that request from the cart and holds the strings; the
// components do no arithmetic on money.

import type { CartState } from './cart';
import { orderAddonIds } from './cart';
import type { QuoteLine, QuoteRequest } from './api';

export type Topology = 'single-region' | 'active-hot-standby';

export const QUOTE_STRINGS = {
  /** Shown in place of a total when the quote has not answered or failed. */
  pending: '—',
  unavailable: 'Total unavailable — the price book could not be read. Retry in a moment.',
  included: 'Included',
  pricing: 'Pricing your order…',
} as const;

/**
 * The canonical topology for the cart: the /bcp step stores the choice at
 * cart.appConfigs.postgres.active_hot_standby (BCPStep.svelte); billing
 * prices `active-hot-standby` and treats anything else as single-region.
 */
export function topologyFor(cart: CartState): Topology {
  const pg = (cart.appConfigs ?? {})['postgres'] ?? {};
  return pg['active_hot_standby'] ? 'active-hot-standby' : 'single-region';
}

/**
 * The quote body — byte-for-byte the pricing fields of the checkout POST
 * (CheckoutStep.svelte handleCheckout): the catalog plan id, the apps, the
 * merged add-on list (catalog ids + BSS SKUs, orderAddonIds), the package
 * sku and the topology. Keeping them identical is what makes the quoted
 * total the billed total.
 */
export function quoteRequestFor(cart: CartState): QuoteRequest {
  return {
    plan_id: cart.plan || '',
    apps: cart.apps || [],
    addons: orderAddonIds(cart),
    package_sku: cart.packageSku || undefined,
    topology: topologyFor(cart),
  };
}

/** A cart with neither a plan nor a package has nothing to price. */
export function quoteable(req: QuoteRequest): boolean {
  return Boolean(req.plan_id || req.package_sku);
}

/** What a line's amount cell reads: "Included" for a redundant line, else "+OMR 1.500". */
export function lineAmountLabel(line: QuoteLine, format: (baisa: number) => string): string {
  return line.redundant ? QUOTE_STRINGS.included : `+${format(line.amount_baisa)}`;
}
