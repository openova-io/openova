// packagesLadder.test.ts — #6971: the v2 package document (the ladder).
//
// Fed from fixtures/public-packages-v2.json — the body BSS 0.1.61 on hw307
// answered at GET /api/v1/public/packages on 2026-10-10, pretty-printed and
// otherwise untouched: `groups`, `floor`, a package `shape` + `step_up`,
// cells with `overage` / `level` / `note` / `included_from`, the `teaser`
// state. Every number is the workbook's (S 2.490 / M 4.490 / L 7.990 /
// XL 13.990; the L→XL gap 6.000 bundling Backup + AI SEO + AI builder +
// Domain = 1.500 + 2.000 + 2.000 + 0.500).
//
//   1. the document parses, and `isLadderDocument` is what tells v2 from v1;
//   2. step 1 — the ladder model: cards with the shape and its guarantee, the
//      groups in order with empty ones skipped, every cell kind, the floor;
//   3. step 3 — the three blocks for a package, the next-level add-on, the
//      "Not on <pkg>" rung, and the step-up hint on the bundled sum;
//   4. the cart bridge with next-level SKUs; the v1 fixture is untouched.
//
// Plus the markup contract: the ladder's stylesheet is a PAGE import (the
// production-bundle lesson of 2026-10-10), the component has no <style>, and
// the Add-ons step consumes the ladder.

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import {
  addonsLadderFor,
  bssAddonSkus,
  buildLadder,
  bundledOn,
  drTopologyFor,
  funnelAddonsFor,
  isLadderDocument,
  ladderCard,
  ladderRecommendedSku,
  moneyString,
  PACKAGE_STRINGS,
  packageCapacity,
  packageSpecsLine,
  parsePublicPackages,
  pruneAddonsForPackage,
  purchasableAddonSkus,
  stepUpHint,
  twinFeatureKey,
  type PublicPackages,
} from './packages';
import type { AddOn } from './api';

const ROOT = join(__dirname, '..', '..');

function fixture(name: string): unknown {
  return JSON.parse(readFileSync(join(ROOT, 'fixtures', name), 'utf8'));
}

function v2(): PublicPackages {
  const p = parsePublicPackages(fixture('public-packages-v2.json'));
  if (!p) throw new Error('v2 fixture does not parse — the test fixture is broken');
  return p;
}

function v1(): PublicPackages {
  const p = parsePublicPackages(fixture('public-packages.json'));
  if (!p) throw new Error('v1 fixture does not parse');
  return p;
}

/** The catalog's add-ons as api.ts::getAddons shapes them (baisa prices). */
const CATALOG_ADDONS: AddOn[] = [
  { id: 'a-backup', slug: 'daily-backup', name: 'Daily Backup', tagline: 'Automated daily backups', icon: '', monthly_price: 3000, included: false },
  { id: 'a-waf', slug: 'waf', name: 'Web Application Firewall', tagline: 'Coraza WAF', icon: '', monthly_price: 4000, included: false },
  { id: 'a-ips', slug: 'ips', name: 'Intrusion Prevention', tagline: 'CrowdSec', icon: '', monthly_price: 3000, included: false },
  { id: 'a-support', slug: 'priority-support', name: '24/7 customer support', tagline: 'Around the clock', icon: '', monthly_price: 5000, included: false },
];

// ---------------------------------------------------------------------------
// 1. The document parses; the version is the presence of `groups`.
// ---------------------------------------------------------------------------

describe('the v2 contract fixture parses', () => {
  it('is a ladder document; the v1 fixture is not', () => {
    expect(isLadderDocument(v2())).toBe(true);
    expect(isLadderDocument(v1())).toBe(false);
    // An empty groups list is a v1 document too.
    const raw = fixture('public-packages-v2.json') as any;
    raw.groups = [];
    expect(isLadderDocument(parsePublicPackages(raw)!)).toBe(false);
  });

  it('has the four packages at the workbook prices, M recommended, the shape and the step-up', () => {
    const p = v2();
    expect(p.packages.map(x => [x.sku, x.price_month])).toEqual([
      ['plan.s', '2.490'], ['plan.m', '4.490'], ['plan.l', '7.990'], ['plan.xl', '13.990'],
    ]);
    expect(p.packages.map(x => x.recommended ?? false)).toEqual([false, true, false, false]);
    expect(p.packages[1].shape).toEqual({ vcpu: 2, memory_gb: 4, vcpu_guaranteed: 0.33, memory_gb_guaranteed: 1.33, disk_gb: 50 });
    expect(p.packages[0].annual_months_free).toBe(0);
    expect(p.packages[0].tagline).toBe('');
    expect(p.packages[0].step_up).toEqual({
      next_sku: 'plan.m', next_name: 'M', gap_month: '2.000', bundled_addon_keys: [], bundled_addons_sum_month: '0.000', rule_holds: true,
    });
    expect(p.packages[2].step_up?.bundled_addon_keys).toEqual(['backup', 'ai_seo', 'ai_builder', 'domain']);
    expect(p.packages[2].step_up?.gap_month).toBe('6.000');
    expect(p.packages[3].step_up).toBeNull();
  });

  it('has the seven groups in order and the nine floor items in order', () => {
    const p = v2();
    expect(p.groups!.map(g => g.key)).toEqual(['capacity', 'features', 'access', 'ops', 'scope', 'resilience', 'service']);
    expect(p.floor!.map(f => f.name)).toEqual([
      'Applications', 'Databases', 'Mail server (unlimited accounts)', 'Unlimited free SSL', 'SSO',
      'Standard DDoS protection', 'Malware scanner', 'Web application firewall', '24/7 customer support',
    ]);
  });

  it('parses every cell kind: quantity + overage, level + levels, access + note, the teaser state', () => {
    const p = v2();
    const f = (k: string) => p.features.find(x => x.key === k)!;
    expect(f('bandwidth').kind).toBe('quantity');
    expect(f('bandwidth').group).toBe('capacity');
    expect(f('bandwidth').addon_sku).toBe('eip.bandwidth_mbps');
    expect(f('bandwidth').cells['plan.l']).toEqual({ state: 'included', quantity: 250, overage: 'metered' });
    expect(f('dr_topology').kind).toBe('level');
    expect(f('dr_topology').levels).toEqual(['single region', 'active-passive']);
    expect(f('dr_topology').cells['plan.xl'].level).toBe(1);
    expect(f('gitea_iac').kind).toBe('access');
    expect(f('gitea_iac').cells['plan.m'].note).toBe('read');
    expect(f('vuln_dashboard').teaser).toBe(true);
    expect(f('vuln_dashboard').cells['plan.s']).toEqual({ state: 'teaser', included_from: 'plan.m' });
  });

  it('drops an unknown overage and reads an unknown kind as boolean, without failing the document', () => {
    const raw = fixture('public-packages-v2.json') as any;
    raw.features[0].cells['plan.s'].overage = 'soft';
    raw.features[0].kind = 'mystery';
    const p = parsePublicPackages(raw)!;
    expect(p.features[0].cells['plan.s'].overage).toBeUndefined();
    expect(p.features[0].kind).toBe('boolean');
  });
});

// ---------------------------------------------------------------------------
// 2. Step 1 — the ladder's render model.
// ---------------------------------------------------------------------------

describe('step 1: the ladder model', () => {
  const doc = v2();
  const model = buildLadder(doc);
  const row = (key: string) => model.groups.flatMap(g => g.rows).find(r => r.key === key)!;
  const cell = (key: string, sku: string) => row(key).cells.find(c => c.sku === sku)!;

  it('has four cards in order with the price, the shape, the guarantee in its own line and the disk', () => {
    expect(model.cards.map(c => c.name)).toEqual(['S', 'M', 'L', 'XL']);
    const m = model.cards[1];
    expect(m.priceMonth).toBe('4.490');
    expect(m.shapeHeadline).toBe('2 vCPU · 4 GB RAM');
    expect(m.shapeGuarantee).toBe('0.33 vCPU · 1.33 GB guaranteed');
    expect(m.diskLine).toBe('50 GB disk');
    expect(model.cards[3].shapeGuarantee).toBe('1.33 vCPU · 5.33 GB guaranteed');
  });

  it('hides the annual line at 0 months and the tagline when empty; shows them when set', () => {
    expect(model.cards.every(c => c.annualLine === null)).toBe(true);
    expect(model.cards.every(c => c.tagline === '')).toBe(true);
    const card = ladderCard({ ...doc.packages[1], tagline: 'Business', annual_months_free: 2 }, true);
    expect(card.tagline).toBe('Business');
    expect(card.annualLine).toBe('Annual: 2 months free');
    expect(ladderCard({ ...doc.packages[1], annual_months_free: 1 }, false).annualLine).toBe('Annual: 1 month free');
  });

  it('recommends the flagged package (M), unless ?recommended= names another', () => {
    expect(model.recommendedSku).toBe('plan.m');
    expect(model.cards.map(c => c.recommended)).toEqual([false, true, false, false]);
    expect(ladderRecommendedSku(doc.packages, 'plan.l')).toBe('plan.l');
    expect(ladderRecommendedSku(doc.packages, 'plan.nope')).toBe('plan.m');
    // Without a flag, the middle one — as the v1 table.
    const unflagged = doc.packages.map(p => ({ ...p, recommended: false }));
    expect(ladderRecommendedSku(unflagged, null)).toBe('plan.m');
  });

  it('renders the groups in the document order, skipping a declared group with no feature', () => {
    expect(model.groups.map(g => g.key)).toEqual(['capacity', 'features', 'access', 'ops', 'resilience']);
    expect(model.groups.map(g => g.name)).toEqual(['Capacity', 'Features', 'Access', 'Managed operations', 'Resilience']);
    expect(model.groups[0].rows.map(r => r.key)).toEqual(['bandwidth', 'disk']);
    expect(model.groups[3].rows).toHaveLength(6);
  });

  it('puts a feature whose group is unknown into a trailing "More" group rather than losing it', () => {
    const raw = fixture('public-packages-v2.json') as any;
    raw.features.push({ key: 'odd', name: 'Odd one', group: 'nowhere', kind: 'boolean', cells: { 'plan.s': { state: 'included' } } });
    raw.features.push({ key: 'odder', name: 'Odder one', kind: 'boolean', cells: {} });
    const m = buildLadder(parsePublicPackages(raw)!);
    const last = m.groups[m.groups.length - 1];
    expect(last.key).toBe('other');
    expect(last.name).toBe(PACKAGE_STRINGS.ladder.otherGroup);
    expect(last.rows.map(r => r.key)).toEqual(['odd', 'odder']);
  });

  it('quantity: the amount with its unit and the overage rule as the hint', () => {
    expect(cell('bandwidth', 'plan.s')).toMatchObject({ state: 'included', label: '50 Mbps', hint: 'hard cap' });
    expect(cell('bandwidth', 'plan.l')).toMatchObject({ label: '250 Mbps', hint: 'then metered' });
    expect(cell('disk', 'plan.xl')).toMatchObject({ label: '250 GB', hint: 'then metered' });
  });

  it('quantity with no number and an unlimited rule reads "Unlimited", said once', () => {
    const raw = fixture('public-packages-v2.json') as any;
    raw.features[0].cells['plan.xl'] = { state: 'included', overage: 'unlimited' };
    const m = buildLadder(parsePublicPackages(raw)!);
    const c = m.groups[0].rows[0].cells.find(x => x.sku === 'plan.xl')!;
    expect(c.label).toBe('Unlimited');
    expect(c.hint).toBeNull();
  });

  it('level: the label from the levels list', () => {
    expect(cell('dr_topology', 'plan.s')).toMatchObject({ state: 'included', kind: 'level', label: 'single region', hint: null });
    expect(cell('dr_topology', 'plan.xl').label).toBe('active-passive');
  });

  it('access: ✓ with the note as the hint, — when not offered', () => {
    expect(cell('gitea_iac', 'plan.m')).toMatchObject({ label: '✓', hint: 'read' });
    expect(cell('gitea_iac', 'plan.l')).toMatchObject({ label: '✓', hint: null });
    expect(cell('gitea_iac', 'plan.s')).toMatchObject({ state: 'not_offered', label: '—', hint: null });
  });

  it('optional: the price in the ADD-ON cell with the "Included from XL" hint; none when it is optional everywhere', () => {
    expect(cell('backup', 'plan.s')).toMatchObject({ state: 'optional', label: '+ 1.500 / mo', hint: 'Included from XL' });
    expect(cell('domain', 'plan.l')).toMatchObject({ state: 'optional', label: '+ 0.500 / mo', hint: 'Included from XL' });
    expect(cell('dedicated_ip', 'plan.xl')).toMatchObject({ state: 'optional', label: '+ 20.833 / mo', hint: null });
    expect(cell('backup', 'plan.xl')).toMatchObject({ state: 'included', label: '✓' });
  });

  it('teaser: "from L" in the cell, with the package named for the screen reader', () => {
    expect(cell('compliance', 'plan.m')).toMatchObject({ state: 'teaser', label: 'from L', hint: null });
    expect(cell('compliance', 'plan.m').stateLabel).toBe('Not on this package — included from L');
    expect(cell('vuln_dashboard', 'plan.s').label).toBe('from M');
    expect(cell('vuln_dashboard', 'plan.m')).toMatchObject({ state: 'included', label: '✓' });
  });

  it('the floor strip is the floor names in order, and the footer carries the book', () => {
    expect(model.floor).toEqual(doc.floor!.map(f => f.name));
    expect(model.floor).toHaveLength(9);
    expect(model.pricesAsOf).toBe('2026-10-10');
    expect(model.priceBook).toBe('OpenOva plans');
  });

  it('every row has exactly one cell per package, in package order', () => {
    for (const g of model.groups) {
      for (const r of g.rows) expect(r.cells.map(c => c.sku)).toEqual(['plan.s', 'plan.m', 'plan.l', 'plan.xl']);
    }
  });
});

// ---------------------------------------------------------------------------
// 3. Step 3 — the three blocks and the step-up hint.
// ---------------------------------------------------------------------------

describe('step 3: the three blocks for a package', () => {
  const doc = v2();

  it('for L: block A has the included items with their value, block B the five optional add-ons, block C the two XL-only features', () => {
    const l = addonsLadderFor(doc, 'plan.l')!;
    expect(l.packageName).toBe('L');
    expect(l.packagePriceBaisa).toBe(7990);
    const inc = Object.fromEntries(l.included.map(i => [i.key, i.value]));
    expect(inc).toMatchObject({
      bandwidth: '250 Mbps · then metered',
      disk: '100 GB · then metered',
      console: null,
      gitea_iac: null,
      vuln_dashboard: null,
      compliance: null,
      patching: null,
      dr_topology: 'single region',
    });
    expect(l.choices.map(c => [c.id, c.priceMonth, c.includedFrom])).toEqual([
      ['addon.backup', '1.500', 'XL'],
      ['addon.ai_seo', '2.000', 'XL'],
      ['addon.ai_builder', '2.000', 'XL'],
      ['addon.domain', '0.500', 'XL'],
      ['addon.dedicated_ip', '20.833', null],
    ]);
    expect(l.missing.map(m => [m.key, m.state, m.upgrade?.name, m.upgrade?.state])).toEqual([
      ['kube_api', 'not_offered', 'XL', 'included'],
      ['maintenance', 'not_offered', 'XL', 'included'],
    ]);
  });

  it('for M: block C names the rung for each teaser and not-offered feature — the cell\'s stated included_from, else the nearest one that has it', () => {
    const m = addonsLadderFor(doc, 'plan.m')!;
    expect(m.missing.map(x => [x.key, x.state, x.upgrade?.name])).toEqual([
      ['kube_api', 'not_offered', 'XL'],
      ['compliance', 'teaser', 'L'],
      ['patching', 'not_offered', 'L'],
      ['maintenance', 'not_offered', 'XL'],
    ]);
    // The live document states included_from on its not-offered cells; a
    // document that does not gets the nearest rung derived from the cells.
    const raw = fixture('public-packages-v2.json') as any;
    for (const f of raw.features) for (const c of Object.values<any>(f.cells)) if (c.state === 'not_offered') delete c.included_from;
    const derived = addonsLadderFor(parsePublicPackages(raw)!, 'plan.m')!;
    expect(derived.missing.map(x => [x.key, x.upgrade?.name])).toEqual([['kube_api', 'XL'], ['compliance', 'L'], ['patching', 'L'], ['maintenance', 'XL']]);
    expect(m.included.find(i => i.key === 'gitea_iac')?.value).toBe('read');
    expect(m.included.find(i => i.key === 'bandwidth')?.value).toBe('100 Mbps · hard cap');
  });

  it('for S: the teaser names its included_from (M), the not-offered its first rung', () => {
    const s = addonsLadderFor(doc, 'plan.s')!;
    const up = Object.fromEntries(s.missing.map(x => [x.key, x.upgrade?.name]));
    expect(up).toEqual({
      gitea_iac: 'M', kube_api: 'XL', vuln_dashboard: 'M', compliance: 'L',
      audit_log: 'M', cost_explorer: 'M', patching: 'L', maintenance: 'XL',
    });
  });

  it('for XL: nothing is missing, Backup is in the package, Dedicated IP is still an add-on', () => {
    const xl = addonsLadderFor(doc, 'plan.xl')!;
    expect(xl.missing).toEqual([]);
    expect(xl.included.some(i => i.key === 'backup')).toBe(true);
    expect(xl.choices.map(c => c.id)).toEqual(['addon.dedicated_ip']);
  });

  it('an unknown sku yields null', () => {
    expect(addonsLadderFor(doc, 'plan.xxl')).toBeNull();
  });

  it('the offer list is the choices in the AddOn shape — BSS add-ons only, no catalog entry and no catalog price', () => {
    const l = addonsLadderFor(doc, 'plan.l')!;
    expect(l.addons.map(a => a.id)).toEqual(l.choices.map(c => c.id));
    expect(l.addons[0]).toMatchObject({ id: 'addon.backup', slug: 'backup', name: 'Backup', monthly_price: 1500, included: false, hint: 'Included from XL' });
    expect(l.addons.every(a => a.id.startsWith('addon.'))).toBe(true);
  });

  it('a stale catalog id in the cart is carried over to its BSS twin where the package sells it, else dropped — the floor counts as a twin too', () => {
    // Daily Backup → addon.backup on L; on XL (included) it is dropped.
    expect(pruneAddonsForPackage(doc, 'plan.l', ['a-backup'], CATALOG_ADDONS)).toEqual(['addon.backup']);
    expect(pruneAddonsForPackage(doc, 'plan.xl', ['a-backup'], CATALOG_ADDONS)).toEqual([]);
    // The WAF and 24/7 support are FLOOR items — every package includes them,
    // nothing is sold, so the catalog entries are dropped, not offered.
    expect(twinFeatureKey(CATALOG_ADDONS[1], doc.features, doc.floor)).toBe('waf');
    expect(twinFeatureKey(CATALOG_ADDONS[3], doc.features, doc.floor)).toBe('support');
    expect(twinFeatureKey(CATALOG_ADDONS[1], doc.features)).toBeNull();
    expect(pruneAddonsForPackage(doc, 'plan.l', ['a-waf', 'a-support', 'a-ips'], CATALOG_ADDONS)).toEqual([]);
  });

  it('the fixture blurbs carry no number — every figure in the journey is the price book\'s', () => {
    // "IPv4" is a protocol name, not a figure.
    const texts = [...doc.features.map(f => f.blurb ?? ''), ...(doc.floor ?? []).map(f => f.blurb ?? '')];
    expect(texts.length).toBeGreaterThan(20);
    expect(texts.filter(t => /\d/.test(t.replace(/IPv[46]/g, '')))).toEqual([]);
  });

  it('a note on an OPTIONAL cell is BSS-internal and reaches neither the ladder cell nor the add-on tile', () => {
    // The live document carries a pricing remark on the Dedicated IP cells.
    // Only an ACCESS cell's note is customer copy (the "read" qualifier);
    // nothing else may put it on the page.
    const raw = fixture('public-packages-v2.json') as any;
    const ip = raw.features.find((f: any) => f.key === 'dedicated_ip');
    for (const c of Object.values<any>(ip.cells)) c.note = 'list price 250 OMR/yr; discount to be decided';
    const d = parsePublicPackages(raw)!;
    const row = buildLadder(d).groups.flatMap(g => g.rows).find(r => r.key === 'dedicated_ip')!;
    for (const c of row.cells) {
      expect(c.label).toBe('+ 20.833 / mo');
      expect(c.hint).toBeNull();
      expect(JSON.stringify(c)).not.toMatch(/discount|250/);
    }
    for (const sku of ['plan.s', 'plan.m', 'plan.l', 'plan.xl']) {
      const l = addonsLadderFor(d, sku)!;
      expect(JSON.stringify([l.choices, l.addons, l.included, l.missing])).not.toMatch(/discount|250 OMR/);
    }
  });

  it('funnelAddonsFor delegates to the ladder for a v2 document, so Review resolves the same ids', () => {
    const l = addonsLadderFor(doc, 'plan.l')!;
    const f = funnelAddonsFor(doc, 'plan.l', CATALOG_ADDONS);
    expect(f.addons.map(a => a.id)).toEqual(l.addons.map(a => a.id));
    expect(f.packageName).toBe('L');
    expect(f.included.find(i => i.key === 'dr_topology')?.blurb).toBe('single region');
  });

  it('a level cell with a next-level add-on becomes a choice that buys the next level', () => {
    const raw = fixture('public-packages-v2.json') as any;
    raw.features.push({
      key: 'backups_level', name: 'Backups', group: 'resilience', kind: 'level',
      levels: ['weekly · 7 days', 'daily · 14 days', 'daily · 30 days'], addon_sku: 'addon.backup_daily',
      cells: {
        'plan.s': { state: 'included', level: 0, next_level_addon: { addon_sku: 'addon.backup_daily', price_month: '1.500' } },
        'plan.m': { state: 'included', level: 1 },
        'plan.l': { state: 'included', level: 2 },
        'plan.xl': { state: 'included', level: 2 },
      },
    });
    const d = parsePublicPackages(raw)!;
    const s = addonsLadderFor(d, 'plan.s')!;
    const choice = s.choices.find(c => c.id === 'addon.backup_daily')!;
    expect(choice).toMatchObject({
      featureKey: 'backups_level', name: 'Backups · daily · 14 days', blurb: 'Upgrade from weekly · 7 days',
      priceMonth: '1.500', priceBaisa: 1500, includedFrom: 'M', targetLevel: 1,
    });
    expect(s.included.find(i => i.key === 'backups_level')?.value).toBe('weekly · 7 days');
    // M includes level 1, so it bundles this choice; the sku is purchasable on S only.
    expect(bundledOn(d, 'plan.m', choice)).toBe(true);
    expect(purchasableAddonSkus(d, 'plan.s').has('addon.backup_daily')).toBe(true);
    expect(purchasableAddonSkus(d, 'plan.m').has('addon.backup_daily')).toBe(false);
    expect(bssAddonSkus(d).has('addon.backup_daily')).toBe(true);
    expect(bssAddonSkus(d).has('eip.bandwidth_mbps')).toBe(true);
  });

  it('a not-offered feature that a larger package only sells as an add-on points at that rung with its price', () => {
    const raw = fixture('public-packages-v2.json') as any;
    raw.features.push({
      key: 'gpu', name: 'GPU slice', group: 'capacity', kind: 'boolean',
      cells: {
        'plan.s': { state: 'not_offered' }, 'plan.m': { state: 'not_offered' },
        'plan.l': { state: 'optional', addon_sku: 'addon.gpu', price_month: '9.000' }, 'plan.xl': { state: 'included' },
      },
    });
    const d = parsePublicPackages(raw)!;
    const m = addonsLadderFor(d, 'plan.m')!;
    expect(m.missing.find(x => x.key === 'gpu')?.upgrade).toEqual({ sku: 'plan.l', name: 'L', state: 'optional', priceMonth: '9.000' });
  });
});

describe('the step-up hint', () => {
  const doc = v2();
  const onL = addonsLadderFor(doc, 'plan.l')!;

  it('fires on L when Backup + AI SEO + AI builder + Domain are ticked: 6.000 ≥ the 6.000 gap to XL', () => {
    const h = stepUpHint(doc, onL, ['addon.backup', 'addon.ai_seo', 'addon.ai_builder', 'addon.domain'])!;
    expect(h).not.toBeNull();
    expect(h.nextSku).toBe('plan.xl');
    expect(h.nextName).toBe('XL');
    expect(h.gapMonth).toBe('6.000');
    expect(h.gapBaisa).toBe(6000);
    expect(h.bundledSumBaisa).toBe(6000);
    expect(h.bundledSumMonth).toBe('6.000');
    expect(h.bundled.map(b => b.name)).toEqual(['Backup', 'AI SEO ready', 'AI website builder', 'Domain']);
  });

  it('is silent one add-on short (5.500 < 6.000)', () => {
    expect(stepUpHint(doc, onL, ['addon.backup', 'addon.ai_seo', 'addon.ai_builder'])).toBeNull();
  });

  it('counts only what the next rung includes — Dedicated IP, optional on XL too, neither triggers nor is listed', () => {
    expect(stepUpHint(doc, onL, ['addon.dedicated_ip', 'addon.backup', 'addon.ai_seo'])).toBeNull();
    const h = stepUpHint(doc, onL, ['addon.dedicated_ip', 'addon.backup', 'addon.ai_seo', 'addon.ai_builder', 'addon.domain'])!;
    expect(h.bundled.map(b => b.id)).not.toContain('addon.dedicated_ip');
    expect(h.bundledSumBaisa).toBe(6000);
  });

  it('a catalog add-on in the cart is not a BSS choice and never counts', () => {
    expect(stepUpHint(doc, onL, ['a-ips', 'addon.backup', 'addon.ai_seo', 'addon.ai_builder'])).toBeNull();
  });

  it('never fires on S or M (their next rung bundles nothing) nor on XL (no next rung)', () => {
    const onS = addonsLadderFor(doc, 'plan.s')!;
    const onM = addonsLadderFor(doc, 'plan.m')!;
    const onXL = addonsLadderFor(doc, 'plan.xl')!;
    const all = ['addon.backup', 'addon.ai_seo', 'addon.ai_builder', 'addon.domain', 'addon.dedicated_ip'];
    expect(stepUpHint(doc, onS, all)).toBeNull();
    expect(stepUpHint(doc, onM, all)).toBeNull();
    expect(stepUpHint(doc, onXL, all)).toBeNull();
  });

  it("agrees with the document's own bundled_addon_keys for every rung", () => {
    for (const p of doc.packages) {
      if (!p.step_up) continue;
      const l = addonsLadderFor(doc, p.sku)!;
      const bundledKeys = l.choices.filter(c => bundledOn(doc, p.step_up!.next_sku, c)).map(c => c.featureKey);
      expect(bundledKeys, p.sku).toEqual(p.step_up.bundled_addon_keys);
    }
  });

  it('moneyString is the inverse of minorUnits', () => {
    expect(moneyString(6000)).toBe('6.000');
    expect(moneyString(20833)).toBe('20.833');
    expect(moneyString(0)).toBe('0.000');
  });
});

// ---------------------------------------------------------------------------
// 4. The cart bridge on a switch.
// ---------------------------------------------------------------------------

describe('switching the package prunes what the new rung bundles and keeps the rest', () => {
  const doc = v2();

  it('L → XL drops the four XL includes and the catalog id (not in the journey), keeps Dedicated IP', () => {
    const before = ['a-ips', 'addon.backup', 'addon.ai_seo', 'addon.ai_builder', 'addon.domain', 'addon.dedicated_ip'];
    expect(pruneAddonsForPackage(doc, 'plan.xl', before, CATALOG_ADDONS)).toEqual(['addon.dedicated_ip']);
  });

  it('M → L keeps every add-on (L sells the same five)', () => {
    const before = ['addon.backup', 'addon.dedicated_ip'];
    expect(pruneAddonsForPackage(doc, 'plan.l', before)).toEqual(before);
  });

  it('the v1 fixture: Backup dropped on XL; a catalog id with no twin is not in the journey', () => {
    expect(pruneAddonsForPackage(v1(), 'plan.xl', ['ips', 'addon.backup'])).toEqual([]);
    expect(pruneAddonsForPackage(v1(), 'plan.l', ['ips', 'addon.backup'])).toEqual(['addon.backup']);
  });
});

// ---------------------------------------------------------------------------
// 5. Step 4 — the topology the package's DR level allows.
// ---------------------------------------------------------------------------

describe('the topology step reads the DR level', () => {
  const doc = v2();

  it('S, M and L are single region: hot-standby is locked, included from XL', () => {
    for (const sku of ['plan.s', 'plan.m', 'plan.l']) {
      const t = drTopologyFor(doc, sku)!;
      expect(t, sku).toEqual({ level: 0, label: 'single region', activePassive: false, activePassiveFrom: { sku: 'plan.xl', name: 'XL' } });
    }
  });

  it('XL includes active-passive', () => {
    expect(drTopologyFor(doc, 'plan.xl')).toEqual({ level: 1, label: 'active-passive', activePassive: true, activePassiveFrom: null });
  });

  it('a document with no DR level feature (the v1 fixture) gates nothing', () => {
    expect(drTopologyFor(v1(), 'plan.s')).toBeNull();
    expect(drTopologyFor(doc, 'plan.nope')).toBeNull();
  });

  it('finds the level feature by its "active-passive" level when the key differs, deriving the rung when none is stated', () => {
    const raw = fixture('public-packages-v2.json') as any;
    const f = raw.features.find((x: any) => x.key === 'dr_topology');
    f.key = 'resilience_mode';
    f.levels = ['one region', 'active passive · two regions', 'active active'];
    f.cells['plan.l'].level = 1;
    for (const c of Object.values<any>(f.cells)) delete c.included_from;
    const d = parsePublicPackages(raw)!;
    expect(drTopologyFor(d, 'plan.m')).toMatchObject({ activePassive: false, activePassiveFrom: { sku: 'plan.l', name: 'L' } });
    expect(drTopologyFor(d, 'plan.l')).toMatchObject({ activePassive: true, label: 'active passive · two regions' });
  });

  it("the cell's stated included_from wins when it names a package that has the level; a stale one falls back to the derivation", () => {
    const raw = fixture('public-packages-v2.json') as any;
    const f = raw.features.find((x: any) => x.key === 'dr_topology');
    f.cells['plan.l'].level = 1; // L now has it too, but the document says XL
    expect(drTopologyFor(parsePublicPackages(raw)!, 'plan.m')!.activePassiveFrom).toEqual({ sku: 'plan.xl', name: 'XL' });
    f.cells['plan.m'].included_from = 'plan.s'; // names a rung without the level
    expect(drTopologyFor(parsePublicPackages(raw)!, 'plan.m')!.activePassiveFrom).toEqual({ sku: 'plan.l', name: 'L' });
  });
});

// ---------------------------------------------------------------------------
// 6. Review — one shape source for the cards, the headroom ring and the hint.
// ---------------------------------------------------------------------------

describe('the review step sizes from the document shape', () => {
  const doc = v2();

  it('capacity and specs come from the v2 shape', () => {
    const l = doc.packages.find(p => p.sku === 'plan.l')!;
    expect(packageCapacity(l)).toEqual({ ram: 8192, cpu: 4000, disk: 100 });
    expect(packageSpecsLine(l)).toBe('4 vCPU · 8 GB · 100 GB');
    expect(packageCapacity(doc.packages[0])).toEqual({ ram: 2048, cpu: 1000, disk: 25 });
    expect(packageSpecsLine(doc.packages[3])).toBe('8 vCPU · 16 GB · 250 GB');
  });

  it('falls back to the v1 includes, and to null when a package publishes neither', () => {
    const s = v1().packages[0];
    expect(packageCapacity(s)).toEqual({ ram: 2048, cpu: 1000, disk: 25 });
    expect(packageSpecsLine(s)).toBe('1 vCPU · 2 GB · 25 GB');
    expect(packageCapacity({ sku: 'plan.x', name: 'X', price_month: '1.000', includes: {} })).toBeNull();
    expect(packageSpecsLine({ sku: 'plan.x', name: 'X', price_month: '1.000', includes: {} })).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// Markup contract.
// ---------------------------------------------------------------------------

describe('ladder markup contract', () => {
  const page = readFileSync(join(ROOT, 'src', 'pages', 'plans.astro'), 'utf8');
  const table = readFileSync(join(ROOT, 'src', 'components', 'PackageTable.svelte'), 'utf8');
  const ladder = readFileSync(join(ROOT, 'src', 'components', 'PackageLadder.svelte'), 'utf8');
  const css = readFileSync(join(ROOT, 'src', 'styles', 'package-ladder.css'), 'utf8');
  const addonsStep = readFileSync(join(ROOT, 'src', 'components', 'AddonsStep.svelte'), 'utf8');
  const review = readFileSync(join(ROOT, 'src', 'components', 'ReviewStep.svelte'), 'utf8');

  it("/plans imports the ladder's stylesheet as a PAGE stylesheet, and the component carries no <style> of its own", () => {
    // The ladder is never part of the server render, so a component-scoped
    // stylesheet would be dropped from the production bundle with the
    // unreachable branch (the 2026-10-10 regression). The page import is
    // unconditional; package-ladder.spec.ts asserts the computed layout.
    expect(page).toMatch(/import ['"]\.\.\/styles\/package-ladder\.css['"]/);
    expect(ladder).not.toMatch(/^\s*<style[\s>]/m);
    expect(css).toMatch(/\.ld-grid\s*\{[^}]*display:\s*grid/);
    expect(css).toMatch(/\.ld-card\s*\{[^}]*display:\s*flex/);
  });

  it('PackageTable mounts the ladder for a v2 document and still falls back to the deck first', () => {
    expect(table).toMatch(/import PackageLadder from ['"]\.\/PackageLadder\.svelte['"]/);
    expect(table).toMatch(/isLadderDocument\(/);
    expect(table).toMatch(/<PackageLadder\s/);
    expect(table).toMatch(/<PlanStep\s*\/>/);
    // The deck is the server render: no branch may precede it with a truthy
    // initial state, and the ladder branch is keyed on a state the server
    // never holds.
    expect(table).toMatch(/\$state<'deck' \| 'table' \| 'ladder'>\('deck'\)/);
  });

  it('the ladder picks nothing: no checkbox, no add-on toggle; every row comes from the document', () => {
    expect(ladder).not.toMatch(/type="checkbox"/);
    expect(ladder).not.toMatch(/toggleAddon/);
    expect(ladder).not.toMatch(/capsByName|capRows/);
    expect(ladder).toMatch(/data-testid="package-floor"/);
    expect(ladder).toMatch(/data-testid="package-group-\{group\.key\}"/);
  });

  it('the ladder has no floating bar — the per-column Choose, top and foot, is the only CTA, so nothing fixed covers a cell', () => {
    expect(ladder).not.toMatch(/ld-nav|float-nav|position:\s*fixed/);
    expect(css).not.toMatch(/position:\s*fixed/);
    expect(ladder).toMatch(/data-testid="package-ladder-foot"/);
    expect(ladder).toMatch(/data-testid="package-choose-foot-\{card\.sku\}"/);
  });

  it('the Add-ons and Topology steps use a fixed bottom bar with the page padded by its height and scroll-padding to match', () => {
    const bcp = readFileSync(join(ROOT, 'src', 'components', 'BCPStep.svelte'), 'utf8');
    for (const [name, src] of [['AddonsStep', addonsStep], ['BCPStep', bcp]] as const) {
      expect(src, name).toMatch(/data-testid="step-bar"/);
      expect(src, name).not.toMatch(/class="float-nav"/);
      expect(src, name).toMatch(/--step-bar-h:\s*4\.5rem/);
      expect(src, name).toMatch(/padding:\s*0 1\.25rem calc\(var\(--step-bar-h\) \+ env\(safe-area-inset-bottom, 0px\)\)/);
      expect(src, name).toMatch(/:global\(html\)\s*\{\s*scroll-padding-bottom:\s*calc\(4\.5rem \+ env\(safe-area-inset-bottom, 0px\)\)/);
      expect(src, name).toMatch(/\.step-bar\s*\{[^}]*position:\s*fixed[^}]*bottom:\s*0/);
      expect(src, name).toMatch(/env\(safe-area-inset-bottom, 0px\)\);\s*\n\s*background/);
    }
  });

  it('the Add-ons step renders the three blocks, the step-up card and the running total, switches through setPackage, and offers no priced catalog add-on', () => {
    expect(addonsStep).toMatch(/addonsLadderFor\(/);
    expect(addonsStep).toMatch(/stepUpHint\(/);
    expect(addonsStep).toMatch(/pruneAddonsForPackage\(/);
    expect(addonsStep).toMatch(/data-testid="addons-included"/);
    expect(addonsStep).toMatch(/data-testid="addons-optional"/);
    expect(addonsStep).toMatch(/data-testid="addons-missing"/);
    expect(addonsStep).toMatch(/data-testid="addons-stepup"/);
    expect(addonsStep).toMatch(/data-testid="addons-stepup-switch"/);
    expect(addonsStep).toMatch(/data-testid="addons-running-total"/);
    expect(addonsStep).toMatch(/setPackage\(\{/);
    // The no-catalog stand-in list carries no price: only a BSS add-on shows one.
    const fallback = addonsStep.match(/const FALLBACK_ADDONS[\s\S]*?\];/)![0];
    expect(fallback).not.toMatch(/monthly_price:\s*[1-9]/);
    expect(fallback).not.toMatch(/priority-support/);
  });

  it('the Topology step gates hot-standby on the DR level and switches through setPackage', () => {
    const bcp = readFileSync(join(ROOT, 'src', 'components', 'BCPStep.svelte'), 'utf8');
    expect(bcp).toMatch(/drTopologyFor\(/);
    expect(bcp).toMatch(/data-testid="topology-card-hot"/);
    expect(bcp).toMatch(/data-locked=\{?"?true/);
    expect(bcp).toMatch(/data-testid="topology-switch-\{dr\.activePassiveFrom\.sku\}"/);
    expect(bcp).toMatch(/setPackage\(\{/);
    expect(bcp).toMatch(/pruneAddonsForPackage\(/);
  });

  it('Review sizes the cards, the ring and the hint from one shape source, labels the usage buckets without user counts, and carries the floor footnote', () => {
    expect(review).toMatch(/data-testid="review-floor"/);
    expect(review).toMatch(/\bgetQuote\b/);
    expect(review).toMatch(/packageCapacity\(/);
    expect(review).toMatch(/packageSpecsLine\(/);
    expect(review).toMatch(/const planCap = \$derived\(selectedPlan \? capFor\(selectedPlan\)/);
    expect(review).toMatch(/capFor\(plan\)/);
    expect(review).not.toMatch(/range:/);
    expect(review).not.toMatch(/\d+–\d+ users/);
  });
});
