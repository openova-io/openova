/**
 * Branded `DeploymentID` type — single source of truth for the
 * 16-char hex deployment id the catalyst-api mints in `newID()`
 * (see api/internal/handler/deployments.go: `make([]byte, 8)` →
 * `hex.EncodeToString` → exactly 16 lowercase hex chars).
 *
 * Why this exists: issues #749 / #754. The `deployment ID truncated
 * by one character` bug recurred multiple times because every code
 * path treated the id as a free-form `string`. Any new error template
 * / toast / URL builder could (and did) introduce a fresh truncation
 * without anyone noticing at compile time.
 *
 * The fix is a *branded type* — a string with a phantom `__brand`
 * tag that the TypeScript compiler refuses to materialise from a raw
 * `string` literal. The ONLY way to land a `DeploymentID` value is
 * via `parseDeploymentID()`, which validates the wire format. Any
 * future code that tries `const id: DeploymentID = someString` will
 * fail to compile until it's routed through the parser.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 (never compromise on quality):
 * the parser rejects 15-char inputs, 17-char inputs, uppercase, and
 * non-string types — every real-world way the truncation has shown
 * up. Per #4 (never hardcode), the regex is the single canonical
 * shape this codebase enforces; servers + clients both speak it.
 */

export type DeploymentID = string & { readonly __brand: 'DeploymentID' }

/**
 * The wire shape: 16 lowercase hexadecimal characters.
 *
 * Mirrors the server-side `newID()` in
 * api/internal/handler/deployments.go — `crypto/rand.Read(8 bytes)`
 * + `hex.EncodeToString` always yields a 16-char `[0-9a-f]` string.
 * If that contract ever changes server-side, this regex changes here
 * AND the parser's tests update in lockstep — failing fast at the
 * UI boundary is the desired behaviour.
 */
const DEPLOYMENT_ID_RE = /^[a-f0-9]{16}$/

/**
 * Parse + validate a value as a `DeploymentID`. Throws on any input
 * that isn't a 16-char lowercase hex string.
 *
 *   • 15 chars → throws (the original issue #749 bug shape)
 *   • 17 chars → throws (off-by-one in the other direction)
 *   • uppercase → throws (server emits lowercase only)
 *   • non-string types (null / undefined / number / object) → throws
 *
 * The error message intentionally truncates inputs to 32 chars so a
 * runaway buffer doesn't end up in a logged exception.
 */
export function parseDeploymentID(s: unknown): DeploymentID {
  if (typeof s !== 'string' || !DEPLOYMENT_ID_RE.test(s)) {
    const preview =
      typeof s === 'string' ? `"${s.slice(0, 32)}"` : `<${typeof s}>`
    throw new Error(`invalid DeploymentID: ${preview}`)
  }
  return s as DeploymentID
}

/**
 * Type-guard variant — returns true iff the value is a valid
 * `DeploymentID`. Use at boundaries where you want to BRANCH on
 * whether the input is a deployment id, not throw.
 */
export function isDeploymentID(s: unknown): s is DeploymentID {
  return typeof s === 'string' && DEPLOYMENT_ID_RE.test(s)
}
