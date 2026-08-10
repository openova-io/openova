// sessionHint — the marketplace's readable "a session exists" signal
// (#5940, UAT rows 3 + 91).
//
// ── The fact this module exists to work around ───────────────────────
//
// `catalyst_session` is HttpOnly, and it must stay that way. The
// marketplace is a STATIC build on a SEPARATE host with client-side
// auth, so its JS cannot see that cookie: `document.cookie` reads EMPTY
// even on the console's own origin. Measured on hw292 2026-08-10 — an
// owner whose session was live (whoami 200, tier `owner`, seconds later)
// opened their own voucher-redeem link and got the "Sign up to redeem"
// stranger form, `redirectCount` 0. The marketplace root did nothing at
// all for the same visitor.
//
// catalyst-api now sets a second, NON-HttpOnly cookie next to every
// session (`products/catalyst/bootstrap/api/internal/handler/
// session_hint.go`). This module reads it.
//
// ── What a hint is, and what it is emphatically NOT ──────────────────
//
// It is a ROUTING signal, never a credential. Everything it can do is
// send the browser to a console — and that console then authenticates
// the visitor against the HttpOnly session exactly as it always did. A
// forged hint therefore buys an attacker one thing: a sign-in page.
//
// So this parser is deliberately paranoid in ONE direction only: it
// refuses to believe anything it cannot use safely. The `org` value is
// spliced into a hostname by the caller, so it is accepted only when it
// is a legal DNS label — a dot, a slash or a colon in that position
// could redirect a visitor off-Sovereign, which is the one outcome that
// would actually matter.

/** The cookie catalyst-api sets alongside every `catalyst_session`. */
export const SESSION_HINT_COOKIE = 'catalyst_session_hint';

/** The only payload version this reader understands. */
const SUPPORTED_VERSION = '1';

/**
 * A whole hint cannot be longer than this. The real value is ~15 bytes
 * (`org=<slug>&v=1`); anything approaching a token's length is something
 * this reader should refuse rather than try to interpret.
 */
const MAX_HINT_LENGTH = 128;

/** A legal lowercase DNS label — the only shape an Org slug can take. */
const DNS_LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;

export interface SessionHint {
  /**
   * The Organization the session is scoped to, or '' for a
   * Sovereign-admin session (which is scoped to the Sovereign itself and
   * has no Org slug to carry).
   */
  org: string;
}

/**
 * Parse a raw hint cookie VALUE.
 *
 * Returns null — meaning "treat this visitor as signed out" — when the
 * value is absent, over-long, of an unknown version, or shaped like a
 * credential. Returning null is always safe: the caller then renders the
 * public funnel, which is the behaviour that shipped before this fix.
 */
export function parseSessionHintValue(raw: string | null | undefined): SessionHint | null {
  if (!raw) return null;
  const value = raw.trim();
  if (!value || value.length > MAX_HINT_LENGTH) return null;

  // A dotted 3-segment value is JWT-shaped. The server never emits one,
  // so seeing one means either a bug or something being smuggled through
  // this channel — refuse either way rather than parse around it.
  if (value.split('.').length >= 3) return null;

  let params: URLSearchParams;
  try {
    params = new URLSearchParams(value);
  } catch {
    return null;
  }

  if (params.get('v') !== SUPPORTED_VERSION) return null;

  const org = (params.get('org') || '').trim().toLowerCase();
  // An unusable slug degrades to a session-exists-only hint — never to a
  // rejected hint, and never to a slug we splice into a host unchecked.
  return { org: DNS_LABEL.test(org) ? org : '' };
}

/**
 * Read the hint out of a `document.cookie` string.
 *
 * Cookie parsing is done by hand rather than with a regex over the whole
 * string because `catalyst_session_hint` and `catalyst_session` share a
 * prefix: a substring match would happily read the wrong cookie, and the
 * wrong cookie here is the HttpOnly one that will never be present.
 */
export function readSessionHint(cookieString: string | null | undefined): SessionHint | null {
  if (!cookieString) return null;
  for (const part of cookieString.split(';')) {
    const eq = part.indexOf('=');
    if (eq < 0) continue;
    if (part.slice(0, eq).trim() !== SESSION_HINT_COOKIE) continue;
    return parseSessionHintValue(decodeURIComponent(part.slice(eq + 1).trim()));
  }
  return null;
}

/** Browser-side convenience. Safe in SSR (returns null, no `document`). */
export function currentSessionHint(): SessionHint | null {
  if (typeof document === 'undefined') return null;
  try {
    return readSessionHint(document.cookie);
  } catch {
    return null;
  }
}
