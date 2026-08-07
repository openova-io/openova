// #5634 (UAT row 92) — the /redeem markup contract.
//
// WHY THIS FILE EXISTS. `redeemPreview.test.ts` covers the decision logic
// exhaustively: it proves a 429 selects the `redeem-throttled` panel and that
// the server's retry window reaches the detail line. It cannot prove the panel
// EXISTS. Its DOM fixture is hand-rebuilt from `REDEEM_PANELS`:
//
//     /** Rebuild the panel skeleton that `src/pages/redeem.astro` renders. */
//
// so the test constructs the very elements whose presence is in question. That
// is the fixture-is-the-contract shape. Measured, not assumed: renaming
// `id="redeem-throttled"` to `id="redeem-throttled-RENAMED"` in the real
// `redeem.astro` left the entire marketplace suite green — 84 passed, 0 failed.
//
// The consequence is exactly the #5634 customer-facing symptom coming back
// silently. `showRedeemPanel` resolves panels by id and no-ops on a miss, so a
// throttled customer would see no notice at all while every unit test still
// passed. This file closes that gap by asserting against the REAL markup on
// disk rather than a reconstruction of it.
//
// The required-id set is DERIVED, never hardcoded: the panel ids come from
// `REDEEM_PANELS`, and the write targets are extracted from the
// `getElementById('...')` literals in the shipping code. Adding a panel or a
// new write target therefore extends this guard automatically — there is no
// second list to forget to update.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { REDEEM_PANELS } from './redeemPreview';

const ROOT = join(__dirname, '..', '..');
const PAGE = join(ROOT, 'src', 'pages', 'redeem.astro');
const HELPER = join(ROOT, 'src', 'lib', 'redeemPreview.ts');

const pageSrc = readFileSync(PAGE, 'utf8');
const helperSrc = readFileSync(HELPER, 'utf8');

/** Every `id="..."` the page actually renders. */
function renderedIds(html: string): Set<string> {
  const out = new Set<string>();
  for (const m of html.matchAll(/\bid="([^"]+)"/g)) out.add(m[1]);
  return out;
}

/** Every element id the shipping code writes to via getElementById. */
function writeTargets(...sources: string[]): Set<string> {
  const out = new Set<string>();
  for (const src of sources) {
    for (const m of src.matchAll(/getElementById\(\s*['"]([^'"]+)['"]\s*\)/g)) {
      out.add(m[1]);
    }
  }
  return out;
}

const ids = renderedIds(pageSrc);

describe('redeem.astro markup contract (#5634)', () => {
  // The headline assertion. `redeem-throttled` is the panel the rate-limit
  // notice lands in, so its absence is #5634 reopening.
  it('renders a panel element for every id in REDEEM_PANELS', () => {
    const missing = REDEEM_PANELS.filter(id => !ids.has(id));
    expect(
      missing,
      `redeem.astro is missing panel element(s) ${missing.join(', ')}. ` +
        'showRedeemPanel() resolves panels by id and no-ops when one is absent, ' +
        'so the corresponding outcome would show the customer nothing at all.',
    ).toEqual([]);
  });

  // The detail line and the retry countdown are separate elements from the
  // panel itself; the panel can exist while the text the customer actually
  // reads has nowhere to go.
  it('renders an element for every getElementById target in the redeem code', () => {
    const targets = [...writeTargets(pageSrc, helperSrc)].sort();
    expect(targets.length).toBeGreaterThan(0); // vacuity: the extractor found something
    const missing = targets.filter(id => !ids.has(id));
    expect(
      missing,
      `redeem.astro is missing element(s) ${missing.join(', ')} that the page ` +
        'or redeemPreview.ts writes to by id.',
    ).toEqual([]);
  });

  // Specific to the issue: the throttle notice needs somewhere to print the
  // server's own message and its retry window. Pinned by name because these two
  // ARE the #5634 fix's customer-visible surface.
  it('renders the throttle notice detail and retry elements', () => {
    expect(ids.has('redeem-throttled')).toBe(true);
    expect(ids.has('redeem-throttled-detail')).toBe(true);
    expect(ids.has('redeem-throttled-retry')).toBe(true);
  });

  // CONTROLS — without these the assertions above could pass on a mis-parse.
  // A regex that matched nothing would make every `missing` array empty and
  // the guard would go green on an empty file.
  it('control: the extractor really parsed the page', () => {
    expect(ids.size).toBeGreaterThanOrEqual(REDEEM_PANELS.length);
    expect(ids.has('redeem-cta')).toBe(true);
  });

  it('control: an id that is NOT in the markup is reported absent', () => {
    // Proves the check can go red. If this ever passes as present, the
    // extractor is over-matching and every assertion above is vacuous.
    expect(ids.has('redeem-throttled-RENAMED')).toBe(false);
  });
});
