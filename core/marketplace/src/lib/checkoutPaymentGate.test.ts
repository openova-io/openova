// UAT row 85 — a fully voucher-covered order must not show payment chrome.
//
// WHY THIS IS NOT "ASSERT THERE IS NO CARD FORM". There is no card form
// anywhere in the funnel to begin with: `stripe.js`, `@stripe/`, `loadStripe`,
// `cardnumber` and a payment `iframe` return ZERO hits across
// `core/marketplace/src`. A test asserting their absence would pass on an
// empty set, would have passed before this fix, and would therefore be
// worthless. The control at the bottom of this file pins that fact so nobody
// "strengthens" this suite back into that trap.
//
// What row 85 actually caught is the payment *chrome* one level up: the
// Apple Pay / Mastercard / Visa picker and the line "you'll be redirected to
// Stripe's PCI-compliant checkout", rendered beside `Due now OMR 0.000` and a
// `Launch my Organization` button. They were gated on `totalCost > 0`, which
// stays TRUE on a covered order because a voucher reduces `Due now`, not
// `totalCost`.
//
// So the assertion below is made against the TEMPLATE'S OWN GUARD, resolved
// through the component's `$derived` declarations and then EVALUATED for a
// covered order. It is not a string match on the fixed text, and it fails
// against the pre-fix `{#if totalCost > 0}` for the real reason: that
// expression evaluates true when credit covers the order.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { chargesCustomer, creditCoversOrder } from './checkoutPaymentGate';

// vitest runs with the package root as cwd (core/marketplace).
const CHECKOUT = join(process.cwd(), 'src/components/CheckoutStep.svelte');
const SOURCE = readFileSync(CHECKOUT, 'utf8');

/** A priced order, in baisa — mirrors the funnel's unit. */
const ORDER = 25_000;

// ---------------------------------------------------------------------------
// The predicates themselves.
// ---------------------------------------------------------------------------

describe('checkout payment gate — predicates', () => {
  it('credit that meets or exceeds the order covers it', () => {
    expect(creditCoversOrder(ORDER, ORDER)).toBe(true);
    expect(creditCoversOrder(ORDER, ORDER + 1)).toBe(true);
  });

  it('a zero-cost order is not "covered by credit" — there is nothing to cover', () => {
    expect(creditCoversOrder(0, 0)).toBe(false);
    expect(creditCoversOrder(0, 5_000)).toBe(false);
  });

  it('does not charge when credit covers the order', () => {
    expect(chargesCustomer(ORDER, ORDER)).toBe(false);
    expect(chargesCustomer(ORDER, ORDER * 2)).toBe(false);
  });

  it('charges when there is no credit, or credit falls short', () => {
    expect(chargesCustomer(ORDER, 0)).toBe(true);
    expect(chargesCustomer(ORDER, ORDER - 1)).toBe(true);
  });

  it('a failed credit lookup (balance 0) keeps the paying path — never a free launch', () => {
    // lib/api's getCreditBalance catch-path yields 0; that must fall through to
    // the charged flow rather than silently presenting a covered order.
    expect(chargesCustomer(ORDER, 0)).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Template block scanner — resolves which {#if} guards enclose a given piece
// of markup, so the assertions land on the COMPONENT'S CALL SITE and not just
// on the helper above (a helper-only test would pass with the template still
// rendering the tiles).
// ---------------------------------------------------------------------------

const TEMPLATE = SOURCE.slice(SOURCE.indexOf('</script>'));

const OPEN_BLOCK = /\{#(if|each|await|key|snippet)\b\s*([^]*?)\}\s*$/;
const CLOSE_BLOCK = /\{\/(if|each|await|key|snippet)\}/;
const ELSE_IF = /\{:else\s+if\s+([^]*?)\}\s*$/;
const ELSE_PLAIN = /\{:else\}/;

/**
 * Returns the stack of enclosing `{#if}` guard expressions for the first line
 * containing `needle`, outermost first. A `null` entry marks an `{:else}`
 * branch, whose condition is not a plain expression.
 */
function guardsEnclosing(needle: string): (string | null)[] {
  const lines = TEMPLATE.split('\n');
  const stack: { kind: string; guard: string | null }[] = [];
  for (const line of lines) {
    const trimmed = line.trim();
    if (trimmed.includes(needle)) {
      return stack.filter((f) => f.kind === 'if').map((f) => f.guard);
    }
    const close = trimmed.match(CLOSE_BLOCK);
    if (close) {
      stack.pop();
      continue;
    }
    const elseIf = trimmed.match(ELSE_IF);
    if (elseIf && stack.length) {
      stack[stack.length - 1].guard = elseIf[1].trim();
      continue;
    }
    if (ELSE_PLAIN.test(trimmed) && stack.length) {
      stack[stack.length - 1].guard = null;
      continue;
    }
    const open = trimmed.match(OPEN_BLOCK);
    if (open) {
      stack.push({ kind: open[1], guard: open[1] === 'if' ? open[2].trim() : null });
    }
  }
  throw new Error(`anchor not found in CheckoutStep.svelte template: ${needle}`);
}

/**
 * The `{#if}` guard immediately wrapping `needle` — the one that decides
 * whether that markup renders.
 *
 * Deliberately NOT "is some enclosing guard false": outer guards include an
 * `{:else}` branch (`null`), and counting a `null` as false would let every
 * assertion below pass without the payment gate existing at all.
 */
function innermostGuard(needle: string): string {
  const guards = guardsEnclosing(needle);
  const inner = guards[guards.length - 1];
  if (inner == null) {
    throw new Error(
      `expected an {#if} guard directly around "${needle}", found ${
        guards.length ? 'an {:else} branch' : 'no enclosing {#if} at all'
      }`,
    );
  }
  return inner;
}

/** `const NAME = $derived(EXPR);` declarations from the component's script. */
function derivedBindings(): Record<string, string> {
  const out: Record<string, string> = {};
  for (const m of SOURCE.matchAll(/const\s+(\w+)\s*=\s*\$derived\(([^]*?)\);/g)) {
    out[m[1]] = m[2].trim();
  }
  return out;
}

/**
 * Evaluates a template guard for a given (totalCost, creditBaisa), resolving a
 * bare `$derived` identifier to its declared expression first. Throws when the
 * guard references state this scenario does not model — which is itself a
 * meaningful failure, since the payment gate must depend only on cost+credit.
 */
function evaluateGuard(guard: string, totalCost: number, creditBaisa: number): boolean {
  const derived = derivedBindings();
  const expr = Object.prototype.hasOwnProperty.call(derived, guard) ? derived[guard] : guard;
  const fn = new Function(
    'totalCost',
    'creditBaisa',
    'creditCoversOrder',
    'chargesCustomer',
    `"use strict"; return Boolean(${expr});`,
  );
  return fn(totalCost, creditBaisa, creditCoversOrder, chargesCustomer);
}

// ---------------------------------------------------------------------------
// The call site.
// ---------------------------------------------------------------------------

const STRIPE_COPY = "you'll be redirected to Stripe's PCI-compliant checkout";
const PAYMENT_HEADING = '>Payment method<';
const TILES = ['aria-label="Apple Pay"', 'aria-label="Mastercard"', 'aria-label="Visa"'];
const VOUCHER_INPUT = 'bind:value={promoCode}';

describe('UAT row 85 — CheckoutStep hides payment chrome on a covered order', () => {
  it('the template anchors exist (guard is not scanning an empty set)', () => {
    // Without this, a renamed label would make every assertion below vacuous.
    for (const anchor of [STRIPE_COPY, PAYMENT_HEADING, VOUCHER_INPUT, ...TILES]) {
      expect(SOURCE, `missing anchor ${anchor}`).toContain(anchor);
    }
  });

  const CHROME: [string, string][] = [
    ['the Stripe redirect copy', STRIPE_COPY],
    ['the "Payment method" heading', PAYMENT_HEADING],
    ...TILES.map((t) => [`the ${t} tile`, t] as [string, string]),
  ];

  it.each(CHROME)('%s is not rendered when credit covers the order', (_label, anchor) => {
    // Pre-fix the guard here is `totalCost > 0`, which is TRUE on a covered
    // order (a voucher reduces `Due now`, not `totalCost`) — so the tiles and
    // the redirect line rendered beside `Due now OMR 0.000`. This is the
    // assertion that fails before the fix.
    const guard = innermostGuard(anchor);
    expect(
      evaluateGuard(guard, ORDER, ORDER),
      `guard \`${guard}\` still renders payment chrome when a voucher covers the order`,
    ).toBe(false);
  });

  it.each(CHROME)('%s IS still rendered on an ordinary paid order', (_label, anchor) => {
    // Counter-case: the fix must not hide payment chrome from a customer who
    // really is about to be charged. Without this, gating on a constant
    // `false` would satisfy the assertion above.
    const guard = innermostGuard(anchor);
    expect(
      evaluateGuard(guard, ORDER, 0),
      `guard \`${guard}\` hides payment chrome from a paying customer`,
    ).toBe(true);
  });

  it('the voucher input survives a covered order so a wrong code can be cleared', () => {
    // Regression pin: the blunt fix — gating the whole block on the new
    // predicate — would strand a customer who mistyped a code, because the
    // input would vanish the moment that code covered the total.
    const guard = innermostGuard(VOUCHER_INPUT);
    expect(
      evaluateGuard(guard, ORDER, ORDER),
      `guard \`${guard}\` hides the voucher input once credit covers the order`,
    ).toBe(true);
  });

  it('payment chrome is gated strictly deeper than the voucher input', () => {
    // Structural pin for the same regression: the chrome must live in a nested
    // block INSIDE the voucher block's guard, never share it.
    const voucher = guardsEnclosing(VOUCHER_INPUT);
    const chrome = guardsEnclosing(STRIPE_COPY);
    expect(chrome.length).toBeGreaterThan(voucher.length);
    expect(chrome.slice(0, voucher.length)).toEqual(voucher);
  });
});

// ---------------------------------------------------------------------------
// Control — see the header comment.
// ---------------------------------------------------------------------------

describe('control — why the obvious test would have been worthless', () => {
  it('the funnel has no card-collection surface at all, so asserting its absence proves nothing', () => {
    for (const token of ['stripe.js', '@stripe/', 'loadStripe', 'cardnumber']) {
      expect(SOURCE.toLowerCase()).not.toContain(token.toLowerCase());
    }
    // Documents the trap: this assertion held BEFORE the fix too, while the
    // page was still showing the picker and the redirect line.
  });
});
