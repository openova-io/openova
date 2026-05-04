/**
 * deployment.test.ts — exhaustive validation of the branded
 * `DeploymentID` parser. Every shape that has previously caused the
 * 15-char-truncation bug (or its mirror image — 17-char overflow,
 * uppercase, non-string) MUST throw here.
 *
 * Issues #749 + #754 — kill the truncation forever via compile-time
 * branding + runtime validation. If a future contributor weakens the
 * parser, this suite fails and CI blocks the regression.
 */

import { describe, it, expect } from 'vitest'
import { parseDeploymentID, isDeploymentID, type DeploymentID } from './deployment'

describe('parseDeploymentID — round-trip on the 16-char hex shape', () => {
  it('accepts the canonical 16-char hex id from the bug report', () => {
    const id = parseDeploymentID('eeb34ecd1414a505')
    expect(id).toBe('eeb34ecd1414a505')
  })

  it('accepts an all-zero hex id', () => {
    expect(parseDeploymentID('0000000000000000')).toBe('0000000000000000')
  })

  it('accepts an all-f hex id (max value)', () => {
    expect(parseDeploymentID('ffffffffffffffff')).toBe('ffffffffffffffff')
  })

  it('returns the same string identity (branded but byte-identical)', () => {
    const raw = 'abcdef0123456789'
    const parsed: DeploymentID = parseDeploymentID(raw)
    // Branded type — at runtime it's still the same string instance.
    expect(parsed).toBe(raw)
    expect(typeof parsed).toBe('string')
    expect(parsed.length).toBe(16)
  })
})

describe('parseDeploymentID — rejects truncation (15 chars)', () => {
  it('throws on the 15-char shape that surfaced in issue #749', () => {
    // The exact bug: founder saw the ID rendered as `eeb34ecd1414a50`
    // when the real id was `eeb34ecd1414a505` (16 chars).
    expect(() => parseDeploymentID('eeb34ecd1414a50')).toThrow(/invalid DeploymentID/)
  })

  it('throws on every other 15-char hex value', () => {
    expect(() => parseDeploymentID('000000000000000')).toThrow(/invalid DeploymentID/)
    expect(() => parseDeploymentID('fffffffffffffff')).toThrow(/invalid DeploymentID/)
  })
})

describe('parseDeploymentID — rejects overflow (17+ chars)', () => {
  it('throws on a 17-char hex string', () => {
    expect(() => parseDeploymentID('eeb34ecd1414a5050')).toThrow(/invalid DeploymentID/)
  })

  it('throws on a 32-char hex string (UUID-without-dashes shape)', () => {
    expect(() => parseDeploymentID('00112233445566778899aabbccddeeff')).toThrow(
      /invalid DeploymentID/,
    )
  })
})

describe('parseDeploymentID — rejects illegal characters', () => {
  it('throws on uppercase hex (server emits lowercase only)', () => {
    expect(() => parseDeploymentID('EEB34ECD1414A505')).toThrow(/invalid DeploymentID/)
  })

  it('throws on mixed case hex', () => {
    expect(() => parseDeploymentID('EeB34ecd1414a505')).toThrow(/invalid DeploymentID/)
  })

  it('throws on non-hex letters (g..z)', () => {
    expect(() => parseDeploymentID('zzzzzzzzzzzzzzzz')).toThrow(/invalid DeploymentID/)
    expect(() => parseDeploymentID('eeb34ecd1414a50g')).toThrow(/invalid DeploymentID/)
  })

  it('throws on punctuation / whitespace', () => {
    expect(() => parseDeploymentID('eeb34ecd-1414-a50')).toThrow(/invalid DeploymentID/)
    expect(() => parseDeploymentID('eeb34ecd1414a505 ')).toThrow(/invalid DeploymentID/)
    expect(() => parseDeploymentID(' eeb34ecd1414a505')).toThrow(/invalid DeploymentID/)
  })
})

describe('parseDeploymentID — rejects non-string types', () => {
  it('throws on null', () => {
    expect(() => parseDeploymentID(null)).toThrow(/invalid DeploymentID/)
  })

  it('throws on undefined', () => {
    expect(() => parseDeploymentID(undefined)).toThrow(/invalid DeploymentID/)
  })

  it('throws on a number', () => {
    expect(() => parseDeploymentID(0xeeb34ecd1414a505)).toThrow(/invalid DeploymentID/)
  })

  it('throws on a boolean', () => {
    expect(() => parseDeploymentID(true)).toThrow(/invalid DeploymentID/)
  })

  it('throws on a plain object', () => {
    expect(() => parseDeploymentID({ id: 'eeb34ecd1414a505' })).toThrow(/invalid DeploymentID/)
  })

  it('throws on an array', () => {
    expect(() => parseDeploymentID(['eeb34ecd1414a505'])).toThrow(/invalid DeploymentID/)
  })
})

describe('parseDeploymentID — error message hygiene', () => {
  it('truncates oversized string previews to 32 chars', () => {
    const huge = 'x'.repeat(10_000)
    let caught: Error | null = null
    try {
      parseDeploymentID(huge)
    } catch (err) {
      caught = err as Error
    }
    expect(caught).toBeTruthy()
    // Error message must NOT echo back the entire 10k string.
    expect(caught!.message.length).toBeLessThan(80)
  })

  it('encodes non-string types with their typeof tag, not their value', () => {
    let caught: Error | null = null
    try {
      parseDeploymentID(42)
    } catch (err) {
      caught = err as Error
    }
    expect(caught).toBeTruthy()
    expect(caught!.message).toMatch(/<number>/)
  })
})

describe('isDeploymentID — type-guard variant', () => {
  it('returns true for valid 16-char hex strings', () => {
    expect(isDeploymentID('eeb34ecd1414a505')).toBe(true)
  })

  it('returns false for 15-char strings', () => {
    expect(isDeploymentID('eeb34ecd1414a50')).toBe(false)
  })

  it('returns false for 17-char strings', () => {
    expect(isDeploymentID('eeb34ecd1414a5050')).toBe(false)
  })

  it('returns false for uppercase hex', () => {
    expect(isDeploymentID('EEB34ECD1414A505')).toBe(false)
  })

  it('returns false for non-string inputs', () => {
    expect(isDeploymentID(null)).toBe(false)
    expect(isDeploymentID(undefined)).toBe(false)
    expect(isDeploymentID(123)).toBe(false)
    expect(isDeploymentID({})).toBe(false)
  })
})
