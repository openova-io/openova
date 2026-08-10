package handlers

import (
	"testing"
	"time"
)

// UAT row 84 — "type the emailed sign-in code" at checkout.
//
// THE DEFECT THIS PINS. The marketplace checkout page tells the customer, in
// so many words, "Codes expire after 10 minutes" (core/marketplace/src/
// components/CheckoutStep.svelte, under the 6-digit entry). The code this
// handler issues died after FIVE. A customer who read the page and took six
// minutes to fetch the mail — entirely normal when the sign-in mail is relayed
// off-Sovereign (#5921) and can queue — typed a code the page still presented
// as valid and got "invalid or expired code" back, at the last step before
// purchase. Nothing about that failure tells them to ask for a new one.
//
// WHY 10 IS THE CORRECT SIDE TO MOVE, not the copy. Three independent
// surfaces already state 10 minutes and only this constant disagreed:
//
//   1. products/catalyst/bootstrap/api/internal/handler/pinstore.go — the
//      console PIN path, `const pinTTL = 10 * time.Minute`. Same 6-digit
//      credential, same purpose, same 5-attempt cap. It is the shipped
//      reference implementation.
//   2. Every customer-facing string: CheckoutStep.svelte, VerifyPinPage.tsx
//      and PinSignInModal.tsx all say 10 minutes.
//   3. The OAuth CSRF state token minted in THIS FILE (GoogleAuth, `Ex(10 *
//      time.Minute)`) — so 10 minutes was already this service's own idea of
//      a short-lived interactive credential.
//
// Moving the copy to 5 instead would have made the marketplace the only
// sign-in surface in the platform with a different expiry, and would have made
// the mail-relay latency problem worse rather than better.
//
// NOT A SECURITY LOOSENING. The brute-force budget is unchanged: a code is
// still invalidated after `magicMaxAttempts` wrong guesses (5), and
// `checkMagicRateLimit` still caps a source IP at `magicRateMax` verifies per
// `magicRateWindow`. Doubling the window doubles the attempts an attacker may
// make against one code only if they are willing to be locked out after 5 —
// which is the same bound catalyst-api has shipped with at 10 minutes.

func TestMagicCodeTTLMatchesTheCustomerFacingPromise(t *testing.T) {
	// The number of minutes the funnel's checkout page promises the customer.
	// Kept as a literal on purpose: this test's whole job is to fail when the
	// server drifts away from what the page says, so it must not read the
	// number from the same place the server does.
	const promisedToCustomer = 10 * time.Minute

	if magicCodeTTL != promisedToCustomer {
		t.Errorf(
			"magicCodeTTL = %v, but the checkout page tells the customer their code is good for %v.\n"+
				"A customer who believes the page has %v to use a code the server killed at %v.\n"+
				"Fix the constant, or fix every 'Codes expire after 10 minutes' string in the funnel and the console.",
			magicCodeTTL, promisedToCustomer, promisedToCustomer, magicCodeTTL,
		)
	}
}

// TestMagicCodeTTLMatchesConsolePinTTL keeps the two sign-in paths from
// drifting apart again. catalyst-api's pinstore.go is the reference; a
// customer signing in at checkout and a sovereign-admin signing in at the
// console are doing the same thing with the same kind of credential and must
// get the same window.
//
// The reference value is restated here rather than imported because
// catalyst-api is a separate Go module (products/catalyst/bootstrap/api) with
// no dependency edge to this one — importing it to share a constant would
// couple the marketplace auth service to the whole provisioning API.
func TestMagicCodeTTLMatchesConsolePinTTL(t *testing.T) {
	// products/catalyst/bootstrap/api/internal/handler/pinstore.go:31
	const consolePinTTL = 10 * time.Minute

	if magicCodeTTL != consolePinTTL {
		t.Errorf(
			"magicCodeTTL = %v but the console PIN path uses %v (pinstore.go). "+
				"Two sign-in surfaces, one credential type, two expiry windows — pick one.",
			magicCodeTTL, consolePinTTL,
		)
	}
}

// TestMagicCodeTTLIsReportedToTheClient pins the mechanism that stops this
// drifting a third time: the send response carries the expiry, so the page can
// render the truth instead of a hardcoded sentence that nobody re-checks when
// the constant moves.
//
// This asserts on the VALUE the handler would put on the wire, not merely that
// some field exists — a test for the field's presence would pass on a zero.
func TestMagicCodeTTLIsReportedToTheClient(t *testing.T) {
	got := magicCodeExpiresInSec()

	if got == 0 {
		t.Fatal("magicCodeExpiresInSec() = 0 — the client would render a '0 minutes' expiry notice")
	}
	if want := int(magicCodeTTL.Seconds()); got != want {
		t.Errorf("magicCodeExpiresInSec() = %d, want %d (magicCodeTTL = %v)", got, want, magicCodeTTL)
	}
}
