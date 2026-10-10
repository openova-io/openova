// packagesGrow.test.ts — #6971: what happens when the Organization reaches its
// package. A package is a prepaid monthly commitment whose shape is an
// allowance; the customer keeps it CAPPED (the default — the bill never
// exceeds the package + add-ons) or lets it GROW up to a ceiling, the usage
// above the allowance billed after the month at the document's overage rates.
//
// Fed from fixtures/public-packages-v4-grow.json — the v3 document plus
// `grow` on all four packages, each with its OWN `grow.overage_rates` (bigger
// packages grow cheaper: vCPU 1.992 / 1.796 / 1.598 / 1.399; XL's ceiling is
// its own headline, so XL cannot grow) and DR `grow_only` cells on S / M / L.
//
//   1. parsing: rates, a package's grow and a grow_only cell are validated at
//      the boundary; v2 / v3 documents carry none of them;
//   2. the grow model: steppers only where there is a rate and room, bounds,
//      the step grid, ceiling clamping, the spend limit;
//   3. the "the next package is the better deal" arithmetic, exact to the baisa;
//   4. DR: grow_only on S / M / L; the order body and the document fallback
//      quote carry the mode.

import { beforeEach, describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import {
  addonsLadderFor,
  buildLadder,
  hasGrowModel,
  cheapestGrowVcpu,
  clampGrowCeiling,
  drTopologyFor,
  growModelFor,
  growSelectionFor,
  growUpgradeHint,
  isPackageCeiling,
  normalizeSpendLimit,
  packageAllowance,
  parseOverageRates,
  parsePublicPackages,
  stepGrow,
  type PublicPackages,
} from './packages';
import { documentQuote, overageFieldsFor, overageSummary, quoteRequestFor } from './quote';
import { readCart, setOverage, setPackage, writeCart, type CartState } from './cart';
import { formatOMR } from './currency';

const FIX = join(__dirname, '..', '..', 'fixtures');
const DOC_URL = 'https://chargeback.t99.omani.works/api/v1/public/packages';
const RAW_V4 = JSON.parse(readFileSync(join(FIX, 'public-packages-v4-grow.json'), 'utf8'));
const RAW_V3 = JSON.parse(readFileSync(join(FIX, 'public-packages-v3-icons.json'), 'utf8'));
const RAW_V2 = JSON.parse(readFileSync(join(FIX, 'public-packages-v2.json'), 'utf8'));

function doc(raw: unknown = RAW_V4): PublicPackages {
  const d = parsePublicPackages(raw, DOC_URL);
  if (!d) throw new Error('fixture did not parse');
  return d;
}
function clone<T>(v: T): T {
  return JSON.parse(JSON.stringify(v));
}

// ---------------------------------------------------------------------------
// 1. Parsing
// ---------------------------------------------------------------------------

describe('the grow additions are parsed at the boundary (#6971)', () => {
  it('reads every package grow with its own overage_rates and the S/M/L grow_only DR cells from the v4 fixture', () => {
    const d = doc();
    expect(d.packages[0].grow!.overage_rates).toEqual([
      { key: 'vcpu', sku: 'k8s.vcpu', unit: 'vCPU', price_month: '1.992' },
      { key: 'memory', sku: 'k8s.memory_gb', unit: 'GB', price_month: '0.374' },
      { key: 'disk', sku: 'k8s.pvc_gb', unit: 'GB', price_month: '0.035' },
      { key: 'bandwidth', sku: 'eip.bandwidth_mbps', unit: 'Mbps', price_month: '1.253' },
    ]);
    expect(d.packages.map(p => p.grow!.overage_rates[0].price_month)).toEqual(['1.992', '1.796', '1.598', '1.399']);
    // No top-level rates in the contract: a stray one is not read.
    expect((d as any).overage_rates).toBeUndefined();
    expect(d.packages.map(p => p.grow?.ceiling)).toEqual([
      { vcpu: 2, memory_gb: 4, disk_gb: 50, bandwidth_mbps: 100 },
      { vcpu: 4, memory_gb: 8, disk_gb: 100, bandwidth_mbps: 250 },
      { vcpu: 8, memory_gb: 16, disk_gb: 250, bandwidth_mbps: 1000 },
      { vcpu: 8, memory_gb: 16, disk_gb: 250, bandwidth_mbps: 1000 },
    ]);
    const dr = d.features.find(f => f.key === 'dr_topology')!;
    for (const sku of ['plan.s', 'plan.m', 'plan.l']) expect(dr.cells[sku]).toMatchObject({ state: 'optional', grow_only: true });
    expect(dr.cells['plan.xl'].grow_only).toBeUndefined();
  });

  it('v2 and v3 documents carry no grow, no rates and no grow_only cell — and the ladder no grow line', () => {
    for (const raw of [RAW_V2, RAW_V3]) {
      const d = doc(raw);
      expect(d.packages.every(p => p.grow === undefined)).toBe(true);
      expect(d.features.every(f => Object.values(f.cells).every(c => c.grow_only === undefined))).toBe(true);
      expect(buildLadder(d).growNote).toBe(false);
      expect(buildLadder(d).growCheapest).toBeNull();
      for (const p of d.packages) expect(growModelFor(d, p.sku)).toBeNull();
    }
    expect(buildLadder(doc()).growNote).toBe(true);
  });

  it('"bigger packages grow cheaper" names the lowest vCPU rate among packages that CAN grow (L 1.598 — XL cannot grow here); equal rates say nothing', () => {
    expect(cheapestGrowVcpu(doc())).toEqual({ sku: 'plan.l', name: 'L', priceMonth: '1.598' });
    const raw = clone(RAW_V4);
    raw.packages[3].grow.ceiling = { vcpu: 16, memory_gb: 32, disk_gb: 500, bandwidth_mbps: 2000 };
    expect(cheapestGrowVcpu(doc(raw))).toEqual({ sku: 'plan.xl', name: 'XL', priceMonth: '1.399' });
    for (const p of raw.packages) p.grow.overage_rates[0].price_month = '1.500';
    expect(cheapestGrowVcpu(doc(raw))).toBeNull();
  });

  it('drops a rate with an unknown key or unit, a non-money price, or a duplicate key', () => {
    expect(parseOverageRates([
      { key: 'gpu', sku: 'x', unit: 'vCPU', price_month: '1.000' },
      { key: 'vcpu', sku: 'k8s.vcpu', unit: 'cores', price_month: '1.000' },
      { key: 'vcpu', sku: 'k8s.vcpu', unit: 'vCPU', price_month: 'free' },
      { key: 'vcpu', sku: 'k8s.vcpu', unit: 'vCPU', price_month: '-1.000' },
      { key: 'memory', sku: 'k8s.memory_gb', unit: 'GB', price_month: '0.374' },
      { key: 'memory', sku: 'other', unit: 'GB', price_month: '9.000' },
      'not an object',
    ])).toEqual([{ key: 'memory', sku: 'k8s.memory_gb', unit: 'GB', price_month: '0.374' }]);
    expect(parseOverageRates(null)).toEqual([]);
  });

  it('drops a grow that is not allowed or whose ceiling is partial or negative; grow_only only on an optional cell', () => {
    const raw = clone(RAW_V4);
    raw.packages[0].grow = { allowed: false, ceiling: raw.packages[0].grow.ceiling };
    raw.packages[1].grow = { allowed: true, ceiling: { vcpu: 4, memory_gb: 8, disk_gb: 100 } };
    raw.packages[2].grow = { allowed: true, ceiling: { vcpu: -1, memory_gb: 8, disk_gb: 100, bandwidth_mbps: 250 } };
    const dr = raw.features.find((f: any) => f.key === 'dr_topology');
    dr.cells['plan.xl'] = { state: 'included', level: 1, grow_only: true };
    const d = doc(raw);
    expect(d.packages[0].grow).toBeUndefined();
    expect(d.packages[1].grow).toBeUndefined();
    expect(d.packages[2].grow).toBeUndefined();
    expect(d.packages[3].grow).toBeDefined();
    expect(d.features.find(f => f.key === 'dr_topology')!.cells['plan.xl'].grow_only).toBeUndefined();
  });
});

// ---------------------------------------------------------------------------
// 2. The grow model
// ---------------------------------------------------------------------------

describe('the grow model: steppers from the document, bounded by allowance and ceiling (#6971)', () => {
  it('M: four steppers from its allowance (2 / 4 / 50 / 100) to its ceiling (4 / 8 / 100 / 250), each with its rate', () => {
    const m = growModelFor(doc(), 'plan.m')!;
    expect(m.packageName).toBe('M');
    expect(m.dims.map(d => [d.key, d.allowance, d.max, d.rate.price_month])).toEqual([
      ['vcpu', 2, 4, '1.796'],
      ['memory_gb', 4, 8, '0.337'],
      ['disk_gb', 50, 100, '0.035'],
      ['bandwidth_mbps', 100, 250, '1.253'],
    ]);
    expect(m.unlocksDr).toBe(true);
  });

  it('XL cannot grow (its ceiling is its allowance): no model, and the selection is capped whatever the cart says', () => {
    const d = doc();
    expect(growModelFor(d, 'plan.xl')).toBeNull();
    expect(growSelectionFor(d, 'plan.xl', { overageMode: 'grow', growCeiling: null, spendLimitMonth: '25.000' }))
      .toEqual({ mode: 'capped', ceiling: null, spendLimit: null });
  });

  it('a dimension without a rate gets no stepper; without any rate there is no model', () => {
    const raw = clone(RAW_V4);
    const m = raw.packages[1].grow;
    m.overage_rates = m.overage_rates.filter((r: any) => r.key !== 'disk');
    expect(growModelFor(doc(raw), 'plan.m')!.dims.map(d => d.key)).toEqual(['vcpu', 'memory_gb', 'bandwidth_mbps']);
    delete m.overage_rates;
    expect(growModelFor(doc(raw), 'plan.m')).toBeNull();
  });

  it('the bandwidth allowance falls back to the bandwidth feature cell when includes has none', () => {
    const raw = clone(RAW_V4);
    delete raw.packages[1].includes.bandwidth_mbps;
    const d = doc(raw);
    expect(packageAllowance(d, d.packages[1]).bandwidth_mbps).toBe(100);
  });

  it('stepGrow walks the step grid from the allowance and never leaves [allowance, max]', () => {
    const disk = { allowance: 50, max: 100, step: 10 };
    expect(stepGrow(disk, 50, 1)).toBe(60);
    expect(stepGrow(disk, 95, 1)).toBe(100);
    expect(stepGrow(disk, 100, 1)).toBe(100);
    expect(stepGrow(disk, 100, -1)).toBe(90);
    expect(stepGrow(disk, 55, -1)).toBe(50);
    expect(stepGrow(disk, 50, -1)).toBe(50);
    const vcpu = { allowance: 2, max: 4, step: 1 };
    expect(stepGrow(vcpu, 4, 1)).toBe(4);
    expect(stepGrow(vcpu, 3, -1)).toBe(2);
  });

  it('clampGrowCeiling bounds every dimension and isPackageCeiling spots the package\'s own', () => {
    const m = growModelFor(doc(), 'plan.m')!;
    const c = clampGrowCeiling(m, { vcpu: 99, memory_gb: 1, disk_gb: 70 });
    expect(c).toEqual({ vcpu: 4, memory_gb: 4, disk_gb: 70, bandwidth_mbps: 250 });
    expect(isPackageCeiling(m, c)).toBe(false);
    expect(isPackageCeiling(m, clampGrowCeiling(m, null))).toBe(true);
  });

  it('normalizeSpendLimit: minor-unit string, empty = no limit, anything else invalid', () => {
    expect(normalizeSpendLimit('25')).toBe('25.000');
    expect(normalizeSpendLimit(' 25.5 ')).toBe('25.500');
    expect(normalizeSpendLimit('25.125')).toBe('25.125');
    expect(normalizeSpendLimit('')).toBeNull();
    for (const bad of ['0', '0.000', '-5', '25.1234', 'abc', '1e3', '25,5']) expect(normalizeSpendLimit(bad)).toBeUndefined();
  });

  it('growSelectionFor: grow with a custom ceiling and a limit; the package\'s own ceiling is omitted; an invalid limit is dropped', () => {
    const d = doc();
    expect(growSelectionFor(d, 'plan.m', { overageMode: 'grow', growCeiling: { vcpu: 3, memory_gb: 6, disk_gb: 100, bandwidth_mbps: 250 }, spendLimitMonth: '25.000' }))
      .toEqual({ mode: 'grow', ceiling: { vcpu: 3, memory_gb: 6, disk_gb: 100, bandwidth_mbps: 250 }, spendLimit: '25.000' });
    expect(growSelectionFor(d, 'plan.m', { overageMode: 'grow', growCeiling: null, spendLimitMonth: 'x' }))
      .toEqual({ mode: 'grow', ceiling: null, spendLimit: null });
    expect(growSelectionFor(d, 'plan.m', { overageMode: 'capped', growCeiling: null, spendLimitMonth: '25.000' }))
      .toEqual({ mode: 'capped', ceiling: null, spendLimit: null });
  });
});

// ---------------------------------------------------------------------------
// 3. "The next package is the better deal"
// ---------------------------------------------------------------------------

describe('growUpgradeHint: the package + its own overage for the next package\'s headline vs the next package (#6971)', () => {
  it('M grown to L\'s headline at M\'s rates: 2 vCPU × 1.796 + 4 GB × 0.337 = 4.940 → 9.430 > L 7.990', () => {
    const h = growUpgradeHint(doc(), 'plan.m')!;
    expect(h.nextSku).toBe('plan.l');
    expect(h.overageBaisa).toBe(2 * 1796 + 4 * 337);
    expect(h.grownBaisa).toBe(4490 + 4940);
    expect(h.nextPriceBaisa).toBe(7990);
    // The headline (vCPU, memory) only — disk and bandwidth are not what "L's size" names.
    expect(h.deltas.map(x => [x.key, x.delta])).toEqual([['vcpu', 2], ['memory_gb', 4]]);
  });

  it('S at S\'s rates: 1 × 1.992 + 2 × 0.374 = 2.740 → 5.230 > M 4.490; L at L\'s rates: 4 × 1.598 + 8 × 0.300 → 16.782 > XL 13.990', () => {
    expect(growUpgradeHint(doc(), 'plan.s')!.grownBaisa).toBe(2490 + 1992 + 748);
    expect(growUpgradeHint(doc(), 'plan.l')!.grownBaisa).toBe(7990 + 4 * 1598 + 8 * 300);
  });

  it('follows the PACKAGE\'s own rates: change S\'s vCPU rate and the sum follows; cut it far enough and the hint goes', () => {
    const raw = clone(RAW_V4);
    raw.packages[0].grow.overage_rates[0].price_month = '2.500';
    expect(growUpgradeHint(doc(raw), 'plan.s')!.overageBaisa).toBe(2500 + 748);
    // M's rates are untouched by S's change.
    expect(growUpgradeHint(doc(raw), 'plan.m')!.overageBaisa).toBe(4940);
    raw.packages[0].grow.overage_rates[0].price_month = '1.000';
    // S + 1 vCPU + 2 GB = 2.490 + 1.748 = 4.238 < M 4.490 → no claim.
    expect(growUpgradeHint(doc(raw), 'plan.s')).toBeNull();
  });

  it('none on the last package, none without rates or without grow', () => {
    expect(growUpgradeHint(doc(), 'plan.xl')).toBeNull();
    expect(growUpgradeHint(doc(RAW_V3), 'plan.m')).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// 4. DR and the order body
// ---------------------------------------------------------------------------

describe('DR grow_only and the order body (#6971)', () => {
  beforeEach(() => localStorage.clear());

  it('drTopologyFor: S/M/L read growOnly (not included, XL named), XL includes it; a v3 document has no growOnly', () => {
    const d = doc();
    for (const sku of ['plan.s', 'plan.m', 'plan.l']) {
      expect(drTopologyFor(d, sku)).toMatchObject({ activePassive: false, growOnly: true, activePassiveFrom: { sku: 'plan.xl', name: 'XL' } });
    }
    expect(drTopologyFor(d, 'plan.xl')).toMatchObject({ activePassive: true, growOnly: false });
    expect(drTopologyFor(doc(RAW_V3), 'plan.m')).toMatchObject({ activePassive: false, growOnly: false });
  });

  it('the /plans ladder reads a grow_only cell "with Grow · billed as usage"', () => {
    const ladder = buildLadder(doc());
    const row = ladder.groups.flatMap(g => g.rows).find(r => r.key === 'dr_topology')!;
    expect(row.cells.slice(0, 3).map(c => [c.label, c.hint])).toEqual([
      ['with Grow', 'billed as usage'], ['with Grow', 'billed as usage'], ['with Grow', 'billed as usage'],
    ]);
    expect(row.cells[3].label).toBe('active-passive');
  });

  it('the quote body: capped by default; grow with ceiling and limit when set; a package switch drops the ceiling', () => {
    setPackage({ planId: 'm', planName: 'M', packageSku: 'plan.m' });
    expect(quoteRequestFor(readCart()).overage_mode).toBe('capped');
    expect(quoteRequestFor(readCart()).grow_ceiling).toBeUndefined();
    setOverage({ mode: 'grow', growCeiling: { vcpu: 3, memory_gb: 8, disk_gb: 100, bandwidth_mbps: 250 }, spendLimitMonth: '25.000' });
    expect(quoteRequestFor(readCart())).toMatchObject({
      overage_mode: 'grow',
      grow_ceiling: { vcpu: 3, memory_gb: 8, disk_gb: 100, bandwidth_mbps: 250 },
      spend_limit_month: '25.000',
    });
    setPackage({ planId: 'l', planName: 'L', packageSku: 'plan.l' });
    expect(readCart().growCeiling).toBeNull();
    expect(readCart().overageMode).toBe('grow');
    // With the document, XL cannot grow → the body says capped.
    setPackage({ planId: 'xl', planName: 'XL', packageSku: 'plan.xl' });
    expect(overageFieldsFor(readCart(), doc())).toEqual({ overage_mode: 'capped' });
  });

  it('documentQuote: hot-standby on M prices only in grow mode (standby billed as usage, 0 on the monthly) and echoes the mode and rates', () => {
    const d = doc();
    const cart: CartState = {
      ...readCart(),
      plan: 'm', planName: 'M', packageSku: 'plan.m', addons: [],
      appConfigs: { postgres: { active_hot_standby: true, primary_region: 'a', replica_region: 'b' } },
    };
    expect(documentQuote(d, { ...cart, overageMode: 'capped' })).toBeNull();
    const q = documentQuote(d, { ...cart, overageMode: 'grow', spendLimitMonth: '25.000' })!;
    expect(q.amount_baisa).toBe(4490);
    expect(q.topology_amount_baisa).toBe(0);
    expect(q.overage_mode).toBe('grow');
    expect(q.spend_limit_month).toBe('25.000');
    expect(q.overage_rates?.map(r => r.price_month)).toEqual(['1.796', '0.337', '0.035', '1.253']);
    // A capped single-region cart carries overage_mode: capped and no rates.
    const capped = documentQuote(d, { ...cart, overageMode: 'capped', appConfigs: {} })!;
    expect(capped.overage_mode).toBe('capped');
    expect(capped.overage_rates).toBeUndefined();
  });

  it('overageSummary: "Capped at OMR 4.490 / mo"; grow lists the ceiling, the limit and how it is billed', () => {
    const d = doc();
    writeCart({ ...readCart(), plan: 'm', planName: 'M', packageSku: 'plan.m' });
    const q = documentQuote(d, readCart());
    expect(overageSummary(q, readCart(), d, formatOMR)).toEqual({ mode: 'capped', title: 'Capped at OMR 4.490 / mo', detail: [] });
    setOverage({ mode: 'grow', growCeiling: { vcpu: 3, memory_gb: 6, disk_gb: 100, bandwidth_mbps: 250 }, spendLimitMonth: '25.000' });
    const g = overageSummary(documentQuote(d, readCart()), readCart(), d, formatOMR);
    expect(g.mode).toBe('grow');
    expect(g.detail).toEqual([
      'up to 3 vCPU · 6 GB memory · 100 GB disk · 250 Mbps',
      'spend limit OMR 25.000 / mo',
      'usage above the package billed after the month',
    ]);
  });
});

describe('with the grow model the allowance follows the customer\'s mode, not the cell (#6971)', () => {
  it('/plans: the quantity cells carry the allowance alone; without the model the cell\'s overage word stays', () => {
    expect(hasGrowModel(doc())).toBe(true);
    const row = (d: PublicPackages, key: string) => buildLadder(d).groups.flatMap(g => g.rows).find(r => r.key === key)!;
    expect(row(doc(), 'bandwidth').cells.map(c => [c.label, c.hint])).toEqual([
      ['50 Mbps', null], ['100 Mbps', null], ['250 Mbps', null], ['1000 Mbps', null],
    ]);
    expect(hasGrowModel(doc(RAW_V3))).toBe(false);
    expect(row(doc(RAW_V3), 'bandwidth').cells.map(c => c.hint)).toEqual(['hard cap', 'hard cap', 'then metered', 'then metered']);
  });

  it('/addons: an included allowance is its value plus the dimension it grows in — the step adds the mode', () => {
    const m = addonsLadderFor(doc(), 'plan.m')!;
    const byKey = Object.fromEntries(m.included.map(i => [i.key, [i.value, i.growKey]]));
    expect(byKey.bandwidth).toEqual(['100 Mbps', 'bandwidth_mbps']);
    expect(byKey.disk).toEqual(['50 GB', 'disk_gb']);
    expect(byKey.gitea_iac).toEqual(['read', null]);
    const v3 = addonsLadderFor(doc(RAW_V3), 'plan.m')!;
    expect(v3.included.find(i => i.key === 'bandwidth')).toMatchObject({ value: '100 Mbps · hard cap', growKey: null });
  });
});
