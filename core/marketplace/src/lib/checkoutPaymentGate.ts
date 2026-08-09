// UAT row 85 — what the checkout page may show a customer whose voucher
// already covers the whole order.
//
// The funnel has NO card form on any path: `stripe.js`, `@stripe/`,
// `loadStripe`, a `cardnumber` field and a payment `iframe` are all absent
// from `core/marketplace/src`. So "the checkout collects no card details" is
// true by construction and asserting it proves nothing. The defect row 85
// actually catches is one level up — the page still renders the payment
// *chrome*: the Apple Pay / Mastercard / Visa picker and the line "you'll be
// redirected to Stripe's PCI-compliant checkout", sitting directly beside
// `Due now OMR 0.000` and a `Launch my Organization` button that goes nowhere
// near Stripe. That is a contradiction the customer has to resolve, and it is
// what made the row fail.
//
// These predicates are the single definition of "will this order actually
// charge the customer", shared by the component's derived state and by the
// test that pins the template's call site.

/**
 * `true` when available credit fully covers the order, so no payment
 * instrument is involved at all.
 *
 * Both figures are in **baisa** (the billing API's unit) — never mix in an
 * OMR-denominated value here.
 *
 * A zero-cost order is NOT "covered by credit": there is nothing to cover.
 * That case is handled separately by the caller (the button already reads
 * `totalCost === 0 || creditCovers`), and keeping it out of this predicate is
 * what lets `chargesCustomer` stay a clean negation.
 */
export function creditCoversOrder(totalCost: number, creditBaisa: number): boolean {
  return totalCost > 0 && creditBaisa >= totalCost;
}

/**
 * `true` only when the customer will genuinely be charged — i.e. the order
 * costs something AND credit does not cover it.
 *
 * This is the gate for the payment-method picker and the Stripe redirect copy.
 * The pre-fix template gated them on `totalCost > 0` alone, which stays true
 * for a fully-covered order (the voucher reduces `Due now`, not `totalCost`),
 * so the card chrome rendered next to a zero balance.
 *
 * Deliberately NOT the gate for the voucher/promo-code input: a voucher is
 * additive credit, not a payment method, and hiding its input the instant the
 * code covers the total would strand a customer who mistyped a code with no
 * way to clear it.
 */
export function chargesCustomer(totalCost: number, creditBaisa: number): boolean {
  return totalCost > 0 && !creditCoversOrder(totalCost, creditBaisa);
}
