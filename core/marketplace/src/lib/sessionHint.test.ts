// sessionHint.test.ts — #5940 (UAT rows 3 + 91).
//
// The hint is the ONLY thing standing between a signed-in customer and the
// stranger signup form, so two properties matter and both are asserted with
// a control that proves the check answers the other way too:
//
//   - a real hint is READ (otherwise the fix is inert);
//   - a hint that is absent, stale-versioned, over-long or credential-shaped
//     is REFUSED (otherwise a signed-out visitor gets bounced, which is a
//     worse outcome than the bug).
//
// The slug matters more than it looks: the caller splices it into a HOSTNAME.
// A value containing a dot, a slash or a colon could point a customer off the
// Sovereign entirely, so those cases are pinned individually rather than
// covered by one "invalid input" case.

import { describe, expect, it } from 'vitest';

import {
  SESSION_HINT_COOKIE,
  parseSessionHintValue,
  readSessionHint,
} from './sessionHint';

describe('parseSessionHintValue', () => {
  it('reads a Sovereign-admin hint (no Org slug)', () => {
    expect(parseSessionHintValue('v=1')).toEqual({ org: '' });
  });

  it('reads an Org-scoped hint and returns the slug', () => {
    // Row 91 needs the slug: without it the marketplace can only reach the
    // Sovereign console, not the customer's OWN Org console.
    expect(parseSessionHintValue('org=acme&v=1')).toEqual({ org: 'acme' });
    expect(parseSessionHintValue('v=1&org=acme-corp')).toEqual({ org: 'acme-corp' });
  });

  // ── refusals ───────────────────────────────────────────────────────────
  it('refuses an absent or empty value', () => {
    expect(parseSessionHintValue(undefined)).toBeNull();
    expect(parseSessionHintValue(null)).toBeNull();
    expect(parseSessionHintValue('')).toBeNull();
    expect(parseSessionHintValue('   ')).toBeNull();
  });

  it('refuses an unknown payload version', () => {
    // A future server that changes the shape must not have this reader
    // guessing at it.
    expect(parseSessionHintValue('v=2&org=acme')).toBeNull();
    expect(parseSessionHintValue('org=acme')).toBeNull();
  });

  it('refuses a JWT-shaped value — the hint is never a token', () => {
    expect(parseSessionHintValue('eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJhIn0.c2ln')).toBeNull();
    // Even carrying the right version, a dotted 3-segment value is refused:
    // the server never emits one, so it is a smuggling attempt or a bug.
    expect(parseSessionHintValue('v=1&x=a.b.c')).toBeNull();
  });

  it('refuses an over-long value', () => {
    expect(parseSessionHintValue('v=1&org=' + 'a'.repeat(200))).toBeNull();
  });

  it('drops a slug that is not a legal DNS label, keeping the session signal', () => {
    // These would otherwise be spliced into a hostname. Degrading to
    // {org:''} sends the visitor to the Sovereign console — never off-host.
    for (const bad of [
      'acme/evil',              // a slash would inject a path
      'acme:8080',              // a colon would inject a port
      '-acme',                  // illegal leading hyphen
      'acme-',                  // illegal trailing hyphen
      'ACME_CORP',              // underscore is not a DNS label char
      'ácme',                   // non-ASCII
    ]) {
      expect(
        parseSessionHintValue(`v=1&org=${encodeURIComponent(bad)}`),
        `slug ${bad} must not survive into a hostname`,
      ).toEqual({ org: '' });
    }
  });

  it('refuses the WHOLE hint when a dotted host is smuggled into the slug', () => {
    // A slug carrying a fully-qualified host trips the earlier
    // JWT-shape/dot refusal, so the hint is rejected outright rather than
    // degraded. Stricter than the case above, and asserted separately so the
    // two layers cannot be confused for one.
    expect(parseSessionHintValue(`v=1&org=${encodeURIComponent('evil.example.test')}`)).toBeNull();
  });
});

describe('readSessionHint — cookie-string parsing', () => {
  it('finds the hint among other cookies', () => {
    expect(readSessionHint(`org-theme=dark; ${SESSION_HINT_COOKIE}=org=acme%26v=1; other=x`))
      .toEqual({ org: 'acme' });
  });

  it('returns null when no cookies are present at all', () => {
    // THE MEASURED STATE on the marketplace origin before this fix:
    // document.cookie was empty, which is why redirectCount was 0.
    expect(readSessionHint('')).toBeNull();
    expect(readSessionHint(undefined)).toBeNull();
  });

  // CONTROL — the two cookie names share a prefix. A substring match would
  // read `catalyst_session` (HttpOnly, hence never present) and quietly
  // return null for every real visitor.
  it('CONTROL: does not mistake catalyst_session for the hint', () => {
    expect(readSessionHint('catalyst_session=v=1')).toBeNull();
    expect(readSessionHint('catalyst_session_hint_extra=v=1')).toBeNull();
  });

  it('CONTROL: an unrelated cookie jar yields no hint', () => {
    expect(readSessionHint('org-cart=%5B%5D; org-pending-voucher=ABC123')).toBeNull();
  });
});
