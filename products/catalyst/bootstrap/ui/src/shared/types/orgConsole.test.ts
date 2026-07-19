import { describe, expect, it } from 'vitest'
import {
  isOrgConsoleKind,
  parseOrgConsoleID,
  parseOrgConsoleKind,
  type OrgConsoleID,
  type OrgConsoleKind,
} from './orgConsole'

describe('OrgConsoleID branded type', () => {
  it('accepts a non-empty string', () => {
    const t: OrgConsoleID = parseOrgConsoleID('orgc-acme')
    expect(t).toBe('orgc-acme')
  })

  it('rejects empty string', () => {
    expect(() => parseOrgConsoleID('')).toThrow(/invalid OrgConsoleID/)
  })

  it('rejects whitespace-only string', () => {
    expect(() => parseOrgConsoleID('   ')).toThrow(/invalid OrgConsoleID/)
  })

  it('rejects non-string types', () => {
    expect(() => parseOrgConsoleID(null)).toThrow(/invalid OrgConsoleID/)
    expect(() => parseOrgConsoleID(undefined)).toThrow(/invalid OrgConsoleID/)
    expect(() => parseOrgConsoleID(42)).toThrow(/invalid OrgConsoleID/)
    expect(() => parseOrgConsoleID({})).toThrow(/invalid OrgConsoleID/)
  })
})

describe('OrgConsoleKind', () => {
  it('parses both legal kinds', () => {
    const otech: OrgConsoleKind = parseOrgConsoleKind('otech')
    const org: OrgConsoleKind = parseOrgConsoleKind('org')
    expect(otech).toBe('otech')
    expect(org).toBe('org')
  })

  it('rejects anything else', () => {
    expect(() => parseOrgConsoleKind('admin')).toThrow(/invalid OrgConsoleKind/)
    expect(() => parseOrgConsoleKind(null)).toThrow(/invalid OrgConsoleKind/)
    expect(() => parseOrgConsoleKind('')).toThrow(/invalid OrgConsoleKind/)
  })

  it('isOrgConsoleKind narrows correctly', () => {
    expect(isOrgConsoleKind('otech')).toBe(true)
    expect(isOrgConsoleKind('org')).toBe(true)
    expect(isOrgConsoleKind('foo')).toBe(false)
    expect(isOrgConsoleKind(null)).toBe(false)
  })
})
