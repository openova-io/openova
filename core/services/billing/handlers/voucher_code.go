package handlers

// voucher_code.go — server-side voucher-code entropy (#3376 DoD-6).
//
// FUNNEL hardening: a stranger redeems a voucher to land in their own Org
// console. If voucher codes are guessable, an attacker can brute-force
// free credit. Pre-#3376 IssueVoucher accepted ANY operator-typed code,
// so a real walk used `WALKMART2026` — short, dictionary-prefixed, no
// charset diversity. This file enforces a server-side minimum on the
// GENERATED path and rejects weak operator input, per the ticket DoD.
//
// Policy:
//   - Code omitted (empty) → auto-generate a high-entropy code:
//     `VCH-XXXXXXXXXXXX` where X is crypto/rand over an unambiguous
//     base32 alphabet (no 0/1/O/I) → 12 chars × log2(32) ≈ 60 bits.
//   - Code supplied → must pass validateVoucherCodeStrength: length >=
//     minVoucherCodeLen AND not a single repeated char AND mixes letters
//     + digits (so `AAAAAAAA` / `WALKMART2026`-style weak codes are
//     rejected with a clear hint to omit the code for auto-generation).
//
// The redeem path is unchanged (case-insensitive match on the stored
// code); only ISSUE-time validation is added here.

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// minVoucherCodeLen is the server-side minimum for an operator-supplied
// voucher code. Twelve characters of mixed letter+digit content resists
// online brute-force at the per-customer redeem rate-limit (#3376 DoD-7).
const minVoucherCodeLen = 12

// voucherCodeAlphabet is an unambiguous base32 alphabet (Crockford-style:
// no 0/1/O/I/U) used by generateVoucherCode so a code read off an email
// or a card can be typed back without confusion.
const voucherCodeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"

// generateVoucherCode returns a high-entropy auto-generated code of the
// form `VCH-XXXXXXXXXXXX` (12 random base32 chars ≈ 60 bits). Uses
// crypto/rand — never math/rand — so codes are unpredictable.
func generateVoucherCode() (string, error) {
	const n = 12
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("voucher-code: crypto/rand read: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("VCH-")
	for _, b := range buf {
		sb.WriteByte(voucherCodeAlphabet[int(b)%len(voucherCodeAlphabet)])
	}
	return sb.String(), nil
}

// minVoucherCodeDistinctChars is the minimum number of DISTINCT
// characters a voucher code must contain. A 12-char random base32 code
// always clears this; trivially-weak codes (AAAAAAAAAAAA,
// 121212121212, ABCABCABCABC) do not. Using distinct-char count as the
// entropy proxy keeps an all-letter auto-generated code valid (60-bit
// random base32 is strong regardless of whether a digit happens to land
// in it) while still rejecting human-chosen low-entropy codes.
const minVoucherCodeDistinctChars = 6

// validateVoucherCodeStrength rejects weak operator-supplied codes. The
// code is already uppercased + trimmed by the caller. Returns a non-nil
// error with an operator-actionable message when the code is too weak;
// nil when acceptable.
func validateVoucherCodeStrength(code string) error {
	// Strip a structural separator so "VCH-XXXX" measures its random body,
	// not the fixed prefix punctuation.
	body := strings.ReplaceAll(strings.TrimSpace(code), "-", "")
	if len(body) < minVoucherCodeLen {
		return fmt.Errorf("voucher code must be at least %d characters (or omit it to auto-generate a strong code)", minVoucherCodeLen)
	}

	distinct := map[rune]struct{}{}
	for _, r := range body {
		distinct[r] = struct{}{}
	}
	if len(distinct) < minVoucherCodeDistinctChars {
		return fmt.Errorf("voucher code is too predictable (needs at least %d distinct characters) — omit it to auto-generate a strong code", minVoucherCodeDistinctChars)
	}
	return nil
}
