// redeemOwnerGate — the ownership pre-check gate for the voucher redeem
// page (#5421, UAT row 3).
//
// The bug this fixes: the redeem page decided "am I a returning owner?"
// from the marketplace-origin `localStorage['org-token']` alone, bailing
// straight to the anonymous signup funnel when that token was absent.
// But an owner authenticates on the CONSOLE origin
// (`console.<slug>.<sov>`), so the MARKETPLACE origin
// (`marketplace.<sov>`) legitimately has no `org-token` in its per-origin
// localStorage — no matter how valid the session is. The session that
// actually spans both subdomains is the `catalyst_session` cookie
// (scoped to `.<sov>` path `/`), which the same-origin `/api/tenant/orgs`
// request carries by default. Gating on the token therefore made the
// page structurally unable to see a cookie-borne owner, so an authed
// owner opening a redeem link was shown the anonymous funnel instead of
// being handed to their dashboard.
//
// The fix: the gate must NOT depend on the localStorage token. It only
// bails to the funnel for the demo/partner opt-out (skipConsoleRedirect
// tenants deliberately keep returning visitors ON the marketplace).
// Otherwise it returns 'consult-server' so init() asks getMyOrgs()
// (which carries the cookie) — and only falls back to the funnel when the
// server confirms no live Organization (unauthenticated / mid-signup) or
// the call errors. Anonymous visitors thus incur one extra /tenant/orgs
// probe that 401s and funnels; an owner is never trapped on the funnel.

export type RedeemGate = 'funnel' | 'consult-server';

export interface RedeemGateInput {
  /**
   * The marketplace-origin `org-token`. Deliberately NOT used to decide
   * the gate (see module doc) — it is accepted only so the call site and
   * this contract document that its absence must NOT short-circuit to the
   * funnel.
   */
  token: string;
  /** Demo/partner tenants that opt out of the console redirect entirely. */
  skipConsoleRedirect: boolean;
}

export function redeemOwnerGate(input: RedeemGateInput): RedeemGate {
  // Honour the same opt-out the shared Layout redirect respects: these
  // tenants deliberately keep returning visitors on the marketplace, so
  // never bounce them to a console — regardless of session.
  if (input.skipConsoleRedirect) return 'funnel';

  // #5421: do NOT gate on input.token. A cookie-only owner has no
  // marketplace-origin token yet must still be detected server-side, so
  // always consult the server. The caller runs the funnel only if the
  // server confirms no live Org (or the call errors).
  return 'consult-server';
}
