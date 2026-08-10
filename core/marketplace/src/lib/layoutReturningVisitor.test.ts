// layoutReturningVisitor.test.ts — #5940 (UAT row 91).
//
// WHY THIS FILE EXISTS, and why `returningVisitor.test.ts` is not enough.
//
// The redirect that row 91 walks is an `is:inline` script in
// `src/layouts/Layout.astro`. It has to be inline — it runs before the Svelte
// bundle so a returning customer never sees the storefront flash — which
// means it cannot `import` the module that holds the tested logic. That is
// the classic setup for a fix that is green in unit tests and absent from the
// shipped page.
//
// So this file does not test a reconstruction. It EXTRACTS the real
// `<script is:inline>` blocks from Layout.astro on disk, runs them against a
// fake window/document/localStorage, and asserts on the URL the script passes
// to `location.replace`. Deleting `hintedConsoleTarget` from the page, or
// changing where it points, fails here.
//
// The last describe block then re-runs the same matrix through
// `resolveReturningVisitor` and requires the two to agree, so the inline copy
// cannot drift from its spec without a red test.

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { resolveReturningVisitor } from './returningVisitor';

const LAYOUT = join(__dirname, '..', 'layouts', 'Layout.astro');
const layoutSrc = readFileSync(LAYOUT, 'utf8');

/** Every `<script is:inline>` body in the layout, in document order. */
function inlineScripts(src: string): string[] {
  const out: string[] = [];
  for (const m of src.matchAll(/<script is:inline>([\s\S]*?)<\/script>/g)) out.push(m[1]);
  return out;
}

const scripts = inlineScripts(layoutSrc);

interface RunOptions {
  host: string;
  cookie: string;
  localStorage?: Record<string, string>;
  brands?: Record<string, unknown>;
  pathname?: string;
  search?: string;
}

interface RunResult {
  /** The URL passed to location.replace, or '' when none was. */
  replaced: string;
  replaceCalls: number;
}

/**
 * Execute the layout's real inline scripts against a synthetic browser.
 *
 * Only the surfaces the scripts touch are provided. Anything they reach for
 * that is missing throws, which is itself informative — a script that started
 * depending on something new would fail loudly here rather than silently.
 */
function runLayoutScripts(opts: RunOptions): RunResult {
  const store = new Map<string, string>(Object.entries(opts.localStorage || {}));
  let replaced = '';
  let replaceCalls = 0;

  const fakeWindow: Record<string, unknown> = {
    __ORG_BRANDS__: opts.brands,
    location: {
      hostname: opts.host,
      pathname: opts.pathname ?? '/',
      search: opts.search ?? '',
      replace(url: string) {
        replaceCalls += 1;
        replaced = url;
      },
    },
    matchMedia: () => ({ matches: false }),
    URLSearchParams,
  };

  const fakeDocument = {
    cookie: opts.cookie,
    title: 'Redeem voucher — OpenOva',
    documentElement: {
      dataset: {} as Record<string, string>,
      setAttribute() {},
    },
  };

  const fakeLocalStorage = {
    getItem: (k: string) => (store.has(k) ? store.get(k)! : null),
    setItem: (k: string, v: string) => void store.set(k, v),
  };
  const fakeSessionStorage = {
    getItem: () => null,
    setItem: () => {},
  };

  const body = scripts.join('\n;\n');
  // `fetch` is provided but never expected to fire on the paths under test —
  // the cookie-hint branch returns before the probe. Returning a rejected
  // promise would mask a wrong branch; throwing surfaces it.
  const fn = new Function(
    'window',
    'document',
    'localStorage',
    'sessionStorage',
    'URLSearchParams',
    'fetch',
    body,
  );
  fn(
    fakeWindow,
    fakeDocument,
    fakeLocalStorage,
    fakeSessionStorage,
    URLSearchParams,
    () => {
      throw new Error('the inline script issued a network probe on a path that should not need one');
    },
  );

  return { replaced, replaceCalls };
}

const SOV_MARKETPLACE = 'marketplace.t92.omani.works';
const HINT = (org: string) => `catalyst_session_hint=${encodeURIComponent(org ? `org=${org}&v=1` : 'v=1')}`;

describe('Layout.astro inline redirect — the SHIPPED script (#5940, row 91)', () => {
  // Vacuity guard. If the extractor stopped matching, every assertion below
  // would run against an empty program and pass.
  it('CONTROL: the extractor found the inline scripts', () => {
    expect(scripts.length).toBeGreaterThanOrEqual(2);
    expect(layoutSrc).toContain('hintedConsoleTarget');
  });

  it('sends an Org-scoped cookie-borne customer to their OWN Org console', () => {
    const out = runLayoutScripts({ host: SOV_MARKETPLACE, cookie: HINT('uatco') });
    expect(out.replaceCalls).toBe(1);
    expect(out.replaced).toBe('https://console.uatco.t92.omani.works/dashboard');
  });

  it('sends a Sovereign-admin cookie-borne visitor to the Sovereign console', () => {
    const out = runLayoutScripts({ host: SOV_MARKETPLACE, cookie: HINT('') });
    expect(out.replaced).toBe('https://console.t92.omani.works/dashboard');
  });

  it('honours the stamped server-authoritative console host (pool TLD)', () => {
    const out = runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: HINT('uatco'),
      localStorage: { 'org-active-console-host': 'console.uatco.omani.homes' },
    });
    expect(out.replaced).toBe('https://console.uatco.omani.homes/dashboard');
  });

  // ── CONTROLS ───────────────────────────────────────────────────────────
  it('CONTROL: a signed-OUT visitor is NOT redirected — the storefront renders', () => {
    // This is the measured pre-fix state for everyone (document.cookie
    // empty). It must remain the behaviour for anyone without a session.
    const out = runLayoutScripts({ host: SOV_MARKETPLACE, cookie: '' });
    expect(out.replaceCalls).toBe(0);
    expect(out.replaced).toBe('');
  });

  it('CONTROL: a cart-only cookie jar does not count as a session', () => {
    const out = runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: 'org-theme=dark; some-analytics=1',
      localStorage: { 'org-cart': '[]', 'org-pending-voucher': 'ABC123' },
    });
    expect(out.replaceCalls).toBe(0);
  });

  it('CONTROL: a demo/partner opt-out tenant is not bounced', () => {
    const out = runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: HINT('uatco'),
      brands: { [SOV_MARKETPLACE]: { id: 'demo', brand: 'Demo', skipConsoleRedirect: true } },
    });
    expect(out.replaceCalls).toBe(0);
  });

  it('CONTROL: a JWT-shaped hint value is refused, not parsed', () => {
    const out = runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: 'catalyst_session_hint=eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln',
    });
    expect(out.replaceCalls).toBe(0);
  });

  it('CONTROL: a slug that is not a DNS label cannot steer the host', () => {
    // Degrades to the Sovereign console — the bad slug is dropped, never
    // spliced into the address.
    const out = runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: `catalyst_session_hint=${encodeURIComponent('org=acme:8080&v=1')}`,
    });
    expect(out.replaced).toBe('https://console.t92.omani.works/dashboard');
    expect(out.replaced).not.toContain('8080');
  });

  it('CONTROL: a fully-qualified host smuggled into the slug is refused outright', () => {
    // The off-Sovereign redirect this whole check exists to prevent.
    const out = runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: `catalyst_session_hint=${encodeURIComponent('org=evil.example.test&v=1')}`,
    });
    expect(out.replaceCalls).toBe(0);
    expect(out.replaced).not.toContain('evil.example.test');
  });

  it('does not fire on the exempt paths (/launching, /auth/callback, ?order_id, ?new=1)', () => {
    for (const [pathname, search] of [
      ['/launching', ''],
      ['/auth/callback', ''],
      ['/checkout', '?order_id=ord_1'],
      ['/plans', '?new=1'],
    ] as const) {
      const out = runLayoutScripts({ host: SOV_MARKETPLACE, cookie: HINT('uatco'), pathname, search });
      expect(out.replaceCalls, `${pathname}${search} must stay put`).toBe(0);
    }
  });
});

describe('inline script and returningVisitor.ts agree (drift guard)', () => {
  const cases = [
    { org: 'uatco', stamped: '' },
    { org: '', stamped: '' },
    { org: 'uatco', stamped: 'console.uatco.omani.homes' },
    { org: 'uatco', stamped: 'console.otherorg.omani.homes' },
    { org: 'acme-corp', stamped: '' },
  ];

  for (const c of cases) {
    it(`org=${c.org || '(none)'} stamped=${c.stamped || '(none)'}`, () => {
      const inline = runLayoutScripts({
        host: SOV_MARKETPLACE,
        cookie: HINT(c.org),
        localStorage: c.stamped ? { 'org-active-console-host': c.stamped } : {},
      });
      const module = resolveReturningVisitor({
        host: SOV_MARKETPLACE,
        skipConsoleRedirect: false,
        hint: { org: c.org },
        stampedConsoleHost: c.stamped,
      });
      expect(module.redirect).toBe(true);
      expect(inline.replaced).toBe(module.redirect ? module.url : '');
    });
  }
});
