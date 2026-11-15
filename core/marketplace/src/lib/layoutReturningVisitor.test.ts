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
// The last two describe blocks then re-run the matrix through
// `resolveReturningVisitor` and (#7922) `decideStoredOrg`, and require the
// inline copy to agree with both, so it cannot drift from its spec without a
// red test. The fake server answers only the paths a test declares.

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import { resolveReturningVisitor } from './returningVisitor';
import { STORED_ORG_KEYS, decideStoredOrg, type OrgCheckAnswer } from './storedOrg';

const LAYOUT = join(__dirname, '..', 'layouts', 'Layout.astro');
const layoutSrc = readFileSync(LAYOUT, 'utf8');

/** Every `<script is:inline>` body in the layout, in document order. */
function inlineScripts(src: string): string[] {
  const out: string[] = [];
  for (const m of src.matchAll(/<script is:inline>([\s\S]*?)<\/script>/g)) out.push(m[1]);
  return out;
}

const scripts = inlineScripts(layoutSrc);

/**
 * What the fake server answers for one request: an HTTP status and a JSON
 * body, or 'network-error' for a fetch that rejects (offline, DNS, reset).
 */
type ApiAnswer = { status: number; body?: unknown } | 'network-error';

interface RunOptions {
  host: string;
  cookie: string;
  localStorage?: Record<string, string>;
  brands?: Record<string, unknown>;
  pathname?: string;
  search?: string;
  /**
   * The server, keyed by request path. A request for a path with no entry
   * throws, exactly as the harness always did for "a probe on a path that
   * should not need one".
   */
  api?: Record<string, ApiAnswer>;
}

interface RunResult {
  /** The URL passed to location.replace, or '' when none was. */
  replaced: string;
  replaceCalls: number;
  /** localStorage as the scripts left it. */
  storage: Record<string, string>;
  /** Every path the scripts fetched, in order. */
  fetched: string[];
}

/** Let the scripts' promise chains (the fake fetch answers) run to the end. */
async function settle(): Promise<void> {
  for (let i = 0; i < 3; i++) await new Promise((r) => setTimeout(r, 0));
}

/**
 * Execute the layout's real inline scripts against a synthetic browser.
 *
 * Only the surfaces the scripts touch are provided. Anything they reach for
 * that is missing throws, which is itself informative — a script that started
 * depending on something new would fail loudly here rather than silently.
 */
async function runLayoutScripts(opts: RunOptions): Promise<RunResult> {
  const store = new Map<string, string>(Object.entries(opts.localStorage || {}));
  let replaced = '';
  let replaceCalls = 0;
  const fetched: string[] = [];

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
    removeItem: (k: string) => void store.delete(k),
  };
  const fakeSessionStorage = {
    getItem: () => null,
    setItem: () => {},
  };

  // `fetch` answers only the paths a test declares in `opts.api`. Any other
  // request throws: returning a rejected promise would mask a wrong branch;
  // throwing surfaces it.
  const fakeFetch = (url: string) => {
    fetched.push(url);
    const answer = opts.api?.[url];
    if (answer === undefined) {
      throw new Error(`the inline script issued a network probe on a path that should not need one: ${url}`);
    }
    if (answer === 'network-error') return Promise.reject(new TypeError('Failed to fetch'));
    return Promise.resolve({
      ok: answer.status >= 200 && answer.status < 300,
      status: answer.status,
      json: () =>
        answer.body === undefined
          ? Promise.reject(new SyntaxError('Unexpected end of JSON input'))
          : Promise.resolve(answer.body),
    });
  };

  const body = scripts.join('\n;\n');
  const fn = new Function(
    'window',
    'document',
    'localStorage',
    'sessionStorage',
    'URLSearchParams',
    'fetch',
    body,
  );
  fn(fakeWindow, fakeDocument, fakeLocalStorage, fakeSessionStorage, URLSearchParams, fakeFetch);
  await settle();

  return { replaced, replaceCalls, storage: Object.fromEntries(store), fetched };
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

  it('sends an Org-scoped cookie-borne customer to their OWN Org console', async () => {
    const out = await runLayoutScripts({ host: SOV_MARKETPLACE, cookie: HINT('uatco') });
    expect(out.replaceCalls).toBe(1);
    expect(out.replaced).toBe('https://console.uatco.t92.omani.works/dashboard');
  });

  it('sends a Sovereign-admin cookie-borne visitor to the Sovereign console', async () => {
    const out = await runLayoutScripts({ host: SOV_MARKETPLACE, cookie: HINT('') });
    expect(out.replaced).toBe('https://console.t92.omani.works/dashboard');
  });

  it('honours the stamped server-authoritative console host (pool TLD) once the server knows the Org', async () => {
    const out = await runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: HINT('uatco'),
      localStorage: { 'org-active-console-host': 'console.uatco.omani.homes' },
      api: { '/api/tenant/check-slug/uatco': { status: 200, body: { available: false } } },
    });
    expect(out.replaced).toBe('https://console.uatco.omani.homes/dashboard');
  });

  // ── CONTROLS ───────────────────────────────────────────────────────────
  it('CONTROL: a signed-OUT visitor is NOT redirected — the storefront renders', async () => {
    // This is the measured pre-fix state for everyone (document.cookie
    // empty). It must remain the behaviour for anyone without a session.
    const out = await runLayoutScripts({ host: SOV_MARKETPLACE, cookie: '' });
    expect(out.replaceCalls).toBe(0);
    expect(out.replaced).toBe('');
  });

  it('CONTROL: a cart-only cookie jar does not count as a session', async () => {
    const out = await runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: 'org-theme=dark; some-analytics=1',
      localStorage: { 'org-cart': '[]', 'org-pending-voucher': 'ABC123' },
    });
    expect(out.replaceCalls).toBe(0);
  });

  it('CONTROL: a demo/partner opt-out tenant is not bounced', async () => {
    const out = await runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: HINT('uatco'),
      brands: { [SOV_MARKETPLACE]: { id: 'demo', brand: 'Demo', skipConsoleRedirect: true } },
    });
    expect(out.replaceCalls).toBe(0);
  });

  it('CONTROL: a JWT-shaped hint value is refused, not parsed', async () => {
    const out = await runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: 'catalyst_session_hint=eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln',
    });
    expect(out.replaceCalls).toBe(0);
  });

  it('CONTROL: a slug that is not a DNS label cannot steer the host', async () => {
    // Degrades to the Sovereign console — the bad slug is dropped, never
    // spliced into the address.
    const out = await runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: `catalyst_session_hint=${encodeURIComponent('org=acme:8080&v=1')}`,
    });
    expect(out.replaced).toBe('https://console.t92.omani.works/dashboard');
    expect(out.replaced).not.toContain('8080');
  });

  it('CONTROL: a fully-qualified host smuggled into the slug is refused outright', async () => {
    // The off-Sovereign redirect this whole check exists to prevent.
    const out = await runLayoutScripts({
      host: SOV_MARKETPLACE,
      cookie: `catalyst_session_hint=${encodeURIComponent('org=evil.example.test&v=1')}`,
    });
    expect(out.replaceCalls).toBe(0);
    expect(out.replaced).not.toContain('evil.example.test');
  });

  it('does not fire on the exempt paths (/launching, /auth/callback, ?order_id, ?new=1)', async () => {
    for (const [pathname, search] of [
      ['/launching', ''],
      ['/auth/callback', ''],
      ['/checkout', '?order_id=ord_1'],
      ['/plans', '?new=1'],
    ] as const) {
      const out = await runLayoutScripts({ host: SOV_MARKETPLACE, cookie: HINT('uatco'), pathname, search });
      expect(out.replaceCalls, `${pathname}${search} must stay put`).toBe(0);
    }
  });
});

// ── #7922 ────────────────────────────────────────────────────────────────
//
// hw307, 16 Nov 2026: the Sovereign was wiped and installed again under the
// same marketplace host. A browser that had used the previous installation
// still held that installation's Organization in localStorage; after sign-in
// the storefront jumped to `console.<old-org>.omani.rest` — an Organization
// the server no longer had. The pool domain in that address can only come
// from the remembered `org-active-console-host`.

/** What the previous installation left in the browser. */
const OLD_ORG = {
  'org-active-org': 'b3f0c7d2-old-installation',
  'org-active-org-slug': 'oldorg',
  'org-active-console-host': 'console.oldorg.omani.rest',
};
const OLD_ORG_CHECK = '/api/tenant/check-slug/oldorg';
const OLD_ORG_CONSOLE = 'https://console.oldorg.omani.rest/dashboard';

function storedKeys(storage: Record<string, string>): string[] {
  return STORED_ORG_KEYS.filter((k) => k in storage);
}

describe('#7922 — a remembered Organization the server no longer knows', () => {
  it('THE DEFECT: a signed-in Sovereign-admin is NOT sent to the old Organization; the keys are dropped', async () => {
    const out = await runLayoutScripts({
      host: 'marketplace.hw307.omani.works',
      cookie: HINT(''), // the session the new installation minted at sign-in
      localStorage: { ...OLD_ORG },
      api: { [OLD_ORG_CHECK]: { status: 200, body: { available: true } } },
    });
    expect(out.fetched).toEqual([OLD_ORG_CHECK]);
    expect(out.replaced).not.toBe(OLD_ORG_CONSOLE);
    expect(out.replaceCalls).toBe(0); // stays on the storefront
    expect(storedKeys(out.storage)).toEqual([]);
  });

  it('the same for a session that still names the old Organization', async () => {
    const out = await runLayoutScripts({
      host: 'marketplace.hw307.omani.works',
      cookie: HINT('oldorg'),
      localStorage: { ...OLD_ORG },
      api: { [OLD_ORG_CHECK]: { status: 200, body: { available: true } } },
    });
    expect(out.replaceCalls).toBe(0);
    expect(storedKeys(out.storage)).toEqual([]);
  });

  it('CONTROL: an Organization the server knows keeps today\'s redirect and its keys', async () => {
    const out = await runLayoutScripts({
      host: 'marketplace.hw307.omani.works',
      cookie: HINT(''),
      localStorage: { ...OLD_ORG },
      api: { [OLD_ORG_CHECK]: { status: 200, body: { available: false } } },
    });
    expect(out.replaced).toBe(OLD_ORG_CONSOLE);
    expect(out.replaceCalls).toBe(1);
    expect(storedKeys(out.storage)).toEqual([...STORED_ORG_KEYS]);
  });

  for (const [label, answer] of [
    ['a 500', { status: 500, body: { error: 'failed to check slug' } }],
    ['a 503 with no body', { status: 503 }],
    ['a 404 (route missing)', { status: 404, body: { error: 'not found' } }],
    ['a network error', 'network-error'],
    ['a 200 of the wrong shape', { status: 200, body: { ok: true } }],
  ] as const) {
    it(`fails CLOSED on ${label}: no redirect to the unconfirmed Organization, keys kept`, async () => {
      const out = await runLayoutScripts({
        host: 'marketplace.hw307.omani.works',
        cookie: HINT(''),
        localStorage: { ...OLD_ORG },
        api: { [OLD_ORG_CHECK]: answer },
      });
      expect(out.replaceCalls).toBe(0);
      expect(storedKeys(out.storage)).toEqual([...STORED_ORG_KEYS]);
    });
  }

  // ── The storefront-token path: the server lists the visitor's Orgs ─────

  it('a storefront token whose Organization list lacks the remembered Org drops the keys', async () => {
    const out = await runLayoutScripts({
      host: 'marketplace.hw307.omani.works',
      cookie: '',
      localStorage: { 'org-token': 'hs256-new-installation', ...OLD_ORG },
      api: { '/api/tenant/orgs': { status: 200, body: [] } },
    });
    expect(out.replaceCalls).toBe(0);
    expect(storedKeys(out.storage)).toEqual([]);
  });

  it('a storefront token with a NEW Organization lands there and carries none of the old one', async () => {
    const out = await runLayoutScripts({
      host: 'marketplace.hw307.omani.works',
      cookie: '',
      localStorage: { 'org-token': 'hs256-new-installation', ...OLD_ORG },
      api: {
        '/api/tenant/orgs': {
          status: 200,
          body: [{ id: 'org-new', slug: 'neworg', status: 'active', console_host: 'console.neworg.omani.homes' }],
        },
      },
    });
    expect(out.replaceCalls).toBe(1);
    const qs = new URLSearchParams(out.replaced.slice(out.replaced.indexOf('?') + 1));
    expect(out.replaced.startsWith('/launching?')).toBe(true);
    expect(qs.get('host')).toBe('https://console.neworg.omani.homes');
    expect(qs.get('tenant')).toBeNull(); // the old installation's id is gone
    expect(out.replaced).not.toContain('oldorg');
    expect(out.storage['org-active-org']).toBeUndefined();
    expect(out.storage['org-active-org-slug']).toBe('neworg');
    expect(out.storage['org-active-console-host']).toBe('console.neworg.omani.homes');
  });

  it('CONTROL: a storefront token whose list contains the remembered Org keeps every key', async () => {
    const out = await runLayoutScripts({
      host: 'marketplace.hw307.omani.works',
      cookie: '',
      localStorage: { 'org-token': 'hs256', ...OLD_ORG },
      api: {
        '/api/tenant/orgs': {
          status: 200,
          body: [{ id: OLD_ORG['org-active-org'], slug: 'oldorg', status: 'active', console_host: 'console.oldorg.omani.rest' }],
        },
      },
    });
    expect(storedKeys(out.storage)).toEqual([...STORED_ORG_KEYS]);
    const qs = new URLSearchParams(out.replaced.slice(out.replaced.indexOf('?') + 1));
    expect(qs.get('host')).toBe('https://console.oldorg.omani.rest');
    expect(qs.get('tenant')).toBe(OLD_ORG['org-active-org']);
  });

  it('CONTROL: a rejected or failed list never wipes the keys', async () => {
    for (const answer of [{ status: 401, body: { error: 'unauthorized' } }, { status: 502 }, 'network-error'] as const) {
      const out = await runLayoutScripts({
        host: 'marketplace.hw307.omani.works',
        cookie: '',
        localStorage: { 'org-token': 'hs256-old-installation', ...OLD_ORG },
        api: { '/api/tenant/orgs': answer },
      });
      expect(out.replaceCalls).toBe(0);
      expect(storedKeys(out.storage), JSON.stringify(answer)).toEqual([...STORED_ORG_KEYS]);
    }
  });
});

describe('inline script and returningVisitor.ts agree (drift guard)', () => {
  const cases = [
    { org: 'uatco', stamped: '' },
    { org: '', stamped: '' },
    { org: 'uatco', stamped: 'console.uatco.omani.homes' },
    { org: '', stamped: 'console.uatco.omani.homes' },
    { org: 'uatco', stamped: 'console.otherorg.omani.homes' },
    { org: 'acme-corp', stamped: '' },
  ];

  for (const c of cases) {
    it(`org=${c.org || '(none)'} stamped=${c.stamped || '(none)'}`, async () => {
      const module = resolveReturningVisitor({
        host: SOV_MARKETPLACE,
        skipConsoleRedirect: false,
        hint: { org: c.org },
        stampedConsoleHost: c.stamped,
      });
      expect(module.redirect).toBe(true);
      if (!module.redirect) return;
      const inline = await runLayoutScripts({
        host: SOV_MARKETPLACE,
        cookie: HINT(c.org),
        localStorage: c.stamped ? { 'org-active-console-host': c.stamped } : {},
        // Answer "the server knows it" for whatever slug the inline copy asks
        // about; a request for a slug the module did not name throws.
        api: module.verifySlug
          ? { [`/api/tenant/check-slug/${module.verifySlug}`]: { status: 200, body: { available: false } } }
          : {},
      });
      expect(inline.replaced).toBe(module.url);
      // The inline copy asks the server exactly when the module says to.
      expect(inline.fetched).toEqual(module.verifySlug ? [`/api/tenant/check-slug/${module.verifySlug}`] : []);
    });
  }
});

describe('inline script and storedOrg.ts agree on every server answer (drift guard, #7922)', () => {
  const answers: Array<{ label: string; api: ApiAnswer; answer: OrgCheckAnswer }> = [
    { label: 'available:true', api: { status: 200, body: { available: true } }, answer: { endpoint: 'check-slug', status: 200, body: { available: true } } },
    { label: 'available:false', api: { status: 200, body: { available: false } }, answer: { endpoint: 'check-slug', status: 200, body: { available: false } } },
    { label: '500', api: { status: 500, body: { error: 'x' } }, answer: { endpoint: 'check-slug', status: 500, body: { error: 'x' } } },
    { label: 'network error', api: 'network-error', answer: { endpoint: 'check-slug', status: 0, body: null } },
    { label: '200 without available', api: { status: 200, body: {} }, answer: { endpoint: 'check-slug', status: 200, body: {} } },
    { label: '200 not JSON', api: { status: 200 }, answer: { endpoint: 'check-slug', status: 200, body: null } },
  ];

  for (const a of answers) {
    it(`check-slug ${a.label}`, async () => {
      const spec = decideStoredOrg({
        stored: { id: OLD_ORG['org-active-org'], slug: 'oldorg', consoleHost: 'console.oldorg.omani.rest' },
        answer: a.answer,
        target: OLD_ORG_CONSOLE,
      });
      const inline = await runLayoutScripts({
        host: 'marketplace.hw307.omani.works',
        cookie: HINT(''),
        localStorage: { ...OLD_ORG },
        api: { [OLD_ORG_CHECK]: a.api },
      });
      expect(inline.replaced).toBe(spec.redirectTo);
      expect(storedKeys(inline.storage).length === 0).toBe(spec.clearStored);
    });
  }

  const lists: Array<{ label: string; body: unknown }> = [
    { label: 'empty', body: [] },
    { label: 'only a deleted copy', body: [{ id: OLD_ORG['org-active-org'], slug: 'oldorg', status: 'deleted' }] },
    { label: 'matched by id', body: [{ id: OLD_ORG['org-active-org'], slug: 'renamed', status: 'active' }] },
    { label: 'matched by slug', body: [{ id: 'other-id', slug: 'OldOrg', status: 'active' }] },
    { label: 'matched by console host', body: [{ id: 'other-id', slug: 'x', status: 'active', console_host: 'console.oldorg.omani.rest' }] },
    { label: 'someone else', body: [{ id: 'n', slug: 'neworg', status: 'active', console_host: 'console.neworg.omani.homes' }] },
  ];

  for (const l of lists) {
    it(`my-orgs list: ${l.label}`, async () => {
      const spec = decideStoredOrg({
        stored: { id: OLD_ORG['org-active-org'], slug: 'oldorg', consoleHost: 'console.oldorg.omani.rest' },
        answer: { endpoint: 'my-orgs', status: 200, body: l.body },
      });
      const inline = await runLayoutScripts({
        host: 'marketplace.hw307.omani.works',
        cookie: '',
        localStorage: { 'org-token': 'hs256', ...OLD_ORG },
        api: { '/api/tenant/orgs': { status: 200, body: l.body } },
      });
      // A live pick re-stamps slug + host, so compare the one key the token
      // path never re-writes: the remembered id survives iff the spec keeps it.
      expect('org-active-org' in inline.storage).toBe(!spec.clearStored);
    });
  }
});
