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

  // ── THE #5421 GAP ────────────────────────────────────────────────────
  // `it.fails` PASSES while the assertion inside fails, and goes RED the
  // moment the assertion starts succeeding. So this is a live tripwire,
  // not a disabled test: it records the exact acceptance criterion for
  // #5421 and forces whoever closes the gap to promote it to `it()`.
  //
  // Persona: an owner authenticated on the CONSOLE origin via the
  // passwordless-PIN flow. The `catalyst_session` cookie is scoped
  // `.<sov>` path `/`, so the browser DOES attach it to the same-origin
  // `/api/tenant/orgs` probe — but the Organization mesh never reads it.
  it.fails(
    'OPEN #5421: an owner carrying ONLY the console session cookie is handed to the console',
    async () => {
      const out = await resolveRedeemDestination({
        skipConsoleRedirect: false,
        token: '', // no marketplace-origin token — the owner authed on console.<sov>
        fetchOrgs: orgMesh({ token: '', consoleSessionCookie: true }),
      });
      expect(out.destination).toBe('console');
    },
  );

  // The characterisation of the same persona TODAY. This is what the
  // walk sees: the owner is funnelled, and the reason is NOT "anonymous"
  // — the probe simply could not be authenticated.
  it('records the current (defective) cookie-only-owner outcome as probe-unauthenticated', async () => {
    const out = await resolveRedeemDestination({
      skipConsoleRedirect: false,
      token: '',
      fetchOrgs: orgMesh({ token: '', consoleSessionCookie: true }),
    });
    expect(out.destination).toBe('funnel');
    expect(out.reason).toBe('probe-unauthenticated');
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
