// UAT row 84 — the checkout sign-in step, in the halves that do not depend on
// whether the mail was delivered.
//
// WHY THESE ASSERTIONS AND NOT "DOES SIGN-IN WORK". Row 84's headline failure
// is that the sign-in mail is relayed through mail.openova.io (#5921) and can
// go missing, which no unit test can reach. Underneath that were two defects
// that bite even when the mail arrives perfectly, and those are pinned here:
// the page advertised twice the expiry the server actually granted, and the
// 6-digit screen had no way out. Both are checked against the component's OWN
// call sites, resolved from source, so they fail against the pre-fix template
// for the real reason rather than matching a fixed string.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { codeExpiryNotice, needsFreshCode, DEFAULT_CODE_TTL_SECONDS } from './checkoutSignIn';

// vitest runs with the package root as cwd (core/marketplace).
const CHECKOUT = join(process.cwd(), 'src/components/CheckoutStep.svelte');
const SOURCE = readFileSync(CHECKOUT, 'utf8');

// The Go handler that issues the code. Read as text on purpose — this suite's
// job includes catching the two sides drifting apart, so it must not learn the
// number from the same place the client does.
const AUTH_HANDLER = join(process.cwd(), '../services/auth/handlers/handlers.go');
const AUTH_SOURCE = readFileSync(AUTH_HANDLER, 'utf8');

/**
 * The verify-stage form only — the screen showing the 6 digit boxes.
 *
 * Sliced out rather than asserting against the whole component: the email
 * stage legitimately has a submit button and the launch stage legitimately
 * has navigation, and an assertion made against the whole file would pass on
 * either of those while the verify screen stayed a dead end.
 */
function verifyStageMarkup(): string {
  const ifLogin = SOURCE.indexOf("{#if authMode === 'login'}");
  const elseAt = SOURCE.indexOf('{:else}', ifLogin);
  const endAt = SOURCE.indexOf('{#if authError}', elseAt);
  return SOURCE.slice(elseAt, endAt);
}

// ---------------------------------------------------------------------------
// Controls — these prove the extraction above actually found the screen. If a
// refactor moves the markup, these fail loudly instead of letting every
// assertion below pass against an empty string.
// ---------------------------------------------------------------------------

describe('control — the verify stage is really being read', () => {
  it('locates a non-empty verify-stage block', () => {
    expect(verifyStageMarkup().length).toBeGreaterThan(200);
  });

  it('the block is the 6-digit screen and not some other branch', () => {
    const markup = verifyStageMarkup();
    expect(markup).toContain('PinInput6');
    expect(markup).toContain('Verify & Continue');
  });

  it('the Go constant is really being parsed, not silently missed', () => {
    expect(AUTH_SOURCE).toContain('magicCodeTTL');
    expect(goMagicCodeTTLSeconds()).not.toBeNull();
  });
});

/** Pulls `magicCodeTTL = N * time.Minute|Second` out of the Go source. */
function goMagicCodeTTLSeconds(): number | null {
  const m = AUTH_SOURCE.match(/magicCodeTTL\s*=\s*(\d+)\s*\*\s*time\.(Minute|Second)/);
  if (!m) return null;
  return m[2] === 'Minute' ? Number(m[1]) * 60 : Number(m[1]);
}

// ---------------------------------------------------------------------------
// Defect 1 — the page promised 10 minutes, the server gave 5.
// ---------------------------------------------------------------------------

describe('code expiry — the page must state what the server actually grants', () => {
  it('the client fallback equals the server TTL', () => {
    expect(DEFAULT_CODE_TTL_SECONDS).toBe(goMagicCodeTTLSeconds());
  });

  it('renders the server-reported expiry, not a baked-in number', () => {
    expect(codeExpiryNotice(600)).toBe('Codes expire after 10 minutes — check your spam folder.');
    expect(codeExpiryNotice(300)).toBe('Codes expire after 5 minutes — check your spam folder.');
    expect(codeExpiryNotice(60)).toBe('Codes expire after 1 minute — check your spam folder.');
  });

  it('never renders a zero or negative expiry', () => {
    for (const bad of [0, -1, NaN, undefined, null]) {
      expect(codeExpiryNotice(bad as number)).toBe(codeExpiryNotice(DEFAULT_CODE_TTL_SECONDS));
    }
  });

  // THE CALL-SITE ASSERTION. Pre-fix the template carried the literal
  // sentence, so the number was true only by coincidence and went stale the
  // moment the TTL moved. It must come from the helper instead.
  it('the verify screen derives its notice from the helper', () => {
    const markup = verifyStageMarkup();
    expect(markup).toContain('codeExpiryNotice');
    expect(markup).not.toContain('Codes expire after 10 minutes');
  });
});

// ---------------------------------------------------------------------------
// Defect 2 — an expired code was a dead end.
// ---------------------------------------------------------------------------

describe('expired code — the customer must be able to get a new one', () => {
  it('recognises the server wordings that mean the code is gone', () => {
    expect(needsFreshCode('invalid or expired code')).toBe(true);
    expect(needsFreshCode('code invalidated after too many attempts')).toBe(true);
  });

  it('does NOT push a resend for a merely mistyped code', () => {
    // The code is still live here; resending would throw away a working one.
    expect(needsFreshCode('invalid code')).toBe(false);
    expect(needsFreshCode('')).toBe(false);
    expect(needsFreshCode(null)).toBe(false);
  });

  // THE STATE-MACHINE ASSERTION. `authMode` went 'login' -> 'verify' and was
  // never set back anywhere in the component, so the 6-digit screen had no
  // exit at all. This fails against the pre-fix source because the string
  // `authMode = 'login'` does not occur in it even once.
  it('some path returns the component to the email stage', () => {
    expect(SOURCE).toMatch(/authMode\s*=\s*'login'/);
  });

  it('the verify screen itself offers that path', () => {
    const markup = verifyStageMarkup();
    expect(markup).toMatch(/requestNewCode|useDifferentEmail/);
  });

  it('the handler behind that control really resets the stage', () => {
    // Guards against wiring the button to a no-op: the function the verify
    // screen calls must be the one that assigns 'login'.
    const handler = SOURCE.match(/function requestNewCode\([^)]*\)\s*\{[\s\S]*?\n  \}/);
    expect(handler).not.toBeNull();
    expect(handler![0]).toMatch(/authMode\s*=\s*'login'/);
  });
});
