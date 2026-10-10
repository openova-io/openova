// currency.test.ts — the storefront's money formatter on the National Cloud
// package prices (#6971, NC-OO-Pricing.xlsx 2026-06-28).
//
// The legacy plan deck (PlanStep.svelte, shown when there is no BSS document)
// renders a plan as "OMR <formatOMRAmount(monthly_price)> / mo", and
// monthly_price is baisa taken from the catalog's price_baisa. The packages
// cost 2.490 / 4.490 / 7.990 / 13.990 OMR — three decimals that a formatter
// rounding to whole OMR, or a reader multiplying price_omr by 1000 in floating
// point, would mangle.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

import { formatOMR, formatOMRAmount, omrToBaisa } from './currency';

describe('formatOMRAmount — the deck price on the National Cloud ladder', () => {
  it('renders each package price with three decimals from baisa', () => {
    expect(formatOMRAmount(2490)).toBe('2.490');
    expect(formatOMRAmount(4490)).toBe('4.490');
    expect(formatOMRAmount(7990)).toBe('7.990');
    expect(formatOMRAmount(13990)).toBe('13.990');
  });

  it('renders the full label the checkout uses', () => {
    expect(formatOMR(2490)).toBe('OMR 2.490');
    expect(formatOMR(13990)).toBe('OMR 13.990');
    expect(formatOMR(0)).toBe('OMR 0.000');
  });

  it('never drops the sub-OMR part (the pre-#6971 whole-OMR rounding)', () => {
    expect(formatOMRAmount(2490)).not.toBe('2');
    expect(formatOMRAmount(2490)).not.toBe('2.49');
  });
});

describe('omrToBaisa — the one float-to-money conversion, rounded', () => {
  it('maps the decimal mirror back to the exact baisa', () => {
    // 2.49 * 1000 is 2490.0000000000005 in binary; rounding makes it 2490.
    expect(omrToBaisa(2.49)).toBe(2490);
    expect(omrToBaisa(4.49)).toBe(4490);
    expect(omrToBaisa(7.99)).toBe(7990);
    expect(omrToBaisa(13.99)).toBe(13990);
    expect(omrToBaisa(9)).toBe(9000);
    expect(omrToBaisa(undefined)).toBe(0);
  });
});

// Markup contract (the packages.test.ts shape): the catalog reader takes the
// plan's money from price_baisa and falls back through omrToBaisa — never
// `price_omr * 1000` in floating point.
describe('getPlans reads the plan price from price_baisa', () => {
  const api = readFileSync(join(__dirname, 'api.ts'), 'utf8');

  it('prefers price_baisa and rounds the price_omr fallback', () => {
    expect(api).toMatch(/price_baisa/);
    expect(api).toMatch(/omrToBaisa\(p\.price_omr\)/);
    expect(api).not.toMatch(/p\.price_omr \|\| 0\) \* 1000/);
  });
});

// And the legacy deck's own fallback rows (catalog unreachable) carry the
// ladder in baisa, with the workbook's shapes.
describe('PlanStep fallback rows carry the National Cloud ladder', () => {
  const deck = readFileSync(join(__dirname, '..', 'components', 'PlanStep.svelte'), 'utf8');

  it('prices and shapes', () => {
    for (const row of [
      /slug: 's'.*cpu: '1 vCPU', memory: '2 GB', storage: '25 GB' \}, monthly_price: 2490/,
      /slug: 'm'.*cpu: '2 vCPU', memory: '4 GB', storage: '50 GB' \}, monthly_price: 4490/,
      /slug: 'l'.*cpu: '4 vCPU', memory: '8 GB', storage: '100 GB' \}, monthly_price: 7990/,
      /slug: 'xl'.*cpu: '8 vCPU', memory: '16 GB', storage: '250 GB' \}, monthly_price: 13990/,
    ]) {
      expect(deck).toMatch(row);
    }
    // The old ladder is gone from the deck.
    expect(deck).not.toMatch(/monthly_price: (5000|9000|16000|30000)/);
    expect(deck).not.toMatch(/'16 vCPU'|'32 GB'/);
  });

  it('lists no invented SLA, support tier or user count', () => {
    expect(deck).not.toMatch(/responseSla|Response SLA|Dedicated account manager|Priority support|up to \d+ users/);
  });
});
