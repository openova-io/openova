// redeemOwnerGate.test.ts — #5421 (UAT row 3).
//
// The decisive assertion is the cookie-only owner: token ABSENT + not
// opted out must still consult the server. Against the pre-fix gate
// (`funnel when !token || skip`) this case returned 'funnel' — the exact
// bug that showed an authed owner the anonymous signup funnel. Both the
// opt-out and the token-present paths are asserted too, so a gate that
// unconditionally returns 'consult-server' would not pass either.

import { describe, expect, it } from 'vitest';

import { redeemOwnerGate } from './redeemOwnerGate';

describe('redeemOwnerGate', () => {
  it('consults the server for a cookie-only owner (no marketplace-origin token) — #5421', () => {
    // The #5421 owner: authed on the console origin, so the marketplace
    // origin has no org-token, but the catalyst_session cookie is present.
    // Pre-fix this returned 'funnel' (owner trapped on the anonymous funnel).
    expect(redeemOwnerGate({ token: '', skipConsoleRedirect: false })).toBe('consult-server');
  });

  it('bails to the funnel for a demo/partner opt-out tenant regardless of session', () => {
    expect(redeemOwnerGate({ token: 'a-real-token', skipConsoleRedirect: true })).toBe('funnel');
    expect(redeemOwnerGate({ token: '', skipConsoleRedirect: true })).toBe('funnel');
  });

  it('consults the server when a token IS present and not opted out (unchanged owner path)', () => {
    expect(redeemOwnerGate({ token: 'a-real-token', skipConsoleRedirect: false })).toBe('consult-server');
  });
});
