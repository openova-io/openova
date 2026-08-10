// UAT row 84 — the stranger's sign-in at checkout: enter email, get a code,
// type it, end up signed in with the Org confirmed.
//
// Two things about that step were wrong in a way that had nothing to do with
// whether the mail itself got delivered, and both bit at the last screen
// before a purchase.
//
// 1. THE PAGE PROMISED TEN MINUTES AND THE SERVER GAVE FIVE. The notice under
//    the 6-digit boxes read "Codes expire after 10 minutes" as a hardcoded
//    sentence, while core/services/auth issued codes with a 5-minute TTL. A
//    customer who took six minutes — ordinary when the sign-in mail is relayed
//    off-Sovereign (#5921) and can queue — typed a code the page still showed
//    as good and was told it was invalid. The server constant moved to 10 to
//    match, and the send response now reports the expiry so this page can
//    render the truth rather than a sentence nobody re-checks.
//
// 2. AN EXPIRED CODE WAS A DEAD END. `authMode` went 'login' -> 'verify' when
//    the code was sent and was never set back, anywhere. There was no resend,
//    no "wrong address?", no way back to the email field. Once the code
//    expired — which, per (1), happened twice as fast as advertised — every
//    further attempt returned "invalid or expired code", the 5-attempt cap
//    then invalidated the code outright, and the only escape was reloading the
//    page. The customer's own words for this is that the site is broken.
//
// These helpers are the shared definition of both, so the component and the
// test that pins its call sites agree by construction.

/**
 * The code lifetime this page assumes when the server does not report one —
 * an older `POST /auth/magic-link` that predates the `expires_in_sec` field.
 *
 * MUST equal `magicCodeTTL` in core/services/auth/handlers/handlers.go.
 * `checkoutSignIn.test.ts` reads that constant out of the Go source and fails
 * if these two drift, which is the whole reason the number is named here
 * instead of being written into a sentence.
 */
export const DEFAULT_CODE_TTL_SECONDS = 600;

/**
 * The sentence shown under the 6-digit boxes.
 *
 * Derived from the server's reported expiry so it cannot go stale the next
 * time the TTL moves. A non-positive or missing value falls back to
 * `DEFAULT_CODE_TTL_SECONDS` rather than rendering "0 minutes".
 */
export function codeExpiryNotice(expiresInSec?: number | null): string {
  const seconds =
    typeof expiresInSec === 'number' && Number.isFinite(expiresInSec) && expiresInSec > 0
      ? expiresInSec
      : DEFAULT_CODE_TTL_SECONDS;
  const minutes = Math.max(1, Math.round(seconds / 60));
  const unit = minutes === 1 ? 'minute' : 'minutes';
  return `Codes expire after ${minutes} ${unit} — check your spam folder.`;
}

/**
 * `true` when the error the verify call came back with means the code is gone
 * (expired, or burned by the attempt cap) rather than merely mistyped.
 *
 * The server's wording for these is "invalid or expired code" and "code
 * invalidated after too many attempts" (core/services/auth/handlers/
 * handlers.go). In both cases retyping cannot possibly work and the customer
 * needs a fresh code — so the page leads with that instead of leaving them to
 * guess. A plain "invalid code" (wrong digits, code still live) is NOT in this
 * set: there the right move is to retype, and pushing a resend would throw
 * away a code that still works.
 */
export function needsFreshCode(message: string | null | undefined): boolean {
  if (!message) return false;
  const m = message.toLowerCase();
  return m.includes('expired') || m.includes('invalidated');
}
