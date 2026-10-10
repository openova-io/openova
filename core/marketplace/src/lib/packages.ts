// packages.ts — the BSS package document, feeding the existing six-step wizard.
//
// #6971. The SME storefront sells hosting-style packages S / M / L / XL. Every
// feature exists for every Organization technically; commercially each one is,
// per package, Included, Optional (a paid add-on), a Teaser (not available
// here — a larger package has it) or Not offered. That matrix is OWNED by BSS
// and published, public and CORS-enabled, at
//
//     GET https://chargeback.<sovereign-fqdn>/api/v1/public/packages
//
// The wizard already has the steps this needs, so the document FEEDS them
// rather than adding a flow of its own:
//
//   Step 1 Plan     — the comparison (PackageTable.svelte). With a v1 document
//                     (no `groups`) it is the flat table: one column per
//                     package, one row per feature. With a v2 document
//                     (`groups`, `floor`, a package `shape` and `step_up`) it
//                     is the LADDER (PackageLadder.svelte): four package
//                     cards, one grouped comparison, the floor strip once.
//                     Choosing a package sets the cart plan exactly as the
//                     legacy deck did and continues to Stack. Nothing else is
//                     picked here.
//   Step 3 Add-ons  — the chosen package's OPTIONAL features ARE the add-ons
//                     (funnelAddonsFor / addonsLadderFor: BSS `addon.*` SKUs in
//                     the AddOn shape the step already renders), its INCLUDED
//                     features show read-only, and with a v2 document the
//                     teaser / not-offered ones list under "Not on <pkg>" with
//                     the rung that has them, plus the STEP-UP hint when the
//                     ticked add-ons the next package bundles cost at least
//                     the price gap to it (stepUpHint).
//   Step 4 Topology — the package's DR level decides whether the hot-standby
//                     topology is selectable here or "Included from XL" with
//                     a switch (drTopologyFor).
//   Review/Checkout — unchanged in structure; the same add-on list resolves
//                     whatever ids the cart holds to name + price, the plan
//                     cards and the headroom estimate size from the package
//                     shape (packageCapacity), and the floor is a footnote
//                     under the Review total.
//
// There is deliberately NO feature list in this tree. When the document is
// unreachable or publishes no packages, `loadPublicPackages` resolves to null,
// logs once, and every step behaves exactly as it does today.
//
// Every customer-visible string lives in PACKAGE_STRINGS. The storefront has
// no i18n mechanism today (`<html lang="en">`, no locale switch anywhere in
// src/), so this is English, kept in one place for the day one arrives.

import type { AddOn } from './api';

export type CellState = 'included' | 'optional' | 'teaser' | 'not_offered';
export type FeatureKind = 'boolean' | 'quantity' | 'level' | 'access';
/** What happens above a quantity cell's allowance. */
export type Overage = 'hard_cap' | 'metered' | 'unlimited';

/** Quantities a package ships with, per the v1 contract's `includes` object. */
export interface PackageIncludes {
  vcpu?: number;
  memory_gb?: number;
  storage_gb?: number;
  /** The v2 document keys a quantity feature `<key>_<unit>`: disk_gb. */
  disk_gb?: number;
  bandwidth_mbps?: number;
}

/** The v2 contract's `shape`: the headline sizing and the guaranteed share. */
export interface PackageShape {
  vcpu?: number;
  memory_gb?: number;
  vcpu_guaranteed?: number;
  memory_gb_guaranteed?: number;
  disk_gb?: number;
}

/** The v2 contract's `step_up`: the next rung and what it bundles. */
export interface PackageStepUp {
  next_sku: string;
  next_name: string;
  /** Money string: the next package's price minus this one's. */
  gap_month: string;
  /** Feature keys the next package includes that are add-ons here. */
  bundled_addon_keys: string[];
  bundled_addons_sum_month: string;
  rule_holds: boolean;
}

/**
 * An icon BSS publishes on a group, floor item, feature or package (DESIGN
 * §22.4). On the wire `src` is a PATH on the chargeback host
 * (`/api/v1/public/icons/<sha256>`); parsePublicPackages resolves it against
 * the URL the document was fetched from, so `src` here is always absolute and
 * always on that host. Nothing visual about a feature or package lives in this
 * tree — no icon, colour or badge is keyed by sku or feature key here; when the
 * document carries none, the storefront renders none.
 */
export interface PackageIcon {
  /** Absolute URL on the document's own host, path under /api/v1/public/icons/. */
  src: string;
  /** The document's alt text (the entity's name when it sends none). */
  alt: string;
  /** "#RRGGBB" — a tile background behind the icon (features). */
  bg?: string;
}

export interface PublicPackage {
  sku: string;
  name: string;
  /** Money is a string at the currency's minor unit, e.g. "9.000". */
  price_month: string;
  includes: PackageIncludes;
  // v2 — absent on a v1 document.
  tagline?: string;
  recommended?: boolean;
  annual_months_free?: number;
  shape?: PackageShape;
  step_up?: PackageStepUp | null;
  // Branding — each absent when BSS publishes none.
  icon?: PackageIcon;
  /** "#RRGGBB" — the package's brand colour. */
  accent?: string;
  /** A short label, e.g. "Most popular". Independent of `recommended`. */
  badge?: string;
  /** Grow mode: absent when the package cannot grow above its allowance. */
  grow?: PackageGrow;
}

/**
 * The four resources a package's allowance is measured in, keyed the way the
 * document's `grow.ceiling` and the order's `grow_ceiling` key them.
 */
export interface GrowCeiling {
  vcpu: number;
  memory_gb: number;
  disk_gb: number;
  bandwidth_mbps: number;
}

export type GrowDimension = keyof GrowCeiling;

/**
 * `packages[].grow` — the package may grow above its allowance, up to
 * `ceiling`, the usage above the allowance billed at the package's OWN
 * `overage_rates` (bigger packages grow cheaper).
 */
export interface PackageGrow {
  allowed: true;
  ceiling: GrowCeiling;
  /** This package's per-unit rates; empty when BSS publishes none (the package then cannot grow). */
  overage_rates: OverageRate[];
}

export type OverageRateKey = 'vcpu' | 'memory' | 'disk' | 'bandwidth';
export type OverageUnit = 'vCPU' | 'GB' | 'Mbps';

/**
 * A `packages[].grow.overage_rates[]` entry: what one unit above the
 * allowance costs for a month on that package, billed after the month in grow
 * mode. Money is a string at the currency's minor unit, like every price in
 * the document.
 */
export interface OverageRate {
  key: OverageRateKey;
  sku: string;
  unit: OverageUnit;
  price_month: string;
}

export interface NextLevelAddon {
  addon_sku: string;
  price_month: string;
}

export interface PublicCell {
  state: CellState;
  addon_sku?: string;
  price_month?: string;
  /** The sku of the cheapest package that includes this feature as standard. */
  included_from?: string;
  quantity?: number;
  // v2
  overage?: Overage;
  /** Index into the feature's `levels`. */
  level?: number;
  /** The add-on that lifts an included level cell to the next level. */
  next_level_addon?: NextLevelAddon;
  /** A short qualifier on an access cell, e.g. "read". */
  note?: string;
  /**
   * An optional cell with no price that is available on this package only in
   * grow mode, billed as usage (active-passive DR on S / M / L).
   */
  grow_only?: true;
}

export interface PublicFeature {
  key: string;
  name: string;
  blurb?: string;
  kind: FeatureKind;
  unit?: string;
  cells: Record<string, PublicCell>;
  // v2
  group?: string;
  levels?: string[];
  addon_sku?: string;
  teaser?: boolean;
  icon?: PackageIcon;
}

export interface PackageGroup {
  key: string;
  name: string;
  icon?: PackageIcon;
}

/** Something every package includes — rendered once, under the comparison. */
export interface FloorItem {
  key: string;
  name: string;
  blurb?: string;
  icon?: PackageIcon;
}

export interface PublicPackages {
  currency: string;
  price_book?: string;
  prices_as_of?: string;
  /** Ordered by price by the server; rendered in that order. */
  packages: PublicPackage[];
  /** Ordered by the server's sort order; rendered in that order. */
  features: PublicFeature[];
  /** v2 only: the comparison's groups, in render order. Its presence IS the version. */
  groups?: PackageGroup[];
  /** v2 only: the floor strip. */
  floor?: FloorItem[];
}

export const PUBLIC_PACKAGES_PATH = '/api/v1/public/packages';

/** A v2 document publishes `groups`; everything the ladder needs hangs off that. */
export function isLadderDocument(doc: PublicPackages): boolean {
  return Array.isArray(doc.groups) && doc.groups.length > 0;
}

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
  // The ladder (document v2).
  ladder: {
    annualFree: (n: number) => `Annual: ${n} month${n === 1 ? '' : 's'} free`,
    shape: (vcpu: number, memoryGb: number) => `${vcpu} vCPU · ${memoryGb} GB RAM`,
    guaranteed: (vcpu: number, memoryGb: number) => `${vcpu} vCPU · ${memoryGb} GB guaranteed`,
    disk: (gb: number) => `${gb} GB disk`,
    addonTag: 'ADD-ON',
    addonPrice: (price: string) => `+ ${price} / mo`,
    from: (name: string) => `from ${name}`,
    teaserLabel: (name: string) => `Not on this package — included from ${name}`,
    hardCap: 'hard cap',
    metered: 'then metered',
    unlimited: 'unlimited',
    unlimitedLabel: 'Unlimited',
    otherGroup: 'More',
    floorLead: 'Every package includes:',
    floorTitle: 'Included in every package',
    floorSub: 'Standard on every package — no add-on, no upgrade.',
    floorMore: (n: number) => `+ everything in every package (${n})`,
    addonsNote: 'Optional add-ons are picked on the Add-ons step.',
    // Step 3 blocks.
    inYourPackage: 'In your package',
    inYourPackageHint: 'Part of your package — nothing to pick.',
    addons: 'Add-ons',
    addonsHint: 'Tick what you need; each is added to your monthly total.',
    notOn: (name: string) => `Not on ${name}`,
    notOnHint: 'Larger packages include these. Switching keeps your apps.',
    upgradeTo: (name: string) => `Upgrade to ${name} to get this`,
    switchTo: (name: string) => `Switch to ${name}`,
    includedBadge: 'INCLUDED',
    // Step 4 — the topology the package allows.
    topologyLocked: (name: string) => `Included from ${name}. Switching keeps your apps and add-ons.`,
    availableOn: (name: string, price: string) => `Available on ${name} as an add-on (+ ${price} / mo)`,
    levelUp: (feature: string, level: string) => `${feature} · ${level}`,
    levelFrom: (level: string) => `Upgrade from ${level}`,
    stepUpTitle: (next: string, gap: string, currency: string) => `${next} includes all of this for ${gap} ${currency} more`,
    stepUpBody: (sum: string, currency: string, next: string) =>
      `You are adding ${sum} ${currency} / mo of add-ons that ${next} includes as standard.`,
    stepUpCta: (next: string) => `Switch to ${next}`,
    runningTotal: 'Your monthly total',
    packageLine: (name: string) => `${name} package`,
    addonsLine: 'Add-ons',
  },
  // Grow mode — what happens when the Organization reaches its package.
  grow: {
    plansNote: 'Every package can grow with you.',
    plansNoteBody: 'Keep it capped at the package price, or let it grow when you get busy and pay per use above it. You choose on Add-ons.',
    title: 'When you reach your package',
    hint: 'Your package is a monthly allowance. Choose what happens at its edge.',
    cappedTitle: 'Capped',
    cappedTag: 'Default',
    cappedBody: (total: string) => `Never pay more than ${total} / mo. Your apps stay within the package; nothing is billed above it.`,
    growTitle: 'Grow with me',
    growTag: 'Pay per use above the package',
    growBody: 'Keep running when you get busy. Usage above your package is billed after the month, at these rates:',
    ratesOn: (name: string) => `Usage rates on ${name}, billed after the month`,
    rateLine: (price: string, currency: string, unit: string) => `+${price} ${currency} per extra ${unit} / mo`,
    rateName: { vcpu: 'vCPU', memory: 'Memory', disk: 'Disk', bandwidth: 'Bandwidth' } as Record<OverageRateKey, string>,
    unitWord: { vcpu: 'vCPU', memory: 'GB of memory', disk: 'GB of disk', bandwidth: 'Mbps' } as Record<OverageRateKey, string>,
    ceilingTitle: 'Grow up to',
    ceilingHint: (name: string) => `From your ${name} allowance up to the most ${name} can grow to.`,
    dimension: { vcpu: 'vCPU', memory_gb: 'Memory', disk_gb: 'Disk', bandwidth_mbps: 'Bandwidth' } as Record<GrowDimension, string>,
    unit: { vcpu: 'vCPU', memory_gb: 'GB', disk_gb: 'GB', bandwidth_mbps: 'Mbps' } as Record<GrowDimension, string>,
    included: (n: number, unit: string) => `${n} ${unit} included`,
    decrease: (dim: string) => `Less ${dim}`,
    increase: (dim: string) => `More ${dim}`,
    spendTitle: 'Monthly spend limit',
    spendOptional: 'optional',
    spendHint: 'Usage charges stop growing at this amount in a month. Leave empty for no limit.',
    spendInvalid: 'Enter an amount such as 25 or 25.500.',
    drUnlocked: 'Also unlocks active-passive DR on the Topology step, the standby billed as usage.',
    upgradeTitle: (extra: string, next: string) => `If you regularly use ${extra}, ${next} is cheaper`,
    upgradeBody: (pkg: string, grown: string, next: string, nextPrice: string) =>
      `${pkg} plus that usage comes to ${grown} / mo. ${next} includes it for ${nextPrice} / mo, and grows at lower rates.`,
    upgradeExtra: (parts: string[]) => parts.join(' and '),
    upgradeDelta: (delta: number, unit: string) => `${delta} extra ${unit}`,
    plansCheaper: (price: string, currency: string, name: string) => `Bigger packages grow cheaper: an extra vCPU from ${price} ${currency} / mo on ${name}.`,
    upgradeCta: (next: string) => `Switch to ${next}`,
    // Review / Checkout.
    reviewCapped: (total: string) => `Capped at ${total} / mo`,
    reviewGrow: 'Grow with me',
    reviewUpTo: (parts: string) => `up to ${parts}`,
    reviewSpend: (amount: string) => `spend limit ${amount} / mo`,
    reviewNoSpend: 'no spend limit',
    reviewBilled: 'usage above the package billed after the month',
    sidebarUsage: 'Usage above your package',
    sidebarUsageValue: 'billed after the month',
    sidebarUsagePer: 'per use',
    // /bcp.
    needsGrow: 'Needs Grow — standby billed as usage',
    needsGrowBody: (name: string) => `On ${name}, active-passive runs with Grow: the standby is billed as usage after the month.`,
    switchToGrow: 'Switch to Grow',
    orSwitchTo: (name: string) => `or switch to ${name}, where it is included`,
    billedAsUsage: 'Billed as usage',
    // /plans cells.
    cellLabel: 'with Grow',
    cellHint: 'billed as usage',
    cellStateLabel: 'Available with Grow, billed as usage',
  },
} as const;

// ---------------------------------------------------------------------------
// Validation — the contract, checked at the boundary.
// ---------------------------------------------------------------------------

const STATES: ReadonlySet<string> = new Set<CellState>(['included', 'optional', 'teaser', 'not_offered']);
const KINDS: ReadonlySet<string> = new Set<FeatureKind>(['boolean', 'quantity', 'level', 'access']);
const OVERAGES: ReadonlySet<string> = new Set<Overage>(['hard_cap', 'metered', 'unlimited']);

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === 'object' && v !== null && !Array.isArray(v);
}

function str(v: unknown): string | null {
  return typeof v === 'string' && v.trim() ? v : null;
}

function num(v: unknown): number | undefined {
  return typeof v === 'number' && Number.isFinite(v) ? v : undefined;
}

function strList(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string' && x.trim() !== '') : [];
}

// ── Branding: icons, accent, badge — validated, never trusted ─────────────

/** The only path an icon may live under, on the document's own host. */
export const PUBLIC_ICONS_PATH = '/api/v1/public/icons/';
const ICON_PATH_RE = /^\/api\/v1\/public\/icons\/[A-Za-z0-9._-]+$/;
const HEX_COLOUR_RE = /^#[0-9A-Fa-f]{6}$/;
const BADGE_MAX = 32;

/** "#RRGGBB" or null — anything else (a name, rgb(), a short hex, CSS) is ignored. */
export function hexColour(v: unknown): string | null {
  return typeof v === 'string' && HEX_COLOUR_RE.test(v) ? v : null;
}

/**
 * An icon object from the document, or null when it is not one we will load.
 * `src` must be a path under /api/v1/public/icons/ (resolved against the
 * document's URL) or an https URL on the document's own host with that path.
 * A protocol-relative URL, another host, another scheme, a data: or
 * javascript: URL, or a path that escapes the icons directory (`..`) is
 * dropped. Without the document's URL there is nothing to resolve against,
 * so every icon is dropped.
 */
export function parseIcon(v: unknown, base: URL | null, fallbackAlt: string): PackageIcon | null {
  if (!base || !isRecord(v) || typeof v.src !== 'string') return null;
  const raw = v.src.trim();
  let url: URL;
  try {
    if (raw.startsWith(PUBLIC_ICONS_PATH)) {
      url = new URL(raw, base);
    } else if (/^https:\/\//i.test(raw)) {
      url = new URL(raw);
      if (url.host !== base.host) return null;
    } else {
      return null;
    }
  } catch {
    return null;
  }
  // The resolved path, after `..` normalisation, must still be an icon.
  if (url.host !== base.host || !ICON_PATH_RE.test(url.pathname)) return null;
  if (url.username || url.password) return null;
  const alt = typeof v.alt === 'string' && v.alt.trim() ? v.alt.trim() : fallbackAlt;
  const out: PackageIcon = { src: url.href, alt };
  const bg = hexColour(v.bg);
  if (bg) out.bg = bg;
  return out;
}

function badgeText(v: unknown): string | null {
  if (typeof v !== 'string') return null;
  const t = v.trim().replace(/\s+/g, ' ');
  if (!t) return null;
  return t.length > BADGE_MAX ? `${t.slice(0, BADGE_MAX - 1).trimEnd()}…` : t;
}

function channel(c: number): number {
  const s = c / 255;
  return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
}

/** WCAG relative luminance of a "#RRGGBB" colour. */
export function relativeLuminance(hex: string): number {
  const n = parseInt(hex.slice(1), 16);
  return 0.2126 * channel((n >> 16) & 255) + 0.7152 * channel((n >> 8) & 255) + 0.0722 * channel(n & 255);
}

/** WCAG contrast ratio between two "#RRGGBB" colours (1 … 21). */
export function contrastRatio(a: string, b: string): number {
  const la = relativeLuminance(a);
  const lb = relativeLuminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/**
 * The text colour to set on a brand-colour background: white or black,
 * whichever contrasts more. One of the two always reaches 4.5:1 against any
 * sRGB colour (the worst case, a mid grey, still gives ~4.6:1 with black).
 */
export function readableOn(hex: string): '#ffffff' | '#000000' {
  return contrastRatio(hex, '#ffffff') >= contrastRatio(hex, '#000000') ? '#ffffff' : '#000000';
}

function parseIncludes(v: unknown): PackageIncludes {
  if (!isRecord(v)) return {};
  const out: PackageIncludes = {};
  const vcpu = num(v.vcpu);
  const memory = num(v.memory_gb);
  const storage = num(v.storage_gb);
  const bandwidth = num(v.bandwidth_mbps);
  const disk = num(v.disk_gb);
  if (vcpu !== undefined) out.vcpu = vcpu;
  if (disk !== undefined) out.disk_gb = disk;
  if (memory !== undefined) out.memory_gb = memory;
  if (storage !== undefined) out.storage_gb = storage;
  if (bandwidth !== undefined) out.bandwidth_mbps = bandwidth;
  return out;
}

function parseShape(v: unknown): PackageShape | undefined {
  if (!isRecord(v)) return undefined;
  const out: PackageShape = {};
  for (const k of ['vcpu', 'memory_gb', 'vcpu_guaranteed', 'memory_gb_guaranteed', 'disk_gb'] as const) {
    const n = num(v[k]);
    if (n !== undefined) out[k] = n;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function parseStepUp(v: unknown): PackageStepUp | null {
  if (!isRecord(v)) return null;
  const next_sku = str(v.next_sku);
  const gap = str(v.gap_month);
  if (!next_sku || !gap) return null;
  return {
    next_sku,
    next_name: str(v.next_name) ?? next_sku,
    gap_month: gap,
    bundled_addon_keys: strList(v.bundled_addon_keys),
    bundled_addons_sum_month: str(v.bundled_addons_sum_month) ?? '0.000',
    rule_holds: v.rule_holds === true,
  };
}

function parsePackage(v: unknown, base: URL | null = null): PublicPackage | null {
  if (!isRecord(v)) return null;
  const sku = str(v.sku);
  const name = str(v.name);
  const price = str(v.price_month);
  if (!sku || !name || !price) return null;
  const out: PublicPackage = { sku, name, price_month: price, includes: parseIncludes(v.includes) };
  if (typeof v.tagline === 'string') out.tagline = v.tagline.trim();
  if (v.recommended === true) out.recommended = true;
  const annual = num(v.annual_months_free);
  if (annual !== undefined && annual >= 0) out.annual_months_free = Math.floor(annual);
  const shape = parseShape(v.shape);
  if (shape) out.shape = shape;
  // A v2 package (one with a shape) always states its step-up: the live
  // document omits the key on the last rung rather than sending null.
  if ('step_up' in v || shape) out.step_up = parseStepUp(v.step_up);
  const icon = parseIcon(v.icon, base, name);
  const accent = hexColour(v.accent);
  const badge = badgeText(v.badge);
  if (icon) out.icon = icon;
  if (accent) out.accent = accent;
  if (badge) out.badge = badge;
  const grow = parseGrow(v.grow);
  if (grow) out.grow = grow;
  return out;
}

const GROW_DIMENSIONS: ReadonlyArray<GrowDimension> = ['vcpu', 'memory_gb', 'disk_gb', 'bandwidth_mbps'];

/** `{allowed: true, ceiling: {…four non-negative numbers}}`, or null — a partial ceiling is no ceiling. */
function parseGrow(v: unknown): PackageGrow | null {
  if (!isRecord(v) || v.allowed !== true || !isRecord(v.ceiling)) return null;
  const c = v.ceiling;
  const ceiling = {} as GrowCeiling;
  for (const k of GROW_DIMENSIONS) {
    const n = num(c[k]);
    if (n === undefined || n < 0) return null;
    ceiling[k] = n;
  }
  return { allowed: true, ceiling, overage_rates: parseOverageRates(v.overage_rates) };
}

const RATE_KEYS: ReadonlySet<string> = new Set<OverageRateKey>(['vcpu', 'memory', 'disk', 'bandwidth']);
const RATE_UNITS: ReadonlySet<string> = new Set<OverageUnit>(['vCPU', 'GB', 'Mbps']);
const MONEY_RE = /^\d+(\.\d+)?$/;

/** A package's overage rates, each checked; one entry per key, the first kept. */
export function parseOverageRates(v: unknown): OverageRate[] {
  if (!Array.isArray(v)) return [];
  const out: OverageRate[] = [];
  for (const raw of v) {
    if (!isRecord(raw)) continue;
    const key = str(raw.key);
    const sku = str(raw.sku);
    const unit = str(raw.unit);
    const price = str(raw.price_month);
    if (!key || !RATE_KEYS.has(key) || !sku || !unit || !RATE_UNITS.has(unit)) continue;
    if (!price || !MONEY_RE.test(price.trim())) continue;
    if (out.some(r => r.key === key)) continue;
    out.push({ key: key as OverageRateKey, sku, unit: unit as OverageUnit, price_month: price.trim() });
  }
  return out;
}

function parseNextLevelAddon(v: unknown): NextLevelAddon | undefined {
  if (!isRecord(v)) return undefined;
  const sku = str(v.addon_sku);
  const price = str(v.price_month);
  return sku && price ? { addon_sku: sku, price_month: price } : undefined;
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
  const overage = str(v.overage);
  const level = num(v.level);
  const nla = parseNextLevelAddon(v.next_level_addon);
  const note = str(v.note);
  if (addon) cell.addon_sku = addon;
  if (price) cell.price_month = price;
  if (from) cell.included_from = from;
  if (qty !== undefined) cell.quantity = qty;
  if (overage && OVERAGES.has(overage)) cell.overage = overage as Overage;
  if (level !== undefined && level >= 0) cell.level = Math.floor(level);
  if (nla) cell.next_level_addon = nla;
  if (note) cell.note = note;
  if (v.grow_only === true && cell.state === 'optional') cell.grow_only = true;
  return cell;
}

function parseFeature(v: unknown, base: URL | null = null): PublicFeature | null {
  if (!isRecord(v)) return null;
  const key = str(v.key);
  const name = str(v.name);
  if (!key || !name) return null;
  const kind: FeatureKind = typeof v.kind === 'string' && KINDS.has(v.kind) ? (v.kind as FeatureKind) : 'boolean';
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
  const group = str(v.group);
  const addon = str(v.addon_sku);
  const levels = strList(v.levels);
  if (blurb) out.blurb = blurb;
  if (unit) out.unit = unit;
  if (group) out.group = group;
  if (addon) out.addon_sku = addon;
  if (levels.length > 0) out.levels = levels;
  if (v.teaser === true) out.teaser = true;
  const icon = parseIcon(v.icon, base, name);
  if (icon) out.icon = icon;
  return out;
}

function parseGroup(v: unknown, base: URL | null = null): PackageGroup | null {
  if (!isRecord(v)) return null;
  const key = str(v.key);
  const name = str(v.name);
  if (!key || !name) return null;
  const out: PackageGroup = { key, name };
  const icon = parseIcon(v.icon, base, name);
  if (icon) out.icon = icon;
  return out;
}

function parseFloorItem(v: unknown, base: URL | null = null): FloorItem | null {
  if (!isRecord(v)) return null;
  const key = str(v.key);
  const name = str(v.name);
  if (!key || !name) return null;
  const out: FloorItem = { key, name };
  const blurb = str(v.blurb);
  if (blurb) out.blurb = blurb;
  const icon = parseIcon(v.icon, base, name);
  if (icon) out.icon = icon;
  return out;
}

function baseURL(documentURL: string | null | undefined): URL | null {
  if (!documentURL) return null;
  try {
    const u = new URL(documentURL);
    return u.protocol === 'https:' || u.protocol === 'http:' ? u : null;
  } catch {
    return null;
  }
}

/**
 * Validate a `GET /api/v1/public/packages` body. Returns null when the body is
 * not the contract or publishes no packages — the wizard then behaves exactly
 * as it does without BSS. A v1 body (no `groups`) and a v2 body both parse;
 * `isLadderDocument` tells them apart.
 *
 * `documentURL` is the URL the body was fetched from: icon paths resolve
 * against it, and only icons on its host are kept. Without it every icon is
 * dropped (accent and badge, which load nothing, still parse).
 */
export function parsePublicPackages(raw: unknown, documentURL?: string | null): PublicPackages | null {
  if (!isRecord(raw)) return null;
  if (!Array.isArray(raw.packages)) return null;
  const base = baseURL(documentURL);
  const packages = raw.packages.map(p => parsePackage(p, base)).filter((p): p is PublicPackage => p !== null);
  if (packages.length === 0) return null;
  const features = Array.isArray(raw.features)
    ? raw.features.map(f => parseFeature(f, base)).filter((f): f is PublicFeature => f !== null)
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
  if (Array.isArray(raw.groups)) {
    const groups = raw.groups.map(g => parseGroup(g, base)).filter((g): g is PackageGroup => g !== null);
    if (groups.length > 0) out.groups = groups;
  }
  if (Array.isArray(raw.floor)) {
    out.floor = raw.floor.map(f => parseFloorItem(f, base)).filter((f): f is FloorItem => f !== null);
  }
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
    // Icon paths resolve against where the document actually came from.
    const parsed = parsePublicPackages(body, res.url || url);
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
// Step 1 (v1 document) — the flat comparison table's render model.
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

function packageName(packages: ReadonlyArray<PublicPackage>, sku: string | undefined): string | null {
  if (!sku) return null;
  const hit = packages.find(p => p.sku === sku);
  return hit ? hit.name : null;
}

/** The text a cell renders. A missing cell is read as not offered. */
export function cellLabel(cell: PublicCell | undefined, feature: PublicFeature, currency: string, packages: ReadonlyArray<PublicPackage> = []): string {
  if (!cell || cell.state === 'not_offered') return PACKAGE_STRINGS.notOfferedGlyph;
  if (cell.state === 'teaser') {
    const from = packageName(packages, cell.included_from);
    return from ? PACKAGE_STRINGS.ladder.from(from) : PACKAGE_STRINGS.notOfferedGlyph;
  }
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

function stateLabel(state: CellState, from: string | null = null): string {
  if (state === 'included') return PACKAGE_STRINGS.includedLabel;
  if (state === 'optional') return PACKAGE_STRINGS.optionalLabel;
  if (state === 'teaser') return from ? PACKAGE_STRINGS.ladder.teaserLabel(from) : PACKAGE_STRINGS.notOfferedLabel;
  return PACKAGE_STRINGS.notOfferedLabel;
}

/**
 * "Included from XL" — only for an optional cell whose `included_from` names a
 * package in this document. Nothing is invented for a dangling sku.
 */
export function includedFromHint(cell: PublicCell | undefined, packages: ReadonlyArray<PublicPackage>): string | null {
  if (!cell || cell.state !== 'optional' || !cell.included_from) return null;
  const target = packages.find(p => p.sku === cell.included_from);
  return target ? PACKAGE_STRINGS.includedFrom(target.name) : null;
}

/**
 * Which column to highlight: `?recommended=<sku>` when it names a package in
 * this document, otherwise the middle one (the lower middle for an even count,
 * so S/M/L/XL highlights M).
 */
export function recommendedSku(packages: ReadonlyArray<PublicPackage>, query: string | null | undefined): string | null {
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
        label: cellLabel(cell, f, data.currency, data.packages),
        stateLabel: stateLabel(state, packageName(data.packages, cell?.included_from)),
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
// Step 1 (v2 document) — the ladder's render model: cards, grouped rows, floor.
// ---------------------------------------------------------------------------

export interface LadderCard {
  sku: string;
  name: string;
  /** Empty when the package has none — the card then shows no tagline line. */
  tagline: string;
  priceMonth: string;
  recommended: boolean;
  /** "Annual: 2 months free", or null when there is nothing to say. */
  annualLine: string | null;
  /** "4 vCPU · 8 GB RAM" */
  shapeHeadline: string | null;
  /** "1 vCPU · 4 GB guaranteed" — in small type under the headline. */
  shapeGuarantee: string | null;
  /** "50 GB disk" */
  diskLine: string | null;
  /** Branding from the document; null when it publishes none. */
  icon: PackageIcon | null;
  accent: string | null;
  /** Black or white — the text colour on `accent`, ≥ 4.5:1. */
  accentFg: string | null;
  badge: string | null;
}

export interface LadderCell {
  sku: string;
  state: CellState;
  kind: FeatureKind;
  /**
   * The cell's main text: "✓", "100 Mbps", "daily · 14 days", "from L", "—";
   * on an optional cell the price ("+ 2.000 / mo") beside the ADD-ON tag.
   */
  label: string;
  /** The small line under it: "hard cap" / "then metered" / "Included from XL" / an access note. */
  hint: string | null;
  stateLabel: string;
}

export interface LadderRow {
  key: string;
  name: string;
  blurb: string;
  kind: FeatureKind;
  icon: PackageIcon | null;
  cells: LadderCell[];
}

export interface LadderGroup {
  key: string;
  name: string;
  icon: PackageIcon | null;
  rows: LadderRow[];
}

export interface LadderFloorItem {
  key: string;
  name: string;
  blurb: string;
  icon: PackageIcon | null;
}

export interface LadderModel {
  currency: string;
  priceBook: string | null;
  pricesAsOf: string | null;
  cards: LadderCard[];
  /** Groups with at least one feature, in the document's order; unknown-group features last. */
  groups: LadderGroup[];
  /** The floor strip's names, in order. */
  floor: string[];
  /** The floor items with their icons, in order. */
  floorItems: LadderFloorItem[];
  /** True when any comparison row has an icon: the name column then keeps an icon gutter on every row, so names line up. */
  rowIcons: boolean;
  /** True when any floor item has an icon: the floor then renders as a chip grid. */
  floorIcons: boolean;
  recommendedSku: string | null;
  /** True when at least one package can grow (growModelFor): the "grow with you" line shows. */
  growNote: boolean;
  /** The lowest extra-vCPU rate among the packages that can grow, when the rates differ — "bigger packages grow cheaper". */
  growCheapest: { sku: string; name: string; priceMonth: string } | null;
}

function overageHint(overage: Overage | undefined): string | null {
  if (overage === 'hard_cap') return PACKAGE_STRINGS.ladder.hardCap;
  if (overage === 'metered') return PACKAGE_STRINGS.ladder.metered;
  if (overage === 'unlimited') return PACKAGE_STRINGS.ladder.unlimited;
  return null;
}

/** A level cell's label, from the feature's `levels` list; "✓" when it has none. */
export function levelLabel(feature: PublicFeature, level: number | undefined): string {
  if (level === undefined || !feature.levels) return PACKAGE_STRINGS.includedGlyph;
  return feature.levels[level] ?? PACKAGE_STRINGS.includedGlyph;
}

/** An included quantity cell's label: "100 Mbps", or "Unlimited" when that is the overage rule and there is no number. */
export function quantityLabel(feature: PublicFeature, cell: PublicCell): string {
  if (cell.quantity !== undefined) return `${cell.quantity}${feature.unit ? ` ${feature.unit}` : ''}`;
  if (cell.overage === 'unlimited') return PACKAGE_STRINGS.ladder.unlimitedLabel;
  return PACKAGE_STRINGS.includedGlyph;
}

function ladderCell(cell: PublicCell | undefined, feature: PublicFeature, packages: ReadonlyArray<PublicPackage>, sku: string): LadderCell {
  const state: CellState = cell?.state ?? 'not_offered';
  const base = { sku, state, kind: feature.kind };
  if (!cell || state === 'not_offered') {
    return { ...base, label: PACKAGE_STRINGS.notOfferedGlyph, hint: null, stateLabel: PACKAGE_STRINGS.notOfferedLabel };
  }
  if (state === 'teaser') {
    const from = packageName(packages, cell.included_from);
    return {
      ...base,
      label: from ? PACKAGE_STRINGS.ladder.from(from) : PACKAGE_STRINGS.notOfferedGlyph,
      hint: null,
      stateLabel: stateLabel('teaser', from),
    };
  }
  if (state === 'optional' && cell.grow_only) {
    return { ...base, label: PACKAGE_STRINGS.grow.cellLabel, hint: PACKAGE_STRINGS.grow.cellHint, stateLabel: PACKAGE_STRINGS.grow.cellStateLabel };
  }
  if (state === 'optional') {
    return {
      ...base,
      label: cell.price_month ? PACKAGE_STRINGS.ladder.addonPrice(cell.price_month) : PACKAGE_STRINGS.optionalNoPrice,
      hint: includedFromHint(cell, packages),
      stateLabel: PACKAGE_STRINGS.optionalLabel,
    };
  }
  // included
  if (feature.kind === 'quantity') {
    const label = quantityLabel(feature, cell);
    const hint = label === PACKAGE_STRINGS.ladder.unlimitedLabel ? null : overageHint(cell.overage);
    return { ...base, label, hint, stateLabel: PACKAGE_STRINGS.includedLabel };
  }
  if (feature.kind === 'level') {
    return { ...base, label: levelLabel(feature, cell.level), hint: null, stateLabel: PACKAGE_STRINGS.includedLabel };
  }
  if (feature.kind === 'access') {
    return { ...base, label: PACKAGE_STRINGS.includedGlyph, hint: cell.note ?? null, stateLabel: PACKAGE_STRINGS.includedLabel };
  }
  return { ...base, label: PACKAGE_STRINGS.includedGlyph, hint: null, stateLabel: PACKAGE_STRINGS.includedLabel };
}

/** The card's lines for a package: shape from `shape`, else from the v1 `includes`. */
export function ladderCard(p: PublicPackage, recommended: boolean): LadderCard {
  const sh = p.shape ?? {};
  const vcpu = sh.vcpu ?? p.includes.vcpu;
  const memory = sh.memory_gb ?? p.includes.memory_gb;
  const disk = sh.disk_gb ?? p.includes.storage_gb;
  const S = PACKAGE_STRINGS.ladder;
  return {
    sku: p.sku,
    name: p.name,
    tagline: p.tagline ?? '',
    priceMonth: p.price_month,
    recommended,
    annualLine: p.annual_months_free && p.annual_months_free > 0 ? S.annualFree(p.annual_months_free) : null,
    shapeHeadline: vcpu !== undefined && memory !== undefined ? S.shape(vcpu, memory) : null,
    shapeGuarantee: sh.vcpu_guaranteed !== undefined && sh.memory_gb_guaranteed !== undefined
      ? S.guaranteed(sh.vcpu_guaranteed, sh.memory_gb_guaranteed)
      : null,
    diskLine: disk !== undefined ? S.disk(disk) : null,
    icon: p.icon ?? null,
    accent: p.accent ?? null,
    accentFg: p.accent ? readableOn(p.accent) : null,
    badge: p.badge ?? null,
  };
}

/**
 * The ladder's highlighted column: `?recommended=<sku>` when it names a
 * package, else the package the document flags `recommended`, else the middle.
 */
export function ladderRecommendedSku(packages: ReadonlyArray<PublicPackage>, query: string | null | undefined): string | null {
  if (packages.length === 0) return null;
  const q = (query ?? '').trim();
  if (q && packages.some(p => p.sku === q)) return q;
  const flagged = packages.find(p => p.recommended);
  return flagged ? flagged.sku : recommendedSku(packages, null);
}

export function buildLadder(
  data: PublicPackages,
  opts: { recommended?: string | null } = {},
): LadderModel {
  const recommended = ladderRecommendedSku(data.packages, opts.recommended);
  const cards = data.packages.map(p => ladderCard(p, p.sku === recommended));
  const rowFor = (f: PublicFeature): LadderRow => ({
    key: f.key,
    name: f.name,
    blurb: f.blurb ?? '',
    kind: f.kind,
    icon: f.icon ?? null,
    cells: data.packages.map(p => ladderCell(f.cells[p.sku], f, data.packages, p.sku)),
  });
  const declared = data.groups ?? [];
  const known = new Set(declared.map(g => g.key));
  const byGroup = new Map<string, LadderRow[]>();
  const other: LadderRow[] = [];
  for (const f of data.features) {
    const key = f.group && known.has(f.group) ? f.group : null;
    if (!key) { other.push(rowFor(f)); continue; }
    const rows = byGroup.get(key) ?? [];
    rows.push(rowFor(f));
    byGroup.set(key, rows);
  }
  // A declared group with no feature is not a header over nothing.
  const groups: LadderGroup[] = declared
    .map(g => ({ key: g.key, name: g.name, icon: g.icon ?? null, rows: byGroup.get(g.key) ?? [] }))
    .filter(g => g.rows.length > 0);
  if (other.length > 0) groups.push({ key: 'other', name: PACKAGE_STRINGS.ladder.otherGroup, icon: null, rows: other });
  const floorItems: LadderFloorItem[] = (data.floor ?? []).map(f => ({
    key: f.key, name: f.name, blurb: f.blurb ?? '', icon: f.icon ?? null,
  }));
  return {
    currency: data.currency,
    priceBook: data.price_book ?? null,
    pricesAsOf: data.prices_as_of ?? null,
    cards,
    groups,
    floor: floorItems.map(f => f.name),
    floorItems,
    rowIcons: groups.some(g => g.rows.some(r => r.icon !== null)),
    floorIcons: floorItems.some(f => f.icon !== null),
    recommendedSku: recommended,
    growNote: data.packages.some(p => growModelFor(data, p.sku) !== null),
    growCheapest: cheapestGrowVcpu(data),
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

/** The reverse of minorUnits: 6000 → "6.000", for strings built from sums. */
export function moneyString(minor: number, decimals = 3): string {
  const n = Number.isFinite(minor) ? minor : 0;
  return (n / 10 ** decimals).toFixed(decimals);
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
 * Catalog add-ons that duplicate a BSS feature. With a document in hand the
 * catalog list is not offered at all; the twin table is how a catalog id
 * already in a cart is carried over to the BSS add-on it stands for
 * (pruneAddonsForPackage). Keyed by the catalog add-on SLUG (what
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

/**
 * The BSS feature key a catalog add-on twins in this document, or null. A v2
 * document's FLOOR counts too: a catalog add-on for something every package
 * includes (the WAF, say) must not be offered for money beside the strip that
 * says it is included.
 */
export function twinFeatureKey(
  addon: { slug: string; name: string },
  features: ReadonlyArray<PublicFeature>,
  floor: ReadonlyArray<FloorItem> = [],
): string | null {
  const explicit = CATALOG_ADDON_TWINS[addon.slug];
  if (explicit && (features.some(f => f.key === explicit) || floor.some(f => f.key === explicit))) return explicit;
  const n = normalizeName(addon.name);
  if (!n) return null;
  const byName = features.find(f => normalizeName(f.name) === n) ?? floor.find(f => normalizeName(f.name) === n);
  return byName ? byName.key : null;
}

/** A feature the package includes as standard — rendered read-only on step 3. */
export interface IncludedFeature {
  key: string;
  name: string;
  blurb: string;
  icon?: PackageIcon | null;
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
 * With a v2 document the list is the ladder's (addonsLadderFor): the optional
 * cells plus the next-level add-ons.
 *
 * With a document in hand the list is BSS add-ons ONLY. A `/catalog/addons`
 * entry carries a price that is not in the price book, so it is not offered
 * beside the document's (founder: every number in the journey is real); the
 * catalog list is the step's offer only when there is no document at all. The
 * `catalog` parameter is kept so a stale catalog id already in the cart can be
 * carried over to its BSS twin (pruneAddonsForPackage).
 */
export function funnelAddonsFor(doc: PublicPackages, sku: string, catalog: ReadonlyArray<AddOn> = []): FunnelAddons {
  void catalog;
  if (isLadderDocument(doc)) {
    const ladder = addonsLadderFor(doc, sku);
    if (ladder) {
      return {
        addons: ladder.addons,
        included: ladder.included.map(i => ({ key: i.key, name: i.name, blurb: i.value ?? i.blurb, icon: i.icon })),
        packageName: ladder.packageName,
      };
    }
  }
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
        ...(f.icon ? { image: f.icon } : {}),
      });
    } else if (cell.state === 'included' && f.kind === 'boolean') {
      included.push({ key: f.key, name: f.name, blurb: f.blurb ?? '', icon: f.icon ?? null });
    }
  }
  return { addons: bss, included, packageName: pkg?.name ?? '' };
}

/** Every add-on SKU this document can bill — the cart ids that are BSS, not catalog. */
export function bssAddonSkus(doc: PublicPackages): Set<string> {
  const out = new Set<string>();
  for (const f of doc.features) {
    if (f.addon_sku) out.add(f.addon_sku);
    for (const cell of Object.values(f.cells)) {
      if (cell.addon_sku) out.add(cell.addon_sku);
      if (cell.next_level_addon) out.add(cell.next_level_addon.addon_sku);
    }
  }
  return out;
}

/** The add-on SKUs a package offers for purchase: its optional cells and its next-level add-ons. */
export function purchasableAddonSkus(doc: PublicPackages, sku: string): Set<string> {
  const out = new Set<string>();
  for (const f of doc.features) {
    const cell = f.cells[sku];
    if (!cell) continue;
    if (cell.state === 'optional' && cell.addon_sku) out.add(cell.addon_sku);
    if (cell.state === 'included' && cell.next_level_addon) out.add(cell.next_level_addon.addon_sku);
  }
  return out;
}

/** The BSS add-on SKU a package sells for a feature key, or null. */
function optionalSkuFor(doc: PublicPackages, sku: string, featureKey: string): string | null {
  const f = doc.features.find(x => x.key === featureKey);
  const cell = f?.cells[sku];
  return cell?.state === 'optional' && cell.addon_sku ? cell.addon_sku : null;
}

/**
 * The cart's add-on ids for a package, with the document in hand: a BSS SKU
 * survives only where the package offers it (Backup is included on XL — the
 * add-on would be refused as redundant there); a catalog id is not part of
 * the journey once there is a document — it is carried over to the BSS add-on
 * it twins when the package sells that (a stale "daily-backup" becomes
 * `addon.backup`, the customer's intent kept), otherwise dropped. Pass the
 * catalog list when it is in hand so the twin can be resolved; without it
 * every non-BSS id is dropped.
 */
export function pruneAddonsForPackage(
  doc: PublicPackages,
  sku: string,
  addons: ReadonlyArray<string>,
  catalog: ReadonlyArray<AddOn> = [],
): string[] {
  const all = bssAddonSkus(doc);
  const here = purchasableAddonSkus(doc, sku);
  const out: string[] = [];
  for (const id of addons) {
    let keep: string | null = null;
    if (all.has(id)) {
      if (here.has(id)) keep = id;
    } else {
      const entry = catalog.find(a => a.id === id);
      const twin = entry ? twinFeatureKey(entry, doc.features, doc.floor ?? []) : null;
      if (twin) keep = optionalSkuFor(doc, sku, twin);
    }
    if (keep && !out.includes(keep)) out.push(keep);
  }
  return out;
}

// ---------------------------------------------------------------------------
// Step 4 — the topology the package's DR level allows.
// ---------------------------------------------------------------------------

export interface DrTopology {
  /** The package's level index into the feature's `levels`, and its label. */
  level: number;
  label: string;
  /** True when the package includes the active-passive (hot-standby) topology. */
  activePassive: boolean;
  /** The first package that includes it, when this one does not. */
  activePassiveFrom: { sku: string; name: string } | null;
  /**
   * True when this package offers active-passive only in grow mode, the
   * standby billed as usage (a `grow_only` optional cell). `activePassive` is
   * then false: whether the card is selectable depends on the cart's mode.
   */
  growOnly: boolean;
}

const ACTIVE_PASSIVE = /active[\s_-]?(passive|hot[\s_-]?standby)/i;

/**
 * What the Topology step may offer for a package: the document's DR level
 * feature (`dr_topology`, or any level feature with an "active-passive"
 * level) decides whether the hot-standby topology is included here or only
 * from a larger package. Null when the document has no such feature — the
 * step then offers both topologies as it does without a document.
 */
export function drTopologyFor(doc: PublicPackages, sku: string): DrTopology | null {
  const f = doc.features.find(x => x.key === 'dr_topology' && x.kind === 'level')
    ?? doc.features.find(x => x.kind === 'level' && (x.levels ?? []).some(l => ACTIVE_PASSIVE.test(l)));
  if (!f || !f.levels || f.levels.length === 0) return null;
  if (!doc.packages.some(p => p.sku === sku)) return null;
  const cell = f.cells[sku];
  let apLevel = f.levels.findIndex(l => ACTIVE_PASSIVE.test(l));
  if (apLevel < 0) apLevel = f.levels.length - 1;
  const firstIncluding = (): DrTopology['activePassiveFrom'] => {
    for (const p of doc.packages) {
      const c = f.cells[p.sku];
      if (c?.state === 'included' && (c.level ?? 0) >= apLevel) return { sku: p.sku, name: p.name };
    }
    return null;
  };
  if (cell?.state === 'optional' && cell.grow_only) {
    // Active-passive here only with Grow, the standby billed as usage; the
    // rung that includes it is still named for the "or switch" path.
    return { level: -1, label: PACKAGE_STRINGS.notOfferedGlyph, activePassive: false, activePassiveFrom: firstIncluding(), growOnly: true };
  }
  if (!cell || cell.state !== 'included') {
    // The package has no DR topology at all (the live document since the
    // founder's 2026-10-10 call: DR only on XL — S/M/L read not_offered).
    // The hot-standby card is then locked, pointing at the first rung that
    // includes the active-passive level; it is never offered "as without a
    // document".
    return { level: -1, label: PACKAGE_STRINGS.notOfferedGlyph, activePassive: false, activePassiveFrom: firstIncluding(), growOnly: false };
  }
  const level = cell.level ?? 0;
  const activePassive = level >= apLevel;
  let from: DrTopology['activePassiveFrom'] = null;
  if (!activePassive) {
    // The cell's own `included_from` when it names a package that has the
    // level (the live document states it), else the first rung that does.
    const stated = cell.included_from ? doc.packages.find(p => p.sku === cell.included_from) : undefined;
    const statedCell = stated ? f.cells[stated.sku] : undefined;
    if (stated && statedCell?.state === 'included' && (statedCell.level ?? 0) >= apLevel) {
      from = { sku: stated.sku, name: stated.name };
    } else {
      from = firstIncluding();
    }
  }
  return { level, label: levelLabel(f, level), activePassive, activePassiveFrom: from, growOnly: false };
}

// ---------------------------------------------------------------------------
// Review — the capacity a package stands for, from the document's shape.
// ---------------------------------------------------------------------------

export interface PackageCapacity {
  /** MiB */
  ram: number;
  /** millicores */
  cpu: number;
  /** GiB */
  disk: number;
}

/**
 * The sizing the Review step's headroom estimate measures against: the v2
 * `shape` (headline vCPU / RAM / disk), else the v1 `includes`. Null when the
 * package publishes neither — the caller then falls back to its catalog shape.
 */
export function packageCapacity(pkg: PublicPackage): PackageCapacity | null {
  const sh = pkg.shape ?? {};
  const vcpu = sh.vcpu ?? pkg.includes.vcpu;
  const memory = sh.memory_gb ?? pkg.includes.memory_gb;
  const disk = sh.disk_gb ?? pkg.includes.storage_gb;
  if (vcpu === undefined || memory === undefined || disk === undefined) return null;
  return { ram: Math.round(memory * 1024), cpu: Math.round(vcpu * 1000), disk };
}

/** The specs line a Review plan card shows for a package: "4 vCPU · 8 GB · 100 GB". */
export function packageSpecsLine(pkg: PublicPackage): string | null {
  const sh = pkg.shape ?? {};
  const vcpu = sh.vcpu ?? pkg.includes.vcpu;
  const memory = sh.memory_gb ?? pkg.includes.memory_gb;
  const disk = sh.disk_gb ?? pkg.includes.storage_gb;
  if (vcpu === undefined || memory === undefined || disk === undefined) return null;
  return `${vcpu} vCPU · ${memory} GB · ${disk} GB`;
}

// ---------------------------------------------------------------------------
// Step 3 (v2 document) — the three blocks and the step-up hint.
// ---------------------------------------------------------------------------

/** Block A — something the package has: a boolean, a quantity with its rule, a level, an access. */
export interface LadderIncluded {
  key: string;
  name: string;
  blurb: string;
  /** "100 Mbps · hard cap", "single region", "read" — null for a plain ✓. */
  value: string | null;
  icon: PackageIcon | null;
}

/** Block B — an add-on the package offers: an optional cell, or the next level of a level cell. */
export interface LadderChoice {
  /** The BSS add-on SKU — what the cart holds and billing prices. */
  id: string;
  featureKey: string;
  name: string;
  blurb: string;
  priceMonth: string;
  priceBaisa: number;
  /** The name of the first package that includes this (the muted "Included from XL"). */
  includedFrom: string | null;
  /** For a next-level add-on: the level index it buys; null for a plain optional. */
  targetLevel: number | null;
  icon: PackageIcon | null;
}

/** Block C — something this package does not have, and the rung that does. */
export interface LadderMissing {
  key: string;
  name: string;
  blurb: string;
  icon: PackageIcon | null;
  state: 'teaser' | 'not_offered';
  upgrade: {
    sku: string;
    name: string;
    /** included: "Upgrade to L to get this"; optional: "Available on L as an add-on". */
    state: 'included' | 'optional';
    priceMonth: string | null;
  } | null;
}

export interface AddonsLadder {
  packageSku: string;
  packageName: string;
  packagePriceMonth: string;
  packagePriceBaisa: number;
  included: LadderIncluded[];
  choices: LadderChoice[];
  missing: LadderMissing[];
  /** The step's offer list: the choices in the AddOn shape — BSS add-ons only. */
  addons: AddOn[];
}

function includedValue(f: PublicFeature, cell: PublicCell): string | null {
  if (f.kind === 'quantity') {
    const label = quantityLabel(f, cell);
    const hint = label === PACKAGE_STRINGS.ladder.unlimitedLabel ? null : overageHint(cell.overage);
    return hint ? `${label} · ${hint}` : label;
  }
  if (f.kind === 'level') return levelLabel(f, cell.level);
  if (f.kind === 'access') return cell.note ?? null;
  return null;
}

/**
 * The rung a missing feature points at: the cell's own `included_from` when
 * it names a package (the live document states it on not-offered cells too),
 * else the first package, in ladder order, that has the feature at all —
 * included first choice, else optional.
 */
function firstRungWith(doc: PublicPackages, f: PublicFeature, cell?: PublicCell): LadderMissing['upgrade'] {
  const stated = cell?.included_from ? doc.packages.find(p => p.sku === cell.included_from) : undefined;
  if (stated) return { sku: stated.sku, name: stated.name, state: 'included', priceMonth: null };
  for (const p of doc.packages) {
    const c = f.cells[p.sku];
    if (!c) continue;
    if (c.state === 'included') return { sku: p.sku, name: p.name, state: 'included', priceMonth: null };
    if (c.state === 'optional') return { sku: p.sku, name: p.name, state: 'optional', priceMonth: c.price_month ?? null };
  }
  return null;
}

/** The first package that includes a level feature at `level` or above. */
function firstRungAtLevel(doc: PublicPackages, f: PublicFeature, level: number): string | null {
  for (const p of doc.packages) {
    const c = f.cells[p.sku];
    if (c?.state === 'included' && (c.level ?? -1) >= level) return p.name;
  }
  return null;
}

function choiceAsAddon(c: LadderChoice): AddOn {
  return {
    id: c.id,
    slug: c.featureKey,
    name: c.name,
    tagline: c.blurb,
    icon: '',
    monthly_price: c.priceBaisa,
    included: false,
    ...(c.includedFrom ? { hint: PACKAGE_STRINGS.includedFrom(c.includedFrom) } : {}),
    ...(c.icon ? { image: c.icon } : {}),
  };
}

/**
 * The Add-ons step's three blocks for a package of a v2 document, plus the
 * offer list in the AddOn shape. Null when the sku is not in the document.
 */
export function addonsLadderFor(doc: PublicPackages, sku: string): AddonsLadder | null {
  const pkg = doc.packages.find(p => p.sku === sku);
  if (!pkg) return null;
  const S = PACKAGE_STRINGS.ladder;
  const included: LadderIncluded[] = [];
  const choices: LadderChoice[] = [];
  const missing: LadderMissing[] = [];
  for (const f of doc.features) {
    const cell = f.cells[sku];
    const blurb = f.blurb ?? '';
    const icon = f.icon ?? null;
    if (!cell || cell.state === 'not_offered') {
      missing.push({ key: f.key, name: f.name, blurb, icon, state: 'not_offered', upgrade: firstRungWith(doc, f, cell) });
      continue;
    }
    if (cell.state === 'teaser') {
      missing.push({ key: f.key, name: f.name, blurb, icon, state: 'teaser', upgrade: firstRungWith(doc, f, cell) });
      continue;
    }
    if (cell.state === 'optional') {
      if (!cell.addon_sku) continue;
      choices.push({
        id: cell.addon_sku,
        featureKey: f.key,
        name: f.name,
        blurb,
        priceMonth: cell.price_month ?? '',
        priceBaisa: minorUnits(cell.price_month),
        includedFrom: packageName(doc.packages, cell.included_from),
        targetLevel: null,
        icon,
      });
      continue;
    }
    // included
    included.push({ key: f.key, name: f.name, blurb, value: includedValue(f, cell), icon });
    if (cell.next_level_addon && f.kind === 'level') {
      const current = cell.level ?? 0;
      const target = current + 1;
      const targetName = f.levels?.[target] ?? '';
      choices.push({
        id: cell.next_level_addon.addon_sku,
        featureKey: f.key,
        name: targetName ? S.levelUp(f.name, targetName) : f.name,
        blurb: S.levelFrom(levelLabel(f, current)),
        priceMonth: cell.next_level_addon.price_month,
        priceBaisa: minorUnits(cell.next_level_addon.price_month),
        includedFrom: firstRungAtLevel(doc, f, target),
        targetLevel: target,
        icon,
      });
    }
  }
  return {
    packageSku: pkg.sku,
    packageName: pkg.name,
    packagePriceMonth: pkg.price_month,
    packagePriceBaisa: minorUnits(pkg.price_month),
    included,
    choices,
    missing,
    addons: choices.map(choiceAsAddon),
  };
}

/** True when `next` includes what this choice buys (a plain feature, or the level it reaches). */
export function bundledOn(doc: PublicPackages, nextSku: string, choice: LadderChoice): boolean {
  const f = doc.features.find(x => x.key === choice.featureKey);
  const cell = f?.cells[nextSku];
  if (!cell || cell.state !== 'included') return false;
  if (choice.targetLevel === null) return true;
  return (cell.level ?? -1) >= choice.targetLevel;
}

export interface StepUpHint {
  nextSku: string;
  nextName: string;
  gapMonth: string;
  gapBaisa: number;
  /** The ticked add-ons the next package includes — what switching clears. */
  bundled: LadderChoice[];
  bundledSumBaisa: number;
  bundledSumMonth: string;
}

/**
 * The step-up hint: when the ticked add-ons that the NEXT package includes as
 * standard add up to at least the price gap to it, the customer is better off
 * one rung up. The sum counts only what the next rung bundles — a ticked add-on
 * it does not include (Dedicated IP is optional on every package) neither
 * triggers the hint nor is cleared by the switch — so the card's claim,
 * "<next> includes all of this", is always true of what it lists.
 */
export function stepUpHint(doc: PublicPackages, ladder: AddonsLadder, ticked: ReadonlyArray<string>): StepUpHint | null {
  const pkg = doc.packages.find(p => p.sku === ladder.packageSku);
  const su = pkg?.step_up;
  if (!su) return null;
  const next = doc.packages.find(p => p.sku === su.next_sku);
  if (!next) return null;
  const bundled = ladder.choices.filter(c => ticked.includes(c.id) && bundledOn(doc, next.sku, c));
  if (bundled.length === 0) return null;
  const sum = bundled.reduce((s, c) => s + c.priceBaisa, 0);
  const gap = minorUnits(su.gap_month);
  if (sum < gap) return null;
  return {
    nextSku: next.sku,
    nextName: next.name,
    gapMonth: su.gap_month,
    gapBaisa: gap,
    bundled,
    bundledSumBaisa: sum,
    bundledSumMonth: moneyString(sum),
  };
}

// ---------------------------------------------------------------------------
// Grow mode — capped (the package is a hard limit) or grow (usage above the
// allowance billed after the month at the document's overage rates).
// ---------------------------------------------------------------------------

export type OverageMode = 'capped' | 'grow';

/** Which overage rate prices growth in a dimension. */
export const GROW_RATE_KEY: Readonly<Record<GrowDimension, OverageRateKey>> = {
  vcpu: 'vcpu',
  memory_gb: 'memory',
  disk_gb: 'disk',
  bandwidth_mbps: 'bandwidth',
};

/** The stepper's increment per dimension. */
export const GROW_STEP: Readonly<Record<GrowDimension, number>> = {
  vcpu: 1,
  memory_gb: 1,
  disk_gb: 10,
  bandwidth_mbps: 10,
};

/**
 * A package's allowance in the four grow dimensions: the shape's headline
 * (vCPU, memory, disk), else the v1 `includes`; bandwidth from `includes`,
 * else the bandwidth quantity feature's cell. A dimension the document does
 * not state is absent.
 */
export function packageAllowance(doc: PublicPackages, pkg: PublicPackage): Partial<GrowCeiling> {
  const sh = pkg.shape ?? {};
  const out: Partial<GrowCeiling> = {};
  const vcpu = sh.vcpu ?? pkg.includes.vcpu;
  const memory = sh.memory_gb ?? pkg.includes.memory_gb;
  const disk = sh.disk_gb ?? pkg.includes.disk_gb ?? pkg.includes.storage_gb;
  let bandwidth = pkg.includes.bandwidth_mbps;
  if (bandwidth === undefined) {
    const f = doc.features.find(x => x.kind === 'quantity' && (x.key === 'bandwidth' || (x.unit ?? '').toLowerCase() === 'mbps'));
    const c = f?.cells[pkg.sku];
    if (c?.state === 'included' && c.quantity !== undefined) bandwidth = c.quantity;
  }
  if (vcpu !== undefined) out.vcpu = vcpu;
  if (memory !== undefined) out.memory_gb = memory;
  if (disk !== undefined) out.disk_gb = disk;
  if (bandwidth !== undefined) out.bandwidth_mbps = bandwidth;
  return out;
}

/** One stepper on the grow card: a dimension the package can grow in and the rate that prices it. */
export interface GrowDim {
  key: GrowDimension;
  label: string;
  unit: string;
  /** The package's allowance — the stepper's floor. */
  allowance: number;
  /** The package's grow ceiling — the stepper's top. */
  max: number;
  step: number;
  rate: OverageRate;
  rateBaisa: number;
}

export interface GrowModel {
  packageSku: string;
  packageName: string;
  currency: string;
  /** The package's own rates, in the document's order — what the grow card lists. */
  rates: OverageRate[];
  /** The dimensions with an allowance, a rate and room above the allowance. */
  dims: GrowDim[];
  allowance: Partial<GrowCeiling>;
  /** The package's grow ceiling, as published. */
  ceiling: GrowCeiling;
  /** True when the package's DR (active-passive) is available only with Grow. */
  unlocksDr: boolean;
}

/**
 * The grow card's model for a package, or null when the package cannot grow:
 * the package carries no `grow`, its `grow.overage_rates` are empty, or its
 * ceiling leaves no room above the allowance in any priced dimension (XL in a
 * book whose XL ceiling is its headline). Null → the step shows no mode block
 * and the order is capped, as before.
 */
export function growModelFor(doc: PublicPackages, sku: string): GrowModel | null {
  const pkg = doc.packages.find(p => p.sku === sku);
  const rates = pkg?.grow?.overage_rates ?? [];
  if (!pkg || !pkg.grow || rates.length === 0) return null;
  const allowance = packageAllowance(doc, pkg);
  const dims: GrowDim[] = [];
  for (const key of GROW_DIMENSIONS) {
    const base = allowance[key];
    const rate = rates.find(r => r.key === GROW_RATE_KEY[key]);
    const max = pkg.grow.ceiling[key];
    if (base === undefined || !rate || max <= base) continue;
    dims.push({
      key,
      label: PACKAGE_STRINGS.grow.dimension[key],
      unit: PACKAGE_STRINGS.grow.unit[key],
      allowance: base,
      max,
      step: GROW_STEP[key],
      rate,
      rateBaisa: minorUnits(rate.price_month),
    });
  }
  if (dims.length === 0) return null;
  return {
    packageSku: pkg.sku,
    packageName: pkg.name,
    currency: doc.currency,
    rates,
    dims,
    allowance,
    ceiling: { ...pkg.grow.ceiling },
    unlocksDr: drTopologyFor(doc, pkg.sku)?.growOnly === true,
  };
}

/**
 * The package with the lowest extra-vCPU rate among those that can actually
 * grow, or null when fewer than two such rates differ (nothing to say).
 */
export function cheapestGrowVcpu(doc: PublicPackages): LadderModel['growCheapest'] {
  const offers = doc.packages.flatMap(p => {
    const m = growModelFor(doc, p.sku);
    const d = m?.dims.find(x => x.key === 'vcpu');
    return d ? [{ sku: p.sku, name: p.name, priceMonth: d.rate.price_month, baisa: d.rateBaisa }] : [];
  });
  if (new Set(offers.map(o => o.baisa)).size < 2) return null;
  const best = offers.reduce((a, b) => (b.baisa < a.baisa ? b : a));
  return { sku: best.sku, name: best.name, priceMonth: best.priceMonth };
}

/** One stepper press: the next value on the step grid from the allowance, clamped to [allowance, max]. */
export function stepGrow(dim: Pick<GrowDim, 'allowance' | 'max' | 'step'>, value: number, direction: 1 | -1): number {
  const v = Number.isFinite(value) ? value : dim.max;
  const pos = (v - dim.allowance) / dim.step;
  const k = direction > 0 ? Math.floor(pos + 1e-9) + 1 : Math.ceil(pos - 1e-9) - 1;
  const next = Math.round((dim.allowance + k * dim.step) * 1000) / 1000;
  return Math.min(dim.max, Math.max(dim.allowance, next));
}

/**
 * The ceiling an order may carry for this package: every stepper dimension
 * within [allowance, package ceiling]; a dimension with no stepper holds the
 * package's own ceiling.
 */
export function clampGrowCeiling(model: GrowModel, wanted: Partial<GrowCeiling> | null | undefined): GrowCeiling {
  const out = { ...model.ceiling };
  if (!wanted) return out;
  for (const d of model.dims) {
    const w = wanted[d.key];
    if (typeof w === 'number' && Number.isFinite(w)) out[d.key] = Math.min(d.max, Math.max(d.allowance, w));
  }
  return out;
}

/** True when a ceiling is exactly the package's own — the order then need not carry one. */
export function isPackageCeiling(model: GrowModel, c: GrowCeiling): boolean {
  return GROW_DIMENSIONS.every(k => c[k] === model.ceiling[k]);
}

const SPEND_RE = /^\d{1,7}(\.\d{1,3})?$/;

/**
 * A monthly spend limit as typed: "25" / "25.5" / "25.500" → "25.000" (the
 * currency's minor unit), "" → null (no limit), anything else, zero included,
 * → undefined (invalid: the field says so and the cart keeps its last valid
 * limit).
 */
export function normalizeSpendLimit(raw: string): string | null | undefined {
  const t = raw.trim();
  if (t === '') return null;
  if (!SPEND_RE.test(t)) return undefined;
  const minor = minorUnits(t);
  if (minor <= 0) return undefined;
  return moneyString(minor);
}

export interface GrowUpgradeHint {
  nextSku: string;
  nextName: string;
  /** What growing to the next package's headline costs above this one, per month, at this package's rates. */
  overageBaisa: number;
  /** This package's price + that overage. */
  grownBaisa: number;
  nextPriceBaisa: number;
  /** The headline deltas priced, e.g. [{key: vcpu, delta: 2, unit: vCPU}, {key: memory_gb, delta: 4, unit: GB}]. */
  deltas: { key: GrowDimension; delta: number; unit: string }[];
}

/** The dimensions a package's HEADLINE names ("4 vCPU · 8 GB RAM") — what the upgrade hint compares. */
const HEADLINE_DIMENSIONS: ReadonlyArray<GrowDimension> = ['vcpu', 'memory_gb'];

/**
 * "If you regularly use 2 extra vCPU and 4 GB, L is cheaper": this package's
 * price plus the overage, at THIS package's rates, for the next package's
 * headline (vCPU and memory) above this one, against the next package's
 * price. Null unless growing really costs more — the hint never claims a
 * saving the document's own numbers do not show — or when there is no next
 * package or this one cannot grow.
 */
export function growUpgradeHint(doc: PublicPackages, sku: string): GrowUpgradeHint | null {
  const i = doc.packages.findIndex(p => p.sku === sku);
  if (i < 0) return null;
  const pkg = doc.packages[i];
  const stated = pkg.step_up ? doc.packages.find(p => p.sku === pkg.step_up!.next_sku) : undefined;
  const next = stated ?? doc.packages[i + 1];
  if (!next) return null;
  if (!growModelFor(doc, pkg.sku)) return null;
  const rates = pkg.grow?.overage_rates ?? [];
  const here = packageAllowance(doc, pkg);
  const there = packageAllowance(doc, next);
  let overage = 0;
  const deltas: GrowUpgradeHint['deltas'] = [];
  for (const key of HEADLINE_DIMENSIONS) {
    const a = here[key];
    const b = there[key];
    const rate = rates.find(r => r.key === GROW_RATE_KEY[key]);
    if (a === undefined || b === undefined || !rate || b <= a) continue;
    const delta = b - a;
    overage += Math.round(delta * minorUnits(rate.price_month));
    deltas.push({ key, delta, unit: PACKAGE_STRINGS.grow.unit[key] });
  }
  if (deltas.length === 0) return null;
  const priceNext = minorUnits(next.price_month);
  const grown = minorUnits(pkg.price_month) + overage;
  if (grown <= priceNext) return null;
  return { nextSku: next.sku, nextName: next.name, overageBaisa: overage, grownBaisa: grown, nextPriceBaisa: priceNext, deltas };
}

/** "8 vCPU · 16 GB memory · 250 GB disk · 1000 Mbps" — a ceiling for Review and Checkout. */
export function growCeilingLine(c: Partial<GrowCeiling>): string {
  const parts: string[] = [];
  if (c.vcpu !== undefined) parts.push(`${c.vcpu} vCPU`);
  if (c.memory_gb !== undefined) parts.push(`${c.memory_gb} GB memory`);
  if (c.disk_gb !== undefined) parts.push(`${c.disk_gb} GB disk`);
  if (c.bandwidth_mbps !== undefined) parts.push(`${c.bandwidth_mbps} Mbps`);
  return parts.join(' · ');
}

export interface GrowSelection {
  mode: OverageMode;
  /** The ceiling the order carries; null = the package's own (the field is omitted). */
  ceiling: GrowCeiling | null;
  /** "25.000", or null for no limit. */
  spendLimit: string | null;
}

/**
 * The cart's grow choice made consistent with the package it stands on: a
 * package that cannot grow is capped; a ceiling is clamped into the package's
 * range and dropped when it is the package's own; a spend limit that is not
 * a valid amount is dropped. The order body carries exactly this.
 */
export function growSelectionFor(
  doc: PublicPackages | null,
  sku: string | null,
  cart: { overageMode?: string | null; growCeiling?: Partial<GrowCeiling> | null; spendLimitMonth?: string | null },
): GrowSelection {
  const model = doc && sku ? growModelFor(doc, sku) : null;
  if (!model || cart.overageMode !== 'grow') return { mode: 'capped', ceiling: null, spendLimit: null };
  const c = clampGrowCeiling(model, cart.growCeiling ?? null);
  const spend = typeof cart.spendLimitMonth === 'string' ? normalizeSpendLimit(cart.spendLimitMonth) ?? null : null;
  return { mode: 'grow', ceiling: isPackageCeiling(model, c) ? null : c, spendLimit: spend };
}
