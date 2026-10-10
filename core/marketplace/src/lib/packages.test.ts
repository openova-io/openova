// packages.test.ts — #6971: the BSS package document feeding the wizard.
//
// Fed from fixtures/public-packages.json, a body in the exact shape BSS
// publishes at GET /api/v1/public/packages. Four groups of assertions:
//
//   1. step 1 — the render model shows the THREE states and the "Included from
//      XL" hint, and the recommended column follows ?recommended=;
//   2. the chargeback URL and the fallback: a failed / non-2xx / empty fetch
//      resolves to null and logs exactly ONCE per page;
//   3. step 1 → cart: choosing a package writes the catalog plan id (what the
//      legacy deck wrote) plus the package sku;
//   4. step 3 — the chosen package's optional features ARE the add-ons (M lists
//      Backup at 1.500 with the hint; XL has Backup in the included group), the
//      catalog twins yield to the BSS feature, and a package change prunes a
//      BSS add-on the new package no longer offers.
//
// Plus a markup contract in the redeemMarkupContract.test.ts shape: /plans
// mounts PackageTable, PackageTable falls back to PlanStep and holds no
// checkbox, AddonsStep consumes the document — asserted against the files on
// disk rather than a reconstruction.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import {
  buildPackageTable,
  CATALOG_ADDON_TWINS,
  catalogPlanIdForPackage,
  funnelAddonsFor,
  includesLines,
  loadPublicPackages,
  minorUnits,
  PACKAGE_STRINGS,
  packageForCart,
  packageForPlan,
  parsePublicPackages,
  pruneAddonsForPackage,
  PUBLIC_PACKAGES_PATH,
  recommendedSku,
  resetPackagesLogOnce,
  twinFeatureKey,
  type PublicPackages,
} from './packages';
import { composeChargebackURL, resolveChargebackURL } from './config';
import { readCart, setPackage, setPlan } from './cart';
import type { AddOn } from './api';

const ROOT = join(__dirname, '..', '..');
const FIXTURE_PATH = join(ROOT, 'fixtures', 'public-packages.json');

function fixture(): unknown {
  return JSON.parse(readFileSync(FIXTURE_PATH, 'utf8'));
}

function parsed(): PublicPackages {
  const p = parsePublicPackages(fixture());
  if (!p) throw new Error('fixture does not parse — the test fixture is broken');
  return p;
}

/** A Response-like for loadPublicPackages without touching the network. */
function response(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  } as unknown as Response;
}

const CATALOG_PLANS = [
  { id: 'cat-s', slug: 's', name: 'S' },
  { id: 'cat-m', slug: 'm', name: 'M' },
  { id: 'cat-l', slug: 'l', name: 'L' },
  { id: 'cat-xl', slug: 'xl', name: 'XL' },
  { id: 'cat-flexi', slug: 'flexi', name: 'Flexi' },
];

/** The catalog's add-ons as api.ts::getAddons shapes them (baisa prices). */
const CATALOG_ADDONS: AddOn[] = [
  { id: 'a-backup', slug: 'daily-backup', name: 'Daily Backup', tagline: 'Automated daily backups', icon: '', monthly_price: 3000, included: false },
  { id: 'a-waf', slug: 'waf', name: 'Web Application Firewall', tagline: 'Coraza WAF', icon: '', monthly_price: 4000, included: false },
  { id: 'a-ips', slug: 'ips', name: 'Intrusion Prevention', tagline: 'CrowdSec', icon: '', monthly_price: 3000, included: false },
  { id: 'a-domain', slug: 'custom-domain', name: 'Custom Domain', tagline: 'Your domain', icon: '', monthly_price: 2000, included: false },
  { id: 'a-ip', slug: 'dedicated-ip', name: 'Dedicated IP', tagline: 'A public IPv4', icon: '', monthly_price: 2500, included: false },
  { id: 'a-logs', slug: 'log-management', name: 'Log Management', tagline: 'Loki', icon: '', monthly_price: 3000, included: false },
];

// ---------------------------------------------------------------------------
// 1. Step 1 — render model from the contract fixture.
// ---------------------------------------------------------------------------

describe('the contract fixture parses', () => {
  it('has four packages in the server order and the fifteen features', () => {
    const p = parsed();
    expect(p.currency).toBe('OMR');
    expect(p.packages.map(x => x.sku)).toEqual(['plan.s', 'plan.m', 'plan.l', 'plan.xl']);
    expect(p.features).toHaveLength(15);
    expect(p.price_book).toBe('OpenOva plans');
    expect(p.prices_as_of).toBe('2026-09-11');
  });

  it('rejects a body with no packages (the fallback trigger), and non-contract bodies', () => {
    expect(parsePublicPackages({ ...(fixture() as object), packages: [] })).toBeNull();
    expect(parsePublicPackages({ currency: 'OMR' })).toBeNull();
    expect(parsePublicPackages(null)).toBeNull();
    expect(parsePublicPackages('nope')).toBeNull();
    expect(parsePublicPackages([])).toBeNull();
  });

  it('drops a malformed package or cell instead of failing the whole table', () => {
    const raw = fixture() as any;
    raw.packages.push({ sku: 'plan.broken' }); // no name / price
    raw.features[0].cells['plan.s'] = { state: 'maybe' }; // unknown state
    const p = parsePublicPackages(raw)!;
    expect(p.packages).toHaveLength(4);
    expect(p.features[0].cells['plan.s']).toBeUndefined();
  });
});

describe('step 1: the table renders the three states and the hint', () => {
  const model = buildPackageTable(parsed());
  const row = (key: string) => model.rows.find(r => r.key === key)!;
  const cell = (key: string, sku: string) => row(key).cells.find(c => c.sku === sku)!;

  it('has one column per package with price and included quantities', () => {
    expect(model.columns.map(c => c.name)).toEqual(['S', 'M', 'L', 'XL']);
    expect(model.columns[0].priceMonth).toBe('9.000');
    expect(includesLines(model.columns[0].includes)).toEqual(['1 vCPU', '2 GB RAM', '25 GB storage', '50 Mbps']);
    expect(includesLines(model.columns[3].includes)).toEqual(['8 vCPU', '16 GB RAM', '200 GB storage', '1000 Mbps']);
  });

  it('Included renders ✓ with no hint', () => {
    const c = cell('backup', 'plan.xl');
    expect(c.state).toBe('included');
    expect(c.label).toBe('✓');
    expect(c.hint).toBeNull();
    expect(c.addonSku).toBeNull();
  });

  it('Optional renders the add-on price beside the muted tag, with the "Included from XL" hint', () => {
    const c = cell('backup', 'plan.s');
    expect(c.state).toBe('optional');
    expect(c.label).toBe('+ 1.500 OMR');
    expect(PACKAGE_STRINGS.addonTag).toBe('add-on');
    expect(c.addonSku).toBe('addon.backup');
    expect(c.hint).toBe('Included from XL');
    expect(c.hint).toBe(PACKAGE_STRINGS.includedFrom('XL'));
  });

  it('an Optional cell with no included_from has no hint rather than an invented one', () => {
    const c = cell('dedicated-ip', 'plan.m');
    expect(c.state).toBe('optional');
    expect(c.label).toBe('+ 2.000 OMR');
    expect(c.hint).toBeNull();
  });

  it('Not offered renders —', () => {
    const c = cell('dedicated-ip', 'plan.s');
    expect(c.state).toBe('not_offered');
    expect(c.label).toBe('—');
    expect(c.hint).toBeNull();
  });

  it('a quantity feature that is included shows the quantity with its unit', () => {
    expect(cell('bandwidth', 'plan.s').label).toBe('50 Mbps');
    expect(cell('bandwidth', 'plan.xl').label).toBe('1000 Mbps');
    expect(row('bandwidth').kind).toBe('quantity');
  });

  it("a package missing from a feature's cells reads as not offered", () => {
    const p = parsed();
    delete p.features[0].cells['plan.l'];
    const m = buildPackageTable(p);
    expect(m.rows[0].cells.find(c => c.sku === 'plan.l')!.state).toBe('not_offered');
  });

  it('every row has exactly one cell per column, in column order', () => {
    for (const r of model.rows) {
      expect(r.cells.map(c => c.sku)).toEqual(model.columns.map(c => c.sku));
    }
  });
});

describe('the recommended package', () => {
  const p = parsed();

  it('is the middle one by default — M for S/M/L/XL', () => {
    expect(recommendedSku(p.packages, null)).toBe('plan.m');
    expect(buildPackageTable(p).recommendedSku).toBe('plan.m');
  });

  it('follows ?recommended=<sku> when it names a package', () => {
    expect(buildPackageTable(p, { recommended: 'plan.l' }).recommendedSku).toBe('plan.l');
    expect(buildPackageTable(p, { recommended: 'plan.xl' }).recommendedSku).toBe('plan.xl');
  });

  it('ignores a ?recommended= that names nothing in the document', () => {
    expect(buildPackageTable(p, { recommended: 'plan.xxl' }).recommendedSku).toBe('plan.m');
    expect(buildPackageTable(p, { recommended: '' }).recommendedSku).toBe('plan.m');
  });

  it('is the exact middle for an odd count', () => {
    expect(recommendedSku(p.packages.slice(0, 3), null)).toBe('plan.m');
    expect(recommendedSku(p.packages.slice(0, 1), null)).toBe('plan.s');
    expect(recommendedSku([], null)).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 2. The chargeback URL and the fallback.
// ---------------------------------------------------------------------------

describe('the chargeback base URL', () => {
  it('derives chargeback.<fqdn> from marketplace.<fqdn>, like the console host', () => {
    expect(composeChargebackURL('marketplace.t99.omani.works')).toBe('https://chargeback.t99.omani.works');
    expect(composeChargebackURL('MARKETPLACE.T99.OMANTEL.BIZ')).toBe('https://chargeback.t99.omantel.biz');
  });

  it('is null on a host without the marketplace. prefix (dev localhost)', () => {
    expect(composeChargebackURL('localhost')).toBeNull();
    expect(composeChargebackURL('')).toBeNull();
    expect(composeChargebackURL('marketplace.')).toBeNull();
    expect(resolveChargebackURL({ hostname: 'localhost' })).toBeNull();
  });

  it('prefers the runtime window value, then the build-time value, then the derivation', () => {
    expect(resolveChargebackURL({
      runtime: 'https://bss.example.test/',
      build: 'http://127.0.0.1:18977',
      hostname: 'marketplace.t99.omani.works',
    })).toBe('https://bss.example.test');
    expect(resolveChargebackURL({
      build: 'http://127.0.0.1:18977/',
      hostname: 'marketplace.t99.omani.works',
    })).toBe('http://127.0.0.1:18977');
    expect(resolveChargebackURL({ hostname: 'marketplace.t99.omani.works' })).toBe('https://chargeback.t99.omani.works');
  });

  it('refuses a non-http(s) or malformed override and falls through', () => {
    expect(resolveChargebackURL({ runtime: 'javascript:alert(1)', hostname: 'marketplace.t99.omani.works' }))
      .toBe('https://chargeback.t99.omani.works');
    expect(resolveChargebackURL({ runtime: 42, build: '   ', hostname: 'localhost' })).toBeNull();
    expect(resolveChargebackURL({ build: 'not a url' })).toBeNull();
  });
});

describe('the fallback: no document → every step behaves as today', () => {
  beforeEach(() => resetPackagesLogOnce());

  it('resolves null and logs once when the request rejects', async () => {
    const warn = vi.fn();
    const fetchImpl = vi.fn(async () => { throw new TypeError('Failed to fetch'); }) as unknown as typeof fetch;
    const base = 'https://chargeback.t99.omani.works';
    expect(await loadPublicPackages(base, { fetchImpl, warn })).toBeNull();
    expect(fetchImpl).toHaveBeenCalledTimes(1);
    expect((fetchImpl as any).mock.calls[0][0]).toBe(`${base}${PUBLIC_PACKAGES_PATH}`);
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][0]).toMatch(/\[packages\].*unreachable/);
    // A second failure on the same page does not log again.
    expect(await loadPublicPackages(base, { fetchImpl, warn })).toBeNull();
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it('resolves null and logs once on a non-2xx status', async () => {
    const warn = vi.fn();
    const fetchImpl = vi.fn(async () => response(503, { error: 'down' })) as unknown as typeof fetch;
    expect(await loadPublicPackages('https://chargeback.t99.omani.works', { fetchImpl, warn })).toBeNull();
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][0]).toMatch(/answered 503/);
  });

  it('resolves null and logs once when the body publishes no packages', async () => {
    const warn = vi.fn();
    const fetchImpl = vi.fn(async () => response(200, { currency: 'OMR', packages: [], features: [] })) as unknown as typeof fetch;
    expect(await loadPublicPackages('https://chargeback.t99.omani.works', { fetchImpl, warn })).toBeNull();
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn.mock.calls[0][0]).toMatch(/published no packages/);
  });

  it('resolves null and logs once when there is no chargeback host at all, without fetching', async () => {
    const warn = vi.fn();
    const fetchImpl = vi.fn() as unknown as typeof fetch;
    expect(await loadPublicPackages(null, { fetchImpl, warn })).toBeNull();
    expect(fetchImpl).not.toHaveBeenCalled();
    expect(warn).toHaveBeenCalledTimes(1);
  });

  it('CONTROL: a healthy response parses and logs nothing', async () => {
    const warn = vi.fn();
    const fetchImpl = vi.fn(async (_url: string, init: RequestInit) => {
      // Public + CORS: a simple GET with no credentials.
      expect(init.credentials).toBe('omit');
      expect(init.mode).toBe('cors');
      return response(200, fixture());
    }) as unknown as typeof fetch;
    const data = await loadPublicPackages('https://chargeback.t99.omani.works', { fetchImpl, warn });
    expect(data?.packages.map(p => p.sku)).toEqual(['plan.s', 'plan.m', 'plan.l', 'plan.xl']);
    expect(warn).not.toHaveBeenCalled();
  });

  it('with no document the add-ons step keeps the catalog list untouched (no twin filtering, no included group)', () => {
    // There is no document to call funnelAddonsFor with — the steps branch on
    // `doc && pkg` and hand the catalog list straight through. This pins the
    // shape they hand through: the catalog's own ids and prices.
    expect(CATALOG_ADDONS.map(a => a.id)).toEqual(['a-backup', 'a-waf', 'a-ips', 'a-domain', 'a-ip', 'a-logs']);
  });
});

// ---------------------------------------------------------------------------
// 3. Step 1 → cart: choosing a package does what the legacy deck did.
// ---------------------------------------------------------------------------

describe('choosing a package sets the cart plan as PlanStep did, plus the sku', () => {
  const p = parsed();

  beforeEach(() => {
    localStorage.clear();
  });

  it('maps the package to the catalog plan id billing requires, by slug, then name, then sku tail', () => {
    expect(catalogPlanIdForPackage(p.packages[1], CATALOG_PLANS)).toBe('cat-m');
    expect(catalogPlanIdForPackage(p.packages[3], CATALOG_PLANS)).toBe('cat-xl');
    expect(catalogPlanIdForPackage(p.packages[1], [{ id: 'by-name', name: 'M' }])).toBe('by-name');
    // No catalog (fetch failed): the sku tail is the legacy deck's own id.
    expect(catalogPlanIdForPackage(p.packages[1], [])).toBe('m');
  });

  it('setPackage writes plan id, name and plan.sku; add-ons are left alone unless given', () => {
    localStorage.setItem('org-cart', JSON.stringify({ plan: 'cat-l', planName: 'L', apps: ['1'], addons: ['a-ips'] }));
    const pkg = p.packages[0];
    const cart = setPackage({
      planId: catalogPlanIdForPackage(pkg, CATALOG_PLANS),
      planName: pkg.name,
      packageSku: pkg.sku,
    });
    expect(cart.plan).toBe('cat-s');
    expect(cart.planName).toBe('S');
    expect(cart.packageSku).toBe('plan.s');
    expect(cart.addons).toEqual(['a-ips']);
    // Persisted — the next step reads the same thing.
    expect(readCart().packageSku).toBe('plan.s');
  });

  it('setPlan (legacy deck) still works as before and drops a stale sku on a plan change', () => {
    setPackage({ planId: 'cat-s', planName: 'S', packageSku: 'plan.s' });
    expect(setPlan('cat-s', 'S').packageSku).toBe('plan.s');
    const after = setPlan('cat-l', 'L');
    expect(after.plan).toBe('cat-l');
    expect(after.planName).toBe('L');
    expect(after.packageSku).toBeNull();
  });

  it('an old cart without the package field reads with a safe default', () => {
    localStorage.setItem('org-cart', JSON.stringify({ plan: 'm', planName: 'M', apps: ['1'], addons: [] }));
    expect(readCart().packageSku).toBeNull();
  });

  it('the reverse bridge finds the package a catalog plan stands for', () => {
    expect(packageForPlan(p, { id: 'cat-m', slug: 'm', name: 'M' })?.sku).toBe('plan.m');
    expect(packageForPlan(p, { id: 'by-name', name: 'XL' })?.sku).toBe('plan.xl');
    expect(packageForPlan(p, { id: 'l' })?.sku).toBe('plan.l'); // the legacy fallback id
    expect(packageForPlan(p, { id: 'cat-flexi', slug: 'flexi', name: 'Flexi' })).toBeNull();
  });

  it('packageForCart prefers the stamped sku, then the plan', () => {
    expect(packageForCart(p, { packageSku: 'plan.l', plan: 'cat-m', planName: 'M' })?.sku).toBe('plan.l');
    expect(packageForCart(p, { packageSku: 'plan.gone', plan: 'm', planName: 'M' })?.sku).toBe('plan.m');
    expect(packageForCart(p, { packageSku: null, plan: 'cat-uuid', planName: 'M' })?.sku).toBe('plan.m');
    expect(packageForCart(p, { packageSku: null, plan: null })).toBeNull();
  });

  it('minor units: "1.500" OMR is 1500 baisa, garbage is 0', () => {
    expect(minorUnits('1.500')).toBe(1500);
    expect(minorUnits('9')).toBe(9000);
    expect(minorUnits('abc')).toBe(0);
    expect(minorUnits(undefined)).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// 4. Step 3 — the chosen package's features become the add-ons list.
// ---------------------------------------------------------------------------

describe('step 3: the add-ons list is the chosen package\'s optional features', () => {
  const p = parsed();

  it('for M: Backup is a priced add-on (1.500, BSS SKU, "Included from XL"), not-offered features are absent', () => {
    const { addons, included, packageName } = funnelAddonsFor(p, 'plan.m', CATALOG_ADDONS);
    expect(packageName).toBe('M');
    const backup = addons.find(a => a.id === 'addon.backup')!;
    expect(backup).toMatchObject({ id: 'addon.backup', slug: 'backup', name: 'Backup', monthly_price: 1500, included: false, hint: 'Included from XL' });
    expect(backup.tagline).toBe('Daily snapshots with 30-day retention.');
    // Dedicated IP is optional on M (no included_from → no hint).
    const ip = addons.find(a => a.id === 'addon.dedicated-ip')!;
    expect(ip.monthly_price).toBe(2000);
    expect(ip.hint).toBeUndefined();
    // The BSS optional set for M, in document order.
    expect(addons.filter(a => a.id.startsWith('addon.')).map(a => a.id))
      .toEqual(['addon.backup', 'addon.dedicated-ip', 'addon.ai-seo', 'addon.ai-website-builder', 'addon.domain']);
    // Included boolean features; the quantity (bandwidth) stays on the table.
    expect(included.map(f => f.key)).toEqual(['applications', 'databases', 'mail', 'ssl', 'sso', 'ddos', 'malware-scanner', 'waf', 'support']);
    expect(included.find(f => f.key === 'ssl')!.blurb).toBe('Automatic TLS certificates for every host.');
  });

  it('for XL: Backup moves to the included group and is not an add-on', () => {
    const { addons, included } = funnelAddonsFor(p, 'plan.xl', CATALOG_ADDONS);
    expect(addons.find(a => a.id === 'addon.backup')).toBeUndefined();
    expect(included.map(f => f.key)).toContain('backup');
    expect(addons.filter(a => a.id.startsWith('addon.')).map(a => a.id)).toEqual(['addon.dedicated-ip']);
  });

  it('for S: Dedicated IP is not offered, so it is neither an add-on nor included', () => {
    const { addons, included } = funnelAddonsFor(p, 'plan.s', CATALOG_ADDONS);
    expect(addons.find(a => a.id === 'addon.dedicated-ip')).toBeUndefined();
    expect(included.find(f => f.key === 'dedicated-ip')).toBeUndefined();
  });

  it('catalog add-ons that twin a BSS feature yield to it; the rest keep their catalog id and price', () => {
    const { addons } = funnelAddonsFor(p, 'plan.m', CATALOG_ADDONS);
    const ids = addons.map(a => a.id);
    // Twins (explicit table): Daily Backup, Custom Domain, Dedicated IP, WAF.
    expect(ids).not.toContain('a-backup');
    expect(ids).not.toContain('a-domain');
    expect(ids).not.toContain('a-ip');
    expect(ids).not.toContain('a-waf');
    // No twin: today's behaviour.
    expect(addons.find(a => a.id === 'a-ips')).toMatchObject({ name: 'Intrusion Prevention', monthly_price: 3000 });
    expect(addons.find(a => a.id === 'a-logs')).toMatchObject({ name: 'Log Management', monthly_price: 3000 });
    // BSS first, then the catalog remainder.
    expect(ids.slice(-2)).toEqual(['a-ips', 'a-logs']);
  });

  it('the twin mapping is the explicit table, then an exact normalised name match', () => {
    expect(CATALOG_ADDON_TWINS).toEqual({ 'daily-backup': 'backup', 'custom-domain': 'domain', 'dedicated-ip': 'dedicated-ip', 'waf': 'waf' });
    expect(twinFeatureKey({ slug: 'daily-backup', name: 'Daily Backup' }, p.features)).toBe('backup');
    // Name match for a catalog add-on the table does not list.
    expect(twinFeatureKey({ slug: 'malware-scan', name: 'Malware Scanner' }, p.features)).toBe('malware-scanner');
    expect(twinFeatureKey({ slug: 'sso-addon', name: '  sso ' }, p.features)).toBe('sso');
    // Not a twin.
    expect(twinFeatureKey({ slug: 'ips', name: 'Intrusion Prevention' }, p.features)).toBeNull();
    expect(twinFeatureKey({ slug: 'vuln-scan', name: 'Vulnerability Scanner' }, p.features)).toBeNull();
    // An explicit entry whose feature is absent from the document falls through to the name.
    expect(twinFeatureKey({ slug: 'waf', name: 'Something Else' }, p.features.filter(f => f.key !== 'waf'))).toBeNull();
  });

  it('a package change keeps catalog ids and only the BSS add-ons the new package still offers', () => {
    const picked = ['a-ips', 'addon.backup', 'addon.dedicated-ip'];
    // M → XL: Backup is included on XL (dropped), Dedicated IP still optional (kept).
    expect(pruneAddonsForPackage(p, 'plan.xl', picked)).toEqual(['a-ips', 'addon.dedicated-ip']);
    // M → S: Dedicated IP is not offered on S (dropped), Backup still optional (kept).
    expect(pruneAddonsForPackage(p, 'plan.s', picked)).toEqual(['a-ips', 'addon.backup']);
    // M → M: nothing changes.
    expect(pruneAddonsForPackage(p, 'plan.m', picked)).toEqual(picked);
  });

  it('setPackage with addons replaces the list in the same write', () => {
    localStorage.clear();
    localStorage.setItem('org-cart', JSON.stringify({ plan: 'm', planName: 'M', apps: ['1'], addons: ['a-ips', 'addon.backup'], packageSku: 'plan.m' }));
    const cart = setPackage({ planId: 'xl', planName: 'XL', packageSku: 'plan.xl', addons: pruneAddonsForPackage(p, 'plan.xl', ['a-ips', 'addon.backup']) });
    expect(cart.addons).toEqual(['a-ips']);
    expect(cart.packageSku).toBe('plan.xl');
    expect(readCart().addons).toEqual(['a-ips']);
  });
});

// ---------------------------------------------------------------------------
// Markup contract — the wiring on disk, not a reconstruction of it.
// ---------------------------------------------------------------------------

describe('wizard markup contract', () => {
  const page = readFileSync(join(ROOT, 'src', 'pages', 'plans.astro'), 'utf8');
  const table = readFileSync(join(ROOT, 'src', 'components', 'PackageTable.svelte'), 'utf8');
  const addonsStep = readFileSync(join(ROOT, 'src', 'components', 'AddonsStep.svelte'), 'utf8');
  const review = readFileSync(join(ROOT, 'src', 'components', 'ReviewStep.svelte'), 'utf8');
  const checkout = readFileSync(join(ROOT, 'src', 'components', 'CheckoutStep.svelte'), 'utf8');

  it('/plans mounts PackageTable', () => {
    expect(page).toMatch(/import PackageTable from ['"]\.\.\/components\/PackageTable\.svelte['"]/);
    expect(page).toMatch(/<PackageTable\s+client:load/);
  });

  it('PackageTable falls back to PlanStep, reads the chargeback base from config, and picks nothing but the package', () => {
    expect(table).toMatch(/import PlanStep from ['"]\.\/PlanStep\.svelte['"]/);
    expect(table).toMatch(/<PlanStep\s*\/>/);
    expect(table).toMatch(/chargebackBaseURL\(\)/);
    // No add-on selection on step 1: no checkbox, no toggle.
    expect(table).not.toMatch(/type="checkbox"/);
    expect(table).not.toMatch(/toggleAddon|addonPicksFor|packageAddons/);
  });

  it('the Add-ons step consumes the document; Review and Checkout resolve ids through the same list', () => {
    expect(addonsStep).toMatch(/funnelAddonsFor\(/);
    expect(addonsStep).toMatch(/data-testid="addons-included"/);
    expect(review).toMatch(/funnelAddonsFor\(/);
    expect(checkout).toMatch(/funnelAddonsFor\(/);
    // The cart's one list is what both POSTs send.
    expect(checkout).toMatch(/addons: cart\.addons/);
    expect(checkout).not.toMatch(/orderAddonIds|packageAddons/);
  });

  it('carries no feature list of its own — every row comes from the document', () => {
    // The legacy deck's hardcoded capability table must not have migrated here.
    expect(table).not.toMatch(/capsByName|capRows|backupRetention|responseSla/);
  });
});
