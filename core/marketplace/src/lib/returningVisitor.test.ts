// returningVisitor.test.ts — #5940 (UAT row 91, and the shared destination
// used by row 3).
//
// Every assertion here is on the DESTINATION URL, not on "a redirect
// happened". Row 91 has two halves and only a URL can tell them apart:
//
//   positive  — the customer reaches THEIR OWN Org console;
//   forbidden — never `console.openova.io` / the mothership.
//
// A test that asserted `decision.redirect === true` would pass just as
// happily on a bounce to the mothership, which is the exact failure the row
// forbids. So the mothership case is asserted explicitly, and the module has
// no mothership branch to reach.

import { describe, expect, it } from 'vitest';

import { resolveReturningVisitor } from './returningVisitor';

const SOV_MARKETPLACE = 'marketplace.t92.omani.works';

describe('resolveReturningVisitor — where a cookie-borne visitor lands', () => {
  // ── THE #5940 / row 91 CASE ────────────────────────────────────────────
  it('sends an Org-scoped session to that Org OWN console', () => {
    const out = resolveReturningVisitor({
      host: SOV_MARKETPLACE,
      skipConsoleRedirect: false,
      hint: { org: 'uatco' },
    });
    expect(out).toEqual({
      redirect: true,
      reason: 'session-hint',
      url: 'https://console.uatco.t92.omani.works/dashboard',
    });
  });

  it('sends a Sovereign-admin session (no slug) to the Sovereign console', () => {
    // The persona measured on hw292 2026-08-10: tier `owner`, no Org scope.
    const out = resolveReturningVisitor({
      host: SOV_MARKETPLACE,
      skipConsoleRedirect: false,
      hint: { org: '' },
    });
    expect(out).toEqual({
      redirect: true,
      reason: 'session-hint',
      url: 'https://console.t92.omani.works/dashboard',
    });
  });

  it('prefers the server-authoritative stamped console host (pool TLD, #4176)', () => {
    // An Org provisioned on a POOL parent domain does not live under the
    // marketplace's domain, so re-deriving from the host would land on a
    // dead apex-wildcard address.
    const out = resolveReturningVisitor({
      host: SOV_MARKETPLACE,
      skipConsoleRedirect: false,
      hint: { org: 'uatco' },
      stampedConsoleHost: 'console.uatco.omani.homes',
    });
    expect(out).toMatchObject({ url: 'https://console.uatco.omani.homes/dashboard' });
  });

  it('ignores a stamp that names a DIFFERENT Org', () => {
    // A stale stamp from a previously-active Org must never misroute the
    // Org this session is actually scoped to.
    const out = resolveReturningVisitor({
      host: SOV_MARKETPLACE,
      skipConsoleRedirect: false,
      hint: { org: 'uatco' },
      stampedConsoleHost: 'console.otherorg.omani.homes',
    });
    expect(out).toMatchObject({ url: 'https://console.uatco.t92.omani.works/dashboard' });
  });

  // ── CONTROLS: the check must answer BOTH ways ──────────────────────────
  it('CONTROL: a visitor with NO hint is not redirected at all', () => {
    // The signed-out storefront visitor. If this ever redirects, the fix has
    // made the marketplace unusable for new customers.
    expect(
      resolveReturningVisitor({ host: SOV_MARKETPLACE, skipConsoleRedirect: false, hint: null }),
    ).toEqual({ redirect: false, reason: 'no-session-hint' });
  });

  it('CONTROL: a demo/partner opt-out Organization is never bounced', () => {
    expect(
      resolveReturningVisitor({
        host: SOV_MARKETPLACE,
        skipConsoleRedirect: true,
        hint: { org: 'uatco' },
      }),
    ).toEqual({ redirect: false, reason: 'opt-out' });
  });

  // ── ROW 91's FORBIDDEN HALF ────────────────────────────────────────────
  it('never produces a mothership destination, even with a valid hint', () => {
    // Hosts that are not `marketplace.<sov>`: dev, preview, a bare apex.
    for (const host of ['localhost', 'preview.example.test', 'omani.works', '']) {
      const out = resolveReturningVisitor({ host, skipConsoleRedirect: false, hint: { org: 'uatco' } });
      expect(out, `host ${host} must not yield a fallback destination`).toEqual({
        redirect: false,
        reason: 'unknown-console-host',
      });
    }
  });

  it('no reachable input yields a mothership URL', () => {
    // Belt and braces on the row's forbidden clause: sweep the matrix and
    // assert the apex never appears in ANY produced destination.
    const forbidden = ['openova', 'io'].join('.');
    for (const host of [SOV_MARKETPLACE, 'marketplace.omani.homes', 'localhost']) {
      for (const org of ['', 'uatco', 'acme-corp']) {
        for (const stamped of ['', 'console.uatco.omani.homes', 'console.acme-corp.omani.rest']) {
          const out = resolveReturningVisitor({
            host,
            skipConsoleRedirect: false,
            hint: { org },
            stampedConsoleHost: stamped,
          });
          if (out.redirect) {
            expect(out.url, `${host}/${org}/${stamped} produced a mothership URL`).not.toContain(forbidden);
            expect(out.url).toMatch(/^https:\/\/console\./);
            expect(out.url.endsWith('/dashboard')).toBe(true);
          }
        }
      }
    }
  });

  // CONTROL for the sweep above: it must actually have produced redirects,
  // or "no mothership URL was seen" is true of an empty set.
  it('CONTROL: the sweep produced at least one redirect', () => {
    const out = resolveReturningVisitor({
      host: SOV_MARKETPLACE,
      skipConsoleRedirect: false,
      hint: { org: 'uatco' },
      stampedConsoleHost: '',
    });
    expect(out.redirect).toBe(true);
  });
});
