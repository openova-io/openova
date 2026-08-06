// redeemOwnerGate.test.ts — #5421 (UAT row 3).
//
// SCOPE — read this before citing these tests as evidence for #5421.
//
// They assert the gate's DECISION only: "should the page ask the server?".
// They do NOT show that a cookie-borne owner is recognised. That outcome is
// decided one hop later, by whether the Organization mesh can authenticate
// the probe — and it cannot, so such an owner is still funnelled. See
// `redeemDestination.test.ts`, which asserts the OUTCOME and carries the
// `it.fails` tripwire for the still-open #5421 gap.
//
// The cookie-only case below returns 'funnel' against the pre-#5686 gate
// (`funnel when !token || skip`) and 'consult-server' now, so it does record
// a real change — just not one the visitor can see. Both the opt-out and the
// token-present paths are asserted too, so a gate that unconditionally
// returns 'consult-server' would not pass either.

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
