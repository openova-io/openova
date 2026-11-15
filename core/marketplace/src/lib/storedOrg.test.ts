// storedOrg.test.ts — #7922.
//
// The decision under test: a stored Organization + what the server answered
// -> go to its console or stay on the storefront, and whether the stored keys
// are dropped. Every row asserts BOTH halves, because the two failure modes
// are different defects: following a gone Organization is the reported bug;
// wiping the keys on a transient error would sign a returning customer out
// of their own console link every time the gateway blinks.

import { describe, expect, it } from 'vitest';

import {
  STORED_ORG_KEYS,
  checkOrgSlug,
  consoleHostSlug,
  decideStoredOrg,
  forgetStoredOrg,
  readStoredOrg,
  type OrgCheckAnswer,
  type StoredOrg,
} from './storedOrg';

const OLD: StoredOrg = {
  id: 'b3f0c7d2-old-installation',
  slug: 'oldorg',
  consoleHost: 'console.oldorg.omani.rest',
};
const TARGET = 'https://console.oldorg.omani.rest/dashboard';

const slugAnswer = (status: number, body: unknown): OrgCheckAnswer => ({ endpoint: 'check-slug', status, body });
const listAnswer = (status: number, body: unknown): OrgCheckAnswer => ({ endpoint: 'my-orgs', status, body });

describe('decideStoredOrg — check-slug (the visitor without a storefront token)', () => {
  it('THE DEFECT: the server has no Organization with the slug -> stay, drop the keys', () => {
    expect(decideStoredOrg({ stored: OLD, answer: slugAnswer(200, { available: true }), target: TARGET })).toEqual({
      verdict: 'gone',
      clearStored: true,
      redirectTo: '',
    });
  });

  it('the server knows the Organization -> today\'s redirect, keys kept', () => {
    expect(decideStoredOrg({ stored: OLD, answer: slugAnswer(200, { available: false }), target: TARGET })).toEqual({
      verdict: 'known',
      clearStored: false,
      redirectTo: TARGET,
    });
  });

  for (const [label, answer] of [
    ['500', slugAnswer(500, { error: 'failed to check slug' })],
    ['503 with no body', slugAnswer(503, null)],
    ['404 — a missing route is not "no such Organization"', slugAnswer(404, { error: 'not found' })],
    ['429', slugAnswer(429, { error: 'rate limited' })],
    ['no answer at all', slugAnswer(0, null)],
    ['200 without `available`', slugAnswer(200, {})],
    ['200 with a string `available`', slugAnswer(200, { available: 'true' })],
    ['200 that was not JSON', slugAnswer(200, null)],
  ] as const) {
    it(`fails CLOSED on ${label}: no redirect, keys kept`, () => {
      expect(decideStoredOrg({ stored: OLD, answer, target: TARGET })).toEqual({
        verdict: 'unverified',
        clearStored: false,
        redirectTo: '',
      });
    });
  }
});

describe('decideStoredOrg — my-orgs (GET /api/tenant/orgs with the storefront token)', () => {
  it('an empty list -> gone', () => {
    expect(decideStoredOrg({ stored: OLD, answer: listAnswer(200, []) }).verdict).toBe('gone');
  });

  it('only other Organizations -> gone', () => {
    const body = [{ id: 'n', slug: 'neworg', status: 'active', console_host: 'console.neworg.omani.homes' }];
    expect(decideStoredOrg({ stored: OLD, answer: listAnswer(200, body) })).toMatchObject({ verdict: 'gone', clearStored: true });
  });

  it('a deleted copy of it does not count -> gone', () => {
    const body = [{ id: OLD.id, slug: 'oldorg', status: 'deleted' }];
    expect(decideStoredOrg({ stored: OLD, answer: listAnswer(200, body) }).verdict).toBe('gone');
  });

  for (const [label, entry] of [
    ['its id', { id: OLD.id, slug: 'renamed', status: 'active' }],
    ['its slug (any case)', { id: 'x', slug: 'OldOrg', status: 'provisioning' }],
    ['its console host', { id: 'x', slug: 'y', status: 'active', console_host: 'console.oldorg.omani.rest' }],
  ] as const) {
    it(`an entry matching ${label} -> known, keys kept`, () => {
      expect(decideStoredOrg({ stored: OLD, answer: listAnswer(200, [entry]), target: TARGET })).toEqual({
        verdict: 'known',
        clearStored: false,
        redirectTo: TARGET,
      });
    });
  }

  it('the slug named by the stored console host alone is enough to match', () => {
    const stored = { consoleHost: 'console.oldorg.omani.rest' };
    expect(decideStoredOrg({ stored, answer: listAnswer(200, [{ id: 'z', slug: 'oldorg', status: 'active' }]) }).verdict).toBe('known');
  });

  for (const [label, answer] of [
    ['401 (a token from the previous installation)', listAnswer(401, { error: 'unauthorized' })],
    ['403', listAnswer(403, null)],
    ['502', listAnswer(502, null)],
    ['no answer', listAnswer(0, null)],
    ['200 that is not a list', listAnswer(200, { orgs: [] })],
  ] as const) {
    it(`fails CLOSED on ${label}: keys kept`, () => {
      expect(decideStoredOrg({ stored: OLD, answer, target: TARGET })).toEqual({
        verdict: 'unverified',
        clearStored: false,
        redirectTo: '',
      });
    });
  }
});

describe('decideStoredOrg — nothing stored', () => {
  it('is not judged, whatever the server says', () => {
    for (const answer of [slugAnswer(200, { available: true }), listAnswer(200, [])]) {
      expect(decideStoredOrg({ stored: { id: '', slug: ' ', consoleHost: '' }, answer, target: TARGET })).toEqual({
        verdict: 'none',
        clearStored: false,
        redirectTo: '',
      });
    }
  });
});

describe('consoleHostSlug', () => {
  it('reads the label after console.', () => {
    expect(consoleHostSlug('console.oldorg.omani.rest')).toBe('oldorg');
    expect(consoleHostSlug(' Console.Acme-Corp.omani.homes ')).toBe('acme-corp');
  });

  it('refuses anything that is not console.<dns-label>.<parent>', () => {
    for (const h of ['', 'console.', 'console.oldorg', 'console.oldorg.', 'console..omani.rest', 'portal.oldorg.omani.rest', 'console.-bad.omani.rest', 'console.a:1.omani.rest']) {
      expect(consoleHostSlug(h), h).toBe('');
    }
  });
});

describe('browser helpers', () => {
  it('forgetStoredOrg removes exactly the three Organization keys', () => {
    const store = new Map<string, string>([
      ['org-active-org', OLD.id!],
      ['org-active-org-slug', 'oldorg'],
      ['org-active-console-host', 'console.oldorg.omani.rest'],
      ['org-cart', '{"plan":"m"}'],
      ['org-theme', 'dark'],
    ]);
    forgetStoredOrg({ removeItem: (k: string) => void store.delete(k) });
    expect([...store.keys()].sort()).toEqual(['org-cart', 'org-theme']);
    expect(STORED_ORG_KEYS).toHaveLength(3);
  });

  it('readStoredOrg and forgetStoredOrg never throw when storage does', () => {
    const broken = {
      getItem: () => { throw new Error('SecurityError'); },
      removeItem: () => { throw new Error('SecurityError'); },
    };
    expect(readStoredOrg(broken)).toEqual({ id: '', slug: '', consoleHost: '' });
    expect(() => forgetStoredOrg(broken)).not.toThrow();
  });

  it('checkOrgSlug asks the public endpoint and reports status + body', async () => {
    const asked: string[] = [];
    const answer = await checkOrgSlug('oldorg', async (url) => {
      asked.push(url);
      return { status: 200, json: async () => ({ available: true }) } as Pick<Response, 'status' | 'json'>;
    });
    expect(asked).toEqual(['/api/tenant/check-slug/oldorg']);
    expect(answer).toEqual({ endpoint: 'check-slug', status: 200, body: { available: true } });
  });

  it('checkOrgSlug turns a rejected fetch or a non-JSON body into an unusable answer, never a throw', async () => {
    const offline = await checkOrgSlug('oldorg', async () => { throw new TypeError('Failed to fetch'); });
    expect(offline).toEqual({ endpoint: 'check-slug', status: 0, body: null });
    const html = await checkOrgSlug('oldorg', async () =>
      ({ status: 502, json: async () => { throw new SyntaxError('Unexpected token <'); } }) as Pick<Response, 'status' | 'json'>,
    );
    expect(html).toEqual({ endpoint: 'check-slug', status: 502, body: null });
    expect(decideStoredOrg({ stored: OLD, answer: offline, target: TARGET }).verdict).toBe('unverified');
  });
});
