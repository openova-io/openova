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
import type { OverageFields, QuoteLine, QuoteRequest, QuoteResponse } from './api';
import {
  drTopologyFor,
  growCeilingLine,
  growModelFor,
  growSelectionFor,
  minorUnits,
  PACKAGE_STRINGS,
  type PublicPackages,
} from './packages';

export type Topology = 'single-region' | 'active-hot-standby';

export const QUOTE_STRINGS = {
  /** Shown in place of a total when the quote has not answered or failed. */
  pending: '—',
  unavailable: 'Total unavailable — the price book could not be read. Retry in a moment.',
  included: 'Included',
  pricing: 'Pricing your order…',
  /** Under a total priced from the published document because the quote did not answer. */
  fromDocument: 'From the published price book — confirmed by billing at checkout.',
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
 * cart's one add-on list (catalog ids, or the chosen package's optional
 * features as BSS `addon.*` SKUs — the Add-ons step writes both into the
 * same list), the package sku and the topology. Keeping them identical is
 * what makes the quoted total the billed total.
 */
export function quoteRequestFor(cart: CartState, doc: PublicPackages | null = null): QuoteRequest {
  return {
    plan_id: cart.plan || '',
    apps: cart.apps || [],
    addons: [...(cart.addons || [])],
    package_sku: cart.packageSku || undefined,
    topology: topologyFor(cart),
    ...overageFieldsFor(cart, doc),
  };
}

/**
 * The grow choice as the quote and checkout bodies carry it: `overage_mode`
 * always ("capped" by default), `grow_ceiling` and `spend_limit_month` only
 * in grow mode and only when set. With the document in hand the choice is
 * first made consistent with the package (growSelectionFor: a package that
 * cannot grow is capped, a ceiling is clamped into its range); without it
 * the cart's values go as the Add-ons step wrote them.
 */
export function overageFieldsFor(cart: CartState, doc: PublicPackages | null = null): OverageFields {
  if (doc) {
    const sel = growSelectionFor(doc, cart.packageSku, cart);
    if (sel.mode !== 'grow') return { overage_mode: 'capped' };
    return {
      overage_mode: 'grow',
      ...(sel.ceiling ? { grow_ceiling: { ...sel.ceiling } } : {}),
      ...(sel.spendLimit ? { spend_limit_month: sel.spendLimit } : {}),
    };
  }
  if (cart.overageMode !== 'grow') return { overage_mode: 'capped' };
  return {
    overage_mode: 'grow',
    ...(cart.growCeiling ? { grow_ceiling: { ...cart.growCeiling } } : {}),
    ...(cart.spendLimitMonth ? { spend_limit_month: cart.spendLimitMonth } : {}),
  };
}

/**
 * The Review / Checkout line for the mode, from the quote's echo when it has
 * one, else from the cart: "Capped at OMR 6.490 / mo", or "Grow with me" with
 * the ceiling, the spend limit and how the usage is billed.
 */
export function overageSummary(
  quote: QuoteResponse | null,
  cart: CartState,
  doc: PublicPackages | null,
  format: (baisa: number) => string,
): { mode: 'capped' | 'grow'; title: string; detail: string[] } {
  const S = PACKAGE_STRINGS.grow;
  const fields = overageFieldsFor(cart, doc);
  const mode = quote?.overage_mode ?? fields.overage_mode ?? 'capped';
  if (mode !== 'grow') {
    return { mode: 'capped', title: quote ? S.reviewCapped(format(quote.amount_baisa)) : S.cappedTitle, detail: [] };
  }
  const ceiling = quote?.grow_ceiling ?? fields.grow_ceiling
    ?? (doc && cart.packageSku ? growModelFor(doc, cart.packageSku)?.ceiling : undefined);
  const spend = quote?.spend_limit_month ?? fields.spend_limit_month;
  const currency = quote?.currency ?? doc?.currency ?? 'OMR';
  const detail: string[] = [];
  if (ceiling) detail.push(S.reviewUpTo(growCeilingLine(ceiling)));
  detail.push(spend ? S.reviewSpend(`${currency} ${spend}`) : S.reviewNoSpend);
  detail.push(S.reviewBilled);
  return { mode: 'grow', title: S.reviewGrow, detail };
}

/** A cart with neither a plan nor a package has nothing to price. */
export function quoteable(req: QuoteRequest): boolean {
  return Boolean(req.plan_id || req.package_sku);
}

/** What a line's amount cell reads: "Included" for a redundant line, else "+OMR 1.500". */
export function lineAmountLabel(line: QuoteLine, format: (baisa: number) => string): string {
  return line.redundant ? QUOTE_STRINGS.included : `+${format(line.amount_baisa)}`;
}

/**
 * The same answer the quote gives, read straight from the BSS public packages
 * document the storefront already holds — for the moment `POST /billing/quote`
 * cannot answer (503 "prices unavailable": billing could not read the price
 * book that /plans, in the same browser, just priced from).
 *
 * It is the quote's rules, not a second price list: the package's
 * `price_month`; each cart add-on from the package's own cell (optional → its
 * price, the next level of an included level feature → that add-on's price,
 * an add-on the package includes → 0 and redundant); the topology free when
 * the package includes the active-passive level. Null — the page then says
 * the total is unavailable — whenever the document cannot price the cart the
 * way billing would: the cart has no package of this document, an add-on the
 * package does not sell, or hot-standby on a package without it.
 */
export function documentQuote(doc: PublicPackages | null, cart: CartState): QuoteResponse | null {
  if (!doc || !cart.packageSku) return null;
  const pkg = doc.packages.find(p => p.sku === cart.packageSku);
  if (!pkg) return null;
  const lines: QuoteLine[] = [];
  for (const sku of cart.addons || []) {
    let line: QuoteLine | null = null;
    for (const f of doc.features) {
      const cell = f.cells[pkg.sku];
      if (!cell) continue;
      if (cell.state === 'optional' && cell.addon_sku === sku) {
        line = { sku, name: f.name, amount_baisa: minorUnits(cell.price_month) };
      } else if (cell.state === 'included' && cell.next_level_addon?.addon_sku === sku) {
        const target = f.levels?.[(cell.level ?? 0) + 1];
        line = {
          sku,
          name: target ? PACKAGE_STRINGS.ladder.levelUp(f.name, target) : f.name,
          amount_baisa: minorUnits(cell.next_level_addon.price_month),
        };
      } else if (cell.state === 'included' && f.addon_sku === sku) {
        line = { sku, name: f.name, amount_baisa: 0, redundant: true };
      }
      if (line) break;
    }
    if (!line) return null;
    lines.push(line);
  }
  const topology = topologyFor(cart);
  const overage = overageFieldsFor(cart, doc);
  if (topology === 'active-hot-standby') {
    // Included on the package, or — on a grow_only cell — in grow mode, the
    // standby billed as usage after the month (nothing on the monthly total).
    const dr = drTopologyFor(doc, pkg.sku);
    if (!dr?.activePassive && !(dr?.growOnly && overage.overage_mode === 'grow')) return null;
  }
  const plan = minorUnits(pkg.price_month);
  const total = plan + lines.reduce((s, l) => s + l.amount_baisa, 0);
  return {
    ...overage,
    ...(overage.overage_mode === 'grow' && pkg.grow ? { overage_rates: pkg.grow.overage_rates.map(r => ({ ...r })) } : {}),
    currency: doc.currency,
    price_source: `document:${doc.price_book ?? ''}@${doc.prices_as_of ?? ''}`,
    package_sku: pkg.sku,
    plan_id: cart.plan || undefined,
    plan_amount_baisa: plan,
    topology,
    topology_amount_baisa: 0,
    lines,
    amount_baisa: total,
    amount_omr: Math.ceil(total / 1000),
  };
}
