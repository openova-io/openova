// packages.ts — the BSS package document, feeding the existing six-step wizard.
//
// #6971. The SME storefront sells hosting-style packages S / M / L / XL. Every
// feature exists for every Organization technically; commercially each one is,
// per package, Included, Optional (a paid add-on) or Not offered. That matrix
// is OWNED by BSS and published, public and CORS-enabled, at
//
//     GET https://chargeback.<sovereign-fqdn>/api/v1/public/packages
//
// The wizard already has the steps this needs, so the document FEEDS them
// rather than adding a flow of its own:
//
//   Step 1 Plan     — the comparison table (PackageTable.svelte): one column per
//                     package, one row per feature, ✓ / "add-on + price" / —.
//                     Choosing a package sets the cart plan exactly as the
//                     legacy deck did (catalog plan id mapped from the sku) and
//                     continues to Stack. Nothing else is picked here.
//   Step 3 Add-ons  — the chosen package's OPTIONAL features ARE the add-ons
//                     (funnelAddonsFor: BSS `addon.*` SKUs in the AddOn shape the
//                     step already renders), its INCLUDED boolean features show
//                     read-only, not-offered ones never appear, and a catalog
//                     add-on that twins a BSS feature yields to it.
//   Review/Checkout — unchanged in structure; the same merged add-on list
//                     resolves whatever ids the cart holds to name + price.
//
// There is deliberately NO feature list in this tree. When the document is
// unreachable or publishes no packages, `loadPublicPackages` resolves to null,
// logs once, and every step behaves exactly as it does today.
//
// Every customer-visible string lives in PACKAGE_STRINGS. The storefront has
// no i18n mechanism today (`<html lang="en">`, no locale switch anywhere in
// src/), so this is English, kept in one place for the day one arrives.

import type { AddOn } from './api';

export type CellState = 'included' | 'optional' | 'not_offered';
export type FeatureKind = 'boolean' | 'quantity';

/** Quantities a package ships with, per the contract's `includes` object. */
export interface PackageIncludes {
  vcpu?: number;
  memory_gb?: number;
  storage_gb?: number;
  bandwidth_mbps?: number;
}

export interface PublicPackage {
  sku: string;
  name: string;
  /** Money is a string at the currency's minor unit, e.g. "9.000". */
  price_month: string;
  includes: PackageIncludes;
}

export interface PublicCell {
  state: CellState;
  addon_sku?: string;
  price_month?: string;
  /** The sku of the cheapest package that includes this feature as standard. */
  included_from?: string;
  quantity?: number;
}

export interface PublicFeature {
  key: string;
  name: string;
  blurb?: string;
  kind: FeatureKind;
  unit?: string;
  cells: Record<string, PublicCell>;
}

export interface PublicPackages {
  currency: string;
  price_book?: string;
  prices_as_of?: string;
  /** Ordered by price by the server; rendered in that order. */
  packages: PublicPackage[];
  /** Ordered by the server's sort order; rendered in that order. */
  features: PublicFeature[];
}

export const PUBLIC_PACKAGES_PATH = '/api/v1/public/packages';

// ---------------------------------------------------------------------------
// Strings — the one place.
// ---------------------------------------------------------------------------

export const PACKAGE_STRINGS = {
  title: 'Pick a package',
  subtitle: 'Every package runs the full platform. Larger packages include more as standard.',
  featureColumn: 'Feature',
  perMonth: '/ mo',
  recommended: 'Recommended',
  continueWith: (name: string) => `Continue with ${name} →`,
  choose: (name: string) => `Choose ${name}`,
  includedGlyph: '✓',
  includedLabel: 'Included',
  notOfferedGlyph: '—',
  notOfferedLabel: 'Not offered',
  /** The muted word on an optional cell; the price sits beside it. */
  addonTag: 'add-on',
  optional: (price: string, currency: string) => `+ ${price} ${currency}`,
  optionalNoPrice: 'priced on Add-ons',
  optionalLabel: 'Optional add-on',
  includedFrom: (name: string) => `Included from ${name}`,
  includes: {
    vcpu: (n: number) => `${n} vCPU`,
    memory_gb: (n: number) => `${n} GB RAM`,
    storage_gb: (n: number) => `${n} GB storage`,
    bandwidth_mbps: (n: number) => `${n} Mbps`,
  },
  pricesAsOf: (date: string) => `Prices as of ${date}`,
  addonsNote: 'Optional features are picked on the Add-ons step and added to your monthly total.',
  continueCta: 'Continue to Stack →',
  loadFailed: 'Could not load packages',
  // Step 3 — the read-only group above the optional extras.
  includedInPackage: 'Included in your package',
  includedHint: 'Part of your package at no extra cost — nothing to pick.',
} as const;

// ---------------------------------------------------------------------------
// Validation — the contract, checked at the boundary.
// ---------------------------------------------------------------------------

const STATES: ReadonlySet<string> = new Set<CellState>(['included', 'optional', 'not_offered']);

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function str(v: unknown): string | null {
  return typeof v === 'string' && v.trim() ? v : null;
}

function num(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined;
}

function parseIncludes(v: unknown): PackageIncludes {
  if (!isRecord(v)) return {};
  const out: PackageIncludes = {};
  const vcpu = num(v.vcpu);
  const memory = num(v.memory_gb);
  const storage = num(v.storage_gb);
  const bandwidth = num(v.bandwidth_mbps);
  if (vcpu !== undefined) out.vcpu = vcpu;
  if (memory !== undefined) out.memory_gb = memory;
  if (storage !== undefined) out.storage_gb = storage;
  if (bandwidth !== undefined) out.bandwidth_mbps = bandwidth;
  return out;
}

function parsePackage(v: unknown): PublicPackage | null {
  if (!isRecord(v)) return null;
  const sku = str(v.sku);
  const name = str(v.name);
  const price = str(v.price_month);
  if (!sku || !name || !price) return null;
  return { sku, name, price_month: price, includes: parseIncludes(v.includes) };
}

function parseCell(v: unknown): PublicCell | null {
  if (!isRecord(v)) return null;
  const state = str(v.state);
  if (!state || !STATES.has(state)) return null;
  const cell: PublicCell = { state: state as CellState };
  const addon = str(v.addon_sku);
  const price = str(v.price_month);
  const from = str(v.included_from);
  const qty = num(v.quantity);
  if (addon) cell.addon_sku = addon;
  if (price) cell.price_month = price;
  if (from) cell.included_from = from;
  if (qty !== undefined) cell.quantity = qty;
  return cell;
}

function parseFeature(v: unknown): PublicFeature | null {
  if (!isRecord(v)) return null;
  const key = str(v.key);
  const name = str(v.name);
  if (!key || !name) return null;
  const kind: FeatureKind = v.kind === 'quantity' ? 'quantity' : 'boolean';
  const cells: Record<string, PublicCell> = {};
  if (isRecord(v.cells)) {
    for (const [sku, raw] of Object.entries(v.cells)) {
      const cell = parseCell(raw);
      if (cell) cells[sku] = cell;
    }
  }
  const out: PublicFeature = { key, name, kind, cells };
  const blurb = str(v.blurb);
  const unit = str(v.unit);
  if (blurb) out.blurb = blurb;
  if (unit) out.unit = unit;
  return out;
}

/**
 * Validate a `GET /api/v1/public/packages` body. Returns null when the body is
 * not the contract or publishes no packages — the wizard then behaves exactly
 * as it does without BSS.
 */
export function parsePublicPackages(raw: unknown): PublicPackages | null {
  if (!isRecord(raw)) return null;
  if (!Array.isArray(raw.packages)) return null;
  const packages = raw.packages.map(parsePackage).filter((p): p is PublicPackage => p !== null);
  if (packages.length === 0) return null;
  const features = Array.isArray(raw.features)
    ? raw.features.map(parseFeature).filter((f): f is PublicFeature => f !== null)
    : [];
  const out: PublicPackages = {
    currency: str(raw.currency) ?? 'OMR',
    packages,
    features,
  };
  const book = str(raw.price_book);
  const asOf = str(raw.prices_as_of);
  if (book) out.price_book = book;
  if (asOf) out.prices_as_of = asOf;
  return out;
}

// ---------------------------------------------------------------------------
// Loading — one fetch, one warning, never a blank step.
// ---------------------------------------------------------------------------

let warnedThisPage = false;

/** Test seam: forget that the one-per-page warning has fired. */
export function resetPackagesLogOnce(): void {
  warnedThisPage = false;
}

function warnOnce(warn: (msg: string) => void, reason: string): void {
  if (warnedThisPage) return;
  warnedThisPage = true;
  warn(`[packages] ${PACKAGE_STRINGS.loadFailed} — ${reason}; this step uses the catalog alone, as before.`);
}

export interface LoadPackagesDeps {
  fetchImpl?: typeof fetch;
  warn?: (msg: string) => void;
  timeoutMs?: number;
}

/**
 * Fetch and validate the public packages document from the chargeback host.
 *
 * Resolves to null — and logs ONCE per page — when there is no chargeback
 * base URL, the request fails or times out, the status is not 2xx, or the body
 * is not the contract / has no packages. Never throws.
 */
export async function loadPublicPackages(
  baseURL: string | null,
  deps: LoadPackagesDeps = {},
): Promise<PublicPackages | null> {
  const warn = deps.warn ?? ((msg: string) => console.warn(msg));
  if (!baseURL) {
    warnOnce(warn, 'no chargeback host for this storefront');
    return null;
  }
  const fetchImpl = deps.fetchImpl ?? (typeof fetch === 'function' ? fetch : null);
  if (!fetchImpl) {
    warnOnce(warn, 'fetch unavailable');
    return null;
  }
  const url = `${baseURL}${PUBLIC_PACKAGES_PATH}`;
  const controller = typeof AbortController === 'function' ? new AbortController() : null;
  const timer = controller ? setTimeout(() => controller.abort(), deps.timeoutMs ?? 6000) : null;
  try {
    const res = await fetchImpl(url, {
      method: 'GET',
      mode: 'cors',
      credentials: 'omit',
      headers: { Accept: 'application/json' },
      signal: controller?.signal,
    });
    if (!res.ok) {
      warnOnce(warn, `${url} answered ${res.status}`);
      return null;
    }
    const body: unknown = await res.json();
    const parsed = parsePublicPackages(body);
    if (!parsed) {
      warnOnce(warn, `${url} published no packages`);
      return null;
    }
    return parsed;
  } catch (e) {
    warnOnce(warn, `${url} unreachable (${e instanceof Error ? e.message : String(e)})`);
    return null;
  } finally {
    if (timer) clearTimeout(timer);
  }
}

// ---------------------------------------------------------------------------
// Step 1 — the comparison table's render model.
// ---------------------------------------------------------------------------

export interface TableColumn {
  sku: string;
  name: string;
  priceMonth: string;
  includes: PackageIncludes;
}

export interface TableCell {
  sku: string;
  state: CellState;
  /** What the cell shows: "✓", "+ 1.500 OMR" (beside the muted add-on tag), "—", or "50 Mbps". */
  label: string;
  /** Screen-reader name for the state. */
  stateLabel: string;
  /** The muted up-sell line under an optional cell, or null. */
  hint: string | null;
  addonSku: string | null;
  priceMonth: string | null;
  quantity: number | null;
}

export interface TableRow {
  key: string;
  name: string;
  blurb: string;
  kind: FeatureKind;
  unit: string;
  cells: TableCell[];
}

export interface PackageTableModel {
  currency: string;
  priceBook: string | null;
  pricesAsOf: string | null;
  columns: TableColumn[];
  rows: TableRow[];
  recommendedSku: string | null;
}

/** The header lines under a package price, in a fixed, readable order. */
export function includesLines(inc: PackageIncludes): string[] {
  const out: string[] = [];
  if (inc.vcpu !== undefined) out.push(PACKAGE_STRINGS.includes.vcpu(inc.vcpu));
  if (inc.memory_gb !== undefined) out.push(PACKAGE_STRINGS.includes.memory_gb(inc.memory_gb));
  if (inc.storage_gb !== undefined) out.push(PACKAGE_STRINGS.includes.storage_gb(inc.storage_gb));
  if (inc.bandwidth_mbps !== undefined) out.push(PACKAGE_STRINGS.includes.bandwidth_mbps(inc.bandwidth_mbps));
  return out;
}

/** The text a cell renders. A missing cell is read as not offered. */
export function cellLabel(cell: PublicCell | undefined, feature: PublicFeature, currency: string): string {
  if (!cell || cell.state === 'not_offered') return PACKAGE_STRINGS.notOfferedGlyph;
  if (cell.state === 'included') {
    if (feature.kind === 'quantity' && cell.quantity !== undefined) {
      return `${cell.quantity}${feature.unit ? ` ${feature.unit}` : ''}`;
    }
    return PACKAGE_STRINGS.includedGlyph;
  }
  return cell.price_month
    ? PACKAGE_STRINGS.optional(cell.price_month, currency)
    : PACKAGE_STRINGS.optionalNoPrice;
}

function stateLabel(state: CellState): string {
  if (state === 'included') return PACKAGE_STRINGS.includedLabel;
  if (state === 'optional') return PACKAGE_STRINGS.optionalLabel;
  return PACKAGE_STRINGS.notOfferedLabel;
}

/**
 * "Included from XL" — only for an optional cell whose `included_from` names a
 * package in this document. Nothing is invented for a dangling sku.
 */
export function includedFromHint(cell: PublicCell | undefined, packages: PublicPackage[]): string | null {
  if (!cell || cell.state !== 'optional' || !cell.included_from) return null;
  const target = packages.find(p => p.sku === cell.included_from);
  return target ? PACKAGE_STRINGS.includedFrom(target.name) : null;
}

/**
 * Which column to highlight: `?recommended=<sku>` when it names a package in
 * this document, otherwise the middle one (the lower middle for an even count,
 * so S/M/L/XL highlights M).
 */
export function recommendedSku(packages: PublicPackage[], query: string | null | undefined): string | null {
  if (packages.length === 0) return null;
  const q = (query ?? '').trim();
  if (q && packages.some(p => p.sku === q)) return q;
  return packages[Math.floor((packages.length - 1) / 2)].sku;
}

export function buildPackageTable(
  data: PublicPackages,
  opts: { recommended?: string | null } = {},
): PackageTableModel {
  const columns: TableColumn[] = data.packages.map(p => ({
    sku: p.sku,
    name: p.name,
    priceMonth: p.price_month,
    includes: p.includes,
  }));
  const rows: TableRow[] = data.features.map(f => ({
    key: f.key,
    name: f.name,
    blurb: f.blurb ?? '',
    kind: f.kind,
    unit: f.unit ?? '',
    cells: data.packages.map(p => {
      const cell = f.cells[p.sku];
      const state: CellState = cell?.state ?? 'not_offered';
      return {
        sku: p.sku,
        state,
        label: cellLabel(cell, f, data.currency),
        stateLabel: stateLabel(state),
        hint: includedFromHint(cell, data.packages),
        addonSku: cell?.state === 'optional' && cell.addon_sku ? cell.addon_sku : null,
        priceMonth: cell?.price_month ?? null,
        quantity: cell?.quantity ?? null,
      };
    }),
  }));
  return {
    currency: data.currency,
    priceBook: data.price_book ?? null,
    pricesAsOf: data.prices_as_of ?? null,
    columns,
    rows,
    recommendedSku: recommendedSku(data.packages, opts.recommended),
  };
}

// ---------------------------------------------------------------------------
// The package ↔ catalog plan bridge.
// ---------------------------------------------------------------------------

/**
 * Money string at the currency's minor unit → integer minor units ("1.500" →
 * 1500), so package prices flow through the storefront's baisa-based
 * `formatOMR`. Non-numeric input is 0; never NaN.
 */
export function minorUnits(price: string | null | undefined, decimals = 3): number {
  if (typeof price !== 'string') return 0;
  const n = Number(price.trim());
  if (!Number.isFinite(n)) return 0;
  return Math.round(n * 10 ** decimals);
}

/** The part of a sku after its last dot: "plan.m" → "m". */
export function skuTail(sku: string): string {
  const i = sku.lastIndexOf('.');
  return (i >= 0 ? sku.slice(i + 1) : sku).toLowerCase();
}

/**
 * The catalog plan id the funnel's billing POSTs require for a package —
 * exactly what the legacy deck put in `cart.plan`.
 *
 * `/billing/checkout` resolves `plan_id` against `/catalog/plans` by id
 * (core/services/billing/handlers/handlers.go computeOrderTotal) and 400s on a
 * miss, so the cart's `plan` must stay a catalog id; the package sku travels
 * beside it as `package_sku`. Match by catalog slug ("m") or name ("M"); when
 * the catalog is unavailable, the sku tail is the same identifier the legacy
 * deck's own fallback uses.
 */
export function catalogPlanIdForPackage(
  pkg: PublicPackage,
  plans: ReadonlyArray<{ id: string; slug?: string; name?: string }>,
): string {
  const tail = skuTail(pkg.sku);
  const name = pkg.name.toLowerCase();
  const hit = plans.find(p => (p.slug ?? '').toLowerCase() === tail)
    ?? plans.find(p => (p.name ?? '').toLowerCase() === name)
    ?? plans.find(p => p.id.toLowerCase() === tail);
  return hit ? hit.id : tail;
}

/** The reverse bridge: the package a catalog plan stands for, or null. */
export function packageForPlan(
  doc: PublicPackages,
  plan: { id: string; slug?: string; name?: string },
): PublicPackage | null {
  const slug = (plan.slug ?? '').toLowerCase();
  const name = (plan.name ?? '').toLowerCase();
  const id = plan.id.toLowerCase();
  return (slug ? doc.packages.find(p => skuTail(p.sku) === slug) : undefined)
    ?? (name ? doc.packages.find(p => p.name.toLowerCase() === name) : undefined)
    ?? doc.packages.find(p => skuTail(p.sku) === id)
    ?? null;
}

/**
 * The package the cart stands on: the stamped `packageSku` when it is in this
 * document, else the package the cart's catalog plan maps to, else null (the
 * step then behaves as it does without BSS).
 */
export function packageForCart(
  doc: PublicPackages,
  cart: { packageSku: string | null; plan: string | null; planName?: string },
): PublicPackage | null {
  if (cart.packageSku) {
    const stamped = doc.packages.find(p => p.sku === cart.packageSku);
    if (stamped) return stamped;
  }
  if (cart.plan) return packageForPlan(doc, { id: cart.plan, name: cart.planName });
  return null;
}

// ---------------------------------------------------------------------------
// Step 3 — the chosen package's features become the add-ons list.
// ---------------------------------------------------------------------------

/**
 * Catalog add-ons that duplicate a BSS feature. When the document is present,
 * only the BSS one is shown. Keyed by the catalog add-on SLUG (what
 * /catalog/addons calls it), valued by the BSS feature KEY. Kept small and
 * explicit; an exact (normalised) NAME match covers any other twin.
 */
export const CATALOG_ADDON_TWINS: Readonly<Record<string, string>> = {
  'daily-backup': 'backup',
  'custom-domain': 'domain',
  'dedicated-ip': 'dedicated-ip',
  'waf': 'waf',
};

function normalizeName(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim();
}

/** The BSS feature key a catalog add-on twins in this document, or null. */
export function twinFeatureKey(
  addon: { slug: string; name: string },
  features: ReadonlyArray<PublicFeature>,
): string | null {
  const explicit = CATALOG_ADDON_TWINS[addon.slug];
  if (explicit && features.some(f => f.key === explicit)) return explicit;
  const n = normalizeName(addon.name);
  if (!n) return null;
  const byName = features.find(f => normalizeName(f.name) === n);
  return byName ? byName.key : null;
}

/** A feature the package includes as standard — rendered read-only on step 3. */
export interface IncludedFeature {
  key: string;
  name: string;
  blurb: string;
}

export interface FunnelAddons {
  /** What the Add-ons step offers: BSS optional features first, then catalog add-ons with no BSS twin. */
  addons: AddOn[];
  /** The package's included boolean features (quantities belong to the table). */
  included: IncludedFeature[];
  packageName: string;
}

/**
 * The Add-ons step's list for a package. BSS optional features arrive in the
 * AddOn shape the step, Review and Checkout already render — `id` is the BSS
 * add-on SKU, so `cart.addons` stays one list and the POSTs are unchanged.
 */
export function funnelAddonsFor(doc: PublicPackages, sku: string, catalog: ReadonlyArray<AddOn>): FunnelAddons {
  const pkg = doc.packages.find(p => p.sku === sku);
  const bss: AddOn[] = [];
  const included: IncludedFeature[] = [];
  for (const f of doc.features) {
    const cell = f.cells[sku];
    if (!cell) continue;
    if (cell.state === 'optional' && cell.addon_sku) {
      const hint = includedFromHint(cell, doc.packages);
      bss.push({
        id: cell.addon_sku,
        slug: f.key,
        name: f.name,
        tagline: f.blurb ?? '',
        icon: '',
        monthly_price: minorUnits(cell.price_month),
        included: false,
        ...(hint ? { hint } : {}),
      });
    } else if (cell.state === 'included' && f.kind === 'boolean') {
      included.push({ key: f.key, name: f.name, blurb: f.blurb ?? '' });
    }
  }
  const rest = catalog.filter(a => twinFeatureKey(a, doc.features) === null);
  return { addons: [...bss, ...rest], included, packageName: pkg?.name ?? '' };
}

/** Every add-on SKU this document can bill — the cart ids that are BSS, not catalog. */
export function bssAddonSkus(doc: PublicPackages): Set<string> {
  const out = new Set<string>();
  for (const f of doc.features) {
    for (const cell of Object.values(f.cells)) {
      if (cell.addon_sku) out.add(cell.addon_sku);
    }
  }
  return out;
}

/**
 * The cart's add-on ids after a package change: catalog ids are untouched,
 * BSS SKUs survive only where the new package still offers them as optional
 * (Backup is included on XL — the add-on would be refused as redundant there).
 */
export function pruneAddonsForPackage(doc: PublicPackages, sku: string, addons: ReadonlyArray<string>): string[] {
  const all = bssAddonSkus(doc);
  const optionalHere = new Set<string>();
  for (const f of doc.features) {
    const cell = f.cells[sku];
    if (cell?.state === 'optional' && cell.addon_sku) optionalHere.add(cell.addon_sku);
  }
  return addons.filter(id => !all.has(id) || optionalHere.has(id));
}
