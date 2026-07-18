/**
 * voucherCodePolicy.ts — client-side mirror of the SERVER voucher-code
 * strength policy (UAT rows 73/74, issue #5223).
 *
 * Source of truth: core/services/shared/voucher/code.go (#3376 DoD-6,
 * edge-enforced by catalyst-api #4914):
 *   • code OMITTED → the server auto-generates a high-entropy
 *     `VCH-XXXXXXXXXXXX` (~60 bits) — an EMPTY field is VALID (row 74: the
 *     old client-side "Voucher code is required." block made the
 *     auto-generate path UI-unreachable).
 *   • code SUPPLIED → hyphen-stripped body must be >= VOUCHER_CODE_MIN_LEN
 *     chars AND carry >= VOUCHER_CODE_MIN_DISTINCT distinct characters
 *     (row 73: a weak `1234` previously reached the server with no inline
 *     rejection).
 * The server remains the authority — this is the same rule surfaced early,
 * with the same remediation hint (leave blank to auto-generate).
 */

/** Server-side minimum length for an operator-supplied voucher code
 *  (voucher.MinCodeLen). */
export const VOUCHER_CODE_MIN_LEN = 12

/** Server-side minimum DISTINCT characters (voucher.MinDistinctChars). */
export const VOUCHER_CODE_MIN_DISTINCT = 6

/** validateVoucherCodeStrength — null when acceptable (or empty =
 *  auto-generate); otherwise the operator-actionable inline message. */
export function validateVoucherCodeStrength(raw: string): string | null {
  const code = raw.trim().toUpperCase()
  if (code === '') return null // auto-generate path — valid
  const body = code.replace(/-/g, '')
  if (body.length < VOUCHER_CODE_MIN_LEN) {
    return `Voucher code must be at least ${VOUCHER_CODE_MIN_LEN} characters — or leave it blank to auto-generate a strong code.`
  }
  const distinct = new Set(body.split(''))
  if (distinct.size < VOUCHER_CODE_MIN_DISTINCT) {
    return `Voucher code is too predictable (needs at least ${VOUCHER_CODE_MIN_DISTINCT} distinct characters) — leave it blank to auto-generate a strong code.`
  }
  return null
}
