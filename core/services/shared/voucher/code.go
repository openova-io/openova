// Package voucher — server-side voucher-code entropy (#3376 DoD-6),
// factored out so every path that issues a voucher shares ONE
// implementation of the strength policy (no divergent copies).
//
// Consumers:
//   - core/services/billing/handlers (the Organization billing service's
//     IssueVoucher — the canonical persist path).
//   - products/catalyst/bootstrap/api/internal/handler (the catalyst-api
//     console BSS `/api/v1/org/billing/vouchers/issue` proxy). #4914: that
//     proxy previously streamed the operator-supplied code straight to the
//     upstream with no edge check, so a weak custom code was accepted on
//     the console path (and, on a Sovereign whose billing image predates
//     #3376, would persist). Both paths now call ValidateCodeStrength.
//
// FUNNEL hardening rationale: a stranger redeems a voucher to land in
// their own Org console. If voucher codes are guessable, an attacker can
// brute-force free credit. The GENERATED path is high-entropy by default;
// an operator-SUPPLIED code must clear the minimum below.
//
// Policy:
//   - Code omitted (empty) → auto-generate a high-entropy code via
//     GenerateCode: `VCH-XXXXXXXXXXXX` where X is crypto/rand over an
//     unambiguous base32 alphabet (no 0/1/O/I/U) → 12 chars × log2(30)
//     ≈ 60 bits.
//   - Code supplied → must pass ValidateCodeStrength: length >= MinCodeLen
//     AND at least MinDistinctChars distinct characters (so AAAAAAAAAAAA /
//     ABABABABABAB-style weak codes are rejected with a clear hint to omit
//     the code for auto-generation).
//
// The redeem path is unchanged (case-insensitive match on the stored
// code); only ISSUE-time validation lives here.
package voucher

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// MinCodeLen is the server-side minimum for an operator-supplied voucher
// code. Twelve characters of mixed content resists online brute-force at
// the per-customer redeem rate-limit (#3376 DoD-7).
const MinCodeLen = 12

// CodeAlphabet is an unambiguous base32 alphabet (Crockford-style: no
// 0/1/O/I/U) used by GenerateCode so a code read off an email or a card
// can be typed back without confusion.
const CodeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// MinDistinctChars is the minimum number of DISTINCT characters a voucher
// code must contain. A 12-char random base32 code always clears this;
// trivially-weak codes (AAAAAAAAAAAA, 121212121212, ABCABCABCABC) do not.
// Using distinct-char count as the entropy proxy keeps an all-letter
// auto-generated code valid (60-bit random base32 is strong regardless of
// whether a digit happens to land in it) while still rejecting
// human-chosen low-entropy codes.
const MinDistinctChars = 6

// GenerateCode returns a high-entropy auto-generated code of the form
// `VCH-XXXXXXXXXXXX` (12 random base32 chars ≈ 60 bits). Uses crypto/rand
// — never math/rand — so codes are unpredictable.
func GenerateCode() (string, error) {
	const n = 12
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("voucher-code: crypto/rand read: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("VCH-")
	for _, b := range buf {
		sb.WriteByte(CodeAlphabet[int(b)%len(CodeAlphabet)])
	}
	return sb.String(), nil
}

// ValidateCodeStrength rejects weak operator-supplied codes. Callers
// should uppercase + trim the code first (matching the stored-code
// convention); ValidateCodeStrength also trims defensively. Returns a
// non-nil error with an operator-actionable message when the code is too
// weak; nil when acceptable.
func ValidateCodeStrength(code string) error {
	// Strip a structural separator so "VCH-XXXX" measures its random body,
	// not the fixed prefix punctuation.
	body := strings.ReplaceAll(strings.TrimSpace(code), "-", "")
	if len(body) < MinCodeLen {
		return fmt.Errorf("voucher code must be at least %d characters (or omit it to auto-generate a strong code)", MinCodeLen)
	}

	distinct := map[rune]struct{}{}
	for _, r := range body {
		distinct[r] = struct{}{}
	}
	if len(distinct) < MinDistinctChars {
		return fmt.Errorf("voucher code is too predictable (needs at least %d distinct characters) — omit it to auto-generate a strong code", MinDistinctChars)
	}
	return nil
}
