// packages.test.ts — #6971: the package comparison table's contract half.
//
// Fed from fixtures/public-packages.json, a body in the exact shape BSS
// publishes at GET /api/v1/public/packages. Three groups of assertions:
//
//   1. the render model shows the THREE states and the "Included from XL" hint
//      from that fixture, and the recommended column follows ?recommended=;
//   2. the fallback: a failed / non-2xx / empty fetch resolves to null and
//      logs exactly ONCE per page — the page then renders the legacy deck;
//   3. the hand-off: choosing a package puts the catalog plan id, the
//      `plan.sku` and the ticked add-on SKUs into the cart, and the cart
//      produces the merged `addons` list both POSTs send.
//
// Plus a markup contract in the redeemMarkupContract.test.ts shape: the page
// really mounts PackageTable and PackageTable really falls back to PlanStep,
// asserted against the files on disk rather than a reconstruction.

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import {
  addonPicksFor,
  buildPackageTable,
  catalogPlanIdForPackage,
  includesLines,
  loadPublicPackages,
  minorUnits,
  PACKAGE_STRINGS,
  parsePublicPackages,
  PUBLIC_PACKAGES_PATH,
  recommendedSku,
  resetPackagesLogOnce,
  type PublicPackages,
} from './packages';
import { composeChargebackURL, resolveChargebackURL } from './config';
import { orderAddonIds, packageAddonsBaisa, readCart, setPackage, setPlan } from './cart';

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

// ---------------------------------------------------------------------------
// 1. Render model from the contract fixture.
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

describe('the table renders the three states and the hint', () => {
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

  it('Optional renders "+ <price> <currency>" with its add-on sku and the "Included from XL" hint', () => {
    const c = cell('backup', 'plan.s');
    expect(c.state).toBe('optional');
    expect(c.label).toBe('+ 1.500 OMR');
    expect(c.addonSku).toBe('addon.backup');
    expect(c.hint).toBe('Included from XL');
    // The hint names the package by its display name, resolved from the sku.
    expect(c.hint).toBe(PACKAGE_STRINGS.includedFrom('XL'));
  });

  it('an Optional cell with no included_from has no hint rather than an invented one', () => {
    const c = cell('dedicated-ip', 'plan.m');
    expect(c.state).toBe('optional');
    expect(c.label).toBe('+ 2.000 OMR');
    expect(c.hint).toBeNull();
  });

  it('Not offered renders — ', () => {
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

  it('a package missing from a feature\'s cells reads as not offered', () => {
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

  it('ignores a ?recommended= that names nothing in the response', () => {
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

describe('the fallback renders when the fetch fails', () => {
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
});

// ---------------------------------------------------------------------------
// 3. Choosing a package → the flow.
// ---------------------------------------------------------------------------

describe('choosing a package passes plan.sku and ticked add-ons into the flow', () => {
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

  it('only optional cells with an add-on sku become picks; included / not-offered ticks are dropped', () => {
    expect(addonPicksFor(p, 'plan.s', ['backup', 'ssl', 'dedicated-ip'])).toEqual([
      { sku: 'addon.backup', feature: 'backup', name: 'Backup', price_month: '1.500', currency: 'OMR' },
    ]);
    // Backup is included on XL: the same tick carries nothing there.
    expect(addonPicksFor(p, 'plan.xl', ['backup'])).toEqual([]);
    // Dedicated IP is optional on M.
    expect(addonPicksFor(p, 'plan.m', ['dedicated-ip']).map(a => a.sku)).toEqual(['addon.dedicated-ip']);
  });

  it('setPackage writes plan id, name, plan.sku and the add-ons to the cart', () => {
    const pkg = p.packages[0];
    const cart = setPackage({
      planId: catalogPlanIdForPackage(pkg, CATALOG_PLANS),
      planName: pkg.name,
      packageSku: pkg.sku,
      addons: addonPicksFor(p, pkg.sku, ['backup', 'domain']),
    });
    expect(cart.plan).toBe('cat-s');
    expect(cart.planName).toBe('S');
    expect(cart.packageSku).toBe('plan.s');
    expect(cart.packageAddons.map(a => a.sku)).toEqual(['addon.backup', 'addon.domain']);
    // Persisted — the next page reads the same thing.
    expect(readCart().packageSku).toBe('plan.s');
    expect(readCart().packageAddons).toHaveLength(2);
  });

  it('the order carries catalog add-on ids AND the package add-on SKUs in one `addons` list', () => {
    setPackage({ planId: 'cat-s', planName: 'S', packageSku: 'plan.s', addons: addonPicksFor(p, 'plan.s', ['backup']) });
    const cart = readCart();
    cart.addons = ['waf', 'addon.backup']; // a catalog pick + an accidental duplicate
    expect(orderAddonIds(cart)).toEqual(['waf', 'addon.backup']);
    expect(packageAddonsBaisa(cart)).toBe(1500);
  });

  it('a plan change made elsewhere drops the package and its add-ons; re-selecting the same plan keeps them', () => {
    setPackage({ planId: 'cat-s', planName: 'S', packageSku: 'plan.s', addons: addonPicksFor(p, 'plan.s', ['backup']) });
    expect(setPlan('cat-s', 'S').packageSku).toBe('plan.s');
    const after = setPlan('cat-l', 'L');
    expect(after.plan).toBe('cat-l');
    expect(after.packageSku).toBeNull();
    expect(after.packageAddons).toEqual([]);
  });

  it('an old cart without the package fields reads with safe defaults', () => {
    localStorage.setItem('org-cart', JSON.stringify({ plan: 'm', planName: 'M', apps: ['1'], addons: [] }));
    const cart = readCart();
    expect(cart.packageSku).toBeNull();
    expect(cart.packageAddons).toEqual([]);
    expect(orderAddonIds(cart)).toEqual([]);
    expect(packageAddonsBaisa(cart)).toBe(0);
  });

  it('minor units: "1.500" OMR is 1500 baisa, garbage is 0', () => {
    expect(minorUnits('1.500')).toBe(1500);
    expect(minorUnits('9')).toBe(9000);
    expect(minorUnits('72.000')).toBe(72000);
    expect(minorUnits('abc')).toBe(0);
    expect(minorUnits(undefined)).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// Markup contract — the wiring on disk, not a reconstruction of it.
// ---------------------------------------------------------------------------

describe('plans page markup contract', () => {
  const page = readFileSync(join(ROOT, 'src', 'pages', 'plans.astro'), 'utf8');
  const table = readFileSync(join(ROOT, 'src', 'components', 'PackageTable.svelte'), 'utf8');

  it('/plans mounts PackageTable', () => {
    expect(page).toMatch(/import PackageTable from ['"]\.\.\/components\/PackageTable\.svelte['"]/);
    expect(page).toMatch(/<PackageTable\s+client:load/);
  });

  it('PackageTable falls back to the legacy PlanStep deck and reads the chargeback base from config', () => {
    expect(table).toMatch(/import PlanStep from ['"]\.\/PlanStep\.svelte['"]/);
    expect(table).toMatch(/<PlanStep\s*\/>/);
    expect(table).toMatch(/chargebackBaseURL\(\)/);
  });

  it('carries no feature list of its own — every row comes from the response', () => {
    // The legacy deck's hardcoded capability table must not have migrated here.
    expect(table).not.toMatch(/capsByName|capRows|backupRetention|responseSla/);
  });
});
