// redeemDestination — the END-TO-END ownership decision for the voucher
// redeem page (#5421, UAT row 3).
//
// ── Why this module exists (and why redeemOwnerGate was not enough) ──
//
// `redeemOwnerGate` decides only the FIRST hop: "should the page ask the
// server at all?". PR #5686 fixed that hop so a cookie-borne owner no
// longer short-circuits to the funnel on a missing localStorage token —
// the gate now returns 'consult-server'.
//
// But 'consult-server' is a DECISION, not an OUTCOME. What the visitor
// actually sees is decided one hop later, by whether the server can
// answer. Testing the gate alone cannot see that, which is how #5686
// shipped green while the reported symptom survived untouched.
//
// This module models the WHOLE pipeline — gate, then server consult, then
// the owner hand-off — so a test can assert the thing the UAT row asserts:
// *which page does this persona land on*.
//
// ── The wire contract the probe actually meets ───────────────────────
//
// The redeem page runs on `marketplace.<sov>` and probes `/api/tenant/orgs`
// as a same-origin relative URL (`core/marketplace/src/lib/config.ts` —
// `API_BASE = ${BASE}api`). On a Sovereign, `marketplace.<sov>` `/api/`
// is routed by `products/catalyst/chart/templates/org-services/
// marketplace-routes.yaml` to the Organization mesh gateway
// (`org-services/gateway:8080`) — NOT to catalyst-api.
//
// Both hops on that path authenticate on the `Authorization: Bearer`
// header ONLY, and both require HS256:
//
//   core/services/gateway/proxy.go            validateJWT — reads
//     `r.Header.Get("Authorization")`, rejects a missing/!Bearer header.
//   core/services/shared/middleware/jwt.go    JWTAuth — same, plus
//     `if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok { … }`.
//
// Neither reads a cookie. `catalyst_session` is an RS256 cookie minted by
// catalyst-api for the CONSOLE origin; the browser does attach it to the
// same-origin probe (it is scoped `.<sov>` path `/`), but the receiving
// services discard it. So for a cookie-only owner the probe carries no
// credential either service accepts, and 401s — see REDEEM_OWNER_PROBE_
// AUTHENTICATOR below.
//
// That is why the outcome did not move: the gate says "ask the server",
// and the server says 401, and 401 lands in the same `runFunnel()` branch
// the old token short-circuit used.
//
// ── How #5940 closes it ──────────────────────────────────────────────
//
// Not by teaching the Organization mesh to read a cookie, and not by
// weakening `catalyst_session`. catalyst-api now sets a second,
// NON-HttpOnly cookie next to every session carrying a version and, at
// most, an Org slug (`products/catalyst/bootstrap/api/internal/handler/
// session_hint.go`). The redeem page reads THAT — synchronously, before
// any probe — and hands the owner to their console.
//
// The probe below is unchanged and still runs for everyone else, so the
// marketplace-origin (`org-token`) owner keeps the path they had. The
// hint only adds an answer where the probe had none:
//
//   hint present            -> console       (the #5940 persona)
//   no hint, token present  -> probe, as before
//   no hint, no token       -> funnel        (anonymous — unchanged)
//
// `ownerProbeIsAuthenticable` stays exactly as it was, and still returns
// false for a cookie-only visitor. That fact did not change and is not
// being papered over: the fix routes AROUND the probe rather than
// pretending the probe can now authenticate.

import { redeemOwnerGate } from './redeemOwnerGate';
import type { SessionHint } from './sessionHint';
import { resolveReturningVisitor } from './returningVisitor';

/** Where the redeem page sends the visitor. */
export type RedeemDestination = 'funnel' | 'console';

/**
 * WHY the visitor landed there. The reason is the part that makes the
 * #5421 gap observable: a cookie-borne owner and a genuinely anonymous
 * visitor both end on the funnel today, and without this discriminator
 * the two are indistinguishable in code, in tests, and in a walk.
 */
export type RedeemReason =
  /** Demo/partner Organization that opts out of the console hand-off. */
  | 'opt-out'
  /**
   * #5940: the readable `catalyst_session_hint` cookie identified a
   * signed-in visitor before any probe was needed. This is the persona
   * UAT row 3 walks — authed on the console origin, no marketplace-origin
   * token, previously shown the "Sign up to redeem" stranger form.
   */
  | 'owner-session-hint'
  /** Server confirmed >=1 live Organization — the owner path. */
  | 'owner'
  /** Server answered, but the visitor owns no live Organization yet. */
  | 'no-live-org'
  /**
   * The ownership probe could not be authenticated by the Organization
   * mesh. Today this covers BOTH a genuinely anonymous visitor AND the
   * #5421 cookie-borne owner, because the probe carries no credential
   * that mesh accepts either way.
   */
  | 'probe-unauthenticated'
  /** The probe reached the server but failed for some other reason. */
  | 'probe-error';

export interface RedeemOrgSummary {
  id?: string;
  slug?: string;
  status?: string;
  console_host?: string;
}

export interface RedeemOutcome {
  destination: RedeemDestination;
  reason: RedeemReason;
  /** Set only when destination === 'console'. */
  org?: RedeemOrgSummary;
  /**
   * The exact address to navigate to. Set only on the hint path, where
   * the visitor already holds a `catalyst_session` cookie for the target
   * console and therefore needs NO token hand-off — the browser presents
   * the session on the navigation itself. The probe path keeps using
   * `consoleLaunchHref`, which threads the marketplace-origin token
   * through the readiness interstitial.
   */
  url?: string;
}

/**
 * The credential the ownership probe presents to the Organization mesh.
 *
 * This is deliberately a NAMED, exported contract rather than an inline
 * `if (token)`, because it is the precise fact PR #5686 assumed away. The
 * probe is authenticable **iff** it can set an `Authorization: Bearer`
 * header — i.e. iff a marketplace-origin `org-token` exists. A
 * `catalyst_session` cookie, however valid, contributes nothing here:
 * neither `core/services/gateway/proxy.go` nor
 * `core/services/shared/middleware/jwt.go` reads cookies.
 *
 * Keep this in lockstep with `request()` in `./api.ts`, which sets the
 * header only when `localStorage['org-token']` is non-empty.
 */
export function ownerProbeIsAuthenticable(input: {
  /** marketplace-origin localStorage `org-token`. */
  token: string;
  /** `catalyst_session` cookie presence — accepted, and deliberately IGNORED. */
  hasConsoleSessionCookie?: boolean;
}): boolean {
  return !!input.token;
}

export interface RedeemDestinationDeps {
  /** Demo/partner opt-out (`__ORG_TENANT__.skipConsoleRedirect`). */
  skipConsoleRedirect: boolean;
  /** marketplace-origin localStorage `org-token` ('' for a cookie-only owner). */
  token: string;
  /** The active-Organization id previously stamped by the funnel, if any. */
  activeOrgId?: string;
  /**
   * Issues the `/api/tenant/orgs` probe. Rejects with an error carrying
   * `.status` on an HTTP error, mirroring `request()` in `./api.ts`.
   */
  fetchOrgs: () => Promise<RedeemOrgSummary[] | null | undefined>;
  /**
   * #5940 — the parsed `catalyst_session_hint` cookie, or null when the
   * visitor has none. Null keeps the pre-#5940 behaviour exactly.
   */
  hint?: SessionHint | null;
  /** `window.location.hostname`, used to compose the console host. */
  host?: string;
  /** `localStorage['org-active-console-host']` (#4176), when stamped. */
  stampedConsoleHost?: string;
}

/**
 * Resolve where the redeem page should send this visitor, mirroring the
 * decision `init()` in `../pages/redeem.astro` performs.
 */
export async function resolveRedeemDestination(
  deps: RedeemDestinationDeps,
): Promise<RedeemOutcome> {
  if (
    redeemOwnerGate({
      token: deps.token,
      skipConsoleRedirect: deps.skipConsoleRedirect,
    }) === 'funnel'
  ) {
    return { destination: 'funnel', reason: 'opt-out' };
  }

  // #5940 — the readable signal, checked BEFORE the probe. This is the
  // whole fix for UAT row 3: the owner measured on hw292 had a live
  // session and no marketplace-origin token, so every path below this
  // ended at the funnel for them no matter what it returned.
  //
  // `resolveReturningVisitor` owns the host derivation so the redeem page
  // and the Layout redirect cannot disagree about where "the console" is.
  const hinted = resolveReturningVisitor({
    host: deps.host || '',
    skipConsoleRedirect: deps.skipConsoleRedirect,
    hint: deps.hint ?? null,
    stampedConsoleHost: deps.stampedConsoleHost,
  });
  if (hinted.redirect) {
    return {
      destination: 'console',
      reason: 'owner-session-hint',
      url: hinted.url,
      org: deps.hint?.org ? { slug: deps.hint.org } : undefined,
    };
  }

  let orgs: RedeemOrgSummary[] | null | undefined;
  try {
    orgs = await deps.fetchOrgs();
  } catch (e) {
    const status = (e as { status?: number } | null)?.status;
    // 401/403 mean the mesh did not accept the probe's credentials. For a
    // cookie-borne owner that is not "anonymous" — it is #5421.
    const reason: RedeemReason =
      status === 401 || status === 403 ? 'probe-unauthenticated' : 'probe-error';
    return { destination: 'funnel', reason };
  }

  const live = Array.isArray(orgs)
    ? orgs.filter((o) => o && o.status !== 'deleted')
    : [];
  if (live.length === 0) {
    return { destination: 'funnel', reason: 'no-live-org' };
  }

  const pick =
    (deps.activeOrgId && live.find((o) => o.id === deps.activeOrgId)) || live[0];
  return { destination: 'console', reason: 'owner', org: pick };
}
