// redeemDestination.test.ts — #5421 (UAT row 3).
//
// The existing `redeemOwnerGate.test.ts` asserts the gate's DECISION
// string ('consult-server'). That decision is now correct, and the tests
// pass — but the visitor's OUTCOME never changed, because the server the
// gate defers to cannot authenticate the probe. These tests assert the
// outcome instead: which page each persona actually lands on.
//
// The fake mesh below is not a convenience stub — it encodes the verified
// wire contract of the two services that actually receive the probe (see
// `orgMesh`). Hand-waving that contract (e.g. "the cookie authenticates
// the request") is exactly the assumption that let #5686 ship green, so it
// is written down here as executable, citable fact.

import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';

import {
  ownerProbeIsAuthenticable,
  resolveRedeemDestination,
  type RedeemOrgSummary,
} from './redeemDestination';

const LIVE_ORG: RedeemOrgSummary = {
  id: 'org-1',
  slug: 'acme',
  status: 'active',
  console_host: 'console.acme.t01.omani.works',
};

/**
 * The Organization mesh as it actually behaves on `marketplace.<sov>/api/`.
 *
 * Verified on `main`, not assumed:
 *   core/services/gateway/proxy.go            validateJWT reads only
 *     `Authorization: Bearer` and rejects when it is absent.
 *   core/services/shared/middleware/jwt.go    JWTAuth does the same, and
 *     additionally rejects any non-HMAC signing method.
 * Neither consults a cookie, so `catalyst_session` is accepted by the
 * browser onto the request and then discarded by the server.
 *
 * Live confirmation (read-only, hw292):
 *   GET https://marketplace.hw292.omani.works/api/tenant/orgs  -> HTTP 401
 */
function orgMesh(session: { token: string; consoleSessionCookie?: boolean }) {
  return async (): Promise<RedeemOrgSummary[]> => {
    if (!ownerProbeIsAuthenticable({
      token: session.token,
      hasConsoleSessionCookie: session.consoleSessionCookie,
    })) {
      throw Object.assign(new Error('unauthorized'), { status: 401 });
    }
    return [LIVE_ORG];
  };
}

describe('resolveRedeemDestination — which page the visitor lands on', () => {
  // ── CONTROL 1 ────────────────────────────────────────────────────────
  // An anonymous visitor must still reach the redeem funnel. Without this,
  // a naive "always hand off to the console" would satisfy the #5421 case.
  it('CONTROL: an anonymous visitor reaches the redeem funnel', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: '',
      fetchOrgs: orgMesh({ token: '' }),
    });
    expect(out.destination).toBe('funnel');
  });

  // ── CONTROL 2 ────────────────────────────────────────────────────────
  // The persona that already worked (signed up through the funnel, so the
  // marketplace origin holds an org-token) must keep working.
  it('CONTROL: a marketplace-origin (localStorage) owner is handed to the console', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: 'hs256-org-token',
      fetchOrgs: orgMesh({ token: 'hs256-org-token' }),
    });
    expect(out.destination).toBe('console');
    expect(out.reason).toBe('owner');
    expect(out.org?.slug).toBe('acme');
  });

  // ── CONTROL 3 ────────────────────────────────────────────────────────
  it('CONTROL: a demo/partner opt-out Organization stays on the marketplace', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: true,
      token: 'hs256-org-token',
      fetchOrgs: orgMesh({ token: 'hs256-org-token' }),
    });
    expect(out.destination).toBe('funnel');
    expect(out.reason).toBe('opt-out');
  });

  // ── CONTROL 4 ────────────────────────────────────────────────────────
  it('CONTROL: a signed-in visitor with no live Organization still reaches the funnel', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: 'hs256-org-token',
      fetchOrgs: async () => [{ id: 'o', slug: 's', status: 'deleted' }],
    });
    expect(out.destination).toBe('funnel');
    expect(out.reason).toBe('no-live-org');
  });

  // ── THE #5421 GAP, CLOSED BY #5940 ───────────────────────────────────
  // This was an `it.fails` tripwire whose comment said it would go RED the
  // moment the assertion started succeeding, and that whoever closed the
  // gap must promote it to `it()`. That is what #5940 does — so it is
  // promoted here, and it asserts the DESTINATION URL rather than merely
  // that a bounce occurred.
  //
  // Persona: an owner authenticated on the CONSOLE origin. There is still
  // no marketplace-origin token and the Organization mesh still cannot
  // authenticate the probe — nothing about that changed. What changed is
  // that catalyst-api now sets a readable `catalyst_session_hint` next to
  // the HttpOnly session, so the page never has to reach the probe.
  it('an owner carrying ONLY the console session is handed to the console (#5940)', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: '', // no marketplace-origin token — the owner authed on console.<sov>
      fetchOrgs: orgMesh({ token: '', consoleSessionCookie: true }),
      hint: { org: 'uatco' },
      host: 'marketplace.t92.omani.works',
    });
    expect(out.destination).toBe('console');
    expect(out.reason).toBe('owner-session-hint');
    // The row asserts the owner reaches /dashboard. Asserting only
    // `destination === 'console'` would pass on a bounce to anywhere.
    expect(out.url).toBe('https://console.uatco.t92.omani.works/dashboard');
  });

  it('a Sovereign-admin session (no Org slug) reaches the Sovereign console', async () => {
    // The exact hw292 2026-08-10 persona: tier `owner`, no Org scope.
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: '',
      fetchOrgs: orgMesh({ token: '', consoleSessionCookie: true }),
      hint: { org: '' },
      host: 'marketplace.t92.omani.works',
    });
    expect(out.url).toBe('https://console.t92.omani.works/dashboard');
  });

  it('the hint path never emits a token-bearing URL', () => {
    // A hint-borne visitor already holds the session cookie for the target
    // console, so there is nothing to hand off. Threading the (empty)
    // marketplace token through /launching would produce a handover with
    // no token to redeem.
    return resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: '',
      fetchOrgs: orgMesh({ token: '', consoleSessionCookie: true }),
      hint: { org: 'uatco' },
      host: 'marketplace.t92.omani.works',
    }).then((out) => {
      expect(typeof out.url).toBe('string');
      expect(out.url ?? '').not.toContain('token=');
      expect(out.url ?? '').not.toContain('/launching');
    });
  });

  // ── CONTROL 5: the check answers the other way ───────────────────────
  // Without a hint the SAME persona must still be funnelled, and for the
  // same recorded reason. This is what proves the assertions above are
  // driven by the hint and not by a gate that now always says 'console'.
  it('CONTROL: with NO hint, the cookie-only visitor is still funnelled as probe-unauthenticated', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: '',
      fetchOrgs: orgMesh({ token: '', consoleSessionCookie: true }),
      hint: null,
      host: 'marketplace.t92.omani.works',
    });
    expect(out.destination).toBe('funnel');
    expect(out.reason).toBe('probe-unauthenticated');
    expect(out.url).toBeUndefined();
  });

  it('CONTROL: an opt-out Organization is not bounced even WITH a hint', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: true,
      token: '',
      fetchOrgs: orgMesh({ token: '' }),
      hint: { org: 'uatco' },
      host: 'marketplace.t92.omani.works',
    });
    expect(out.destination).toBe('funnel');
    expect(out.reason).toBe('opt-out');
  });
});

// ── PAGE WIRING ────────────────────────────────────────────────────────
//
// The module above can be perfect while `redeem.astro` never passes it a
// hint — the shape that let #5686 ship green. These assertions read the
// REAL page source, so deleting the wiring fails here.

describe('redeem.astro actually consumes the hint (#5940)', () => {
  const pageSrc = readFileSync(
    join(__dirname, '..', 'pages', 'redeem.astro'),
    'utf8',
  );

  it('imports the hint reader and passes it into the resolver', () => {
    expect(pageSrc).toContain('currentSessionHint');
    expect(pageSrc).toMatch(/hint:\s*currentSessionHint\(\)/);
  });

  it('passes the host and the stamped console host, which the destination needs', () => {
    expect(pageSrc).toMatch(/host:\s*window\.location\.hostname/);
    expect(pageSrc).toContain('org-active-console-host');
  });

  it('navigates to the resolved URL rather than re-deriving one', () => {
    expect(pageSrc).toMatch(/window\.location\.replace\(outcome\.url\)/);
  });

  // CONTROL — proves these greps can report absence. If this ever passes as
  // present, the matcher is over-matching and the assertions above are
  // vacuous.
  it('CONTROL: a token that is NOT in the page is reported absent', () => {
    expect(pageSrc).not.toContain('currentSessionHint_RENAMED');
  });
});

describe('ownerProbeIsAuthenticable — the fact #5686 assumed away', () => {
  it('a console session cookie does NOT make the probe authenticable', () => {
    // The whole of #5686 rests on the opposite of this assertion. Neither
    // the Organization gateway nor the tenant middleware reads a cookie,
    // so presenting one changes nothing about whether the probe is
    // accepted. Flipping this to `true` requires a real server change.
    expect(
      ownerProbeIsAuthenticable({ token: '', hasConsoleSessionCookie: true }),
    ).toBe(false);
  });

  it('a marketplace-origin org-token does', () => {
    expect(ownerProbeIsAuthenticable({ token: 'hs256', hasConsoleSessionCookie: false })).toBe(true);
  });
});
