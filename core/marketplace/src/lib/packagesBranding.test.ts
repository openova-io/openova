// packagesBranding.test.ts — #6971: icons, accent and badge from the BSS
// public packages document, validated at the boundary.
//
// The storefront hardcodes nothing visual about a feature or a package: every
// icon, colour and badge is read from the document (DESIGN §22.4 additions:
// `groups[].icon`, `floor[].icon`, `features[].icon`, `packages[].icon`,
// `packages[].accent`, `packages[].badge`). Fed from
// fixtures/public-packages-v3-icons.json — the v2 fixture (the live hw307
// document) plus an icon on every group / floor item / package and on all
// but two features, an accent on all four packages and a badge on M.
//
//   1. parseIcon: a path under /api/v1/public/icons/ resolves against the
//      document's URL; an https URL on the same host with that path is kept;
//      every other shape (another host, http, protocol-relative, data:,
//      javascript:, `..` escape, credentials, no document URL) is dropped;
//   2. accent / bg must be #RRGGBB; badge is trimmed and bounded;
//   3. readableOn always reaches 4.5:1;
//   4. the render models carry what the document says — and nothing when it
//      says nothing (the v2 fixture renders exactly as before);
//   5. the Review fallback total (documentQuote) and the DR level a package
//      does not have at all.

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import {
  addonsLadderFor,
  buildLadder,
  contrastRatio,
  drTopologyFor,
  funnelAddonsFor,
  hexColour,
  parseIcon,
  parsePublicPackages,
  readableOn,
  type PublicPackages,
} from './packages';
import { documentQuote } from './quote';
import type { CartState } from './cart';

const ROOT = join(__dirname, '..', '..');
const DOC_URL = 'https://chargeback.t99.omani.works/api/v1/public/packages';
const BASE = new URL(DOC_URL);
const HASH = 'a'.repeat(64);

function raw(name: string): any {
  return JSON.parse(readFileSync(join(ROOT, 'fixtures', name), 'utf8'));
}
function v3(url: string | null = DOC_URL): PublicPackages {
  const p = parsePublicPackages(raw('public-packages-v3-icons.json'), url);
  if (!p) throw new Error('v3 fixture does not parse');
  return p;
}
function v2(): PublicPackages {
  const p = parsePublicPackages(raw('public-packages-v2.json'), DOC_URL);
  if (!p) throw new Error('v2 fixture does not parse');
  return p;
}

describe('parseIcon — only icons on the document host, under the icons path', () => {
  it('resolves a path against the document URL and keeps alt and bg', () => {
    expect(parseIcon({ src: `/api/v1/public/icons/${HASH}`, alt: 'Backup', bg: '#312E81' }, BASE, 'x')).toEqual({
      src: `https://chargeback.t99.omani.works/api/v1/public/icons/${HASH}`,
      alt: 'Backup',
      bg: '#312E81',
    });
  });

  it('keeps an https URL on the same host with the icons path', () => {
    const icon = parseIcon({ src: `https://chargeback.t99.omani.works/api/v1/public/icons/${HASH}` }, BASE, 'Name');
    expect(icon?.src).toBe(`https://chargeback.t99.omani.works/api/v1/public/icons/${HASH}`);
    expect(icon?.alt).toBe('Name');
  });

  it.each([
    ['another host', `https://evil.example/api/v1/public/icons/${HASH}`],
    ['a sibling subdomain', `https://cdn.t99.omani.works/api/v1/public/icons/${HASH}`],
    ['plain http on the same host', `http://chargeback.t99.omani.works/api/v1/public/icons/${HASH}`],
    ['protocol-relative', `//evil.example/api/v1/public/icons/${HASH}`],
    ['another path on the host', '/api/v1/public/packages'],
    ['a relative path', `api/v1/public/icons/${HASH}`],
    ['a `..` escape', '/api/v1/public/icons/../../internal/secret'],
    ['an encoded `..` escape', '/api/v1/public/icons/%2e%2e/%2e%2e/internal'],
    ['a nested path', `/api/v1/public/icons/${HASH}/more`],
    ['data:', 'data:image/svg+xml,<svg/>'],
    ['javascript:', 'javascript:alert(1)'],
    ['credentials', `https://u:p@chargeback.t99.omani.works/api/v1/public/icons/${HASH}`],
    ['empty', ''],
  ])('drops %s', (_label, src) => {
    expect(parseIcon({ src, alt: 'x' }, BASE, 'x')).toBeNull();
  });

  it('drops a non-object, a missing src, and every icon when the document URL is unknown', () => {
    expect(parseIcon('/api/v1/public/icons/abc', BASE, 'x')).toBeNull();
    expect(parseIcon({ alt: 'x' }, BASE, 'x')).toBeNull();
    expect(parseIcon({ src: `/api/v1/public/icons/${HASH}` }, null, 'x')).toBeNull();
    const noUrl = v3(null);
    expect(noUrl.packages.every(p => !p.icon)).toBe(true);
    expect(noUrl.features.every(f => !f.icon)).toBe(true);
    // accent and badge load nothing and still parse.
    expect(noUrl.packages.map(p => p.accent)).toEqual(['#0369A1', '#4F46E5', '#7C3AED', '#F59E0B']);
  });

  it('drops a bg that is not #RRGGBB but keeps the icon', () => {
    const icon = parseIcon({ src: `/api/v1/public/icons/${HASH}`, bg: 'red' }, BASE, 'x');
    expect(icon).not.toBeNull();
    expect(icon!.bg).toBeUndefined();
  });
});

describe('accent, bg and badge validation', () => {
  it.each(['#0EA5E9', '#abcdef', '#000000'])('accepts %s', v => expect(hexColour(v)).toBe(v));
  it.each(['red', '#fff', '#12345', '#1234567', 'rgb(0,0,0)', '#GGGGGG', ' #000000', 1, null])('rejects %s', v => {
    expect(hexColour(v)).toBeNull();
  });

  it('a package keeps a valid accent and a trimmed, bounded badge; drops the rest', () => {
    const doc = parsePublicPackages({
      packages: [
        { sku: 'a', name: 'A', price_month: '1.000', accent: '#123ABC', badge: '  Most   popular  ' },
        { sku: 'b', name: 'B', price_month: '2.000', accent: 'blue', badge: '   ' },
        { sku: 'c', name: 'C', price_month: '3.000', badge: 'x'.repeat(80) },
      ],
    }, DOC_URL)!;
    expect(doc.packages[0].accent).toBe('#123ABC');
    expect(doc.packages[0].badge).toBe('Most popular');
    expect(doc.packages[1].accent).toBeUndefined();
    expect(doc.packages[1].badge).toBeUndefined();
    expect(doc.packages[2].badge!.length).toBeLessThanOrEqual(32);
  });
});

describe('readableOn — the text colour on an accent always reaches 4.5:1', () => {
  it('picks white on dark and black on light', () => {
    expect(readableOn('#000000')).toBe('#ffffff');
    expect(readableOn('#4F46E5')).toBe('#ffffff');
    // White on indigo-500 is 4.47:1 — under the bar — so black wins there.
    expect(readableOn('#6366F1')).toBe('#000000');
    expect(readableOn('#F59E0B')).toBe('#000000');
    expect(readableOn('#FFFFFF')).toBe('#000000');
  });

  it('holds across the whole RGB cube (sampled every 17 per channel)', () => {
    let worst = 21;
    for (let r = 0; r <= 255; r += 17) for (let g = 0; g <= 255; g += 17) for (let b = 0; b <= 255; b += 17) {
      const hex = `#${[r, g, b].map(c => c.toString(16).padStart(2, '0')).join('')}`;
      worst = Math.min(worst, contrastRatio(hex, readableOn(hex)));
    }
    expect(worst).toBeGreaterThanOrEqual(4.5);
  });
});

describe('the render models carry the document branding — and nothing without it', () => {
  it('ladder cards: icon, accent, readable foreground, badge only where the document has one', () => {
    const m = buildLadder(v3());
    expect(m.cards.map(c => c.accent)).toEqual(['#0369A1', '#4F46E5', '#7C3AED', '#F59E0B']);
    expect(m.cards.every(c => c.icon?.src.startsWith('https://chargeback.t99.omani.works/api/v1/public/icons/'))).toBe(true);
    expect(m.cards.map(c => c.badge)).toEqual([null, 'Most popular', null, null]);
    for (const c of m.cards) expect(contrastRatio(c.accent!, c.accentFg!)).toBeGreaterThanOrEqual(4.5);
    // `recommended` still drives the recommended column, independently of the badge.
    expect(m.recommendedSku).toBe('plan.m');
  });

  it('rows, groups and the floor carry icons; a feature without one has null and the gutter flag is set', () => {
    const m = buildLadder(v3());
    const rows = m.groups.flatMap(g => g.rows);
    expect(rows.find(r => r.key === 'backup')!.icon).toMatchObject({ alt: 'Backup', bg: '#312E81' });
    expect(rows.find(r => r.key === 'ai_builder')!.icon).toBeNull();
    expect(rows.find(r => r.key === 'audit_log')!.icon).toBeNull();
    expect(rows.find(r => r.key === 'console')!.icon!.bg).toBeUndefined();
    expect(m.groups.every(g => g.icon !== null)).toBe(true);
    expect(m.rowIcons).toBe(true);
    expect(m.floorIcons).toBe(true);
    expect(m.floorItems.map(f => f.name)).toEqual(m.floor);
    expect(m.floorItems.every(f => f.icon !== null)).toBe(true);
  });

  it('the v2 document (no branding) yields no icon, accent or badge anywhere', () => {
    const m = buildLadder(v2());
    expect(m.cards.every(c => c.icon === null && c.accent === null && c.accentFg === null && c.badge === null)).toBe(true);
    expect(m.groups.every(g => g.icon === null && g.rows.every(r => r.icon === null))).toBe(true);
    expect(m.rowIcons).toBe(false);
    expect(m.floorIcons).toBe(false);
  });

  it('step 3: the blocks and the AddOn list carry the feature icons; a feature without one carries none', () => {
    const doc = v3();
    const l = addonsLadderFor(doc, 'plan.l')!;
    expect(l.included.find(i => i.key === 'bandwidth')!.icon).not.toBeNull();
    expect(l.missing.find(i => i.key === 'kube_api')!.icon).not.toBeNull();
    const backup = l.addons.find(a => a.id === 'addon.backup')!;
    expect(backup.image?.src).toMatch(/^https:\/\/chargeback\.t99\.omani\.works\/api\/v1\/public\/icons\/[0-9a-f]{64}$/);
    expect(backup.icon).toBe('');
    const builder = l.addons.find(a => a.id === 'addon.ai_builder')!;
    expect(builder.image).toBeUndefined();
    expect(builder.icon).toBe('');
    // The same through funnelAddonsFor (the list Review resolves the cart against).
    expect(funnelAddonsFor(doc, 'plan.l').addons.find(a => a.id === 'addon.backup')!.image).toEqual(backup.image);
  });
});

function cart(over: Partial<CartState>): CartState {
  return {
    plan: 'l', planName: 'L', apps: ['1'], addons: [], orgName: '', subdomain: '', email: '',
    tld: 'omani.homes', agents: [], appConfigs: {}, packageSku: 'plan.l', ...over,
  } as CartState;
}

describe('documentQuote — the Review total when POST /billing/quote cannot answer', () => {
  it('prices the package and its add-ons from the document by the quote rules', () => {
    const q = documentQuote(v2(), cart({ addons: ['addon.backup', 'addon.domain'] }))!;
    expect(q.plan_amount_baisa).toBe(7990);
    expect(q.lines).toEqual([
      { sku: 'addon.backup', name: 'Backup', amount_baisa: 1500 },
      { sku: 'addon.domain', name: 'Domain', amount_baisa: 500 },
    ]);
    expect(q.amount_baisa).toBe(9990);
    expect(q.topology_amount_baisa).toBe(0);
  });

  it('is null when the document cannot price the cart the way billing would', () => {
    expect(documentQuote(null, cart({}))).toBeNull();
    expect(documentQuote(v2(), cart({ packageSku: null }))).toBeNull();
    expect(documentQuote(v2(), cart({ packageSku: 'plan.nope' }))).toBeNull();
    expect(documentQuote(v2(), cart({ addons: ['daily-backup'] }))).toBeNull();
    // Hot-standby on a package without the active-passive level.
    expect(documentQuote(v2(), cart({ appConfigs: { postgres: { active_hot_standby: true } } }))).toBeNull();
  });

  it('hot-standby on XL is included (0)', () => {
    const q = documentQuote(v2(), cart({ plan: 'xl', packageSku: 'plan.xl', appConfigs: { postgres: { active_hot_standby: true } } }))!;
    expect(q.topology).toBe('active-hot-standby');
    expect(q.amount_baisa).toBe(13990);
  });
});

describe('DR topology a package does not have at all (DR only on XL)', () => {
  it('a not_offered DR cell locks hot-standby and points at the first rung that includes it', () => {
    const r = raw('public-packages-v2.json');
    const f = r.features.find((x: any) => x.key === 'dr_topology');
    for (const sku of ['plan.s', 'plan.m', 'plan.l']) f.cells[sku] = { state: 'not_offered', included_from: 'plan.xl' };
    const doc = parsePublicPackages(r, DOC_URL)!;
    expect(drTopologyFor(doc, 'plan.m')).toEqual({ level: -1, label: '—', activePassive: false, activePassiveFrom: { sku: 'plan.xl', name: 'XL' }, growOnly: false });
    expect(drTopologyFor(doc, 'plan.xl')!.activePassive).toBe(true);
    // The matrix renders the dash for those cells.
    const row = buildLadder(doc).groups.flatMap(g => g.rows).find(x => x.key === 'dr_topology')!;
    expect(row.cells.map(c => c.label)).toEqual(['—', '—', '—', 'active-passive']);
  });
});

describe('markup contract — nothing visual is keyed by feature key or sku in the storefront', () => {
  const read = (...p: string[]) => readFileSync(join(ROOT, 'src', ...p), 'utf8');
  const files = {
    PackageLadder: read('components', 'PackageLadder.svelte'),
    AddonsStep: read('components', 'AddonsStep.svelte'),
    ReviewStep: read('components', 'ReviewStep.svelte'),
    css: read('styles', 'package-ladder.css'),
  };

  it('no icon map keyed by slug / feature key, no emoji placeholder, no {@html}', () => {
    for (const [name, src] of Object.entries(files)) {
      expect(src, name).not.toMatch(/addonIcons/);
      expect(src, name).not.toMatch(/\{@html/);
      expect(src, name).not.toMatch(/'📦'/);
    }
  });

  it('no colour or rule per package sku in the stylesheet or the components', () => {
    for (const [name, src] of Object.entries(files)) {
      expect(src, name).not.toMatch(/plan\.(s|m|l|xl)\b/);
    }
  });

  it('every document icon is a plain lazy <img> with explicit size and an empty alt (it sits beside its name)', () => {
    let seen = 0;
    for (const [name, src] of Object.entries(files)) {
      for (const tag of src.match(/<img\s[^>]*src=\{[^}]*\.src\}[^>]*>/g) ?? []) {
        seen++;
        expect(tag, name).toMatch(/alt=""/);
        expect(tag, name).toMatch(/loading="lazy"/);
        expect(tag, name).toMatch(/decoding="async"/);
        expect(tag, name).toMatch(/width="\d+" height="\d+"/);
      }
    }
    expect(seen).toBeGreaterThanOrEqual(10);
  });

  it('the floor is first-class on /plans (from floor[], above the cards, outside the package columns) and listed on /addons and /review', () => {
    expect(files.PackageLadder).toMatch(/model\.floorItems as item/);
    expect(files.PackageLadder.indexOf('data-testid="package-floor"')).toBeLessThan(files.PackageLadder.indexOf('<!-- One grouped comparison'));
    expect(files.AddonsStep).toMatch(/data-testid="addons-included-floor"/);
    expect(files.ReviewStep).toMatch(/data-testid="review-floor"/);
  });
});
